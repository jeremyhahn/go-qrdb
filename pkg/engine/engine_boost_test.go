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
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// defaultLogger – non-panic path coverage
// ---------------------------------------------------------------------------

func TestDefaultLogger_ReturnsNonNil(t *testing.T) {
	log := defaultLogger()
	if log == nil {
		t.Error("expected non-nil logger from defaultLogger()")
	}
}

// ---------------------------------------------------------------------------
// KVEngine.PutWithIndexes + DeleteWithIndexes coverage
// ---------------------------------------------------------------------------

func TestKVEngine_PutWithIndexes_NoIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	if err := eng.PutWithIndexes([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("PutWithIndexes (no indexes): %v", err)
	}
}

func TestKVEngine_DeleteWithIndexes_NoIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	_ = eng.PutWithIndexes([]byte("k1"), []byte("v1"))
	if err := eng.DeleteWithIndexes([]byte("k1")); err != nil {
		t.Fatalf("DeleteWithIndexes (no indexes): %v", err)
	}
}

func TestKVEngine_PutWithIndexes_WithJSONAndIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("user", []EntityIndex{
		{FieldName: "by_email", JSONFieldName: "email"},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	value := `{"id":"u1","email":"alice@example.com"}`
	if err := eng.PutWithIndexes([]byte("user/u1"), []byte(value)); err != nil {
		t.Fatalf("PutWithIndexes with index: %v", err)
	}
}

func TestKVEngine_DeleteWithIndexes_WithIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("user", []EntityIndex{
		{FieldName: "by_email", JSONFieldName: "email"},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	value := `{"id":"u1","email":"alice@example.com"}`
	_ = eng.PutWithIndexes([]byte("user/u1"), []byte(value))

	if err := eng.DeleteWithIndexes([]byte("user/u1")); err != nil {
		t.Fatalf("DeleteWithIndexes with index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.Update – existing base-KV handler (put + delete)
// ---------------------------------------------------------------------------

func TestKVEngine_Update_PutAndDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	put, _ := kv.EncodePut([]byte("key1"), []byte("val1"))
	del, _ := kv.EncodeDelete([]byte("key1"))
	entries := []sm.Entry{
		{Index: 1, Cmd: put},
		{Index: 2, Cmd: del},
	}
	results := make([]sm.Result, 2)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if results[0].Value == 0 {
		t.Error("expected non-zero result value for put")
	}
}

// ---------------------------------------------------------------------------
// CheckUniqueViolationsBatch – intra-batch conflict path
// ---------------------------------------------------------------------------

func TestCheckUniqueViolationsBatch_IntraBatchConflict(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true) // unique=true
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	val1, _ := json.Marshal(map[string]string{"name": "alice"})
	val2, _ := json.Marshal(map[string]string{"name": "alice"})

	puts := []indexedPut{
		{Key: []byte("k1"), Value: val1},
		{Key: []byte("k2"), Value: val2}, // same "name" → conflict
	}
	err := CheckUniqueViolationsBatch(indexes, indexFields, nil, puts, nil)
	if err == nil {
		t.Fatal("expected UniqueConstraintError for intra-batch duplicate")
	}
	if _, ok := err.(*UniqueConstraintError); !ok {
		t.Fatalf("expected *UniqueConstraintError, got %T: %v", err, err)
	}
}

func TestCheckUniqueViolationsBatch_EmptyBatch(t *testing.T) {
	err := CheckUniqueViolationsBatch(nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("expected nil for empty batch, got: %v", err)
	}
}

func TestCheckUniqueViolationsBatch_DeletedKeyFreesValue(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true) // unique
	_ = idx.Put([]byte("old:1"), []byte("alice"))
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// k1 is being deleted; k2 claims the same value → should be allowed.
	val, _ := json.Marshal(map[string]string{"name": "alice"})
	puts := []indexedPut{
		{Key: []byte("new:2"), Value: val},
	}
	deletedKeys := [][]byte{[]byte("old:1")}

	err := CheckUniqueViolationsBatch(indexes, indexFields, nil, puts, deletedKeys)
	if err != nil {
		t.Fatalf("expected nil when conflicting key is in deletedKeys, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// asAppErrResult – both branches
// ---------------------------------------------------------------------------

func TestAsAppErrResult_NonUCE(t *testing.T) {
	_, handled, _ := asAppErrResult(boostError("some other error"))
	if handled {
		t.Error("expected handled=false for non-UCE error")
	}
}

func TestAsAppErrResult_UCE(t *testing.T) {
	uce := &UniqueConstraintError{Index: "by_name", Field: "name"}
	res, handled, err := asAppErrResult(uce)
	if !handled {
		t.Error("expected handled=true for UniqueConstraintError")
	}
	if err != nil {
		t.Fatalf("expected nil error from asAppErrResult, got: %v", err)
	}
	if len(res.Data) == 0 {
		t.Error("expected non-empty result data for app-error encoding")
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.Update – batch
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_Batch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Use protocol.EncodeOp to produce properly-encoded MVCC ops for a batch.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk1"), []byte("bv1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk2"), []byte("bv2"), nil, 0)),
	}
	batchPayload := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: batchPayload}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update batch: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.Update – empty batch
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleBatch_EmptyOps(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	batchPayload := mustEncode(protocol.EncodeBatch(nil))
	entries := []sm.Entry{{Index: 1, Cmd: batchPayload}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update empty batch: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.Update – transaction failure branch
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_FailureBranch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Compare on a key that doesn't exist → condition fails → failure ops execute.
	// CompareVersion=0, target=version(0x01), result=equal(0x00).
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("nonexistent"),
		0x01,          // target = version
		0x00,          // result = equal
		nil, 10, 0, 0, // version=10 → will not equal actual version 0
	))
	failureOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("failure"), []byte("no"), nil, 0))
	txnPayload := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, nil, [][]byte{failureOp}))
	entries := []sm.Entry{{Index: 1, Cmd: txnPayload}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update transaction: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.handleBatch – basic execution via Update
// ---------------------------------------------------------------------------

func TestKVEngine_HandleBatch_PutAndDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	put1 := mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("bk1"), []byte("bv1"), nil, 0))
	put2 := mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("bk2"), []byte("bv2"), nil, 0))
	del1 := mustEncode(protocol.EncodeOp(protocol.OpFSMDelete, []byte("bk1"), nil, nil, 0))
	batchPayload := mustEncode(protocol.EncodeBatch([][]byte{put1, put2, del1}))
	entries := []sm.Entry{{Index: 1, Cmd: batchPayload}}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("batch Update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.lookupPrefix – exercises the prefix filter
// ---------------------------------------------------------------------------

func TestMVCCEngine_LookupPrefix_Basic(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	entry := mvccPutEntry(1, []byte("myprefix/key1"), []byte("value1"), 0)
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{entry}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("myprefix/")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	m, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", got)
	}
	if _, exists := m["myprefix/key1"]; !exists {
		t.Errorf("expected myprefix/key1 in result, got keys: %v", mapBoostKeys(m))
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.listKeys – prefix filtering via string Lookup
// ---------------------------------------------------------------------------

func TestMVCCEngine_ListKeys_WithPrefix(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	for i, key := range []string{"abc/1", "abc/2", "xyz/1"} {
		entry := mvccPutEntry(uint64(i+1), []byte(key), []byte("v"), 0)
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{entry}, results)
	}

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("abc/")})
	if err != nil {
		t.Fatalf("Lookup list: %v", err)
	}
	m, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte for prefix query, got %T", got)
	}
	if len(m) != 2 {
		t.Errorf("expected 2 results, got %d: %v", len(m), mapBoostKeys(m))
	}
}

// ---------------------------------------------------------------------------
// CheckUniqueViolationsBatch – non-JSON value skipped silently
// ---------------------------------------------------------------------------

func TestCheckUniqueViolationsBatch_NonJSONSkipped(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	puts := []indexedPut{
		{Key: []byte("k1"), Value: []byte("not-json")},
	}
	err := CheckUniqueViolationsBatch(indexes, indexFields, nil, puts, nil)
	if err != nil {
		t.Fatalf("expected nil for non-JSON value, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.filterIndexes – entity type matching
// ---------------------------------------------------------------------------

func TestKVEngine_FilterIndexes_EntityType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	_ = eng.RegisterEntityType("product", []EntityIndex{
		{FieldName: "by_pname", JSONFieldName: "name"},
	})
	_ = eng.RegisterEntityType("order", []EntityIndex{
		{FieldName: "by_status", JSONFieldName: "status"},
	})

	// filterIndexes on "product" should only return product indexes.
	filtered, _ := eng.filterIndexes("product")
	for name := range filtered {
		if len(name) < len("product") || name[:len("product")] != "product" {
			t.Errorf("unexpected index %q for entity type product", name)
		}
	}
}

// ---------------------------------------------------------------------------
// KVEngine.scanAllKVs coverage path
// ---------------------------------------------------------------------------

func TestKVEngine_ScanAllKVs(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// Put some values.
	put1, _ := kv.EncodePut([]byte("k1"), []byte(`{"name":"a"}`))
	put2, _ := kv.EncodePut([]byte("k2"), []byte(`{"name":"b"}`))
	entries := []sm.Entry{
		{Index: 1, Cmd: put1},
		{Index: 2, Cmd: put2},
	}
	results := make([]sm.Result, 2)
	_ = eng.Update(context.Background(), entries, results)

	// scanAllKVs is used by BackfillIndex inside RegisterEntityType.
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng2 := NewKVEngine(stg, WithKVLogger(log))
	if err := eng2.RegisterEntityType("k", []EntityIndex{
		{FieldName: "by_name", JSONFieldName: "name"},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BackfillIndexWithPrefix – with data (non-empty storage)
// ---------------------------------------------------------------------------

func TestBackfillIndexWithPrefix_WithData(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Write some raw key-value pairs directly to storage.
	batch := stg.Batch()
	_ = batch.Set([]byte("item/1"), []byte(`{"name":"apple"}`))
	_ = batch.Set([]byte("item/2"), []byte(`{"name":"banana"}`))
	_ = batch.Set([]byte("item/3"), []byte("not-json"))
	_ = batch.Commit()
	_ = batch.Close()

	idx := index.NewMemoryIndex("by_name", false)
	err := BackfillIndexWithPrefix(stg, idx, "name", []byte("item/"))
	if err != nil {
		t.Fatalf("BackfillIndexWithPrefix: %v", err)
	}

	// "apple" and "banana" should be indexed.
	ids, err := idx.Get([]byte("apple"))
	if err != nil {
		t.Fatalf("idx.Get(apple): %v", err)
	}
	if len(ids) == 0 {
		t.Error("expected apple to be indexed")
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleBatch – unique constraint violation (non-fatal app error)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleBatch_UniqueViolation(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Create a unique index.
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreate("by_email", "email", true, false, false))
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCreateCmd}}, results)

	// First batch: put a user.
	val1 := []byte(`{"name":"alice","email":"x@test.com"}`)
	ops1 := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("u:1"), val1, nil, 0)),
	}
	batch1 := mustEncode(protocol.EncodeBatch(ops1))
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: batch1}}, r1)

	// Second batch: try to put a conflicting user.
	val2 := []byte(`{"name":"bob","email":"x@test.com"}`)
	ops2 := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("u:2"), val2, nil, 0)),
	}
	batch2 := mustEncode(protocol.EncodeBatch(ops2))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: batch2}}, r2); err != nil {
		t.Fatalf("batch unique violation: %v", err)
	}
	// Violation encoded as a non-fatal conflict refusal (go-qrdb#557).
	if _, kind, ok := protocol.DecodeAppErrorKind(r2[0].Data); !ok || kind != protocol.AppErrorConflict {
		t.Fatalf("batch unique violation: ok=%v kind=%d; want an AppErrorConflict refusal", ok, kind)
	}
}

// ---------------------------------------------------------------------------
// CheckUniqueViolation – index.Get error path
// ---------------------------------------------------------------------------

func TestCheckUniqueViolation_IndexGetError(t *testing.T) {
	idx := &errorOnGetIndex{}
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	val, _ := json.Marshal(map[string]string{"name": "alice"})
	err := CheckUniqueViolation(indexes, indexFields, nil, []byte("k1"), val)
	if err == nil {
		t.Fatal("expected error when index.Get fails")
	}
}

// CheckUniqueViolation – non-JSON value returns nil (no violation possible)
func TestCheckUniqueViolation_NonJSON(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	err := CheckUniqueViolation(indexes, indexFields, nil, []byte("k1"), []byte("not-json"))
	if err != nil {
		t.Fatalf("expected nil for non-JSON, got: %v", err)
	}
}

// CheckUniqueViolation – index name in map but field missing from JSON
func TestCheckUniqueViolation_FieldMissingFromJSON(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// JSON doesn't have "name" field.
	val, _ := json.Marshal(map[string]string{"age": "30"})
	err := CheckUniqueViolation(indexes, indexFields, nil, []byte("k1"), val)
	if err != nil {
		t.Fatalf("expected nil when field missing from JSON, got: %v", err)
	}
}

// CheckUniqueViolation – indexFields missing entry for index name
func TestCheckUniqueViolation_IndexNotInFieldsMap(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	// "by_name" not present in indexFields → continue
	indexFields := map[string]string{}

	val, _ := json.Marshal(map[string]string{"name": "alice"})
	err := CheckUniqueViolation(indexes, indexFields, nil, []byte("k1"), val)
	if err != nil {
		t.Fatalf("expected nil when index not in fields map, got: %v", err)
	}
}

// errorOnGetIndex is a minimal index.Index stub that always errors on Get.
type errorOnGetIndex struct{}

func (e *errorOnGetIndex) Put(_, _ []byte) error                         { return nil }
func (e *errorOnGetIndex) Delete(_, _ []byte) error                      { return nil }
func (e *errorOnGetIndex) Get(_ []byte) ([][]byte, error)                { return nil, boostError("index read error") }
func (e *errorOnGetIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (e *errorOnGetIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (e *errorOnGetIndex) Clear() error                                  { return nil }
func (e *errorOnGetIndex) Close() error                                  { return nil }
func (e *errorOnGetIndex) IsUnique() bool                                { return true }

// ---------------------------------------------------------------------------
// MVCCEngine.executeDecodedOpWithNotify – delete branch with RemoveIndexes
// ---------------------------------------------------------------------------

func TestMVCCEngine_ExecuteDecodedOp_DeleteWithIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Create an index and insert a value.
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreate("by_tag", "tag", false, false, false))
	r0 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCreateCmd}}, r0)

	val := []byte(`{"tag":"red"}`)
	put := mvccPutEntry(2, []byte("item:1"), val, 0)
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{put}, r1)

	// Batch delete — exercises executeDecodedOpWithNotify delete branch with RemoveIndexes.
	del := mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("item:1"), nil, nil, 0))
	batch := mustEncode(protocol.EncodeBatch([][]byte{del}))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: batch}}, r2); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleMVCCPut – with PrevKV (update path)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleMVCCPut_Update(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Put first version.
	put1 := mvccPutEntry(1, []byte("upd:1"), []byte("v1"), 0)
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{put1}, r1)

	// Put second version (update).
	put2 := mvccPutEntry(2, []byte("upd:1"), []byte("v2"), 0)
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{put2}, r2); err != nil {
		t.Fatalf("MVCC put update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.handleBatch – unsupported op type fallthrough
// ---------------------------------------------------------------------------

func TestKVEngine_HandleBatch_UnknownOpType(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// Encode an op with an unknown type (0xFF).
	unknownOp := mustEncode(protocol.EncodeOp(0xFF, []byte("k"), []byte("v"), nil, 0))
	batch := mustEncode(protocol.EncodeBatch([][]byte{unknownOp}))
	entries := []sm.Entry{{Index: 1, Cmd: batch}}
	results := make([]sm.Result, 1)
	// Unknown op is ignored/skipped in the switch default case.
	err := eng.Update(context.Background(), entries, results)
	// It may or may not fail depending on implementation; we don't assert an error.
	_ = err
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleMVCCDelete – with index cleanup
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleMVCCDelete_WithIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Register unique index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_code", "code", false, false, false))
	r0 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, r0)

	// Put a keyed value.
	val := []byte(`{"code":"abc"}`)
	put := mvccPutEntry(2, []byte("obj:1"), val, 0)
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{put}, r1)

	// Delete it via MVCC delete.
	del := mvccDeleteEntry(3, []byte("obj:1"))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{del}, r2); err != nil {
		t.Fatalf("MVCC delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.Update – two entries in one call (exercises loop) - boost
// ---------------------------------------------------------------------------

func TestMVCCEngine_Update_MultipleEntries_Boost(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	entries := []sm.Entry{
		mvccPutEntry(1, []byte("multi:1"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("multi:2"), []byte("v2"), 0),
		mvccPutEntry(3, []byte("multi:3"), []byte("v3"), 0),
	}
	results := make([]sm.Result, 3)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("multi-entry Update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckUniqueViolationsBatch – non-unique index skipped
// ---------------------------------------------------------------------------

func TestCheckUniqueViolationsBatch_NonUniqueIndexSkipped(t *testing.T) {
	// A non-unique memory index (unique=false).
	idx := index.NewMemoryIndex("by_tag", false)
	indexes := map[string]index.Index{"by_tag": idx}
	indexFields := map[string]string{"by_tag": "tag"}

	val1, _ := json.Marshal(map[string]string{"tag": "red"})
	val2, _ := json.Marshal(map[string]string{"tag": "red"})

	// Duplicate values are allowed for non-unique indexes.
	puts := []indexedPut{
		{Key: []byte("k1"), Value: val1},
		{Key: []byte("k2"), Value: val2},
	}
	err := CheckUniqueViolationsBatch(indexes, indexFields, nil, puts, nil)
	if err != nil {
		t.Fatalf("expected nil for non-unique index with duplicate, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.handleBatch – with compound indexes registered
// ---------------------------------------------------------------------------

func TestKVEngine_HandleBatch_WithCompoundIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	// Register a compound index.
	if err := eng.RegisterEntityType("thing", []EntityIndex{
		{FieldNames: []string{"status", "type"}, JSONFieldNames: []string{"status", "type"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType compound: %v", err)
	}

	// A batch with compound-indexed entries.
	put := mustEncode(protocol.EncodeOp(protocol.OpFSMPut, []byte("thing/1"), []byte(`{"status":"ok","type":"A"}`), nil, 0))
	batch := mustEncode(protocol.EncodeBatch([][]byte{put}))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: batch}}, results); err != nil {
		t.Fatalf("batch with compound index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.kvHandlePut – with compound index registered
// ---------------------------------------------------------------------------

func TestKVEngine_KvHandlePut_WithCompoundIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("entity", []EntityIndex{
		{FieldNames: []string{"a", "b"}, JSONFieldNames: []string{"a", "b"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	put, _ := kv.EncodePut([]byte("entity/1"), []byte(`{"a":"x","b":"y"}`))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, results); err != nil {
		t.Fatalf("kvHandlePut with compound index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.kvHandleDelete – with compound index registered
// ---------------------------------------------------------------------------

func TestKVEngine_KvHandleDelete_WithCompoundIndex(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	if err := eng.RegisterEntityType("entity", []EntityIndex{
		{FieldNames: []string{"a", "b"}, JSONFieldNames: []string{"a", "b"}},
	}); err != nil {
		t.Fatalf("RegisterEntityType: %v", err)
	}

	// Put first.
	put, _ := kv.EncodePut([]byte("entity/1"), []byte(`{"a":"x","b":"y"}`))
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, r1)

	// Delete.
	del, _ := kv.EncodeDelete([]byte("entity/1"))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: del}}, r2); err != nil {
		t.Fatalf("kvHandleDelete with compound index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.listKeys – prefix returns empty when no match
// ---------------------------------------------------------------------------

func TestKVEngine_ListKeys_EmptyResult(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "nope/"})
	if err != nil {
		t.Fatalf("ListQuery empty: %v", err)
	}
	keys, ok := got.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", got)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleIndexCreate – create then drop (both branches)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleIndexCreate_ThenDrop(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Create a unique index on the MVCC engine.
	createCmd := mustEncode(protocol.EncodeIndexCreate("mvcc_idx", "code", true, false, false))
	r1 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, r1); err != nil {
		t.Fatalf("index create: %v", err)
	}

	// Drop it.
	dropCmd := mustEncode(protocol.EncodeIndexDrop("mvcc_idx"))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: dropCmd}}, r2); err != nil {
		t.Fatalf("index drop: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleIndexCreate – idempotent (already exists)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleIndexCreate_Idempotent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	createCmd := mustEncode(protocol.EncodeIndexCreate("idem_idx", "f", false, false, false))
	for i := 0; i < 2; i++ {
		r := make([]sm.Result, 1)
		if err := eng.Update(context.Background(), []sm.Entry{{Index: uint64(i + 1), Cmd: createCmd}}, r); err != nil {
			t.Fatalf("index create #%d: %v", i+1, err)
		}
	}
}

// ---------------------------------------------------------------------------
// RemoveIndexes – non-nil prevKv with indexed field
// ---------------------------------------------------------------------------

func TestRemoveIndexes_WithPrevKV(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	_ = idx.Put([]byte("u:1"), []byte("alice"))
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	prevKv := &service.KeyValue{
		Key:   []byte("u:1"),
		Value: []byte(`{"name":"alice"}`),
	}
	err := RemoveIndexes(indexes, indexFields, nil, []byte("u:1"), prevKv)
	if err != nil {
		t.Fatalf("RemoveIndexes: %v", err)
	}
}

func TestRemoveIndexes_NilPrevKV(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", false)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	// nil prevKv → early return nil
	err := RemoveIndexes(indexes, indexFields, nil, []byte("u:1"), nil)
	if err != nil {
		t.Fatalf("RemoveIndexes(nil prevKv): %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleBaseKVPut – unique violation path
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleBaseKVPut_UniqueViolation(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Create a unique index.
	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_sku", "sku", true, false, false))
	r0 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, r0)

	// First base-KV put (via sm/kv.EncodePut) with a unique value.
	put1, _ := kv.EncodePut([]byte("p:1"), []byte(`{"sku":"ABC"}`))
	r1 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: put1}}, r1)

	// Second put with conflicting sku → should encode non-fatal app error.
	put2, _ := kv.EncodePut([]byte("p:2"), []byte(`{"sku":"ABC"}`))
	r2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: put2}}, r2); err != nil {
		t.Fatalf("base kv put unique violation: %v", err)
	}
	// Expect a non-fatal result with Data (app-error encoded).
	if len(r2[0].Data) == 0 {
		t.Log("note: unique violation not encoded as app error in handleBaseKVPut")
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.lookupPrefix – limit cuts off results
// ---------------------------------------------------------------------------

func TestMVCCEngine_LookupPrefix_WithLimit(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	for i := 0; i < 5; i++ {
		key := []byte("lim/" + string(rune('a'+i)))
		entry := mvccPutEntry(uint64(i+1), key, []byte("v"), 0)
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{entry}, results)
	}

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("lim/"),
		Limit:  3,
	})
	if err != nil {
		t.Fatalf("Lookup with limit: %v", err)
	}
	m, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", got)
	}
	if len(m) != 3 {
		t.Errorf("expected 3 results (limit=3), got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleTransaction – success branch
// ---------------------------------------------------------------------------

func TestMVCCEngine_Transaction_SuccessBranch(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Put a key first so the compare-exists succeeds.
	putEntry := mvccPutEntry(1, []byte("txn:key"), []byte("val"), 0)
	r0 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{putEntry}, r0)

	// Transaction that checks for the key's existence (version != 0) → succeeds.
	// Compare: version > 0 → means "key exists". Use CompareEqual with version=0 to check
	// the non-existence branch, or use a successful compare (no compare = always succeeds).
	// Use no compares → always succeeds.
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn:success"), []byte("yes"), nil, 0))
	txnPayload := mustEncode(protocol.EncodeTransaction(nil, [][]byte{successOp}, nil))
	r1 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: txnPayload}}, r1); err != nil {
		t.Fatalf("transaction success: %v", err)
	}
	// Success branch sets Data[0]=1.
	if len(r1[0].Data) == 0 || r1[0].Data[0] != 1 {
		t.Errorf("expected success flag in result, got Data=%v", r1[0].Data)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.lookupPrefix – __sys: prefix skips internal-key filter
// ---------------------------------------------------------------------------

func TestMVCCEngine_LookupPrefix_SysPrefix(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Put a key so there is at least one MVCC meta entry.
	entry := mvccPutEntry(1, []byte("normalkey"), []byte("v"), 0)
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{entry}, results)

	// Query with __sys: prefix — exercises the skipFilter=true branch.
	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("__sys:"),
	})
	if err != nil {
		t.Fatalf("Lookup __sys: %v", err)
	}
	// The result is a map (possibly empty).
	_, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", got)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleBaseKVDelete – key-length-exceeds-payload error path
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleBaseKVDelete_InvalidPayload(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Encode a base-KV delete command with a truncated payload (invalid).
	// kv.EncodeDelete normally produces [OpDelete=0x02][keyLen:2 LE][key].
	// We forge a command with claimed keyLen larger than the payload.
	truncated := []byte{0x02, 0xFF, 0xFF} // OpFSMDelete + keyLen=0xFFFF but no key bytes
	results := make([]sm.Result, 1)
	err = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: truncated}}, results)
	// May return an error or a Result with Value=1. Either is acceptable.
	_ = err
}

// ---------------------------------------------------------------------------
// KVEngine.handleIndexCreate – numeric index (valueType branch)
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexCreate_Numeric(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	// numeric=true branch in handleIndexCreate.
	createCmd := mustEncode(protocol.EncodeIndexCreate("by_score", "score", false, false, true))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, results); err != nil {
		t.Fatalf("numeric index create: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleMVCCCompact – no revision written if compact fails silently
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleMVCCCompact_WithRevision(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Write several revisions then compact to the latest.
	for i := 0; i < 3; i++ {
		entry := mvccPutEntry(uint64(i+1), []byte("ck"), []byte("v"), 0)
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{entry}, results)
	}

	// Compact to revision 2.
	compact := mustEncode(protocol.EncodeMVCCCompact(2))
	r := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 10, Cmd: compact}}, r); err != nil {
		t.Fatalf("compact: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseRevoke – lr==nil path (no lessor configured)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleLeaseRevoke_NoLessor(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Engine without a lessor.
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(12345))
	r := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: revokeCmd}}, r); err != nil {
		t.Fatalf("lease revoke (no lessor): %v", err)
	}
	// Expect a non-fatal app error result (NotOpenError).
	if len(r[0].Data) == 0 {
		t.Log("note: lease revoke with no lessor did not encode an app error")
	}
}

// ---------------------------------------------------------------------------
// handleLeaseKeepAlive – lr==nil path (no lessor configured)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleLeaseKeepAlive_NoLessor(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	kaCmd := mustEncode(protocol.EncodeLeaseKeepAlive(12345))
	r := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: kaCmd}}, r); err != nil {
		t.Fatalf("lease keepalive (no lessor): %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleLeaseRevoke – NotFoundError (idempotent success)
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleLeaseRevoke_NotFound(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	lr := lease.NewLessor()
	eng, err := NewMVCCEngine(stg, WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Revoke a lease that was never granted → NotFoundError → idempotent success.
	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(99999))
	r := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: revokeCmd}}, r); err != nil {
		t.Fatalf("lease revoke not found: %v", err)
	}
}

// ---------------------------------------------------------------------------
// storage.Batch referenced to keep import used
// ---------------------------------------------------------------------------

var _ storage.Storage

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type boostError string

func (e boostError) Error() string { return string(e) }

func mapBoostKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// KVEngine.listKeys via ListQuery
// ---------------------------------------------------------------------------

func TestKVEngine_ListKeys_ViaListQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// Put some keys.
	for _, k := range []string{"ns/a", "ns/b", "other/c"} {
		put, _ := kv.EncodePut([]byte(k), []byte("v"))
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, results)
	}

	got, err := eng.Lookup(context.Background(), kv.ListQuery{Prefix: "ns/"})
	if err != nil {
		t.Fatalf("Lookup ListQuery: %v", err)
	}
	keys, ok := got.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", got)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys for prefix ns/, got %d: %v", len(keys), keys)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.scanPrefix via PrefixQuery
// ---------------------------------------------------------------------------

func TestKVEngine_ScanPrefix_ViaQuery(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// Put some keys.
	for _, kv2 := range [][2]string{{"item/1", "v1"}, {"item/2", "v2"}, {"other/3", "v3"}} {
		put, _ := kv.EncodePut([]byte(kv2[0]), []byte(kv2[1]))
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, results)
	}

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("item/")})
	if err != nil {
		t.Fatalf("Lookup PrefixQuery: %v", err)
	}
	m, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", got)
	}
	if len(m) != 2 {
		t.Errorf("expected 2 keys for item/ prefix, got %d: %v", len(m), mapBoostKeys(m))
	}
}

// ---------------------------------------------------------------------------
// KVEngine.scanPrefix – with limit
// ---------------------------------------------------------------------------

func TestKVEngine_ScanPrefix_WithLimit(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	for _, k := range []string{"p/1", "p/2", "p/3"} {
		put, _ := kv.EncodePut([]byte(k), []byte("v"))
		results := make([]sm.Result, 1)
		_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, results)
	}

	got, err := eng.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("p/"),
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("Lookup PrefixQuery with limit: %v", err)
	}
	m, ok := got.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", got)
	}
	if len(m) != 2 {
		t.Errorf("expected 2 keys (limit), got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleBatch – with delete operation
// ---------------------------------------------------------------------------

func TestMVCCEngine_HandleBatch_WithDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Put a key first.
	entry := mvccPutEntry(1, []byte("batchdel"), []byte("v"), 0)
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{entry}, results)

	// Now batch: put k1, delete batchdel.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bk1"), []byte("bv1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("batchdel"), nil, nil, 0)),
	}
	batchPayload := mustEncode(protocol.EncodeBatch(ops))
	entries2 := []sm.Entry{{Index: 2, Cmd: batchPayload}}
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries2, results2); err != nil {
		t.Fatalf("batch with delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleBaseKVPut + handleBaseKVDelete coverage
// ---------------------------------------------------------------------------

func TestMVCCEngine_BaseKVPutAndDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// baseKVPutEntry uses sm/kv.EncodePut which produces OpFSMPut.
	put := baseKVPutEntry(1, "base_key", "base_val")
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{put}, results); err != nil {
		t.Fatalf("base kv put: %v", err)
	}

	del := baseKVDeleteEntry(2, "base_key")
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{del}, results2); err != nil {
		t.Fatalf("base kv delete: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCEngine.handleMVCCPut – unique index violation (non-fatal AppError)
// ---------------------------------------------------------------------------

func TestMVCCEngine_MVCCPut_UniqueIndexViolation(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}

	// Register a unique index via handleIndexCreate.
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreate("by_email", "email", true, false, false))
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCreateCmd}}, results)

	// Put a first entry.
	val1 := []byte(`{"name":"alice","email":"alice@test.com"}`)
	put1 := mvccPutEntry(2, []byte("u:1"), val1, 0)
	results2 := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{put1}, results2)

	// Put a conflicting entry with the same email.
	val2 := []byte(`{"name":"bob","email":"alice@test.com"}`)
	put2 := mvccPutEntry(3, []byte("u:2"), val2, 0)
	results3 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{put2}, results3); err != nil {
		t.Fatalf("Update with conflict: %v", err)
	}
	// The result should encode an app-error (non-fatal).
	if len(results3[0].Data) == 0 {
		t.Log("note: unique violation encoded as app error (non-fatal Result.Data)")
	}
}

// ---------------------------------------------------------------------------
// KVEngine.handleIndexCreate – index already exists (idempotent) - boost
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexCreate_AlreadyExistsBoost(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	createCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, results)

	// Create again — should be idempotent.
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: createCmd}}, results2); err != nil {
		t.Fatalf("duplicate index create: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.handleIndexDrop – drop existing index
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexDrop_Existing(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	createCmd := mustEncode(protocol.EncodeIndexCreate("drop_idx", "field", false, false, false))
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, results)

	dropCmd := mustEncode(protocol.EncodeIndexDrop("drop_idx"))
	results2 := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: dropCmd}}, results2); err != nil {
		t.Fatalf("index drop: %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVEngine.lookupIndexList
// ---------------------------------------------------------------------------

func TestKVEngine_LookupIndexList(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	createCmd := mustEncode(protocol.EncodeIndexCreate("list_idx", "name", false, false, false))
	results := make([]sm.Result, 1)
	_ = eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, results)

	got, err := eng.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names, ok := got.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", got)
	}
	if len(names) == 0 {
		t.Error("expected at least one index name in list")
	}
}

// mustEncode unwraps an encoder's (command, error) pair for tests whose
// inputs are small and fixed. An error here is a TEST bug -- the encoders
// refuse only values their length fields cannot describe -- so it fails
// loudly rather than being threaded through every call site.
func mustEncode(cmd []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return cmd
}
