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
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// pebble.go: evaluateCompareResult - all branches
// ---------------------------------------------------------------------------

func TestEvaluateCompareResult_Equal(t *testing.T) {
	if !evaluateCompareResult(0, 0) {
		t.Fatal("expected true for Equal with cmp=0")
	}
	if evaluateCompareResult(0, 1) {
		t.Fatal("expected false for Equal with cmp=1")
	}
}

func TestEvaluateCompareResult_Greater(t *testing.T) {
	if !evaluateCompareResult(1, 1) {
		t.Fatal("expected true for Greater with cmp=1")
	}
	if evaluateCompareResult(1, 0) {
		t.Fatal("expected false for Greater with cmp=0")
	}
	if evaluateCompareResult(1, -1) {
		t.Fatal("expected false for Greater with cmp=-1")
	}
}

func TestEvaluateCompareResult_Less(t *testing.T) {
	if !evaluateCompareResult(2, -1) {
		t.Fatal("expected true for Less with cmp=-1")
	}
	if evaluateCompareResult(2, 0) {
		t.Fatal("expected false for Less with cmp=0")
	}
	if evaluateCompareResult(2, 1) {
		t.Fatal("expected false for Less with cmp=1")
	}
}

func TestEvaluateCompareResult_NotEqual(t *testing.T) {
	if !evaluateCompareResult(3, 1) {
		t.Fatal("expected true for NotEqual with cmp=1")
	}
	if !evaluateCompareResult(3, -1) {
		t.Fatal("expected true for NotEqual with cmp=-1")
	}
	if evaluateCompareResult(3, 0) {
		t.Fatal("expected false for NotEqual with cmp=0")
	}
}

func TestEvaluateCompareResult_InvalidResult(t *testing.T) {
	if evaluateCompareResult(99, 0) {
		t.Fatal("expected false for unknown result byte")
	}
	if evaluateCompareResult(255, 1) {
		t.Fatal("expected false for unknown result byte 255")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: evaluateCompareInt - all branches
// ---------------------------------------------------------------------------

func TestEvaluateCompareInt_Equal(t *testing.T) {
	if !evaluateCompareInt(0, 5, 5) {
		t.Fatal("expected true for Equal 5==5")
	}
	if evaluateCompareInt(0, 5, 6) {
		t.Fatal("expected false for Equal 5==6")
	}
}

func TestEvaluateCompareInt_Greater(t *testing.T) {
	if !evaluateCompareInt(1, 10, 5) {
		t.Fatal("expected true for Greater 10>5")
	}
	if evaluateCompareInt(1, 5, 10) {
		t.Fatal("expected false for Greater 5>10")
	}
	if evaluateCompareInt(1, 5, 5) {
		t.Fatal("expected false for Greater 5>5")
	}
}

func TestEvaluateCompareInt_Less(t *testing.T) {
	if !evaluateCompareInt(2, 3, 5) {
		t.Fatal("expected true for Less 3<5")
	}
	if evaluateCompareInt(2, 5, 3) {
		t.Fatal("expected false for Less 5<3")
	}
	if evaluateCompareInt(2, 5, 5) {
		t.Fatal("expected false for Less 5<5")
	}
}

func TestEvaluateCompareInt_NotEqual(t *testing.T) {
	if !evaluateCompareInt(3, 5, 6) {
		t.Fatal("expected true for NotEqual 5!=6")
	}
	if evaluateCompareInt(3, 5, 5) {
		t.Fatal("expected false for NotEqual 5!=5")
	}
}

func TestEvaluateCompareInt_InvalidResult(t *testing.T) {
	if evaluateCompareInt(99, 5, 5) {
		t.Fatal("expected false for unknown result byte")
	}
	if evaluateCompareInt(255, 0, 0) {
		t.Fatal("expected false for unknown result byte 255")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleEvaluateCompare - additional target branches
// ---------------------------------------------------------------------------

func TestPebbleEvaluateCompare_CreateRevision(t *testing.T) {
	s := openExtendedStore(t)

	// Put a key.
	putCmd := protocol.EncodeMVCCPut([]byte("cr-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// CompareCreateRevision == 0 (non-MVCC mode always returns 0).
	dc := protocol.DecodedCompare{
		Key:            []byte("cr-key"),
		Target:         1, // CompareCreateRevision
		Result:         0, // Equal
		CreateRevision: 0,
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected create revision compare to succeed (0 == 0)")
	}

	// CompareCreateRevision != 0 should fail.
	dc.CreateRevision = 5
	if pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected create revision compare to fail (0 != 5)")
	}
}

func TestPebbleEvaluateCompare_ModRevision(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("mr-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// CompareModRevision == 0 (non-MVCC mode always returns 0).
	dc := protocol.DecodedCompare{
		Key:         []byte("mr-key"),
		Target:      2, // CompareModRevision
		Result:      0, // Equal
		ModRevision: 0,
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected mod revision compare to succeed (0 == 0)")
	}

	// CompareModRevision != 0 should fail.
	dc.ModRevision = 3
	if pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected mod revision compare to fail (0 != 3)")
	}
}

func TestPebbleEvaluateCompare_DefaultTarget(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("dt-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Unknown target byte.
	dc := protocol.DecodedCompare{
		Key:    []byte("dt-key"),
		Target: 99, // unknown
		Result: 0,
	}
	if pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected false for unknown target")
	}
}

func TestPebbleEvaluateCompare_ValueGreater(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("vg-key"), []byte("bbb"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Value compare: actual "bbb" > expected "aaa" => true.
	dc := protocol.DecodedCompare{
		Key:    []byte("vg-key"),
		Target: 3, // CompareValue
		Result: 1, // Greater
		Value:  []byte("aaa"),
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected value greater compare to succeed")
	}
}

func TestPebbleEvaluateCompare_ValueLess(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("vl-key"), []byte("aaa"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Value compare: actual "aaa" < expected "bbb" => true.
	dc := protocol.DecodedCompare{
		Key:    []byte("vl-key"),
		Target: 3, // CompareValue
		Result: 2, // Less
		Value:  []byte("bbb"),
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected value less compare to succeed")
	}
}

func TestPebbleEvaluateCompare_ValueNotEqual(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("vne-key"), []byte("hello"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	dc := protocol.DecodedCompare{
		Key:    []byte("vne-key"),
		Target: 3, // CompareValue
		Result: 3, // NotEqual
		Value:  []byte("world"),
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected value not-equal compare to succeed")
	}
}

func TestPebbleEvaluateCompare_NonexistentKeyValueNotEmpty(t *testing.T) {
	s := openExtendedStore(t)

	// Value compare on nonexistent key: only "equal to empty" succeeds.
	dc := protocol.DecodedCompare{
		Key:    []byte("nokey"),
		Target: 3, // CompareValue
		Result: 0, // Equal
		Value:  []byte("notempty"),
	}
	if pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected false for nonexistent key with non-empty value compare")
	}
}

func TestPebbleEvaluateCompare_VersionGreater(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("vgk"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Key exists => version=1, compare 1 > 0.
	dc := protocol.DecodedCompare{
		Key:     []byte("vgk"),
		Target:  0, // CompareVersion
		Result:  1, // Greater
		Version: 0,
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected version greater compare to succeed (1 > 0)")
	}
}

func TestPebbleEvaluateCompare_VersionNonexistent(t *testing.T) {
	s := openExtendedStore(t)

	// Nonexistent key => version=0, compare 0 == 0.
	dc := protocol.DecodedCompare{
		Key:     []byte("nokey-ver"),
		Target:  0, // CompareVersion
		Result:  0, // Equal
		Version: 0,
	}
	if !pebbleEvaluateCompare(s, dc) {
		t.Fatal("expected version equal compare to succeed for nonexistent key (0 == 0)")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleExecuteOp - Delete path with hub
// ---------------------------------------------------------------------------

func TestPebbleExecuteOp_DeleteWithHub(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put a key first.
	putCmd := protocol.EncodeMVCCPut([]byte("exec-del"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Set up a watcher.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "exec-del")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Execute batch delete via the extended protocol path.
	delOp := protocol.EncodeOp(protocol.OpMVCCDelete, []byte("exec-del"), nil, nil, 0)
	batchCmd := protocol.EncodeBatch([][]byte{delOp})
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: batchCmd}})

	// Verify delete event.
	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

func TestPebbleExecuteOp_PutWithHubAndLessor(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	hub := watch.NewHub()
	s.SetLessor(lr)
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Set up watcher.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "batch-leased")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Batch put with leaseID via the batch/executeOp path.
	putOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("batch-leased"), []byte("val"), nil, leaseID)
	batchCmd := protocol.EncodeBatch([][]byte{putOp})
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: batchCmd}})

	// Verify watch event.
	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for put event")
	}

	// Verify lease attachment.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist")
	}
}

func TestPebbleExecuteOp_UnknownOpType(t *testing.T) {
	s := openExtendedStore(t)

	unknownOp := protocol.EncodeOp(0x99, []byte("k"), []byte("v"), nil, 0)
	batchCmd := protocol.EncodeBatch([][]byte{unknownOp})

	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown op type in batch")
	}
	var uoe *protocol.UnknownOpError
	if !errors.As(err, &uoe) {
		t.Fatalf("expected *UnknownOpError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleMVCCDelete with watch hub
// ---------------------------------------------------------------------------

func TestPebbleHandleMVCCDelete_WithHub(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put then delete via MVCC protocol.
	putCmd := protocol.EncodeMVCCPut([]byte("mvcc-del-hub"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "mvcc-del-hub")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	delCmd := protocol.EncodeMVCCDelete([]byte("mvcc-del-hub"))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

func TestPebbleHandleMVCCDelete_NonexistentKey(t *testing.T) {
	s := openExtendedStore(t)

	// Delete a key that never existed - should not error.
	delCmd := protocol.EncodeMVCCDelete([]byte("never-existed"))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseRevoke with watch hub
// ---------------------------------------------------------------------------

func TestPebbleHandleLeaseRevoke_WithHub(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	hub := watch.NewHub()
	s.SetLessor(lr)
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease and attach a key.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	putCmd := protocol.EncodeMVCCPut([]byte("lr-key"), []byte("lr-val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "lr-key")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Revoke the lease - should emit delete notification for attached key.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	// Verify delete event.
	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event from lease revoke")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseGrant invalid payload
// ---------------------------------------------------------------------------

func TestPebbleHandleLeaseGrant_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseGrant} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease grant payload")
	}
}

func TestPebbleHandleLeaseGrant_ValidPayload(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	cmd := protocol.EncodeLeaseGrant(30)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	leaseID := int64(results[0].Value)
	if leaseID <= 0 {
		t.Fatalf("expected positive lease ID, got %d", leaseID)
	}
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseKeepAlive invalid payload
// ---------------------------------------------------------------------------

func TestPebbleHandleLeaseKeepAlive_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseKeepAlive} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease keep-alive payload")
	}
}

func TestPebbleHandleLeaseKeepAlive_NonexistentLease(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := protocol.EncodeLeaseKeepAlive(99999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for nonexistent lease")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseRevoke invalid payload
// ---------------------------------------------------------------------------

func TestPebbleHandleLeaseRevoke_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseRevoke} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease revoke payload")
	}
}

func TestPebbleHandleLeaseRevoke_NonexistentLease(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := protocol.EncodeLeaseRevoke(99999)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for nonexistent lease revoke")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleMVCCPut with hub (watch notification path)
// ---------------------------------------------------------------------------

func TestPebbleHandleMVCCPut_WithHubPrevValue(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put initial value.
	putCmd := protocol.EncodeMVCCPut([]byte("mvcc-put-hub"), []byte("v1"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "mvcc-put-hub", watch.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Overwrite - prevKv should be captured.
	putCmd2 := protocol.EncodeMVCCPut([]byte("mvcc-put-hub"), []byte("v2"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd2}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
		if resp.Events[0].PrevKv == nil {
			t.Fatal("expected PrevKv to be set")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for overwrite put event")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandlePut/Delete with hub (base KV watch notification)
// ---------------------------------------------------------------------------

func TestPebbleHandlePut_WithHub(t *testing.T) {
	s, hub := openStoreWithHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "base-put-key")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	putCmd, err := kv.EncodePut([]byte("base-put-key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for base put event")
	}
}

func TestPebbleHandleDelete_WithHub(t *testing.T) {
	s, hub := openStoreWithHub(t)

	putCmd, err := kv.EncodePut([]byte("base-del-key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "base-del-key", watch.WithPrevKV())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	delCmd, err := kv.EncodeDelete([]byte("base-del-key"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
		if resp.Events[0].PrevKv == nil {
			t.Fatal("expected PrevKv to be set for delete")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for base delete event")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleIndexCreate/Drop invalid payloads
// ---------------------------------------------------------------------------

func TestPebbleHandleIndexCreate_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpIndexCreate} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid index create payload")
	}
}

func TestPebbleHandleIndexDrop_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpIndexDrop} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid index drop payload")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: TS handler invalid payloads
// ---------------------------------------------------------------------------

func TestPebbleHandleTSWrite_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWrite} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write payload")
	}
}

func TestPebbleHandleTSWriteBatch_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWriteBatch} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write batch payload")
	}
}

func TestPebbleHandleTSWritePoint_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWritePoint} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS write point payload")
	}
}

func TestPebbleHandleTSDeleteSeries_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSDeleteSeries} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS delete series payload")
	}
}

func TestPebbleHandleTSSetRetention_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSSetRetention} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TS set retention payload")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: TSDeleteSeries with no time bounds (zero start/end)
// ---------------------------------------------------------------------------

func TestPebbleHandleTSDeleteSeries_NoTimeBounds(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	writeCmd := protocol.EncodeTSWrite("ts-del-no-bounds", now, 1.0, nil)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	// Delete with zero start/end (no time bounds).
	delCmd := protocol.EncodeTSDeleteSeries("ts-del-no-bounds", 0, 0, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Lookup - TSGetRetention and TSListMetrics with valid storage
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_TSGetRetention(t *testing.T) {
	s := openStoreWithTS(t)

	// Set retention policy first.
	setCmd := protocol.EncodeTSSetRetention("ret-metric", int64(24*time.Hour), int64(time.Minute), 1)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: setCmd}})

	// Now query it.
	result, err := s.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "ret-metric"})
	if err != nil {
		t.Fatalf("Lookup TSGetRetentionRequest: %v", err)
	}
	policy := result.(*timeseries.RetentionPolicy)
	if policy.Duration != 24*time.Hour {
		t.Fatalf("expected 24h duration, got %v", policy.Duration)
	}
}

func TestPebbleStateMachine_Lookup_TSAggregate(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now()
	writeCmd := protocol.EncodeTSWrite("agg-metric", now.UnixNano(), 42.0, nil)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	result, err := s.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric:   "agg-metric",
		Start:    now.Add(-time.Minute),
		End:      now.Add(time.Minute),
		Func:     timeseries.AggAvg,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("Lookup TSAggregateRequest: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil aggregate result")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Lookup - IndexScan with valid index
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_IndexScan(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := protocol.EncodeIndexCreate("scan_idx", "field", false, false, false)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	idx := s.indexes["scan_idx"]
	if err := idx.Put([]byte("10"), []byte("aaa")); err != nil {
		t.Fatalf("index Put: %v", err)
	}
	if err := idx.Put([]byte("20"), []byte("bbb")); err != nil {
		t.Fatalf("index Put: %v", err)
	}

	result, err := s.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "scan_idx",
		Start:     []byte("aaa"),
		End:       []byte("ccc"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexScanRequest: %v", err)
	}
	scanResult := result.(map[string][][]byte)
	if len(scanResult) == 0 {
		t.Fatal("expected non-empty scan result")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Lookup - IndexListRequest with no indexes
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_IndexListEmpty(t *testing.T) {
	s := openExtendedStore(t)

	result, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := result.([]string)
	if len(names) != 0 {
		t.Fatalf("expected empty index list, got %v", names)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Transaction compare result Greater/Less/NotEqual
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_CompareGreater(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("gt-key"), []byte("bbb"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	compare := protocol.EncodeCompare(
		[]byte("gt-key"),
		3, // CompareValue
		1, // Greater
		[]byte("aaa"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("gt-result"), []byte("yes"), nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed with Greater compare")
	}
}

func TestPebbleStateMachine_Transaction_CompareLess(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("lt-key"), []byte("aaa"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	compare := protocol.EncodeCompare(
		[]byte("lt-key"),
		3, // CompareValue
		2, // Less
		[]byte("bbb"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("lt-result"), []byte("yes"), nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed with Less compare")
	}
}

func TestPebbleStateMachine_Transaction_CompareNotEqual(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("ne-key"), []byte("hello"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	compare := protocol.EncodeCompare(
		[]byte("ne-key"),
		3, // CompareValue
		3, // NotEqual
		[]byte("world"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("ne-result"), []byte("yes"), nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed with NotEqual compare")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Transaction compare on CreateRevision / ModRevision targets
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_CompareCreateRevision(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("cr-txn-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Non-MVCC: createRevision is always 0. Compare 0 == 0 should succeed.
	compare := protocol.EncodeCompare(
		[]byte("cr-txn-key"),
		1, // CompareCreateRevision
		0, // Equal
		nil,
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("cr-result"), []byte("ok"), nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed for create revision == 0")
	}
}

func TestPebbleStateMachine_Transaction_CompareModRevision(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("mr-txn-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Non-MVCC: modRevision is always 0. Compare 0 == 0 should succeed.
	compare := protocol.EncodeCompare(
		[]byte("mr-txn-key"),
		2, // CompareModRevision
		0, // Equal
		nil,
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("mr-result"), []byte("ok"), nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed for mod revision == 0")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Transaction with delete in ops
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_WithDeleteOps(t *testing.T) {
	s := openExtendedStore(t)

	putCmd := protocol.EncodeMVCCPut([]byte("txn-del-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Compare value == "val" -> success deletes the key.
	compare := protocol.EncodeCompare(
		[]byte("txn-del-key"),
		3, // CompareValue
		0, // Equal
		[]byte("val"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCDelete, []byte("txn-del-key"), nil, nil, 0)
	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed")
	}

	// Key should be gone.
	_, err := s.Lookup(context.Background(), []byte("txn-del-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleMVCCPut with lessor attachment
// ---------------------------------------------------------------------------

func TestPebbleHandleMVCCPut_WithLessorAttachment(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Put with leaseID.
	putCmd := protocol.EncodeMVCCPut([]byte("leased-key"), []byte("val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	// Verify key is stored.
	val, err := s.Lookup(context.Background(), []byte("leased-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected val, got %s", val)
	}

	// Revoke should delete the key.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	// Key should be gone.
	_, err = s.Lookup(context.Background(), []byte("leased-key"))
	if err == nil {
		t.Fatal("expected error for deleted key after lease revoke")
	}

	_ = lr // used for cleanup
}

// ---------------------------------------------------------------------------
// pebble.go: Empty command error path
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UpdateEmptyCommand(t *testing.T) {
	s := openExtendedStore(t)

	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: clearAll on empty DB
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ClearAllEmptyDB(t *testing.T) {
	s := openExtendedStore(t)

	// RecoverFromSnapshot on empty DB triggers clearAll with no keys.
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint32(1)); err != nil {
		t.Fatal(err)
	}
	key := []byte("k")
	val := []byte("v")
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(key))); err != nil {
		t.Fatal(err)
	}
	buf.Write(key)
	if err := binary.Write(&buf, binary.LittleEndian, uint32(len(val))); err != nil {
		t.Fatal(err)
	}
	buf.Write(val)

	// This should succeed even though clearAll iterates empty DB.
	if err := s.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot on empty DB: %v", err)
	}

	// Verify the recovered key exists.
	result, err := s.Lookup(context.Background(), []byte("k"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(result.([]byte), []byte("v")) {
		t.Fatalf("expected 'v', got %s", result)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: SaveSnapshot with failing writer (PebbleStateMachine)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SaveSnapshotWriteError(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{putEntry(1, "k", "v")})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	err = s.SaveSnapshot(context.Background(), snapCtx, errWriter{}, nil)
	if err == nil {
		t.Fatal("expected error from SaveSnapshot with failing writer")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine with corrupted lastApplied metadata
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_OpenWithCorruptedLastApplied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	if err := db.Set([]byte(metaKeyLastApplied), []byte{1, 2, 3, 4, 5}, pebble.Sync); err != nil {
		t.Fatalf("Set corrupted metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := NewPebbleStateMachine(nil)
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with corrupted metadata: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected last applied 0 for corrupted metadata, got %d", idx)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Accessor methods
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_DBReturnsNilWhenNotOpened(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.DB() != nil {
		t.Fatal("expected nil DB before Open")
	}
}

func TestPebbleStateMachine_DBReturnsNonNilAfterOpen(t *testing.T) {
	s := openStore(t)
	if s.DB() == nil {
		t.Fatal("expected non-nil DB after Open")
	}
}

func TestPebbleStateMachine_LessorReturnsNil(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil lessor")
	}
}

func TestPebbleStateMachine_TimeSeriesReturnsNil(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.TimeSeries() != nil {
		t.Fatal("expected nil time series")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleMVCCDelete with lessor detach key path
// ---------------------------------------------------------------------------

func TestMVCCHandleMVCCDelete_WithLessorDetach(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	results := make([]sm.Result, 1)
	entries := []sm.Entry{{Index: 1, Cmd: grantCmd}}
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	// Put key with lease attached.
	putEntry := mvccPutEntry(2, []byte("leased-del"), []byte("val"), leaseID)
	applyMVCCEntries(t, s, []sm.Entry{putEntry})

	// Delete the key - should detach from lease.
	delEntry := mvccDeleteEntry(3, []byte("leased-del"))
	applyMVCCEntries(t, s, []sm.Entry{delEntry})

	// Key should be deleted.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("leased-del")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestMVCCHandleMVCCDelete_WithLessorNoLeaseOnKey(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Put key without lease.
	putE := mvccPutEntry(1, []byte("no-lease-del"), []byte("val"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Delete - no lease to detach, should still work.
	delE := mvccDeleteEntry(2, []byte("no-lease-del"))
	applyMVCCEntries(t, s, []sm.Entry{delE})

	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("no-lease-del")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleMVCCPut with lessor attachment
// ---------------------------------------------------------------------------

func TestMVCCHandleMVCCPut_WithLessorAttach(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	// Put with lease.
	putE := mvccPutEntry(2, []byte("mvcc-leased"), []byte("val"), leaseID)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Verify the key is attached to the lease by revoking.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	rResults := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: revokeCmd}}, rResults); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if rResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", rResults[0].Value)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup - Lease queries with lessor
// ---------------------------------------------------------------------------

func TestMVCCLookup_LeaseTimeToLiveFound(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	grantCmd := protocol.EncodeLeaseGrant(30)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	result, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("Lookup LeaseTimeToLiveQuery: %v", err)
	}
	l := result.(*service.Lease)
	if l.ID != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, l.ID)
	}
}

func TestMVCCLookup_LeaseTimeToLiveNotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	_, err = s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 99999})
	if err == nil {
		t.Fatal("expected error for nonexistent lease")
	}
}

func TestMVCCLookup_LeaseTimeToLiveNoLessor(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), service.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestMVCCLookup_LeasesNoLessor(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), service.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestMVCCLookup_LeasesWithLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	grantCmd := protocol.EncodeLeaseGrant(10)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	result, err := s.Lookup(context.Background(), service.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup LeasesQuery: %v", err)
	}
	leases := result.([]*service.Lease)
	if len(leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(leases))
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup - Index queries
// ---------------------------------------------------------------------------

func TestMVCCLookup_IndexQueryNotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), service.IndexQueryRequest{
		IndexName: "missing",
		Value:     []byte("test"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

func TestMVCCLookup_IndexScanNotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), service.IndexScanRequest{
		IndexName: "missing",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index scan")
	}
}

func TestMVCCLookup_IndexListEmpty(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	result, err := s.Lookup(context.Background(), service.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := result.([]string)
	if len(names) != 0 {
		t.Fatalf("expected empty index list, got %v", names)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup - TS queries no storage
// ---------------------------------------------------------------------------

func TestMVCCLookup_TSQueryNoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	_, err := s.Lookup(context.Background(), service.TSQueryRequest{
		Metric: "m",
		Start:  time.Now(),
		End:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error when TS not configured")
	}
}

func TestMVCCLookup_TSAggregateNoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	_, err := s.Lookup(context.Background(), service.TSAggregateRequest{
		Metric:   "m",
		Start:    time.Now(),
		End:      time.Now(),
		Func:     1,
		Interval: time.Second,
	})
	if err == nil {
		t.Fatal("expected error when TS not configured")
	}
}

func TestMVCCLookup_TSRichQueryNoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	_, err := s.Lookup(context.Background(), service.TSRichQueryRequest{})
	if err == nil {
		t.Fatal("expected error when TS not configured")
	}
}

func TestMVCCLookup_TSGetRetentionNoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	_, err := s.Lookup(context.Background(), service.TSGetRetentionRequest{Metric: "m"})
	if err == nil {
		t.Fatal("expected error when TS not configured")
	}
}

func TestMVCCLookup_TSListMetricsNoStorage(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	_, err := s.Lookup(context.Background(), service.TSListMetricsRequest{Prefix: "", Limit: 10})
	if err == nil {
		t.Fatal("expected error when TS not configured")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Accessor methods
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SetWatchHub(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	defer hub.Close()

	// Hub should be set (no public accessor, but exercise the path).
	if s.hub != hub {
		t.Fatal("expected hub to be set")
	}
}

func TestMVCCStateMachine_SetLessor(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	defer lr.Close()

	if s.Lessor() != lr {
		t.Fatal("expected lessor to be set")
	}
}

func TestMVCCStateMachine_LessorNil(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil lessor")
	}
}

func TestMVCCStateMachine_TimeSeriesNil(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.TimeSeries() != nil {
		t.Fatal("expected nil time series")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: executeDecodedOpWithNotify - Put path with lessor
// ---------------------------------------------------------------------------

func TestMVCCExecuteDecodedOpWithNotify_PutWithLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	hub := watch.NewHub()
	s.SetLessor(lr)
	s.SetWatchHub(hub)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	// Batch put with lease -> exercises executeDecodedOpWithNotify Put path with lessor.
	putOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("batch-lease"), []byte("val"), nil, leaseID)
	batchCmd := protocol.EncodeBatch([][]byte{putOp})
	batchResults := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: batchCmd}}, batchResults); err != nil {
		t.Fatalf("Update batch: %v", err)
	}

	// Verify key.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("batch-lease")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if kv.LeaseID != leaseID {
		t.Fatalf("expected leaseID %d, got %d", leaseID, kv.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: executeDecodedOpWithNotify - Delete path with lessor
// ---------------------------------------------------------------------------

func TestMVCCExecuteDecodedOpWithNotify_DeleteWithLessor(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant and put with lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	putE := mvccPutEntry(2, []byte("batch-del-lease"), []byte("val"), leaseID)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Batch delete -> exercises executeDecodedOpWithNotify Delete path with lessor.
	delOp := protocol.EncodeOp(protocol.OpMVCCDelete, []byte("batch-del-lease"), nil, nil, 0)
	batchCmd := protocol.EncodeBatch([][]byte{delOp})
	batchResults := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: batchCmd}}, batchResults); err != nil {
		t.Fatalf("Update batch delete: %v", err)
	}

	// Key should be gone.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("batch-del-lease")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleMVCCCompact error path (revision 0)
// ---------------------------------------------------------------------------

func TestMVCCHandleMVCCCompact_RevisionZero(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Compact at revision 0 - this should be handled by the MVCC store.
	compactCmd := protocol.EncodeMVCCCompact(0)
	entries := []sm.Entry{{Index: 1, Cmd: compactCmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	// Compacting at revision 0 should succeed (no-op or handled gracefully).
	if err != nil {
		t.Fatalf("Compact at revision 0: %v", err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleLeaseGrant/Revoke/KeepAlive - no lessor error
// ---------------------------------------------------------------------------

func TestMVCCHandleLeaseGrant_NoLessor(t *testing.T) {
	s := newTestMVCCStateMachine(t) // no lessor

	cmd := protocol.EncodeLeaseGrant(10)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestMVCCHandleLeaseRevoke_NoLessor(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := protocol.EncodeLeaseRevoke(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestMVCCHandleLeaseKeepAlive_NoLessor(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := protocol.EncodeLeaseKeepAlive(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleLeaseGrant/Revoke/KeepAlive - invalid payloads
// ---------------------------------------------------------------------------

func TestMVCCHandleLeaseGrant_InvalidPayload(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	cmd := []byte{protocol.OpLeaseGrant} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease grant payload")
	}
}

func TestMVCCHandleLeaseRevoke_InvalidPayload(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	cmd := []byte{protocol.OpLeaseRevoke} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease revoke payload")
	}
}

func TestMVCCHandleLeaseKeepAlive_InvalidPayload(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	cmd := []byte{protocol.OpLeaseKeepAlive} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid lease keep-alive payload")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCRangeQuery with CountOnly and KeysOnly options
// ---------------------------------------------------------------------------

func TestMVCCLookup_RangeQueryWithCountOnly(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("c"), []byte("3"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:     []byte("a"),
		End:       []byte("d"),
		CountOnly: true,
	})
	if err != nil {
		t.Fatalf("Lookup range: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	// CountOnly should return count in KeyValues or set count field.
	if rr == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestMVCCLookup_RangeQueryWithKeysOnly(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("b"), []byte("2"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:    []byte("a"),
		End:      []byte("c"),
		KeysOnly: true,
	})
	if err != nil {
		t.Fatalf("Lookup range: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if rr == nil {
		t.Fatal("expected non-nil result")
	}
	for _, kv := range rr.KeyValues {
		if len(kv.Value) > 0 {
			t.Fatalf("expected empty values with KeysOnly, got %q", kv.Value)
		}
	}
}

func TestMVCCLookup_RangeQueryWithRevision(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("v1"), 0),
	})
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(2, []byte("a"), []byte("v2"), 0),
	})

	val, err := s.Lookup(context.Background(), service.MVCCRangeQuery{
		Start:    []byte("a"),
		End:      []byte("b"),
		Revision: 2,
	})
	if err != nil {
		t.Fatalf("Lookup range at revision: %v", err)
	}
	rr := val.(*service.MVCCRangeResult)
	if len(rr.KeyValues) != 1 {
		t.Fatalf("expected 1 key-value, got %d", len(rr.KeyValues))
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Update with watch hub delivering notifications
// ---------------------------------------------------------------------------

func TestMVCCUpdate_WatchPutNotification(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "mvcc-watch")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	putE := mvccPutEntry(1, []byte("mvcc-watch"), []byte("val"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestMVCCUpdate_WatchDeleteNotification(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})

	putE := mvccPutEntry(1, []byte("mvcc-del-watch"), []byte("val"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "mvcc-del-watch")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	delE := mvccDeleteEntry(2, []byte("mvcc-del-watch"))
	applyMVCCEntries(t, s, []sm.Entry{delE})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delete event")
	}
}

// ---------------------------------------------------------------------------
// factory.go: WithWALDir option
// ---------------------------------------------------------------------------

func TestWithWALDir_SetsWALDir(t *testing.T) {
	walDir := t.TempDir()
	opt := WithWALDir(walDir)
	pOpts := &pebble.Options{}
	opt(pOpts)
	if pOpts.WALDir != walDir {
		t.Fatalf("expected WALDir %q, got %q", walDir, pOpts.WALDir)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: Lookup with string query (PebbleStateMachine)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LookupStringKeyNotFound(t *testing.T) {
	s := openStore(t)
	_, err := s.Lookup(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing key via string")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine.listKeys excludes __sys: keys
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysExcludesSysKeys(t *testing.T) {
	s := openStore(t)

	// Write system and user keys.
	applyEntries(t, s, []sm.Entry{
		putEntry(1, "__sys:role:admin", "data"),
		putEntry(2, "user:key", "val"),
	})

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)
	for _, k := range keys {
		if len(k) >= 6 && k[:6] == "__sys:" {
			t.Fatalf("system key should be excluded: %q", k)
		}
	}
	found := false
	for _, k := range keys {
		if k == "user:key" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:key to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.listKeys excludes __sys: keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ListKeysExcludesSysKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:test:entry"), []byte("data"), 0),
		mvccPutEntry(2, []byte("user:key"), []byte("val"), 0),
	})

	result, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := result.([]string)
	for _, k := range keys {
		if len(k) >= 6 && k[:6] == "__sys:" {
			t.Fatalf("system key should be excluded: %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine.listKeys excludes TimeSeries internal keys
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysExcludesTimeSeriesKeys(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{
		putEntry(1, "ts:metric:cpu:...", "data"),
		putEntry(2, "idx:metric:cpu", "data"),
		putEntry(3, "idx:tag:host:server1", "data"),
		putEntry(4, "retention:cpu", "data"),
		putEntry(5, "user:visible", "val"),
	})

	val, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)

	tsPrefixes := []string{"ts:", "idx:metric:", "idx:tag:", "retention:"}
	for _, k := range keys {
		for _, prefix := range tsPrefixes {
			if strings.HasPrefix(k, prefix) {
				t.Fatalf("internal TS key should be excluded: %q", k)
			}
		}
	}

	found := false
	for _, k := range keys {
		if k == "user:visible" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:visible to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.listKeys excludes TimeSeries internal keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ListKeysExcludesTimeSeriesKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("ts:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(2, []byte("idx:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(3, []byte("idx:tag:host:server1"), []byte("data"), 0),
		mvccPutEntry(4, []byte("retention:cpu"), []byte("data"), 0),
		mvccPutEntry(5, []byte("user:visible"), []byte("val"), 0),
	})

	result, err := s.Lookup(context.Background(), kv.ListQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := result.([]string)

	tsPrefixes := []string{"ts:", "idx:metric:", "idx:tag:", "retention:"}
	for _, k := range keys {
		for _, prefix := range tsPrefixes {
			if strings.HasPrefix(k, prefix) {
				t.Fatalf("internal TS key should be excluded: %q", k)
			}
		}
	}

	found := false
	for _, k := range keys {
		if k == "user:visible" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected user:visible to be present")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: MVCCStateMachine.lookupPrefix excludes internal keys
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupPrefixExcludesInternalKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:role:admin"), []byte("data"), 0),
		mvccPutEntry(2, []byte("ts:metric:cpu"), []byte("data"), 0),
		mvccPutEntry(3, []byte("app:key1"), []byte("val1"), 0),
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{Prefix: []byte("")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	m := result.(map[string][]byte)

	if _, ok := m["app:key1"]; !ok {
		t.Fatal("expected app:key1 to be present in prefix scan results")
	}
	if _, ok := m["__sys:role:admin"]; ok {
		t.Fatal("__sys:role:admin should be excluded from prefix scan results")
	}
	if _, ok := m["ts:metric:cpu"]; ok {
		t.Fatal("ts:metric:cpu should be excluded from prefix scan results")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: isInternalKey helper
// ---------------------------------------------------------------------------

func TestIsInternalKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "sys_prefix", key: "__sys:role:admin", want: true},
		{name: "ts_prefix", key: "ts:metric:cpu", want: true},
		{name: "idx_metric_prefix", key: "idx:metric:cpu", want: true},
		{name: "idx_tag_prefix", key: "idx:tag:host:server1", want: true},
		{name: "retention_prefix", key: "retention:cpu", want: true},
		{name: "user_key", key: "user:key", want: false},
		{name: "empty_key", key: "", want: false},
		{name: "regular_key", key: "regular-key", want: false},
		{name: "ts_without_colon", key: "tsdata", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInternalKey(tt.key)
			if got != tt.want {
				t.Fatalf("isInternalKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine SaveSnapshot empty DB count=0
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SaveSnapshotEmptyDBVerifyCount(t *testing.T) {
	s := openStore(t)

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	if buf.Len() < 4 {
		t.Fatalf("expected at least 4 bytes, got %d", buf.Len())
	}
	count := binary.LittleEndian.Uint32(buf.Bytes()[:4])
	if count != 0 {
		t.Fatalf("expected count 0 for empty DB snapshot, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: SnapshotExcludesMetadata for PebbleStateMachine
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SnapshotExcludesMetadata(t *testing.T) {
	s := openStore(t)

	applyEntries(t, s, []sm.Entry{putEntry(1, "userkey", "val")})

	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover on a second store.
	s2 := openStore(t)
	if err := s2.RecoverFromSnapshot(context.Background(), bytes.NewReader(buf.Bytes()), nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	val, err := s2.Lookup(context.Background(), []byte("userkey"))
	if err != nil {
		t.Fatalf("Lookup after recover: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected 'val', got %q", val)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: PebbleStateMachine listKeys all-0xFF prefix upper bound
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ListKeysAllFFPrefix(t *testing.T) {
	s := openStore(t)

	// Use a prefix that's all 0xFF - prefixUpperBound returns nil.
	val, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: string([]byte{0xFF, 0xFF})})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	keys := val.([]string)
	if len(keys) != 0 {
		t.Fatalf("expected empty keys, got %v", keys)
	}
}
