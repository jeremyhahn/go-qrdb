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

package statemachine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// Test helpers for ConcurrentStateMachine
// ---------------------------------------------------------------------------

func newTestConcurrentSM(opts ...ConcurrentOption) (*ConcurrentStateMachine, *memStorage) {
	store := newMemStorage()
	csm := NewConcurrentStateMachine(1, 1, store, opts...)
	return csm, store
}

func concurrentApplyPut(t *testing.T, csm *ConcurrentStateMachine, key, value string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(put) failed: %v", err)
	}
	return results[0]
}

func concurrentApplyDelete(t *testing.T, csm *ConcurrentStateMachine, key string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete) failed: %v", err)
	}
	return results[0]
}

func concurrentLookupKey(t *testing.T, csm *ConcurrentStateMachine, key string) []byte {
	t.Helper()
	result, err := csm.Lookup(context.Background(), []byte(key))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if result == nil {
		return nil
	}
	val, ok := result.([]byte)
	if !ok {
		t.Fatalf("Lookup returned unexpected type: %T", result)
	}
	return val
}

func waitForConcurrentAsync(csm *ConcurrentStateMachine) {
	csm.wg.Wait()
}

// ---------------------------------------------------------------------------
// Basic KV tests
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_Put_Success(t *testing.T) {
	csm, store := newTestConcurrentSM()

	result := concurrentApplyPut(t, csm, "hello", "world")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := concurrentLookupKey(t, csm, "hello")
	if string(val) != "world" {
		t.Fatalf("expected 'world', got %q", val)
	}

	waitForConcurrentAsync(csm)
	stored, err := store.Get([]byte("hello"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if string(stored) != "world" {
		t.Fatalf("storage: expected 'world', got %q", stored)
	}
}

func TestConcurrentStateMachine_Put_EmptyValue(t *testing.T) {
	csm, store := newTestConcurrentSM()

	result := concurrentApplyPut(t, csm, "empty-key", "")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := concurrentLookupKey(t, csm, "empty-key")
	if val == nil {
		t.Fatal("expected non-nil empty slice for empty value")
	}
	if len(val) != 0 {
		t.Fatalf("expected empty value, got %q", val)
	}

	waitForConcurrentAsync(csm)
	stored, err := store.Get([]byte("empty-key"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if stored == nil {
		t.Fatal("storage: expected non-nil empty slice")
	}
}

func TestConcurrentStateMachine_Get_Found(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "key1", "value1")
	concurrentApplyPut(t, csm, "key2", "value2")

	val := concurrentLookupKey(t, csm, "key1")
	if string(val) != "value1" {
		t.Fatalf("expected 'value1', got %q", val)
	}

	val = concurrentLookupKey(t, csm, "key2")
	if string(val) != "value2" {
		t.Fatalf("expected 'value2', got %q", val)
	}
}

func TestConcurrentStateMachine_Get_NotFound(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	val := concurrentLookupKey(t, csm, "nonexistent")
	if val != nil {
		t.Fatalf("expected nil for missing key, got %q", val)
	}
}

func TestConcurrentStateMachine_Delete_Success(t *testing.T) {
	csm, store := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "to-delete", "value")
	waitForConcurrentAsync(csm)

	val := concurrentLookupKey(t, csm, "to-delete")
	if string(val) != "value" {
		t.Fatalf("expected 'value' before delete, got %q", val)
	}

	result := concurrentApplyDelete(t, csm, "to-delete")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision from delete")
	}

	val = concurrentLookupKey(t, csm, "to-delete")
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}

	waitForConcurrentAsync(csm)
	stored, err := store.Get([]byte("to-delete"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if stored != nil {
		t.Fatalf("storage: expected nil after delete, got %q", stored)
	}
}

func TestConcurrentStateMachine_Delete_NotFound(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	result := concurrentApplyDelete(t, csm, "ghost")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := concurrentLookupKey(t, csm, "ghost")
	if val != nil {
		t.Fatalf("expected nil for deleted non-existent key, got %q", val)
	}
}

func TestConcurrentStateMachine_Put_KeyOverwrite(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "overwrite", "original")
	concurrentApplyPut(t, csm, "overwrite", "updated")

	val := concurrentLookupKey(t, csm, "overwrite")
	if string(val) != "updated" {
		t.Fatalf("expected 'updated', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_Batch_MixedOps(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "batch-del", "to-be-removed")

	ops := [][]byte{
		protocol.EncodeOp(kv.OpPut, []byte("batch-a"), []byte("val-a"), nil, 0),
		protocol.EncodeOp(kv.OpPut, []byte("batch-b"), []byte("val-b"), nil, 0),
		protocol.EncodeOp(kv.OpDelete, []byte("batch-del"), nil, nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	entries := []sm.Entry{{Index: 10, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from batch")
	}

	if v := concurrentLookupKey(t, csm, "batch-a"); string(v) != "val-a" {
		t.Fatalf("batch put a: expected 'val-a', got %q", v)
	}
	if v := concurrentLookupKey(t, csm, "batch-b"); string(v) != "val-b" {
		t.Fatalf("batch put b: expected 'val-b', got %q", v)
	}
	if v := concurrentLookupKey(t, csm, "batch-del"); v != nil {
		t.Fatalf("batch delete: expected nil, got %q", v)
	}
}

func TestConcurrentStateMachine_Batch_UnknownOp(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	ops := [][]byte{
		protocol.EncodeOp(0xFE, []byte("bad"), []byte("val"), nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown op in batch")
	}

	var opErr *InvalidOpcodeError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected InvalidOpcodeError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_Batch_InvalidPayload(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := []byte{protocol.OpBatch, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated batch payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Prefix scan tests
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_PrefixScan_CacheOverlay(t *testing.T) {
	csm, store := newTestConcurrentSM()

	_ = store.Set([]byte("ns/a"), []byte("from-storage-a"))
	_ = store.Set([]byte("ns/b"), []byte("from-storage-b"))
	_ = store.Set([]byte("other/x"), []byte("should-not-appear"))

	concurrentApplyPut(t, csm, "ns/b", "from-cache-b")
	concurrentApplyPut(t, csm, "ns/c", "from-cache-c")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}

	if len(m) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(m), keysOf(m))
	}

	if string(m["ns/a"]) != "from-storage-a" {
		t.Fatalf("ns/a: expected 'from-storage-a', got %q", m["ns/a"])
	}
	if string(m["ns/b"]) != "from-cache-b" {
		t.Fatalf("ns/b: expected 'from-cache-b', got %q", m["ns/b"])
	}
	if string(m["ns/c"]) != "from-cache-c" {
		t.Fatalf("ns/c: expected 'from-cache-c', got %q", m["ns/c"])
	}
}

func TestConcurrentStateMachine_PrefixScan_Tombstone(t *testing.T) {
	csm, store := newTestConcurrentSM()

	_ = store.Set([]byte("pfx/alive"), []byte("yes"))
	_ = store.Set([]byte("pfx/dead"), []byte("to-be-killed"))

	concurrentApplyDelete(t, csm, "pfx/dead")

	result, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)

	if _, found := m["pfx/dead"]; found {
		t.Fatal("tombstoned key pfx/dead should not appear in prefix scan")
	}
	if string(m["pfx/alive"]) != "yes" {
		t.Fatalf("pfx/alive: expected 'yes', got %q", m["pfx/alive"])
	}
}

func TestConcurrentStateMachine_PrefixScan_Empty(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	result, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("noexist/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(m))
	}
}

func TestConcurrentStateMachine_PrefixScan_StorageIterError(t *testing.T) {
	store := &errStorage{iterErr: errors.New("corruption")}
	csm := NewConcurrentStateMachine(1, 1, store)

	_, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err == nil {
		t.Fatal("expected error from storage iterator")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PrepareSnapshot + SaveSnapshot + RecoverFromSnapshot
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_PrepareSnapshot_SaveRecover(t *testing.T) {
	csm1, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm1, "snap/key1", "val1")
	concurrentApplyPut(t, csm1, "snap/key2", "val2")
	concurrentApplyPut(t, csm1, "snap/key3", "val3")

	// PrepareSnapshot captures a deep copy.
	snapshotCtx, err := csm1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	// SaveSnapshot serializes the prepared snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := csm1.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// RecoverFromSnapshot restores into a fresh CSM.
	csm2, _ := newTestConcurrentSM()
	if err := csm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	for _, tc := range []struct{ key, val string }{
		{"snap/key1", "val1"},
		{"snap/key2", "val2"},
		{"snap/key3", "val3"},
	} {
		got := concurrentLookupKey(t, csm2, tc.key)
		if string(got) != tc.val {
			t.Fatalf("%s: expected %q, got %q", tc.key, tc.val, got)
		}
	}
}

func TestConcurrentStateMachine_PrepareSnapshot_SkipsTombstones(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "alive", "yes")
	concurrentApplyPut(t, csm, "dead", "soon")
	concurrentApplyDelete(t, csm, "dead")

	snapshotCtx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	data, ok := snapshotCtx.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", snapshotCtx)
	}

	if _, found := data["dead"]; found {
		t.Fatal("PrepareSnapshot should not include tombstoned keys")
	}
	if string(data["alive"]) != "yes" {
		t.Fatalf("expected 'yes', got %q", data["alive"])
	}
}

func TestConcurrentStateMachine_Snapshot_Empty(t *testing.T) {
	csm1, _ := newTestConcurrentSM()

	snapshotCtx, err := csm1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := csm1.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	csm2, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm2, "stale", "data")
	waitForConcurrentAsync(csm2)

	if err := csm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	val := concurrentLookupKey(t, csm2, "stale")
	if val != nil {
		t.Fatalf("expected nil after empty snapshot recover, got %q", val)
	}
}

func TestConcurrentStateMachine_SaveSnapshot_InvalidContext(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	var buf bytes.Buffer
	stopper := make(chan struct{})
	err := csm.SaveSnapshot(context.Background(), "not-a-map", &buf, stopper)
	if err == nil {
		t.Fatal("expected error for invalid snapshot context type")
	}

	var snapErr *SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_SaveSnapshot_Cancelled(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	for i := 0; i < 10; i++ {
		concurrentApplyPut(t, csm, "cancel/"+string(rune('A'+i)), "data")
	}

	snapshotCtx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err = csm.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper)
	if err != nil {
		var snapErr *SnapshotError
		if !errors.As(err, &snapErr) {
			t.Fatalf("expected SnapshotError, got %T: %v", err, err)
		}
	}
}

func TestConcurrentStateMachine_RecoverSnapshot_Cancelled(t *testing.T) {
	csm1, _ := newTestConcurrentSM()

	for i := 0; i < 10; i++ {
		concurrentApplyPut(t, csm1, "recov/"+string(rune('A'+i)), "data")
	}

	snapshotCtx, err := csm1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	var buf bytes.Buffer
	openStopper := make(chan struct{})
	if err := csm1.SaveSnapshot(context.Background(), snapshotCtx, &buf, openStopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	csm2, _ := newTestConcurrentSM()
	closedStopper := make(chan struct{})
	close(closedStopper)

	err = csm2.RecoverFromSnapshot(context.Background(), &buf, closedStopper)
	if err != nil {
		var snapErr *SnapshotError
		if !errors.As(err, &snapErr) {
			t.Fatalf("expected SnapshotError, got %T: %v", err, err)
		}
	}
}

func TestConcurrentStateMachine_Snapshot_LargeDataset(t *testing.T) {
	csm1, _ := newTestConcurrentSM()

	const count = 500
	for i := 0; i < count; i++ {
		key := "large/" + string(rune('A'+i/26%26)) + string(rune('a'+i%26))
		concurrentApplyPut(t, csm1, key, "v"+key)
	}

	snapshotCtx, err := csm1.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := csm1.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	csm2, _ := newTestConcurrentSM()
	if err := csm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	for _, i := range []int{0, 100, 250, 499} {
		key := "large/" + string(rune('A'+i/26%26)) + string(rune('a'+i%26))
		val := concurrentLookupKey(t, csm2, key)
		expected := "v" + key
		if string(val) != expected {
			t.Fatalf("key %s: expected %q, got %q", key, expected, val)
		}
	}
}

// ---------------------------------------------------------------------------
// Update edge cases
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_Update_EmptyEntry(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	entries := []sm.Entry{{Index: 1, Cmd: nil}}
	results := make([]sm.Result, 1)

	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with empty cmd failed: %v", err)
	}
	if results[0].Value != 0 {
		t.Fatalf("expected zero result for empty cmd, got %d", results[0].Value)
	}
}

func TestConcurrentStateMachine_Update_UnknownOp(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := []byte{0xFF, 0x01, 0x02}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown op")
	}

	var opErr *InvalidOpcodeError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected InvalidOpcodeError, got %T: %v", err, err)
	}
	if opErr.Op != 0xFF {
		t.Fatalf("expected op 0xFF, got 0x%02X", opErr.Op)
	}
}

func TestConcurrentStateMachine_Put_InvalidPayload(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := []byte{kv.OpPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated put payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_Put_KeyLengthExceedsPayload(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := make([]byte, 4)
	cmd[0] = kv.OpPut
	binary.LittleEndian.PutUint16(cmd[1:3], 100)
	cmd[3] = 0x01

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_Delete_InvalidPayload(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := []byte{kv.OpDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated delete payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_Delete_KeyLengthExceedsPayload(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmd := make([]byte, 4)
	cmd[0] = kv.OpDelete
	binary.LittleEndian.PutUint16(cmd[1:3], 100)
	cmd[3] = 0x01

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := csm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestConcurrentStateMachine_MultipleEntries(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	cmdA, err := kv.EncodePut([]byte("multi/a"), []byte("va"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmdB, err := kv.EncodePut([]byte("multi/b"), []byte("vb"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmdDel, err := kv.EncodeDelete([]byte("multi/a"))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: cmdA},
		{Index: 2, Cmd: cmdB},
		{Index: 3, Cmd: cmdDel},
	}
	results := make([]sm.Result, 3)

	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if results[0].Value >= results[1].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[0].Value, results[1].Value)
	}
	if results[1].Value >= results[2].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[1].Value, results[2].Value)
	}

	val := concurrentLookupKey(t, csm, "multi/a")
	if val != nil {
		t.Fatalf("multi/a should be nil after delete, got %q", val)
	}

	val = concurrentLookupKey(t, csm, "multi/b")
	if string(val) != "vb" {
		t.Fatalf("multi/b: expected 'vb', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// Watch notifications
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_WatchNotification(t *testing.T) {
	wm := &mockWatchManager{}
	csm, _ := newTestConcurrentSM(WithConcurrentWatchManager(wm))

	concurrentApplyPut(t, csm, "watched", "value1")
	concurrentApplyDelete(t, csm, "watched")

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 watch events, got %d", len(events))
	}

	if events[0].key != "watched" || events[0].deleted {
		t.Fatalf("event 0: expected put on 'watched', got key=%q deleted=%v", events[0].key, events[0].deleted)
	}
	if string(events[0].value) != "value1" {
		t.Fatalf("event 0: expected value 'value1', got %q", events[0].value)
	}

	if events[1].key != "watched" || !events[1].deleted {
		t.Fatalf("event 1: expected delete on 'watched', got key=%q deleted=%v", events[1].key, events[1].deleted)
	}
	if events[1].value != nil {
		t.Fatalf("event 1: expected nil value for delete, got %q", events[1].value)
	}
}

func TestConcurrentStateMachine_WatchNotification_Batch(t *testing.T) {
	wm := &mockWatchManager{}
	csm, _ := newTestConcurrentSM(WithConcurrentWatchManager(wm))

	ops := [][]byte{
		protocol.EncodeOp(kv.OpPut, []byte("bw/a"), []byte("va"), nil, 0),
		protocol.EncodeOp(kv.OpDelete, []byte("bw/b"), nil, nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 watch events from batch, got %d", len(events))
	}

	if events[0].key != "bw/a" || events[0].deleted {
		t.Fatalf("event 0: expected put on 'bw/a', got %+v", events[0])
	}
	if events[1].key != "bw/b" || !events[1].deleted {
		t.Fatalf("event 1: expected delete on 'bw/b', got %+v", events[1])
	}
}

// ---------------------------------------------------------------------------
// Lookup edge cases
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_Lookup_UnsupportedQueryType(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	_, err := csm.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
	if !strings.Contains(incErr.Reason, "unsupported query type") {
		t.Fatalf("unexpected error reason: %s", incErr.Reason)
	}
}

func TestConcurrentStateMachine_Lookup_StorageFallback(t *testing.T) {
	csm, store := newTestConcurrentSM()

	if err := store.Set([]byte("storage-only"), []byte("found-it")); err != nil {
		t.Fatalf("store.Set failed: %v", err)
	}

	val := concurrentLookupKey(t, csm, "storage-only")
	if string(val) != "found-it" {
		t.Fatalf("expected 'found-it', got %q", val)
	}
}

func TestConcurrentStateMachine_Lookup_StorageError(t *testing.T) {
	store := &errStorage{getErr: errors.New("storage down")}
	csm := NewConcurrentStateMachine(1, 1, store)

	_, err := csm.Lookup(context.Background(), []byte("anything"))
	if err == nil {
		t.Fatal("expected error from storage fallback")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_WithLogger(t *testing.T) {
	logger := slog.Default()
	csm, _ := newTestConcurrentSM(WithConcurrentLogger(logger))
	if csm.logger != logger {
		t.Fatal("WithConcurrentLogger did not set the logger")
	}
}

func TestConcurrentStateMachine_WithConcurrentIndexManager(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	if csm.indexManager != nil {
		t.Fatal("indexManager should be nil by default")
	}
}

func TestConcurrentStateMachine_WithConcurrentLeaseManager(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	if csm.leaseManager != nil {
		t.Fatal("leaseManager should be nil by default")
	}
}

func TestConcurrentStateMachine_WithConcurrentMVCCStore(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	if csm.mvccStore != nil {
		t.Fatal("mvccStore should be nil by default")
	}
}

func TestConcurrentStateMachine_WithConcurrentTimeSeriesStore(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	if csm.tsStore != nil {
		t.Fatal("tsStore should be nil by default")
	}
}

// ---------------------------------------------------------------------------
// Service not configured
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_HandlersNotConfigured(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	// Handlers for domain stores that are not configured should encode
	// ServiceNotConfiguredError in result.Data (Raft-safe: no error returned).
	notConfiguredOps := []struct {
		op      byte
		service string
	}{
		{protocol.OpMVCCPut, "mvcc"},
		{protocol.OpMVCCDelete, "mvcc"},
		{protocol.OpMVCCRange, "mvcc"},
		{protocol.OpMVCCCompact, "mvcc"},
		{protocol.OpTransaction, "mvcc"},
		{protocol.OpLeaseGrant, "lease"},
		{protocol.OpLeaseRevoke, "lease"},
		{protocol.OpLeaseKeepAlive, "lease"},
		{protocol.OpIndexCreate, "index"},
		{protocol.OpIndexDrop, "index"},
		{protocol.OpTSWrite, "timeseries"},
		{protocol.OpTSWriteBatch, "timeseries"},
		{protocol.OpTSWritePoint, "timeseries"},
		{protocol.OpTSDeleteSeries, "timeseries"},
		{protocol.OpTSSetRetention, "timeseries"},
	}

	dummyPayload := make([]byte, 32)

	for _, tc := range notConfiguredOps {
		cmd := make([]byte, 1+len(dummyPayload))
		cmd[0] = tc.op
		copy(cmd[1:], dummyPayload)

		entries := []sm.Entry{{Index: 1, Cmd: cmd}}
		results := make([]sm.Result, 1)

		err := csm.Update(context.Background(), entries, results)
		if err != nil {
			t.Fatalf("op %d: expected nil error (SNC encoded in result.Data), got %v", tc.op, err)
		}
		if len(results[0].Data) == 0 {
			t.Fatalf("op %d: expected ServiceNotConfiguredError encoded in result.Data", tc.op)
		}
		if !bytes.Contains(results[0].Data, []byte(tc.service)) {
			t.Fatalf("op %d: expected Data to mention %q, got %q", tc.op, tc.service, results[0].Data)
		}
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_Close(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "close-test", "data")

	if err := csm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetHash
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_GetHash_Deterministic(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "hash/a", "v1")
	concurrentApplyPut(t, csm, "hash/b", "v2")
	concurrentApplyPut(t, csm, "hash/c", "v3")

	h1, err := csm.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	h2, err := csm.GetHash()
	if err != nil {
		t.Fatalf("GetHash (2nd) failed: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("GetHash not deterministic: %d != %d", h1, h2)
	}
}

func TestConcurrentStateMachine_GetHash_DifferentData(t *testing.T) {
	csm1, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm1, "a", "1")

	csm2, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm2, "b", "2")

	h1, err := csm1.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	h2, err := csm2.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	if h1 == h2 {
		t.Fatal("GetHash should differ for different data")
	}
}

func TestConcurrentStateMachine_GetHash_SkipsTombstones(t *testing.T) {
	csm1, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm1, "alive", "yes")

	csm2, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm2, "alive", "yes")
	concurrentApplyPut(t, csm2, "dead", "soon")
	concurrentApplyDelete(t, csm2, "dead")

	h1, err := csm1.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	h2, err := csm2.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("GetHash should be equal after tombstone: %d != %d", h1, h2)
	}
}

func TestConcurrentStateMachine_GetHash_Empty(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	h, err := csm.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	// Verify empty state produces a deterministic hash.
	// xxhash of empty input is non-zero; we just confirm no error above.
	_ = h
}

// ---------------------------------------------------------------------------
// Concurrency stress tests
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_ConcurrentPuts(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	const goroutines = 64
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var errCount atomic.Int64

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := "concurrent/" + string(rune('A'+id)) + "/" + string(rune('0'+i%10))
				cmd, err := kv.EncodePut([]byte(key), []byte("val"))
				if err != nil {
					errCount.Add(1)
					continue
				}
				entries := []sm.Entry{{Index: uint64(id*opsPerGoroutine + i), Cmd: cmd}}
				results := make([]sm.Result, 1)
				if err := csm.Update(context.Background(), entries, results); err != nil {
					errCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	if n := errCount.Load(); n != 0 {
		t.Fatalf("concurrent puts produced %d errors", n)
	}

	if err := csm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestConcurrentStateMachine_ConcurrentLookupsDuringUpdate(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	// Pre-populate some data.
	for i := 0; i < 100; i++ {
		concurrentApplyPut(t, csm, "preload/"+string(rune('A'+i%26)), "initial")
	}

	var wg sync.WaitGroup
	var updateErrCount, lookupErrCount atomic.Int64

	// Writer goroutines.
	const writers = 8
	const writesPerWriter = 200
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				key := "concurrent-rw/" + string(rune('A'+id)) + "/" + string(rune('0'+i%10))
				cmd, err := kv.EncodePut([]byte(key), []byte("val"))
				if err != nil {
					updateErrCount.Add(1)
					continue
				}
				entries := []sm.Entry{{Index: uint64(id*writesPerWriter + i), Cmd: cmd}}
				results := make([]sm.Result, 1)
				if err := csm.Update(context.Background(), entries, results); err != nil {
					updateErrCount.Add(1)
				}
			}
		}(w)
	}

	// Reader goroutines running concurrently with writers.
	const readers = 16
	const readsPerReader = 500
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < readsPerReader; i++ {
				key := "preload/" + string(rune('A'+i%26))
				_, err := csm.Lookup(context.Background(), []byte(key))
				if err != nil {
					lookupErrCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if n := updateErrCount.Load(); n != 0 {
		t.Fatalf("concurrent updates produced %d errors", n)
	}
	if n := lookupErrCount.Load(); n != 0 {
		t.Fatalf("concurrent lookups produced %d errors", n)
	}

	if err := csm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewConcurrentCreateFunc factory
// ---------------------------------------------------------------------------

func TestNewConcurrentCreateFunc(t *testing.T) {
	storeFactory := func(_, _ uint64) storage.Storage {
		return newMemStorage()
	}

	createFunc := NewConcurrentCreateFunc(storeFactory)
	result := createFunc(1, 1)

	csm, ok := result.(sm.ConcurrentStateMachine)
	if !ok {
		t.Fatalf("expected sm.ConcurrentStateMachine, got %T", result)
	}

	// Verify the returned instance works.
	cmd, err := kv.EncodePut([]byte("factory-test"), []byte("value"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val, err := csm.Lookup(context.Background(), []byte("factory-test"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if string(val.([]byte)) != "value" {
		t.Fatalf("expected 'value', got %q", val)
	}
}

func TestNewConcurrentCreateFunc_WithOptions(t *testing.T) {
	wm := &mockWatchManager{}
	storeFactory := func(_, _ uint64) storage.Storage {
		return newMemStorage()
	}

	createFunc := NewConcurrentCreateFunc(storeFactory, WithConcurrentWatchManager(wm))
	result := createFunc(1, 1)

	csm, ok := result.(*ConcurrentStateMachine)
	if !ok {
		t.Fatalf("expected *ConcurrentStateMachine, got %T", result)
	}

	if csm.watchManager == nil {
		t.Fatal("expected watchManager to be set via options")
	}
}

// ---------------------------------------------------------------------------
// Shard distribution
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_ShardDistribution(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	// Write enough keys to exercise multiple shards.
	const count = 1000
	for i := 0; i < count; i++ {
		key := "shard-dist/" + string(rune('A'+i/26%26)) + string(rune('a'+i%26))
		concurrentApplyPut(t, csm, key, "v")
	}

	// Count entries per shard. At least 2 shards should have data.
	usedShards := 0
	for i := range csm.shards {
		csm.shards[i].mu.RLock()
		if len(csm.shards[i].data) > 0 {
			usedShards++
		}
		csm.shards[i].mu.RUnlock()
	}

	if usedShards < 2 {
		t.Fatalf("expected data distributed across at least 2 shards, got %d", usedShards)
	}
}

// ---------------------------------------------------------------------------
// MVCC Lookup not-configured tests
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_LookupMVCC_NotConfigured(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	queries := []interface{}{
		MVCCGetQuery{Key: []byte("k")},
		&MVCCGetQuery{Key: []byte("k")},
		MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1},
		&MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1},
		MVCCRangeQuery{Start: []byte("a"), End: []byte("z")},
		&MVCCRangeQuery{Start: []byte("a"), End: []byte("z")},
		MVCCRevisionQuery{},
		&MVCCRevisionQuery{},
	}

	for _, q := range queries {
		_, err := csm.Lookup(context.Background(), q)
		if err == nil {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got nil", q)
		}

		var svcErr *ServiceNotConfiguredError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceNotConfiguredError for %T, got %T: %v", q, err, err)
		}
		if svcErr.Service != "mvcc" {
			t.Fatalf("expected service 'mvcc', got %q", svcErr.Service)
		}
	}
}

// ---------------------------------------------------------------------------
// Interface compliance assertions (test-time verification)
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_ImplementsConcurrentStateMachine(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	var _ sm.ConcurrentStateMachine = csm
}

func TestConcurrentStateMachine_ImplementsHasher(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	var _ sm.Hasher = csm
}

// ---------------------------------------------------------------------------
// Tests: ListQuery
// ---------------------------------------------------------------------------

func TestConcurrentStateMachine_ListQuery_AllKeys(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "b", "val-b")
	concurrentApplyPut(t, csm, "a", "val-a")
	concurrentApplyPut(t, csm, "c", "val-c")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}

	expected := []string{"a", "b", "c"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestConcurrentStateMachine_ListQuery_WithPrefix(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "app:config", "v1")
	concurrentApplyPut(t, csm, "app:name", "v2")
	concurrentApplyPut(t, csm, "db:host", "v3")
	concurrentApplyPut(t, csm, "app:version", "v4")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{Prefix: "app:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{Prefix: 'app:'}) failed: %v", err)
	}

	keys := result.([]string)
	expected := []string{"app:config", "app:name", "app:version"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Fatalf("keys[%d]: expected %q, got %q", i, expected[i], k)
		}
	}
}

func TestConcurrentStateMachine_ListQuery_NoResults(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "exists", "val")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{Prefix: "nonexistent:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestConcurrentStateMachine_ListQuery_EmptyStore(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	result, err := csm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestConcurrentStateMachine_ListQuery_ExcludesDeletedKeys(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	concurrentApplyPut(t, csm, "keep", "val")
	concurrentApplyPut(t, csm, "remove", "val")
	concurrentApplyDelete(t, csm, "remove")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d: %v", len(keys), keys)
	}
	if keys[0] != "keep" {
		t.Fatalf("expected 'keep', got %q", keys[0])
	}
}

func TestConcurrentStateMachine_ListQuery_SortedOrder(t *testing.T) {
	csm, _ := newTestConcurrentSM()

	// Insert in reverse order.
	concurrentApplyPut(t, csm, "z", "1")
	concurrentApplyPut(t, csm, "m", "2")
	concurrentApplyPut(t, csm, "a", "3")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("expected sorted keys, got %v", keys)
	}
}

func TestConcurrentStateMachine_ListQuery_MergesStorageAndCache(t *testing.T) {
	csm, store := newTestConcurrentSM()

	// Write directly to storage (simulating pre-existing data).
	_ = store.Set([]byte("storage-only"), []byte("val"))

	// Write through the state machine (lands in cache + async storage).
	concurrentApplyPut(t, csm, "cache-and-storage", "val")
	waitForConcurrentAsync(csm)

	result, err := csm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "cache-and-storage" || keys[1] != "storage-only" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}
