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

package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// chanWatcher is a watch.Watcher over a channel the test feeds.
type chanWatcher struct {
	events    chan watch.WatchResponse
	closeOnce sync.Once
}

func newChanWatcher() *chanWatcher { return &chanWatcher{events: make(chan watch.WatchResponse, 8)} }

func (w *chanWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *chanWatcher) WatchID() int64                     { return 1 }
func (w *chanWatcher) Err() error                         { return nil }
func (w *chanWatcher) Close() error                       { return nil }

// end closes the channel, as a hub that dropped the watcher does.
func (w *chanWatcher) end() { w.closeOnce.Do(func() { close(w.events) }) }

var _ watch.Watcher = (*chanWatcher)(nil)

// queueWatchService hands out its watchers in order, recording each prefix
// and how many it was asked for.
type queueWatchService struct {
	mu       sync.Mutex
	watchers []*chanWatcher
	errs     []error
	prefixes []string
}

func (s *queueWatchService) WatchPrefix(_ context.Context, prefix string, _ ...watch.WatchOption) (watch.Watcher, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefixes = append(s.prefixes, prefix)
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	w := s.watchers[0]
	s.watchers = s.watchers[1:]
	return w, nil
}

// recordingReconciler records the ids handed to ReconcileUndone and the full
// passes run.
type recordingReconciler struct {
	ids   chan string
	full  chan struct{}
	undoE error
	fullE error
}

func newRecordingReconciler() *recordingReconciler {
	return &recordingReconciler{ids: make(chan string, 8), full: make(chan struct{}, 8)}
}

func (r *recordingReconciler) ReconcileUndone(_ context.Context, id string) error {
	r.ids <- id
	return r.undoE
}

func (r *recordingReconciler) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	r.full <- struct{}{}
	return &tenant.ReconcileReport{}, r.fullE
}

func putEvent(key string) watch.WatchResponse {
	return watch.WatchResponse{Events: []*watch.Event{{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(key)}}}}
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// Every tombstone write is handed to ReconcileUndone by its id; anything
// outside the prefix, a delete, or a bare prefix is not.
func TestWatchUndoneTenants_ReconcilesEachTombstoneWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := newChanWatcher()
	ws := &queueWatchService{watchers: []*chanWatcher{w}}
	r := newRecordingReconciler()
	r.undoE = errors.New("not yet")

	if err := watchUndoneTenants(ctx, &nodeWork{}, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	if len(ws.prefixes) != 1 || ws.prefixes[0] != tenant.TombstonePrefix {
		t.Fatalf("watched %v, want [%s]", ws.prefixes, tenant.TombstonePrefix)
	}
	w.events <- watch.WatchResponse{Events: []*watch.Event{
		nil,
		{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte(tenant.TombstonePrefix + "skipped")}},
		{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("elsewhere/acme")}},
		{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(tenant.TombstonePrefix)}},
		{Type: watch.EventPut},
	}}
	// A failed reconcile does not stop the watch.
	w.events <- putEvent(tenant.TombstonePrefix + "acme")
	w.events <- putEvent(tenant.TombstonePrefix + "globex")
	if id := receive(t, r.ids, "acme"); id != "acme" {
		t.Fatalf("reconciled %q first, want acme", id)
	}
	if id := receive(t, r.ids, "globex"); id != "globex" {
		t.Fatalf("reconciled %q second, want globex", id)
	}
}

// A watch the hub closes is opened again, after a failed attempt, and a full
// reconcile covers whatever the gap hid.
func TestWatchUndoneTenants_ReestablishesAndCatchesUp(t *testing.T) {
	restore := undoneWatchBackoff
	undoneWatchBackoff = time.Millisecond
	t.Cleanup(func() { undoneWatchBackoff = restore })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first, second := newChanWatcher(), newChanWatcher()
	ws := &queueWatchService{watchers: []*chanWatcher{first, second}, errs: []error{nil, errors.New("hub not ready")}}
	r := newRecordingReconciler()
	if err := watchUndoneTenants(ctx, &nodeWork{}, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	receive(t, r.full, "the full reconcile once the first watch is registered")
	first.end()
	receive(t, r.full, "the full reconcile after the gap")
	second.events <- putEvent(tenant.TombstonePrefix + "acme")
	if id := receive(t, r.ids, "acme on the new watch"); id != "acme" {
		t.Fatalf("reconciled %q, want acme", id)
	}
}

// A tombstone written before the watch was registered is delivered by no
// event; a full reconcile runs once it is, before any event is waited on.
func TestWatchUndoneTenants_ReconcilesOnceTheWatchIsRegistered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws := &queueWatchService{watchers: []*chanWatcher{newChanWatcher()}}
	r := newRecordingReconciler()
	if err := watchUndoneTenants(ctx, &nodeWork{}, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	receive(t, r.full, "the full reconcile once the watch is registered")
}

// A failed reconcile is logged, and the watch it precedes still runs.
func TestWatchUndoneTenants_AFailedReconcileStillWatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := newChanWatcher()
	ws := &queueWatchService{watchers: []*chanWatcher{w}}
	r := newRecordingReconciler()
	r.fullE = errors.New("sealed")
	if err := watchUndoneTenants(ctx, &nodeWork{}, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	receive(t, r.full, "the failing full reconcile")
	w.events <- putEvent(tenant.TombstonePrefix + "acme")
	if id := receive(t, r.ids, "acme"); id != "acme" {
		t.Fatalf("reconciled %q, want acme", id)
	}
}

func TestWatchUndoneTenants_OpenFailureIsTyped(t *testing.T) {
	boom := errors.New("no hub")
	ws := &queueWatchService{errs: []error{boom}}
	err := watchUndoneTenants(context.Background(), &nodeWork{}, ws, newRecordingReconciler(), slog.New(slog.DiscardHandler))
	var typed *UndoneTenantWatchError
	if !errors.As(err, &typed) || !errors.Is(err, boom) {
		t.Fatalf("watchUndoneTenants = %v, want *UndoneTenantWatchError wrapping %v", err, boom)
	}
	if typed.Error() == "" {
		t.Fatal("UndoneTenantWatchError has no message")
	}
}

func TestStartUndoneWatch_NothingToWatch(t *testing.T) {
	for name, n := range map[string]*Node{"nil node": nil, "no watch service": {}} {
		t.Run(name, func(t *testing.T) {
			if err := startUndoneWatch(context.Background(), n); err != nil {
				t.Fatalf("startUndoneWatch = %v, want nil", err)
			}
		})
	}
}

// A cancelled context ends the re-establishing loop.
func TestReestablishUndoneWatch_StopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := reestablishUndoneWatch(ctx, &queueWatchService{}, slog.New(slog.DiscardHandler)); ok {
		t.Fatal("reestablishUndoneWatch on a cancelled context reported a watch")
	}
}

// blockingReconciler's full pass reads until its context ends, as a reconcile
// scanning the tenant keyspace holds a storage iterator for as long as it runs.
type blockingReconciler struct {
	entered chan struct{}
	left    chan struct{}
}

func newBlockingReconciler() *blockingReconciler {
	return &blockingReconciler{entered: make(chan struct{}), left: make(chan struct{})}
}

func (r *blockingReconciler) ReconcileUndone(context.Context, string) error { return nil }

func (r *blockingReconciler) ReconcileDeletedTenants(ctx context.Context) (*tenant.ReconcileReport, error) {
	close(r.entered)
	defer close(r.left)
	<-ctx.Done()
	return nil, ctx.Err()
}

var _ undoneReconciler = (*blockingReconciler)(nil)

// Stop returns only once the undone watch's reconcile has: the composer closes
// the storage the reconcile reads through right after Stop, and the context
// the watch was started with outlives Stop (the node's, across a data-plane
// rebuild), so nothing else ends it in time.
func TestNodeStop_WaitsForTheUndoneWatchReconcile(t *testing.T) {
	n := &Node{}
	r := newBlockingReconciler()
	ws := &queueWatchService{watchers: []*chanWatcher{newChanWatcher()}}
	if err := watchUndoneTenants(context.Background(), &n.work, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	receive(t, r.entered, "the full reconcile once the watch is registered")

	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-r.left:
	default:
		t.Fatal("Stop returned while the undone watch's reconcile was still reading")
	}
}

// closeRecordingWatcher is a chanWatcher that reports its Close.
type closeRecordingWatcher struct {
	*chanWatcher
	closed chan struct{}
}

func (w *closeRecordingWatcher) Close() error {
	close(w.closed)
	return nil
}

var _ watch.Watcher = (*closeRecordingWatcher)(nil)

// A watch opened after the node stopped is closed again and supervised by
// nothing, so no reconcile can start against storage being closed.
func TestWatchUndoneTenants_StartsNothingOnceTheNodeStopped(t *testing.T) {
	work := &nodeWork{}
	work.stop()
	w := &closeRecordingWatcher{chanWatcher: newChanWatcher(), closed: make(chan struct{})}
	ws := &recordedWatchService{w: w}
	r := newRecordingReconciler()
	if err := watchUndoneTenants(context.Background(), work, ws, r, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("watchUndoneTenants: %v", err)
	}
	select {
	case <-w.closed:
	default:
		t.Fatal("the watch opened after the node stopped was left open")
	}
	if len(r.full) != 0 {
		t.Fatal("a reconcile ran after the node stopped")
	}
}

// recordedWatchService hands out one watcher.
type recordedWatchService struct{ w watch.Watcher }

func (s *recordedWatchService) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.w, nil
}

var _ prefixWatcher = (*recordedWatchService)(nil)
