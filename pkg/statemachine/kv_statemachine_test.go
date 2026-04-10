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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// Test helpers for KVStateMachine
// ---------------------------------------------------------------------------

func newTestKVSM(opts ...KVOption) *KVStateMachine {
	backend := NewStandardBackend()
	return NewKVStateMachine(backend, opts...)
}

func kvApplyPut(t *testing.T, k *KVStateMachine, key, value string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update(put) failed: %v", err)
	}
	return results[0]
}

func kvApplyDelete(t *testing.T, k *KVStateMachine, key string) sm.Result {
	t.Helper()
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		t.Fatalf("EncodeDelete failed: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update(delete) failed: %v", err)
	}
	return results[0]
}

func kvLookupKey(t *testing.T, k *KVStateMachine, key string) []byte {
	t.Helper()
	result, err := k.Lookup([]byte(key))
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

// ---------------------------------------------------------------------------
// KVStateMachine core tests
// ---------------------------------------------------------------------------

func TestKVStateMachine_Put_Success(t *testing.T) {
	k := newTestKVSM()
	result := kvApplyPut(t, k, "hello", "world")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := kvLookupKey(t, k, "hello")
	if string(val) != "world" {
		t.Fatalf("expected 'world', got %q", val)
	}
}

func TestKVStateMachine_Put_EmptyValue(t *testing.T) {
	k := newTestKVSM()
	result := kvApplyPut(t, k, "empty", "")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := kvLookupKey(t, k, "empty")
	if val == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(val) != 0 {
		t.Fatalf("expected empty value, got %q", val)
	}
}

func TestKVStateMachine_Get_NotFound(t *testing.T) {
	k := newTestKVSM()
	val := kvLookupKey(t, k, "nonexistent")
	if val != nil {
		t.Fatalf("expected nil, got %q", val)
	}
}

func TestKVStateMachine_Delete_Success(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "to-delete", "val")

	result := kvApplyDelete(t, k, "to-delete")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	val := kvLookupKey(t, k, "to-delete")
	if val != nil {
		t.Fatalf("expected nil after delete, got %q", val)
	}
}

func TestKVStateMachine_Delete_NonExistent(t *testing.T) {
	k := newTestKVSM()
	result := kvApplyDelete(t, k, "ghost")
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestKVStateMachine_Batch_MixedOps(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "batch-del", "to-be-removed")

	ops := [][]byte{
		protocol.EncodeOp(kv.OpPut, []byte("batch-a"), []byte("val-a"), nil, 0),
		protocol.EncodeOp(kv.OpPut, []byte("batch-b"), []byte("val-b"), nil, 0),
		protocol.EncodeOp(kv.OpDelete, []byte("batch-del"), nil, nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	entries := []sm.Entry{{Index: 10, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update(batch) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	if v := kvLookupKey(t, k, "batch-a"); string(v) != "val-a" {
		t.Fatalf("expected 'val-a', got %q", v)
	}
	if v := kvLookupKey(t, k, "batch-b"); string(v) != "val-b" {
		t.Fatalf("expected 'val-b', got %q", v)
	}
	if v := kvLookupKey(t, k, "batch-del"); v != nil {
		t.Fatalf("expected nil, got %q", v)
	}
}

func TestKVStateMachine_WatchManager(t *testing.T) {
	wm := &mockWatchManager{}
	k := newTestKVSM(WithKVWatchManager(wm))

	kvApplyPut(t, k, "w-key", "w-val")
	kvApplyDelete(t, k, "w-key")

	events := wm.getEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].deleted {
		t.Fatal("first event should not be deleted")
	}
	if !events[1].deleted {
		t.Fatal("second event should be deleted")
	}
}

func TestKVStateMachine_EmptyEntry(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: nil}}
	results := make([]sm.Result, 1)
	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update with empty entry: %v", err)
	}
	if results[0].Value != 0 {
		t.Fatalf("expected zero result for empty entry, got %d", results[0].Value)
	}
}

func TestKVStateMachine_InvalidOpcode(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: []byte{0xFF}}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for invalid opcode")
	}
	var opcodeErr *InvalidOpcodeError
	if !errors.As(err, &opcodeErr) {
		t.Fatalf("expected InvalidOpcodeError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_PutInvalidPayload(t *testing.T) {
	k := newTestKVSM()
	// Put with payload too short (only op byte, no key length).
	entries := []sm.Entry{{Index: 1, Cmd: []byte{kv.OpPut}}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated put payload")
	}
	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_DeleteInvalidPayload(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: []byte{kv.OpDelete, 0xFF}}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated delete payload")
	}
	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_PrefixScan(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "ns/a", "va")
	kvApplyPut(t, k, "ns/b", "vb")
	kvApplyPut(t, k, "other/x", "vx")

	result, err := k.Lookup(&PrefixQuery{Prefix: []byte("ns/")})
	if err != nil {
		t.Fatalf("prefix lookup failed: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
}

func TestKVStateMachine_ListKeys(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "list/a", "va")
	kvApplyPut(t, k, "list/b", "vb")
	kvApplyPut(t, k, "other/c", "vc")

	result, err := k.Lookup(kv.ListQuery{Prefix: "list/"})
	if err != nil {
		t.Fatalf("list keys failed: %v", err)
	}
	keys, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestKVStateMachine_UnsupportedQueryType(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_Snapshot(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "snap-a", "val-a")
	kvApplyPut(t, k, "snap-b", "val-b")

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := k.SaveSnapshot(&buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover into a new KVSM.
	k2 := newTestKVSM()
	if err := k2.RecoverSnapshot(&buf, stopper); err != nil {
		t.Fatalf("RecoverSnapshot failed: %v", err)
	}

	val := kvLookupKey(t, k2, "snap-a")
	if string(val) != "val-a" {
		t.Fatalf("expected 'val-a', got %q", val)
	}
	val = kvLookupKey(t, k2, "snap-b")
	if string(val) != "val-b" {
		t.Fatalf("expected 'val-b', got %q", val)
	}
}

func TestKVStateMachine_GetHash(t *testing.T) {
	k := newTestKVSM()
	h1, _ := k.GetHash()

	kvApplyPut(t, k, "hash-key", "hash-val")
	h2, _ := k.GetHash()

	if h1 == h2 {
		t.Fatal("hash should change after put")
	}
}

func TestKVStateMachine_Close(t *testing.T) {
	k := newTestKVSM()
	kvApplyPut(t, k, "close-key", "close-val")
	if err := k.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Subsystem not configured tests
// ---------------------------------------------------------------------------

func TestKVStateMachine_MVCCNotConfigured(t *testing.T) {
	k := newTestKVSM()
	_, err := k.Lookup(MVCCGetQuery{Key: []byte("k")})
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_LeaseNotConfigured(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: append([]byte{protocol.OpLeaseGrant}, make([]byte, 8)...)}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_IndexNotConfigured(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: append([]byte{protocol.OpIndexCreate}, make([]byte, 20)...)}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_TimeSeriesNotConfigured(t *testing.T) {
	k := newTestKVSM()
	entries := []sm.Entry{{Index: 1, Cmd: append([]byte{protocol.OpTSWrite}, make([]byte, 20)...)}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KVStateMachine with FastBackend
// ---------------------------------------------------------------------------

func TestKVStateMachine_FastBackend_PutAndLookup(t *testing.T) {
	store := newMemStorage()
	backend := NewFastBackend(1, 1, store, nil)
	k := NewKVStateMachine(backend)

	kvApplyPut(t, k, "fast-key", "fast-val")
	backend.wg.Wait()

	val := kvLookupKey(t, k, "fast-key")
	if string(val) != "fast-val" {
		t.Fatalf("expected 'fast-val', got %q", val)
	}

	// Verify persisted to storage.
	stored, err := store.Get([]byte("fast-key"))
	if err != nil {
		t.Fatalf("storage.Get failed: %v", err)
	}
	if string(stored) != "fast-val" {
		t.Fatalf("storage: expected 'fast-val', got %q", stored)
	}
}

// ---------------------------------------------------------------------------
// KVStateMachine with ConcurrentBackend
// ---------------------------------------------------------------------------

func TestKVStateMachine_ConcurrentBackend_PutAndLookup(t *testing.T) {
	backend := NewConcurrentBackend()
	k := NewKVStateMachine(backend)

	kvApplyPut(t, k, "conc-key", "conc-val")

	val := kvLookupKey(t, k, "conc-key")
	if string(val) != "conc-val" {
		t.Fatalf("expected 'conc-val', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// KVStateMachine with DiskBackend
// ---------------------------------------------------------------------------

func TestKVStateMachine_DiskBackend_PutAndLookup(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	k := NewKVStateMachine(backend)

	kvApplyPut(t, k, "disk-key", "disk-val")

	val := kvLookupKey(t, k, "disk-key")
	if string(val) != "disk-val" {
		t.Fatalf("expected 'disk-val', got %q", val)
	}
}
