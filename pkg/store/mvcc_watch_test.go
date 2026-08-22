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

package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Watch hub integration helpers
// ---------------------------------------------------------------------------

// newTestMVCCWithHub creates an MVCCStateMachine with a watch hub attached,
// opens it, and registers cleanup. It returns both the state machine and hub.
func newTestMVCCWithHub(t *testing.T) (*MVCCStateMachine, *watch.Hub) {
	t.Helper()
	dir := t.TempDir()
	hub := watch.NewHub()
	s := NewMVCCStateMachine(nil)
	s.SetWatchHub(hub)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("open mvcc sm: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(ctx)
	})
	return s, hub
}

// receiveEvents drains a single batch of events from the watcher within a
// timeout. It fails the test if no events arrive.
func receiveEvents(t *testing.T, w service.Watcher) []*service.Event {
	t.Helper()
	select {
	case resp := <-w.Events():
		return resp.Events
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch events")
		return nil
	}
}

// assertNoEvents verifies that no events arrive on the watcher within a
// short window.
func assertNoEvents(t *testing.T, w service.Watcher) {
	t.Helper()
	select {
	case resp := <-w.Events():
		t.Fatalf("expected no events, got %d", len(resp.Events))
	case <-time.After(100 * time.Millisecond):
		// No events arrived; success.
	}
}

// ---------------------------------------------------------------------------
// SetWatchHub
// ---------------------------------------------------------------------------

func TestMVCCWatch_SetWatchHub_AttachesHub(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Create a watcher before the put.
	w, err := hub.Watch(context.Background(), "key1", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Put a value and verify the watcher receives a notification.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("val1"), 0),
	})

	events := receiveEvents(t, w)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != service.EventPut {
		t.Fatalf("expected EventPut, got %d", events[0].Type)
	}
	if !bytes.Equal(events[0].Kv.Key, []byte("key1")) {
		t.Fatalf("event key = %q, want %q", events[0].Kv.Key, "key1")
	}
	if !bytes.Equal(events[0].Kv.Value, []byte("val1")) {
		t.Fatalf("event value = %q, want %q", events[0].Kv.Value, "val1")
	}
}

func TestMVCCWatch_SetWatchHub_NilHub(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)

	// Setting nil hub must not panic.
	s.SetWatchHub(nil)

	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	// Update with nil hub must succeed without panic.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("val1"), 0),
	})

	// Verify the value was written correctly.
	result, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("key1")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := result.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("val1")) {
		t.Fatalf("value = %q, want %q", kv.Value, "val1")
	}
}

// ---------------------------------------------------------------------------
// Update - Put notifications
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_Put_NotifiesExactWatcher(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Watch "key1" before putting.
	w, err := hub.Watch(context.Background(), "key1", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("hello"), 0),
	})

	events := receiveEvents(t, w)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != service.EventPut {
		t.Fatalf("expected EventPut, got %d", ev.Type)
	}
	if !bytes.Equal(ev.Kv.Key, []byte("key1")) {
		t.Fatalf("kv.Key = %q, want %q", ev.Kv.Key, "key1")
	}
	if !bytes.Equal(ev.Kv.Value, []byte("hello")) {
		t.Fatalf("kv.Value = %q, want %q", ev.Kv.Value, "hello")
	}
	// First put has no previous value.
	if ev.PrevKv != nil {
		t.Fatalf("expected nil prevKv on first put, got %+v", ev.PrevKv)
	}
}

func TestMVCCWatch_Update_Put_IncludesPrevKv(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// First put (seed the key).
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("first"), 0),
	})

	// Watch after the first put, with PrevKV enabled.
	w, err := hub.Watch(context.Background(), "key1", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Second put overwrites.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("key1"), []byte("second"), 0),
	})

	events := receiveEvents(t, w)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != service.EventPut {
		t.Fatalf("expected EventPut, got %d", ev.Type)
	}
	if !bytes.Equal(ev.Kv.Value, []byte("second")) {
		t.Fatalf("kv.Value = %q, want %q", ev.Kv.Value, "second")
	}
	// prevKv must reflect the first value.
	if ev.PrevKv == nil {
		t.Fatal("expected non-nil prevKv on second put")
	}
	if !bytes.Equal(ev.PrevKv.Value, []byte("first")) {
		t.Fatalf("prevKv.Value = %q, want %q", ev.PrevKv.Value, "first")
	}
}

// ---------------------------------------------------------------------------
// Update - Delete notifications
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_Delete_NotifiesExactWatcher(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Seed a key.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("delkey"), []byte("val"), 0),
	})

	// Watch with prevKV.
	w, err := hub.Watch(context.Background(), "delkey", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Delete the key.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccDeleteEntry(2, []byte("delkey")),
	})

	events := receiveEvents(t, w)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != service.EventDelete {
		t.Fatalf("expected EventDelete, got %d", ev.Type)
	}
	// kv is the tombstone entry with the key and mod revision.
	if !bytes.Equal(ev.Kv.Key, []byte("delkey")) {
		t.Fatalf("kv.Key = %q, want %q", ev.Kv.Key, "delkey")
	}
	if ev.Kv.ModRevision == 0 {
		t.Fatal("expected non-zero ModRevision on delete tombstone")
	}
	// prevKv must reflect the previous value.
	if ev.PrevKv == nil {
		t.Fatal("expected non-nil prevKv on delete")
	}
	if !bytes.Equal(ev.PrevKv.Value, []byte("val")) {
		t.Fatalf("prevKv.Value = %q, want %q", ev.PrevKv.Value, "val")
	}
}

// ---------------------------------------------------------------------------
// Update - Prefix watcher
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_PrefixWatcher_MatchesPut(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Watch all keys under "prefix/".
	w, err := hub.WatchPrefix(context.Background(), "prefix/", service.WithPrevKV())
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	// Put a key matching the prefix.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("prefix/a"), []byte("aval"), 0),
	})

	events := receiveEvents(t, w)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != service.EventPut {
		t.Fatalf("expected EventPut, got %d", ev.Type)
	}
	if !bytes.Equal(ev.Kv.Key, []byte("prefix/a")) {
		t.Fatalf("kv.Key = %q, want %q", ev.Kv.Key, "prefix/a")
	}
	if !bytes.Equal(ev.Kv.Value, []byte("aval")) {
		t.Fatalf("kv.Value = %q, want %q", ev.Kv.Value, "aval")
	}
}

// ---------------------------------------------------------------------------
// Update - No hub attached
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_NoHub_NoNotifications(t *testing.T) {
	// Create a state machine without a watch hub.
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	ctx := context.Background()
	_, err := s.Open(ctx, dir, make(chan struct{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })

	// Update must succeed without panic.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("nowatch"), []byte("val"), 0),
	})

	// Verify write succeeded.
	result, err := s.Lookup(ctx, service.MVCCGetQuery{Key: []byte("nowatch")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := result.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("val")) {
		t.Fatalf("value = %q, want %q", kv.Value, "val")
	}
}

// ---------------------------------------------------------------------------
// Update - Transaction notifications
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_Transaction_NotifiesAllOps(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Watch both keys that will be written in the transaction.
	w1, err := hub.Watch(context.Background(), "txk1", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch txk1: %v", err)
	}
	w2, err := hub.Watch(context.Background(), "txk2", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch txk2: %v", err)
	}

	// Build a transaction with empty compares (always succeeds) and 2 put ops.
	op1 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txk1"), []byte("v1"), nil, 0))
	op2 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txk2"), []byte("v2"), nil, 0))
	txnCmd := mustEncode(protocol.EncodeTransaction(nil, [][]byte{op1, op2}, nil))
	entries := []sm.Entry{{Index: 1, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify both watchers received their events.
	events1 := receiveEvents(t, w1)
	if len(events1) != 1 {
		t.Fatalf("w1: expected 1 event, got %d", len(events1))
	}
	if events1[0].Type != service.EventPut {
		t.Fatalf("w1: expected EventPut, got %d", events1[0].Type)
	}
	if !bytes.Equal(events1[0].Kv.Key, []byte("txk1")) {
		t.Fatalf("w1: kv.Key = %q, want %q", events1[0].Kv.Key, "txk1")
	}
	if !bytes.Equal(events1[0].Kv.Value, []byte("v1")) {
		t.Fatalf("w1: kv.Value = %q, want %q", events1[0].Kv.Value, "v1")
	}

	events2 := receiveEvents(t, w2)
	if len(events2) != 1 {
		t.Fatalf("w2: expected 1 event, got %d", len(events2))
	}
	if events2[0].Type != service.EventPut {
		t.Fatalf("w2: expected EventPut, got %d", events2[0].Type)
	}
	if !bytes.Equal(events2[0].Kv.Key, []byte("txk2")) {
		t.Fatalf("w2: kv.Key = %q, want %q", events2[0].Kv.Key, "txk2")
	}
	if !bytes.Equal(events2[0].Kv.Value, []byte("v2")) {
		t.Fatalf("w2: kv.Value = %q, want %q", events2[0].Kv.Value, "v2")
	}
}

// ---------------------------------------------------------------------------
// Update - Batch notifications
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_Batch_NotifiesAllOps(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Watch both keys.
	w1, err := hub.Watch(context.Background(), "bk1", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch bk1: %v", err)
	}
	w2, err := hub.Watch(context.Background(), "bk2", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch bk2: %v", err)
	}

	// Build a batch with 2 put operations.
	op1 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk1"), []byte("bv1"), nil, 0))
	op2 := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk2"), []byte("bv2"), nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{op1, op2}))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify both watchers received their events.
	events1 := receiveEvents(t, w1)
	if len(events1) != 1 {
		t.Fatalf("w1: expected 1 event, got %d", len(events1))
	}
	if events1[0].Type != service.EventPut {
		t.Fatalf("w1: expected EventPut, got %d", events1[0].Type)
	}
	if !bytes.Equal(events1[0].Kv.Key, []byte("bk1")) {
		t.Fatalf("w1: kv.Key = %q, want %q", events1[0].Kv.Key, "bk1")
	}
	if !bytes.Equal(events1[0].Kv.Value, []byte("bv1")) {
		t.Fatalf("w1: kv.Value = %q, want %q", events1[0].Kv.Value, "bv1")
	}

	events2 := receiveEvents(t, w2)
	if len(events2) != 1 {
		t.Fatalf("w2: expected 1 event, got %d", len(events2))
	}
	if events2[0].Type != service.EventPut {
		t.Fatalf("w2: expected EventPut, got %d", events2[0].Type)
	}
	if !bytes.Equal(events2[0].Kv.Key, []byte("bk2")) {
		t.Fatalf("w2: kv.Key = %q, want %q", events2[0].Kv.Key, "bk2")
	}
	if !bytes.Equal(events2[0].Kv.Value, []byte("bv2")) {
		t.Fatalf("w2: kv.Value = %q, want %q", events2[0].Kv.Value, "bv2")
	}
}

// ---------------------------------------------------------------------------
// Update - Compact produces no notifications
// ---------------------------------------------------------------------------

func TestMVCCWatch_Update_Compact_NoNotifications(t *testing.T) {
	s, hub := newTestMVCCWithHub(t)

	// Seed data to create revisions 1 and 2.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("ckey"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("ckey"), []byte("v2"), 0),
	})

	// Watch the key. Any compact notification would appear here.
	w, err := hub.Watch(context.Background(), "ckey", service.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Compact up to revision 1.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccCompactEntry(3, 1),
	})

	// Verify no events were delivered.
	assertNoEvents(t, w)

	// Verify the state machine still works by doing a lookup.
	result, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("ckey")})
	if err != nil {
		t.Fatalf("Lookup after compact: %v", err)
	}
	kv := result.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("v2")) {
		t.Fatalf("value after compact = %q, want %q", kv.Value, "v2")
	}
}
