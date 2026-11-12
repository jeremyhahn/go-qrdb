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
// storage.Batch referenced to keep import used
// ---------------------------------------------------------------------------

var _ storage.Storage

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type boostError string

func (e boostError) Error() string { return string(e) }

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
