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

//go:build !integration

package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// MVCCStateMachine: Open with hub and lessor (unique paths)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenWithHubAndLessor(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	lr := lease.NewLessor()
	s.SetWatchHub(hub)
	s.SetLessor(lr)
	dir := filepath.Join(t.TempDir(), "db")

	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with hub+lessor: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: readLastApplied with persisted value
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ReadLastApplied_WithPersistedValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)

	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 0))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("key2"), []byte("val2"), 0))},
	}
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen — readLastApplied should load the persisted value.
	s2 := NewMVCCStateMachine(nil)
	lastApplied, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close(context.Background()) })

	if lastApplied != 2 {
		t.Errorf("expected lastApplied=2, got %d", lastApplied)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: Update — not open error
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_Update_NotOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	entries := []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for not-open state machine")
	}
	var ne *NotOpenError
	if !errors.As(err, &ne) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: clearAll — empty database path (iter.First() returns false)
// ---------------------------------------------------------------------------

// TestMVCCStateMachine_ClearAll_EmptyDB exercises the early-return path in
// clearAll when the PebbleDB has no keys (iter.First() == false).
// We recover from an empty snapshot on a freshly-opened machine that has no
// data, so clearAll operates on an empty database.
func TestMVCCStateMachine_ClearAll_EmptyDB(t *testing.T) {
	// Build an empty snapshot from a freshly-opened MVCC machine.
	src := newTestMVCCStateMachine(t)

	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover into a second empty machine — clearAll hits the no-keys path.
	dst := newTestMVCCStateMachine(t)
	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot on empty snapshot: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: GetHash — not open
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_GetHash_NotOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.GetHash()
	if err == nil {
		t.Fatal("expected error for GetHash on closed state machine")
	}
	var ne *NotOpenError
	if !errors.As(err, &ne) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: Open — readLastApplied with persisted value
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ReadLastApplied_WithPersistedValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewPebbleStateMachine(nil)

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}

	cmd, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 5, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewPebbleStateMachine(nil)
	lastApplied, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close(context.Background()) })

	if lastApplied != 5 {
		t.Errorf("expected lastApplied=5, got %d", lastApplied)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: listKeys — __sys: prefix skips filter
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeys_SysPrefixSkipsFilter(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("__sys:role:admin"), []byte("data"), 0))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("user:key"), []byte("val"), 0))},
	})

	keys, err := s.listKeys("__sys:role:")
	if err != nil {
		t.Fatalf("listKeys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "__sys:role:admin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected __sys:role:admin in listKeys results, got: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleMVCCPut — with hub prev value captured
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCPutWithHub_UpdateExistingKey(t *testing.T) {
	s, hub := openStoreWithHub(t)
	_ = hub

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("hub-key"), []byte("v1"), 0))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("hub-key"), []byte("v2"), 0))},
	})

	val, err := s.Lookup(context.Background(), []byte("hub-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("v2")) {
		t.Errorf("expected v2, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleMVCCDelete — with lessor DetachKey
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCDeleteWithLessor_DetachKey(t *testing.T) {
	s, _ := openStoreWithLessor(t)
	id := grantLease(t, s, 1, 60)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("lease-key"), []byte("val"), id))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeMVCCDelete([]byte("lease-key")))},
	})

	_, err := s.Lookup(context.Background(), []byte("lease-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandlePut — with hub captures prev value
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_PutWithHub_CapturesPrevValue(t *testing.T) {
	s, hub := openStoreWithHub(t)
	_ = hub

	cmd1, err := kv.EncodePut([]byte("hub-put-key"), []byte("initial"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd1}})

	cmd2, err := kv.EncodePut([]byte("hub-put-key"), []byte("updated"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd2}})

	val, err := s.Lookup(context.Background(), []byte("hub-put-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("updated")) {
		t.Errorf("expected updated, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleDelete — with hub captures prev value
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_DeleteWithHub_CapturesPrevValue(t *testing.T) {
	s, hub := openStoreWithHub(t)
	_ = hub

	cmd1, err := kv.EncodePut([]byte("hub-del-key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd1}})

	delCmd, err := kv.EncodeDelete([]byte("hub-del-key"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})

	_, err = s.Lookup(context.Background(), []byte("hub-del-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleTransaction — failure ops executed
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_ExecutesFailureOps(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("txn-key"), []byte("actual"), 0))},
	})

	// Compare txn-key value == "expected" will fail (actual != expected).
	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("txn-key"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("expected"),
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("success"), nil, 0))
	failureOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("fallback"), nil, 0))
	txnCmd := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{successOp}, [][]byte{failureOp}))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	val, err := s.Lookup(context.Background(), []byte("txn-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("fallback")) {
		t.Errorf("expected fallback, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleTransaction — success ops with delete
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_SuccessDeletesKey(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("del-txn-key"), []byte("val"), 0))},
	})

	cmp := mustEncode(protocol.EncodeCompare(
		[]byte("del-txn-key"),
		byte(service.CompareValue),
		byte(service.CompareEqual),
		[]byte("val"),
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("del-txn-key"), nil, nil, 0))
	txnCmd := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{successOp}, nil))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	_, err := s.Lookup(context.Background(), []byte("del-txn-key"))
	if err == nil {
		t.Fatal("expected error for key deleted in transaction")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: SaveSnapshot/RecoverFromSnapshot with data
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Snapshot_WithData(t *testing.T) {
	src := openExtendedStore(t)
	dst := openExtendedStore(t)

	applyExtEntries(t, src, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("snap-key"), []byte("snap-val"), 0))},
	})

	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Put data in dst so clearAll is exercised.
	applyExtEntries(t, dst, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("old-key"), []byte("old-val"), 0))},
	})

	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	val, err := dst.Lookup(context.Background(), []byte("snap-key"))
	if err != nil {
		t.Fatalf("Lookup snap-key: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("snap-val")) {
		t.Errorf("expected snap-val, got %s", val)
	}

	_, err = dst.Lookup(context.Background(), []byte("old-key"))
	if err == nil {
		t.Fatal("expected old-key to be cleared after snapshot recovery")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: getKeyInBatch — not found
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_GetKeyInBatch_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	s.mu.Lock()
	_, err := s.getKeyInBatch(readBatch(t, s), []byte("nonexistent"))
	s.mu.Unlock()

	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleMVCCPut — with lessor AttachKey
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCPut_WithLessorAttachKey(t *testing.T) {
	s, _ := openStoreWithLessor(t)
	id := grantLease(t, s, 1, 60)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("lease-attach-key"), []byte("val"), id))},
	})

	val, err := s.Lookup(context.Background(), []byte("lease-attach-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Errorf("expected val, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: clearAll — empty database path
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_ClearAll_EmptySnapshotRecovery exercises the early-return
// path in PebbleStateMachine.clearAll when pebble has no keys, via RecoverFromSnapshot.
func TestPebbleStateMachine_ClearAll_EmptySnapshotRecovery(t *testing.T) {
	// Take a snapshot of a fresh pebble machine (empty DB).
	src := openExtendedStore(t)
	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover into another empty machine — clearAll hits iter.First()==false.
	dst := openExtendedStore(t)
	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot on empty pebble snapshot: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleExecuteOp — default (unknown op type in batch)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_Batch_UnknownOpType exercises the default branch
// in pebbleExecuteOp when a batch contains an unrecognized operation type.
func TestPebbleStateMachine_Batch_UnknownOpType(t *testing.T) {
	s := openExtendedStore(t)

	// Craft a batch with an unknown op type byte (0xFF).
	unknownOp := mustEncode(protocol.EncodeOp(0xFF, []byte("key"), []byte("val"), nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{unknownOp}))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleExecuteOp — OpMVCCPut with hub, existing key
// (captures prevKv in the transaction/batch path)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_Transaction_MVCCPutWithHub_PrevValue exercises
// the pebbleExecuteOp OpMVCCPut branch where s.hub != nil and getKeyNoLock
// succeeds, setting prevKv (existing key update via transaction).
func TestPebbleStateMachine_Transaction_MVCCPutWithHub_PrevValue(t *testing.T) {
	s, hub := openStoreWithHub(t)
	_ = hub

	// Write initial value via MVCCPut direct command.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("txn-hub-key"), []byte("v1"), 0))},
	})

	// Now update via a transaction success op — this goes through pebbleExecuteOp.
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-hub-key"), []byte("v2"), nil, 0))
	txnCmd := mustEncode(protocol.EncodeTransaction(nil, [][]byte{successOp}, nil))
	entries := []sm.Entry{{Index: 2, Cmd: txnCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: snapshot save/recover with data
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SnapshotWithData(t *testing.T) {
	src := newTestMVCCStateMachine(t)
	dst := newTestMVCCStateMachine(t)

	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("snap-key"), []byte("snap-val"), 0))},
	}
	results := make([]sm.Result, 1)
	if err := src.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update src: %v", err)
	}

	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Apply data to dst so clearAll is exercised during recovery.
	dstEntries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("dst-key"), []byte("dst-val"), 0))},
	}
	dstResults := make([]sm.Result, 1)
	if err := dst.Update(context.Background(), dstEntries, dstResults); err != nil {
		t.Fatalf("Update dst: %v", err)
	}

	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// snap-key should be readable in dst.
	_, err = dst.Lookup(context.Background(), []byte("snap-key"))
	if err != nil {
		t.Fatalf("Lookup snap-key after recover: %v", err)
	}
}
