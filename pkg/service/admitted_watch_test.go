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

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// tenantRefusedError stands in for the middleware's typed refusal; the
// service package cannot import the middleware.
type tenantRefusedError struct{ tenantID string }

func (e *tenantRefusedError) Error() string { return "tenant " + e.tenantID + " is not active" }

// testStatusTTL stands in for middleware.TenantStatusTTL: how long an
// admission answered after a fresh status read holds.
const testStatusTTL = time.Second

// switchAdmitter admits until refuse is set, recording each identity and
// whether the context it was asked under was already canceled. Each
// admission holds for the next entry of holds, as an answer from a status
// cache entry holds only for what is left of it, and for testStatusTTL, an
// answer from a fresh read, once holds is exhausted.
type switchAdmitter struct {
	mu         sync.Mutex
	refuse     error
	holds      []time.Duration
	identities []*auth.Identity
	canceled   []bool
}

func (a *switchAdmitter) AdmitStream(ctx context.Context, identity *auth.Identity) (time.Duration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identities = append(a.identities, identity)
	a.canceled = append(a.canceled, ctx.Err() != nil)
	if a.refuse != nil {
		return 0, a.refuse
	}
	if len(a.holds) == 0 {
		return testStatusTTL, nil
	}
	holds := a.holds[0]
	a.holds = a.holds[1:]
	return holds, nil
}

// holdFor makes the next admissions hold for d each, in order.
func (a *switchAdmitter) holdFor(d ...time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.holds = append(a.holds, d...)
}

func (a *switchAdmitter) set(err error) {
	a.mu.Lock()
	a.refuse = err
	a.mu.Unlock()
}

func (a *switchAdmitter) seen() ([]*auth.Identity, []bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*auth.Identity(nil), a.identities...), append([]bool(nil), a.canceled...)
}

// admitTestWatcher is a watcher a test pushes responses onto; Close is
// safe from the forwarding goroutine and the test at once.
type admitTestWatcher struct {
	events chan watch.WatchResponse
	closed chan struct{}
	once   sync.Once
	err    error
}

func newAdmitTestWatcher() *admitTestWatcher {
	return &admitTestWatcher{events: make(chan watch.WatchResponse, 4), closed: make(chan struct{})}
}

func (w *admitTestWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *admitTestWatcher) WatchID() int64                     { return 7 }
func (w *admitTestWatcher) Err() error                         { return w.err }
func (w *admitTestWatcher) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// admitTestService returns the configured watcher and entity channel.
type admitTestService struct {
	WatchServicer
	watcher *admitTestWatcher
	entity  chan WatchEntityEvent
	openErr error
}

func (s *admitTestService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.watcher, nil
}

func (s *admitTestService) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.watcher, nil
}

func (s *admitTestService) WatchEntity(context.Context, *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.entity, nil
}

func (s *admitTestService) WatchEntityType(context.Context, *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.entity, nil
}

const admitTestEntity EntityType = "user"

func acmeIdentityCtx() (context.Context, *auth.Identity) {
	id := &auth.Identity{Subject: "bob", TenantID: "acme"}
	return auth.WithIdentity(context.Background(), id), id
}

func TestNewAdmittedWatch_NilArgumentsReturnServiceUnchanged(t *testing.T) {
	svc := &admitTestService{}
	if got := NewAdmittedWatch(svc, nil); got != WatchServicer(svc) {
		t.Fatalf("NewAdmittedWatch(svc, nil) = %T, want svc unchanged", got)
	}
	if got := NewAdmittedWatch(nil, &switchAdmitter{}); got != nil {
		t.Fatalf("NewAdmittedWatch(nil, admitter) = %T, want nil", got)
	}
}

// A watch opened while its tenant was admitted ends on its next event once
// the admitter refuses, with the admitter's typed error from Err, and the
// inner watcher is closed.
func TestAdmittedWatch_RefusalEndsStreamOnNextEvent(t *testing.T) {
	for _, open := range []struct {
		name string
		fn   func(WatchServicer, context.Context) (watch.Watcher, error)
	}{
		{"Watch", func(s WatchServicer, ctx context.Context) (watch.Watcher, error) { return s.Watch(ctx, "k") }},
		{"WatchPrefix", func(s WatchServicer, ctx context.Context) (watch.Watcher, error) { return s.WatchPrefix(ctx, "p/") }},
	} {
		t.Run(open.name, func(t *testing.T) {
			inner := newAdmitTestWatcher()
			admitter := &switchAdmitter{}
			ctx, id := acmeIdentityCtx()
			w, err := open.fn(NewAdmittedWatch(&admitTestService{watcher: inner}, admitter), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer w.Close()

			inner.events <- watch.WatchResponse{Revision: 1}
			if resp, ok := <-w.Events(); !ok || resp.Revision != 1 {
				t.Fatalf("first event = %+v, %v; want revision 1 delivered", resp, ok)
			}

			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			inner.events <- watch.WatchResponse{Revision: 2}
			if resp, ok := <-w.Events(); ok {
				t.Fatalf("event after refusal delivered: %+v", resp)
			}
			var got *tenantRefusedError
			if !errors.As(w.Err(), &got) || got != refusal {
				t.Fatalf("Err() = %v, want the admitter's refusal", w.Err())
			}
			select {
			case <-inner.closed:
			default:
				t.Fatal("inner watcher not closed after refusal")
			}
			ids, _ := admitter.seen()
			if len(ids) != 2 || ids[0] != id || ids[1] != id {
				t.Fatalf("admitted identities = %v, want the opening identity twice", ids)
			}
		})
	}
}

// The check runs under a context detached from the stream's cancellation:
// its answer is about the tenant, not about a client that hung up.
func TestAdmittedWatch_AdmitsUnderDetachedContext(t *testing.T) {
	inner := newAdmitTestWatcher()
	admitter := &switchAdmitter{}
	idCtx, id := acmeIdentityCtx()
	ctx, cancel := context.WithCancel(idCtx)
	w, err := NewAdmittedWatch(&admitTestService{watcher: inner}, admitter).Watch(ctx, "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Close()
	cancel()

	inner.events <- watch.WatchResponse{}
	if _, ok := <-w.Events(); !ok {
		t.Fatal("event not delivered")
	}
	ids, canceled := admitter.seen()
	if len(ids) != 1 || ids[0] != id || canceled[0] {
		t.Fatalf("admit saw identities %v canceled %v, want the identity under a live context", ids, canceled)
	}
}

// A context without an identity is handed to the admitter as nil; the
// admitter, not the wrapper, decides (the middleware refuses it).
func TestAdmittedWatch_NoIdentityIsAdmitterDecision(t *testing.T) {
	inner := newAdmitTestWatcher()
	admitter := &switchAdmitter{refuse: auth.ErrUnauthenticated}
	w, err := NewAdmittedWatch(&admitTestService{watcher: inner}, admitter).WatchPrefix(context.Background(), "p/")
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	defer w.Close()
	inner.events <- watch.WatchResponse{}
	if _, ok := <-w.Events(); ok {
		t.Fatal("event delivered without an identity")
	}
	if !errors.Is(w.Err(), auth.ErrUnauthenticated) {
		t.Fatalf("Err() = %v, want ErrUnauthenticated", w.Err())
	}
	if ids, _ := admitter.seen(); len(ids) != 1 || ids[0] != nil {
		t.Fatalf("admitted identities = %v, want one nil", ids)
	}
}

// Without a refusal the wrapper is transparent: the inner watcher's ID,
// its own end and its own error pass through, and Close is idempotent.
func TestAdmittedWatch_TransparentWithoutRefusal(t *testing.T) {
	inner := newAdmitTestWatcher()
	inner.err = errors.New("compacted")
	ctx, _ := acmeIdentityCtx()
	w, err := NewAdmittedWatch(&admitTestService{watcher: inner}, &switchAdmitter{}).Watch(ctx, "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if w.WatchID() != 7 {
		t.Fatalf("WatchID() = %d, want 7", w.WatchID())
	}
	close(inner.events)
	if _, ok := <-w.Events(); ok {
		t.Fatal("Events still open after inner closed")
	}
	if w.Err() != inner.err {
		t.Fatalf("Err() = %v, want the inner watcher's error", w.Err())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Close ends the forwarder even while an event is pending delivery.
func TestAdmittedWatch_CloseReleasesPendingDelivery(t *testing.T) {
	inner := newAdmitTestWatcher()
	ctx, _ := acmeIdentityCtx()
	w, err := NewAdmittedWatch(&admitTestService{watcher: inner}, &switchAdmitter{}).Watch(ctx, "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	inner.events <- watch.WatchResponse{}
	_ = w.Close()
	for range w.Events() {
	}
	if w.Err() != nil {
		t.Fatalf("Err() after Close = %v, want nil", w.Err())
	}
}

func TestAdmittedWatch_OpenErrorsPropagate(t *testing.T) {
	openErr := errors.New("no such shard")
	svc := NewAdmittedWatch(&admitTestService{openErr: openErr}, &switchAdmitter{})
	ctx, _ := acmeIdentityCtx()
	if _, err := svc.Watch(ctx, "k"); err != openErr {
		t.Fatalf("Watch err = %v", err)
	}
	if _, err := svc.WatchPrefix(ctx, "p/"); err != openErr {
		t.Fatalf("WatchPrefix err = %v", err)
	}
	if _, err := svc.WatchEntity(ctx, &WatchEntityRequest{EntityType: admitTestEntity}); err != openErr {
		t.Fatalf("WatchEntity err = %v", err)
	}
	if _, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: admitTestEntity}); err != openErr {
		t.Fatalf("WatchEntityType err = %v", err)
	}
}

// entityWatchOpeners opens each kind of entity watch on admitTestEntity.
var entityWatchOpeners = []struct {
	name string
	fn   func(WatchServicer, context.Context) (<-chan WatchEntityEvent, error)
}{
	{"WatchEntity", func(s WatchServicer, ctx context.Context) (<-chan WatchEntityEvent, error) {
		return s.WatchEntity(ctx, &WatchEntityRequest{EntityType: admitTestEntity, ID: "u1"})
	}},
	{"WatchEntityType", func(s WatchServicer, ctx context.Context) (<-chan WatchEntityEvent, error) {
		return s.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: admitTestEntity})
	}},
}

// An entity watch delivers admitted events, then ends a refused one with a
// single terminal Canceled event carrying the refusal, like any reasoned
// end.
func TestAdmittedWatch_EntityRefusalDeliversTerminalEvent(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			in := make(chan WatchEntityEvent, 2)
			admitter := &switchAdmitter{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = auth.WithIdentity(ctx, &auth.Identity{Subject: "bob", TenantID: "acme"})
			out, err := open.fn(NewAdmittedWatch(&admitTestService{entity: in}, admitter), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u1"}
			if ev := <-out; ev.ID != "u1" || ev.Canceled {
				t.Fatalf("first event = %+v, want u1 delivered", ev)
			}

			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u2"}
			ev, ok := <-out
			if !ok || !ev.Canceled || ev.CancelReason != refusal.Error() || ev.EntityType != admitTestEntity || ev.ID != "" {
				t.Fatalf("event after refusal = %+v (open %v), want one terminal Canceled event", ev, ok)
			}
			if ev, ok := <-out; ok {
				t.Fatalf("event after terminal: %+v", ev)
			}
		})
	}
}

// releaseWatcher is a hub watcher that reports its Close: the hub's
// subscription for it is released.
type releaseWatcher struct {
	*mockWatcher
	once   sync.Once
	closed chan struct{}
}

func (w *releaseWatcher) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// A refused entity watch releases its underlying subscription when the relay
// ends, while the stream's context is still live: the hub watcher behind the
// real WatchService is closed, for both entity-watch methods.
func TestAdmittedWatch_EntityRefusalReleasesTheSubscription(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			sub := &releaseWatcher{mockWatcher: &mockWatcher{eventsCh: make(chan watch.WatchResponse, 1)}, closed: make(chan struct{})}
			subscribe := func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) { return sub, nil }
			hub := &mockWatchHub{watchFn: subscribe, watchPrefixFn: subscribe}
			ctx, _ := acmeIdentityCtx()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			refusal := &tenantRefusedError{tenantID: "acme"}
			out, err := open.fn(NewAdmittedWatch(NewWatchService(hub), &switchAdmitter{refuse: refusal}), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			sub.eventsCh <- watch.WatchResponse{Revision: 1, Events: []*watch.Event{{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte(string(admitTestEntity) + "/u1"), Value: []byte(`{}`)},
			}}}
			if ev, ok := <-out; !ok || !ev.Canceled || ev.CancelReason != refusal.Error() {
				t.Fatalf("event after refusal = %+v (open %v), want one terminal Canceled event", ev, ok)
			}
			if ev, ok := <-out; ok {
				t.Fatalf("event after terminal: %+v", ev)
			}
			select {
			case <-sub.closed:
			case <-time.After(10 * time.Second):
				t.Fatal("the refused watch's hub subscription was never released")
			}
			if ctx.Err() != nil {
				t.Fatal("the stream's context ended; the release must not depend on it")
			}
		})
	}
}

// An entity watch closes when its source closes or its stream ends.
func TestAdmittedWatch_EntityEndsWithSourceOrContext(t *testing.T) {
	ctx, _ := acmeIdentityCtx()
	in := make(chan WatchEntityEvent)
	out, err := NewAdmittedWatch(&admitTestService{entity: in}, &switchAdmitter{}).WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: admitTestEntity})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}
	close(in)
	if ev, ok := <-out; ok {
		t.Fatalf("event after source closed: %+v", ev)
	}

	cctx, cancel := context.WithCancel(ctx)
	in = make(chan WatchEntityEvent)
	out, err = NewAdmittedWatch(&admitTestService{entity: in}, &switchAdmitter{}).WatchEntity(cctx, &WatchEntityRequest{EntityType: admitTestEntity, ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}
	cancel()
	if ev, ok := <-out; ok {
		t.Fatalf("event after context canceled: %+v", ev)
	}
}
