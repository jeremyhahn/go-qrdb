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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// mockMVCCStore: in-memory mock of the MVCCStore interface for testing
// ---------------------------------------------------------------------------

type mvccPutCall struct {
	Key      []byte
	Value    []byte
	LeaseID  int64
	Revision uint64
}

type mvccDeleteCall struct {
	Key      []byte
	Revision uint64
}

type mvccRangeCall struct {
	Start     []byte
	End       []byte
	Revision  int64
	Limit     int64
	CountOnly bool
	KeysOnly  bool
}

type txnCall struct {
	Compares   []MVCCCompare
	SuccessOps []MVCCOp
	FailureOps []MVCCOp
}

type mockMVCCStore struct {
	mu sync.Mutex

	// Call tracking
	putCalls        []mvccPutCall
	deleteCalls     []mvccDeleteCall
	rangeCalls      []mvccRangeCall
	compactCalls    []int64
	compactPrefixes [][]byte
	txnCalls        []txnCall
	scanCalls       []mvccScanChangesCall

	// Error injection
	putErr    error
	deleteErr error
	rangeErr  error
	txnErr    error
	scanErr   error

	// Return values
	putResult     interface{}
	deleteResult  interface{}
	rangeResult   interface{}
	rangeRevision int64
	scanResult    interface{}
	txnSucceeded  bool
	txnRevision   int64
	txnResponses  interface{}
}

func (m *mockMVCCStore) Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putCalls = append(m.putCalls, mvccPutCall{
		Key:      key,
		Value:    value,
		LeaseID:  leaseID,
		Revision: revision,
	})
	return m.putResult, m.putErr
}

func (m *mockMVCCStore) Delete(key []byte, revision uint64) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls = append(m.deleteCalls, mvccDeleteCall{
		Key:      key,
		Revision: revision,
	})
	return m.deleteResult, m.deleteErr
}

func (m *mockMVCCStore) Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rangeCalls = append(m.rangeCalls, mvccRangeCall{
		Start:     start,
		End:       end,
		Revision:  revision,
		Limit:     limit,
		CountOnly: countOnly,
		KeysOnly:  keysOnly,
	})
	return m.rangeResult, m.rangeRevision, m.rangeErr
}

func (m *mockMVCCStore) Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (bool, int64, interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txnCalls = append(m.txnCalls, txnCall{
		Compares:   compares,
		SuccessOps: successOps,
		FailureOps: failureOps,
	})
	return m.txnSucceeded, m.txnRevision, m.txnResponses, m.txnErr
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newTestFSMWithMVCC(ms MVCCStore) (*FastStateMachine, *memStorage) {
	store := newMemStorage()
	fsm := NewFastStateMachine(1, 1, store, WithMVCCStore(ms))
	return fsm, store
}

// ---------------------------------------------------------------------------
// MVCC Put (Update path)
// ---------------------------------------------------------------------------

func TestFSM_MVCCPut_Success(t *testing.T) {
	ms := &mockMVCCStore{putResult: "ok"}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("mykey"), []byte("myval"), 42))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(mvcc put) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from MVCC put")
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.putCalls) != 1 {
		t.Fatalf("expected 1 put call, got %d", len(ms.putCalls))
	}

	call := ms.putCalls[0]
	if !bytes.Equal(call.Key, []byte("mykey")) {
		t.Fatalf("expected key 'mykey', got %q", call.Key)
	}
	if !bytes.Equal(call.Value, []byte("myval")) {
		t.Fatalf("expected value 'myval', got %q", call.Value)
	}
	if call.LeaseID != 42 {
		t.Fatalf("expected leaseID 42, got %d", call.LeaseID)
	}
	if call.Revision != results[0].Value {
		t.Fatalf("expected revision %d, got %d", results[0].Value, call.Revision)
	}
}

func TestFSM_MVCCPut_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
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

func TestFSM_MVCCPut_StoreError(t *testing.T) {
	storeErr := errors.New("disk full")
	ms := &mockMVCCStore{putErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "mvcc-put" {
		t.Fatalf("expected op 'mvcc-put', got %q", storageErr.Op)
	}
	if !errors.Is(storageErr, storeErr) {
		t.Fatalf("expected inner error to be 'disk full', got %v", storageErr.Inner)
	}
}

// ---------------------------------------------------------------------------
// MVCC Delete (Update path)
// ---------------------------------------------------------------------------

func TestFSM_MVCCDelete_Success(t *testing.T) {
	ms := &mockMVCCStore{deleteResult: "deleted"}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("delkey")))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(mvcc delete) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from MVCC delete")
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.deleteCalls) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(ms.deleteCalls))
	}

	call := ms.deleteCalls[0]
	if !bytes.Equal(call.Key, []byte("delkey")) {
		t.Fatalf("expected key 'delkey', got %q", call.Key)
	}
	if call.Revision != results[0].Value {
		t.Fatalf("expected revision %d, got %d", results[0].Value, call.Revision)
	}
}

func TestFSM_MVCCDelete_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("key")))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
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

func TestFSM_MVCCDelete_StoreError(t *testing.T) {
	storeErr := errors.New("key locked")
	ms := &mockMVCCStore{deleteErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("key")))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "mvcc-delete" {
		t.Fatalf("expected op 'mvcc-delete', got %q", storageErr.Op)
	}
	if !errors.Is(storageErr, storeErr) {
		t.Fatalf("expected inner error to be 'key locked', got %v", storageErr.Inner)
	}
}

// ---------------------------------------------------------------------------
// MVCC Range (Update path)
// ---------------------------------------------------------------------------

func TestFSM_MVCCRange_Success(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   []string{"a", "b"},
		rangeRevision: 10,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("start"), []byte("end"), 5, 100, false, true))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(mvcc range) failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from MVCC range")
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if !bytes.Equal(call.Start, []byte("start")) {
		t.Fatalf("expected start 'start', got %q", call.Start)
	}
	if !bytes.Equal(call.End, []byte("end")) {
		t.Fatalf("expected end 'end', got %q", call.End)
	}
	if call.Revision != 5 {
		t.Fatalf("expected revision 5, got %d", call.Revision)
	}
	if call.Limit != 100 {
		t.Fatalf("expected limit 100, got %d", call.Limit)
	}
	if call.CountOnly {
		t.Fatal("expected countOnly=false")
	}
	if !call.KeysOnly {
		t.Fatal("expected keysOnly=true")
	}
}

func TestFSM_MVCCRange_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("a"), []byte("z"), 0, 10, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
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

func TestFSM_MVCCRange_StoreError(t *testing.T) {
	storeErr := errors.New("range scan timeout")
	ms := &mockMVCCStore{rangeErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("a"), []byte("z"), 0, 10, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "mvcc-range" {
		t.Fatalf("expected op 'mvcc-range', got %q", storageErr.Op)
	}
}

// ---------------------------------------------------------------------------
// MVCC Compact (Update path)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Transaction (Update path)
// ---------------------------------------------------------------------------

func TestFSM_Transaction_Success(t *testing.T) {
	ms := &mockMVCCStore{
		txnSucceeded: true,
		txnRevision:  77,
		txnResponses: "txn-ok",
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	// Build compare: key "k1", target=0, result=0, value=[]byte("v1")
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("k1"), // key
		0,            // target
		0,            // result
		[]byte("v1"), // value
		0,            // version
		0,            // createRevision
		0,            // modRevision
	))
	// Build success ops: put k1=new-v1, delete k2
	putOp := mustEncode(protocol.EncodeOp(1, []byte("k1"), []byte("new-v1"), nil, 0)) // put
	deleteOp := mustEncode(protocol.EncodeOp(3, []byte("k2"), nil, nil, 0))           // delete

	cmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{compare},
		[][]byte{putOp, deleteOp},
		nil, // no failure ops
	))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(transaction) failed: %v", err)
	}

	// The result value should be the transaction revision.
	if results[0].Value != 77 {
		t.Fatalf("expected result value 77, got %d", results[0].Value)
	}

	// The result data should encode succeeded=true (byte 1).
	if len(results[0].Data) != 1 || results[0].Data[0] != 1 {
		t.Fatalf("expected succeeded flag [1], got %v", results[0].Data)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.txnCalls) != 1 {
		t.Fatalf("expected 1 txn call, got %d", len(ms.txnCalls))
	}

	txn := ms.txnCalls[0]

	// Verify compares
	if len(txn.Compares) != 1 {
		t.Fatalf("expected 1 compare, got %d", len(txn.Compares))
	}
	cmp := txn.Compares[0]
	if !bytes.Equal(cmp.Key, []byte("k1")) {
		t.Fatalf("compare key: expected 'k1', got %q", cmp.Key)
	}
	if cmp.Target != 0 {
		t.Fatalf("compare target: expected 0, got %d", cmp.Target)
	}
	if cmp.Result != 0 {
		t.Fatalf("compare result: expected 0, got %d", cmp.Result)
	}
	if !bytes.Equal(cmp.Value, []byte("v1")) {
		t.Fatalf("compare value: expected 'v1', got %q", cmp.Value)
	}

	// Verify success ops
	if len(txn.SuccessOps) != 2 {
		t.Fatalf("expected 2 success ops, got %d", len(txn.SuccessOps))
	}

	sop0 := txn.SuccessOps[0]
	if sop0.Type != 1 {
		t.Fatalf("success op 0 type: expected 1 (put), got %d", sop0.Type)
	}
	if !bytes.Equal(sop0.Key, []byte("k1")) {
		t.Fatalf("success op 0 key: expected 'k1', got %q", sop0.Key)
	}
	if !bytes.Equal(sop0.Value, []byte("new-v1")) {
		t.Fatalf("success op 0 value: expected 'new-v1', got %q", sop0.Value)
	}

	sop1 := txn.SuccessOps[1]
	if sop1.Type != 3 {
		t.Fatalf("success op 1 type: expected 3 (delete), got %d", sop1.Type)
	}
	if !bytes.Equal(sop1.Key, []byte("k2")) {
		t.Fatalf("success op 1 key: expected 'k2', got %q", sop1.Key)
	}

	// Verify no failure ops
	if len(txn.FailureOps) != 0 {
		t.Fatalf("expected 0 failure ops, got %d", len(txn.FailureOps))
	}
}

func TestFSM_Transaction_FailedBranch(t *testing.T) {
	ms := &mockMVCCStore{
		txnSucceeded: false,
		txnRevision:  50,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	compare := mustEncode(protocol.EncodeCompare([]byte("k1"), 0, 0, []byte("v1"), 0, 0, 0))
	putOp := mustEncode(protocol.EncodeOp(1, []byte("k1"), []byte("new-v1"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{compare},
		[][]byte{putOp},
		nil,
	))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(transaction) failed: %v", err)
	}

	// The result data should encode succeeded=false (byte 0).
	if len(results[0].Data) != 1 || results[0].Data[0] != 0 {
		t.Fatalf("expected succeeded flag [0], got %v", results[0].Data)
	}
	if results[0].Value != 50 {
		t.Fatalf("expected result value 50, got %d", results[0].Value)
	}
}

func TestFSM_Transaction_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()

	compare := mustEncode(protocol.EncodeCompare([]byte("k1"), 0, 0, []byte("v1"), 0, 0, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, nil, nil))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
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

func TestFSM_Transaction_StoreError(t *testing.T) {
	storeErr := errors.New("conflict detected")
	ms := &mockMVCCStore{txnErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	compare := mustEncode(protocol.EncodeCompare([]byte("k1"), 0, 0, []byte("v1"), 0, 0, 0))
	putOp := mustEncode(protocol.EncodeOp(1, []byte("k1"), []byte("new-v1"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{compare},
		[][]byte{putOp},
		nil,
	))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected StorageError, got nil")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "transaction" {
		t.Fatalf("expected op 'transaction', got %q", storageErr.Op)
	}
	if !errors.Is(storageErr, storeErr) {
		t.Fatalf("expected inner error to be 'conflict detected', got %v", storageErr.Inner)
	}
}

func TestFSM_Transaction_CompareRevisionFields(t *testing.T) {
	ms := &mockMVCCStore{
		txnSucceeded: true,
		txnRevision:  99,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	// Build a compare with all revision fields populated.
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("k1"),
		1, // target
		2, // result
		[]byte("v1"),
		10, // version
		20, // createRevision
		30, // modRevision
	))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, nil, nil))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(transaction) failed: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	cmp := ms.txnCalls[0].Compares[0]
	if cmp.Target != 1 {
		t.Fatalf("expected target 1, got %d", cmp.Target)
	}
	if cmp.Result != 2 {
		t.Fatalf("expected result 2, got %d", cmp.Result)
	}
	if cmp.Version != 10 {
		t.Fatalf("expected version 10, got %d", cmp.Version)
	}
	if cmp.CreateRevision != 20 {
		t.Fatalf("expected createRevision 20, got %d", cmp.CreateRevision)
	}
	if cmp.ModRevision != 30 {
		t.Fatalf("expected modRevision 30, got %d", cmp.ModRevision)
	}
}

func TestFSM_Transaction_WithFailureOps(t *testing.T) {
	ms := &mockMVCCStore{
		txnSucceeded: false,
		txnRevision:  60,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	compare := mustEncode(protocol.EncodeCompare([]byte("k1"), 0, 0, []byte("v1"), 0, 0, 0))
	successOp := mustEncode(protocol.EncodeOp(1, []byte("k1"), []byte("new-v1"), nil, 0))
	failureOp := mustEncode(protocol.EncodeOp(1, []byte("k1"), []byte("fallback"), nil, 99))
	cmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{compare},
		[][]byte{successOp},
		[][]byte{failureOp},
	))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(transaction) failed: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	txn := ms.txnCalls[0]
	if len(txn.FailureOps) != 1 {
		t.Fatalf("expected 1 failure op, got %d", len(txn.FailureOps))
	}

	fop := txn.FailureOps[0]
	if fop.Type != 1 {
		t.Fatalf("failure op type: expected 1, got %d", fop.Type)
	}
	if !bytes.Equal(fop.Key, []byte("k1")) {
		t.Fatalf("failure op key: expected 'k1', got %q", fop.Key)
	}
	if !bytes.Equal(fop.Value, []byte("fallback")) {
		t.Fatalf("failure op value: expected 'fallback', got %q", fop.Value)
	}
	if fop.LeaseID != 99 {
		t.Fatalf("failure op leaseID: expected 99, got %d", fop.LeaseID)
	}
}

func TestFSM_Transaction_OpRangeEnd(t *testing.T) {
	ms := &mockMVCCStore{
		txnSucceeded: true,
		txnRevision:  88,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	compare := mustEncode(protocol.EncodeCompare([]byte("k1"), 0, 0, nil, 0, 0, 0))
	rangeOp := mustEncode(protocol.EncodeOp(2, []byte("a"), nil, []byte("z"), 0)) // type 2 = get/range

	cmd := mustEncode(protocol.EncodeTransaction(
		[][]byte{compare},
		[][]byte{rangeOp},
		nil,
	))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(transaction) failed: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	sop := ms.txnCalls[0].SuccessOps[0]
	if !bytes.Equal(sop.RangeEnd, []byte("z")) {
		t.Fatalf("success op rangeEnd: expected 'z', got %q", sop.RangeEnd)
	}
}

// ---------------------------------------------------------------------------
// MVCC Lookup handlers (Lookup path)
// ---------------------------------------------------------------------------

func TestFSM_MVCCLookup_GetQuery(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "get-result",
		rangeRevision: 5,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), MVCCGetQuery{Key: []byte("k1")})
	if err != nil {
		t.Fatalf("Lookup(MVCCGetQuery) failed: %v", err)
	}
	if result != "get-result" {
		t.Fatalf("expected 'get-result', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if !bytes.Equal(call.Start, []byte("k1")) {
		t.Fatalf("expected start 'k1', got %q", call.Start)
	}
	if call.End != nil {
		t.Fatalf("expected nil end, got %q", call.End)
	}
	if call.Revision != 0 {
		t.Fatalf("expected revision 0, got %d", call.Revision)
	}
	if call.Limit != 1 {
		t.Fatalf("expected limit 1, got %d", call.Limit)
	}
	if call.CountOnly {
		t.Fatal("expected countOnly=false")
	}
	if call.KeysOnly {
		t.Fatal("expected keysOnly=false")
	}
}

func TestFSM_MVCCLookup_GetQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "ptr-result",
		rangeRevision: 7,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), &MVCCGetQuery{Key: []byte("k1")})
	if err != nil {
		t.Fatalf("Lookup(*MVCCGetQuery) failed: %v", err)
	}
	if result != "ptr-result" {
		t.Fatalf("expected 'ptr-result', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if !bytes.Equal(call.Start, []byte("k1")) {
		t.Fatalf("expected start 'k1', got %q", call.Start)
	}
	if call.End != nil {
		t.Fatalf("expected nil end, got %q", call.End)
	}
	if call.Limit != 1 {
		t.Fatalf("expected limit 1, got %d", call.Limit)
	}
}

func TestFSM_MVCCLookup_GetAtRevision(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "rev-result",
		rangeRevision: 5,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), MVCCGetAtRevisionQuery{
		Key:      []byte("k1"),
		Revision: 5,
	})
	if err != nil {
		t.Fatalf("Lookup(MVCCGetAtRevisionQuery) failed: %v", err)
	}
	if result != "rev-result" {
		t.Fatalf("expected 'rev-result', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if !bytes.Equal(call.Start, []byte("k1")) {
		t.Fatalf("expected start 'k1', got %q", call.Start)
	}
	if call.End != nil {
		t.Fatalf("expected nil end, got %q", call.End)
	}
	if call.Revision != 5 {
		t.Fatalf("expected revision 5, got %d", call.Revision)
	}
	if call.Limit != 1 {
		t.Fatalf("expected limit 1, got %d", call.Limit)
	}
}

func TestFSM_MVCCLookup_GetAtRevision_Pointer(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "ptr-rev-result",
		rangeRevision: 12,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), &MVCCGetAtRevisionQuery{
		Key:      []byte("k1"),
		Revision: 12,
	})
	if err != nil {
		t.Fatalf("Lookup(*MVCCGetAtRevisionQuery) failed: %v", err)
	}
	if result != "ptr-rev-result" {
		t.Fatalf("expected 'ptr-rev-result', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	call := ms.rangeCalls[0]
	if call.Revision != 12 {
		t.Fatalf("expected revision 12, got %d", call.Revision)
	}
}

func TestFSM_MVCCLookup_RangeQuery(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "range-data",
		rangeRevision: 15,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), MVCCRangeQuery{
		Start:    []byte("a"),
		End:      []byte("z"),
		Revision: 3,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("Lookup(MVCCRangeQuery) failed: %v", err)
	}
	if result != "range-data" {
		t.Fatalf("expected 'range-data', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if !bytes.Equal(call.Start, []byte("a")) {
		t.Fatalf("expected start 'a', got %q", call.Start)
	}
	if !bytes.Equal(call.End, []byte("z")) {
		t.Fatalf("expected end 'z', got %q", call.End)
	}
	if call.Revision != 3 {
		t.Fatalf("expected revision 3, got %d", call.Revision)
	}
	if call.Limit != 10 {
		t.Fatalf("expected limit 10, got %d", call.Limit)
	}
	if call.CountOnly {
		t.Fatal("expected countOnly=false")
	}
	if call.KeysOnly {
		t.Fatal("expected keysOnly=false")
	}
}

func TestFSM_MVCCLookup_RangeQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{
		rangeResult:   "ptr-range-data",
		rangeRevision: 20,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), &MVCCRangeQuery{
		Start:     []byte("a"),
		End:       []byte("z"),
		Revision:  3,
		Limit:     10,
		CountOnly: true,
		KeysOnly:  true,
	})
	if err != nil {
		t.Fatalf("Lookup(*MVCCRangeQuery) failed: %v", err)
	}
	if result != "ptr-range-data" {
		t.Fatalf("expected 'ptr-range-data', got %v", result)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	call := ms.rangeCalls[0]
	if !call.CountOnly {
		t.Fatal("expected countOnly=true")
	}
	if !call.KeysOnly {
		t.Fatal("expected keysOnly=true")
	}
}

func TestFSM_MVCCLookup_RevisionQuery(t *testing.T) {
	ms := &mockMVCCStore{
		rangeRevision: 42,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup(MVCCRevisionQuery) failed: %v", err)
	}

	rev, ok := result.(int64)
	if !ok {
		t.Fatalf("expected int64 result, got %T", result)
	}
	if rev != 42 {
		t.Fatalf("expected revision 42, got %d", rev)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.rangeCalls) != 1 {
		t.Fatalf("expected 1 range call, got %d", len(ms.rangeCalls))
	}

	call := ms.rangeCalls[0]
	if call.Start != nil {
		t.Fatalf("expected nil start, got %q", call.Start)
	}
	if call.End != nil {
		t.Fatalf("expected nil end, got %q", call.End)
	}
	if call.Revision != 0 {
		t.Fatalf("expected revision 0, got %d", call.Revision)
	}
	if call.Limit != 0 {
		t.Fatalf("expected limit 0, got %d", call.Limit)
	}
	if !call.CountOnly {
		t.Fatal("expected countOnly=true")
	}
	if call.KeysOnly {
		t.Fatal("expected keysOnly=false")
	}
}

func TestFSM_MVCCLookup_RevisionQuery_Pointer(t *testing.T) {
	ms := &mockMVCCStore{
		rangeRevision: 100,
	}
	fsm, _ := newTestFSMWithMVCC(ms)

	result, err := fsm.Lookup(context.Background(), &MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup(*MVCCRevisionQuery) failed: %v", err)
	}

	rev, ok := result.(int64)
	if !ok {
		t.Fatalf("expected int64 result, got %T", result)
	}
	if rev != 100 {
		t.Fatalf("expected revision 100, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// MVCC Lookup: not configured
// ---------------------------------------------------------------------------

func TestFSM_MVCCLookup_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM() // no MVCC store

	queries := []struct {
		name  string
		query interface{}
	}{
		{"MVCCGetQuery", MVCCGetQuery{Key: []byte("k")}},
		{"*MVCCGetQuery", &MVCCGetQuery{Key: []byte("k")}},
		{"MVCCGetAtRevisionQuery", MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1}},
		{"*MVCCGetAtRevisionQuery", &MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1}},
		{"MVCCRangeQuery", MVCCRangeQuery{Start: []byte("a"), End: []byte("z")}},
		{"*MVCCRangeQuery", &MVCCRangeQuery{Start: []byte("a"), End: []byte("z")}},
		{"MVCCRevisionQuery", MVCCRevisionQuery{}},
		{"*MVCCRevisionQuery", &MVCCRevisionQuery{}},
	}

	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fsm.Lookup(context.Background(), tc.query)
			if err == nil {
				t.Fatalf("expected ServiceNotConfiguredError for %s, got nil", tc.name)
			}

			var svcErr *ServiceNotConfiguredError
			if !errors.As(err, &svcErr) {
				t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
			}
			if svcErr.Service != "mvcc" {
				t.Fatalf("expected service 'mvcc', got %q", svcErr.Service)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MVCC Lookup: store errors
// ---------------------------------------------------------------------------

func TestFSM_MVCCLookup_GetQuery_StoreError(t *testing.T) {
	storeErr := errors.New("range failed")
	ms := &mockMVCCStore{rangeErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	_, err := fsm.Lookup(context.Background(), MVCCGetQuery{Key: []byte("k1")})
	if err == nil {
		t.Fatal("expected error from MVCC store, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected inner error to be 'range failed', got %v", err)
	}
}

func TestFSM_MVCCLookup_RangeQuery_StoreError(t *testing.T) {
	storeErr := errors.New("io error")
	ms := &mockMVCCStore{rangeErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	_, err := fsm.Lookup(context.Background(), MVCCRangeQuery{
		Start: []byte("a"),
		End:   []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error from MVCC store, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected inner error to be 'io error', got %v", err)
	}
}

func TestFSM_MVCCLookup_RevisionQuery_StoreError(t *testing.T) {
	storeErr := errors.New("revision unavailable")
	ms := &mockMVCCStore{rangeErr: storeErr}
	fsm, _ := newTestFSMWithMVCC(ms)

	_, err := fsm.Lookup(context.Background(), MVCCRevisionQuery{})
	if err == nil {
		t.Fatal("expected error from MVCC store, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected inner error to be 'revision unavailable', got %v", err)
	}
}

// ---------------------------------------------------------------------------
// MVCC Put: payload edge cases
// ---------------------------------------------------------------------------

func TestFSM_MVCCPut_EmptyValue(t *testing.T) {
	ms := &mockMVCCStore{putResult: "ok"}
	fsm, _ := newTestFSMWithMVCC(ms)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), nil, 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(mvcc put empty value) failed: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	call := ms.putCalls[0]
	if len(call.Value) != 0 {
		t.Fatalf("expected empty value, got %q", call.Value)
	}
}

func TestFSM_MVCCPut_LargeLeaseID(t *testing.T) {
	ms := &mockMVCCStore{putResult: "ok"}
	fsm, _ := newTestFSMWithMVCC(ms)

	var largeLeaseID int64 = 9223372036854775807 // max int64
	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("key"), []byte("val"), largeLeaseID))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(mvcc put large leaseID) failed: %v", err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.putCalls[0].LeaseID != largeLeaseID {
		t.Fatalf("expected leaseID %d, got %d", largeLeaseID, ms.putCalls[0].LeaseID)
	}
}

// ---------------------------------------------------------------------------
// Multiple MVCC operations in a single Update batch
// ---------------------------------------------------------------------------

func TestFSM_MVCCMultipleEntries(t *testing.T) {
	ms := &mockMVCCStore{putResult: "ok", deleteResult: "ok"}
	fsm, _ := newTestFSMWithMVCC(ms)

	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("k1"), []byte("v1"), 0))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("k2"), []byte("v2"), 10))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeMVCCDelete([]byte("k1")))},
	}
	results := make([]sm.Result, 3)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("Update(multiple mvcc) failed: %v", err)
	}

	// Verify monotonically increasing revisions.
	if results[0].Value >= results[1].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[0].Value, results[1].Value)
	}
	if results[1].Value >= results[2].Value {
		t.Fatalf("expected increasing revisions: %d >= %d", results[1].Value, results[2].Value)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.putCalls) != 2 {
		t.Fatalf("expected 2 put calls, got %d", len(ms.putCalls))
	}
	if len(ms.deleteCalls) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(ms.deleteCalls))
	}
}

// ---------------------------------------------------------------------------
// MVCC Compact: negative revision
// ---------------------------------------------------------------------------

// mvccScanChangesCall records one ScanChanges invocation.
type mvccScanChangesCall struct {
	startRev, endRev int64
	prefix           []byte
	limit            int64
}

// ScanChanges records the call and returns the configured result, satisfying
// the MVCCStore interface.
func (m *mockMVCCStore) ScanChanges(startRev, endRev int64, prefix []byte, limit int64) (interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanCalls = append(m.scanCalls, mvccScanChangesCall{
		startRev: startRev, endRev: endRev, prefix: prefix, limit: limit,
	})
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	return m.scanResult, nil
}
