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

package store

import (
	"bytes"
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Extended PebbleStateMachine test helpers
// ---------------------------------------------------------------------------

// openExtendedStore creates a PebbleStateMachine with optional components, opens it,
// and registers cleanup.
func openExtendedStore(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("Open: expected last applied 0 on fresh DB, got %d", idx)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("Close: unexpected error: %v", err)
		}
	})
	return s
}

// readBatch returns an indexed batch usable as the reader argument of the
// apply-path helpers, which take the batch they are writing into so they read
// their own writes. A test that only wants to read committed state passes an
// empty one.
func readBatch(t *testing.T, s *PebbleStateMachine) *pebble.Batch {
	t.Helper()
	b := s.db.NewIndexedBatch()
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// openStoreWithLessor creates a PebbleStateMachine with a Lessor attached.
func openStoreWithLessor(t *testing.T) (*PebbleStateMachine, *lease.Lessor) {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})
	return s, lr
}

// openStoreWithHub creates a PebbleStateMachine with a watch Hub attached.
func openStoreWithHub(t *testing.T) (*PebbleStateMachine, *watch.Hub) {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})
	return s, hub
}

// openStoreWithTS creates a PebbleStateMachine with a TimeSeries storage attached.
func openStoreWithTS(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create a separate PebbleDB for time-series storage.
	tsDir := t.TempDir()
	tsDB, err := pebble.Open(tsDir, nil)
	if err != nil {
		t.Fatalf("open ts pebble: %v", err)
	}
	t.Cleanup(func() { _ = tsDB.Close() })

	ts, err := timeseries.New(tsDB)
	if err != nil {
		t.Fatalf("create ts storage: %v", err)
	}
	s.SetTimeSeries(ts)

	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// applyExtEntries applies entries to the extended store and returns results.
func applyExtEntries(t *testing.T, s *PebbleStateMachine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	return results
}

// ---------------------------------------------------------------------------
// Tests: Backward compatibility (base KV ops still work)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ExtendedBackwardCompat_PutGetDelete(t *testing.T) {
	s := openExtendedStore(t)

	// Put via base KV protocol.
	putCmd, err := kv.EncodePut([]byte("key1"), []byte("val1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: putCmd},
	}
	results := applyExtEntries(t, s, entries)
	if results[0].Value != uint64(len("val1")) {
		t.Fatalf("expected result %d, got %d", len("val1"), results[0].Value)
	}

	// Lookup via []byte query.
	val, err := s.Lookup(context.Background(), []byte("key1"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val1")) {
		t.Fatalf("expected val1, got %s", val)
	}

	// Delete via base KV protocol.
	delCmd, err := kv.EncodeDelete([]byte("key1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delEntries := []sm.Entry{
		{Index: 2, Cmd: delCmd},
	}
	delResults := applyExtEntries(t, s, delEntries)
	if delResults[0].Value != 1 {
		t.Fatalf("expected delete result 1, got %d", delResults[0].Value)
	}

	// Verify key is gone.
	_, err = s.Lookup(context.Background(), []byte("key1"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_ExtendedBackwardCompat_ListQuery(t *testing.T) {
	s := openExtendedStore(t)

	cmd1, err := kv.EncodePut([]byte("prefix:a"), []byte("1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	cmd2, err := kv.EncodePut([]byte("prefix:b"), []byte("2"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	cmd3, err := kv.EncodePut([]byte("other:c"), []byte("3"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: cmd1},
		{Index: 2, Cmd: cmd2},
		{Index: 3, Cmd: cmd3},
	}
	applyExtEntries(t, s, entries)

	result, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: "prefix:"})
	if err != nil {
		t.Fatalf("Lookup ListQuery: %v", err)
	}
	keys := result.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0] != "prefix:a" || keys[1] != "prefix:b" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Tests: MVCC-compatible put/delete (no revision tracking)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCPut(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeMVCCPut([]byte("mvcc-key"), []byte("mvcc-val"), 0)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := applyExtEntries(t, s, entries)

	if results[0].Value != uint64(len("mvcc-val")) {
		t.Fatalf("expected %d, got %d", len("mvcc-val"), results[0].Value)
	}

	// Verify the value is stored.
	val, err := s.Lookup(context.Background(), []byte("mvcc-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("mvcc-val")) {
		t.Fatalf("expected mvcc-val, got %s", val)
	}
}

func TestPebbleStateMachine_MVCCPut_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	// Create a command with OpMVCCPut but empty payload.
	cmd := []byte{protocol.OpMVCCPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC Put payload")
	}
}

func TestPebbleStateMachine_MVCCDelete(t *testing.T) {
	s := openExtendedStore(t)

	// First put a value.
	putCmd := protocol.EncodeMVCCPut([]byte("del-key"), []byte("del-val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Then delete it.
	delCmd := protocol.EncodeMVCCDelete([]byte("del-key"))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify key is gone.
	_, err := s.Lookup(context.Background(), []byte("del-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestPebbleStateMachine_MVCCDelete_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpMVCCDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC Delete payload")
	}
}

// ---------------------------------------------------------------------------
// Tests: Batch operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Batch(t *testing.T) {
	s := openExtendedStore(t)

	ops := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("b1"), []byte("v1"), nil, 0),
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("b2"), []byte("v2"), nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 2 {
		t.Fatalf("expected batch count 2, got %d", results[0].Value)
	}

	// Verify both keys exist.
	for _, key := range []string{"b1", "b2"} {
		_, err := s.Lookup(context.Background(), []byte(key))
		if err != nil {
			t.Fatalf("Lookup %s: %v", key, err)
		}
	}
}

func TestPebbleStateMachine_Batch_WithDelete(t *testing.T) {
	s := openExtendedStore(t)

	// First put a value.
	putCmd := protocol.EncodeMVCCPut([]byte("bd-key"), []byte("bd-val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Batch delete + put.
	ops := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCDelete, []byte("bd-key"), nil, nil, 0),
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("bd-new"), []byte("new-val"), nil, 0),
	}
	cmd := protocol.EncodeBatch(ops)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Old key should be gone.
	_, err := s.Lookup(context.Background(), []byte("bd-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}

	// New key should exist.
	val, err := s.Lookup(context.Background(), []byte("bd-new"))
	if err != nil {
		t.Fatalf("Lookup bd-new: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("new-val")) {
		t.Fatalf("expected new-val, got %s", val)
	}
}

func TestPebbleStateMachine_Batch_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpBatch}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid batch payload")
	}
}

// ---------------------------------------------------------------------------
// Tests: Transaction (compare-and-swap)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_SuccessPath(t *testing.T) {
	s := openExtendedStore(t)

	// Put initial value.
	putCmd := protocol.EncodeMVCCPut([]byte("txn-key"), []byte("initial"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Transaction: compare value == "initial", then set to "updated".
	compare := protocol.EncodeCompare(
		[]byte("txn-key"),
		3, // CompareValue
		0, // CompareEqual
		[]byte("initial"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("updated"), nil, 0)
	failureOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("failed"), nil, 0)

	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, [][]byte{failureOp})
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Check succeeded flag.
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed")
	}

	// Verify value was updated.
	val, err := s.Lookup(context.Background(), []byte("txn-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("updated")) {
		t.Fatalf("expected updated, got %s", val)
	}
}

func TestPebbleStateMachine_Transaction_FailurePath(t *testing.T) {
	s := openExtendedStore(t)

	// Put initial value.
	putCmd := protocol.EncodeMVCCPut([]byte("txn-key"), []byte("initial"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Transaction: compare value == "wrong", then set to "updated" (should fail).
	compare := protocol.EncodeCompare(
		[]byte("txn-key"),
		3, // CompareValue
		0, // CompareEqual
		[]byte("wrong"),
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("updated"), nil, 0)
	failureOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("failed"), nil, 0)

	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, [][]byte{failureOp})
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Check succeeded flag is 0.
	if len(results[0].Data) > 0 && results[0].Data[0] != 0 {
		t.Fatal("expected transaction to fail")
	}

	// Verify failure ops ran.
	val, err := s.Lookup(context.Background(), []byte("txn-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("failed")) {
		t.Fatalf("expected failed, got %s", val)
	}
}

func TestPebbleStateMachine_Transaction_VersionCompare(t *testing.T) {
	s := openExtendedStore(t)

	// Put a key so it exists (version = 1 in non-MVCC mode).
	putCmd := protocol.EncodeMVCCPut([]byte("ver-key"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Compare version == 1 (key exists -> version = 1).
	compare := protocol.EncodeCompare(
		[]byte("ver-key"),
		0, // CompareVersion
		0, // CompareEqual
		nil,
		1, 0, 0, // version=1
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("ver-key"), []byte("success"), nil, 0)

	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected version compare to succeed")
	}
}

func TestPebbleStateMachine_Transaction_NonexistentKey(t *testing.T) {
	s := openExtendedStore(t)

	// Compare value of nonexistent key == empty (should succeed).
	compare := protocol.EncodeCompare(
		[]byte("missing"),
		3, // CompareValue
		0, // CompareEqual
		[]byte{},
		0, 0, 0,
	)
	successOp := protocol.EncodeOp(protocol.OpMVCCPut, []byte("missing"), []byte("created"), nil, 0)

	cmd := protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed for missing key with empty value compare")
	}

	val, err := s.Lookup(context.Background(), []byte("missing"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("created")) {
		t.Fatalf("expected created, got %s", val)
	}
}

func TestPebbleStateMachine_Transaction_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpTransaction}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid transaction payload")
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LeaseGrant(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	cmd := protocol.EncodeLeaseGrant(10)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	leaseID := int64(results[0].Value)
	if leaseID <= 0 {
		t.Fatalf("expected positive lease ID, got %d", leaseID)
	}

	// Verify lease exists in lessor.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist")
	}
	if l.TTL != 10 {
		t.Fatalf("expected TTL 10, got %d", l.TTL)
	}
}

func TestPebbleStateMachine_LeaseGrant_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor configured

	cmd := protocol.EncodeLeaseGrant(10)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

func TestPebbleStateMachine_LeaseRevoke(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Put a key with the lease attached.
	putCmd := protocol.EncodeMVCCPut([]byte("lease-key"), []byte("lease-val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	// Revoke the lease.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})

	// Should have revoked 1 attached key.
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	// Verify key is deleted.
	_, err := s.Lookup(context.Background(), []byte("lease-key"))
	if err == nil {
		t.Fatal("expected error for deleted key after lease revoke")
	}

	// Verify lease is gone.
	if lr.Lookup(leaseID) != nil {
		t.Fatal("expected lease to be revoked")
	}
}

func TestPebbleStateMachine_LeaseKeepAlive(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(5)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Keep alive.
	kaCmd := protocol.EncodeLeaseKeepAlive(leaseID)
	kaResults := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: kaCmd}})

	if int64(kaResults[0].Value) != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, kaResults[0].Value)
	}

	// Verify lease still exists.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist after keep-alive")
	}
}

func TestPebbleStateMachine_LeaseKeepAlive_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	cmd := protocol.EncodeLeaseKeepAlive(99)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

func TestPebbleStateMachine_LeaseRevoke_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	cmd := protocol.EncodeLeaseRevoke(1)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease Lookup queries
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_LeaseTimeToLive(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(30)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Query the lease.
	result, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("Lookup LeaseTimeToLiveQuery: %v", err)
	}
	l := result.(*lease.Lease)
	if l.ID != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, l.ID)
	}
	if l.TTL != 30 {
		t.Fatalf("expected TTL 30, got %d", l.TTL)
	}
}

func TestPebbleStateMachine_Lookup_LeaseTimeToLive_NotFound(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	_, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 999})
	if err == nil {
		t.Fatal("expected error for nonexistent lease")
	}
}

func TestPebbleStateMachine_Lookup_LeaseTimeToLive_NoLessor(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestPebbleStateMachine_Lookup_Leases(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	// Grant two leases.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeLeaseGrant(10)},
		{Index: 2, Cmd: protocol.EncodeLeaseGrant(20)},
	})

	result, err := s.Lookup(context.Background(), lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup LeasesQuery: %v", err)
	}
	leases := result.([]*lease.Lease)
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(leases))
	}
}

func TestPebbleStateMachine_Lookup_Leases_NoLessor(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), lease.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

// ---------------------------------------------------------------------------
// Tests: Index operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_IndexCreate(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeIndexCreate("test_idx", "name", true, false, false)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify index exists via Lookup.
	result, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := result.([]string)
	if len(names) != 1 || names[0] != "test_idx" {
		t.Fatalf("expected [test_idx], got %v", names)
	}
}

func TestPebbleStateMachine_IndexCreate_Duplicate(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeIndexCreate("dup_idx", "field", false, false, false)
	firstResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if firstResults[0].Value != 1 {
		t.Fatalf("first create: expected result 1, got %d", firstResults[0].Value)
	}

	// Creating the same index again is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so duplicate-create must succeed without error.
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("duplicate create: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("duplicate create: expected result 1, got %d", results[0].Value)
	}

	// Only one index entry should exist.
	listResult, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 1 {
		t.Fatalf("expected 1 index after duplicate create, got %d", len(names))
	}
	if names[0] != "dup_idx" {
		t.Fatalf("expected index name dup_idx, got %s", names[0])
	}
}

func TestPebbleStateMachine_IndexDrop(t *testing.T) {
	s := openExtendedStore(t)

	// Create then drop.
	createCmd := protocol.EncodeIndexCreate("drop_idx", "field", false, false, false)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	dropCmd := protocol.EncodeIndexDrop("drop_idx")
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: dropCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify index is gone.
	result, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := result.([]string)
	if len(names) != 0 {
		t.Fatalf("expected no indexes, got %v", names)
	}
}

func TestPebbleStateMachine_IndexDrop_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	// Dropping a non-existent index is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so drop-not-found must succeed without error.
	cmd := protocol.EncodeIndexDrop("nonexistent")
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop nonexistent: expected result 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_Lookup_IndexQuery(t *testing.T) {
	s := openExtendedStore(t)

	// Create an index.
	createCmd := protocol.EncodeIndexCreate("q_idx", "field", false, false, false)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Manually add entries to the index via the PebbleDB-backed index.
	idx := s.indexes["q_idx"]
	if err := idx.Put([]byte("42"), []byte("hello")); err != nil {
		t.Fatalf("index Put: %v", err)
	}

	// Query via Lookup.
	result, err := s.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "q_idx",
		Value:     []byte("hello"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexQueryRequest: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) != 1 || string(keys[0]) != "42" {
		t.Fatalf("expected [42], got %v", keys)
	}
}

func TestPebbleStateMachine_Lookup_IndexQuery_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "missing_idx",
		Value:     []byte("test"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

func TestPebbleStateMachine_Lookup_IndexScan_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "missing_idx",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_TSWrite(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := protocol.EncodeTSWrite("cpu.usage", ts, 42.5, map[string]string{"host": "node1"})
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWrite_NoStorage(t *testing.T) {
	s := openExtendedStore(t) // no TS configured

	ts := time.Now().UnixNano()
	cmd := protocol.EncodeTSWrite("cpu.usage", ts, 42.5, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSWriteBatch(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	params := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: now, Value: 42.5, Tags: map[string]string{"host": "node1"}},
		{Metric: "mem.usage", Timestamp: now, Value: 85.0, Tags: map[string]string{"host": "node1"}},
	}
	cmd := protocol.EncodeTSWriteBatch(params)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 2 {
		t.Fatalf("expected 2, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWriteBatch_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	params := []protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: time.Now().UnixNano(), Value: 1.0, Tags: nil},
	}
	cmd := protocol.EncodeTSWriteBatch(params)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSWritePoint(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := protocol.EncodeTSWritePoint("cpu.usage", ts, 42.5, map[string]string{"host": "node1"})
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWritePoint_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeTSWritePoint("cpu", time.Now().UnixNano(), 1.0, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSDeleteSeries(t *testing.T) {
	s := openStoreWithTS(t)

	// Write a point first.
	now := time.Now().UnixNano()
	writeCmd := protocol.EncodeTSWrite("del.metric", now, 1.0, nil)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	// Delete the series.
	delCmd := protocol.EncodeTSDeleteSeries("del.metric", now-1000, now+1000, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSDeleteSeries_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeTSDeleteSeries("m", 0, 0, nil)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSSetRetention(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := protocol.EncodeTSSetRetention("cpu.usage", int64(24*time.Hour), int64(time.Minute), 1)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSSetRetention_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeTSSetRetention("cpu", int64(time.Hour), 0, 0)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series Lookup queries
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_TSQuery(t *testing.T) {
	s := openStoreWithTS(t)

	// Write a data point.
	now := time.Now()
	writeCmd := protocol.EncodeTSWrite("qry.metric", now.UnixNano(), 42.5, nil)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	// Query the data.
	result, err := s.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "qry.metric",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: %v", err)
	}
	points := result.([]timeseries.DataPoint)
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if math.Abs(points[0].Value-42.5) > 0.001 {
		t.Fatalf("expected value 42.5, got %f", points[0].Value)
	}
}

func TestPebbleStateMachine_Lookup_TSQuery_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "m",
		Start:  time.Now(),
		End:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSAggregate_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric:   "m",
		Start:    time.Now(),
		End:      time.Now(),
		Func:     1,
		Interval: time.Second,
	})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSGetRetention_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "m"})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSListMetrics_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "", Limit: 10})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSRichQuery_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSRichQueryRequest{})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSListMetrics(t *testing.T) {
	s := openStoreWithTS(t)

	// Write data to create metric entries.
	now := time.Now().UnixNano()
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeTSWrite("cpu.usage", now, 1.0, nil)},
		{Index: 2, Cmd: protocol.EncodeTSWrite("mem.usage", now, 2.0, nil)},
	})

	result, err := s.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "", Limit: 100})
	if err != nil {
		t.Fatalf("Lookup TSListMetricsRequest: %v", err)
	}
	metrics := result.([]string)
	if len(metrics) < 2 {
		t.Fatalf("expected at least 2 metrics, got %d: %v", len(metrics), metrics)
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch hub integration
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_WatchPut(t *testing.T) {
	s, hub := openStoreWithHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "watch-key")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Put a value.
	putCmd, err := kv.EncodePut([]byte("watch-key"), []byte("watch-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Read the event.
	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
		if !bytes.Equal(resp.Events[0].Kv.Key, []byte("watch-key")) {
			t.Fatalf("unexpected key: %s", resp.Events[0].Kv.Key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestPebbleStateMachine_WatchDelete(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put initial value.
	putCmd, err := kv.EncodePut([]byte("wd-key"), []byte("wd-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "wd-key")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Delete the key.
	delCmd, err := kv.EncodeDelete([]byte("wd-key"))
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
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestPebbleStateMachine_WatchMVCCPut(t *testing.T) {
	s, hub := openStoreWithHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "mvcc-watch")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	cmd := protocol.EncodeMVCCPut([]byte("mvcc-watch"), []byte("val"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

// ---------------------------------------------------------------------------
// Tests: Unknown opcode
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UnknownOp(t *testing.T) {
	s := openExtendedStore(t)

	// Use an opcode that is not registered.
	cmd := []byte{200, 0, 0}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for unknown opcode")
	}
	var unknownErr *protocol.UnknownOpError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("expected UnknownOpError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_EmptyCommand(t *testing.T) {
	s := openExtendedStore(t)

	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// ---------------------------------------------------------------------------
// Tests: Setter methods
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SetTimeSeries(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.TimeSeries() != nil {
		t.Fatal("expected nil TimeSeries")
	}

	tsDB, err := pebble.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = tsDB.Close() }()

	ts, err := timeseries.New(tsDB)
	if err != nil {
		t.Fatalf("ts new: %v", err)
	}
	s.SetTimeSeries(ts)
	if s.TimeSeries() != ts {
		t.Fatal("expected TimeSeries to be set")
	}
}

func TestPebbleStateMachine_SetLessor(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil Lessor")
	}

	lr := lease.NewLessor()
	s.SetLessor(lr)
	if s.Lessor() != lr {
		t.Fatal("expected Lessor to be set")
	}
}

func TestPebbleStateMachine_DB(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.DB() != nil {
		t.Fatal("expected nil DB before open")
	}

	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	if s.DB() == nil {
		t.Fatal("expected non-nil DB after open")
	}
}

// ---------------------------------------------------------------------------
// Tests: Lookup unsupported query type
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_UnsupportedType(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Not-open errors
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Update_NotOpen(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	putCmd, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: putCmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected NotOpenError")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected NotOpenError, got %T: %v", err, err)
	}
}
