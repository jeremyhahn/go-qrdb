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

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newMVCCEngine(t *testing.T, opts ...MVCCOption) (*MVCCEngine, *memory.MemoryStorage) {
	t.Helper()
	stg := memory.NewStorage()
	eng, err := NewMVCCEngine(stg, opts...)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	return eng, stg
}

func mvccPutEntry(idx uint64, key, value []byte, leaseID int64) sm.Entry {
	cmd := mustEncode(protocol.EncodeMVCCPut(key, value, leaseID))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func mvccDeleteEntry(idx uint64, key []byte) sm.Entry {
	cmd := mustEncode(protocol.EncodeMVCCDelete(key))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func baseKVPutEntry(idx uint64, key, value string) sm.Entry {
	cmd, _ := kv.EncodePut([]byte(key), []byte(value))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func baseKVDeleteEntry(idx uint64, key string) sm.Entry {
	cmd, _ := kv.EncodeDelete([]byte(key))
	return sm.Entry{Index: idx, Cmd: cmd}
}

func applyMVCCEntries(t *testing.T, eng *MVCCEngine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// NewMVCCEngine
// ---------------------------------------------------------------------------

func TestNewMVCCEngine_MemoryStorage(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.Store() == nil {
		t.Fatal("Store() should not be nil")
	}
}

func TestNewMVCCEngine_WithOptions(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub), WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.Lessor() != lr {
		t.Error("Lessor() does not match configured lessor")
	}
}

// ---------------------------------------------------------------------------
// Type
// ---------------------------------------------------------------------------

func TestMVCCEngine_Type(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if got := eng.Type(); got != "mvcc" {
		t.Errorf("Type() = %q, want %q", got, "mvcc")
	}
}

func TestMVCCEngine_TypeConsistent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	first := eng.Type()
	second := eng.Type()
	if first != second {
		t.Errorf("Type() not consistent: %q != %q", first, second)
	}
}

// ---------------------------------------------------------------------------
// Update: Base KV Put
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_BaseKVPut(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "key1", "val1")})

	if results[0].Value == 0 {
		t.Error("expected non-zero result value for put")
	}

	// Verify via MVCC get query.
	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key1")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "val1" {
		t.Errorf("value = %q, want %q", kvResult.Value, "val1")
	}
}

func TestMVCCEngine_Update_BaseKVPutOverwrite(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "key1", "v1")})
	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(2, "key1", "v2")})

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("key1")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "v2" {
		t.Errorf("value = %q, want %q", kvResult.Value, "v2")
	}
}

// ---------------------------------------------------------------------------
// Update: Base KV Delete
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_BaseKVDelete(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "key1", "val1")})
	applyMVCCEntries(t, eng, []sm.Entry{baseKVDeleteEntry(2, "key1")})

	got, err := eng.Lookup(context.Background(), []byte("key1"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %v", got)
	}
}

func TestMVCCEngine_Update_BaseKVDeleteNonExistent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Delete non-existent key should not error.
	applyMVCCEntries(t, eng, []sm.Entry{baseKVDeleteEntry(1, "nokey")})
}

// ---------------------------------------------------------------------------
// Update: MVCC Put
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_MVCCPut(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("mkey"), []byte("mval"), 0),
	})

	if results[0].Value == 0 {
		t.Error("expected non-zero revision from MVCC put")
	}

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("mkey")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "mval" {
		t.Errorf("value = %q, want %q", kvResult.Value, "mval")
	}
}

func TestMVCCEngine_Update_MVCCPutWithLease(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant a lease first.
	leaseCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	leaseEntries := []sm.Entry{{Index: 1, Cmd: leaseCmd}}
	leaseResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), leaseEntries, leaseResults); err != nil {
		t.Fatalf("lease grant: %v", err)
	}
	leaseID := int64(leaseResults[0].Value)

	// Put with lease.
	results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("lkey"), []byte("lval"), leaseID),
	})
	if results[0].Value == 0 {
		t.Error("expected non-zero revision")
	}
}

// ---------------------------------------------------------------------------
// Update: MVCC Delete
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_MVCCDelete(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("mkey"), []byte("mval"), 0),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccDeleteEntry(2, []byte("mkey")),
	})

	got, err := eng.Lookup(context.Background(), []byte("mkey"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after MVCC delete, got %v", got)
	}
}

func TestMVCCEngine_Update_MVCCDeleteNonExistent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccDeleteEntry(1, []byte("nokey")),
	})
}

// ---------------------------------------------------------------------------
// Update: Multiple entries
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_MultipleEntries(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	entries := []sm.Entry{
		mvccPutEntry(1, []byte("k1"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("k2"), []byte("v2"), 0),
	}
	results := applyMVCCEntries(t, eng, entries)

	for i, r := range results {
		if r.Value == 0 {
			t.Errorf("entry %d: expected non-zero revision", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Lookup: MVCCGetQuery
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_MVCCGetQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("qkey"), []byte("qval"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("qkey")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "qval" {
		t.Errorf("value = %q, want %q", kvResult.Value, "qval")
	}
}

func TestMVCCEngine_Lookup_MVCCGetQueryNotFound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("missing")})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// ---------------------------------------------------------------------------
// Lookup: MVCCRangeQuery
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_MVCCRangeQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
		// A 4th write to an already-matched key advances the store's
		// revision past the match count, so a regression that returns the
		// revision in place of the count cannot pass this assertion by
		// coincidence.
		mvccPutEntry(4, []byte("a"), []byte("1b"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("a"),
		End:   []byte("d"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	if len(result.KeyValues) != 3 {
		t.Errorf("expected 3 kvs, got %d", len(result.KeyValues))
	}
	if result.Count != 3 {
		t.Errorf("Count: got %d, want 3 (match count, not revision)", result.Count)
	}
	if result.Revision != 4 {
		t.Errorf("Revision: got %d, want 4 (store's current revision)", result.Revision)
	}
}

func TestMVCCEngine_Lookup_MVCCRangeQueryWithLimit(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start: []byte("a"),
		End:   []byte("d"),
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	if len(result.KeyValues) != 2 {
		t.Errorf("expected 2 kvs with limit, got %d", len(result.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// Lookup: MVCCRevisionQuery
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_MVCCRevisionQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	rev := got.(int64)
	if rev == 0 {
		t.Error("expected non-zero revision after put")
	}
}

func TestMVCCEngine_Lookup_MVCCRevisionQueryInitial(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	got, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	rev := got.(int64)
	if rev != 0 {
		t.Errorf("expected initial revision 0, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// Lookup: string and []byte convenience
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_String(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("skey"), []byte("sval"), 0),
	})

	got, err := eng.Lookup(context.Background(), "skey")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "sval" {
		t.Errorf("value = %q, want %q", got, "sval")
	}
}

func TestMVCCEngine_Lookup_StringNotFound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	got, err := eng.Lookup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing key, got %v", got)
	}
}

func TestMVCCEngine_Lookup_Bytes(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("bkey"), []byte("bval"), 0),
	})

	got, err := eng.Lookup(context.Background(), []byte("bkey"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "bval" {
		t.Errorf("value = %q, want %q", got, "bval")
	}
}

func TestMVCCEngine_Lookup_BytesNotFound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	got, err := eng.Lookup(context.Background(), []byte("missing"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing key, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Lookup: kv.ListQuery
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_ListQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("user:1"), []byte("alice"), 0),
		mvccPutEntry(2, []byte("user:2"), []byte("bob"), 0),
		mvccPutEntry(3, []byte("product:1"), []byte("widget"), 0),
	})

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "user:"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := got.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestMVCCEngine_Lookup_ListQueryEmpty(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "nonexistent:"})
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

func TestMVCCEngine_Lookup_PrefixQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("ns:a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("ns:b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("other:c"), []byte("3"), 0),
	})

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("ns:")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][]byte)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
}

func TestMVCCEngine_Lookup_PrefixQueryWithLimit(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("ns:a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("ns:b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("ns:c"), []byte("3"), 0),
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

// ---------------------------------------------------------------------------
// Lookup: unsupported query type
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_UnsupportedType(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Errorf("expected *InvalidQueryError, got %T", err)
	}
}

func TestMVCCEngine_Lookup_UnsupportedStructType(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), struct{ X int }{X: 1})
	if err == nil {
		t.Fatal("expected error for unsupported struct query type")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestMVCCEngine_Close(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()

	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMVCCEngine_CloseWithIndexes(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()

	// Create an index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch notifications
// ---------------------------------------------------------------------------

func TestMVCCEngine_WatchNotification_Put(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, werr := hub.Watch(ctx, "wkey")
	if werr != nil {
		t.Fatalf("Watch: %v", werr)
	}

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("wkey"), []byte("wval"), 0),
	})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

func TestMVCCEngine_WatchNotification_Delete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("wkey"), []byte("wval"), 0),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, werr := hub.Watch(ctx, "wkey")
	if werr != nil {
		t.Fatalf("Watch: %v", werr)
	}

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccDeleteEntry(2, []byte("wkey")),
	})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected at least 1 event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for delete event")
	}
}

// ---------------------------------------------------------------------------
// Index create and query
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexCreate(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put data first.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("user:1"), []byte(`{"name":"alice"}`), 0),
		mvccPutEntry(2, []byte("user:2"), []byte(`{"name":"bob"}`), 0),
	})

	// Create index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 3, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("result Value = %d, want 1", results[0].Value)
	}

	// Query the index.
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
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

func TestMVCCEngine_IndexCreateDuplicate(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("first create: expected result 1, got %d", results[0].Value)
	}

	// Second create of the same index is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so duplicate-create must succeed without error.
	entries2 := []sm.Entry{{Index: 2, Cmd: idxCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("duplicate create: expected no-op success, got error: %v", err)
	}
	if results2[0].Value != 1 {
		t.Fatalf("duplicate create: expected result 1, got %d", results2[0].Value)
	}

	// The original index must still be intact after the duplicate proposal.
	got, err := eng.Lookup(context.Background(), service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := got.([]string)
	if len(names) != 1 {
		t.Fatalf("expected 1 index after duplicate create, got %d", len(names))
	}
	if names[0] != "by_name" {
		t.Fatalf("expected index name by_name, got %s", names[0])
	}
}

// TestMVCCEngine_IndexCreate_NotVisibleUntilBackfilled is a concurrency
// regression test: handleIndexCreate must not publish the index into
// e.indexes until the synchronous backfill has fully populated it, so a
// concurrent Lookup can never observe a partially-built index. It races a
// tight Lookup loop against a large index-create backfill (widened by -race
// instrumentation) and asserts that whenever the index becomes visible, it
// is already complete.
func TestMVCCEngine_IndexCreate_NotVisibleUntilBackfilled(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	const total = 2000
	putEntries := make([]sm.Entry, total)
	for i := 0; i < total; i++ {
		key := []byte{byte(i >> 8), byte(i)}
		putEntries[i] = mvccPutEntry(uint64(i+1), key, []byte(`{"city":"nyc"}`), 0)
	}
	applyMVCCEntries(t, eng, putEntries)

	stop := make(chan struct{})
	finished := make(chan struct{})
	var raced bool
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
				IndexName: "by_city",
				Value:     []byte("nyc"),
			})
			if err != nil {
				// Index not yet published — expected before backfill completes.
				continue
			}
			ids := got.([][]byte)
			if len(ids) != 0 && len(ids) != total {
				raced = true
				return
			}
		}
	}()

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_city", "city", false, false, false))
	entries := []sm.Entry{{Index: total + 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}
	close(stop)
	<-finished

	if raced {
		t.Fatal("concurrent Lookup observed a partially-backfilled index")
	}
}

// ---------------------------------------------------------------------------
// Index drop
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexDrop(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

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
	if results2[0].Value != 1 {
		t.Errorf("drop result = %d, want 1", results2[0].Value)
	}

	// Query should fail.
	_, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err == nil {
		t.Fatal("expected error querying dropped index")
	}
}

func TestMVCCEngine_IndexDropNonExistent(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Dropping a non-existent index is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so drop-not-found must succeed without error.
	dropCmd := mustEncode(protocol.EncodeIndexDrop("nonexistent"))
	entries := []sm.Entry{{Index: 1, Cmd: dropCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop nonexistent: expected result 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Index list
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexList(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create indexes.
	cmd1 := mustEncode(protocol.EncodeIndexCreate("idx_a", "field_a", false, false, false))
	cmd2 := mustEncode(protocol.EncodeIndexCreate("idx_b", "field_b", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd1}, {Index: 2, Cmd: cmd2}}
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := eng.Lookup(context.Background(), service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := got.([]string)
	if len(names) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// Lease grant / revoke / keepalive
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseGrant(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := mustEncode(protocol.EncodeLeaseGrant(60))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("lease grant: %v", err)
	}

	leaseID := int64(results[0].Value)
	if leaseID == 0 {
		t.Error("expected non-zero lease ID")
	}
}

func TestMVCCEngine_LeaseGrantNoLessor(t *testing.T) {
	eng, stg := newMVCCEngine(t) // no lessor
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := mustEncode(protocol.EncodeLeaseGrant(60))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload in result with no lessor")
	}
}

func TestMVCCEngine_LeaseRevoke(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("lk"), []byte("lv"), leaseID),
	})

	// Revoke.
	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(leaseID))
	revokeEntries := []sm.Entry{{Index: 3, Cmd: revokeCmd}}
	revokeResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), revokeEntries, revokeResults); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Key should be deleted.
	got, err := eng.Lookup(context.Background(), []byte("lk"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after lease revoke, got %v", got)
	}
}

func TestMVCCEngine_LeaseKeepAlive(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// KeepAlive.
	kaCmd := mustEncode(protocol.EncodeLeaseKeepAlive(leaseID))
	kaEntries := []sm.Entry{{Index: 2, Cmd: kaCmd}}
	kaResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), kaEntries, kaResults); err != nil {
		t.Fatalf("keep-alive: %v", err)
	}
	if int64(kaResults[0].Value) != leaseID {
		t.Errorf("keep-alive result = %d, want %d", kaResults[0].Value, leaseID)
	}
}

func TestMVCCEngine_LeaseKeepAliveNoLessor(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	kaCmd := mustEncode(protocol.EncodeLeaseKeepAlive(1))
	entries := []sm.Entry{{Index: 1, Cmd: kaCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload in result with no lessor")
	}
}

// ---------------------------------------------------------------------------
// Lookup: Lease queries
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_LeaseTimeToLive(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	entries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	got, err := eng.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	l := got.(*lease.Lease)
	if l.ID != leaseID {
		t.Errorf("lease ID = %d, want %d", l.ID, leaseID)
	}
}

func TestMVCCEngine_Lookup_LeaseTimeToLiveNoLessor(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error with no lessor")
	}
}

func TestMVCCEngine_Lookup_LeasesQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant 2 leases.
	for i := 0; i < 2; i++ {
		grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
		entries := []sm.Entry{{Index: uint64(i + 1), Cmd: grantCmd}}
		results := make([]sm.Result, 1)
		if err := eng.Update(context.Background(), entries, results); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}

	got, err := eng.Lookup(context.Background(), service.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	leases := got.([]*lease.Lease)
	if len(leases) != 2 {
		t.Errorf("expected 2 leases, got %d", len(leases))
	}
}

func TestMVCCEngine_Lookup_LeasesQueryNoLessor(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error with no lessor")
	}
}

// ---------------------------------------------------------------------------
// Transaction (compare-and-swap)
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_Success(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put initial value.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("txkey"), []byte("old"), 0),
	})

	// Get the current value for compare.
	got, _ := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("txkey")})
	kvResult := got.(*service.KeyValue)

	// Transaction: compare value == "old", then put "new".
	compares := [][]byte{
		mustEncode(protocol.EncodeCompare(
			[]byte("txkey"),
			byte(service.CompareValue),
			byte(service.CompareEqual),
			[]byte("old"),
			0, 0, kvResult.ModRevision,
		)),
	}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txkey"), []byte("new"), nil, 0)),
	}
	failureOps := [][]byte{}

	txnCmd := mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	// Check succeeded flag.
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Error("expected transaction to succeed")
	}

	// Verify new value.
	got2, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("txkey")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got2.(*service.KeyValue).Value) != "new" {
		t.Errorf("value = %q, want %q", got2.(*service.KeyValue).Value, "new")
	}
}

func TestMVCCEngine_Transaction_Failure(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put initial value.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("txkey"), []byte("old"), 0),
	})

	// Transaction: compare value == "wrong" (will fail), failureOps put "failed".
	compares := [][]byte{
		mustEncode(protocol.EncodeCompare(
			[]byte("txkey"),
			byte(service.CompareValue),
			byte(service.CompareEqual),
			[]byte("wrong"),
			0, 0, 0,
		)),
	}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txkey"), []byte("new"), nil, 0)),
	}
	failureOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txkey"), []byte("failed"), nil, 0)),
	}

	txnCmd := mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	// Check failed flag.
	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Error("expected transaction to fail")
	}

	// Verify failure value.
	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("txkey")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.(*service.KeyValue).Value) != "failed" {
		t.Errorf("value = %q, want %q", got.(*service.KeyValue).Value, "failed")
	}
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

func TestMVCCEngine_Batch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk1"), []byte("bv1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk2"), []byte("bv2"), nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("batch: %v", err)
	}

	// Verify both keys.
	for _, k := range []string{"bk1", "bk2"} {
		got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(k)})
		if err != nil {
			t.Fatalf("Lookup(%s): %v", k, err)
		}
		if got == nil {
			t.Errorf("Lookup(%s) = nil", k)
		}
	}
}

func TestMVCCEngine_BatchWithDelete(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put first.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("dk"), []byte("dv"), 0),
	})

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("dk"), nil, nil, 0)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("batch delete: %v", err)
	}

	got, err := eng.Lookup(context.Background(), []byte("dk"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after batch delete, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Compact
// ---------------------------------------------------------------------------

func TestMVCCEngine_Compact(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put some data to create revisions.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("ck"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("ck"), []byte("v2"), 0),
	})

	// Compact to revision 1.
	compactCmd := mustEncode(protocol.EncodeMVCCCompact(1))
	entries := []sm.Entry{{Index: 3, Cmd: compactCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("compact result = %d, want 1", results[0].Value)
	}
}

func TestMVCCEngine_CompactEmpty(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	compactCmd := mustEncode(protocol.EncodeMVCCCompact(0))
	entries := []sm.Entry{{Index: 1, Cmd: compactCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("compact empty: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DeliverNotifications
// ---------------------------------------------------------------------------

func TestMVCCEngine_DeliverNotifications_NilHub(t *testing.T) {
	eng, stg := newMVCCEngine(t) // no hub
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Should not panic with nil hub.
	eng.DeliverNotifications([]Notification{
		{EventType: service.EventPut, Key: []byte("k")},
	})
}

func TestMVCCEngine_DeliverNotifications_Empty(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Should not panic with empty notifications.
	eng.DeliverNotifications(nil)
	eng.DeliverNotifications([]Notification{})
}

// ---------------------------------------------------------------------------
// TimeSeries accessor
// ---------------------------------------------------------------------------

func TestMVCCEngine_TimeSeries_Nil(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.TimeSeries() != nil {
		t.Error("expected nil TimeSeries when not configured")
	}
}

// ---------------------------------------------------------------------------
// TS Lookup without TS configured
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_TSQueryNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.TSQueryRequest{})
	if err == nil {
		t.Fatal("expected error for TS query without TS configured")
	}
}

func TestMVCCEngine_Lookup_TSAggregateNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.TSAggregateRequest{})
	if err == nil {
		t.Fatal("expected error for TS aggregate without TS configured")
	}
}

// ---------------------------------------------------------------------------
// Index scan lookup
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_IndexScan(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put data and create index.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("u:1"), []byte(`{"age":"25"}`), 0),
		mvccPutEntry(2, []byte("u:2"), []byte(`{"age":"30"}`), 0),
	})

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_age", "age", false, false, false))
	entries := []sm.Entry{{Index: 3, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	got, err := eng.Lookup(context.Background(), service.IndexScanRequest{
		IndexName: "by_age",
		Start:     []byte("2"),
		End:       []byte("4"),
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][][]byte)
	if len(result) < 1 {
		t.Errorf("expected at least 1 scan result, got %d", len(result))
	}
}

func TestMVCCEngine_Lookup_IndexScanNotFound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.IndexScanRequest{
		IndexName: "nonexistent",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index scan")
	}
}

// ---------------------------------------------------------------------------
// Index query not found
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_IndexQueryNotFound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "nonexistent",
		Value:     []byte("x"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index query")
	}
}

// ---------------------------------------------------------------------------
// MVCCGetAtRevisionQuery
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_MVCCGetAtRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("rk"), []byte("v1"), 0),
	})
	rev1Results := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("rk"), []byte("v2"), 0),
	})
	_ = rev1Results

	// Get current revision.
	revGot, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	currentRev := revGot.(int64)

	// Get at current revision (should be v2).
	got, err := eng.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("rk"),
		Revision: currentRev,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "v2" {
		t.Errorf("value = %q, want %q", kvResult.Value, "v2")
	}
}

func TestMVCCEngine_Lookup_MVCCGetAtRevisionOld(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	r1 := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("rk"), []byte("v1"), 0),
	})
	rev1 := int64(r1[0].Value)

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("rk"), []byte("v2"), 0),
	})

	// Get at old revision.
	got, err := eng.Lookup(context.Background(), service.MVCCGetAtRevisionQuery{
		Key:      []byte("rk"),
		Revision: rev1,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "v1" {
		t.Errorf("value = %q, want %q", kvResult.Value, "v1")
	}
}

// ---------------------------------------------------------------------------
// isInternalKey
// ---------------------------------------------------------------------------

func TestIsInternalKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "sys_prefix", key: "__sys:users", want: true},
		{name: "ts_prefix", key: "ts:metric", want: true},
		{name: "idx_metric", key: "idx:metric:foo", want: true},
		{name: "idx_tag", key: "idx:tag:bar", want: true},
		{name: "retention", key: "retention:cpu", want: true},
		{name: "user_key", key: "mykey", want: false},
		{name: "user_prefixed", key: "user:1", want: false},
		{name: "empty", key: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInternalKey(tt.key); got != tt.want {
				t.Errorf("isInternalKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsInternalKey_BoundaryPrefixes(t *testing.T) {
	// Ensure partial prefix match doesn't falsely trigger.
	if isInternalKey("__sy:foo") {
		t.Error("__sy: should not match __sys: prefix")
	}
	if isInternalKey("t:foo") {
		t.Error("t: should not match ts: prefix")
	}
}

// ---------------------------------------------------------------------------
// Engine interface compliance
// ---------------------------------------------------------------------------

func TestMVCCEngine_ImplementsEngine(t *testing.T) {
	var _ Engine = (*MVCCEngine)(nil)
}

// ---------------------------------------------------------------------------
// Lookup: LeaseTimeToLive not found
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_LeaseTimeToLiveNotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	_, err = eng.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 99999})
	if err == nil {
		t.Fatal("expected error for non-existent lease")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Errorf("expected *InvalidQueryError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TS lookup error paths
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_TSRichQueryNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.TSRichQueryRequest{})
	if err == nil {
		t.Fatal("expected error for TS rich query without TS configured")
	}
}

func TestMVCCEngine_Lookup_TSGetRetentionNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.TSGetRetentionRequest{})
	if err == nil {
		t.Fatal("expected error for TS get retention without TS configured")
	}
}

func TestMVCCEngine_Lookup_TSListMetricsNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, err := eng.Lookup(context.Background(), service.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error for TS list metrics without TS configured")
	}
}

// ---------------------------------------------------------------------------
// Index update via MVCC put
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexUpdateViaPut(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create index first.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Put data with JSON field.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("u:1"), []byte(`{"name":"alice"}`), 0),
	})

	// Query index.
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
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

func TestMVCCEngine_IndexRemoveViaDelete(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Put then delete.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("u:1"), []byte(`{"name":"alice"}`), 0),
	})
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccDeleteEntry(3, []byte("u:1")),
	})

	// Index should be empty for alice.
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
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
// MVCCRangeQuery with options
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_MVCCRangeQueryWithRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	r1 := applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("rk1"), []byte("v1"), 0),
	})
	rev1 := int64(r1[0].Value)

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("rk2"), []byte("v2"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:    []byte("rk"),
		End:      []byte("rl"),
		Revision: rev1,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	// At rev1, only rk1 should exist.
	if len(result.KeyValues) != 1 {
		t.Errorf("expected 1 kv at rev1, got %d", len(result.KeyValues))
	}
}

func TestMVCCEngine_Lookup_MVCCRangeQueryCountOnly(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:     []byte("a"),
		End:       []byte("c"),
		CountOnly: true,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	if result.KeyValues != nil {
		t.Errorf("KeyValues: got %v, want nil for CountOnly", result.KeyValues)
	}
	if result.Count != 2 {
		t.Errorf("Count: got %d, want 2", result.Count)
	}
	if result.Revision != 2 {
		t.Errorf("Revision: got %d, want 2", result.Revision)
	}
}

// TestMVCCEngine_Lookup_MVCCRangeQuery_SortThreaded verifies the engine
// threads MVCCRangeQuery.SortOrder/SortTarget into the store's range scan,
// reproducing (as a regression guard) the bug where these fields existed on
// neither the query type nor the engine's option translation, causing
// --sort-order/--sort-by to be silently dropped.
func TestMVCCEngine_Lookup_MVCCRangeQuery_SortThreaded(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:      []byte("a"),
		End:        []byte("d"),
		SortOrder:  service.SortDescend,
		SortTarget: service.SortByKey,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	if len(result.KeyValues) != 3 {
		t.Fatalf("expected 3 kvs, got %d", len(result.KeyValues))
	}
	wantOrder := []string{"c", "b", "a"}
	for i, want := range wantOrder {
		if got := string(result.KeyValues[i].Key); got != want {
			t.Fatalf("KeyValues[%d]: got %q, want %q (descending key order)", i, got, want)
		}
	}
}

func TestMVCCEngine_Lookup_MVCCRangeQueryKeysOnly(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
	})

	got, err := eng.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:    []byte("a"),
		End:      []byte("b"),
		KeysOnly: true,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(*service.MVCCRangeResult)
	if len(result.KeyValues) != 1 {
		t.Errorf("expected 1 kv, got %d", len(result.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// TS update error paths
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_TSWriteNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSWrite("cpu", time.Now().UnixNano(), 42.0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error without TS configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for TS write without TS configured")
	}
}

func TestMVCCEngine_Update_TSWritePointNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSWritePoint("cpu", time.Now().UnixNano(), 42.0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error without TS configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for TS write point without TS configured")
	}
}

func TestMVCCEngine_Update_TSDeleteSeriesNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu", 0, 0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error without TS configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for TS delete series without TS configured")
	}
}

func TestMVCCEngine_Update_TSSetRetentionNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSSetRetention("cpu", 3600_000_000_000, 60_000_000_000, 0))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error without TS configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for TS set retention without TS configured")
	}
}

func TestMVCCEngine_Update_TSWriteBatchNoTS(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: time.Now().UnixNano(), Value: 42.0},
	}))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error without TS configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for TS write batch without TS configured")
	}
}

// ---------------------------------------------------------------------------
// Lookup: Lease revoke with no lessor
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseRevokeNoLessor(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(1))
	entries := []sm.Entry{{Index: 1, Cmd: revokeCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload in result with no lessor")
	}
}

// ---------------------------------------------------------------------------
// Store accessor
// ---------------------------------------------------------------------------

func TestMVCCEngine_Store(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.Store() == nil {
		t.Fatal("Store() should not be nil")
	}
}

func TestMVCCEngine_Lessor_Nil(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.Lessor() != nil {
		t.Error("expected nil Lessor when not configured")
	}
}

// ---------------------------------------------------------------------------
// Base KV delete with lessor detach
// ---------------------------------------------------------------------------

func TestMVCCEngine_BaseKVDeleteWithLessor(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("lk"), []byte("lv"), leaseID),
	})

	// Base KV delete should detach key from lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVDeleteEntry(3, "lk"),
	})

	// Key should be gone.
	got, err := eng.Lookup(context.Background(), []byte("lk"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after base KV delete, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// MVCC delete with lessor detach
// ---------------------------------------------------------------------------

func TestMVCCEngine_MVCCDeleteWithLessorDetach(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("lk2"), []byte("lv2"), leaseID),
	})

	// MVCC delete should detach key from lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccDeleteEntry(3, []byte("lk2")),
	})

	// Key should be gone.
	got, err := eng.Lookup(context.Background(), []byte("lk2"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after MVCC delete with lease, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Transaction with delete ops
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_DeleteOp(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put initial value.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("txdel"), []byte("v1"), 0),
	})

	// Transaction: no compares (always succeed), success ops = delete.
	compares := [][]byte{}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("txdel"), nil, nil, 0)),
	}
	failureOps := [][]byte{}

	txnCmd := mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	// Verify deleted.
	got, err := eng.Lookup(context.Background(), []byte("txdel"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after transaction delete, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Transaction with lease in ops
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_PutWithLease(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Transaction with put + leaseID.
	compares := [][]byte{}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txlk"), []byte("txlv"), nil, leaseID)),
	}
	failureOps := [][]byte{}

	txnCmd := mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("txlk")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kvResult := got.(*service.KeyValue)
	if string(kvResult.Value) != "txlv" {
		t.Errorf("value = %q, want %q", kvResult.Value, "txlv")
	}
}

// ---------------------------------------------------------------------------
// Transaction delete with lease detach
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_DeleteWithLeaseDetach(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("tdlk"), []byte("tdlv"), leaseID),
	})

	// Transaction: delete with lease detach.
	compares := [][]byte{}
	successOps := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("tdlk"), nil, nil, 0)),
	}
	txnCmd := mustEncode(protocol.EncodeTransaction(compares, successOps, nil))
	entries := []sm.Entry{{Index: 3, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Batch with lease ops
// ---------------------------------------------------------------------------

func TestMVCCEngine_BatchWithLeaseOps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Batch: put with lease, then delete.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("blk"), []byte("blv"), nil, leaseID)),
	}
	batchCmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 2, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("batch: %v", err)
	}

	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("blk")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil after batch put")
	}
}

// ---------------------------------------------------------------------------
// Lease revoke with attached keys and watch
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseRevokeWithWatch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr), WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("wlk"), []byte("wlv"), leaseID),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, werr := hub.Watch(ctx, "wlk")
	if werr != nil {
		t.Fatalf("Watch: %v", werr)
	}

	// Revoke.
	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(leaseID))
	revokeEntries := []sm.Entry{{Index: 3, Cmd: revokeCmd}}
	revokeResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), revokeEntries, revokeResults); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected delete event from lease revoke")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for lease revoke event")
	}
}

// ---------------------------------------------------------------------------
// Base KV put error path coverage
// ---------------------------------------------------------------------------

func TestMVCCEngine_BaseKVPut_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create a command with OpPut but invalid payload (too short).
	cmd := []byte{kv.OpPut, 0x00} // only 2 bytes, needs at least 3
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid base KV put payload")
	}
}

func TestMVCCEngine_BaseKVDelete_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create a command with OpDelete but invalid payload.
	cmd := []byte{kv.OpDelete, 0x00} // only 2 bytes, needs at least 3
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid base KV delete payload")
	}
}

// ---------------------------------------------------------------------------
// MVCC put with lessor and previous key with lease
// ---------------------------------------------------------------------------

func TestMVCCEngine_MVCCPut_LeaseAttach(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Grant lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantEntries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	grantResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), grantEntries, grantResults); err != nil {
		t.Fatalf("grant: %v", err)
	}
	leaseID := int64(grantResults[0].Value)

	// Put with lease.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("lak"), []byte("lav"), leaseID),
	})

	// Verify the key is attached to the lease.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("lease should exist")
	}
}

// ---------------------------------------------------------------------------
// tsTimestampFromNano
// ---------------------------------------------------------------------------

func TestTsTimestampFromNano(t *testing.T) {
	ts := tsTimestampFromNano(1000000000)
	if ts.Unix() != 1 {
		t.Errorf("expected Unix 1, got %d", ts.Unix())
	}
}

func TestTsTimestampFromNano_Zero(t *testing.T) {
	ts := tsTimestampFromNano(0)
	if ts.Unix() != 0 || ts.Nanosecond() != 0 {
		t.Errorf("expected zero time, got %v", ts)
	}
}

// ---------------------------------------------------------------------------
// Update with batch that has an unsupported op in decoded ops
// ---------------------------------------------------------------------------

func TestMVCCEngine_BatchUnsupportedOp(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

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
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// Compact error path
// ---------------------------------------------------------------------------

func TestMVCCEngine_Compact_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Compact with truncated payload.
	cmd := []byte{protocol.OpMVCCCompact, 0x01, 0x02} // too short for int64
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid compact payload")
	}
}

// ---------------------------------------------------------------------------
// Lookup: system key prefix in listKeys
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_ListQuery_SystemPrefix(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Put a system key and a user key.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:users:admin"), []byte("admin"), 0),
		mvccPutEntry(2, []byte("user:1"), []byte("alice"), 0),
	})

	// List with __sys: prefix should include system keys.
	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "__sys:"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := got.([]string)
	if len(keys) != 1 {
		t.Errorf("expected 1 system key, got %d: %v", len(keys), keys)
	}

	// List without prefix should exclude system keys.
	got2, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: ""})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys2 := got2.([]string)
	found := false
	for _, k := range keys2 {
		if k == "__sys:users:admin" {
			found = true
			break
		}
	}
	if found {
		t.Error("system key should be excluded from non-system list")
	}
}

// ---------------------------------------------------------------------------
// Lookup: the relational keyspace
// ---------------------------------------------------------------------------

// TestMVCCEngine_Lookup_GuardsTheRelationalKeyspace asserts both halves of the
// guard at the engine layer: an unrooted enumeration must not disclose
// relational rows, index entries, or sequences, while an enumeration rooted at
// the table's own prefix -- which is how a table scan reaches this engine --
// must return them. A guard that got only the first half would turn every
// table scan into a silent zero-row result.
func TestMVCCEngine_Lookup_GuardsTheRelationalKeyspace(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("user data"), 0),
		mvccPutEntry(2, []byte("__tbl:shop:1/row"), []byte("classified"), 0),
		mvccPutEntry(3, []byte("__idx:shop:1:uq_e/a@b"), []byte("pk"), 0),
		mvccPutEntry(4, []byte("__seq:shop:__tableid"), []byte("counter"), 0),
	})

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: ""})
	if err != nil {
		t.Fatalf("Lookup list: %v", err)
	}
	keys := got.([]string)
	if len(keys) != 1 || keys[0] != "app/a" {
		t.Fatalf("unrooted list = %v, want only the user key", keys)
	}

	scanned, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{})
	if err != nil {
		t.Fatalf("Lookup scan: %v", err)
	}
	pairs := scanned.(map[string][]byte)
	if len(pairs) != 1 {
		t.Fatalf("unrooted scan = %v, want only the user key", pairs)
	}

	rows, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("__tbl:shop:1/"),
	})
	if err != nil {
		t.Fatalf("Lookup rows: %v", err)
	}
	rowPairs := rows.(map[string][]byte)
	if string(rowPairs["__tbl:shop:1/row"]) != "classified" {
		t.Fatalf("the table's own scan returned %v -- the guard is hiding the relational layer from its own rows", rowPairs)
	}

	idx, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "__idx:shop:1:uq_e/"})
	if err != nil {
		t.Fatalf("Lookup index entries: %v", err)
	}
	idxKeys := idx.([]string)
	if len(idxKeys) != 1 || idxKeys[0] != "__idx:shop:1:uq_e/a@b" {
		t.Fatalf("the index's own listing = %v, want its single entry", idxKeys)
	}

	// The sequence namespace has no enumerating owner, so naming it discloses
	// nothing even though it is asked for outright.
	seq, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "__seq:"})
	if err != nil {
		t.Fatalf("Lookup sequences: %v", err)
	}
	if seqKeys := seq.([]string); len(seqKeys) != 0 {
		t.Fatalf("sequence listing = %v, want nothing", seqKeys)
	}
}

// ---------------------------------------------------------------------------
// PrefixQuery with system key prefix
// ---------------------------------------------------------------------------

func TestMVCCEngine_Lookup_PrefixQuery_SystemPrefix(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:roles:admin"), []byte("role"), 0),
	})

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("__sys:")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := got.(map[string][]byte)
	if len(result) != 1 {
		t.Errorf("expected 1 system key, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Base KV put with hub but no indexes
// ---------------------------------------------------------------------------

func TestMVCCEngine_BaseKVPut_WithHub(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, werr := hub.Watch(ctx, "bkvh")
	if werr != nil {
		t.Fatalf("Watch: %v", werr)
	}

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "bkvh", "val")})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for base KV put event")
	}
}

// ---------------------------------------------------------------------------
// Base KV delete with hub
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Time-series with pebble.DB
// ---------------------------------------------------------------------------

func newPebbleTS(t *testing.T) (*pebble.DB, *timeseries.Storage) {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	ts, err := timeseries.New(db)
	if err != nil {
		t.Fatalf("timeseries.New: %v", err)
	}
	return db, ts
}

func TestMVCCEngine_TSWrite(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now().UnixNano()
	tsCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now, 42.5, map[string]string{"host": "a"}))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS write: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("result = %d, want 1", results[0].Value)
	}
}

func TestMVCCEngine_TSWritePoint(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now().UnixNano()
	tsCmd := mustEncode(protocol.EncodeTSWritePoint("cpu.usage", now, 55.0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS write point: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("result = %d, want 1", results[0].Value)
	}
}

func TestMVCCEngine_TSWriteBatch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now().UnixNano()
	tsCmd := mustEncode(protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: now, Value: 42.0, Tags: map[string]string{"host": "a"}},
		{Metric: "mem", Timestamp: now, Value: 80.0, Tags: map[string]string{"host": "a"}},
	}))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS write batch: %v", err)
	}
	if results[0].Value != 2 {
		t.Errorf("result = %d, want 2", results[0].Value)
	}
}

func TestMVCCEngine_TSDeleteSeries(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now().UnixNano()

	// Write a data point first.
	writeCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now, 42.5, nil))
	wEntries := []sm.Entry{{Index: 1, Cmd: writeCmd}}
	wResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), wEntries, wResults); err != nil {
		t.Fatalf("TS write: %v", err)
	}

	// Delete series.
	delCmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu.usage", now-1000, now+1000, nil))
	entries := []sm.Entry{{Index: 2, Cmd: delCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS delete series: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("result = %d, want 1", results[0].Value)
	}
}

func TestMVCCEngine_TSSetRetention(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	tsCmd := mustEncode(protocol.EncodeTSSetRetention("cpu.usage", int64(24*time.Hour), int64(time.Minute), 0))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS set retention: %v", err)
	}
	if results[0].Value != 1 {
		t.Errorf("result = %d, want 1", results[0].Value)
	}
}

func TestMVCCEngine_TSQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now()

	// Write a data point.
	writeCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now.UnixNano(), 42.5, nil))
	wEntries := []sm.Entry{{Index: 1, Cmd: writeCmd}}
	wResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), wEntries, wResults); err != nil {
		t.Fatalf("TS write: %v", err)
	}

	// Query.
	got, err := eng.Lookup(context.Background(), service.TSQueryRequest{
		Metric: "cpu.usage",
		Start:  now.Add(-time.Minute),
		End:    now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("TS query: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil query result")
	}
}

func TestMVCCEngine_TSAggregate(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now()

	// Write data points.
	for i := 0; i < 3; i++ {
		writeCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now.Add(time.Duration(i)*time.Second).UnixNano(), float64(40+i), nil))
		wEntries := []sm.Entry{{Index: uint64(i + 1), Cmd: writeCmd}}
		wResults := make([]sm.Result, 1)
		if err := eng.Update(context.Background(), wEntries, wResults); err != nil {
			t.Fatalf("TS write %d: %v", i, err)
		}
	}

	// Aggregate.
	got, err := eng.Lookup(context.Background(), service.TSAggregateRequest{
		Metric:   "cpu.usage",
		Start:    now.Add(-time.Minute),
		End:      now.Add(time.Minute),
		Func:     service.AggAvg,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("TS aggregate: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil aggregate result")
	}
}

func TestMVCCEngine_TSGetRetention(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Set retention first.
	tsCmd := mustEncode(protocol.EncodeTSSetRetention("cpu.usage", int64(24*time.Hour), int64(time.Minute), 0))
	entries := []sm.Entry{{Index: 1, Cmd: tsCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("set retention: %v", err)
	}

	// Get retention.
	got, err := eng.Lookup(context.Background(), service.TSGetRetentionRequest{Metric: "cpu.usage"})
	if err != nil {
		t.Fatalf("get retention: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil retention")
	}
}

func TestMVCCEngine_TSListMetrics(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Write data points for different metrics.
	now := time.Now().UnixNano()
	writeCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now, 42.5, nil))
	wEntries := []sm.Entry{{Index: 1, Cmd: writeCmd}}
	wResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), wEntries, wResults); err != nil {
		t.Fatalf("TS write: %v", err)
	}

	got, err := eng.Lookup(context.Background(), service.TSListMetricsRequest{
		Prefix: "cpu",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list metrics: %v", err)
	}
	metrics := got.([]string)
	if len(metrics) < 1 {
		t.Errorf("expected at least 1 metric, got %d", len(metrics))
	}
}

// ---------------------------------------------------------------------------
// WithMVCCPebbleDB and WithMVCCTimeSeries options
// ---------------------------------------------------------------------------

func TestMVCCEngine_WithMVCCPebbleDB(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCPebbleDB(db))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Create index -- should use PebbleIndex.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}
}

func TestMVCCEngine_WithMVCCTimeSeries(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	if eng.TimeSeries() != ts {
		t.Error("TimeSeries() does not match configured TS")
	}
}

// ---------------------------------------------------------------------------
// TSDeleteSeries with zero timestamps
// ---------------------------------------------------------------------------

func TestMVCCEngine_TSDeleteSeries_ZeroTimestamps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Delete with zero timestamps (deletes all).
	delCmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu.usage", 0, 0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: delCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("TS delete series: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TSRichQuery with TS configured
// ---------------------------------------------------------------------------

func TestMVCCEngine_TSRichQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	now := time.Now()

	// Write a point.
	writeCmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", now.UnixNano(), 42.5, nil))
	wEntries := []sm.Entry{{Index: 1, Cmd: writeCmd}}
	wResults := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), wEntries, wResults); err != nil {
		t.Fatalf("TS write: %v", err)
	}

	// Rich query.
	got, err := eng.Lookup(context.Background(), service.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu.usage",
			StartTime: now.Add(-time.Minute),
			EndTime:   now.Add(time.Minute),
			GroupBy:   []string{},
		},
	})
	if err != nil {
		t.Fatalf("TS rich query: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil rich query result")
	}
}

// ---------------------------------------------------------------------------
// Lease grant with successful grant
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseGrant_Success(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := mustEncode(protocol.EncodeLeaseGrant(30))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("lease grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	// Verify lease exists.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("lease should exist after grant")
	}
	if l.GrantedTTL != 30 {
		t.Errorf("GrantedTTL = %d, want 30", l.GrantedTTL)
	}
}

// ---------------------------------------------------------------------------
// Lease grant with invalid TTL
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseGrant_InvalidTTL(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := mustEncode(protocol.EncodeLeaseGrant(0)) // invalid TTL
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err = eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error for zero TTL: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload for zero TTL")
	}
}

// ---------------------------------------------------------------------------
// Lease keepalive with non-existent lease
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseKeepAlive_NonExistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// KeepAlive on a non-existent lease is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so keepalive-not-found must succeed without error.
	kaCmd := mustEncode(protocol.EncodeLeaseKeepAlive(99999))
	entries := []sm.Entry{{Index: 1, Cmd: kaCmd}}
	results := make([]sm.Result, 1)
	if err = eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("keepalive non-existent: expected no-op success, got error: %v", err)
	}
	// Value is 0 when the lease is not found (no lease ID echoed back).
	if results[0].Value != 0 {
		t.Fatalf("keepalive non-existent: expected result 0, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Lease revoke with non-existent lease
// ---------------------------------------------------------------------------

func TestMVCCEngine_LeaseRevoke_NonExistent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Revoking a non-existent lease is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so revoke-not-found must succeed without error.
	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(99999))
	entries := []sm.Entry{{Index: 1, Cmd: revokeCmd}}
	results := make([]sm.Result, 1)
	if err = eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("revoke non-existent: expected no-op success, got error: %v", err)
	}
	// Value is 0: no attached keys were deleted.
	if results[0].Value != 0 {
		t.Fatalf("revoke non-existent: expected result 0, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// Index drop error paths in MVCC
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexDrop_ClearAndClose(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create index, put data, then drop.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_x", "x", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("create: %v", err)
	}

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("d:1"), []byte(`{"x":"val"}`), 0),
	})

	dropCmd := mustEncode(protocol.EncodeIndexDrop("by_x"))
	entries2 := []sm.Entry{{Index: 3, Cmd: dropCmd}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// Index list should be empty.
	got, err := eng.Lookup(context.Background(), service.IndexListRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := got.([]string)
	if len(names) != 0 {
		t.Errorf("expected 0 indexes, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// Update with invalid decode
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_InvalidCommand(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Empty command.
	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// ---------------------------------------------------------------------------
// MVCC put error path (invalid payload)
// ---------------------------------------------------------------------------

func TestMVCCEngine_MVCCPut_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// OpMVCCPut with truncated payload.
	cmd := []byte{protocol.OpMVCCPut, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC put payload")
	}
}

// ---------------------------------------------------------------------------
// MVCC Compact with invalid revision
// ---------------------------------------------------------------------------

func TestMVCCEngine_Compact_HighRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Compact at a revision higher than any data -- should not error.
	compactCmd := mustEncode(protocol.EncodeMVCCCompact(999))
	entries := []sm.Entry{{Index: 1, Cmd: compactCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("compact high revision: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCC index create/drop error paths
// ---------------------------------------------------------------------------

func TestMVCCEngine_IndexCreate_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Invalid index create payload.
	cmd := []byte{protocol.OpIndexCreate, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid index create payload")
	}
}

func TestMVCCEngine_IndexDrop_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpIndexDrop, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid index drop payload")
	}
}

// ---------------------------------------------------------------------------
// TS error paths with invalid payloads
// ---------------------------------------------------------------------------

func TestMVCCEngine_TSWrite_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpTSWrite, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write payload")
	}
}

func TestMVCCEngine_TSWriteBatch_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpTSWriteBatch, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write batch payload")
	}
}

func TestMVCCEngine_TSWritePoint_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpTSWritePoint, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write point payload")
	}
}

func TestMVCCEngine_TSDeleteSeries_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpTSDeleteSeries, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS delete series payload")
	}
}

func TestMVCCEngine_TSSetRetention_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	db, ts := newPebbleTS(t)
	defer func() { _ = db.Close() }()

	eng, err := NewMVCCEngine(stg, WithMVCCTimeSeries(ts))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpTSSetRetention, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS set retention payload")
	}
}

func TestMVCCEngine_MVCCDelete_InvalidPayload(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	cmd := []byte{protocol.OpMVCCDelete, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC delete payload")
	}
}

func TestMVCCEngine_BaseKVDelete_WithHub(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutEntry(1, "bkvd", "val")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, werr := hub.Watch(ctx, "bkvd")
	if werr != nil {
		t.Fatalf("Watch: %v", werr)
	}

	applyMVCCEntries(t, eng, []sm.Entry{baseKVDeleteEntry(2, "bkvd")})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected event")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for base KV delete event")
	}
}

// ---------------------------------------------------------------------------
// Reproducer: base KV put → index create (backfill) → query
// This simulates the exact GUI flow where the user puts JSON data via
// the KV tab, then creates an index, then queries it.
// ---------------------------------------------------------------------------

func TestMVCCEngine_BaseKVPut_ThenIndexCreate_ThenQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Step 1: Put data via base KV (same as GUI "Raw KV" tab).
	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVPutEntry(1, "mykey", `{"test":"value","f2": 123}`),
	})

	// Step 2: Create index on field "f2" (same as GUI "Create Index").
	idxCmd := mustEncode(protocol.EncodeIndexCreate("f2", "f2", false, false, false))
	entries := []sm.Entry{{Index: 2, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Step 3: Query the index for value "123" (same as GUI "Query").
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "f2",
		Value:     []byte("123"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) == 0 {
		t.Fatal("expected at least 1 ID from index query, got 0")
	}
}

func TestMVCCEngine_BaseKVPut_AfterIndexCreate_ThenQuery(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Step 1: Create index on field "f2" FIRST.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("f2", "f2", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: idxCmd}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Step 2: Put data via base KV (index-on-write should populate index).
	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVPutEntry(2, "mykey", `{"test":"value","f2": 123}`),
	})

	// Step 3: Query the index for value "123".
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "f2",
		Value:     []byte("123"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) == 0 {
		t.Fatal("expected at least 1 ID from index query, got 0")
	}
}

// ---------------------------------------------------------------------------
// Unique constraint enforcement (Bug Fix)
// ---------------------------------------------------------------------------

// TestMVCCEngine_UniqueIndex_ViolationAbortsMVCCPut verifies that writing a
// second document with the same unique-indexed field value via OpMVCCPut
// returns a UniqueViolationError and does NOT persist the document.
func TestMVCCEngine_UniqueIndex_ViolationAbortsMVCCPut(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create a unique index on field "email".
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_email", "email", true, false, false))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// First document: must succeed.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("user:1"), []byte(`{"email":"a@example.com"}`), 0),
	})

	// Second document with the same email: must be rejected as a non-fatal
	// app-error (a unique-index violation is a deterministic consequence of
	// valid committed input, so Update must not return a Go error).
	results2 := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{
		mvccPutEntry(3, []byte("user:2"), []byte(`{"email":"a@example.com"}`), 0),
	}, results2)
	if err != nil {
		t.Fatalf("Update must not return a fatal error on unique violation: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results2[0].Data); !ok {
		t.Fatal("expected app-error payload for unique constraint violation")
	}

	// user:2 must NOT exist in the store (batch was not committed).
	got, lookupErr := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("user:2")})
	if lookupErr == nil && got != nil {
		t.Error("user:2 was persisted despite unique constraint violation")
	}
}

// TestMVCCEngine_UniqueIndex_ViolationAbortsBaseKVPut verifies that writing
// a second document via the base kv OpPut path also enforces unique constraints.
func TestMVCCEngine_UniqueIndex_ViolationAbortsBaseKVPut(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Create a unique index on field "uid".
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_uid", "uid", true, false, false))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// First document via base KV put.
	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVPutEntry(2, "doc:1", `{"uid":"x42"}`),
	})

	// Duplicate via base KV put: must be rejected as a non-fatal app-error.
	results2 := make([]sm.Result, 1)
	err := eng.Update(context.Background(), []sm.Entry{
		baseKVPutEntry(3, "doc:2", `{"uid":"x42"}`),
	}, results2)
	if err != nil {
		t.Fatalf("Update must not return a fatal error on unique violation: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results2[0].Data); !ok {
		t.Fatal("expected app-error payload for unique constraint violation")
	}

	// doc:2 must NOT exist in the store (batch was not committed).
	got, lookupErr := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("doc:2")})
	if lookupErr == nil && got != nil {
		t.Error("doc:2 was persisted despite unique constraint violation")
	}
}

// TestMVCCEngine_UniqueIndex_UpdateSameKeyAllowed verifies that updating an
// existing key's indexed field value does not produce a false violation when
// the key is replacing its own previous index entry.
func TestMVCCEngine_UniqueIndex_UpdateSameKeyAllowed(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_email", "email", true, false, false))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, results); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Initial write.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("user:1"), []byte(`{"email":"old@example.com"}`), 0),
	})

	// Update the same key to a new email value: must not return an error.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(3, []byte("user:1"), []byte(`{"email":"new@example.com"}`), 0),
	})

	// Old email must no longer be indexed.
	oldResult, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "by_email",
		Value:     []byte("old@example.com"),
	})
	if err != nil {
		t.Fatalf("index query (old): %v", err)
	}
	if len(oldResult.([][]byte)) != 0 {
		t.Error("old email still indexed after update")
	}

	// New email must be indexed.
	newResult, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "by_email",
		Value:     []byte("new@example.com"),
	})
	if err != nil {
		t.Fatalf("index query (new): %v", err)
	}
	if len(newResult.([][]byte)) != 1 {
		t.Errorf("expected 1 entry for new email, got %d", len(newResult.([][]byte)))
	}
}

// ---------------------------------------------------------------------------
// Numeric index E2E tests
// ---------------------------------------------------------------------------

// newMVCCEngineWithPebble creates an MVCCEngine backed by an in-memory storage
// and a PebbleDB instance so that numeric indexes use PebbleIndex.
func newMVCCEngineWithPebble(t *testing.T) (*MVCCEngine, func()) {
	t.Helper()
	stg := memory.NewStorage()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	eng, err := NewMVCCEngine(stg, WithMVCCPebbleDB(db))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	return eng, func() {
		_ = eng.Close(context.Background())
		_ = db.Close()
		_ = stg.Close()
	}
}

// TestMVCCEngine_NumericIndex_ScanRange verifies that a numeric index makes
// range scans numerically correct. Inserting 5, 123, and 1234 and scanning
// [1, 222) must return only 5 and 123 — NOT 1234, which a lexicographic scan
// would incorrectly include because "1234" < "222".
func TestMVCCEngine_NumericIndex_ScanRange(t *testing.T) {
	eng, cleanup := newMVCCEngineWithPebble(t)
	defer cleanup()

	// Create a numeric index on field "f2".
	idxCmd := mustEncode(protocol.EncodeIndexCreate("f2num", "f2", false, false, true))
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Insert documents with different numeric values.
	entries := []sm.Entry{
		mvccPutEntry(2, []byte("doc-5"), []byte(`{"f2": 5}`), 0),
		mvccPutEntry(3, []byte("doc-123"), []byte(`{"f2": 123}`), 0),
		mvccPutEntry(4, []byte("doc-1234"), []byte(`{"f2": 1234}`), 0),
	}
	applyMVCCEntries(t, eng, entries)

	// Query "123" — must return doc-123.
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "f2num",
		Value:     []byte("123"),
	})
	if err != nil {
		t.Fatalf("index query(123): %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 1 {
		t.Fatalf("query(123): want 1 result, got %d", len(ids))
	}
	if string(ids[0]) != "doc-123" {
		t.Errorf("query(123): got %s, want doc-123", ids[0])
	}

	// Scan [1, 222) — must include 5 and 123, must NOT include 1234.
	scanResult, err := eng.Lookup(context.Background(), service.IndexScanRequest{
		IndexName: "f2num",
		Start:     []byte("1"),
		End:       []byte("222"),
	})
	if err != nil {
		t.Fatalf("Scan[1,222): %v", err)
	}
	entries222 := scanResult.(map[string][][]byte)

	if _, has := entries222["1234"]; has {
		t.Error("Scan[1,222) incorrectly included 1234 (lexicographic comparison bug)")
	}
	if _, has := entries222["123"]; !has {
		t.Error("Scan[1,222) missing 123")
	}
	if _, has := entries222["5"]; !has {
		t.Error("Scan[1,222) missing 5")
	}
	if len(entries222) != 2 {
		t.Errorf("Scan[1,222) returned %d entries, want 2: %v", len(entries222), entries222)
	}

	// Scan [1, 2000) — must include all three.
	scanWide, err := eng.Lookup(context.Background(), service.IndexScanRequest{
		IndexName: "f2num",
		Start:     []byte("1"),
		End:       []byte("2000"),
	})
	if err != nil {
		t.Fatalf("Scan[1,2000): %v", err)
	}
	wide := scanWide.(map[string][][]byte)

	for _, v := range []string{"5", "123", "1234"} {
		if _, ok := wide[v]; !ok {
			t.Errorf("Scan[1,2000) missing %s; result: %v", v, wide)
		}
	}
	if len(wide) != 3 {
		t.Errorf("Scan[1,2000) returned %d entries, want 3: %v", len(wide), wide)
	}
}

// TestMVCCEngine_NumericIndex_NonNumericSkipped verifies that non-numeric values
// are silently skipped during indexing and do not cause errors.
func TestMVCCEngine_NumericIndex_NonNumericSkipped(t *testing.T) {
	eng, cleanup := newMVCCEngineWithPebble(t)
	defer cleanup()

	idxCmd := mustEncode(protocol.EncodeIndexCreate("numskip", "score", false, false, true))
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Insert a doc with a non-numeric score value; must not return an error.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(2, []byte("doc-invalid"), []byte(`{"score": "not-a-number"}`), 0),
	})

	// The index must be empty for this value.
	got, err := eng.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "numskip",
		Value:     []byte("not-a-number"),
	})
	if err != nil {
		t.Fatalf("index query: %v", err)
	}
	ids := got.([][]byte)
	if len(ids) != 0 {
		t.Errorf("expected 0 results for non-numeric value, got %d", len(ids))
	}
}
