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

//go:build codec_json && raft

package statemachine

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// ---------------------------------------------------------------------------
// WithKVLogger option (0% coverage)
// ---------------------------------------------------------------------------

func TestWithKVLogger_SetsLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	k := newTestKVSM(WithKVLogger(logger))
	if k.logger != logger {
		t.Fatal("expected logger to be set via WithKVLogger")
	}
}

// ---------------------------------------------------------------------------
// handleTSWriteBatch (0% coverage)
// ---------------------------------------------------------------------------

func TestKVDispatch_TSWriteBatch_Success(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: 1000, Value: 42.5, Tags: map[string]string{"host": "a"}},
		{Metric: "mem.used", Timestamp: 2000, Value: 75.0, Tags: nil},
	})
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSWriteBatch failed: %v", err)
	}
	if !ts.writeBatchCalled {
		t.Fatal("expected TimeSeriesStore.WriteDataPoints to be called")
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}
}

func TestKVDispatch_TSWriteBatch_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: 1000, Value: 1.0, Tags: nil},
	})
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention timeseries, got %q", results[0].Data)
	}
}

func TestKVDispatch_TSWriteBatch_Empty(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := protocol.EncodeTSWriteBatch([]protocol.TSWriteParams{})
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSWriteBatch empty failed: %v", err)
	}
	if !ts.writeBatchCalled {
		t.Fatal("expected WriteDataPoints to be called even for empty batch")
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go: error paths for storage errors
// ---------------------------------------------------------------------------

type errMVCCStore struct {
	kvMockMVCCStore
	putErr     error
	deleteErr  error
	rangeErr   error
	compactErr error
	txnErr     error
}

func (m *errMVCCStore) Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error) {
	m.putCalled = true
	return nil, m.putErr
}

func (m *errMVCCStore) Delete(key []byte, revision uint64) (interface{}, error) {
	m.deleteCalled = true
	return nil, m.deleteErr
}

func (m *errMVCCStore) Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error) {
	m.rangeCalled = true
	return nil, 0, m.rangeErr
}

func (m *errMVCCStore) Compact(revision int64) error {
	m.compactCalled = true
	return m.compactErr
}

func (m *errMVCCStore) Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (bool, int64, interface{}, error) {
	m.txnCalled = true
	return false, 0, nil, m.txnErr
}

func TestKVDispatch_MVCCPut_StorageError(t *testing.T) {
	storageErr := errors.New("disk full")
	mvcc := &errMVCCStore{putErr: storageErr}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := protocol.EncodeMVCCPut([]byte("key"), []byte("val"), 0)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error on storage failure")
	}
	var storErr *StorageError
	if !errors.As(err, &storErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storErr.Op != "mvcc-put" {
		t.Fatalf("expected op 'mvcc-put', got %q", storErr.Op)
	}
}

func TestKVDispatch_MVCCDelete_StorageError(t *testing.T) {
	storageErr := errors.New("write failed")
	mvcc := &errMVCCStore{deleteErr: storageErr}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := protocol.EncodeMVCCDelete([]byte("key"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	var storErr *StorageError
	if !errors.As(err, &storErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storErr.Op != "mvcc-delete" {
		t.Fatalf("expected op 'mvcc-delete', got %q", storErr.Op)
	}
}

func TestKVDispatch_MVCCRange_StorageError(t *testing.T) {
	storageErr := errors.New("range error")
	mvcc := &errMVCCStore{rangeErr: storageErr}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := protocol.EncodeMVCCRange([]byte("a"), []byte("z"), 0, 10, false, false)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	var storErr *StorageError
	if !errors.As(err, &storErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storErr.Op != "mvcc-range" {
		t.Fatalf("expected op 'mvcc-range', got %q", storErr.Op)
	}
}

func TestKVDispatch_MVCCCompact_StorageError(t *testing.T) {
	storageErr := errors.New("compact error")
	mvcc := &errMVCCStore{compactErr: storageErr}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := protocol.EncodeMVCCCompact(5)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	var storErr *StorageError
	if !errors.As(err, &storErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storErr.Op != "mvcc-compact" {
		t.Fatalf("expected op 'mvcc-compact', got %q", storErr.Op)
	}
}

func TestKVDispatch_Transaction_StorageError(t *testing.T) {
	storageErr := errors.New("txn error")
	mvcc := &errMVCCStore{txnErr: storageErr}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := protocol.EncodeTransaction(nil, nil, nil)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	var storErr *StorageError
	if !errors.As(err, &storErr) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if storErr.Op != "transaction" {
		t.Fatalf("expected op 'transaction', got %q", storErr.Op)
	}
}

func TestKVDispatch_Transaction_Failure_Result(t *testing.T) {
	// Transaction that returns succeeded=false uses a dedicated mock.
	failMVCC := &failedTransactionMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(failMVCC))
	cmd := protocol.EncodeTransaction(nil, nil, nil)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := k.Update(entries, results); err != nil {
		t.Fatalf("transaction failed: %v", err)
	}
	// failedTransactionMVCCStore returns succeeded=false; verify data byte is 0.
	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Fatalf("expected succeeded=false (0), got %v", results[0].Data)
	}
}

// failedTransactionMVCCStore is a minimal MVCCStore that returns succeeded=false.
type failedTransactionMVCCStore struct{}

func (m *failedTransactionMVCCStore) Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error) {
	return nil, nil
}

func (m *failedTransactionMVCCStore) Delete(key []byte, revision uint64) (interface{}, error) {
	return nil, nil
}

func (m *failedTransactionMVCCStore) Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error) {
	return nil, 0, nil
}

func (m *failedTransactionMVCCStore) Compact(revision int64) error {
	return nil
}

func (m *failedTransactionMVCCStore) Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (bool, int64, interface{}, error) {
	return false, 0, nil, nil
}

// ---------------------------------------------------------------------------
// kv_dispatch: IndexDrop not configured error path
// ---------------------------------------------------------------------------

func TestKVDispatch_IndexDrop_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := protocol.EncodeIndexDrop("my-index")
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("index")) {
		t.Fatalf("expected Data to mention index, got %q", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch: Lease not configured for revoke and keep-alive
// ---------------------------------------------------------------------------

func TestKVDispatch_LeaseRevoke_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	payload := protocol.EncodeLeaseRevoke(100)
	cmd := append([]byte{protocol.OpLeaseRevoke}, payload...)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention lease, got %q", results[0].Data)
	}
}

func TestKVDispatch_LeaseKeepAlive_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	payload := protocol.EncodeLeaseKeepAlive(100)
	cmd := append([]byte{protocol.OpLeaseKeepAlive}, payload...)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("lease")) {
		t.Fatalf("expected Data to mention lease, got %q", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch: TS handlers not configured
// ---------------------------------------------------------------------------

func TestKVDispatch_TSWritePoint_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	cmd := protocol.EncodeTSWritePoint("cpu", 1000, 42.0, nil)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention timeseries, got %q", results[0].Data)
	}
}

func TestKVDispatch_TSDeleteSeries_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	cmd := protocol.EncodeTSDeleteSeries("cpu", 0, 1000, nil)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention timeseries, got %q", results[0].Data)
	}
}

func TestKVDispatch_TSSetRetention_NotConfigured(t *testing.T) {
	k := newTestKVSM()
	cmd := protocol.EncodeTSSetRetention("cpu", 86400000000000, 60000000000, 1)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if !bytes.Contains(results[0].Data, []byte("timeseries")) {
		t.Fatalf("expected Data to mention timeseries, got %q", results[0].Data)
	}
}

// ---------------------------------------------------------------------------
// kv_statemachine.go: handleBatch error path (batch with backend Put error)
// ---------------------------------------------------------------------------

func TestKVStateMachine_HandleBatch_DecodeFailed(t *testing.T) {
	k := newTestKVSM()
	// OpBatch with invalid payload (no op count)
	entries := []sm.Entry{{Index: 1, Cmd: []byte{protocol.OpBatch}}}
	results := make([]sm.Result, 1)
	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for batch with no payload")
	}
	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ScanChanges satisfies the MVCCStore interface.
func (m *failedTransactionMVCCStore) ScanChanges(_, _ int64, _ []byte, _ int64) (interface{}, error) {
	return nil, nil
}
