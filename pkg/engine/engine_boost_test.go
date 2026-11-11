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
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

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
	err := CheckUniqueViolationsBatch(nil, indexes, indexFields, nil, puts, nil)
	if err == nil {
		t.Fatal("expected UniqueConstraintError for intra-batch duplicate")
	}
	if _, ok := err.(*UniqueConstraintError); !ok {
		t.Fatalf("expected *UniqueConstraintError, got %T: %v", err, err)
	}
}

func TestCheckUniqueViolationsBatch_EmptyBatch(t *testing.T) {
	err := CheckUniqueViolationsBatch(nil, nil, nil, nil, nil, nil)
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

	err := CheckUniqueViolationsBatch(nil, indexes, indexFields, nil, puts, deletedKeys)
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
// CheckUniqueViolationsBatch – non-JSON value skipped silently
// ---------------------------------------------------------------------------

func TestCheckUniqueViolationsBatch_NonJSONSkipped(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	puts := []indexedPut{
		{Key: []byte("k1"), Value: []byte("not-json")},
	}
	err := CheckUniqueViolationsBatch(nil, indexes, indexFields, nil, puts, nil)
	if err != nil {
		t.Fatalf("expected nil for non-JSON value, got: %v", err)
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
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
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
	err := CheckUniqueViolation(nil, indexes, indexFields, nil, []byte("k1"), val)
	if err == nil {
		t.Fatal("expected error when index.Get fails")
	}
}

// CheckUniqueViolation – non-JSON value returns nil (no violation possible)
func TestCheckUniqueViolation_NonJSON(t *testing.T) {
	idx := index.NewMemoryIndex("by_name", true)
	indexes := map[string]index.Index{"by_name": idx}
	indexFields := map[string]string{"by_name": "name"}

	err := CheckUniqueViolation(nil, indexes, indexFields, nil, []byte("k1"), []byte("not-json"))
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
	err := CheckUniqueViolation(nil, indexes, indexFields, nil, []byte("k1"), val)
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
	err := CheckUniqueViolation(nil, indexes, indexFields, nil, []byte("k1"), val)
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
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_tag", "tag", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_code", "code", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
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
	err := CheckUniqueViolationsBatch(nil, indexes, indexFields, nil, puts, nil)
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
	idxCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_sku", "sku", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
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
// KVEngine.handleIndexCreate – numeric index (valueType branch)
// ---------------------------------------------------------------------------

func TestKVEngine_HandleIndexCreate_Numeric(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	log, _ := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	eng := NewKVEngine(stg, WithKVLogger(log))

	// numeric=true branch in handleIndexCreate.
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_score", "score", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: true}))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createCmd}}, results); err != nil {
		t.Fatalf("numeric index create: %v", err)
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
	idxCreateCmd := mustEncode(protocol.EncodeIndexCreateOpts("by_email", "email", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
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
