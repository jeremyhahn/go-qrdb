//go:build !integration

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
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Test helpers for RegularStateMachine subsystem tests
// ---------------------------------------------------------------------------

func newRegularWithLease(lm LeaseManager) *RegularStateMachine {
	return NewRegularStateMachine(WithRegularLeaseManager(lm))
}

func newRegularWithIndex(im IndexManager) *RegularStateMachine {
	return NewRegularStateMachine(WithRegularIndexManager(im))
}

func newRegularWithTS(ts TimeSeriesStore) *RegularStateMachine {
	return NewRegularStateMachine(WithRegularTimeSeriesStore(ts))
}

// ---------------------------------------------------------------------------
// Tests: WithRegular* functional options
// ---------------------------------------------------------------------------

func TestRegularStateMachine_WithRegularLogger(t *testing.T) {
	logger := slog.Default()
	rsm := NewRegularStateMachine(WithRegularLogger(logger))
	if rsm.logger != logger {
		t.Fatal("expected logger to be set via WithRegularLogger")
	}
}

func TestRegularStateMachine_WithRegularLogger_Custom(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	rsm := NewRegularStateMachine(WithRegularLogger(logger))
	if rsm.logger != logger {
		t.Fatal("expected custom logger to be set")
	}
}

func TestRegularStateMachine_WithRegularIndexManager(t *testing.T) {
	im := &mockIndexManager{}
	rsm := NewRegularStateMachine(WithRegularIndexManager(im))
	if rsm.indexManager == nil {
		t.Fatal("expected indexManager to be set")
	}
}

func TestRegularStateMachine_WithRegularIndexManager_Nil(t *testing.T) {
	rsm := NewRegularStateMachine()
	if rsm.indexManager != nil {
		t.Fatal("expected indexManager to be nil by default")
	}
}

func TestRegularStateMachine_WithRegularLeaseManager(t *testing.T) {
	lm := &mockLeaseManager{}
	rsm := NewRegularStateMachine(WithRegularLeaseManager(lm))
	if rsm.leaseManager == nil {
		t.Fatal("expected leaseManager to be set")
	}
}

func TestRegularStateMachine_WithRegularLeaseManager_Nil(t *testing.T) {
	rsm := NewRegularStateMachine()
	if rsm.leaseManager != nil {
		t.Fatal("expected leaseManager to be nil by default")
	}
}

func TestRegularStateMachine_WithRegularTimeSeriesStore(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := NewRegularStateMachine(WithRegularTimeSeriesStore(ts))
	if rsm.tsStore == nil {
		t.Fatal("expected tsStore to be set")
	}
}

func TestRegularStateMachine_WithRegularTimeSeriesStore_Nil(t *testing.T) {
	rsm := NewRegularStateMachine()
	if rsm.tsStore != nil {
		t.Fatal("expected tsStore to be nil by default")
	}
}

func TestRegularStateMachine_WithRegularMVCCStore(t *testing.T) {
	ms := &mockMVCCStore{}
	rsm := NewRegularStateMachine(WithRegularMVCCStore(ms))
	if rsm.mvccStore == nil {
		t.Fatal("expected mvccStore to be set")
	}
}

func TestRegularStateMachine_WithRegularMVCCStore_Nil(t *testing.T) {
	rsm := NewRegularStateMachine()
	if rsm.mvccStore != nil {
		t.Fatal("expected mvccStore to be nil by default")
	}
}

// ---------------------------------------------------------------------------
// Tests: MVCC write handlers - success and error paths
// ---------------------------------------------------------------------------

func TestRegularStateMachine_MVCCPut_Success(t *testing.T) {
	ms := &mockMVCCStore{putResult: "ok"}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 42))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.putCalls) != 1 {
		t.Fatalf("expected 1 put call, got %d", len(ms.putCalls))
	}
	if !bytes.Equal(ms.putCalls[0].Key, []byte("key")) {
		t.Fatalf("expected key 'key', got %q", ms.putCalls[0].Key)
	}
}

func TestRegularStateMachine_MVCCPut_StoreError(t *testing.T) {
	ms := &mockMVCCStore{putErr: errors.New("disk full")}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 0))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from store")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestRegularStateMachine_MVCCDelete_Success(t *testing.T) {
	ms := &mockMVCCStore{deleteResult: "ok"}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("key")))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.deleteCalls) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(ms.deleteCalls))
	}
}

func TestRegularStateMachine_MVCCDelete_StoreError(t *testing.T) {
	ms := &mockMVCCStore{deleteErr: errors.New("io error")}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("key")))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from store")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T", err)
	}
}

func TestRegularStateMachine_MVCCRange_Success(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "range-data", rangeRevision: 5}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("a"), []byte("z"), 0, 100, false, false))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_MVCCRange_StoreError(t *testing.T) {
	ms := &mockMVCCStore{rangeErr: errors.New("range fail")}
	rsm := newRegularWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("a"), []byte("z"), 0, 100, false, false))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from store")
	}
}

func TestRegularStateMachine_Transaction_Success(t *testing.T) {
	ms := &mockMVCCStore{txnSucceeded: true, txnRevision: 7}
	rsm := newRegularWithMVCC(ms)

	compareEnc := mustEncode(protocol.EncodeCompare([]byte("key"), 1, 1, []byte("val"), 0, 0, 0))
	successOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("key"), []byte("new"), nil, 0))
	failureOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("key"), []byte("old"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compareEnc}, [][]byte{successOpEnc}, [][]byte{failureOpEnc}))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value != 7 {
		t.Fatalf("expected revision 7, got %d", result.Value)
	}
	if len(result.Data) != 1 || result.Data[0] != 1 {
		t.Fatal("expected success flag in Data")
	}
}

func TestRegularStateMachine_Transaction_FailedBranch(t *testing.T) {
	ms := &mockMVCCStore{txnSucceeded: false, txnRevision: 3}
	rsm := newRegularWithMVCC(ms)

	compareEnc := mustEncode(protocol.EncodeCompare([]byte("key"), 1, 1, []byte("val"), 0, 0, 0))
	successOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("key"), []byte("new"), nil, 0))
	failureOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("key"), []byte("old"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compareEnc}, [][]byte{successOpEnc}, [][]byte{failureOpEnc}))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Data) != 1 || result.Data[0] != 0 {
		t.Fatal("expected failure flag in Data")
	}
}

func TestRegularStateMachine_Transaction_StoreError(t *testing.T) {
	ms := &mockMVCCStore{txnErr: errors.New("txn fail")}
	rsm := newRegularWithMVCC(ms)

	compareEnc := mustEncode(protocol.EncodeCompare([]byte("k"), 1, 1, []byte("v"), 0, 0, 0))
	successOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("v2"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compareEnc}, [][]byte{successOpEnc}, nil))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from store")
	}
}

func TestRegularStateMachine_Transaction_CompareFields(t *testing.T) {
	ms := &mockMVCCStore{txnSucceeded: true, txnRevision: 1}
	rsm := newRegularWithMVCC(ms)

	compareEnc := mustEncode(protocol.EncodeCompare([]byte("key"), 2, 3, nil, 10, 20, 30))
	successOpEnc := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("key"), []byte("val"), []byte("end"), 55))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compareEnc}, [][]byte{successOpEnc}, nil))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.txnCalls) != 1 {
		t.Fatalf("expected 1 txn call, got %d", len(ms.txnCalls))
	}
	cmp := ms.txnCalls[0].Compares[0]
	if cmp.Version != 10 || cmp.CreateRevision != 20 || cmp.ModRevision != 30 {
		t.Fatalf("compare revision fields mismatch: %+v", cmp)
	}
	op := ms.txnCalls[0].SuccessOps[0]
	if op.LeaseID != 55 {
		t.Fatalf("expected lease ID 55, got %d", op.LeaseID)
	}
	if !bytes.Equal(op.RangeEnd, []byte("end")) {
		t.Fatalf("expected range end 'end', got %q", op.RangeEnd)
	}
}

// ---------------------------------------------------------------------------
// Tests: MVCC lookup handlers
// ---------------------------------------------------------------------------

func TestRegularStateMachine_MVCCLookup_GetQuery_Success(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "found"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), MVCCGetQuery{Key: []byte("key")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "found" {
		t.Fatalf("expected 'found', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_GetQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "found-ptr"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), &MVCCGetQuery{Key: []byte("key")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "found-ptr" {
		t.Fatalf("expected 'found-ptr', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_GetAtRevision_Value(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "rev-data"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "rev-data" {
		t.Fatalf("expected 'rev-data', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_GetAtRevision_Pointer(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "rev-ptr"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), &MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "rev-ptr" {
		t.Fatalf("expected 'rev-ptr', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_RangeQuery_Value(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "range-data"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), MVCCRangeQuery{Start: []byte("a"), End: []byte("z"), Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "range-data" {
		t.Fatalf("expected 'range-data', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_RangeQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{rangeResult: "range-ptr"}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), &MVCCRangeQuery{Start: []byte("a"), End: []byte("z")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "range-ptr" {
		t.Fatalf("expected 'range-ptr', got %v", result)
	}
}

func TestRegularStateMachine_MVCCLookup_RevisionQuery_Value(t *testing.T) {
	ms := &mockMVCCStore{rangeRevision: 42}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rev, ok := result.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T", result)
	}
	if rev != 42 {
		t.Fatalf("expected revision 42, got %d", rev)
	}
}

func TestRegularStateMachine_MVCCLookup_RevisionQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{rangeRevision: 99}
	rsm := newRegularWithMVCC(ms)

	result, err := rsm.Lookup(context.Background(), &MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rev, ok := result.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T", result)
	}
	if rev != 99 {
		t.Fatalf("expected revision 99, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease write handlers - success and error paths
// ---------------------------------------------------------------------------

func TestRegularStateMachine_LeaseGrant_Success(t *testing.T) {
	lm := &mockLeaseManager{
		grantFn: func(ttl int64) (int64, int64, error) {
			return 100, ttl, nil
		},
	}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseGrant(30))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value != 100 {
		t.Fatalf("expected lease ID 100, got %d", result.Value)
	}
}

func TestRegularStateMachine_LeaseGrant_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		grantFn: func(ttl int64) (int64, int64, error) {
			return 0, 0, errors.New("grant fail")
		},
	}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseGrant(30))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}
}

func TestRegularStateMachine_LeaseRevoke_Success(t *testing.T) {
	lm := &mockLeaseManager{}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseRevoke(42))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_LeaseRevoke_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		revokeFn: func(leaseID int64) error {
			return errors.New("revoke fail")
		},
	}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseRevoke(42))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}
}

func TestRegularStateMachine_LeaseKeepAlive_Success(t *testing.T) {
	lm := &mockLeaseManager{
		keepAliveFn: func(leaseID int64) (int64, error) {
			return 25, nil
		},
	}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseKeepAlive(42))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_LeaseKeepAlive_ManagerError(t *testing.T) {
	lm := &mockLeaseManager{
		keepAliveFn: func(leaseID int64) (int64, error) {
			return 0, errors.New("keepalive fail")
		},
	}
	rsm := newRegularWithLease(lm)

	cmd := mustEncode(protocol.EncodeLeaseKeepAlive(42))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from lease manager")
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease lookup handlers
// ---------------------------------------------------------------------------

func TestRegularStateMachine_LeaseLookup_TimeToLive_Pointer(t *testing.T) {
	lm := &mockLeaseManager{
		ttlFn: func(leaseID int64) (interface{}, error) {
			return &lease.Lease{ID: leaseID, TTL: 30}, nil
		},
	}
	rsm := newRegularWithLease(lm)

	result, err := rsm.Lookup(context.Background(), &lease.LeaseTimeToLiveQuery{ID: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	l, ok := result.(*lease.Lease)
	if !ok {
		t.Fatalf("expected *lease.Lease, got %T", result)
	}
	if l.ID != 42 {
		t.Fatalf("expected lease ID 42, got %d", l.ID)
	}
}

func TestRegularStateMachine_LeaseLookup_TimeToLive_Value(t *testing.T) {
	lm := &mockLeaseManager{
		ttlFn: func(leaseID int64) (interface{}, error) {
			return &lease.Lease{ID: leaseID, TTL: 60}, nil
		},
	}
	rsm := newRegularWithLease(lm)

	result, err := rsm.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 99})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	l, ok := result.(*lease.Lease)
	if !ok {
		t.Fatalf("expected *lease.Lease, got %T", result)
	}
	if l.ID != 99 {
		t.Fatalf("expected lease ID 99, got %d", l.ID)
	}
}

func TestRegularStateMachine_LeaseLookup_Leases_Pointer(t *testing.T) {
	lm := &mockLeaseManager{
		leasesFn: func() (interface{}, error) {
			return []*lease.Lease{{ID: 1}, {ID: 2}}, nil
		},
	}
	rsm := newRegularWithLease(lm)

	result, err := rsm.Lookup(context.Background(), &lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	leases, ok := result.([]*lease.Lease)
	if !ok {
		t.Fatalf("expected []*lease.Lease, got %T", result)
	}
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(leases))
	}
}

func TestRegularStateMachine_LeaseLookup_Leases_Value(t *testing.T) {
	lm := &mockLeaseManager{
		leasesFn: func() (interface{}, error) {
			return []*lease.Lease{{ID: 3}}, nil
		},
	}
	rsm := newRegularWithLease(lm)

	result, err := rsm.Lookup(context.Background(), lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	leases, ok := result.([]*lease.Lease)
	if !ok {
		t.Fatalf("expected []*lease.Lease, got %T", result)
	}
	if len(leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(leases))
	}
}

// ---------------------------------------------------------------------------
// Tests: Index write handlers - success and error paths
// ---------------------------------------------------------------------------

func TestRegularStateMachine_IndexCreate_Success(t *testing.T) {
	im := &mockIndexManager{}
	rsm := newRegularWithIndex(im)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("email_idx", "email", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_IndexCreate_ManagerError(t *testing.T) {
	im := &mockIndexManager{
		createErr: errors.New("create fail"),
	}
	rsm := newRegularWithIndex(im)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("email_idx", "email", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from index manager")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T", err)
	}
}

func TestRegularStateMachine_IndexDrop_Success(t *testing.T) {
	im := &mockIndexManager{}
	rsm := newRegularWithIndex(im)

	cmd := mustEncode(protocol.EncodeIndexDrop("email_idx"))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_IndexDrop_ManagerError(t *testing.T) {
	im := &mockIndexManager{
		dropErr: errors.New("drop fail"),
	}
	rsm := newRegularWithIndex(im)

	cmd := mustEncode(protocol.EncodeIndexDrop("email_idx"))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from index manager")
	}
}

// ---------------------------------------------------------------------------
// Tests: Index lookup handlers
// ---------------------------------------------------------------------------

func TestRegularStateMachine_IndexLookup_Query_Success(t *testing.T) {
	im := &mockIndexManager{
		queryResult: [][]byte{[]byte("1"), []byte("2"), []byte("3")},
	}
	rsm := newRegularWithIndex(im)

	result, err := rsm.Lookup(context.Background(), index.IndexQueryRequest{IndexName: "idx", Value: []byte("val")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ids, ok := result.([][]byte)
	if !ok {
		t.Fatalf("expected [][]byte, got %T", result)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 ids, got %d", len(ids))
	}
}

func TestRegularStateMachine_IndexLookup_Scan_Success(t *testing.T) {
	im := &mockIndexManager{
		scanResult: map[string][][]byte{"a": {[]byte("1")}},
	}
	rsm := newRegularWithIndex(im)

	result, err := rsm.Lookup(context.Background(), index.IndexScanRequest{IndexName: "idx", Start: []byte("a"), End: []byte("z")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	scan, ok := result.(map[string][][]byte)
	if !ok {
		t.Fatalf("expected map[string][][]byte, got %T", result)
	}
	if len(scan) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(scan))
	}
}

func TestRegularStateMachine_IndexLookup_List_Success(t *testing.T) {
	im := &mockIndexManager{
		listResult: []interface{}{"idx1", "idx2"},
	}
	rsm := newRegularWithIndex(im)

	result, err := rsm.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, ok := result.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", result)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series write handlers - success and error paths
// ---------------------------------------------------------------------------

func TestRegularStateMachine_TSWrite_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSWrite("cpu", 1000, 42.5, map[string]string{"host": "a"}))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_TSWrite_StoreError(t *testing.T) {
	ts := &mockTimeSeriesStore{writePointErr: errors.New("write fail")}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSWrite("cpu", 1000, 42.5, nil))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from ts store")
	}
}

func TestRegularStateMachine_TSWriteBatch_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := newRegularWithTS(ts)

	points := []protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: 1000, Value: 42.5},
		{Metric: "mem", Timestamp: 2000, Value: 80.0},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(points))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_TSWriteBatch_StoreError(t *testing.T) {
	ts := &mockTimeSeriesStore{writeBatchErr: errors.New("batch fail")}
	rsm := newRegularWithTS(ts)

	points := []protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: 1000, Value: 42.5},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(points))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from ts store")
	}
}

func TestRegularStateMachine_TSWritePoint_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSWritePoint("mem", 3000, 64.0, nil))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_TSWritePoint_StoreError(t *testing.T) {
	ts := &mockTimeSeriesStore{writePointErr: errors.New("point fail")}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSWritePoint("mem", 3000, 64.0, nil))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from ts store")
	}
}

func TestRegularStateMachine_TSDeleteSeries_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu", 0, 9999, nil))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_TSDeleteSeries_StoreError(t *testing.T) {
	ts := &mockTimeSeriesStore{deleteSeriesErr: errors.New("delete fail")}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu", 0, 9999, nil))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from ts store")
	}
}

func TestRegularStateMachine_TSSetRetention_Success(t *testing.T) {
	ts := &mockTimeSeriesStore{}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu", 86400000000000, 60000000000, 1))
	result, err := regularApplyCmd(t, rsm, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestRegularStateMachine_TSSetRetention_StoreError(t *testing.T) {
	ts := &mockTimeSeriesStore{setRetentionErr: errors.New("retention fail")}
	rsm := newRegularWithTS(ts)

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu", 86400000000000, 60000000000, 1))
	_, err := regularApplyCmd(t, rsm, cmd)
	if err == nil {
		t.Fatal("expected error from ts store")
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series lookup handlers
// ---------------------------------------------------------------------------

func TestRegularStateMachine_TSLookup_QueryRange_Value(t *testing.T) {
	ts := &mockTimeSeriesStore{queryRangeResult: "range-data"}
	rsm := newRegularWithTS(ts)

	now := time.Now()
	result, err := rsm.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "cpu", Start: now.Add(-time.Hour), End: now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "range-data" {
		t.Fatalf("expected 'range-data', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_QueryRange_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{queryRangeResult: "ptr-data"}
	rsm := newRegularWithTS(ts)

	now := time.Now()
	result, err := rsm.Lookup(context.Background(), &timeseries.TSQueryRequest{
		Metric: "cpu", Start: now.Add(-time.Hour), End: now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ptr-data" {
		t.Fatalf("expected 'ptr-data', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_Aggregate_Value(t *testing.T) {
	ts := &mockTimeSeriesStore{queryAggResult: "agg-data"}
	rsm := newRegularWithTS(ts)

	now := time.Now()
	result, err := rsm.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric: "cpu", Start: now.Add(-time.Hour), End: now, Func: 1, Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "agg-data" {
		t.Fatalf("expected 'agg-data', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_Aggregate_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{queryAggResult: "agg-ptr"}
	rsm := newRegularWithTS(ts)

	now := time.Now()
	result, err := rsm.Lookup(context.Background(), &timeseries.TSAggregateRequest{
		Metric: "cpu", Start: now.Add(-time.Hour), End: now, Func: 1, Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "agg-ptr" {
		t.Fatalf("expected 'agg-ptr', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_RichQuery_Value(t *testing.T) {
	ts := &mockTimeSeriesStore{queryRichResult: "rich-data"}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), timeseries.TSRichQueryRequest{Request: nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "rich-data" {
		t.Fatalf("expected 'rich-data', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_RichQuery_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{queryRichResult: "rich-ptr"}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), &timeseries.TSRichQueryRequest{Request: nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "rich-ptr" {
		t.Fatalf("expected 'rich-ptr', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_GetRetention_Value(t *testing.T) {
	ts := &mockTimeSeriesStore{getRetentionResult: "retention-data"}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "retention-data" {
		t.Fatalf("expected 'retention-data', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_GetRetention_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{getRetentionResult: "retention-ptr"}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), &timeseries.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "retention-ptr" {
		t.Fatalf("expected 'retention-ptr', got %v", result)
	}
}

func TestRegularStateMachine_TSLookup_ListMetrics_Value(t *testing.T) {
	ts := &mockTimeSeriesStore{listMetricsResult: []string{"cpu", "mem"}}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "c", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	metrics, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(metrics))
	}
}

func TestRegularStateMachine_TSLookup_ListMetrics_Pointer(t *testing.T) {
	ts := &mockTimeSeriesStore{listMetricsResult: []string{"disk"}}
	rsm := newRegularWithTS(ts)

	result, err := rsm.Lookup(context.Background(), &timeseries.TSListMetricsRequest{Prefix: "d", Limit: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	metrics, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", result)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}
}

// ---------------------------------------------------------------------------
// Tests: Snapshot save/recover write error paths
// ---------------------------------------------------------------------------

func TestRegularStateMachine_SaveSnapshot_WriteKeyLenError(t *testing.T) {
	rsm := newTestRegularFSM()
	regularApplyPut(t, rsm, "key", "value")

	w := &failWriter{failAt: 1} // fail on second write (key len after count)
	stopper := make(chan struct{})
	err := rsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from failing writer")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

func TestRegularStateMachine_RecoverSnapshot_KeyReadError(t *testing.T) {
	rsm := newTestRegularFSM()

	// Construct a truncated snapshot: count=1 but no key data
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	countBuf[0] = 1 // 1 entry
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	lenBuf[0] = 10 // key length = 10 but no data follows
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := rsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error from truncated snapshot")
	}
}

// ---------------------------------------------------------------------------
// failWriter: a writer that fails after N successful writes
// ---------------------------------------------------------------------------

type failWriter struct {
	count  int
	failAt int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.count >= w.failAt {
		return 0, errors.New("write failed")
	}
	w.count++
	return len(p), nil
}
