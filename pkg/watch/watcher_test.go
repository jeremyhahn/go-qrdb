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

package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// ---------------------------------------------------------------------------
// Compile-time assertion
// ---------------------------------------------------------------------------

func TestWatcher_ImplementsWatcherInterface(t *testing.T) {
	var _ Watcher = (*watcher)(nil)
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func TestWatcher_Events_ReturnsChannel(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	ch := w.Events()
	if ch == nil {
		t.Fatal("expected non-nil events channel")
	}
}

func TestWatcher_Events_ChannelReceivesEvents(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	resp := WatchResponse{
		WatchID: 1,
		Events: []*Event{
			{Type: EventPut, Kv: &kv.KeyValue{Key: []byte("key"), Value: []byte("val")}},
		},
	}
	w.send(resp)

	select {
	case got := <-w.Events():
		if len(got.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(got.Events))
		}
		if got.Events[0].Type != EventPut {
			t.Fatalf("expected EventPut, got %d", got.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events")
	}
}

// ---------------------------------------------------------------------------
// WatchID
// ---------------------------------------------------------------------------

func TestWatcher_WatchID_ReturnsID(t *testing.T) {
	w := newWatcher(42, []byte("key"), nil, WatchOptions{}, context.Background())
	if got := w.WatchID(); got != 42 {
		t.Fatalf("WatchID() = %d, want 42", got)
	}
}

func TestWatcher_WatchID_DifferentWatchers(t *testing.T) {
	w1 := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	w2 := newWatcher(2, []byte("key"), nil, WatchOptions{}, context.Background())
	if w1.WatchID() == w2.WatchID() {
		t.Fatal("expected different WatchIDs")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestWatcher_Close_CancelsContext(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())

	if err := w.Close(); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}

	select {
	case <-w.ctx.Done():
		// Context was cancelled as expected.
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled after Close")
	}
}

func TestWatcher_Close_Idempotent(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())

	if err := w.Close(); err != nil {
		t.Fatalf("first Close returned error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}

	// Verify the context is still cancelled.
	select {
	case <-w.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context should be cancelled")
	}
}

func TestWatcher_Close_ReturnsNil(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	if err := w.Close(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Err
// ---------------------------------------------------------------------------

func TestWatcher_Err_NilWhenNoError(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	if err := w.Err(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWatcher_Err_ReturnsStoredError(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	want := &HubClosedError{}
	w.setErr(want)

	got := w.Err()
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.Error() != want.Error() {
		t.Fatalf("Err() = %q, want %q", got.Error(), want.Error())
	}
}

func TestWatcher_SetErr_OnlyFirstErrorStored(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	first := &HubClosedError{}
	w.setErr(first)

	second := &HubClosedError{}
	w.setErr(second)

	got := w.Err()
	if got != first {
		t.Fatal("expected first error to be retained")
	}
}

// ---------------------------------------------------------------------------
// send
// ---------------------------------------------------------------------------

func TestWatcher_Send_DeliversEvents(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())
	resp := WatchResponse{
		WatchID: 1,
		Events: []*Event{
			{Type: EventPut, Kv: &kv.KeyValue{Key: []byte("key"), Value: []byte("val")}},
		},
	}
	w.send(resp)

	select {
	case got := <-w.events:
		if len(got.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(got.Events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send")
	}
}

func TestWatcher_Send_CancelledContextDoesNotBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, ctx)

	done := make(chan struct{})
	go func() {
		resp := WatchResponse{
			WatchID: 1,
			Events: []*Event{
				{Type: EventPut, Kv: &kv.KeyValue{Key: []byte("key"), Value: []byte("val")}},
			},
		}
		w.send(resp)
		close(done)
	}()

	select {
	case <-done:
		// send returned without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("send blocked on cancelled context")
	}
}

func TestWatcher_Send_MultipleEventBatches(t *testing.T) {
	w := newWatcher(1, []byte("key"), nil, WatchOptions{}, context.Background())

	batch1 := WatchResponse{
		WatchID: 1,
		Events: []*Event{
			{Type: EventPut, Kv: &kv.KeyValue{Key: []byte("k1"), Value: []byte("v1")}},
		},
	}
	batch2 := WatchResponse{
		WatchID: 1,
		Events: []*Event{
			{Type: EventDelete, Kv: &kv.KeyValue{Key: []byte("k2")}},
		},
	}

	w.send(batch1)
	w.send(batch2)

	for i, want := range []EventType{EventPut, EventDelete} {
		select {
		case got := <-w.events:
			if len(got.Events) != 1 {
				t.Fatalf("batch %d: expected 1 event, got %d", i, len(got.Events))
			}
			if got.Events[0].Type != want {
				t.Fatalf("batch %d: expected event type %d, got %d", i, want, got.Events[0].Type)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for batch %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// hasFilter
// ---------------------------------------------------------------------------

func TestWatcher_HasFilter_ReturnsTrue(t *testing.T) {
	opts := WatchOptions{Filters: []WatchFilter{WatchFilterNoPut}}
	w := newWatcher(1, []byte("key"), nil, opts, context.Background())
	if !w.hasFilter(WatchFilterNoPut) {
		t.Fatal("expected hasFilter(WatchFilterNoPut) to return true")
	}
}

func TestWatcher_HasFilter_ReturnsFalse(t *testing.T) {
	opts := WatchOptions{Filters: []WatchFilter{WatchFilterNoPut}}
	w := newWatcher(1, []byte("key"), nil, opts, context.Background())
	if w.hasFilter(WatchFilterNoDelete) {
		t.Fatal("expected hasFilter(WatchFilterNoDelete) to return false")
	}
}

// ---------------------------------------------------------------------------
// newWatcher configuration
// ---------------------------------------------------------------------------

func TestNewWatcher_SetsFields(t *testing.T) {
	opts := WatchOptions{
		PrevKV:         true,
		StartRevision:  42,
		ProgressNotify: true,
		Filters:        []WatchFilter{WatchFilterNoPut},
	}
	w := newWatcher(7, []byte("mykey"), []byte("myend"), opts, context.Background())

	if w.id != 7 {
		t.Fatalf("id = %d, want 7", w.id)
	}
	if string(w.key) != "mykey" {
		t.Fatalf("key = %q, want %q", w.key, "mykey")
	}
	if string(w.rangeEnd) != "myend" {
		t.Fatalf("rangeEnd = %q, want %q", w.rangeEnd, "myend")
	}
	if !w.prevKV {
		t.Fatal("expected prevKV to be true")
	}
	if w.startRevision != 42 {
		t.Fatalf("startRevision = %d, want 42", w.startRevision)
	}
	if len(w.filters) != 1 || w.filters[0] != WatchFilterNoPut {
		t.Fatalf("filters = %v, want [WatchFilterNoPut]", w.filters)
	}
}

func TestNewWatcher_DefaultOptions(t *testing.T) {
	w := newWatcher(1, []byte("k"), nil, WatchOptions{}, context.Background())

	if w.prevKV {
		t.Fatal("expected prevKV to be false by default")
	}
	if w.startRevision != 0 {
		t.Fatalf("expected startRevision 0, got %d", w.startRevision)
	}
	if len(w.filters) != 0 {
		t.Fatalf("expected no filters by default, got %d", len(w.filters))
	}
}

// TestWatcher_SendOverflowEvictsWithoutBlocking verifies that send() never
// blocks when the event buffer is full — it evicts the watcher (cancel +
// ErrWatcherOverflow) instead. This is the property that keeps a slow or
// abandoned watcher from stalling the state-machine apply loop.
func TestWatcher_SendOverflowEvictsWithoutBlocking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &watcher{
		id:     1,
		events: make(chan WatchResponse, 1),
		ctx:    ctx,
		cancel: cancel,
	}

	// Fill the single-slot buffer.
	w.send(WatchResponse{WatchID: 1})

	// The buffer is now full; this send must return promptly (not block) and
	// evict the watcher.
	done := make(chan struct{})
	go func() {
		w.send(WatchResponse{WatchID: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("send blocked on a full buffer; it must be non-blocking")
	}

	// Eviction: context cancelled and overflow error recorded.
	select {
	case <-w.ctx.Done():
	default:
		t.Error("overflow did not cancel (evict) the watcher")
	}
	if ep := w.err.Load(); ep == nil || !errors.Is(*ep, ErrWatcherOverflow) {
		t.Errorf("expected ErrWatcherOverflow recorded, got %v", ep)
	}
}
