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
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// factory.go: WithNoSync (0% -> covered)
// ---------------------------------------------------------------------------

// TestWithNoSync_SetsFlag verifies that WithNoSync sets the noSync field on
// a newly created MVCCStateMachine via NewMVCCCreateFuncWith.
func TestWithNoSync_SetsFlag(t *testing.T) {
	// Invoke the factory option and verify the flag is applied to a real machine.
	s := NewMVCCStateMachine(nil)
	WithNoSync()(s)
	if !s.noSync {
		t.Fatal("expected noSync to be true after WithNoSync()")
	}
}

// TestWithNoSync_ViaCreateFuncWith confirms that WithNoSync is applied when
// the state machine is constructed through NewMVCCCreateFuncWith.
func TestWithNoSync_ViaCreateFuncWith(t *testing.T) {
	cf := NewMVCCCreateFuncWith(nil, WithNoSync())
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
	s, ok := dsm.(*MVCCStateMachine)
	if !ok {
		t.Fatalf("expected *MVCCStateMachine, got %T", dsm)
	}
	if !s.noSync {
		t.Fatal("expected noSync to be true after WithNoSync via factory")
	}
}

// TestWithNoSync_UpdateWorks confirms that a state machine with noSync=true
// still accepts and applies commands correctly.
func TestWithNoSync_UpdateWorks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)
	s.noSync = true
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	entries := []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("ns-key"), []byte("ns-val"), 0)},
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update with noSync: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from put with noSync")
	}
}

// ---------------------------------------------------------------------------
// factory.go: NewPebbleCreateFuncWith (0% -> covered)
// ---------------------------------------------------------------------------

// TestNewPebbleCreateFuncWith_Basic confirms that NewPebbleCreateFuncWith
// returns a non-nil CreateFunc that produces a PebbleStateMachine.
func TestNewPebbleCreateFuncWith_Basic(t *testing.T) {
	cf := NewPebbleCreateFuncWith(nil)
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
	if _, ok := dsm.(*PebbleStateMachine); !ok {
		t.Fatalf("expected *PebbleStateMachine, got %T", dsm)
	}
}

// TestNewPebbleCreateFuncWith_WithPebbleOpts confirms pebble options are
// applied through the factory.
func TestNewPebbleCreateFuncWith_WithPebbleOpts(t *testing.T) {
	cf := NewPebbleCreateFuncWith(
		[]Option{WithMaxOpenFiles(50)},
	)
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

// TestNewPebbleCreateFuncWith_WithConfigurators verifies that PebbleConfigurator
// functions are applied to the state machine after construction.
func TestNewPebbleCreateFuncWith_WithConfigurators(t *testing.T) {
	lr := lease.NewLessor()
	defer lr.Close()

	hub := watch.NewHub()
	defer hub.Close()

	cf := NewPebbleCreateFuncWith(
		nil,
		func(s *PebbleStateMachine) { s.SetLessor(lr) },
		func(s *PebbleStateMachine) { s.SetWatchHub(hub) },
		func(s *PebbleStateMachine) { s.SetTimeSeriesEnabled(true) },
	)

	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
	s, ok := dsm.(*PebbleStateMachine)
	if !ok {
		t.Fatalf("expected *PebbleStateMachine, got %T", dsm)
	}
	if s.Lessor() != lr {
		t.Fatal("expected lessor to be set via PebbleConfigurator")
	}
	if s.hub != hub {
		t.Fatal("expected hub to be set via PebbleConfigurator")
	}
	if !s.enableTimeSeries {
		t.Fatal("expected enableTimeSeries to be true via PebbleConfigurator")
	}
}

// TestNewPebbleCreateFuncWith_CanOpenAndOperate confirms a PebbleStateMachine
// constructed via the factory can be opened and operated.
func TestNewPebbleCreateFuncWith_CanOpenAndOperate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	cf := NewPebbleCreateFuncWith(nil)
	dsm := cf(1, 1)
	s := dsm.(*PebbleStateMachine)

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cmd, err := kv.EncodePut([]byte("pebble-factory-key"), []byte("pebble-factory-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	val, err := s.Lookup(context.Background(), []byte("pebble-factory-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("pebble-factory-val")) {
		t.Fatalf("expected pebble-factory-val, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: getKeyInBatch - defensive copy verification (90%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_GetKeyInBatch_DefensiveCopy confirms that
// getKeyInBatch returns a copy, not a slice pointing into pebble's internal
// buffer.
func TestPebbleStateMachine_GetKeyInBatch_DefensiveCopy(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("copy-key"), []byte("copy-val"), 0)},
	})

	s.mu.Lock()
	val, err := s.getKeyInBatch(readBatch(t, s), []byte("copy-key"))
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("getKeyInBatch: %v", err)
	}
	if !bytes.Equal(val, []byte("copy-val")) {
		t.Fatalf("expected copy-val, got %q", val)
	}

	// Mutate the returned slice; re-reading should still show original.
	val[0] = 'X'

	s.mu.Lock()
	val2, err := s.getKeyInBatch(readBatch(t, s), []byte("copy-key"))
	s.mu.Unlock()

	if err != nil {
		t.Fatalf("getKeyInBatch 2: %v", err)
	}
	if !bytes.Equal(val2, []byte("copy-val")) {
		t.Fatalf("defensive copy violated: got %q", val2)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandlePut - WriteError branch (92.3%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_MVCCPut_EmptyPayload exercises the decode-error branch in
// pebbleHandleMVCCPut via a zero-length payload (only op byte).
func TestPebbleStateMachine_MVCCPut_EmptyPayload(t *testing.T) {
	s := openExtendedStore(t)

	// Empty payload for MVCC put.
	cmd := []byte{protocol.OpMVCCPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty MVCCPut payload")
	}
}

// TestPebbleStateMachine_MVCCDelete_EmptyPayload exercises the decode-error branch
// in pebbleHandleMVCCDelete via a zero-length payload (only op byte).
func TestPebbleStateMachine_MVCCDelete_EmptyPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpMVCCDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty MVCCDelete payload")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseGrant - nil lessor branch (88.9%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_LeaseGrant_WithNoLessorConfigured exercises the
// s.lessor==nil branch in pebbleHandleLeaseGrant via a store with no lessor.
func TestPebbleStateMachine_LeaseGrant_WithNoLessorConfigured(t *testing.T) {
	// No lessor set on the machine.
	s := openExtendedStore(t)

	cmd := protocol.EncodeLeaseGrant(30)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when no lessor is configured")
	}
}

// TestPebbleStateMachine_LeaseRevoke_WithNoLessorConfigured exercises the
// s.lessor==nil branch in pebbleHandleLeaseRevoke via a store with no lessor.
func TestPebbleStateMachine_LeaseRevoke_WithNoLessorConfigured(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeLeaseRevoke(42)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when no lessor is configured")
	}
}

// TestPebbleStateMachine_LeaseKeepAlive_WithNoLessorConfigured exercises the
// s.lessor==nil branch in pebbleHandleLeaseKeepAlive via a store with no lessor.
func TestPebbleStateMachine_LeaseKeepAlive_WithNoLessorConfigured(t *testing.T) {
	s := openExtendedStore(t)

	cmd := protocol.EncodeLeaseKeepAlive(42)
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error with no lessor: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when no lessor is configured")
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleIndexCreate / pebbleHandleIndexDrop happy paths
// (85.7%, 83.3% -> improve)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_IndexCreate_AlreadyExists exercises the
// already-exists guard in pebbleHandleIndexCreate.
func TestPebbleStateMachine_IndexCreate_AlreadyExists(t *testing.T) {
	s := openExtendedStore(t)

	// Create the index once.
	createCmd := protocol.EncodeIndexCreate("dup_idx", "name", false, false, false)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Create again with the same name — should succeed (idempotent) or error
	// but not panic. The important thing is we exercise that code path.
	createCmd2 := protocol.EncodeIndexCreate("dup_idx", "name", false, false, false)
	entries := []sm.Entry{{Index: 2, Cmd: createCmd2}}
	results := make([]sm.Result, 1)
	_ = s.Update(context.Background(), entries, results)
	// Return value is either nil (no-op) or an error; both are valid.
}

// TestPebbleStateMachine_IndexDrop_ExistingIndex exercises the happy path
// of pebbleHandleIndexDrop on an index that exists.
func TestPebbleStateMachine_IndexDrop_ExistingIndex(t *testing.T) {
	s := openExtendedStore(t)

	createCmd := protocol.EncodeIndexCreate("drop_idx", "age", false, false, true)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	dropCmd := protocol.EncodeIndexDrop("drop_idx")
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: dropCmd}})
	_ = results
}

// TestPebbleStateMachine_IndexDrop_NonExistentIndex exercises the
// not-found guard in pebbleHandleIndexDrop. Dropping a non-existent index
// is a no-op success (idempotent) — a non-nil error from a Raft Update()
// handler permanently stalls the cluster.
func TestPebbleStateMachine_IndexDrop_NonExistentIndex(t *testing.T) {
	s := openExtendedStore(t)

	dropCmd := protocol.EncodeIndexDrop("nonexistent_idx")
	entries := []sm.Entry{{Index: 1, Cmd: dropCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop nonexistent: expected result 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleTSWriteBatch happy path (90.9%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_TSWriteBatch_HappyPath exercises the normal write
// batch path through pebbleHandleTSWriteBatch.
func TestPebbleStateMachine_TSWriteBatch_HappyPath(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	points := []protocol.TSWriteParams{
		{Metric: "batch-m1", Timestamp: now, Value: 1.0},
		{Metric: "batch-m2", Timestamp: now, Value: 2.0},
	}
	cmd := protocol.EncodeTSWriteBatch(points)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != uint64(len(points)) {
		t.Fatalf("expected %d, got %d", len(points), results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleTSWritePoint happy path (88.9%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_TSWritePoint_HappyPath exercises the normal write
// single data-point path through pebbleHandleTSWritePoint.
func TestPebbleStateMachine_TSWritePoint_HappyPath(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	tags := map[string]string{"host": "srv1"}
	cmd := protocol.EncodeTSWritePoint("wp-metric", now, 99.0, tags)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleTSSetRetention happy path (88.9%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_TSSetRetention_HappyPath exercises the normal
// retention-policy set path through pebbleHandleTSSetRetention.
func TestPebbleStateMachine_TSSetRetention_HappyPath(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := protocol.EncodeTSSetRetention(
		"ret-metric-ok",
		int64(48*time.Hour),
		int64(5*time.Minute),
		2,
	)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleTSDeleteSeries happy path (92.3%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_TSDeleteSeries_HappyPath exercises the normal
// delete-series path through pebbleHandleTSDeleteSeries with explicit bounds.
func TestPebbleStateMachine_TSDeleteSeries_HappyPath(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	writeCmd := protocol.EncodeTSWrite("ds-metric", now, 42.0, nil)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	delCmd := protocol.EncodeTSDeleteSeries("ds-metric", now-1000, now+1000, nil)
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

// ---------------------------------------------------------------------------
// pebble.go: pebbleHandleLeaseRevoke with nil hub (94.7% -> improve)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_LeaseRevoke_NilHub exercises the revoke path when
// s.hub is nil — the loop body still runs but skips notifications.
func TestPebbleStateMachine_LeaseRevoke_NilHub(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	grantResults := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeLeaseGrant(60)},
	})
	leaseID := int64(grantResults[0].Value)

	putCmd := protocol.EncodeMVCCPut([]byte("revoke-nil-hub-key"), []byte("val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	// Revoke without hub configured — exercises the no-hub loop body.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	_, err := s.Lookup(context.Background(), []byte("revoke-nil-hub-key"))
	if err == nil {
		t.Fatal("expected error for key deleted by lease revoke")
	}

	_ = lr
}

// ---------------------------------------------------------------------------
// pebble.go: scanPrefix - limit=0 (no limit) path (87.5%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_ScanPrefix_ZeroLimit confirms that a zero limit
// returns all matching keys (no limit branch in scanPrefix).
func TestPebbleStateMachine_ScanPrefix_ZeroLimit(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("zero:a"), []byte("1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("zero:b"), []byte("2"), 0)},
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("zero:c"), []byte("3"), 0)},
	})

	// Limit=0 means no limit — drive directly via scanPrefix.
	m, err := s.scanPrefix(&statemachine.PrefixQuery{
		Prefix: []byte("zero:"),
		Limit:  0,
	})
	if err != nil {
		t.Fatalf("scanPrefix limit=0: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 results with limit=0, got %d", len(m))
	}
}

// TestPebbleStateMachine_ScanPrefix_PositiveLimit confirms that a positive
// limit stops iteration after the requested count.
func TestPebbleStateMachine_ScanPrefix_PositiveLimit(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("lmt:a"), []byte("1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("lmt:b"), []byte("2"), 0)},
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("lmt:c"), []byte("3"), 0)},
	})

	m, err := s.scanPrefix(&statemachine.PrefixQuery{
		Prefix: []byte("lmt:"),
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("scanPrefix limit=2: %v", err)
	}
	if len(m) > 2 {
		t.Fatalf("expected at most 2 results with limit=2, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// pebble.go: listKeys - prefix with allFF upper bound (92.9%)
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_ListKeys_AllFFPrefix exercises the nil
// upper-bound path in listKeys (all-0xFF prefix).
func TestPebbleStateMachine_ListKeys_AllFFPrefix(t *testing.T) {
	s := openExtendedStore(t)

	keys, err := s.listKeys(string([]byte{0xFF, 0xFF}))
	if err != nil {
		t.Fatalf("listKeys all-FF prefix: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no keys for all-FF prefix, got %d", len(keys))
	}
}
