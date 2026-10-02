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
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Test helpers for RegularStateMachine
// ---------------------------------------------------------------------------

func newTestRegularFSM(opts ...RegularOption) *RegularStateMachine {
	return NewRegularStateMachine(opts...)
}

// regularApplyPut builds a KV Put entry and applies it via Update.
func regularApplyPut(t *testing.T, rsm *RegularStateMachine, key, value string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(put) failed: %v", err)
	}
	return results[0]
}

// regularApplyDelete builds a KV Delete entry and applies it via Update.
func regularApplyDelete(t *testing.T, rsm *RegularStateMachine, key string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(delete) failed: %v", err)
	}
	return results[0]
}

// regularLookupKey performs a point lookup and returns the result.
func regularLookupKey(t *testing.T, rsm *RegularStateMachine, key string) []byte {
	t.Helper()
	result, err := rsm.Lookup(context.Background(), []byte(key))
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

// regularKeysOf extracts sorted keys from a map result.
func regularKeysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Tests: Construction and factory
// ---------------------------------------------------------------------------

func TestRegularStateMachine_New(t *testing.T) {
	rsm := NewRegularStateMachine()
	if rsm == nil {
		t.Fatal("NewRegularStateMachine returned nil")
	}
	if rsm.data == nil {
		t.Fatal("data map should be initialized")
	}
	if rsm.handlers == nil {
		t.Fatal("handlers map should be initialized")
	}
	if rsm.logger == nil {
		t.Fatal("logger should default to slog.Default()")
	}
}

func TestRegularStateMachine_NewWithOptions(t *testing.T) {
	wm := &mockWatchManager{}
	rsm := NewRegularStateMachine(
		WithRegularWatchManager(wm),
	)
	if rsm.watchManager == nil {
		t.Fatal("watchManager should be set")
	}
}

func TestRegularStateMachine_CreateFunc(t *testing.T) {
	cf := NewRegularCreateFunc()
	result := cf(42, 7)
	if result == nil {
		t.Fatal("CreateFunc returned nil")
	}
	_, ok := result.(sm.StateMachine)
	if !ok {
		t.Fatalf("expected sm.StateMachine, got %T", result)
	}
}

func TestRegularStateMachine_CreateFunc_WithOptions(t *testing.T) {
	wm := &mockWatchManager{}
	cf := NewRegularCreateFunc(WithRegularWatchManager(wm))
	result := cf(1, 1)
	rsm, ok := result.(*RegularStateMachine)
	if !ok {
		t.Fatalf("expected *RegularStateMachine, got %T", result)
	}
	if rsm.watchManager == nil {
		t.Fatal("watchManager should be set via CreateFunc options")
	}
}

// ---------------------------------------------------------------------------
// Tests: Basic KV Put
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Put_Success(t *testing.T) {
	rsm := newTestRegularFSM()

	result := regularApplyPut(t, rsm, "hello", "world")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := regularLookupKey(t, rsm, "hello")
	if string(val) != "world" {
		t.Fatalf("expected 'world', got %q", val)
	}
}

func TestRegularStateMachine_Put_EmptyValue(t *testing.T) {
	rsm := newTestRegularFSM()

	result := regularApplyPut(t, rsm, "empty-key", "")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := regularLookupKey(t, rsm, "empty-key")
	if val == nil {
		t.Fatal("expected non-nil empty slice for empty value")
	}
	if len(val) != 0 {
		t.Fatalf("expected empty value, got %q", val)
	}
}

func TestRegularStateMachine_Put_Overwrite(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "key", "first")
	regularApplyPut(t, rsm, "key", "second")

	val := regularLookupKey(t, rsm, "key")
	if string(val) != "second" {
		t.Fatalf("expected 'second' after overwrite, got %q", val)
	}
}

func TestRegularStateMachine_Put_InvalidPayload_TooShort(t *testing.T) {
	rsm := newTestRegularFSM()

	// Op = OpPut (1) but payload is too short (missing keyLen).
	cmd := []byte{kv.OpPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated put payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Put_InvalidPayload_KeyExceedsLength(t *testing.T) {
	rsm := newTestRegularFSM()

	// keyLen says 100 but only 2 bytes of payload follow.
	cmd := make([]byte, 5)
	cmd[0] = kv.OpPut
	cmd[1] = 100 // keyLen low byte
	cmd[2] = 0   // keyLen high byte
	cmd[3] = 'a'
	cmd[4] = 'b'

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Basic KV Get
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Get_Found(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "key1", "value1")
	regularApplyPut(t, rsm, "key2", "value2")

	val := regularLookupKey(t, rsm, "key1")
	if string(val) != "value1" {
		t.Fatalf("expected 'value1', got %q", val)
	}

	val = regularLookupKey(t, rsm, "key2")
	if string(val) != "value2" {
		t.Fatalf("expected 'value2', got %q", val)
	}
}

func TestRegularStateMachine_Get_NotFound(t *testing.T) {
	rsm := newTestRegularFSM()

	val := regularLookupKey(t, rsm, "nonexistent")
	if val != nil {
		t.Fatalf("expected nil for missing key, got %q", val)
	}
}

func TestRegularStateMachine_Get_ReturnsCopy(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "key", "original")

	val := regularLookupKey(t, rsm, "key")
	// Mutate the returned copy.
	val[0] = 'X'

	// The internal state should be unchanged.
	val2 := regularLookupKey(t, rsm, "key")
	if string(val2) != "original" {
		t.Fatalf("internal state was mutated: expected 'original', got %q", val2)
	}
}

// ---------------------------------------------------------------------------
// Tests: Basic KV Delete
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Delete_Success(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "to-delete", "value")

	val := regularLookupKey(t, rsm, "to-delete")
	if string(val) != "value" {
		t.Fatalf("expected 'value' before delete, got %q", val)
	}

	result := regularApplyDelete(t, rsm, "to-delete")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision from delete")
	}

	val = regularLookupKey(t, rsm, "to-delete")
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}
}

func TestRegularStateMachine_Delete_NotFound(t *testing.T) {
	rsm := newTestRegularFSM()

	// Deleting a key that does not exist should succeed without error.
	result := regularApplyDelete(t, rsm, "ghost")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := regularLookupKey(t, rsm, "ghost")
	if val != nil {
		t.Fatalf("expected nil for deleted non-existent key, got %q", val)
	}
}

func TestRegularStateMachine_Delete_InvalidPayload(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{kv.OpDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated delete payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Delete_InvalidPayload_KeyExceedsLength(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := make([]byte, 4)
	cmd[0] = kv.OpDelete
	cmd[1] = 100 // keyLen low byte = 100
	cmd[2] = 0   // keyLen high byte
	cmd[3] = 'a' // only 1 byte of key, but keyLen says 100

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for key length exceeding payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Batch operations
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Batch_MixedOps(t *testing.T) {
	rsm := newTestRegularFSM()

	// Pre-populate a key that the batch will delete.
	regularApplyPut(t, rsm, "batch-del", "to-be-removed")

	// Build batch: 2 puts + 1 delete.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("batch-a"), []byte("val-a"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("batch-b"), []byte("val-b"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("batch-del"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 10, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from batch")
	}

	// Verify puts.
	if v := regularLookupKey(t, rsm, "batch-a"); string(v) != "val-a" {
		t.Fatalf("batch put a: expected 'val-a', got %q", v)
	}
	if v := regularLookupKey(t, rsm, "batch-b"); string(v) != "val-b" {
		t.Fatalf("batch put b: expected 'val-b', got %q", v)
	}

	// Verify delete.
	if v := regularLookupKey(t, rsm, "batch-del"); v != nil {
		t.Fatalf("batch delete: expected nil, got %q", v)
	}
}

func TestRegularStateMachine_Batch_InvalidPayload(t *testing.T) {
	rsm := newTestRegularFSM()

	// Batch with truncated payload.
	cmd := []byte{protocol.OpBatch, 0xFF}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid batch payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Batch_InvalidOpcodeInBatch(t *testing.T) {
	rsm := newTestRegularFSM()

	// Build a batch with an unknown op type (0xFF).
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(0xFF, []byte("key"), []byte("val"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertOpcodeRefused(t, rsm.Update(context.Background(), entries, results), results[0])
}

// ---------------------------------------------------------------------------
// Tests: Prefix scan
// ---------------------------------------------------------------------------

func TestRegularStateMachine_PrefixScan_Success(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "ns/a", "val-a")
	regularApplyPut(t, rsm, "ns/b", "val-b")
	regularApplyPut(t, rsm, "ns/c", "val-c")
	regularApplyPut(t, rsm, "other/x", "should-not-appear")

	result, err := rsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}

	if len(m) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(m), regularKeysOf(m))
	}

	if string(m["ns/a"]) != "val-a" {
		t.Fatalf("ns/a: expected 'val-a', got %q", m["ns/a"])
	}
	if string(m["ns/b"]) != "val-b" {
		t.Fatalf("ns/b: expected 'val-b', got %q", m["ns/b"])
	}
	if string(m["ns/c"]) != "val-c" {
		t.Fatalf("ns/c: expected 'val-c', got %q", m["ns/c"])
	}
}

func TestRegularStateMachine_PrefixScan_WithLimit(t *testing.T) {
	rsm := newTestRegularFSM()

	for i := 0; i < 100; i++ {
		regularApplyPut(t, rsm, "many/key-"+string(rune('A'+i%26))+string(rune('0'+i/26)), "val")
	}

	result, err := rsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("many/"), Limit: 5})
	if err != nil {
		t.Fatalf("prefix lookup with limit failed: %v", err)
	}
	m := result.(map[string][]byte)

	if len(m) > 5 {
		t.Fatalf("expected at most 5 entries with limit, got %d", len(m))
	}
}

func TestRegularStateMachine_PrefixScan_Empty(t *testing.T) {
	rsm := newTestRegularFSM()

	result, err := rsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("nonexistent/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(m))
	}
}

func TestRegularStateMachine_PrefixScan_DeletedKeyExcluded(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "pfx/alive", "yes")
	regularApplyPut(t, rsm, "pfx/dead", "to-be-killed")
	regularApplyDelete(t, rsm, "pfx/dead")

	result, err := rsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m := result.(map[string][]byte)

	if _, found := m["pfx/dead"]; found {
		t.Fatal("deleted key pfx/dead should not appear in prefix scan")
	}
	if string(m["pfx/alive"]) != "yes" {
		t.Fatalf("pfx/alive: expected 'yes', got %q", m["pfx/alive"])
	}
}

// ---------------------------------------------------------------------------
// Tests: Snapshot save/recover
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Snapshot_SaveRecover(t *testing.T) {
	rsm1 := newTestRegularFSM()

	regularApplyPut(t, rsm1, "snap/key1", "val1")
	regularApplyPut(t, rsm1, "snap/key2", "val2")
	regularApplyPut(t, rsm1, "snap/key3", "val3")

	// Save snapshot.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := rsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Create a new FSM and recover.
	rsm2 := newTestRegularFSM()
	if err := rsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// Verify all keys are present.
	for _, tc := range []struct{ key, val string }{
		{"snap/key1", "val1"},
		{"snap/key2", "val2"},
		{"snap/key3", "val3"},
	} {
		got := regularLookupKey(t, rsm2, tc.key)
		if string(got) != tc.val {
			t.Fatalf("%s: expected %q, got %q", tc.key, tc.val, got)
		}
	}
}

func TestRegularStateMachine_Snapshot_Empty(t *testing.T) {
	rsm1 := newTestRegularFSM()

	// Save snapshot with no data.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := rsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover into a fresh FSM with pre-existing data.
	rsm2 := newTestRegularFSM()
	regularApplyPut(t, rsm2, "stale", "data")

	if err := rsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// The stale key should be gone.
	val := regularLookupKey(t, rsm2, "stale")
	if val != nil {
		t.Fatalf("expected nil after empty snapshot recover, got %q", val)
	}
}

func TestRegularStateMachine_Snapshot_Cancellation(t *testing.T) {
	rsm := newTestRegularFSM()

	// Populate enough data for the stopper check to fire.
	for i := 0; i < 100; i++ {
		regularApplyPut(t, rsm, "cancel/"+string(rune('A'+i%26))+string(rune('0'+i/26)), "val")
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	close(stopper) // Signal cancellation immediately.

	err := rsm.SaveSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		// It's possible that all entries were written before the check.
		// This is acceptable for an empty or small dataset.
		return
	}

	var snapErr *SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Snapshot_RecoverCancellation(t *testing.T) {
	rsm1 := newTestRegularFSM()
	for i := 0; i < 100; i++ {
		regularApplyPut(t, rsm1, "rc/"+string(rune('A'+i%26))+string(rune('0'+i/26)), "val")
	}

	var buf bytes.Buffer
	openStopper := make(chan struct{})
	if err := rsm1.SaveSnapshot(context.Background(), &buf, openStopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	rsm2 := newTestRegularFSM()
	closedStopper := make(chan struct{})
	close(closedStopper)

	err := rsm2.RecoverFromSnapshot(context.Background(), &buf, closedStopper)
	if err == nil {
		return // Small dataset may complete before check
	}

	var snapErr *SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Snapshot_RecoverBadData(t *testing.T) {
	rsm := newTestRegularFSM()
	stopper := make(chan struct{})

	// Truncated reader with just 1 byte (not enough for entry count).
	err := rsm.RecoverFromSnapshot(context.Background(), bytes.NewReader([]byte{0x01}), stopper)
	if err == nil {
		t.Fatal("expected error for truncated snapshot")
	}

	var snapErr *SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: GetHash
// ---------------------------------------------------------------------------

func TestRegularStateMachine_GetHash_DeterministicForSameData(t *testing.T) {
	rsm1 := newTestRegularFSM()
	rsm2 := newTestRegularFSM()

	keys := []string{"b", "a", "c"}
	for _, k := range keys {
		regularApplyPut(t, rsm1, k, "val-"+k)
		regularApplyPut(t, rsm2, k, "val-"+k)
	}

	h1, err := rsm1.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}
	h2, err := rsm2.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("hashes should match for identical data: %d != %d", h1, h2)
	}
}

func TestRegularStateMachine_GetHash_DiffersForDifferentData(t *testing.T) {
	rsm1 := newTestRegularFSM()
	rsm2 := newTestRegularFSM()

	regularApplyPut(t, rsm1, "key", "value1")
	regularApplyPut(t, rsm2, "key", "value2")

	h1, err := rsm1.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}
	h2, err := rsm2.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	if h1 == h2 {
		t.Fatal("hashes should differ for different data")
	}
}

func TestRegularStateMachine_GetHash_EmptyMap(t *testing.T) {
	rsm := newTestRegularFSM()

	h, err := rsm.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}
	// Verify empty state produces a deterministic hash.
	// xxhash of empty input is non-zero; we just confirm no error above.
	_ = h
}

// ---------------------------------------------------------------------------
// Tests: Update edge cases
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Update_EmptyEntry(t *testing.T) {
	rsm := newTestRegularFSM()

	entries := []sm.Entry{{Index: 1, Cmd: nil}}
	results := make([]sm.Result, 1)

	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with empty cmd failed: %v", err)
	}
	if results[0].Value != 0 {
		t.Fatalf("expected zero result for empty cmd, got %d", results[0].Value)
	}
}

func TestRegularStateMachine_Update_EmptyCmd(t *testing.T) {
	rsm := newTestRegularFSM()

	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)

	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with empty cmd failed: %v", err)
	}
	if results[0].Value != 0 {
		t.Fatalf("expected zero result for empty cmd, got %d", results[0].Value)
	}
}

func TestRegularStateMachine_Update_UnknownOp(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{0xFF, 0x01, 0x02}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertOpcodeRefused(t, rsm.Update(context.Background(), entries, results), results[0])
	assertRefusalNamesOpcode(t, results[0], 0xFF)
}

func TestRegularStateMachine_Update_MultipleEntries(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd1, err := kv.EncodePut([]byte("k1"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmd2, err := kv.EncodePut([]byte("k2"), []byte("v2"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	cmd3, err := kv.EncodePut([]byte("k3"), []byte("v3"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: cmd1},
		{Index: 2, Cmd: cmd2},
		{Index: 3, Cmd: cmd3},
	}
	results := make([]sm.Result, 3)

	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Each result should have an increasing revision.
	for i := 0; i < 3; i++ {
		if results[i].Value == 0 {
			t.Fatalf("results[%d] should have non-zero revision", i)
		}
	}
	if results[0].Value >= results[1].Value || results[1].Value >= results[2].Value {
		t.Fatalf("revisions should be strictly increasing: %d, %d, %d",
			results[0].Value, results[1].Value, results[2].Value)
	}

	// Verify all keys.
	for _, tc := range []struct{ key, val string }{
		{"k1", "v1"}, {"k2", "v2"}, {"k3", "v3"},
	} {
		got := regularLookupKey(t, rsm, tc.key)
		if string(got) != tc.val {
			t.Fatalf("%s: expected %q, got %q", tc.key, tc.val, got)
		}
	}
}

func TestRegularStateMachine_Update_RevisionMonotonicallyIncreases(t *testing.T) {
	rsm := newTestRegularFSM()

	var lastRevision uint64
	for i := 0; i < 50; i++ {
		result := regularApplyPut(t, rsm, "rev-test", "val")
		if result.Value <= lastRevision {
			t.Fatalf("revision %d is not greater than previous %d", result.Value, lastRevision)
		}
		lastRevision = result.Value
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch notifications
// ---------------------------------------------------------------------------

func TestRegularStateMachine_WatchNotification_PutAndDelete(t *testing.T) {
	wm := &mockWatchManager{}
	rsm := newTestRegularFSM(WithRegularWatchManager(wm))

	regularApplyPut(t, rsm, "watched", "value1")
	regularApplyDelete(t, rsm, "watched")

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 watch events, got %d", len(events))
	}

	// First event: put.
	if events[0].key != "watched" || events[0].deleted {
		t.Fatalf("event 0: expected put on 'watched', got key=%q deleted=%v",
			events[0].key, events[0].deleted)
	}
	if string(events[0].value) != "value1" {
		t.Fatalf("event 0: expected value 'value1', got %q", events[0].value)
	}

	// Second event: delete.
	if events[1].key != "watched" || !events[1].deleted {
		t.Fatalf("event 1: expected delete on 'watched', got key=%q deleted=%v",
			events[1].key, events[1].deleted)
	}
	if events[1].value != nil {
		t.Fatalf("event 1: expected nil value for delete, got %q", events[1].value)
	}
}

func TestRegularStateMachine_WatchNotification_Batch(t *testing.T) {
	wm := &mockWatchManager{}
	rsm := newTestRegularFSM(WithRegularWatchManager(wm))

	regularApplyPut(t, rsm, "batch-watch", "to-delete")

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("bw-a"), []byte("val-a"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("batch-watch"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Index: 10, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := rsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	events := wm.getEvents()
	// 1 from initial put + 2 from batch = 3
	if len(events) != 3 {
		t.Fatalf("expected 3 watch events, got %d", len(events))
	}

	// Batch put event.
	if events[1].key != "bw-a" || events[1].deleted {
		t.Fatalf("event 1: expected put on 'bw-a', got key=%q deleted=%v",
			events[1].key, events[1].deleted)
	}

	// Batch delete event.
	if events[2].key != "batch-watch" || !events[2].deleted {
		t.Fatalf("event 2: expected delete on 'batch-watch', got key=%q deleted=%v",
			events[2].key, events[2].deleted)
	}
}

func TestRegularStateMachine_WatchNotification_NoManager(t *testing.T) {
	// When no watch manager is set, operations should still succeed.
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "no-watch", "value")
	regularApplyDelete(t, rsm, "no-watch")
	// No panic or error means success.
}

// ---------------------------------------------------------------------------
// Tests: Lookup unsupported query type
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Lookup_UnsupportedQueryType(t *testing.T) {
	rsm := newTestRegularFSM()

	_, err := rsm.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Close
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Close(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "close-test", "data")

	if err := rsm.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// After close, data should be nil.
	rsm.mu.RLock()
	if rsm.data != nil {
		t.Fatal("data should be nil after Close")
	}
	rsm.mu.RUnlock()
}

// ---------------------------------------------------------------------------
// Tests: Concurrent safety
// ---------------------------------------------------------------------------

func TestRegularStateMachine_ConcurrentPuts(t *testing.T) {
	rsm := newTestRegularFSM()

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
				if err := rsm.Update(context.Background(), entries, results); err != nil {
					errCount.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	if n := errCount.Load(); n != 0 {
		t.Fatalf("concurrent puts produced %d errors", n)
	}
}

func TestRegularStateMachine_ConcurrentPutAndLookup(t *testing.T) {
	rsm := newTestRegularFSM()

	const iterations = 200
	var wg sync.WaitGroup
	var errCount atomic.Int64

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			key := "rw/" + string(rune('A'+i%26))
			cmd, err := kv.EncodePut([]byte(key), []byte("val"))
			if err != nil {
				errCount.Add(1)
				continue
			}
			entries := []sm.Entry{{Index: uint64(i), Cmd: cmd}}
			results := make([]sm.Result, 1)
			if err := rsm.Update(context.Background(), entries, results); err != nil {
				errCount.Add(1)
			}
		}
	}()

	// Reader goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			key := "rw/" + string(rune('A'+i%26))
			_, err := rsm.Lookup(context.Background(), []byte(key))
			if err != nil {
				errCount.Add(1)
			}
		}
	}()

	wg.Wait()

	if n := errCount.Load(); n != 0 {
		t.Fatalf("concurrent read/write produced %d errors", n)
	}
}

// ---------------------------------------------------------------------------
// Tests: Service not configured errors
// ---------------------------------------------------------------------------

func TestRegularStateMachine_MVCC_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	// Try MVCC lookup without configured store.
	_, err := rsm.Lookup(context.Background(), MVCCGetQuery{Key: []byte("key")})
	if err == nil {
		t.Fatal("expected error for MVCC not configured")
	}
	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
	if svcErr.Service != "mvcc" {
		t.Fatalf("expected service 'mvcc', got %q", svcErr.Service)
	}
}

func TestRegularStateMachine_MVCC_NotConfigured_PointerQuery(t *testing.T) {
	rsm := newTestRegularFSM()

	_, err := rsm.Lookup(context.Background(), &MVCCGetQuery{Key: []byte("key")})
	if err == nil {
		t.Fatal("expected error for MVCC not configured")
	}
	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_MVCC_NotConfigured_AtRevision(t *testing.T) {
	rsm := newTestRegularFSM()

	_, err := rsm.Lookup(context.Background(), MVCCGetAtRevisionQuery{Key: []byte("key"), Revision: 5})
	if err == nil {
		t.Fatal("expected error for MVCC not configured")
	}
	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_MVCC_NotConfigured_Range(t *testing.T) {
	rsm := newTestRegularFSM()

	_, err := rsm.Lookup(context.Background(), MVCCRangeQuery{Start: []byte("a"), End: []byte("z")})
	if err == nil {
		t.Fatal("expected error for MVCC not configured")
	}
	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_MVCC_NotConfigured_Revision(t *testing.T) {
	rsm := newTestRegularFSM()

	_, err := rsm.Lookup(context.Background(), MVCCRevisionQuery{})
	if err == nil {
		t.Fatal("expected error for MVCC revision not configured")
	}
	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_Lease_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	// Try a lease grant via Update.
	cmd := make([]byte, 9)
	cmd[0] = protocol.OpLeaseGrant
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

func TestRegularStateMachine_Index_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	// Try an index create via Update with minimal valid payload.
	cmd := []byte{protocol.OpIndexCreate, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("index")) {
		t.Fatalf("expected Data to mention %q, got %q", "index", results[0].Data)
	}
}

func TestRegularStateMachine_TimeSeries_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpTSWrite, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention %q, got %q", "timeseries", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Tests: MVCC Update handlers not configured
// ---------------------------------------------------------------------------

func TestRegularStateMachine_MVCCPut_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpMVCCPut, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("mvcc")) {
		t.Fatalf("expected Data to mention %q, got %q", "mvcc", results[0].Data)
	}
}

func TestRegularStateMachine_MVCCDelete_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpMVCCDelete, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("mvcc")) {
		t.Fatalf("expected Data to mention %q, got %q", "mvcc", results[0].Data)
	}
}

func TestRegularStateMachine_MVCCRange_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpMVCCRange, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("mvcc")) {
		t.Fatalf("expected Data to mention %q, got %q", "mvcc", results[0].Data)
	}
}

func TestRegularStateMachine_MVCCCompact_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpMVCCCompactPrefix, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("mvcc")) {
		t.Fatalf("expected Data to mention %q, got %q", "mvcc", results[0].Data)
	}
}

// TestRegularStateMachine_Transaction_RunsNativelyWithoutMVCCStore pins
// go-qrdb#136: with no MVCCStore injected — the "regular" sm_type as the
// registry builds it — a transaction is decided against the map itself rather
// than refused, and its writes are visible to a plain read.
func TestRegularStateMachine_Transaction_RunsNativelyWithoutMVCCStore(t *testing.T) {
	rsm := newTestRegularFSM()
	ctx := context.Background()

	seed := mustEncode(kv.EncodePut([]byte("guard"), []byte("held")))
	cmp := mustEncode(protocol.EncodeCompare([]byte("guard"), txnTargetValue, txnResultEqual, []byte("held"), 0, 0, 0))
	op := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("written"), []byte("yes"), nil, 0))
	txn := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, nil))

	results := make([]sm.Result, 2)
	if err := rsm.Update(ctx, []sm.Entry{{Index: 1, Cmd: seed}, {Index: 2, Cmd: txn}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if msg, refused := protocol.DecodeAppError(results[1].Data); refused {
		t.Fatalf("transaction refused on a state machine with no MVCC store: %s", msg)
	}
	if len(results[1].Data) != 1 || results[1].Data[0] != 1 {
		t.Fatalf("transaction whose compare holds reported Data=%v; want [1]", results[1].Data)
	}
	got, err := rsm.Lookup(ctx, []byte("written"))
	if err != nil || string(got.([]byte)) != "yes" {
		t.Fatalf("transaction write not visible to a plain read: %v, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease Update handlers not configured
// ---------------------------------------------------------------------------

func TestRegularStateMachine_LeaseRevoke_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := make([]byte, 9)
	cmd[0] = protocol.OpLeaseRevoke
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

func TestRegularStateMachine_LeaseKeepAlive_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := make([]byte, 9)
	cmd[0] = protocol.OpLeaseKeepAlive
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention %q, got %q", "lease", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Tests: Index Update handlers not configured
// ---------------------------------------------------------------------------

func TestRegularStateMachine_IndexDrop_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpIndexDrop, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("index")) {
		t.Fatalf("expected Data to mention %q, got %q", "index", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Tests: TimeSeries Update handlers not configured
// ---------------------------------------------------------------------------

func TestRegularStateMachine_TSWriteBatch_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpTSWriteBatch, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention %q, got %q", "timeseries", results[0].Data)
	}
}

func TestRegularStateMachine_TSWritePoint_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpTSWritePoint, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention %q, got %q", "timeseries", results[0].Data)
	}
}

func TestRegularStateMachine_TSDeleteSeries_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpTSDeleteSeries, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention %q, got %q", "timeseries", results[0].Data)
	}
}

func TestRegularStateMachine_TSSetRetention_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()

	cmd := []byte{protocol.OpTSSetRetention, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := rsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention %q, got %q", "timeseries", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// Tests: Snapshot round-trip preserves hash
// ---------------------------------------------------------------------------

func TestRegularStateMachine_Snapshot_PreservesHash(t *testing.T) {
	rsm1 := newTestRegularFSM()

	regularApplyPut(t, rsm1, "hash/a", "1")
	regularApplyPut(t, rsm1, "hash/b", "2")
	regularApplyPut(t, rsm1, "hash/c", "3")

	h1, err := rsm1.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := rsm1.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	rsm2 := newTestRegularFSM()
	if err := rsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	h2, err := rsm2.GetHash()
	if err != nil {
		t.Fatalf("GetHash failed: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("hash mismatch after snapshot: %d != %d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// Tests: Large data set
// ---------------------------------------------------------------------------

func TestRegularStateMachine_LargeDataSet(t *testing.T) {
	rsm := newTestRegularFSM()

	const count = 1000
	for i := 0; i < count; i++ {
		key := "large/" + string(rune('A'+i/100)) + string(rune('A'+i%100/10)) + string(rune('0'+i%10))
		regularApplyPut(t, rsm, key, "val")
	}

	// Snapshot and recover.
	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := rsm.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	rsm2 := newTestRegularFSM()
	if err := rsm2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	// Verify hash matches.
	h1, _ := rsm.GetHash()
	h2, _ := rsm2.GetHash()
	if h1 != h2 {
		t.Fatalf("hash mismatch after large snapshot: %d != %d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// Tests: ListQuery
// ---------------------------------------------------------------------------

func TestRegularStateMachine_ListQuery_AllKeys(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "b", "val-b")
	regularApplyPut(t, rsm, "a", "val-a")
	regularApplyPut(t, rsm, "c", "val-c")

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{})
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

func TestRegularStateMachine_ListQuery_WithPrefix(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "app:config", "v1")
	regularApplyPut(t, rsm, "app:name", "v2")
	regularApplyPut(t, rsm, "db:host", "v3")
	regularApplyPut(t, rsm, "app:version", "v4")

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{Prefix: "app:"})
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

func TestRegularStateMachine_ListQuery_NoResults(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "exists", "val")

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{Prefix: "nonexistent:"})
	if err != nil {
		t.Fatalf("Lookup(ListQuery) failed: %v", err)
	}

	keys := result.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestRegularStateMachine_ListQuery_EmptyStore(t *testing.T) {
	rsm := newTestRegularFSM()

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if keys == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(keys) != 0 {
		t.Fatalf("expected empty slice, got %v", keys)
	}
}

func TestRegularStateMachine_ListQuery_ExcludesDeletedKeys(t *testing.T) {
	rsm := newTestRegularFSM()

	regularApplyPut(t, rsm, "keep", "val")
	regularApplyPut(t, rsm, "remove", "val")
	regularApplyDelete(t, rsm, "remove")

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{})
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

func TestRegularStateMachine_ListQuery_SortedOrder(t *testing.T) {
	rsm := newTestRegularFSM()

	// Insert in reverse order.
	regularApplyPut(t, rsm, "z", "1")
	regularApplyPut(t, rsm, "m", "2")
	regularApplyPut(t, rsm, "a", "3")

	result, err := rsm.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup(ListQuery{}) failed: %v", err)
	}

	keys := result.([]string)
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("expected sorted keys, got %v", keys)
	}
}
