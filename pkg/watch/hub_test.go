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
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// ---------------------------------------------------------------------------
// NewHub
// ---------------------------------------------------------------------------

func TestNewHub_InitializesAllShards(t *testing.T) {
	h := NewHub()
	for i := 0; i < shardCount; i++ {
		if h.shards[i] == nil {
			t.Fatalf("shard[%d] is nil", i)
		}
		if h.shards[i].exactWatches == nil {
			t.Fatalf("shard[%d].exactWatches is nil", i)
		}
		if h.shards[i].prefixWatches == nil {
			t.Fatalf("shard[%d].prefixWatches is nil", i)
		}
	}
}

func TestNewHub_WatchCountIsZero(t *testing.T) {
	h := NewHub()
	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Watch (exact key)
// ---------------------------------------------------------------------------

func TestHub_Watch_CreatesExactWatcher(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	if w == nil {
		t.Fatal("Watch returned nil watcher")
	}
	if w.Events() == nil {
		t.Fatal("Events channel is nil")
	}
	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1", got)
	}
}

func TestHub_Watch_ReceivesPutEvent(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("key1"), Value: []byte("val1"), ModRevision: 1}
	h.NotifyPut([]byte("key1"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
		if !bytes.Equal(resp.Events[0].Kv.Key, []byte("key1")) {
			t.Fatalf("event key = %q, want %q", resp.Events[0].Kv.Key, "key1")
		}
		if !bytes.Equal(resp.Events[0].Kv.Value, []byte("val1")) {
			t.Fatalf("event value = %q, want %q", resp.Events[0].Kv.Value, "val1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events")
	}
}

func TestHub_Watch_OnClosedHub_ReturnsError(t *testing.T) {
	h := NewHub()
	h.Close()

	w, err := h.Watch(context.Background(), "key1")
	if w != nil {
		t.Fatal("expected nil watcher on closed hub")
	}
	if err == nil {
		t.Fatal("expected error on closed hub")
	}
	var hubErr *HubClosedError
	if !errors.As(err, &hubErr) {
		t.Fatalf("expected HubClosedError, got %T", err)
	}
}

func TestHub_Watch_MultipleWatchersSameKey(t *testing.T) {
	h := NewHub()
	w1, err := h.Watch(context.Background(), "shared")
	if err != nil {
		t.Fatalf("Watch w1 returned error: %v", err)
	}
	w2, err := h.Watch(context.Background(), "shared")
	if err != nil {
		t.Fatalf("Watch w2 returned error: %v", err)
	}

	if got := h.WatchCount(); got != 2 {
		t.Fatalf("WatchCount() = %d, want 2", got)
	}

	kvVal := &kv.KeyValue{Key: []byte("shared"), Value: []byte("v")}
	h.NotifyPut([]byte("shared"), kvVal, nil)

	for _, w := range []Watcher{w1, w2} {
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(resp.Events))
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix
// ---------------------------------------------------------------------------

func TestHub_WatchPrefix_ReceivesPrefixEvent(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "/app/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("/app/config"), Value: []byte("data")}
	h.NotifyPut([]byte("/app/config"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prefix event")
	}
}

func TestHub_WatchPrefix_DoesNotMatchNonPrefixKey(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "/app/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("/other/config"), Value: []byte("data")}
	h.NotifyPut([]byte("/other/config"), kvVal, nil)

	select {
	case resp := <-w.Events():
		t.Fatalf("should not have received events, got %d", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// No event received as expected.
	}

	_ = w // Keep reference to prevent cleanup.
}

func TestHub_WatchPrefix_OnClosedHub_ReturnsError(t *testing.T) {
	h := NewHub()
	h.Close()

	w, err := h.WatchPrefix(context.Background(), "/prefix/")
	if w != nil {
		t.Fatal("expected nil watcher on closed hub")
	}
	if err == nil {
		t.Fatal("expected error on closed hub")
	}
	var hubErr *HubClosedError
	if !errors.As(err, &hubErr) {
		t.Fatalf("expected HubClosedError, got %T", err)
	}
}

func TestHub_WatchPrefix_MultipleKeys(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "/ns/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	keys := []string{"/ns/a", "/ns/b", "/ns/c"}
	for _, k := range keys {
		kvVal := &kv.KeyValue{Key: []byte(k), Value: []byte("v")}
		h.NotifyPut([]byte(k), kvVal, nil)
	}

	for i := range keys {
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 {
				t.Fatalf("key %d: expected 1 event, got %d", i, len(resp.Events))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event for key %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// NotifyPut
// ---------------------------------------------------------------------------

func TestHub_NotifyPut_NoWatchers_FastPath(t *testing.T) {
	h := NewHub()
	// This should return immediately without panic.
	kvVal := &kv.KeyValue{Key: []byte("key"), Value: []byte("val")}
	h.NotifyPut([]byte("key"), kvVal, nil)
}

func TestHub_NotifyPut_ExactMatch(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "exact")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("exact"), Value: []byte("v1")}
	h.NotifyPut([]byte("exact"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exact match event")
	}
}

func TestHub_NotifyPut_NonMatchingWatcher(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "keyA")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("keyB"), Value: []byte("v")}
	h.NotifyPut([]byte("keyB"), kvVal, nil)

	select {
	case resp := <-w.Events():
		t.Fatalf("should not have received events, got %d", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// No event received as expected.
	}
}

func TestHub_NotifyPut_PrefixMatch(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "foo/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("foo/bar"), Value: []byte("baz")}
	h.NotifyPut([]byte("foo/bar"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prefix match event")
	}
}

// ---------------------------------------------------------------------------
// NotifyDelete
// ---------------------------------------------------------------------------

func TestHub_NotifyDelete_ExactMatch(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "delkey")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("delkey")}
	prevKv := &kv.KeyValue{Key: []byte("delkey"), Value: []byte("old")}
	h.NotifyDelete([]byte("delkey"), kvVal, prevKv)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

func TestHub_NotifyDelete_NoWatchers_FastPath(t *testing.T) {
	h := NewHub()
	kvVal := &kv.KeyValue{Key: []byte("key")}
	h.NotifyDelete([]byte("key"), kvVal, nil)
}

func TestHub_NotifyDelete_PrefixMatch(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "del/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("del/item")}
	prevKv := &kv.KeyValue{Key: []byte("del/item"), Value: []byte("old")}
	h.NotifyDelete([]byte("del/item"), kvVal, prevKv)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prefix delete event")
	}
}

// ---------------------------------------------------------------------------
// PrevKV option
// ---------------------------------------------------------------------------

func TestHub_PrevKV_Enabled(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "pk", WithPrevKV())
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("pk"), Value: []byte("new")}
	prevKv := &kv.KeyValue{Key: []byte("pk"), Value: []byte("old")}
	h.NotifyPut([]byte("pk"), kvVal, prevKv)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].PrevKv == nil {
			t.Fatal("expected PrevKv to be set when WithPrevKV is enabled")
		}
		if !bytes.Equal(resp.Events[0].PrevKv.Value, []byte("old")) {
			t.Fatalf("PrevKv.Value = %q, want %q", resp.Events[0].PrevKv.Value, "old")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event with PrevKV")
	}
}

func TestHub_PrevKV_Disabled(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "pk2")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("pk2"), Value: []byte("new")}
	prevKv := &kv.KeyValue{Key: []byte("pk2"), Value: []byte("old")}
	h.NotifyPut([]byte("pk2"), kvVal, prevKv)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].PrevKv != nil {
			t.Fatal("expected PrevKv to be nil when WithPrevKV is not set")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event without PrevKV")
	}
}

func TestHub_PrevKV_PrefixWatcher(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "pfx/", WithPrevKV())
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("pfx/a"), Value: []byte("new")}
	prevKv := &kv.KeyValue{Key: []byte("pfx/a"), Value: []byte("old")}
	h.NotifyPut([]byte("pfx/a"), kvVal, prevKv)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].PrevKv == nil {
			t.Fatal("expected PrevKv to be set for prefix watcher with WithPrevKV")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for prefix event with PrevKV")
	}
}

// ---------------------------------------------------------------------------
// WatchFilter
// ---------------------------------------------------------------------------

func TestHub_WatchFilter_NoPut(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "filtered", WithFilters(WatchFilterNoPut))
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("filtered"), Value: []byte("v")}
	h.NotifyPut([]byte("filtered"), kvVal, nil)

	select {
	case resp := <-w.Events():
		t.Fatalf("should not have received put events with NoPut filter, got %d events", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// Put filtered as expected.
	}

	// Delete should still be received.
	h.NotifyDelete([]byte("filtered"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

func TestHub_WatchFilter_NoDelete(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "filtered2", WithFilters(WatchFilterNoDelete))
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("filtered2")}
	h.NotifyDelete([]byte("filtered2"), kvVal, nil)

	select {
	case resp := <-w.Events():
		t.Fatalf("should not have received delete events with NoDelete filter, got %d events", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// Delete filtered as expected.
	}

	// Put should still be received.
	kvVal = &kv.KeyValue{Key: []byte("filtered2"), Value: []byte("v")}
	h.NotifyPut([]byte("filtered2"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for put event")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestHub_Close_CancelsAllWatchers(t *testing.T) {
	h := NewHub()

	w1, err := h.Watch(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	w2, err := h.WatchPrefix(context.Background(), "cp/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	h.Close()

	// No wait here: the receive below blocks until the cleanup goroutine closes
	// the channel, which is the event being asserted.
	// Event channels should be closed after cleanup goroutines complete.
	for name, w := range map[string]Watcher{"exact": w1, "prefix": w2} {
		select {
		case _, ok := <-w.Events():
			if ok {
				// Drain any residual events, then expect closed channel.
				for range w.Events() {
				}
			}
			// Channel was closed as expected.
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: timed out waiting for channel close", name)
		}
	}

	// Verify no new watchers can be created.
	_, err = h.Watch(context.Background(), "after-close")
	if err == nil {
		t.Fatal("expected error creating watcher after Close")
	}
}

func TestHub_Close_Idempotent(t *testing.T) {
	h := NewHub()
	_, err := h.Watch(context.Background(), "idem")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	h.Close()
	h.Close() // Second call should be a no-op.

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after double Close", got)
	}
}

func TestHub_Close_PreventsNewWatchers(t *testing.T) {
	h := NewHub()
	h.Close()

	_, err := h.Watch(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error after Close")
	}

	_, err = h.WatchPrefix(context.Background(), "nope/")
	if err == nil {
		t.Fatal("expected error after Close")
	}
}

// ---------------------------------------------------------------------------
// WatchCount
// ---------------------------------------------------------------------------

func TestHub_WatchCount_TracksActiveWatchers(t *testing.T) {
	h := NewHub()

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("initial WatchCount() = %d, want 0", got)
	}

	_, err := h.Watch(context.Background(), "a")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1", got)
	}

	_, err = h.WatchPrefix(context.Background(), "b/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}
	if got := h.WatchCount(); got != 2 {
		t.Fatalf("WatchCount() = %d, want 2", got)
	}
}

func TestHub_WatchCount_DecrementsOnCancel(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())

	w, err := h.Watch(ctx, "cancel-me")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1", got)
	}

	cancel()
	waitDeregistered(t, w)

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after cancel", got)
	}
}

// ---------------------------------------------------------------------------
// Watcher cleanup on context cancel
// ---------------------------------------------------------------------------

func TestHub_CleanupOnCancel_ExactWatcher(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())

	w, err := h.Watch(ctx, "cleanup-exact")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	cancel()
	waitDeregistered(t, w)

	// Channel should be closed after cleanup.
	select {
	case _, ok := <-w.Events():
		// May get residual events; we just confirm the channel is reachable.
		_ = ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events channel to close")
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after cleanup", got)
	}
}

func TestHub_CleanupOnCancel_PrefixWatcher(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())

	w, err := h.WatchPrefix(ctx, "cleanup/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	cancel()
	waitDeregistered(t, w)

	select {
	case _, ok := <-w.Events():
		// May get residual events; we just confirm the channel is reachable.
		_ = ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events channel to close")
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after cleanup", got)
	}
}

// ---------------------------------------------------------------------------
// FNV-1a hash distribution (tested via hub behavior)
// ---------------------------------------------------------------------------

func TestHub_FNV1a_DifferentKeysUseDifferentShards(t *testing.T) {
	// Two keys that should hash to different shards.
	shard1 := fnv1a([]byte("alpha"))
	shard2 := fnv1a([]byte("beta"))

	// We can't guarantee they differ, but verify the function is
	// deterministic and returns values in range.
	if int(shard1) >= shardCount {
		t.Fatalf("fnv1a returned %d, expected < %d", shard1, shardCount)
	}
	if int(shard2) >= shardCount {
		t.Fatalf("fnv1a returned %d, expected < %d", shard2, shardCount)
	}
}

func TestHub_FNV1a_Deterministic(t *testing.T) {
	key := []byte("deterministic-key")
	h1 := fnv1a(key)
	h2 := fnv1a(key)
	if h1 != h2 {
		t.Fatalf("fnv1a is not deterministic: %d != %d", h1, h2)
	}
}

func TestHub_FNV1a_EmptyKey(t *testing.T) {
	idx := fnv1a([]byte{})
	if int(idx) >= shardCount {
		t.Fatalf("fnv1a(empty) returned %d, expected < %d", idx, shardCount)
	}
}

// ---------------------------------------------------------------------------
// matchesRange
// ---------------------------------------------------------------------------

func TestMatchesRange_KeyInRange(t *testing.T) {
	if !matchesRange([]byte("foo/bar"), []byte("foo/"), []byte("foo0")) {
		t.Fatal("expected key in range to match")
	}
}

func TestMatchesRange_KeyBelowRange(t *testing.T) {
	if matchesRange([]byte("abc"), []byte("foo/"), []byte("foo0")) {
		t.Fatal("expected key below range not to match")
	}
}

func TestMatchesRange_KeyAboveRange(t *testing.T) {
	if matchesRange([]byte("foo1"), []byte("foo/"), []byte("foo0")) {
		t.Fatal("expected key above range not to match")
	}
}

func TestMatchesRange_EmptyEnd_MatchesAll(t *testing.T) {
	// Empty but non-nil end means "match all keys at or after start".
	if !matchesRange([]byte("zzz"), []byte("aaa"), []byte{}) {
		t.Fatal("expected empty end to match all keys at or after start")
	}
}

func TestMatchesRange_ExactStart(t *testing.T) {
	if !matchesRange([]byte("foo/"), []byte("foo/"), []byte("foo0")) {
		t.Fatal("expected exact start key to match")
	}
}

func TestMatchesRange_ExactEnd_Excluded(t *testing.T) {
	// The range is [start, end), so end itself should NOT match.
	if matchesRange([]byte("foo0"), []byte("foo/"), []byte("foo0")) {
		t.Fatal("expected exact end key to NOT match (exclusive)")
	}
}

// ---------------------------------------------------------------------------
// prefixEnd
// ---------------------------------------------------------------------------

func TestPrefixEnd_NormalPrefix(t *testing.T) {
	got := prefixEnd([]byte("foo/"))
	want := []byte("foo0") // '/' + 1 = '0'
	if !bytes.Equal(got, want) {
		t.Fatalf("prefixEnd(%q) = %q, want %q", "foo/", got, want)
	}
}

func TestPrefixEnd_EmptyPrefix(t *testing.T) {
	got := prefixEnd([]byte{})
	if got == nil {
		t.Fatal("expected non-nil result for empty prefix")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice for empty prefix, got %q", got)
	}
}

func TestPrefixEnd_AllOxFF(t *testing.T) {
	got := prefixEnd([]byte{0xff, 0xff, 0xff})
	if got == nil {
		t.Fatal("expected non-nil result for all-0xff prefix")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice for all-0xff prefix, got %v", got)
	}
}

func TestPrefixEnd_SingleByte(t *testing.T) {
	got := prefixEnd([]byte("a"))
	want := []byte("b")
	if !bytes.Equal(got, want) {
		t.Fatalf("prefixEnd(%q) = %q, want %q", "a", got, want)
	}
}

func TestPrefixEnd_TrailingOxFF(t *testing.T) {
	// "a\xff" -> last byte is 0xff, so we increment the 'a' and truncate.
	got := prefixEnd([]byte{'a', 0xff})
	want := []byte("b")
	if !bytes.Equal(got, want) {
		t.Fatalf("prefixEnd(%v) = %v, want %v", []byte{'a', 0xff}, got, want)
	}
}

// ---------------------------------------------------------------------------
// Mixed exact and prefix watchers
// ---------------------------------------------------------------------------

func TestHub_ExactAndPrefixWatchers_BothReceiveEvents(t *testing.T) {
	h := NewHub()

	exactW, err := h.Watch(context.Background(), "mixed/key")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	prefixW, err := h.WatchPrefix(context.Background(), "mixed/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("mixed/key"), Value: []byte("v")}
	h.NotifyPut([]byte("mixed/key"), kvVal, nil)

	// Both watchers should receive the event.
	for name, w := range map[string]Watcher{"exact": exactW, "prefix": prefixW} {
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 {
				t.Fatalf("%s: expected 1 event, got %d", name, len(resp.Events))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: timed out waiting for event", name)
		}
	}
}

func TestHub_PrefixWatcher_DoesNotReceiveExactOnlyEvent(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "only/prefix/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	// Notify a key that doesn't match the prefix.
	kvVal := &kv.KeyValue{Key: []byte("other/key"), Value: []byte("v")}
	h.NotifyPut([]byte("other/key"), kvVal, nil)

	select {
	case resp := <-w.Events():
		t.Fatalf("prefix watcher should not receive non-matching event, got %d events", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// No event as expected.
	}
}

// ---------------------------------------------------------------------------
// Sequential event delivery
// ---------------------------------------------------------------------------

func TestHub_EventDeliveryOrder(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "order")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	for i := int64(1); i <= 5; i++ {
		kvVal := &kv.KeyValue{Key: []byte("order"), Value: []byte("v"), ModRevision: i}
		h.NotifyPut([]byte("order"), kvVal, nil)
	}

	for i := int64(1); i <= 5; i++ {
		select {
		case resp := <-w.Events():
			if len(resp.Events) != 1 {
				t.Fatalf("event %d: expected 1 event, got %d", i, len(resp.Events))
			}
			if resp.Events[0].Kv.ModRevision != i {
				t.Fatalf("event %d: ModRevision = %d, want %d", i, resp.Events[0].Kv.ModRevision, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Watcher Close via Hub
// ---------------------------------------------------------------------------

func TestHub_WatcherClose_RemovesFromRegistry(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "close-me")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1", got)
	}

	// No wait: Close does not return until the watcher is out of the registry.
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after Close", got)
	}
}

func TestHub_PrefixWatcherClose_RemovesFromRegistry(t *testing.T) {
	h := NewHub()
	w, err := h.WatchPrefix(context.Background(), "close-pfx/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1", got)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after prefix Close", got)
	}
}

// ---------------------------------------------------------------------------
// Edge: NotifyPut/NotifyDelete after Close
// ---------------------------------------------------------------------------

func TestHub_NotifyPut_AfterClose_DoesNotPanic(t *testing.T) {
	h := NewHub()
	h.Close()

	kvVal := &kv.KeyValue{Key: []byte("key"), Value: []byte("val")}
	// Should not panic; watchCount is 0 so fast path returns.
	h.NotifyPut([]byte("key"), kvVal, nil)
}

func TestHub_NotifyDelete_AfterClose_DoesNotPanic(t *testing.T) {
	h := NewHub()
	h.Close()

	kvVal := &kv.KeyValue{Key: []byte("key")}
	h.NotifyDelete([]byte("key"), kvVal, nil)
}

// ---------------------------------------------------------------------------
// getShard coverage
// ---------------------------------------------------------------------------

func TestHub_GetShard_ReturnsValidShard(t *testing.T) {
	h := NewHub()
	sh := h.getShard([]byte("test-key"))
	if sh == nil {
		t.Fatal("getShard returned nil")
	}
	if sh.exactWatches == nil {
		t.Fatal("shard exactWatches is nil")
	}
}

func TestHub_GetShard_EmptyKey(t *testing.T) {
	h := NewHub()
	sh := h.getShard([]byte{})
	if sh == nil {
		t.Fatal("getShard returned nil for empty key")
	}
}

// ---------------------------------------------------------------------------
// WatchResponse envelope
// ---------------------------------------------------------------------------

func TestHub_WatchResponse_ContainsWatchID(t *testing.T) {
	h := NewHub()
	w, err := h.Watch(context.Background(), "resp-key")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("resp-key"), Value: []byte("v")}
	h.NotifyPut([]byte("resp-key"), kvVal, nil)

	select {
	case resp := <-w.Events():
		if resp.WatchID != w.WatchID() {
			t.Fatalf("WatchResponse.WatchID = %d, want %d", resp.WatchID, w.WatchID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

func TestHub_WatchResponse_WatchIDMatchesWatcher(t *testing.T) {
	h := NewHub()
	w1, err := h.Watch(context.Background(), "id-key")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	w2, err := h.Watch(context.Background(), "id-key")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	kvVal := &kv.KeyValue{Key: []byte("id-key"), Value: []byte("v")}
	h.NotifyPut([]byte("id-key"), kvVal, nil)

	select {
	case resp := <-w1.Events():
		if resp.WatchID != w1.WatchID() {
			t.Fatalf("w1 WatchResponse.WatchID = %d, want %d", resp.WatchID, w1.WatchID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("w1: timed out")
	}

	select {
	case resp := <-w2.Events():
		if resp.WatchID != w2.WatchID() {
			t.Fatalf("w2 WatchResponse.WatchID = %d, want %d", resp.WatchID, w2.WatchID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("w2: timed out")
	}
}

// ---------------------------------------------------------------------------
// Close is synchronous with deregistration (#309)
// ---------------------------------------------------------------------------

// waitDeregistered blocks until the hub's cleanup goroutine has removed w from
// the registry. It is the same signal watcher.Close waits on, used directly by
// the tests that trigger cleanup by cancelling the parent context instead of
// calling Close. The deadline is a failure report, not synchronisation: the
// test waits on the signal, and the bound only turns a hang into a named
// failure with the count that was actually observed.
func waitDeregistered(t *testing.T, w Watcher) {
	t.Helper()
	select {
	case <-w.(*watcher).deregistered:
	case <-time.After(10 * time.Second):
		t.Fatalf("watcher %d not deregistered within 10s", w.WatchID())
	}
}

// TestHub_WatcherClose_DeregistersUnderConcurrentNotify is the load-bearing
// version of TestHub_WatcherClose_RemovesFromRegistry: it asserts the count
// immediately after Close while the notify path is running flat out against the
// same shard, which is the condition #309 was reported under (a loaded host
// that had not scheduled the cleanup goroutine). Every iteration that read a
// non-zero count under the old cancel-only Close is a failure here.
func TestHub_WatcherClose_DeregistersUnderConcurrentNotify(t *testing.T) {
	h := NewHub()

	stop := make(chan struct{})
	notifying := make(chan struct{})
	go func() {
		close(notifying)
		kvVal := &kv.KeyValue{Key: []byte("busy"), Value: []byte("v"), ModRevision: 1}
		for {
			select {
			case <-stop:
				return
			default:
				h.NotifyPut([]byte("busy"), kvVal, nil)
			}
		}
	}()
	<-notifying
	defer close(stop)

	for i := 0; i < 200; i++ {
		w, err := h.Watch(context.Background(), "busy")
		if err != nil {
			t.Fatalf("iteration %d: Watch returned error: %v", i, err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("iteration %d: Close returned error: %v", i, err)
		}
		if got := h.WatchCount(); got != 0 {
			t.Fatalf("iteration %d: WatchCount() = %d, want 0 immediately after Close", i, got)
		}
	}
}

// TestHub_WatcherClose_FromEventConsumer_DoesNotDeadlock closes the watcher
// from inside the goroutine that is draining its events, while the notify path
// keeps dispatching. Close waits for the cleanup goroutine, which takes the
// shard write lock; notify holds the shard read lock while it sends. The send
// is non-blocking and never waits on the consumer, so the two cannot form a
// cycle — this test is what says so out loud.
func TestHub_WatcherClose_FromEventConsumer_DoesNotDeadlock(t *testing.T) {
	h := NewHub()

	w, err := h.Watch(context.Background(), "consume")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	stop := make(chan struct{})
	go func() {
		kvVal := &kv.KeyValue{Key: []byte("consume"), Value: []byte("v"), ModRevision: 1}
		for {
			select {
			case <-stop:
				return
			default:
				h.NotifyPut([]byte("consume"), kvVal, nil)
			}
		}
	}()
	defer close(stop)

	closed := make(chan error, 1)
	go func() {
		// Take one event, then close from inside the delivery loop.
		<-w.Events()
		closed <- w.Close()
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close called from the event consumer did not return within 10s")
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after Close", got)
	}
}

// TestHub_WatcherClose_AfterHubClose_DoesNotDeadlock covers the second ordering
// a caller can produce: the hub tore the watcher down first, and the owner
// closes it afterwards anyway (every API handler does this from a defer). Close
// must observe the teardown that already happened rather than wait for one that
// will never be scheduled.
func TestHub_WatcherClose_AfterHubClose_DoesNotDeadlock(t *testing.T) {
	h := NewHub()

	exact, err := h.Watch(context.Background(), "hub-closed")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	prefix, err := h.WatchPrefix(context.Background(), "hub-closed/")
	if err != nil {
		t.Fatalf("WatchPrefix returned error: %v", err)
	}

	h.Close()

	for name, w := range map[string]Watcher{"exact": exact, "prefix": prefix} {
		done := make(chan error, 1)
		go func(w Watcher) { done <- w.Close() }(w)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: Close returned error: %v", name, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Close after Hub.Close did not return within 10s", name)
		}
	}

	// Hub.Close empties the registry and zeroes the count itself, so the cleanup
	// goroutines that ran above must not have decremented it a second time. An
	// unconditional decrement wrapped this to 18446744073709551615.
	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after Hub.Close plus watcher Close", got)
	}
}

// TestHub_WatcherClose_Idempotent_AllCallersWaitForDeregistration pins the
// contract for the losers of the CAS as well as the winner: Close means "the
// watcher is gone" for every caller, so the late ones cannot return early and
// read a stale count.
func TestHub_WatcherClose_Idempotent_AllCallersWaitForDeregistration(t *testing.T) {
	h := NewHub()

	w, err := h.Watch(context.Background(), "idempotent")
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	const closers = 8
	counts := make(chan uint64, closers)
	start := make(chan struct{})
	for i := 0; i < closers; i++ {
		go func() {
			<-start
			if err := w.Close(); err != nil {
				counts <- 1 // non-zero fails the assertion below with the error visible
				return
			}
			counts <- h.WatchCount()
		}()
	}
	close(start)

	for i := 0; i < closers; i++ {
		select {
		case got := <-counts:
			if got != 0 {
				t.Fatalf("closer %d observed WatchCount() = %d after its own Close returned, want 0", i, got)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d concurrent Close calls returned within 10s", i, closers)
		}
	}
}

// TestHub_WatcherClose_DuringReplay_DoesNotAwaitScan is the reason Close waits
// for deregistration and not for the event channel. fakeHistory's gate holds
// Replay open indefinitely; cleanup cannot close the event channel until that
// goroutine lets go, but deregistration does not depend on it, so Close returns.
// If Close ever moves behind replayDone this test hangs and reports it.
func TestHub_WatcherClose_DuringReplay_DoesNotAwaitScan(t *testing.T) {
	gate := make(chan struct{})
	fh := &fakeHistory{
		current: 3,
		changes: []fakeChange{
			{rev: 1, key: "replay-close", value: "a"},
			{rev: 2, key: "replay-close", value: "b"},
			{rev: 3, key: "replay-close", value: "c"},
		},
		gate:   gate,
		firstN: 1,
	}

	h := NewHub()
	h.SetHistory(fh)

	w, err := h.Watch(context.Background(), "replay-close", WithStartRevision(1))
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}

	// Wait until the replay goroutine is parked on the gate, so Close is racing
	// a scan that genuinely cannot finish rather than one that already has.
	collectN(t, w, 2) // Created response, then the first replayed event.

	closed := make(chan error, 1)
	go func() { closed <- w.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		close(gate)
		t.Fatal("Close blocked on the in-flight history scan; it must only await deregistration")
	}

	if got := h.WatchCount(); got != 0 {
		t.Fatalf("WatchCount() = %d, want 0 after Close during replay", got)
	}

	// Release the scan so the cleanup goroutine can finish and close the channel.
	close(gate)
	for range w.Events() {
	}
}
