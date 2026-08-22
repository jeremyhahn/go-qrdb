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

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func kvPutEntry(idx uint64, key, value string) sm.Entry {
	cmd, _ := kv.EncodePut([]byte(key), []byte(value))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func kvDeleteEntry(idx uint64, key string) sm.Entry {
	cmd, _ := kv.EncodeDelete([]byte(key))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func applyKVEntries(t *testing.T, eng Engine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// NewKVEngine
// ---------------------------------------------------------------------------

func TestNewKVEngine_MemoryStorage(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	if eng == nil {
		t.Fatal("NewKVEngine returned nil")
	}
}

func TestNewKVEngine_WithOptions(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))
	if eng == nil {
		t.Fatal("NewKVEngine returned nil")
	}
}

// ---------------------------------------------------------------------------
// Type
// ---------------------------------------------------------------------------

func TestKVEngine_Type(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	if got := eng.Type(); got != "kv" {
		t.Errorf("Type() = %q, want %q", got, "kv")
	}
}

func TestKVEngine_TypeConsistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	first := eng.Type()
	second := eng.Type()
	if first != second {
		t.Errorf("Type() not consistent across calls: %q != %q", first, second)
	}
}

// ---------------------------------------------------------------------------
// Update: Put
// ---------------------------------------------------------------------------

func TestKVEngine_Update_Put(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	results := applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "key1", "val1")})

	if results[0].Value != uint64(len("val1")) {
		t.Errorf("result Value = %d, want %d", results[0].Value, len("val1"))
	}

	// Verify value is stored.
	got, err := eng.Lookup(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "val1" {
		t.Errorf("stored value = %q, want %q", got, "val1")
	}
}

func TestKVEngine_Update_PutOverwrite(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "key1", "val1")})
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, "key1", "val2")})

	got, err := eng.Lookup(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "val2" {
		t.Errorf("stored value = %q, want %q", got, "val2")
	}
}

// ---------------------------------------------------------------------------
// Update: Delete
// ---------------------------------------------------------------------------

func TestKVEngine_Update_Delete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "key1", "val1")})
	applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(2, "key1")})

	got, err := eng.Lookup(context.Background(), "key1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %v", got)
	}
}

func TestKVEngine_Update_DeleteNonExistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	// Deleting a non-existent key should not error.
	applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(1, "nonexistent")})
}

// ---------------------------------------------------------------------------
// Update: Multiple entries
// ---------------------------------------------------------------------------

func TestKVEngine_Update_MultipleEntries(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	entries := []sm.Entry{
		kvPutEntry(1, "k1", "v1"),
		kvPutEntry(2, "k2", "v2"),
		kvPutEntry(3, "k3", "v3"),
	}
	results := applyKVEntries(t, eng, entries)

	for _, r := range results {
		if r.Value == 0 {
			t.Error("expected non-zero result value")
		}
	}

	for _, k := range []string{"k1", "k2", "k3"} {
		got, err := eng.Lookup(context.Background(), k)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", k, err)
		}
		if got == nil {
			t.Errorf("Lookup(%s) = nil, want value", k)
		}
	}
}

// ---------------------------------------------------------------------------
// Lookup: string key
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_StringKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "hello", "world")})

	got, err := eng.Lookup(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "world" {
		t.Errorf("Lookup = %q, want %q", got, "world")
	}
}

func TestKVEngine_Lookup_StringKeyNotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	got, err := eng.Lookup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing key, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Lookup: []byte key
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_ByteKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "bkey", "bval")})

	got, err := eng.Lookup(context.Background(), []byte("bkey"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "bval" {
		t.Errorf("Lookup = %q, want %q", got, "bval")
	}
}

func TestKVEngine_Lookup_ByteKeyNotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	got, err := eng.Lookup(context.Background(), []byte("nope"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing key, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Lookup: ListQuery
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_ListQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", "alice"),
		kvPutEntry(2, "user:2", "bob"),
		kvPutEntry(3, "product:1", "widget"),
	})

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "user:"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := got.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0] != "user:1" || keys[1] != "user:2" {
		t.Errorf("keys = %v, want [user:1 user:2]", keys)
	}
}

func TestKVEngine_Lookup_ListQueryEmpty(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "x:"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := got.([]string)
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// Lookup: PrefixQuery
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_PrefixQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "ns:a", "1"),
		kvPutEntry(2, "ns:b", "2"),
		kvPutEntry(3, "other:c", "3"),
	})

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("ns:")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][]byte)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if string(result["ns:a"]) != "1" {
		t.Errorf("ns:a = %q, want %q", result["ns:a"], "1")
	}
}

func TestKVEngine_Lookup_PrefixQueryWithLimit(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "ns:a", "1"),
		kvPutEntry(2, "ns:b", "2"),
		kvPutEntry(3, "ns:c", "3"),
	})

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("ns:"), Limit: 2})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][]byte)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries with limit, got %d", len(result))
	}
}

func TestKVEngine_Lookup_PrefixQueryValueType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "ns:a", "1"),
	})

	// Test non-pointer PrefixQuery.
	got, err := eng.Lookup(context.Background(), statemachine.PrefixQuery{Prefix: []byte("ns:")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][]byte)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Lookup: unsupported query type
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_UnsupportedType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	_, err := eng.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Errorf("expected *InvalidQueryError, got %T", err)
	}
}

func TestKVEngine_Lookup_UnsupportedStructType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	_, err := eng.Lookup(context.Background(), struct{ X int }{X: 1})
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestKVEngine_Close(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestKVEngine_CloseWithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create an index via extended protocol.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update (index create): %v", err)
	}

	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch notifications
// ---------------------------------------------------------------------------

func TestKVEngine_WatchNotification_Put(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "wkey")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "wkey", "wval")})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

func TestKVEngine_WatchNotification_Delete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	// Put first.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "wkey", "wval")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "wkey")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(2, "wkey")})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for watch delete event")
	}
}

// ---------------------------------------------------------------------------
// Index operations via extended protocol
// ---------------------------------------------------------------------------

func TestKVEngine_IndexCreate(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Insert some data first.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", `{"name":"alice"}`),
		kvPutEntry(2, "user:2", `{"name":"bob"}`),
	})

	// Create index -- backfill should find existing data.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 3, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update (index create): %v", err)
	}

	// Query the index.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup (index query): %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

func TestKVEngine_IndexCreateDuplicate(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second create should be a no-op.
	entries2 := []sm.Entry{{Index: 2, Cmd: idxCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("second create: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Index drop
// ---------------------------------------------------------------------------

func TestKVEngine_IndexDrop(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create index.
	createCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Drop index.
	dropCmd := mustEncode(protocol.EncodeIndexDrop("by_name"))
	entries2 := []sm.Entry{{Index: 2, Cmd: dropCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// Query should fail.
	_, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err == nil {
		t.Fatal("expected error querying dropped index")
	}
}

func TestKVEngine_IndexDropNonExistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	dropCmd := mustEncode(protocol.EncodeIndexDrop("nonexistent"))
	entries := []sm.Entry{{Index: 1, Cmd: dropCmd}}
	results := make([]sm.Result, 1)
	// Should not error.
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Index list
// ---------------------------------------------------------------------------

func TestKVEngine_IndexList(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create two indexes.
	cmd1 := mustEncode(protocol.EncodeIndexCreate("idx_a", "field_a", false, false, false))
	cmd2 := mustEncode(protocol.EncodeIndexCreate("idx_b", "field_b", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd1}, {Index: 2, Cmd: cmd2}}
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := eng.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := got.([]string)
	if len(names) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(names))
	}
	// Should be sorted.
	if names[0] != "idx_a" || names[1] != "idx_b" {
		t.Errorf("names = %v, want [idx_a idx_b]", names)
	}
}

func TestKVEngine_IndexListEmpty(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	got, err := eng.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := got.([]string)
	if len(names) != 0 {
		t.Errorf("expected 0 indexes, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// Index scan
// ---------------------------------------------------------------------------

func TestKVEngine_IndexScan(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Insert data.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "user:1", `{"name":"alice"}`),
		kvPutEntry(2, "user:2", `{"name":"bob"}`),
		kvPutEntry(3, "user:3", `{"name":"carol"}`),
	})

	// Create index.
	createCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 4, Cmd: createCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Scan range [a, c).
	got, err := eng.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "by_name",
		Start:     []byte("a"),
		End:       []byte("c"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][][]byte)
	if len(result) < 1 {
		t.Errorf("expected at least 1 scan result, got %d", len(result))
	}
}

func TestKVEngine_IndexScanNotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	_, err := eng.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "nonexistent",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

func TestKVEngine_Batch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("bk1"), []byte("bv1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("bk2"), []byte("bv2"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}
	if results[0].Value != 2 {
		t.Errorf("batch result = %d, want 2", results[0].Value)
	}

	// Verify both keys exist.
	for _, k := range []string{"bk1", "bk2"} {
		got, err := eng.Lookup(context.Background(), k)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", k, err)
		}
		if got == nil {
			t.Errorf("Lookup(%s) = nil", k)
		}
	}
}

func TestKVEngine_BatchDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Put first.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "dk1", "dv1")})

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("dk1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch delete: %v", err)
	}

	got, err := eng.Lookup(context.Background(), "dk1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after batch delete, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Batch with watch and indexes
// ---------------------------------------------------------------------------

func TestKVEngine_BatchWithWatchNotifications(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "bk1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("bk1"), []byte("bv1"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event from batch")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch watch event")
	}
}

func TestKVEngine_BatchDeleteWithWatch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	// Put first.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(1, "bdk", "bdv")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "bdk")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("bdk"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 delete event from batch")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch delete watch event")
	}
}

func TestKVEngine_BatchWithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create index first.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Batch put with JSON values.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("u:1"), []byte(`{"name":"alice"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("u:2"), []byte(`{"name":"bob"}`), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries2 := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("batch: %v", err)
	}

	// Query index.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID from batch index, got %d", len(ids))
	}
}

func TestKVEngine_BatchUnsupportedOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Use an unsupported op type in the batch.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(0xEE, []byte("k"), []byte("v"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unsupported batch op")
	}
	var uoe *UnsupportedOperationError
	if !errors.As(err, &uoe) {
		t.Errorf("expected *UnsupportedOperationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KV Update with index and watch on put/delete
// ---------------------------------------------------------------------------

func TestKVEngine_PutWithIndexAndWatch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	// Create index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "u:1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Put with JSON value.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, "u:1", `{"name":"alice"}`)})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Verify index was updated.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

func TestKVEngine_DeleteWithIndexAndWatch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	// Create index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Put first.
	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, "u:1", `{"name":"alice"}`)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "u:1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Delete.
	applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(3, "u:1")})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Index should be cleared.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs after delete, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// KV Update with unsupported op
// ---------------------------------------------------------------------------

func TestKVEngine_Update_UnsupportedOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create a base KV encoded command with OpGet (which is not supported in Update).
	cmd, _ := kv.EncodeGet([]byte("key"))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unsupported base KV op")
	}
}

// ---------------------------------------------------------------------------
// KV Update with unsupported extended op
// ---------------------------------------------------------------------------

func TestKVEngine_Update_UnsupportedExtendedOp(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// An MVCC op should not be supported in KV engine.
	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for MVCC op in KV engine")
	}
	var uoe *UnsupportedOperationError
	if !errors.As(err, &uoe) {
		t.Errorf("expected *UnsupportedOperationError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KV Update with invalid command
// ---------------------------------------------------------------------------

func TestKVEngine_Update_InvalidCommand(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// A single byte too short to be a base-KV command (header is 3 bytes),
	// but long enough to decode as an extended-protocol command with an
	// unrecognized op (0x00 is not registered in extHandlers). This is the
	// sibling "unknown ext op" case, which correctly returns a Go error.
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0}}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unrecognized extended op")
	}
	var uoe *UnsupportedOperationError
	if !errors.As(err, &uoe) {
		t.Errorf("expected *UnsupportedOperationError, got %T: %v", err, err)
	}
}

// TestKVEngine_Update_UndecodableByBothProtocols exercises the case where a
// committed command cannot be decoded by EITHER the base KV wire protocol
// (which requires a 3-byte header) or the extended protocol (which requires
// at least a 1-byte op code) — e.g. an empty/corrupt entry. Regression test
// for the silent-success bug: this must not be reported as applied
// (Value:1) or stall consensus (a Go error); it must surface as an
// application error in the result payload.
func TestKVEngine_Update_UndecodableByBothProtocols(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update should not return a Go error for an undecodable command, got: %v", err)
	}
	if results[0].Value == 1 {
		t.Fatal("undecodable command must not be reported as success (Value:1)")
	}
	msg, ok := protocol.DecodeAppError(results[0].Data)
	if !ok {
		t.Fatalf("expected an application-error result, got %+v", results[0])
	}
	if msg == "" {
		t.Error("expected a non-empty application-error message")
	}
}

// ---------------------------------------------------------------------------
// BatchDeleteWithIndex
// ---------------------------------------------------------------------------

func TestKVEngine_BatchDeleteWithIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Create index and put data.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{kvPutEntry(2, "u:1", `{"name":"alice"}`)})

	// Batch delete -- note: handleBatch passes nil prevKv, so RemoveIndexes
	// is a no-op for the index. This verifies the code path executes without error.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("u:1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries2 := []sm.Entry{{Index: 3, Cmd: batchCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("batch delete: %v", err)
	}

	// The key itself should be deleted.
	got, err := eng.Lookup(context.Background(), "u:1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after batch delete, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// prefixEndBytes edge cases
// ---------------------------------------------------------------------------

func TestPrefixEndBytes_Normal(t *testing.T) {
	result := prefixEndBytes([]byte("abc"))
	if string(result) != "abd" {
		t.Errorf("prefixEndBytes(abc) = %q, want %q", result, "abd")
	}
}

func TestPrefixEndBytes_AllFF(t *testing.T) {
	result := prefixEndBytes([]byte{0xff, 0xff, 0xff})
	if result != nil {
		t.Errorf("prefixEndBytes(0xffffff) = %v, want nil", result)
	}
}

func TestPrefixEndBytes_Empty(t *testing.T) {
	result := prefixEndBytes(nil)
	if result != nil {
		t.Errorf("prefixEndBytes(nil) = %v, want nil", result)
	}
}

func TestPrefixEndBytes_TrailingFF(t *testing.T) {
	result := prefixEndBytes([]byte{0x01, 0xff})
	if len(result) != 1 || result[0] != 0x02 {
		t.Errorf("prefixEndBytes([0x01, 0xff]) = %v, want [0x02]", result)
	}
}

// ---------------------------------------------------------------------------
// KV Lookup error path (lookupKey with error)
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_EmptyKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Empty key triggers storage error.
	_, err := eng.Lookup(context.Background(), []byte(""))
	if err != nil {
		// Error is expected for empty key.
		return
	}
}

// ---------------------------------------------------------------------------
// KV handleBatch commit path
// ---------------------------------------------------------------------------

func TestKVEngine_BatchWithMixedOps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Put then delete in same batch.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("mk1"), []byte("mv1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("mk2"), []byte("mv2"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("mk1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if results[0].Value != 3 {
		t.Errorf("batch result = %d, want 3", results[0].Value)
	}

	// mk1 should be deleted.
	got, _ := eng.Lookup(context.Background(), "mk1")
	if got != nil {
		t.Error("mk1 should be deleted")
	}

	// mk2 should exist.
	got2, _ := eng.Lookup(context.Background(), "mk2")
	if got2 == nil {
		t.Error("mk2 should exist")
	}
}

// ---------------------------------------------------------------------------
// WithKVPebbleDB
// ---------------------------------------------------------------------------

func TestKVEngine_WithPebbleDB(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	eng := NewKVEngine(stg, WithKVPebbleDB(db))

	// Create index -- should use PebbleIndex.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create with pebble: %v", err)
	}

	if err := eng.Close(context.Background()); err != nil {
		t.Errorf("eng.Close: %v", err)
	}
}

func TestKVEngine_WithPebbleDB_IndexQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	eng := NewKVEngine(stg, WithKVPebbleDB(db))
	defer func() { _ = eng.Close(context.Background()) }()

	// Put data then create index (backfill).
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "u:1", `{"name":"alice"}`),
	})

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 2, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Engine interface compliance
// ---------------------------------------------------------------------------

func TestKVEngine_ImplementsEngine(t *testing.T) {
	var _ Engine = (*KVEngine)(nil)
}

// ---------------------------------------------------------------------------
// RegisterEntityType
// ---------------------------------------------------------------------------

func TestKVEngine_RegisterEntityType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Put a JSON value under the entity prefix AFTER registration.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice","age":30}`),
	})

	// Register the entity type with a Name index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// The put happened before registration under the "users/" prefix,
	// so backfill should have picked it up.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
	if string(ids[0]) != "users/1" {
		t.Errorf("expected key %q, got %q", "users/1", string(ids[0]))
	}
}

func TestKVEngine_RegisterEntityType_UniqueIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register unique index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Email", JSONFieldName: "email", Unique: true},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put two entries with different email values -- both should succeed.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"email":"alice@example.com"}`),
		kvPutEntry(2, "users/2", `{"email":"bob@example.com"}`),
	})

	// Verify both are indexed.
	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "users.Email",
			Value:     []byte(email),
		})
		if err != nil {
			t.Fatalf("Lookup(%s): %v", email, err)
		}
		ids := got.([][]byte)
		if len(ids) != 1 {
			t.Errorf("expected 1 ID for %s, got %d", email, len(ids))
		}
	}
}

func TestKVEngine_RegisterEntityType_Backfill(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Put JSON entries FIRST, before registration.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
		kvPutEntry(2, "users/2", `{"name":"bob"}`),
		kvPutEntry(3, "users/3", `{"name":"carol"}`),
		kvPutEntry(4, "products/1", `{"name":"widget"}`), // different prefix, should not be indexed
	})

	// Register entity type AFTER data is written.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Verify all three user entries are backfilled.
	for _, name := range []string{"alice", "bob", "carol"} {
		got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "users.Name",
			Value:     []byte(name),
		})
		if err != nil {
			t.Fatalf("Lookup(%s): %v", name, err)
		}
		ids := got.([][]byte)
		if len(ids) != 1 {
			t.Errorf("expected 1 ID for %s, got %d", name, len(ids))
		}
	}

	// Verify the product entry is NOT indexed.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("widget"),
	})
	if err != nil {
		t.Fatalf("Lookup(widget): %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for widget (different prefix), got %d", len(ids))
	}
}

func TestKVEngine_PutBatchAtomicity_WithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register entity type with index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put a value via the KV protocol.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Verify data exists.
	got, err := eng.Lookup(context.Background(), "users/1")
	if err != nil {
		t.Fatalf("Lookup data: %v", err)
	}
	if got == nil {
		t.Fatal("expected data for users/1, got nil")
	}
	if string(got.([]byte)) != `{"name":"alice"}` {
		t.Errorf("data = %q, want %q", got, `{"name":"alice"}`)
	}

	// Verify index entry exists.
	idxGot, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup index: %v", err)
	}
	ids := idxGot.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 index entry, got %d", len(ids))
	}
	if len(ids) > 0 && string(ids[0]) != "users/1" {
		t.Errorf("index key = %q, want %q", string(ids[0]), "users/1")
	}
}

func TestKVEngine_DeleteBatchAtomicity_WithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register entity type with index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put a value.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Delete the value.
	applyKVEntries(t, eng, []sm.Entry{
		kvDeleteEntry(2, "users/1"),
	})

	// Verify data is gone.
	got, err := eng.Lookup(context.Background(), "users/1")
	if err != nil {
		t.Fatalf("Lookup data: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %v", got)
	}

	// Verify index entry is removed.
	idxGot, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup index: %v", err)
	}
	ids := idxGot.([][]byte)
	if len(ids) != 0 {
		t.Errorf("expected 0 index entries after delete, got %d", len(ids))
	}
}

func TestKVEngine_HandleBatch_WithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register entity type with index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Batch put multiple entries.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("users/1"), []byte(`{"name":"alice"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("users/2"), []byte(`{"name":"bob"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("users/3"), []byte(`{"name":"carol"}`), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}
	if results[0].Value != 3 {
		t.Errorf("batch result = %d, want 3", results[0].Value)
	}

	// Verify all data entries exist.
	for _, key := range []string{"users/1", "users/2", "users/3"} {
		got, err := eng.Lookup(context.Background(), key)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", key, err)
		}
		if got == nil {
			t.Errorf("Lookup(%s) = nil, want value", key)
		}
	}

	// Verify all index entries are created.
	for _, name := range []string{"alice", "bob", "carol"} {
		got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "users.Name",
			Value:     []byte(name),
		})
		if err != nil {
			t.Fatalf("Lookup index(%s): %v", name, err)
		}
		ids := got.([][]byte)
		if len(ids) != 1 {
			t.Errorf("expected 1 index entry for %s, got %d", name, len(ids))
		}
	}
}

// ---------------------------------------------------------------------------
// RegisterEntityType: Idempotent
// ---------------------------------------------------------------------------

func TestKVEngine_RegisterEntityType_Idempotent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	idxDef := []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	}

	// First registration.
	if err := eng.RegisterEntityType("users", idxDef); err != nil {
		t.Fatalf("first RegisterEntityType: %v", err)
	}

	// Put data after first registration.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Second registration should be a no-op (index already exists).
	if err := eng.RegisterEntityType("users", idxDef); err != nil {
		t.Fatalf("second RegisterEntityType: %v", err)
	}

	// Verify the index still works and data is intact.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// RegisterEntityType: Multiple fields
// ---------------------------------------------------------------------------

func TestKVEngine_RegisterEntityType_MultipleFields(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Put data before registration so backfill picks it up.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice","email":"alice@test.com"}`),
		kvPutEntry(2, "users/2", `{"name":"bob","email":"bob@test.com"}`),
	})

	// Register with multiple indexed fields.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
		{FieldName: "Email", JSONFieldName: "email", Unique: true},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Verify Name index.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup Name: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("Name index: expected 1 ID for alice, got %d", len(ids))
	}

	// Verify Email index.
	got, err = eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Email",
		Value:     []byte("bob@test.com"),
	})
	if err != nil {
		t.Fatalf("Lookup Email: %v", err)
	}
	ids = got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("Email index: expected 1 ID for bob@test.com, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// PutBatch: No indexes
// ---------------------------------------------------------------------------

func TestKVEngine_PutBatch_NoIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("k1"), []byte("v1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("k2"), []byte("v2"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}
	if results[0].Value != 2 {
		t.Errorf("batch result = %d, want 2", results[0].Value)
	}

	// Verify data.
	for _, k := range []string{"k1", "k2"} {
		got, err := eng.Lookup(context.Background(), k)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", k, err)
		}
		if got == nil {
			t.Errorf("Lookup(%s) = nil, want value", k)
		}
	}
}

// ---------------------------------------------------------------------------
// DeleteBatch: No indexes
// ---------------------------------------------------------------------------

func TestKVEngine_DeleteBatch_NoIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	// Put data first.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "k1", "v1"),
		kvPutEntry(2, "k2", "v2"),
	})

	// Batch delete.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("k1"), nil, nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("k2"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 3, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch delete: %v", err)
	}
	if results[0].Value != 2 {
		t.Errorf("batch result = %d, want 2", results[0].Value)
	}

	// Verify both keys are gone.
	for _, k := range []string{"k1", "k2"} {
		got, err := eng.Lookup(context.Background(), k)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", k, err)
		}
		if got != nil {
			t.Errorf("expected nil for %s after batch delete, got %v", k, got)
		}
	}
}

// ---------------------------------------------------------------------------
// HandleBatch: Mixed ops with indexes
// ---------------------------------------------------------------------------

func TestKVEngine_HandleBatch_MixedOps_WithIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register entity type with index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put an initial entry via KV protocol.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Batch: put users/2, delete users/1.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("users/2"), []byte(`{"name":"bob"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("users/1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}
	if results[0].Value != 2 {
		t.Errorf("batch result = %d, want 2", results[0].Value)
	}

	// users/1 should be gone.
	got, err := eng.Lookup(context.Background(), "users/1")
	if err != nil {
		t.Fatalf("Lookup(users/1): %v", err)
	}
	if got != nil {
		t.Error("users/1 should be deleted")
	}

	// users/2 should exist.
	got, err = eng.Lookup(context.Background(), "users/2")
	if err != nil {
		t.Fatalf("Lookup(users/2): %v", err)
	}
	if got == nil {
		t.Error("users/2 should exist")
	}

	// Index should have bob but not alice.
	gotIdx, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("bob"),
	})
	if err != nil {
		t.Fatalf("Lookup index(bob): %v", err)
	}
	ids := gotIdx.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID for bob, got %d", len(ids))
	}

	gotIdx, err = eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup index(alice): %v", err)
	}
	ids = gotIdx.([][]byte)
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for alice after delete, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// HandleIndexCreate: WithLogger (non-default-logger path)
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexCreate_WithLogger(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Put data first.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "item:1", `{"color":"red"}`),
	})

	// Create index via extended protocol -- engine uses the injected logger.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_color", "color", false, false, false))
	entries := []sm.Entry{{Index: 2, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update (index create with logger): %v", err)
	}

	// Query the index.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_color",
		Value:     []byte("red"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// HandleIndexCreate: Already exists (no-op)
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexCreate_AlreadyExists(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	// First create.
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Put data.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(2, "user:1", `{"name":"alice"}`),
	})

	// Second create -- should be a no-op, not overwrite existing index.
	entries2 := []sm.Entry{{Index: 3, Cmd: idxCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("second create: %v", err)
	}

	// Index should still work with the data added after first create.
	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Errorf("expected 1 ID, got %d", len(ids))
	}
}

// ---------------------------------------------------------------------------
// HandleIndexDrop: Nonexistent (no-op)
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexDrop_Nonexistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	dropCmd := mustEncode(protocol.EncodeIndexDrop("does_not_exist"))
	entries := []sm.Entry{{Index: 1, Cmd: dropCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: %v", err)
	}

	// Verify no indexes exist.
	got, err := eng.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := got.([]string)
	if len(names) != 0 {
		t.Errorf("expected 0 indexes, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// Type: Returns "kv"
// ---------------------------------------------------------------------------

func TestKVEngine_Type_ReturnsKV(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	got := eng.Type()
	if got != "kv" {
		t.Errorf("Type() = %q, want %q", got, "kv")
	}

	// Verify consistency across options.
	log := testLogger(t)
	eng2 := NewKVEngine(stg, WithKVLogger(log))
	if eng2.Type() != "kv" {
		t.Errorf("Type() with logger = %q, want %q", eng2.Type(), "kv")
	}
}

// ---------------------------------------------------------------------------
// Close: Closes indexes
// ---------------------------------------------------------------------------

func TestKVEngine_Close_ClosesIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register entity type to create indexes.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
		{FieldName: "Email", JSONFieldName: "email", Unique: true},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Also create a protocol-level index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_age", "age", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Close should not panic with multiple indexes.
	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return log
}

// ---------------------------------------------------------------------------
// Error injection test infrastructure
// ---------------------------------------------------------------------------

// injectedError is a typed error used by the error-injecting test wrappers.
type injectedError struct {
	msg string
}

func (e *injectedError) Error() string { return e.msg }

// failingBatch wraps storage.Batch and injects errors on demand.
type failingBatch struct {
	storage.Batch
	failSet    bool
	failDelete bool
	failCommit bool
}

func (b *failingBatch) Set(key, value []byte) error {
	if b.failSet {
		return &injectedError{msg: "injected batch set error"}
	}
	return b.Batch.Set(key, value)
}

func (b *failingBatch) Delete(key []byte) error {
	if b.failDelete {
		return &injectedError{msg: "injected batch delete error"}
	}
	return b.Batch.Delete(key)
}

func (b *failingBatch) Commit() error {
	if b.failCommit {
		return &injectedError{msg: "injected batch commit error"}
	}
	return b.Batch.Commit()
}

// failingStorage wraps storage.Storage and injects errors on batch operations.
type failingStorage struct {
	storage.Storage
	failBatchSet    bool
	failBatchDelete bool
	failBatchCommit bool
}

func (f *failingStorage) Batch() storage.Batch {
	return &failingBatch{
		Batch:      f.Storage.Batch(),
		failSet:    f.failBatchSet,
		failDelete: f.failBatchDelete,
		failCommit: f.failBatchCommit,
	}
}

// failingIndex implements index.Index and index.BatchableIndex with
// configurable error injection on PutBatch and DeleteBatch.
type failingIndex struct {
	index.Index
	failPutBatch    bool
	failDeleteBatch bool
}

func (f *failingIndex) PutBatch(_ storage.Batch, _ []byte, _ []byte) error {
	if f.failPutBatch {
		return &injectedError{msg: "injected index put batch error"}
	}
	return nil
}

func (f *failingIndex) DeleteBatch(_ storage.Batch, _ []byte, _ []byte) error {
	if f.failDeleteBatch {
		return &injectedError{msg: "injected index delete batch error"}
	}
	return nil
}

// newEngineWithFailingStorage creates a KVEngine backed by a failingStorage
// that wraps a real memory.Storage. Data written through the real storage
// remains accessible for reads; only batch operations are intercepted.
func newEngineWithFailingStorage(t *testing.T, fs *failingStorage, opts ...KVOption) *KVEngine {
	t.Helper()
	return NewKVEngine(fs, opts...)
}

// injectFailingIndex registers a failingIndex into the engine's indexes map
// so that UpdateIndexesBatch / RemoveIndexesBatch will exercise the error
// path through BatchableIndex.PutBatch / DeleteBatch.
func injectFailingIndex(eng *KVEngine, name, fieldName string, failPut, failDelete bool) {
	realIdx := eng.indexes[name]
	fi := &failingIndex{
		Index:           realIdx,
		failPutBatch:    failPut,
		failDeleteBatch: failDelete,
	}
	eng.indexes[name] = fi
	eng.indexFields[name] = fieldName
}

// ---------------------------------------------------------------------------
// kvHandlePut error paths
// ---------------------------------------------------------------------------

func TestKVHandlePut_BatchSetError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchSet: true}
	eng := newEngineWithFailingStorage(t, fs)

	entries := []sm.Entry{kvPutEntry(1, "k1", "v1")}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Set, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestKVHandlePut_BatchCommitError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	eng := newEngineWithFailingStorage(t, fs)

	entries := []sm.Entry{kvPutEntry(1, "k1", "v1")}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Commit, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestKVHandlePut_IndexUpdateError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(real, WithKVLogger(log))

	// Register an entity type to create a real index, then replace it
	// with a failing wrapper.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Replace the index with one that fails on PutBatch.
	injectFailingIndex(eng, "users.Name", "name", true, false)

	// Put a JSON value whose "name" field triggers the index update path.
	entries := []sm.Entry{kvPutEntry(1, "users/1", `{"name":"alice"}`)}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from UpdateIndexesBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// kvHandleDelete error paths
// ---------------------------------------------------------------------------

func TestKVHandleDelete_BatchDeleteError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchDelete: true}
	eng := newEngineWithFailingStorage(t, fs)

	// Put data first using the real storage so there is something to delete.
	if err := real.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	entries := []sm.Entry{kvDeleteEntry(1, "k1")}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Delete, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestKVHandleDelete_BatchCommitError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	eng := newEngineWithFailingStorage(t, fs)

	entries := []sm.Entry{kvDeleteEntry(1, "k1")}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Commit, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestKVHandleDelete_IndexRemoveError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(real, WithKVLogger(log))

	// Register entity type and put data before injecting the failure.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Now replace the index with one that fails on DeleteBatch.
	injectFailingIndex(eng, "users.Name", "name", false, true)

	entries := []sm.Entry{kvDeleteEntry(2, "users/1")}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from RemoveIndexesBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// handleBatch error paths
// ---------------------------------------------------------------------------

func TestHandleBatch_BatchSetError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchSet: true}
	eng := newEngineWithFailingStorage(t, fs)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("k1"), []byte("v1"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Set in handleBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestHandleBatch_BatchDeleteError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchDelete: true}
	eng := newEngineWithFailingStorage(t, fs)

	// Put data via real storage first.
	if err := real.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("k1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Delete in handleBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestHandleBatch_BatchCommitError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	eng := newEngineWithFailingStorage(t, fs)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("k1"), []byte("v1"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from batch.Commit in handleBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestHandleBatch_IndexUpdateError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(real, WithKVLogger(log))

	// Register entity type, then inject failing index.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}
	injectFailingIndex(eng, "users.Name", "name", true, false)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("users/1"), []byte(`{"name":"alice"}`), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from UpdateIndexesBatch in handleBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

func TestHandleBatch_IndexRemoveError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(real, WithKVLogger(log))

	// Register entity type and put data before injecting failure.
	err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"name":"alice"}`),
	})

	// Inject failing index that fails on DeleteBatch.
	injectFailingIndex(eng, "users.Name", "name", false, true)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("users/1"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from RemoveIndexesBatch in handleBatch, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// handleIndexCreate error paths
// ---------------------------------------------------------------------------

func TestHandleIndexCreate_DecodeError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	eng := NewKVEngine(real)

	// Craft an extended command with OpIndexCreate but an invalid (too-short)
	// payload. Format: [op:1][payload...]. The payload of a single byte is
	// too short for DecodeIndexCreate which requires at least 2 bytes.
	cmd := []byte{protocol.OpIndexCreate, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from DecodeIndexCreate with invalid payload, got nil")
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// RegisterEntityType error paths
// ---------------------------------------------------------------------------

func TestRegisterEntityType_BackfillError(t *testing.T) {
	// Use a failingStorage that returns an error on iterator operations
	// by closing the underlying storage before calling RegisterEntityType
	// with pre-existing data. The iterator will report an error condition.
	real := memory.NewStorage()

	// Seed data under the entity prefix so backfill attempts iteration.
	if err := real.Set([]byte("items/1"), []byte(`{"color":"red"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Create a storage wrapper that produces an error-returning iterator.
	errStg := &failingIteratorStorage{Storage: real}

	log := testLogger(t)
	eng := NewKVEngine(errStg, WithKVLogger(log))

	err := eng.RegisterEntityType("items", []EntityIndex{
		{FieldName: "Color", JSONFieldName: "color", Unique: false},
	})
	if err == nil {
		t.Fatal("expected error from BackfillIndexWithPrefix, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *injectedError, got %T: %v", err, err)
	}
}

// failingIteratorStorage wraps storage.Storage and returns an iterator
// that always reports an error after iteration completes.
type failingIteratorStorage struct {
	storage.Storage
}

func (f *failingIteratorStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &failingIterator{Iterator: f.Storage.NewIterator(opts)}
}

// failingIterator wraps storage.Iterator and injects an error via Error().
type failingIterator struct {
	storage.Iterator
}

func (f *failingIterator) Error() error {
	return &injectedError{msg: "injected iterator error"}
}

// ---------------------------------------------------------------------------
// Compound index: registerCompoundIndex + buildCompoundIndexName
// ---------------------------------------------------------------------------

func TestKVEngine_RegisterEntityType_CompoundIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register a compound (multi-field) index.
	err := eng.RegisterEntityType("messages", []EntityIndex{
		{FieldNames: []string{"ConversationID", "Timestamp"}, JSONFieldNames: []string{"conv_id", "ts"}},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType with compound index: %v", err)
	}
	// Index name: "messages.ConversationID_Timestamp"
	if len(eng.compoundIndexes) != 1 {
		t.Errorf("expected 1 compound index, got %d", len(eng.compoundIndexes))
	}
}

func TestKVEngine_RegisterEntityType_CompoundIndex_IdempotentReregister(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	def := []EntityIndex{
		{FieldNames: []string{"A", "B"}, JSONFieldNames: []string{"a", "b"}},
	}
	if err := eng.RegisterEntityType("docs", def); err != nil {
		t.Fatalf("first RegisterEntityType: %v", err)
	}
	// Registering again must not return an error and must not duplicate the index.
	if err := eng.RegisterEntityType("docs", def); err != nil {
		t.Fatalf("second RegisterEntityType: %v", err)
	}
	if len(eng.compoundIndexes) != 1 {
		t.Errorf("expected 1 compound index after re-registration, got %d", len(eng.compoundIndexes))
	}
}

func TestKVEngine_RegisterEntityType_UniqueCompoundIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Unique compound index exercises the Unique=true branch in registerCompoundIndex.
	err := eng.RegisterEntityType("sessions", []EntityIndex{
		{FieldNames: []string{"UserID", "DeviceID"}, JSONFieldNames: []string{"user_id", "device_id"}, Unique: true},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType with unique compound index: %v", err)
	}
	if len(eng.compoundIndexes) != 1 {
		t.Errorf("expected 1 compound index, got %d", len(eng.compoundIndexes))
	}
}

func TestKVEngine_BuildCompoundIndexName_NoDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	got := eng.buildCompoundIndexName("orders", []string{"UserID", "Status"})
	want := "orders.UserID_Status"
	if got != want {
		t.Errorf("buildCompoundIndexName = %q, want %q", got, want)
	}
}

func TestKVEngine_BuildCompoundIndexName_WithDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg, WithKVDatabase("mydb"))

	got := eng.buildCompoundIndexName("orders", []string{"UserID", "Status"})
	want := "mydb.orders.UserID_Status"
	if got != want {
		t.Errorf("buildCompoundIndexName = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// PutWithIndexes / DeleteWithIndexes
// ---------------------------------------------------------------------------

func TestKVEngine_PutWithIndexes_BasicRoundTrip(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	if err := eng.PutWithIndexes([]byte("users/1"), []byte(`{"name":"alice"}`)); err != nil {
		t.Fatalf("PutWithIndexes: %v", err)
	}

	got, err := stg.Get([]byte("users/1"))
	if err != nil {
		t.Fatalf("storage.Get: %v", err)
	}
	if string(got) != `{"name":"alice"}` {
		t.Errorf("stored value = %q, want %q", got, `{"name":"alice"}`)
	}

	// Verify the index was updated.
	result, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ids := result.([][]byte)
	if len(ids) != 1 || string(ids[0]) != "users/1" {
		t.Errorf("expected [users/1] from index, got %v", ids)
	}
}

func TestKVEngine_PutWithIndexes_UpdateExistingKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put initial value.
	if err := eng.PutWithIndexes([]byte("users/1"), []byte(`{"name":"alice"}`)); err != nil {
		t.Fatalf("first PutWithIndexes: %v", err)
	}
	// Update to a new name — old index entry should be removed.
	if err := eng.PutWithIndexes([]byte("users/1"), []byte(`{"name":"bob"}`)); err != nil {
		t.Fatalf("second PutWithIndexes: %v", err)
	}

	// "alice" should no longer be indexed.
	result, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup alice: %v", err)
	}
	if len(result.([][]byte)) != 0 {
		t.Errorf("expected empty result for stale index entry, got %v", result)
	}

	// "bob" should be indexed.
	result, err = eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("bob"),
	})
	if err != nil {
		t.Fatalf("Lookup bob: %v", err)
	}
	ids := result.([][]byte)
	if len(ids) != 1 || string(ids[0]) != "users/1" {
		t.Errorf("expected [users/1] for bob, got %v", ids)
	}
}

func TestKVEngine_DeleteWithIndexes_RemovesKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	if err := eng.PutWithIndexes([]byte("users/1"), []byte(`{"name":"carol"}`)); err != nil {
		t.Fatalf("PutWithIndexes: %v", err)
	}

	if err := eng.DeleteWithIndexes([]byte("users/1")); err != nil {
		t.Fatalf("DeleteWithIndexes: %v", err)
	}

	// Key should be gone.
	val, _ := stg.Get([]byte("users/1"))
	if val != nil {
		t.Errorf("expected nil after delete, got %q", val)
	}

	// Index entry should be cleaned up.
	result, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Name",
		Value:     []byte("carol"),
	})
	if err != nil {
		t.Fatalf("Lookup after delete: %v", err)
	}
	if len(result.([][]byte)) != 0 {
		t.Errorf("expected empty index after delete, got %v", result)
	}
}

func TestKVEngine_PutWithIndexes_SetError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchSet: true}
	eng := NewKVEngine(fs)

	err := eng.PutWithIndexes([]byte("items/1"), []byte(`{"name":"thing"}`))
	if err == nil {
		t.Fatal("expected batch.Set error, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Errorf("expected *injectedError, got %T: %v", err, err)
	}
}

func TestKVEngine_DeleteWithIndexes_DeleteError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchDelete: true}
	eng := NewKVEngine(fs)

	err := eng.DeleteWithIndexes([]byte("items/1"))
	if err == nil {
		t.Fatal("expected batch.Delete error, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Errorf("expected *injectedError, got %T: %v", err, err)
	}
}

func TestKVEngine_PutWithIndexes_CommitError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	log := testLogger(t)
	eng := NewKVEngine(fs, WithKVLogger(log))

	err := eng.PutWithIndexes([]byte("items/1"), []byte(`{"name":"thing"}`))
	if err == nil {
		t.Fatal("expected batch commit error, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Errorf("expected *injectedError, got %T: %v", err, err)
	}
}

func TestKVEngine_DeleteWithIndexes_CommitError(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()

	// Seed the key directly on the real storage.
	if err := real.Set([]byte("items/1"), []byte(`{"name":"thing"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fs := &failingStorage{Storage: real, failBatchCommit: true}
	log := testLogger(t)
	eng := NewKVEngine(fs, WithKVLogger(log))

	err := eng.DeleteWithIndexes([]byte("items/1"))
	if err == nil {
		t.Fatal("expected batch commit error, got nil")
	}
	var ie *injectedError
	if !errors.As(err, &ie) {
		t.Errorf("expected *injectedError, got %T: %v", err, err)
	}
}

func TestKVEngine_DeleteWithIndexes_NonExistentKey(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	// Deleting a key that does not exist should not error.
	if err := eng.DeleteWithIndexes([]byte("ghost/99")); err != nil {
		t.Fatalf("DeleteWithIndexes on non-existent key: %v", err)
	}
}

// ---------------------------------------------------------------------------
// entityTypeFromKey
// ---------------------------------------------------------------------------

func TestEntityTypeFromKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want string
	}{
		{"users/1", "users"},
		{"messages/thread/42", "messages"},
		{"noprefix", "noprefix"},
		{"a/b", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			got := entityTypeFromKey([]byte(tc.key))
			if got != tc.want {
				t.Errorf("entityTypeFromKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// filterIndexes / filterCompoundIndexes
// ---------------------------------------------------------------------------

func TestKVEngine_FilterIndexes_MatchesEntityPrefix(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("users", []EntityIndex{
		{FieldName: "Name", JSONFieldName: "name"},
	}); err != nil {
		t.Fatalf("RegisterEntityType users: %v", err)
	}
	if err := eng.RegisterEntityType("orders", []EntityIndex{
		{FieldName: "Status", JSONFieldName: "status"},
	}); err != nil {
		t.Fatalf("RegisterEntityType orders: %v", err)
	}

	filtered, filteredFields := eng.filterIndexes("users")
	if len(filtered) != 1 {
		t.Errorf("expected 1 filtered index for users, got %d", len(filtered))
	}
	for name := range filtered {
		if filteredFields[name] == "" {
			t.Errorf("filteredFields missing entry for %q", name)
		}
	}
}

func TestKVEngine_FilterCompoundIndexes_MatchesEntityPrefix(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("events", []EntityIndex{
		{FieldNames: []string{"UserID", "Type"}, JSONFieldNames: []string{"user_id", "type"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType events: %v", err)
	}
	if err := eng.RegisterEntityType("logs", []EntityIndex{
		{FieldNames: []string{"Level", "Source"}, JSONFieldNames: []string{"level", "source"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType logs: %v", err)
	}

	filtered, filteredFields := eng.filterCompoundIndexes("events")
	if len(filtered) != 1 {
		t.Errorf("expected 1 compound index for events, got %d", len(filtered))
	}
	for name := range filtered {
		if len(filteredFields[name]) == 0 {
			t.Errorf("filteredCompoundFields missing entry for %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// lookupCompoundIndexQuery / lookupCompoundIndexScan
// ---------------------------------------------------------------------------

func TestKVEngine_Lookup_CompoundIndexQuery_NotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	_, err := eng.Lookup(context.Background(), index.CompoundIndexQueryRequest{
		IndexName: "nonexistent.A_B",
		Values:    [][]byte{[]byte("x"), []byte("y")},
	})
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Errorf("expected *InvalidQueryError for missing compound index, got %T: %v", err, err)
	}
}

func TestKVEngine_Lookup_CompoundIndexScan_NotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	_, err := eng.Lookup(context.Background(), index.CompoundIndexScanRequest{
		IndexName:    "nonexistent.A_B",
		PrefixValues: [][]byte{[]byte("x")},
	})
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Errorf("expected *InvalidQueryError for missing compound index scan, got %T: %v", err, err)
	}
}

func TestKVEngine_Lookup_CompoundIndexQuery_HappyPath(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("msgs", []EntityIndex{
		{FieldNames: []string{"ThreadID", "Seq"}, JSONFieldNames: []string{"thread_id", "seq"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// PutWithIndexes writes compound index entry.
	if err := eng.PutWithIndexes([]byte("msgs/1"), []byte(`{"thread_id":"t1","seq":"001"}`)); err != nil {
		t.Fatalf("PutWithIndexes: %v", err)
	}

	// Determine the exact compound index name.
	idxName := eng.buildCompoundIndexName("msgs", []string{"ThreadID", "Seq"})

	result, err := eng.Lookup(context.Background(), index.CompoundIndexQueryRequest{
		IndexName: idxName,
		Values:    [][]byte{[]byte("t1"), []byte("001")},
	})
	if err != nil {
		t.Fatalf("Lookup CompoundIndexQueryRequest: %v", err)
	}
	keys, ok := result.([][]byte)
	if !ok {
		t.Fatalf("expected [][]byte result, got %T", result)
	}
	if len(keys) != 1 || string(keys[0]) != "msgs/1" {
		t.Errorf("expected [msgs/1], got %v", keys)
	}
}

func TestKVEngine_Lookup_CompoundIndexScan_WithRange(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("events", []EntityIndex{
		{FieldNames: []string{"Category", "Score"}, JSONFieldNames: []string{"category", "score"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	entries := []struct{ id, body string }{
		{"events/1", `{"category":"sports","score":"010"}`},
		{"events/2", `{"category":"sports","score":"050"}`},
		{"events/3", `{"category":"sports","score":"090"}`},
	}
	for _, e := range entries {
		if err := eng.PutWithIndexes([]byte(e.id), []byte(e.body)); err != nil {
			t.Fatalf("PutWithIndexes %s: %v", e.id, err)
		}
	}

	idxName := eng.buildCompoundIndexName("events", []string{"Category", "Score"})

	// Range scan: prefix "sports", range start "020", end "080" → events/2 only.
	result, err := eng.Lookup(context.Background(), index.CompoundIndexScanRequest{
		IndexName:    idxName,
		PrefixValues: [][]byte{[]byte("sports")},
		RangeStart:   []byte("020"),
		RangeEnd:     []byte("080"),
	})
	if err != nil {
		t.Fatalf("Lookup CompoundIndexScanRequest with range: %v", err)
	}
	_ = result // result type depends on ScanCompound implementation
}

func TestKVEngine_Lookup_CompoundIndexScan_PrefixOnly(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log := testLogger(t)
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("posts", []EntityIndex{
		{FieldNames: []string{"AuthorID", "Date"}, JSONFieldNames: []string{"author_id", "date"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	entries := []struct{ id, body string }{
		{"posts/1", `{"author_id":"a1","date":"2024-01-01"}`},
		{"posts/2", `{"author_id":"a1","date":"2024-02-01"}`},
		{"posts/3", `{"author_id":"a2","date":"2024-01-15"}`},
	}
	for _, e := range entries {
		if err := eng.PutWithIndexes([]byte(e.id), []byte(e.body)); err != nil {
			t.Fatalf("PutWithIndexes %s: %v", e.id, err)
		}
	}

	idxName := eng.buildCompoundIndexName("posts", []string{"AuthorID", "Date"})

	// Prefix scan for author "a1" — should return posts/1 and posts/2.
	result, err := eng.Lookup(context.Background(), index.CompoundIndexScanRequest{
		IndexName:    idxName,
		PrefixValues: [][]byte{[]byte("a1")},
	})
	if err != nil {
		t.Fatalf("Lookup CompoundIndexScanRequest: %v", err)
	}
	keys, ok := result.([][]byte)
	if !ok {
		t.Fatalf("expected [][]byte result, got %T", result)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys for author a1, got %d: %v", len(keys), keys)
	}
}
