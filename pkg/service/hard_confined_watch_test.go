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

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// fakeWatcher is a minimal watch.Watcher double that lets a test push
// events directly onto its channel.
type fakeWatcher struct {
	events    chan watch.WatchResponse
	closeOnce sync.Once
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{events: make(chan watch.WatchResponse, 8)}
}

func (w *fakeWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *fakeWatcher) WatchID() int64                     { return 1 }
func (w *fakeWatcher) Close() error {
	w.closeOnce.Do(func() { close(w.events) })
	return nil
}
func (w *fakeWatcher) Err() error { return nil }

// fakeWatch is a minimal WatchServicer double recording whether Watch /
// WatchPrefix was reached, so a refusal can be proven to have
// short-circuited BEFORE a watcher was ever created, and returning a
// fakeWatcher a test can push synthetic events onto for WatchPrefix.
type fakeWatch struct {
	WatchServicer
	calledWatch       bool
	calledWatchPrefix bool
	prefixWatcher     *fakeWatcher

	// entityTypeChan, when non-nil, is returned by WatchEntityType instead
	// of the default already-closed empty channel, so a test can push
	// synthetic WatchEntityEvents onto it.
	entityTypeChan chan WatchEntityEvent
}

func (f *fakeWatch) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	f.calledWatch = true
	return nil, nil
}

func (f *fakeWatch) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	f.calledWatchPrefix = true
	f.prefixWatcher = newFakeWatcher()
	return f.prefixWatcher, nil
}

func (f *fakeWatch) WatchEntity(_ context.Context, _ *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	f.calledWatch = true
	ch := make(chan WatchEntityEvent)
	close(ch)
	return ch, nil
}

func (f *fakeWatch) WatchEntityType(_ context.Context, _ *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	f.calledWatchPrefix = true
	if f.entityTypeChan != nil {
		return f.entityTypeChan, nil
	}
	ch := make(chan WatchEntityEvent)
	close(ch)
	return ch, nil
}

func TestNewHardConfinedWatch_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedWatch(nil); got != nil {
		t.Fatalf("NewHardConfinedWatch(nil) = %v, want nil", got)
	}
}

func TestHardConfinedWatch_RefusesTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.Watch(ctx, confinedKey); err == nil {
		t.Fatal("Watch(confinedKey) succeeded, want a refusal")
	} else {
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}
	if fake.calledWatch {
		t.Fatal("Watch delegated to the underlying servicer despite the refusal; a watcher must never be created")
	}
}

func TestHardConfinedWatch_OrdinaryKeyPassesThrough(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.Watch(ctx, "ordinary/key"); err != nil {
		t.Fatalf("Watch(ordinary key): %v", err)
	}
	if !fake.calledWatch {
		t.Fatal("Watch did not reach the underlying servicer for an ordinary key")
	}
}

// TestHardConfinedWatch_WatchPrefixFiltersConfinedEventsFromAWideOpenSweep
// is B2's Watch regression proof: a prefix watch spanning the whole
// keyspace ("" or "__sys:", the reviewer's own probe shape) must not be
// refused outright -- that would break a legitimate broad watch, the same
// false positive go-qrdb#555's registry-level attempt made for Range --
// and must never deliver an event naming the confined key, from any
// revision.
func TestHardConfinedWatch_WatchPrefixFiltersConfinedEventsFromAWideOpenSweep(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, "")
	if err != nil {
		t.Fatalf("WatchPrefix(\"\"): %v", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("WatchPrefix(\"\") did not reach the underlying servicer; a wide-open watch must be filtered, not refused")
	}

	fake.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(confinedKey), Value: []byte("the cluster CA's private key")}},
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("ordinary/a"), Value: []byte("1")}},
		},
	}
	fake.prefixWatcher.Close()

	select {
	case resp, ok := <-watcher.Events():
		if !ok {
			t.Fatal("Events() closed before delivering the one response pushed")
		}
		if len(resp.Events) != 1 || string(resp.Events[0].Kv.Key) != "ordinary/a" {
			t.Fatalf("WatchPrefix delivered %v, want exactly the non-confined event", resp.Events)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the filtered watch event")
	}

	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestHardConfinedWatch_WatchPrefixFiltersADeleteEventsPrevKv proves a
// delete event -- which carries its key only in PrevKv, Kv being the
// post-event (absent) state -- is also filtered.
func TestHardConfinedWatch_WatchPrefixFiltersADeleteEventsPrevKv(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, InternalKeyPrefixSystem)
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	fake.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 2,
		Events: []*watch.Event{
			{Type: watch.EventDelete, PrevKv: &kv.KeyValue{Key: []byte(confinedKey), Value: []byte("gone now")}},
		},
	}
	fake.prefixWatcher.Close()

	select {
	case resp, ok := <-watcher.Events():
		if ok && len(resp.Events) != 0 {
			t.Fatalf("WatchPrefix delivered a delete event for the confined key: %v", resp.Events)
		}
	case <-time.After(time.Second):
		// The pump drops the response's entire Events slice to empty and
		// still forwards it (not drop the response wholesale), so either
		// an empty-Events response or the channel closing without one is
		// acceptable; a timeout here would mean the pump wedged, which is
		// the real failure this guards against.
		t.Fatal("timed out waiting for the filtered (now-empty) watch response")
	}

	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestHardConfinedWatch_WatchEntityRefusesTheConfinedEntity is go-qrdb#555's
// B2b regression proof for WatchEntity: entityKey(EntityType, ID) composes
// exactly to confinedKey, and WatchEntity must refuse it outright before a
// watcher is ever created, the same as Watch does for a literal key.
func TestHardConfinedWatch_WatchEntityRefusesTheConfinedEntity(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.WatchEntity(ctx, &WatchEntityRequest{EntityType: confinedEntityType, ID: confinedEntityID}); err == nil {
		t.Fatal("WatchEntity(confined entity) succeeded, want a refusal")
	} else {
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}
	if fake.calledWatch {
		t.Fatal("WatchEntity delegated to the underlying servicer despite the refusal")
	}
}

// TestHardConfinedWatch_WatchEntityTypeRefusesTheConfinedType proves
// WatchEntityType refuses outright when EntityType is itself the confined
// entity type, rather than returning a channel that could only ever
// deliver nothing.
func TestHardConfinedWatch_WatchEntityTypeRefusesTheConfinedType(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: confinedEntityType}); err == nil {
		t.Fatal("WatchEntityType(confined type) succeeded, want a refusal")
	} else {
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}
	if fake.calledWatchPrefix {
		t.Fatal("WatchEntityType delegated to the underlying servicer despite the refusal")
	}
}

// TestHardConfinedWatch_WatchEntityTypeFiltersASubPathOverlap is go-qrdb#591's
// review (non-blocking, "latent, but close it") red/green proof: an
// EntityType whose own composed prefix lies INSIDE a confined namespace
// without being EQUAL to its root (confinedEntityType + "/sub", here)
// neither matches isHardConfinedRoot (not an exact match) nor
// isHardConfinedKey (wrong suffix, a prefix test was never meant to use
// it) -- so it was never refused at all before this fix, and reached the
// underlying WatchEntityType unfiltered. It must now be let through
// (never refused outright, the same go-qrdb#555 lesson WatchPrefix
// learned for a broad sweep) but filtered: an event naming a
// hard-confined key is dropped, and an ordinary one passes.
func TestHardConfinedWatch_WatchEntityTypeFiltersASubPathOverlap(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{entityTypeChan: make(chan WatchEntityEvent, 2)}
	w := NewHardConfinedWatch(fake)

	subPathType := EntityType(string(confinedEntityType) + "/sub")
	ch, err := w.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: subPathType})
	if err != nil {
		t.Fatalf("WatchEntityType(sub-path overlap) = %v, want no refusal", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("expected WatchEntityType to reach the underlying servicer rather than refusing outright")
	}

	fake.entityTypeChan <- WatchEntityEvent{EntityType: subPathType, ID: confinedEntityID}
	fake.entityTypeChan <- WatchEntityEvent{EntityType: subPathType, ID: "ordinary-id"}
	close(fake.entityTypeChan)

	got, ok := <-ch
	if !ok {
		t.Fatal("expected the ordinary event to be delivered, got channel closed first")
	}
	if got.ID != "ordinary-id" {
		t.Fatalf("delivered event ID = %q, want %q (the confined one must have been dropped)", got.ID, "ordinary-id")
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected the channel to close after the one ordinary event")
	}
}

// TestHardConfinedWatch_WatchEntityTypeFilterGoroutineExitsOnContextDone is
// the red/green proof for go-qrdb#591's review (non-blocking 4):
// newHardConfinedFilteredEntityEvents' forwarding goroutine must not block
// forever on `out <- ev` when nothing ever reads from the returned
// channel -- a caller that abandons it (rather than draining it to
// completion) must still let the goroutine exit once its ctx ends, instead
// of leaking it, and pinning the underlying watch open, for the rest of
// the process's life.
func TestHardConfinedWatch_WatchEntityTypeFilterGoroutineExitsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeWatch{entityTypeChan: make(chan WatchEntityEvent, 1)}
	w := NewHardConfinedWatch(fake)

	subPathType := EntityType(string(confinedEntityType) + "/sub")
	ch, err := w.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: subPathType})
	if err != nil {
		t.Fatalf("WatchEntityType(sub-path overlap) = %v, want no refusal", err)
	}

	// Pushed into a buffered channel, so this send itself never blocks;
	// the forwarding goroutine's own attempt to relay it onto the
	// unbuffered `ch` -- with nothing here ever reading from `ch` -- is
	// exactly the blocking send this test targets.
	fake.entityTypeChan <- WatchEntityEvent{EntityType: subPathType, ID: "ordinary-id"}

	// Give the goroutine a moment to reach its blocking send before
	// canceling, so this test actually exercises that send rather than
	// racing ahead of it.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected ch to close once ctx ended, not to deliver a value nobody raced to read")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch never closed after ctx was canceled: the forwarding goroutine leaked, blocked on an unread send")
	}
}

// TestHardConfinedWatch_EntityMethodsPassThroughForOrdinaryTypes proves an
// unconfined entity type is unaffected.
func TestHardConfinedWatch_EntityMethodsPassThroughForOrdinaryTypes(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.WatchEntity(ctx, &WatchEntityRequest{EntityType: "items", ID: "1"}); err != nil {
		t.Fatalf("WatchEntity(ordinary entity): %v", err)
	}
	if !fake.calledWatch {
		t.Fatal("WatchEntity did not reach the underlying servicer for an ordinary entity")
	}

	if _, err := w.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: "items"}); err != nil {
		t.Fatalf("WatchEntityType(ordinary type): %v", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("WatchEntityType did not reach the underlying servicer for an ordinary type")
	}
}

func TestHardConfinedWatch_WatchPrefixOnAnOrdinaryPrefixIsNotWrapped(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, "ordinary/")
	if err != nil {
		t.Fatalf("WatchPrefix(ordinary prefix): %v", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("WatchPrefix did not reach the underlying servicer for an ordinary prefix")
	}
	// An ordinary prefix that cannot overlap the confined namespace gets
	// the RAW watcher back, unwrapped -- proven here by identity.
	if watcher != fake.prefixWatcher {
		t.Fatal("WatchPrefix wrapped a watcher for a prefix that cannot overlap the confined namespace")
	}
}

// TestHardConfinedFilteredWatcher_ConcurrentCloseClosesDoneOnce releases many
// Close calls together, the shape admittedWatcher produces when its caller's
// Close and its relay's end both close the inner watcher: done must be closed
// once, every call must return the inner watcher's nil, and the pump must
// still end.
func TestHardConfinedFilteredWatcher_ConcurrentCloseClosesDoneOnce(t *testing.T) {
	const rounds, closers = 50, 32
	for range rounds {
		w := newHardConfinedFilteredWatcher(newFakeWatcher())
		release := make(chan struct{})
		errs := make(chan error, closers)
		var wg sync.WaitGroup
		for range closers {
			wg.Go(func() {
				<-release
				errs <- w.Close()
			})
		}
		close(release)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("Close() = %v, want nil", err)
			}
		}
		if _, ok := <-w.Events(); ok {
			t.Fatal("Events() delivered after Close, want the channel closed")
		}
	}
}

// TestHardConfinedFilteredWatcher_CloseTwiceIsIdempotent closes the watcher
// twice in sequence: the second call must not panic and must return nil.
func TestHardConfinedFilteredWatcher_CloseTwiceIsIdempotent(t *testing.T) {
	w := newHardConfinedFilteredWatcher(newFakeWatcher())
	if err := w.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
	if _, ok := <-w.Events(); ok {
		t.Fatal("Events() delivered after Close, want the channel closed")
	}
}
