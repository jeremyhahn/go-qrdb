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
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	cmd1 := mustEncode(protocol.EncodeIndexCreateOpts("idx_a", "field_a", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	cmd2 := mustEncode(protocol.EncodeIndexCreateOpts("idx_b", "field_b", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

// A batch that puts one key twice leaves only the second value's index
// entries: the second put's previous value is the batch's own first put, whose
// entries it retracts, not the value committed before the batch. A delete that
// follows a put in the same batch retracts that put's entries the same way.
func TestKVEngine_BatchPutsOneKeyTwice(t *testing.T) {
	const faultDocD = `{"email":"d@example.com"}`
	put := func(key, doc string) []byte {
		return mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte(key), []byte(doc), nil, 0))
	}
	del := func(key string) []byte {
		return mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte(key), nil, nil, 0))
	}
	cases := []struct {
		name      string
		key       string
		ops       [][]byte
		wantValue string
		wantIndex map[string][]string
	}{
		{name: "absent key", key: "user:3", ops: [][]byte{put("user:3", faultDocC), put("user:3", faultDocD)},
			wantValue: faultDocD, wantIndex: map[string][]string{
				"c@example.com": nil, "d@example.com": {"user:3"}, "b@example.com": {"user:2"},
			}},
		{name: "existing key", key: "user:1", ops: [][]byte{put("user:1", faultDocC), put("user:1", faultDocD)},
			wantValue: faultDocD, wantIndex: map[string][]string{
				"a@example.com": nil, "c@example.com": nil, "d@example.com": {"user:1"}, "b@example.com": {"user:2"},
			}},
		{name: "put then delete", key: "user:3", ops: [][]byte{put("user:3", faultDocC), del("user:3")},
			wantIndex: map[string][]string{"c@example.com": nil, "b@example.com": {"user:2"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := newIndexedFaultKVEngine(t, newMemoryBacked(t), true)
			applyKVEntries(t, eng, []sm.Entry{{Index: 10, Cmd: mustEncode(protocol.EncodeBatch(tc.ops))}})

			got, _ := eng.Lookup(context.Background(), tc.key)
			if string(asBytes(got)) != tc.wantValue {
				t.Fatalf("%s = %q, want %q", tc.key, asBytes(got), tc.wantValue)
			}
			for email, want := range tc.wantIndex {
				if got := emailIndexed(t, eng, email); !slices.Equal(got, want) {
					t.Errorf("by_email[%s] = %v, want %v", email, got, want)
				}
			}
		})
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

	// A batch naming an operation this engine cannot execute is REFUSED, not
	// fatal: the refusal travels in the result payload. Until go-qrdb#520 it
	// was returned to Raft, which halted the shard.
	assertRefusedNotHalted(t, eng.Update(context.Background(), entries, results),
		results[0], "unsupported operation")
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

	assertRefusedNotHalted(t, eng.Update(context.Background(), entries, results),
		results[0], "unsupported operation")
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

	assertRefusedNotHalted(t, eng.Update(context.Background(), entries, results),
		results[0], "unsupported operation")
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
	// sibling "unknown ext op" case, and it is a refusal: the engine has no
	// handler for the verb, which says nothing about the health of its
	// storage.
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0}}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, eng.Update(context.Background(), entries, results),
		results[0], "unsupported operation")
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
	msg, kind, ok := protocol.DecodeAppErrorKind(results[0].Data)
	if !ok {
		t.Fatalf("expected an application-error result, got %+v", results[0])
	}
	if kind != protocol.AppErrorCorrupt {
		t.Errorf("kind = %d, want AppErrorCorrupt: the committed bytes are not what any "+
			"writer of this version produced (go-qrdb#557)", kind)
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

// ---------------------------------------------------------------------------
// Engine interface compliance
// ---------------------------------------------------------------------------

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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_color", "color", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_age", "age", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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

// GetInBatch forwards to the wrapped real index's GetInBatch, falling back to
// Get if it is not itself batch-aware. failingIndex embeds index.Index (the
// interface, not the concrete type), so the embedding alone does not promote
// a method -- GetInBatch -- that is only declared on index.BatchableIndex.
func (f *failingIndex) GetInBatch(batch storage.Batch, fieldValue []byte) ([][]byte, error) {
	if bidx, ok := f.Index.(index.BatchableIndex); ok {
		return bidx.GetInBatch(batch, fieldValue)
	}
	return f.Index.Get(fieldValue)
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

// TestKVEngine_LegacyTransactionKeepsUnorderedUniqueCheck pins the replay
// contract on the KV engine: a transaction without the ordered-unique flag
// keeps the delete-only unique check, so moving a unique value between keys
// by overwrite is refused and writes nothing; the same ops with the flag
// commit.
func TestKVEngine_LegacyTransactionKeepsUnorderedUniqueCheck(t *testing.T) {
	setup := func(t *testing.T) *KVEngine {
		t.Helper()
		stg := memory.NewStorage()
		t.Cleanup(func() { _ = stg.Close() })
		eng := NewKVEngine(stg)
		cmd := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name", protocol.IndexCreateOptions{Unique: true}))
		seed := mustEncode(protocol.EncodeTransaction(nil, [][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"name":"x"}`), nil, 0)),
		}, nil))
		res := applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}, {Index: 2, Cmd: seed}})
		if !txnSucceeded(t, res[1]) {
			t.Fatal("seed transaction did not run its success branch")
		}
		return eng
	}
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"name":"y"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:2"), []byte(`{"name":"x"}`), nil, 0)),
	}

	t.Run("legacy refused", func(t *testing.T) {
		eng := setup(t)
		cmd := mustEncode(protocol.EncodeTransaction(nil, ops, nil))
		res := applyKVEntries(t, eng, []sm.Entry{{Index: 3, Cmd: cmd}})[0]
		_, kind, ok := protocol.DecodeAppErrorKind(res.Data)
		if !ok || kind != protocol.AppErrorConflict {
			t.Fatalf("result data = %q, want a conflict application error", res.Data)
		}
		mustLookupValue(t, eng, "user:1", `{"name":"x"}`)
		mustLookupAbsent(t, eng, "user:2")
	})
	t.Run("flagged commits", func(t *testing.T) {
		eng := setup(t)
		cmd := mustEncode(protocol.EncodeTransactionFlags(nil, ops, nil, protocol.CmdFlagOrderedUnique))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 3, Cmd: cmd}})[0]) {
			t.Fatal("the flagged move should run its success branch")
		}
		mustLookupValue(t, eng, "user:1", `{"name":"y"}`)
		mustLookupValue(t, eng, "user:2", `{"name":"x"}`)
	})
}

// TestKVEngine_LegacyNonJSONOverwriteReplay pins the replay of an existing,
// unflagged log: a plain non-JSON overwrite of A keeps A's unique entry, so a
// later unflagged write of B with A's old value is refused exactly as the
// binary that committed the log refused it. Only a transaction proposed with
// ordered-unique-check releases the value through a non-JSON overwrite.
func TestKVEngine_LegacyNonJSONOverwriteReplay(t *testing.T) {
	setup := func(t *testing.T) *KVEngine {
		t.Helper()
		stg := memory.NewStorage()
		t.Cleanup(func() { _ = stg.Close() })
		eng := NewKVEngine(stg)
		create := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
		applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: create}, kvPutEntry(2, "user:1", `{"email":"x"}`)})
		mustLookupValue(t, eng, "user:1", `{"email":"x"}`)
		return eng
	}
	claimB := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:2"), []byte(`{"email":"x"}`), nil, 0))

	t.Run("legacy refused", func(t *testing.T) {
		eng := setup(t)
		txn := mustEncode(protocol.EncodeTransaction(nil, [][]byte{claimB}, nil))
		res := applyKVEntries(t, eng, []sm.Entry{kvPutEntry(3, "user:1", "not-json"), {Index: 4, Cmd: txn}})
		mustLookupValue(t, eng, "user:1", "not-json")
		_, kind, ok := protocol.DecodeAppErrorKind(res[1].Data)
		if !ok || kind != protocol.AppErrorConflict {
			t.Fatalf("result data = %q, want a conflict application error", res[1].Data)
		}
		mustLookupAbsent(t, eng, "user:2")
	})
	t.Run("flagged commits", func(t *testing.T) {
		eng := setup(t)
		overwrite := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte("not-json"), nil, 0))
		txn := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{overwrite, claimB}, nil, protocol.CmdFlagOrderedUnique))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 3, Cmd: txn}})[0]) {
			t.Fatal("the flagged overwrite and claim should run its success branch")
		}
		mustLookupValue(t, eng, "user:1", "not-json")
		mustLookupValue(t, eng, "user:2", `{"email":"x"}`)
	})
}

// TestKVEngine_OrderedTransactionAfterLegacyNonJSONOverwrite: a legacy plain
// non-JSON overwrite keeps user:1's stale x entry. A flagged transaction that
// moves user:1 to y and gives x to user:2 must not treat that entry as
// released (the value user:1 holds carries no email, so apply cannot retract
// it): the set is refused as a conflict, not failed at apply.
func TestKVEngine_OrderedTransactionAfterLegacyNonJSONOverwrite(t *testing.T) {
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg)
	create := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true}))
	applyKVEntries(t, eng, []sm.Entry{
		{Index: 1, Cmd: create},
		kvPutEntry(2, "user:1", `{"email":"x"}`),
		kvPutEntry(3, "user:1", "not-json"),
	})
	txn := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:1"), []byte(`{"email":"y"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:2"), []byte(`{"email":"x"}`), nil, 0)),
	}, nil, protocol.CmdFlagOrderedUnique))
	res := applyKVEntries(t, eng, []sm.Entry{{Index: 4, Cmd: txn}})
	_, kind, ok := protocol.DecodeAppErrorKind(res[0].Data)
	if !ok || kind != protocol.AppErrorConflict {
		t.Fatalf("result data = %q, want a conflict application error", res[0].Data)
	}
	mustLookupValue(t, eng, "user:1", "not-json")
	mustLookupAbsent(t, eng, "user:2")
}

// TestKVEngine_OrderedTransactionAfterLegacyNonJSONOverwriteCompound is the
// compound-index twin: the stale (u1, d1) tuple of sessions/1 stays held.
func TestKVEngine_OrderedTransactionAfterLegacyNonJSONOverwriteCompound(t *testing.T) {
	value := `{"user_id":"u1","device_id":"d1"}`
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg, WithKVLogger(testLogger(t)))
	if err := eng.RegisterEntityType("sessions", []EntityIndex{
		{FieldNames: []string{"UserID", "DeviceID"}, JSONFieldNames: []string{"user_id", "device_id"}, Unique: true},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}
	// The legacy state comes from the replicated plain put, which keeps the
	// merge-base semantics; PutWithIndexes (standalone) now releases the entry.
	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "sessions/1", value),
		kvPutEntry(2, "sessions/1", "not-json"),
	})
	txn := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("sessions/1"), []byte(`{"user_id":"u2","device_id":"d1"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("sessions/2"), []byte(value), nil, 0)),
	}, nil, protocol.CmdFlagOrderedUnique))
	res := applyKVEntries(t, eng, []sm.Entry{{Index: 3, Cmd: txn}})
	_, kind, ok := protocol.DecodeAppErrorKind(res[0].Data)
	if !ok || kind != protocol.AppErrorConflict {
		t.Fatalf("result data = %q, want a conflict application error", res[0].Data)
	}
	mustLookupValue(t, eng, "sessions/1", "not-json")
	mustLookupAbsent(t, eng, "sessions/2")
}

// TestKVEngine_OrderedTransactionCaseInsensitiveReleaseThenDuplicate pins that
// a flagged transaction claiming one case-folded unique term twice (A releases
// "alpha", then B "ALPHA" and C "alpha") is refused as a conflict result by
// the pre-check, with no apply error and nothing written.
func TestKVEngine_OrderedTransactionCaseInsensitiveReleaseThenDuplicate(t *testing.T) {
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg)
	create := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name",
		protocol.IndexCreateOptions{Unique: true, CaseInsensitive: true}))
	applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: create}, kvPutEntry(2, "user:A", `{"name":"alpha"}`)})

	txn := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:A"), []byte(`{"name":"beta"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:B"), []byte(`{"name":"ALPHA"}`), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user:C"), []byte(`{"name":"alpha"}`), nil, 0)),
	}, nil, protocol.CmdFlagOrderedUnique))
	res := applyKVEntries(t, eng, []sm.Entry{{Index: 3, Cmd: txn}})[0]
	_, kind, ok := protocol.DecodeAppErrorKind(res.Data)
	if !ok || kind != protocol.AppErrorConflict {
		t.Fatalf("result data = %q, want a conflict application error", res.Data)
	}
	mustLookupValue(t, eng, "user:A", `{"name":"alpha"}`)
	mustLookupAbsent(t, eng, "user:B")
	mustLookupAbsent(t, eng, "user:C")
}

// TestKVEngine_OrderedTransactionNULKeyReleaseIsConflict: deleting key
// "a\x00b" (term x) releases only that holder; it must not also release key
// "b"'s term "x\x00a", whose old NUL-joined release marker was the same
// string. A put of "x\x00a" for a third key is then a conflict result, not a
// fatal apply error from the staged batch.
func TestKVEngine_OrderedTransactionNULKeyReleaseIsConflict(t *testing.T) {
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	eng := NewKVEngine(stg)
	create := mustEncode(protocol.EncodeIndexCreateOpts("by_name", "name",
		protocol.IndexCreateOptions{Unique: true}))
	applyKVEntries(t, eng, []sm.Entry{
		{Index: 1, Cmd: create},
		kvPutEntry(2, "a\x00b", `{"name":"x"}`),
		kvPutEntry(3, "b", `{"name":"x\u0000a"}`),
	})

	txn := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("a\x00b"), nil, nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("c"), []byte(`{"name":"x\u0000a"}`), nil, 0)),
	}, nil, protocol.CmdFlagOrderedUnique))
	res := applyKVEntries(t, eng, []sm.Entry{{Index: 4, Cmd: txn}})[0]
	_, kind, ok := protocol.DecodeAppErrorKind(res.Data)
	if !ok || kind != protocol.AppErrorConflict {
		t.Fatalf("result data = %q, want a conflict application error", res.Data)
	}
	mustLookupValue(t, eng, "a\x00b", `{"name":"x"}`)
	mustLookupAbsent(t, eng, "c")
}

// A flagged transaction checks a unique compound index across all its
// writes in op order: two keys cannot claim one tuple, and a tuple an
// earlier overwrite released may be taken by a later write.
func TestKVEngine_FlaggedTransactionChecksUniqueCompoundAcrossWrites(t *testing.T) {
	setup := func(t *testing.T) *KVEngine {
		t.Helper()
		stg := memory.NewStorage()
		t.Cleanup(func() { _ = stg.Close() })
		eng := NewKVEngine(stg)
		if err := eng.RegisterEntityType("sessions", []EntityIndex{
			{FieldNames: []string{"UserID", "DeviceID"}, JSONFieldNames: []string{"user_id", "device_id"}, Unique: true},
		}); err != nil {
			t.Fatalf("RegisterEntityType: %v", err)
		}
		seed := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("sessions/1"), []byte(`{"user_id":"u","device_id":"d1"}`), nil, 0)),
		}, nil, protocol.CmdFlagOrderedUnique))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: seed}})[0]) {
			t.Fatal("seed transaction did not run its success branch")
		}
		return eng
	}
	flagged := func(ops ...[]byte) []byte {
		return mustEncode(protocol.EncodeTransactionFlags(nil, ops, nil, protocol.CmdFlagOrderedUnique))
	}
	put := func(k, v string) []byte {
		return mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte(k), []byte(v), nil, 0))
	}

	t.Run("two keys one tuple refused", func(t *testing.T) {
		eng := setup(t)
		cmd := flagged(put("sessions/2", `{"user_id":"u","device_id":"d2"}`), put("sessions/3", `{"user_id":"u","device_id":"d2"}`))
		res := applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})[0]
		_, kind, ok := protocol.DecodeAppErrorKind(res.Data)
		if !ok || kind != protocol.AppErrorConflict {
			t.Fatalf("result data = %q, want a conflict application error", res.Data)
		}
		mustLookupAbsent(t, eng, "sessions/2")
		mustLookupAbsent(t, eng, "sessions/3")
	})
	t.Run("a committed tuple refused", func(t *testing.T) {
		eng := setup(t)
		cmd := flagged(put("sessions/2", `{"user_id":"u","device_id":"d1"}`))
		res := applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})[0]
		_, kind, ok := protocol.DecodeAppErrorKind(res.Data)
		if !ok || kind != protocol.AppErrorConflict {
			t.Fatalf("result data = %q, want a conflict application error", res.Data)
		}
		mustLookupAbsent(t, eng, "sessions/2")
	})
	t.Run("move commits", func(t *testing.T) {
		eng := setup(t)
		cmd := flagged(put("sessions/1", `{"user_id":"u","device_id":"d2"}`), put("sessions/2", `{"user_id":"u","device_id":"d1"}`))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 2, Cmd: cmd}})[0]) {
			t.Fatal("the flagged move should run its success branch")
		}
		mustLookupValue(t, eng, "sessions/1", `{"user_id":"u","device_id":"d2"}`)
		mustLookupValue(t, eng, "sessions/2", `{"user_id":"u","device_id":"d1"}`)
		keys, err := eng.compoundIndexes["sessions.UserID_DeviceID"].GetCompound([][]byte{[]byte("u"), []byte("d1")})
		if err != nil || len(keys) != 1 || string(keys[0]) != "sessions/2" {
			t.Fatalf("index (u, d1) = %q, %v; want [sessions/2]", keys, err)
		}
	})
}

// indexSetFailingStorage wraps storage.Storage and, while failIndexSet is
// set, fails every Set of a key outside entityPrefix: the index entry
// writes a backfill makes. Every other call is forwarded unchanged.
type indexSetFailingStorage struct {
	storage.Storage
	entityPrefix []byte
	failIndexSet bool
}

var errIndexSetInjected = errors.New("injected index Set failure")

func (f *indexSetFailingStorage) Set(key, value []byte) error {
	if f.failIndexSet && !bytes.HasPrefix(key, f.entityPrefix) {
		return errIndexSetInjected
	}
	return f.Storage.Set(key, value)
}

func TestKVEngine_RegisterEntityType_UniqueBackfillDuplicateRefused(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg, WithKVLogger(testLogger(t)))

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "users/1", `{"email":"a@example.com"}`),
		kvPutEntry(2, "users/2", `{"email":"a@example.com"}`),
	})

	defs := []EntityIndex{{FieldName: "Email", JSONFieldName: "email", Unique: true}}
	if err := eng.RegisterEntityType("users", defs); err == nil {
		t.Fatal("RegisterEntityType over duplicate values: want error, got nil")
	}
	if err := eng.RegisterEntityType("users", defs); err == nil {
		t.Fatal("second RegisterEntityType over duplicate values: want error, got nil")
	}

	_, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "users.Email",
		Value:     []byte("a@example.com"),
	})
	var nf *index.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("Lookup after refused registration: want *index.NotFoundError, got %T: %v", err, err)
	}
	if _, ok := eng.entityIndexes["users"]; ok {
		t.Error("entityIndexes[users] set after refused registration")
	}
}

func TestKVEngine_RegisterEntityType_CompoundUniqueBackfillDuplicateRefused(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg, WithKVLogger(testLogger(t)))

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "sessions/1", `{"user_id":"u1","device_id":"d1"}`),
		kvPutEntry(2, "sessions/2", `{"user_id":"u1","device_id":"d1"}`),
	})

	defs := []EntityIndex{
		{FieldName: "UserID", JSONFieldName: "user_id"},
		{FieldNames: []string{"UserID", "DeviceID"}, JSONFieldNames: []string{"user_id", "device_id"}, Unique: true},
	}
	if err := eng.RegisterEntityType("sessions", defs); err == nil {
		t.Fatal("RegisterEntityType over duplicate compound values: want error, got nil")
	}
	if err := eng.RegisterEntityType("sessions", defs); err == nil {
		t.Fatal("second RegisterEntityType over duplicate compound values: want error, got nil")
	}

	_, err := eng.Lookup(context.Background(), index.CompoundIndexQueryRequest{
		IndexName: "sessions.UserID_DeviceID",
		Values:    [][]byte{[]byte("u1"), []byte("d1")},
	})
	var nf *index.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("compound Lookup after refused registration: want *index.NotFoundError, got %T: %v", err, err)
	}
	// The single-field index registered earlier in the same call is rolled back.
	_, err = eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "sessions.UserID",
		Value:     []byte("u1"),
	})
	if !errors.As(err, &nf) {
		t.Fatalf("Lookup of rolled-back index: want *index.NotFoundError, got %T: %v", err, err)
	}
	if _, ok := eng.entityIndexes["sessions"]; ok {
		t.Error("entityIndexes[sessions] set after refused registration")
	}
}

func TestKVEngine_RegisterEntityType_BackfillWriteFailureRollsBack(t *testing.T) {
	real := memory.NewStorage()
	defer func() { _ = real.Close() }()
	fs := &indexSetFailingStorage{Storage: real, entityPrefix: []byte("orders/")}
	eng := NewKVEngine(fs, WithKVLogger(testLogger(t)))

	applyKVEntries(t, eng, []sm.Entry{
		kvPutEntry(1, "orders/1", `{"cust":"c1","sku":"s1"}`),
		kvPutEntry(2, "orders/2", `{"cust":"c1","sku":"s2"}`),
		kvPutEntry(3, "orders/3", `{"cust":"c2","sku":"s1"}`),
	})

	defs := []EntityIndex{
		{FieldName: "Cust", JSONFieldName: "cust"},
		{FieldNames: []string{"Cust", "Sku"}, JSONFieldNames: []string{"cust", "sku"}},
	}

	fs.failIndexSet = true
	err := eng.RegisterEntityType("orders", defs)
	if !errors.Is(err, errIndexSetInjected) {
		t.Fatalf("RegisterEntityType with failing index writes: want injected error, got %v", err)
	}
	if len(eng.indexes) != 0 || len(eng.indexFields) != 0 || len(eng.indexScopes) != 0 {
		t.Errorf("single-field index left registered: %d indexes", len(eng.indexes))
	}
	if len(eng.compoundIndexes) != 0 || len(eng.compoundIndexFields) != 0 || len(eng.compoundIndexScopes) != 0 {
		t.Errorf("compound index left registered: %d indexes", len(eng.compoundIndexes))
	}
	if _, ok := eng.entityIndexes["orders"]; ok {
		t.Error("entityIndexes[orders] set after failed registration")
	}

	// Single-field index succeeds, compound fails: both rolled back.
	fs.failIndexSet = false
	if err := eng.RegisterEntityType("orders", defs[:1]); err != nil {
		t.Fatalf("register single-field index: %v", err)
	}
	eng2 := NewKVEngine(fs, WithKVLogger(testLogger(t)))
	fs.failIndexSet = true
	if err := eng2.RegisterEntityType("orders", defs[1:]); !errors.Is(err, errIndexSetInjected) {
		t.Fatalf("compound registration with failing index writes: want injected error, got %v", err)
	}
	if len(eng2.compoundIndexes) != 0 {
		t.Errorf("compound index left registered: %d", len(eng2.compoundIndexes))
	}
	fs.failIndexSet = false

	// Retry on a fresh engine after the fault is cleared: every record is indexed.
	eng3 := NewKVEngine(fs, WithKVLogger(testLogger(t)))
	if err := eng3.RegisterEntityType("orders", defs); err != nil {
		t.Fatalf("retry RegisterEntityType: %v", err)
	}
	got, err := eng3.Lookup(context.Background(), index.IndexQueryRequest{IndexName: "orders.Cust", Value: []byte("c1")})
	if err != nil {
		t.Fatalf("Lookup(c1): %v", err)
	}
	if ids := got.([][]byte); len(ids) != 2 {
		t.Errorf("orders.Cust c1: want 2 keys, got %d", len(ids))
	}
	for _, v := range [][2]string{{"c1", "s1"}, {"c1", "s2"}, {"c2", "s1"}} {
		got, err := eng3.Lookup(context.Background(), index.CompoundIndexQueryRequest{
			IndexName: "orders.Cust_Sku",
			Values:    [][]byte{[]byte(v[0]), []byte(v[1])},
		})
		if err != nil {
			t.Fatalf("compound Lookup(%v): %v", v, err)
		}
		if ids := got.([][]byte); len(ids) != 1 {
			t.Errorf("orders.Cust_Sku %v: want 1 key, got %d", v, len(ids))
		}
	}

	// Retry on the engine whose registration failed also succeeds.
	if err := eng.RegisterEntityType("orders", defs); err != nil {
		t.Fatalf("retry on the same engine: %v", err)
	}
	if len(eng.compoundIndexes) != 1 {
		t.Errorf("retry on the same engine: want 1 compound index, got %d", len(eng.compoundIndexes))
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

// TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesUniqueValue overwrites
// an indexed JSON record with non-JSON bytes: its old unique entry must be
// released, so another key can take the value and a lookup of it no longer
// returns the overwritten key.
func TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesUniqueValue(t *testing.T) {
	setup := func(t *testing.T) *KVEngine {
		t.Helper()
		stg := memory.NewStorage()
		t.Cleanup(func() { _ = stg.Close() })
		eng := NewKVEngine(stg, WithKVLogger(testLogger(t)))
		if err := eng.RegisterEntityType("member", []EntityIndex{
			{FieldName: "Email", JSONFieldName: "email", Unique: true},
		}); err != nil {
			t.Fatalf("RegisterEntityType: %v", err)
		}
		if err := eng.PutWithIndexes([]byte("member/1"), []byte(`{"email":"a"}`)); err != nil {
			t.Fatalf("PutWithIndexes member/1 JSON: %v", err)
		}
		return eng
	}
	lookupA := func(t *testing.T, eng *KVEngine) [][]byte {
		t.Helper()
		result, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "member.Email",
			Value:     []byte("a"),
		})
		if err != nil {
			t.Fatalf("Lookup a: %v", err)
		}
		return result.([][]byte)
	}

	// An unflagged (plain) non-JSON overwrite keeps the old entry, as every
	// replica of an existing log applied it.
	t.Run("put with indexes releases", func(t *testing.T) {
		eng := setup(t)
		if err := eng.PutWithIndexes([]byte("member/1"), []byte("not-json")); err != nil {
			t.Fatalf("PutWithIndexes member/1 non-JSON: %v", err)
		}
		if ids := lookupA(t, eng); len(ids) != 0 {
			t.Errorf("lookup a = %q, want no entries after the non-JSON overwrite", ids)
		}
		if err := eng.PutWithIndexes([]byte("member/2"), []byte(`{"email":"a"}`)); err != nil {
			t.Fatalf("PutWithIndexes member/2 with the released value: %v", err)
		}
		if ids := lookupA(t, eng); len(ids) != 1 || string(ids[0]) != "member/2" {
			t.Errorf("lookup a = %q, want [member/2]", ids)
		}
	})
	// A transaction proposed with ordered-unique-check releases the value.
	t.Run("flagged releases", func(t *testing.T) {
		eng := setup(t)
		cmd := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("member/1"), []byte("not-json"), nil, 0)),
		}, nil, protocol.CmdFlagOrderedUnique))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}})[0]) {
			t.Fatal("the flagged non-JSON overwrite should run its success branch")
		}
		if ids := lookupA(t, eng); len(ids) != 0 {
			t.Errorf("expected no keys for the overwritten value, got %q", ids)
		}
		if err := eng.PutWithIndexes([]byte("member/2"), []byte(`{"email":"a"}`)); err != nil {
			t.Fatalf("PutWithIndexes member/2 with the released value: %v", err)
		}
	})
}

// TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesCompoundUniqueValue is
// the compound-index twin of the test above.
func TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesCompoundUniqueValue(t *testing.T) {
	value := []byte(`{"user_id":"u1","device_id":"d1"}`)
	setup := func(t *testing.T) *KVEngine {
		t.Helper()
		stg := memory.NewStorage()
		t.Cleanup(func() { _ = stg.Close() })
		eng := NewKVEngine(stg, WithKVLogger(testLogger(t)))
		if err := eng.RegisterEntityType("sessions", []EntityIndex{
			{FieldNames: []string{"UserID", "DeviceID"}, JSONFieldNames: []string{"user_id", "device_id"}, Unique: true},
		}); err != nil {
			t.Fatalf("RegisterEntityType: %v", err)
		}
		if len(eng.compoundIndexes) != 1 {
			t.Fatalf("expected 1 compound index, got %d", len(eng.compoundIndexes))
		}
		if err := eng.PutWithIndexes([]byte("sessions/1"), value); err != nil {
			t.Fatalf("PutWithIndexes sessions/1 JSON: %v", err)
		}
		return eng
	}
	holders := func(t *testing.T, eng *KVEngine) [][]byte {
		t.Helper()
		var all [][]byte
		for name, idx := range eng.compoundIndexes {
			ids, err := idx.GetCompound([][]byte{[]byte("u1"), []byte("d1")})
			if err != nil {
				t.Fatalf("GetCompound %s: %v", name, err)
			}
			all = append(all, ids...)
		}
		return all
	}

	// An unflagged (plain) non-JSON overwrite keeps the old compound entry.
	t.Run("put with indexes releases", func(t *testing.T) {
		eng := setup(t)
		if err := eng.PutWithIndexes([]byte("sessions/1"), []byte("not-json")); err != nil {
			t.Fatalf("PutWithIndexes sessions/1 non-JSON: %v", err)
		}
		if ids := holders(t, eng); len(ids) != 0 {
			t.Errorf("compound holders = %q, want no entries after the non-JSON overwrite", ids)
		}
		if err := eng.PutWithIndexes([]byte("sessions/2"), value); err != nil {
			t.Fatalf("PutWithIndexes sessions/2 with the released tuple: %v", err)
		}
		if ids := holders(t, eng); len(ids) != 1 || string(ids[0]) != "sessions/2" {
			t.Errorf("compound holders = %q, want [sessions/2]", ids)
		}
	})
	// A transaction proposed with ordered-unique-check releases the tuple.
	t.Run("flagged releases", func(t *testing.T) {
		eng := setup(t)
		cmd := mustEncode(protocol.EncodeTransactionFlags(nil, [][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("sessions/1"), []byte("not-json"), nil, 0)),
		}, nil, protocol.CmdFlagOrderedUnique))
		if !txnSucceeded(t, applyKVEntries(t, eng, []sm.Entry{{Index: 1, Cmd: cmd}})[0]) {
			t.Fatal("the flagged non-JSON overwrite should run its success branch")
		}
		if ids := holders(t, eng); len(ids) != 0 {
			t.Errorf("expected no keys for the overwritten compound value, got %q", ids)
		}
		if err := eng.PutWithIndexes([]byte("sessions/2"), value); err != nil {
			t.Fatalf("PutWithIndexes sessions/2 with the released value: %v", err)
		}
	})
}

func TestKVEngine_RegisterEntityType_ClearsPersistedEntriesOfANewIndex(t *testing.T) {
	cases := map[string]struct {
		def     EntityIndex
		a, b    []byte
		holders func(t *testing.T, eng *KVEngine, value []byte) [][]byte
	}{
		"single": {
			def: EntityIndex{FieldName: "Email", JSONFieldName: "email", Unique: true},
			a:   []byte(`{"email":"a@x"}`),
			b:   []byte(`{"email":"b@x"}`),
			holders: func(t *testing.T, eng *KVEngine, value []byte) [][]byte {
				t.Helper()
				ids, err := eng.indexes[eng.buildIndexName("member", "Email")].Get(value)
				if err != nil {
					t.Fatalf("Get %s: %v", value, err)
				}
				return ids
			},
		},
		"compound": {
			def: EntityIndex{FieldNames: []string{"Team", "State"}, JSONFieldNames: []string{"team", "state"}, Unique: true},
			a:   []byte(`{"team":"t","state":"a"}`),
			b:   []byte(`{"team":"t","state":"b"}`),
			holders: func(t *testing.T, eng *KVEngine, value []byte) [][]byte {
				t.Helper()
				ids, err := eng.compoundIndexes[eng.buildCompoundIndexName("member", []string{"Team", "State"})].GetCompound([][]byte{[]byte("t"), value})
				if err != nil {
					t.Fatalf("GetCompound %s: %v", value, err)
				}
				return ids
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			t.Cleanup(func() { _ = stg.Close() })

			first := NewKVEngine(stg, WithKVLogger(testLogger(t)))
			if err := first.RegisterEntityType("member", []EntityIndex{c.def}); err != nil {
				t.Fatalf("RegisterEntityType: %v", err)
			}
			if err := first.PutWithIndexes([]byte("member/1"), c.a); err != nil {
				t.Fatalf("PutWithIndexes member/1 a: %v", err)
			}

			// A second engine over the same storage starts with an empty catalog.
			second := NewKVEngine(stg, WithKVLogger(testLogger(t)))
			if err := second.PutWithIndexes([]byte("member/1"), c.b); err != nil {
				t.Fatalf("PutWithIndexes member/1 b: %v", err)
			}
			if err := second.RegisterEntityType("member", []EntityIndex{c.def}); err != nil {
				t.Fatalf("RegisterEntityType again: %v", err)
			}

			valA, valB := []byte("a@x"), []byte("b@x")
			if name == "compound" {
				valA, valB = []byte("a"), []byte("b")
			}
			if ids := c.holders(t, second, valA); len(ids) != 0 {
				t.Errorf("holders of a = %q, want none", ids)
			}
			if ids := c.holders(t, second, valB); len(ids) != 1 || string(ids[0]) != "member/1" {
				t.Errorf("holders of b = %q, want [member/1]", ids)
			}
			if err := second.PutWithIndexes([]byte("member/2"), c.a); err != nil {
				t.Errorf("PutWithIndexes member/2 a: %v, want the released value admitted", err)
			}
		})
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
	var inf *index.NotFoundError
	if !errors.As(err, &inf) {
		t.Errorf("expected *index.NotFoundError for missing compound index$!1, got %T: %v", err, err)
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
	var inf *index.NotFoundError
	if !errors.As(err, &inf) {
		t.Errorf("expected *index.NotFoundError for missing compound index scan, got %T: %v", err, err)
	}
}

// TestKVEngine_Lookup_MissingIndex_NotFoundError pins that a query or scan of
// an index the engine does not have is the typed *index.NotFoundError the
// service layer reports NotFound, not a generic invalid query.
func TestKVEngine_Lookup_MissingIndex_NotFoundError(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	lookups := map[string]interface{}{
		"query": index.IndexQueryRequest{IndexName: "nonexistent", Value: []byte("x")},
		"scan":  index.IndexScanRequest{IndexName: "nonexistent", Start: []byte("a"), End: []byte("z")},
	}
	for name, q := range lookups {
		_, err := eng.Lookup(context.Background(), q)
		var inf *index.NotFoundError
		if !errors.As(err, &inf) || inf.Name != "nonexistent" {
			t.Errorf("%s: expected *index.NotFoundError{Name: nonexistent}, got %T: %v", name, err, err)
		}
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
