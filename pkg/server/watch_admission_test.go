// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// refusingAdmitter refuses every admission with err.
type refusingAdmitter struct{ err error }

func (a refusingAdmitter) AdmitStream(context.Context, *auth.Identity) (time.Duration, error) {
	return 0, a.err
}

// registryPushWatcher is a watcher the test pushes responses onto.
type registryPushWatcher struct{ events chan watch.WatchResponse }

func (w *registryPushWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *registryPushWatcher) WatchID() int64                     { return 1 }
func (w *registryPushWatcher) Close() error                       { return nil }
func (w *registryPushWatcher) Err() error                         { return nil }

type registryPushWatchService struct {
	service.WatchServicer
	w *registryPushWatcher
}

func (s *registryPushWatchService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.w, nil
}

// openRegistryWatch resolves the default database's watch service through
// a registry built with opts and opens a watch on it.
func openRegistryWatch(t *testing.T, opts ...RESTRegistryOption) (*registryPushWatcher, watch.Watcher) {
	t.Helper()
	src := &registryPushWatcher{events: make(chan watch.WatchResponse, 1)}
	svcs := newFilledServices("default")
	svcs.Watch = &registryPushWatchService{w: src}
	dbs := NewDatabaseServiceRegistry("default")
	dbs.Register("default", svcs)
	svc, err := NewRESTServiceRegistry(dbs, nil, "system", opts...).WatchService("system", "default")
	if err != nil {
		t.Fatalf("WatchService: %v", err)
	}
	w, err := svc.Watch(context.Background(), "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return src, w
}

// Every watch the registry resolves -- the one path the REST, gRPC, QUIC,
// unix and MCP transports take -- is admitted per event by the installed
// admitter.
func TestRESTServiceRegistry_WatchService_AdmitsEachEvent(t *testing.T) {
	refusal := errors.New("tenant suspended")
	src, w := openRegistryWatch(t, withStreamAdmitter(refusingAdmitter{err: refusal}))
	src.events <- watch.WatchResponse{Revision: 1}
	if resp, ok := <-w.Events(); ok {
		t.Fatalf("refused event delivered: %+v", resp)
	}
	if !errors.Is(w.Err(), refusal) {
		t.Fatalf("Err() = %v, want the admitter's refusal", w.Err())
	}
}

// Without an admitter (a server with no auth middleware) the registry's
// watch is not wrapped.
func TestRESTServiceRegistry_WatchService_NoAdmitterDelivers(t *testing.T) {
	src, w := openRegistryWatch(t)
	src.events <- watch.WatchResponse{Revision: 1}
	if resp, ok := <-w.Events(); !ok || resp.Revision != 1 {
		t.Fatalf("event = %+v, %v; want revision 1 delivered", resp, ok)
	}
}

// signalAdmitter admits every identity and reports each admission.
type signalAdmitter chan struct{}

func (a signalAdmitter) AdmitStream(context.Context, *auth.Identity) (time.Duration, error) {
	select {
	case a <- struct{}{}:
	default:
	}
	return time.Second, nil
}

// closeCountingWatcher is a registryPushWatcher that reports each Close.
type closeCountingWatcher struct {
	registryPushWatcher
	closes chan struct{}
}

func (w *closeCountingWatcher) Close() error {
	select {
	case w.closes <- struct{}{}:
	default:
	}
	return nil
}

type closeCountingWatchService struct {
	service.WatchServicer
	w *closeCountingWatcher
}

func (s *closeCountingWatchService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.w, nil
}

// TestRESTServiceRegistry_WatchService_SealEndsABlockedDelivery: the
// admission wrapper sits outside the seal guard, so a response admitted
// before its consumer stopped reading is the admission relay's to hold. A
// seal landing meanwhile ends the stream; the consumer, reading afterwards,
// gets the seal's error and not the response (#809).
func TestRESTServiceRegistry_WatchService_SealEndsABlockedDelivery(t *testing.T) {
	src := &closeCountingWatcher{
		registryPushWatcher: registryPushWatcher{events: make(chan watch.WatchResponse, 1)},
		closes:              make(chan struct{}, 4),
	}
	svcs := newFilledServices("default")
	svcs.Watch = &closeCountingWatchService{w: src}
	dbs := NewDatabaseServiceRegistry("default")
	dbs.Register("default", svcs)
	cs := newTestClusterSeal()
	admitted := make(signalAdmitter, 16)
	svc, err := NewRESTServiceRegistry(dbs, nil, "system", withClusterSealRegistry(cs), withStreamAdmitter(admitted)).WatchService("system", "default")
	if err != nil {
		t.Fatalf("WatchService: %v", err)
	}
	w, err := svc.Watch(context.Background(), "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	src.events <- watch.WatchResponse{Revision: 1}
	select {
	case <-admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the response was never admitted")
	}
	cs.streams.EndAll(&clusterseal.SealStateUnconfirmedError{})
	// The seal guard closes src as it ends, and the admission relay closes
	// it again as it ends in turn.
	for range 2 {
		select {
		case <-src.closes:
		case <-time.After(10 * time.Second):
			t.Fatal("the seal did not end the blocked stream")
		}
	}
	if resp, ok := <-w.Events(); ok {
		t.Fatalf("response delivered after the seal: %+v", resp)
	}
	var unconfirmed *clusterseal.SealStateUnconfirmedError
	if !errors.As(w.Err(), &unconfirmed) {
		t.Fatalf("Err() = %v, want the seal's error", w.Err())
	}
}
