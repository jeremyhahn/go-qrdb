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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errStreamGateClosed is the refusal the test gate answers once it closes.
var errStreamGateClosed = errors.New("stream gate closed")

// testStreamGate is the gate a seal closes before it ends streams, as the
// monitor does: the published record (or an expired lease) first, End* after.
type testStreamGate struct{ closed atomic.Bool }

func (g *testStreamGate) refuses() error {
	if g.closed.Load() {
		return errStreamGateClosed
	}
	return nil
}

// sealNow lands a seal exactly as the monitor does: close the gate, then end
// every stream.
func (g *testStreamGate) sealNow(streams *clusterseal.Streams, cause error) {
	g.closed.Store(true)
	streams.EndAll(cause)
}

// openSealedStream opens one stream kind through svc and returns a channel
// that closes when the stream ends.
type openSealedStream func(*sealedWatchService) (<-chan struct{}, error)

func sealedStreamKinds() map[string]openSealedStream {
	return map[string]openSealedStream{
		"Watch": func(s *sealedWatchService) (<-chan struct{}, error) {
			w, err := s.Watch(context.Background(), "k")
			if err != nil {
				return nil, err
			}
			done := make(chan struct{})
			go func() {
				for range w.Events() {
				}
				close(done)
			}()
			return done, nil
		},
		"WatchPrefix": func(s *sealedWatchService) (<-chan struct{}, error) {
			w, err := s.WatchPrefix(context.Background(), "k")
			if err != nil {
				return nil, err
			}
			done := make(chan struct{})
			go func() {
				for range w.Events() {
				}
				close(done)
			}()
			return done, nil
		},
		"WatchEntity": func(s *sealedWatchService) (<-chan struct{}, error) {
			ch, err := s.WatchEntity(context.Background(), &service.WatchEntityRequest{})
			if err != nil {
				return nil, err
			}
			done := make(chan struct{})
			go func() {
				for range ch {
				}
				close(done)
			}()
			return done, nil
		},
		"WatchEntityType": func(s *sealedWatchService) (<-chan struct{}, error) {
			ch, err := s.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{})
			if err != nil {
				return nil, err
			}
			done := make(chan struct{})
			go func() {
				for range ch {
				}
				close(done)
			}()
			return done, nil
		},
	}
}

// TestSealedWatchService_SealLandingMidOpenEndsTheStream: a seal that lands
// while a stream is opening -- after the request passed the gate, before the
// stream is registered -- must still end it. The seal is landed from inside
// the inner open, the narrowest point of that window.
func TestSealedWatchService_SealLandingMidOpenEndsTheStream(t *testing.T) {
	for name, open := range sealedStreamKinds() {
		t.Run(name, func(t *testing.T) {
			streams := clusterseal.NewStreams()
			gate := &testStreamGate{}
			cause := &clusterseal.SealedError{Barrier: clusterseal.TenantBarrier("acme")}
			inner := &queuedWatchService{onOpen: func() { gate.sealNow(streams, cause) }}
			done, err := open(newSealedWatchService(inner, streams, "acme", "orders", gate.refuses))
			if err != nil {
				// Refusing the open outright is as good as ending it.
				return
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("a stream opened as the seal landed outlived the seal")
			}
			if streams.Len() != 0 {
				t.Fatalf("subscriptions = %d after the seal", streams.Len())
			}
		})
	}
}

// TestSealedWatchService_SealLandedBeforeTheSubscriptionRefusesTheOpen: a
// seal that has already closed the gate and ended every stream by the time
// the stream subscribes -- it passed resolution just before -- must not open:
// nothing would ever end it.
func TestSealedWatchService_SealLandedBeforeTheSubscriptionRefusesTheOpen(t *testing.T) {
	for name, open := range sealedStreamKinds() {
		t.Run(name, func(t *testing.T) {
			streams := clusterseal.NewStreams()
			gate := &testStreamGate{}
			gate.sealNow(streams, &clusterseal.SealStateUnconfirmedError{})
			inner := &queuedWatchService{}
			_, err := open(newSealedWatchService(inner, streams, "acme", "orders", gate.refuses))
			if !errors.Is(err, errStreamGateClosed) {
				t.Fatalf("open after the seal: err = %v, want the gate's refusal", err)
			}
			if streams.Len() != 0 {
				t.Fatalf("subscriptions = %d after a refused open", streams.Len())
			}
		})
	}
}
