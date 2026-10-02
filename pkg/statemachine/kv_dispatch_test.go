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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Mock subsystem managers for KV dispatch tests (prefixed to avoid
// collisions with existing fast_*_test.go mocks)
// ---------------------------------------------------------------------------

type kvMockMVCCStore struct {
	putCalled    bool
	deleteCalled bool
	rangeCalled  bool
	txnCalled    bool
	scanCalled   bool
	scanResult   interface{}
	scanErr      error
}

// ScanChanges satisfies the MVCCStore interface.
func (m *kvMockMVCCStore) ScanChanges(_, _ int64, _ []byte, _ int64) (interface{}, error) {
	m.scanCalled = true
	return m.scanResult, m.scanErr
}

func (m *kvMockMVCCStore) Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error) {
	m.putCalled = true
	return nil, nil
}

func (m *kvMockMVCCStore) Delete(key []byte, revision uint64) (interface{}, error) {
	m.deleteCalled = true
	return nil, nil
}

func (m *kvMockMVCCStore) Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error) {
	m.rangeCalled = true
	return nil, 0, nil
}

func (m *kvMockMVCCStore) Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (bool, int64, interface{}, error) {
	m.txnCalled = true
	return true, 1, nil, nil
}

type kvMockIndexManager struct {
	createCalled bool
	dropCalled   bool
}

func (m *kvMockIndexManager) CreateIndex(name, fieldName string, opts protocol.IndexCreateOptions) error {
	m.createCalled = true
	return nil
}

func (m *kvMockIndexManager) DropIndex(name string, _ protocol.IndexDropOptions) error {
	m.dropCalled = true
	return nil
}

func (m *kvMockIndexManager) Query(indexName string, value []byte) ([][]byte, error) {
	return nil, nil
}

func (m *kvMockIndexManager) Scan(indexName string, start, end []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *kvMockIndexManager) List() ([]interface{}, error) {
	return nil, nil
}

type kvMockLeaseManager struct {
	grantCalled     bool
	revokeCalled    bool
	keepAliveCalled bool
}

func (m *kvMockLeaseManager) Grant(ttl int64) (int64, int64, error) {
	m.grantCalled = true
	return 100, ttl, nil
}

func (m *kvMockLeaseManager) Revoke(leaseID int64) error {
	m.revokeCalled = true
	return nil
}

func (m *kvMockLeaseManager) KeepAlive(leaseID int64) (int64, error) {
	m.keepAliveCalled = true
	return 30, nil
}

func (m *kvMockLeaseManager) TimeToLive(leaseID int64) (interface{}, error) {
	return nil, nil
}

func (m *kvMockLeaseManager) Leases() (interface{}, error) {
	return nil, nil
}

type kvMockTimeSeriesStore struct {
	writeCalled      bool
	writeBatchCalled bool
	deleteCalled     bool
	retentionCalled  bool
}

func (m *kvMockTimeSeriesStore) WriteDataPoint(metric string, timestamp int64, value float64, tags map[string]string) error {
	m.writeCalled = true
	return nil
}

func (m *kvMockTimeSeriesStore) WriteDataPoints(points []TSDataPoint) error {
	m.writeBatchCalled = true
	return nil
}

func (m *kvMockTimeSeriesStore) DeleteSeries(metric string, startNs, endNs int64, tags map[string]string) error {
	m.deleteCalled = true
	return nil
}

func (m *kvMockTimeSeriesStore) SetRetentionPolicy(metric string, durationNs, resolutionNs int64, aggregation byte) error {
	m.retentionCalled = true
	return nil
}

func (m *kvMockTimeSeriesStore) QueryRange(metric string, startNs, endNs int64) (interface{}, error) {
	return nil, nil
}

func (m *kvMockTimeSeriesStore) QueryAggregate(metric string, startNs, endNs int64, fn byte, intervalNs int64) (interface{}, error) {
	return nil, nil
}

func (m *kvMockTimeSeriesStore) QueryRich(request interface{}) (interface{}, error) {
	return nil, nil
}

func (m *kvMockTimeSeriesStore) GetRetentionPolicy(metric string) (interface{}, error) {
	return nil, nil
}

func (m *kvMockTimeSeriesStore) ListMetrics(prefix string, limit int) ([]string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// MVCC dispatch tests
// ---------------------------------------------------------------------------

func TestKVDispatch_MVCCPut(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	// EncodeMVCCPut includes the op byte; use as full command.
	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("mvcc-key"), []byte("mvcc-val"), 0))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("MVCC Put failed: %v", err)
	}
	if !mvcc.putCalled {
		t.Fatal("expected MVCCStore.Put to be called")
	}
}

func TestKVDispatch_MVCCPut_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	// Update must not return an error — ServiceNotConfiguredError is encoded
	// in result.Data to prevent Raft lastApplied from stalling.
	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("Update must not return error for unconfigured mvcc service: %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
}

func TestKVDispatch_MVCCDelete(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("mvcc-del")))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("MVCC Delete failed: %v", err)
	}
	if !mvcc.deleteCalled {
		t.Fatal("expected MVCCStore.Delete to be called")
	}
}

func TestKVDispatch_MVCCDelete_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := mustEncode(protocol.EncodeMVCCDelete([]byte("k")))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("Update must not return error for unconfigured mvcc service: %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
}

func TestKVDispatch_MVCCRange(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("start"), []byte("end"), 0, 100, false, false))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("MVCC Range failed: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected MVCCStore.Range to be called")
	}
}

func TestKVDispatch_Transaction(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := newTestKVSM(WithKVMVCCStore(mvcc))

	cmd := mustEncode(protocol.EncodeTransaction(nil, nil, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}
	if !mvcc.txnCalled {
		t.Fatal("expected MVCCStore.Transaction to be called")
	}
	// Verify success flag.
	if len(results[0].Data) != 1 || results[0].Data[0] != 1 {
		t.Fatal("expected succeeded=true in result data")
	}
}

func TestKVDispatch_Transaction_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := mustEncode(protocol.EncodeTransaction(nil, nil, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("Update must not return error for unconfigured transaction service: %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
}

// ---------------------------------------------------------------------------
// Index dispatch tests
// ---------------------------------------------------------------------------

func TestKVDispatch_IndexCreate(t *testing.T) {
	idx := &kvMockIndexManager{}
	k := newTestKVSM(WithKVIndexManager(idx))

	cmd := mustEncode(protocol.EncodeIndexCreate("test-idx", "field1", true, false, false))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("IndexCreate failed: %v", err)
	}
	if !idx.createCalled {
		t.Fatal("expected IndexManager.CreateIndex to be called")
	}
}

func TestKVDispatch_IndexDrop(t *testing.T) {
	idx := &kvMockIndexManager{}
	k := newTestKVSM(WithKVIndexManager(idx))

	cmd := mustEncode(protocol.EncodeIndexDrop("test-idx"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("IndexDrop failed: %v", err)
	}
	if !idx.dropCalled {
		t.Fatal("expected IndexManager.DropIndex to be called")
	}
}

// ---------------------------------------------------------------------------
// Lease dispatch tests
// ---------------------------------------------------------------------------

func TestKVDispatch_LeaseGrant(t *testing.T) {
	lm := &kvMockLeaseManager{}
	k := newTestKVSM(WithKVLeaseManager(lm))

	payload := mustEncode(protocol.EncodeLeaseGrant(30))
	cmd := append([]byte{protocol.OpLeaseGrant}, payload...)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("LeaseGrant failed: %v", err)
	}
	if !lm.grantCalled {
		t.Fatal("expected LeaseManager.Grant to be called")
	}
	if results[0].Value != 100 { // mock returns leaseID=100
		t.Fatalf("expected leaseID 100, got %d", results[0].Value)
	}
}

func TestKVDispatch_LeaseRevoke(t *testing.T) {
	lm := &kvMockLeaseManager{}
	k := newTestKVSM(WithKVLeaseManager(lm))

	payload := mustEncode(protocol.EncodeLeaseRevoke(100))
	cmd := append([]byte{protocol.OpLeaseRevoke}, payload...)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("LeaseRevoke failed: %v", err)
	}
	if !lm.revokeCalled {
		t.Fatal("expected LeaseManager.Revoke to be called")
	}
}

func TestKVDispatch_LeaseKeepAlive(t *testing.T) {
	lm := &kvMockLeaseManager{}
	k := newTestKVSM(WithKVLeaseManager(lm))

	payload := mustEncode(protocol.EncodeLeaseKeepAlive(100))
	cmd := append([]byte{protocol.OpLeaseKeepAlive}, payload...)
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("LeaseKeepAlive failed: %v", err)
	}
	if !lm.keepAliveCalled {
		t.Fatal("expected LeaseManager.KeepAlive to be called")
	}
}

// ---------------------------------------------------------------------------
// Time-series dispatch tests
// ---------------------------------------------------------------------------

func TestKVDispatch_TSWrite(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", 1000, 42.5, map[string]string{"host": "a"}))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSWrite failed: %v", err)
	}
	if !ts.writeCalled {
		t.Fatal("expected TimeSeriesStore.WriteDataPoint to be called")
	}
}

func TestKVDispatch_TSWritePoint(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSWritePoint("cpu.usage", 1000, 42.5, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSWritePoint failed: %v", err)
	}
	if !ts.writeCalled {
		t.Fatal("expected TimeSeriesStore.WriteDataPoint to be called")
	}
}

func TestKVDispatch_TSDeleteSeries(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSDeleteSeries("cpu.usage", 0, 1000, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
	if !ts.deleteCalled {
		t.Fatal("expected TimeSeriesStore.DeleteSeries to be called")
	}
}

func TestKVDispatch_TSSetRetention(t *testing.T) {
	ts := &kvMockTimeSeriesStore{}
	k := newTestKVSM(WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu.usage", 86400000000000, 60000000000, 1))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
	if !ts.retentionCalled {
		t.Fatal("expected TimeSeriesStore.SetRetentionPolicy to be called")
	}
}

func TestKVDispatch_TSWrite_NotConfigured(t *testing.T) {
	k := newTestKVSM()

	cmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", 1000, 42.5, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("Update must not return error for unconfigured timeseries service: %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
}

// ---------------------------------------------------------------------------
// Batch with invalid opcode
// ---------------------------------------------------------------------------

func TestKVDispatch_Batch_InvalidOpcode(t *testing.T) {
	k := newTestKVSM()

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(0xFE, []byte("k"), []byte("v"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertOpcodeRefused(t, k.Update(entries, results), results[0])
}
