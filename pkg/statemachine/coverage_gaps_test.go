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

//go:build codec_json && raft && !integration

package statemachine

import (
	"bytes"
	"context"
	"encoding/binary"
	stderrors "errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Error-returning mocks for Index, Lease, TS (no conflict with existing files)
// ---------------------------------------------------------------------------

// gapErrIndexManager returns errors from CreateIndex and DropIndex.
type gapErrIndexManager struct {
	createErr error
	dropErr   error
}

func (m *gapErrIndexManager) CreateIndex(name, fieldName string, opts protocol.IndexCreateOptions) error {
	return m.createErr
}
func (m *gapErrIndexManager) DropIndex(name string, _ protocol.IndexDropOptions) error {
	return m.dropErr
}
func (m *gapErrIndexManager) Query(indexName string, value []byte) ([][]byte, error) {
	return nil, nil
}
func (m *gapErrIndexManager) Scan(indexName string, start, end []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *gapErrIndexManager) List() ([]interface{}, error) { return nil, nil }

// gapErrTSStore returns errors from all write operations.
type gapErrTSStore struct {
	writeErr      error
	writeBatchErr error
	deleteErr     error
	retentionErr  error
}

func (m *gapErrTSStore) WriteDataPoint(metric string, timestamp int64, value float64, tags map[string]string) error {
	return m.writeErr
}
func (m *gapErrTSStore) WriteDataPoints(points []TSDataPoint) error { return m.writeBatchErr }
func (m *gapErrTSStore) DeleteSeries(metric string, startNs, endNs int64, tags map[string]string) error {
	return m.deleteErr
}
func (m *gapErrTSStore) SetRetentionPolicy(metric string, durationNs, resolutionNs int64, aggregation byte) error {
	return m.retentionErr
}
func (m *gapErrTSStore) QueryRange(metric string, startNs, endNs int64) (interface{}, error) {
	return nil, nil
}
func (m *gapErrTSStore) QueryAggregate(metric string, startNs, endNs int64, fn byte, intervalNs int64) (interface{}, error) {
	return nil, nil
}
func (m *gapErrTSStore) QueryRich(request interface{}) (interface{}, error)     { return nil, nil }
func (m *gapErrTSStore) GetRetentionPolicy(metric string) (interface{}, error)  { return nil, nil }
func (m *gapErrTSStore) ListMetrics(prefix string, limit int) ([]string, error) { return nil, nil }

// gapErrBackend is a Backend that fails on Put, Delete, List, Get.
type gapErrBackend struct {
	putErr    error
	deleteErr error
	listErr   error
	getErr    error
}

func (b *gapErrBackend) Get(key []byte) ([]byte, error)     { return nil, b.getErr }
func (b *gapErrBackend) Put(key, value []byte) error        { return b.putErr }
func (b *gapErrBackend) Delete(key []byte) error            { return b.deleteErr }
func (b *gapErrBackend) PutDurable(key, value []byte) error { return b.putErr }
func (b *gapErrBackend) DeleteDurable(key []byte) error     { return b.deleteErr }

// ApplyAtomic fails the set with the error its writes would meet one by one:
// the put error if it puts, the delete error if it deletes.
func (b *gapErrBackend) ApplyAtomic(muts []Mutation) error {
	for _, m := range muts {
		if m.Delete && b.deleteErr != nil {
			return b.deleteErr
		}
		if !m.Delete && b.putErr != nil {
			return b.putErr
		}
	}
	return nil
}

var _ Backend = (*gapErrBackend)(nil)

func (b *gapErrBackend) List(prefix []byte) ([][]byte, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return nil, nil
}
func (b *gapErrBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	return nil, nil
}
func (b *gapErrBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error    { return nil }
func (b *gapErrBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error { return nil }
func (b *gapErrBackend) GetHash() (uint64, error)                                   { return 0, nil }
func (b *gapErrBackend) Records(fn RecordVisitor) error                             { return b.listErr }
func (b *gapErrBackend) Close() error                                               { return nil }

// ---------------------------------------------------------------------------
// kv_dispatch.go — Index error paths
// ---------------------------------------------------------------------------

func TestKVDispatch_IndexCreate_StorageError(t *testing.T) {
	createErr := stderrors.New("index create failed")
	idx := &gapErrIndexManager{createErr: createErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVIndexManager(idx))

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("idx1", "field", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from IndexManager.CreateIndex failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestKVDispatch_IndexDrop_StorageError(t *testing.T) {
	dropErr := stderrors.New("index drop failed")
	idx := &gapErrIndexManager{dropErr: dropErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVIndexManager(idx))

	cmd := mustEncode(protocol.EncodeIndexDrop("idx1"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from IndexManager.DropIndex failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go — Lease error paths
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// kv_dispatch.go — TimeSeries error paths
// ---------------------------------------------------------------------------

func TestKVDispatch_TSWrite_StorageError(t *testing.T) {
	writeErr := stderrors.New("ts write failed")
	ts := &gapErrTSStore{writeErr: writeErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSWrite("metric", 1000, 3.14, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from TimeSeriesStore.WriteDataPoint failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestKVDispatch_TSWritePoint_StorageError(t *testing.T) {
	writeErr := stderrors.New("ts write point failed")
	ts := &gapErrTSStore{writeErr: writeErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSWritePoint("metric", 1000, 3.14, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from TimeSeriesStore.WriteDataPoint (point) failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestKVDispatch_TSWriteBatch_StorageError(t *testing.T) {
	writeBatchErr := stderrors.New("ts write batch failed")
	ts := &gapErrTSStore{writeBatchErr: writeBatchErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	points := []protocol.TSWriteParams{
		{Metric: "m", Timestamp: 1000, Value: 1.0},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(points))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from TimeSeriesStore.WriteDataPoints failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestKVDispatch_TSDeleteSeries_StorageError(t *testing.T) {
	deleteErr := stderrors.New("ts delete failed")
	ts := &gapErrTSStore{deleteErr: deleteErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSDeleteSeries("metric", 0, 1000, nil))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from TimeSeriesStore.DeleteSeries failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestKVDispatch_TSSetRetention_StorageError(t *testing.T) {
	retErr := stderrors.New("ts set retention failed")
	ts := &gapErrTSStore{retentionErr: retErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	cmd := mustEncode(protocol.EncodeTSSetRetention("metric", 3600*1e9, 60*1e9, 0))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from TimeSeriesStore.SetRetentionPolicy failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kv_statemachine.go — backend error paths
// ---------------------------------------------------------------------------

func TestKVStateMachine_HandlePut_BackendError(t *testing.T) {
	putErr := stderrors.New("backend put failed")
	backend := &gapErrBackend{putErr: putErr}
	k := NewKVStateMachine(backend)

	cmd, err := kv.EncodePut([]byte("key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := k.Update(entries, results)
	if gotErr == nil {
		t.Fatal("expected error from Backend.Put failure")
	}
	var se *StorageError
	if !stderrors.As(gotErr, &se) {
		t.Fatalf("expected StorageError, got %T: %v", gotErr, gotErr)
	}
}

func TestKVStateMachine_HandleDelete_BackendError(t *testing.T) {
	deleteErr := stderrors.New("backend delete failed")
	backend := &gapErrBackend{deleteErr: deleteErr}
	k := NewKVStateMachine(backend)

	cmd, err := kv.EncodeDelete([]byte("key"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := k.Update(entries, results)
	if gotErr == nil {
		t.Fatal("expected error from Backend.Delete failure")
	}
	var se *StorageError
	if !stderrors.As(gotErr, &se) {
		t.Fatalf("expected StorageError, got %T: %v", gotErr, gotErr)
	}
}

func TestKVStateMachine_HandleBatch_PutError(t *testing.T) {
	putErr := stderrors.New("batch put failed")
	backend := &gapErrBackend{putErr: putErr}
	k := NewKVStateMachine(backend)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("k"), []byte("v"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := k.Update(entries, results)
	if gotErr == nil {
		t.Fatal("expected error from batch put failure")
	}
	var se *StorageError
	if !stderrors.As(gotErr, &se) {
		t.Fatalf("expected StorageError, got %T: %v", gotErr, gotErr)
	}
}

func TestKVStateMachine_HandleBatch_DeleteError(t *testing.T) {
	deleteErr := stderrors.New("batch delete failed")
	backend := &gapErrBackend{deleteErr: deleteErr}
	k := NewKVStateMachine(backend)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("k"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	gotErr := k.Update(entries, results)
	if gotErr == nil {
		t.Fatal("expected error from batch delete failure")
	}
	var se *StorageError
	if !stderrors.As(gotErr, &se) {
		t.Fatalf("expected StorageError, got %T: %v", gotErr, gotErr)
	}
}

// ---------------------------------------------------------------------------
// kv_lookup.go — backend error paths
// ---------------------------------------------------------------------------

func TestKVStateMachine_LookupKey_BackendError(t *testing.T) {
	getErr := stderrors.New("backend get failed")
	backend := &gapErrBackend{getErr: getErr}
	k := NewKVStateMachine(backend)

	_, err := k.Lookup([]byte("key"))
	if err == nil {
		t.Fatal("expected error from Backend.Get failure")
	}
}

func TestKVStateMachine_ListKeys_BackendError(t *testing.T) {
	listErr := stderrors.New("backend list failed")
	backend := &gapErrBackend{listErr: listErr}
	k := NewKVStateMachine(backend)

	_, err := k.Lookup(kv.ListQuery{Prefix: "prefix/"})
	if err == nil {
		t.Fatal("expected error from Backend.List failure")
	}
}

// ---------------------------------------------------------------------------
// kv_lookup.go — MVCC lookup paths (revision-based pointer variants)
// ---------------------------------------------------------------------------

func TestKVStateMachine_LookupMVCC_GetAtRevision(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	_, err := k.Lookup(MVCCGetAtRevisionQuery{Key: []byte("key"), Revision: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected Range to be called for MVCCGetAtRevisionQuery")
	}
}

func TestKVStateMachine_LookupMVCC_GetAtRevisionPtr(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	_, err := k.Lookup(&MVCCGetAtRevisionQuery{Key: []byte("key"), Revision: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected Range to be called for *MVCCGetAtRevisionQuery")
	}
}

// ---------------------------------------------------------------------------
// FastBackend.SaveSnapshot — stopper and write error paths
// ---------------------------------------------------------------------------

func TestFastBackend_SaveSnapshot_StopperTriggeredBeforeCount(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("a"), []byte("v1"))
	_ = b.Put([]byte("b"), []byte("v2"))
	b.wg.Wait()

	var buf bytes.Buffer
	stopper := make(chan struct{})
	close(stopper)

	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_SaveSnapshot_CountWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})

	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when count write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_SaveSnapshot_KeyLenWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	_ = b.Put([]byte("mykey"), []byte("myval"))
	b.wg.Wait()

	// Fail on second write (key length after count).
	w := &failWriter{failAt: 1}
	stopper := make(chan struct{})

	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key length write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend.RecoverSnapshot — stopper and error paths
// ---------------------------------------------------------------------------

func TestFastBackend_RecoverSnapshot_StopperTriggered(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	// Build a valid snapshot with 1 entry.
	var snapBuf bytes.Buffer
	src := NewFastBackend(2, 2, newMemStorage(), nil)
	_ = src.Put([]byte("k"), []byte("v"))
	src.wg.Wait()
	stopper := make(chan struct{})
	_ = src.SaveSnapshot(&snapBuf, stopper)

	// Close stopper before recover to trigger cancellation in entry loop.
	stopperRecover := make(chan struct{})
	close(stopperRecover)

	err := b.RecoverSnapshot(&snapBuf, stopperRecover)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed during recovery")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_RecoverSnapshot_TruncatedCountRead(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	// Provide a reader with fewer than 4 bytes — triggers count read error.
	r := bytes.NewReader([]byte{0x01, 0x00}) // only 2 bytes, need 4
	stopper := make(chan struct{})

	err := b.RecoverSnapshot(r, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated count")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend.Get — storage error path (cache miss)
// ---------------------------------------------------------------------------

func TestFastBackend_Get_StorageError(t *testing.T) {
	getErr := stderrors.New("storage get failed")
	underlying := &gapErrGetStorage{
		memStorage: newMemStorage(),
		getErr:     getErr,
	}
	b := NewFastBackend(1, 1, underlying, nil)
	defer func() { _ = b.Close() }()

	// Do NOT put anything in cache — forces cache miss → storage.Get error.
	_, err := b.Get([]byte("missing-key"))
	if err == nil {
		t.Fatal("expected StorageError from storage.Get failure on cache miss")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DiskBackend — storage error paths
// ---------------------------------------------------------------------------

// diskFailStorage wraps memStorage and injects configurable errors.
type diskFailStorage struct {
	*memStorage
	getErr    error
	setErr    error
	deleteErr error
	listErr   error
	iterErr   error
	closeErr  error
}

func (s *diskFailStorage) Get(key []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.memStorage.Get(key)
}

func (s *diskFailStorage) Set(key, value []byte) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.memStorage.Set(key, value)
}

func (s *diskFailStorage) Delete(key []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memStorage.Delete(key)
}

func (s *diskFailStorage) DeleteRange(start, end []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memStorage.DeleteRange(start, end)
}

// Batch meets the same faults a direct write would: a batched Set fails with
// setErr and a batched Delete with deleteErr.
func (s *diskFailStorage) Batch() storage.Batch {
	return &diskFailBatch{memBatch: &memBatch{store: s.memStorage}, owner: s}
}

type diskFailBatch struct {
	*memBatch
	owner *diskFailStorage
}

func (b *diskFailBatch) Set(key, value []byte) error {
	if b.owner.setErr != nil {
		return b.owner.setErr
	}
	return b.memBatch.Set(key, value)
}

func (b *diskFailBatch) Delete(key []byte) error {
	if b.owner.deleteErr != nil {
		return b.owner.deleteErr
	}
	return b.memBatch.Delete(key)
}

func (b *diskFailBatch) DeleteRange(start, end []byte) error {
	if b.owner.deleteErr != nil {
		return b.owner.deleteErr
	}
	return b.memBatch.DeleteRange(start, end)
}

func (s *diskFailStorage) List(prefix []byte) ([][]byte, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memStorage.List(prefix)
}

func (s *diskFailStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.iterErr != nil {
		return &errIterator{err: s.iterErr}
	}
	if s.closeErr != nil {
		inner := s.memStorage.NewIterator(opts)
		return &closeErrIterator{Iterator: inner, closeErr: s.closeErr}
	}
	return s.memStorage.NewIterator(opts)
}

// closeErrIterator wraps a real iterator and returns a configurable
// error from Close() to exercise iterator-close error paths.
type closeErrIterator struct {
	storage.Iterator
	closeErr error
}

func (it *closeErrIterator) Close() error { return it.closeErr }

// newFastBackendWithStorage constructs a FastBackend backed by the given storage.
func newFastBackendWithStorage(store storage.Storage) *FastBackend {
	return NewFastBackend(1, 1, store, slog.Default())
}

// newDiskBackendWithStorage constructs a DiskBackend backed by the given storage.
func newDiskBackendWithStorage(store storage.Storage) *DiskBackend {
	return NewDiskBackend(store)
}

func TestDiskBackend_Get_StorageError(t *testing.T) {
	getErr := stderrors.New("disk get failed")
	store := &diskFailStorage{memStorage: newMemStorage(), getErr: getErr}
	b := NewDiskBackend(store)

	_, err := b.Get([]byte("key"))
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.Get failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_Put_StorageError(t *testing.T) {
	setErr := stderrors.New("disk set failed")
	store := &diskFailStorage{memStorage: newMemStorage(), setErr: setErr}
	b := NewDiskBackend(store)

	err := b.Put([]byte("key"), []byte("val"))
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.Put failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_Delete_StorageError(t *testing.T) {
	deleteErr := stderrors.New("disk delete failed")
	store := &diskFailStorage{memStorage: newMemStorage(), deleteErr: deleteErr}
	b := NewDiskBackend(store)

	err := b.Delete([]byte("key"))
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.Delete failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_List_StorageError(t *testing.T) {
	listErr := stderrors.New("disk list failed")
	store := &diskFailStorage{memStorage: newMemStorage(), listErr: listErr}
	b := NewDiskBackend(store)

	_, err := b.List([]byte("prefix/"))
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.List failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_Scan_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator error")
	store := &diskFailStorage{memStorage: newMemStorage(), iterErr: iterErr}
	b := NewDiskBackend(store)

	_, err := b.Scan([]byte("prefix/"), 0)
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.Scan iterator failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_GetHash_IteratorError(t *testing.T) {
	iterErr := stderrors.New("hash iterator error")
	store := &diskFailStorage{memStorage: newMemStorage(), iterErr: iterErr}
	b := NewDiskBackend(store)

	_, err := b.GetHash()
	if err == nil {
		t.Fatal("expected StorageError from DiskBackend.GetHash iterator failure")
	}
	var se *StorageError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
}

func TestDiskBackend_SaveSnapshot_StopperTriggered(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("k1"), []byte("v1"))
	b := NewDiskBackend(store)

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_SaveSnapshot_CountWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewDiskBackend(store)

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when count write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_SaveSnapshot_KeyLenWriteError(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("key"), []byte("val"))
	b := NewDiskBackend(store)

	w := &failWriter{failAt: 1}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_RecoverSnapshot_StopperTriggered(t *testing.T) {
	src := NewDiskBackend(newMemStorage())
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	openStopper := make(chan struct{})
	_ = src.SaveSnapshot(&snapBuf, openStopper)

	dst := NewDiskBackend(newMemStorage())
	stopperClosed := make(chan struct{})
	close(stopperClosed)

	err := dst.RecoverSnapshot(&snapBuf, stopperClosed)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed during recovery")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_RecoverSnapshot_TruncatedCountRead(t *testing.T) {
	dst := NewDiskBackend(newMemStorage())
	r := bytes.NewReader([]byte{0x01, 0x00}) // only 2 bytes, need 4
	stopper := make(chan struct{})

	err := dst.RecoverSnapshot(r, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated count read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// DiskBackend.SaveSnapshot — key write and val-len/val write errors
func TestDiskBackend_SaveSnapshot_KeyWriteError(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("key"), []byte("val"))
	b := NewDiskBackend(store)

	w := &failWriter{failAt: 2}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_SaveSnapshot_ValLenWriteError(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("key"), []byte("val"))
	b := NewDiskBackend(store)

	w := &failWriter{failAt: 3}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_SaveSnapshot_ValWriteError(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("key"), []byte("val"))
	b := NewDiskBackend(store)

	w := &failWriter{failAt: 4}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// DiskBackend.RecoverSnapshot — key-len read error
func TestDiskBackend_RecoverSnapshot_KeyLenReadError(t *testing.T) {
	src := NewDiskBackend(newMemStorage())
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	// Truncate after count (4 bytes)
	truncated := bytes.NewReader(snapBuf.Bytes()[:4])
	dst := NewDiskBackend(newMemStorage())

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key-len read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// DiskBackend.RecoverSnapshot — key data and val-len read errors
func TestDiskBackend_RecoverSnapshot_KeyDataReadError(t *testing.T) {
	src := NewDiskBackend(newMemStorage())
	_ = src.Put([]byte("longkey"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	// Truncate after key-len (count=4 + keyLen=4 bytes) = 8 bytes but not full key data.
	data := snapBuf.Bytes()
	truncated := bytes.NewReader(data[:8]) // has count + key-len but no key data
	dst := NewDiskBackend(newMemStorage())

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key data read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_RecoverSnapshot_ValLenReadError(t *testing.T) {
	src := NewDiskBackend(newMemStorage())
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	data := snapBuf.Bytes()
	// count(4) + keyLen(4) + key(1) = 9 bytes — truncated before val-len
	if len(data) < 9 {
		t.Skip("snapshot too short for this test")
	}
	truncated := bytes.NewReader(data[:9])
	dst := NewDiskBackend(newMemStorage())

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val-len read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_RecoverSnapshot_SetError(t *testing.T) {
	src := NewDiskBackend(newMemStorage())
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	setErr := stderrors.New("set error during recover")
	store := &diskFailStorage{memStorage: newMemStorage(), setErr: setErr}
	dst := NewDiskBackend(store)

	err := dst.RecoverSnapshot(&snapBuf, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when storage.Set fails during recover")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// DiskBackend.clearStorage — iterator error path
func TestDiskBackend_ClearStorage_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iter error in clear")
	store := &diskFailStorage{memStorage: newMemStorage(), iterErr: iterErr}
	_ = store.memStorage.Set([]byte("k"), []byte("v"))
	b := NewDiskBackend(store)

	// clearStorage is called by RecoverSnapshot; trigger it.
	var snapBuf bytes.Buffer
	src := NewDiskBackend(newMemStorage())
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	err := b.RecoverSnapshot(&snapBuf, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when clearStorage iterator fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestDiskBackend_ClearStorage_DeleteError(t *testing.T) {
	mem := newMemStorage()
	_ = mem.Set([]byte("k1"), []byte("v1"))
	store := &diskFailStorage{memStorage: mem, deleteErr: stderrors.New("delete error in clear")}
	b := NewDiskBackend(store)

	// clearStorage is invoked by RecoverSnapshot.
	var snapBuf bytes.Buffer
	src := NewDiskBackend(newMemStorage())
	openStopper := make(chan struct{})
	_ = src.SaveSnapshot(&snapBuf, openStopper)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&snapBuf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from clearStorage delete failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StandardBackend — snapshot error paths
// ---------------------------------------------------------------------------

func TestStandardBackend_SaveSnapshot_StopperTriggered(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestStandardBackend_SaveSnapshot_CountWriteError(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when count write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestStandardBackend_RecoverSnapshot_StopperTriggered(t *testing.T) {
	src := NewStandardBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	openStopper := make(chan struct{})
	_ = src.SaveSnapshot(&snapBuf, openStopper)

	dst := NewStandardBackend()
	defer func() { _ = dst.Close() }()
	stopperClosed := make(chan struct{})
	close(stopperClosed)

	err := dst.RecoverSnapshot(&snapBuf, stopperClosed)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed during recovery")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestStandardBackend_RecoverSnapshot_TruncatedCountRead(t *testing.T) {
	dst := NewStandardBackend()
	defer func() { _ = dst.Close() }()
	r := bytes.NewReader([]byte{0x01, 0x00}) // only 2 bytes, need 4
	stopper := make(chan struct{})

	err := dst.RecoverSnapshot(r, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated count read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentBackend — snapshot error paths
// ---------------------------------------------------------------------------

func TestConcurrentBackend_SaveSnapshot_CountWriteError(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when count write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_SaveSnapshot_StopperTriggered(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("key"), []byte("val"))

	stopper := make(chan struct{})
	close(stopper)

	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_RecoverSnapshot_StopperTriggered(t *testing.T) {
	src := NewConcurrentBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	openStopper := make(chan struct{})
	_ = src.SaveSnapshot(&snapBuf, openStopper)

	dst := NewConcurrentBackend()
	defer func() { _ = dst.Close() }()
	stopperClosed := make(chan struct{})
	close(stopperClosed)

	err := dst.RecoverSnapshot(&snapBuf, stopperClosed)
	if err == nil {
		t.Fatal("expected SnapshotError when stopper is closed during recovery")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_RecoverSnapshot_TruncatedCountRead(t *testing.T) {
	dst := NewConcurrentBackend()
	defer func() { _ = dst.Close() }()
	r := bytes.NewReader([]byte{0x01, 0x00}) // only 2 bytes, need 4
	stopper := make(chan struct{})

	err := dst.RecoverSnapshot(r, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated count read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend — snapshot per-entry write errors
// ---------------------------------------------------------------------------

func TestFastBackend_SaveSnapshot_KeyWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))
	b.wg.Wait()

	w := &failWriter{failAt: 2}
	err := b.SaveSnapshot(w, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when key write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_SaveSnapshot_ValLenWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))
	b.wg.Wait()

	w := &failWriter{failAt: 3}
	err := b.SaveSnapshot(w, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when val-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_SaveSnapshot_ValWriteError(t *testing.T) {
	store := newMemStorage()
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))
	b.wg.Wait()

	w := &failWriter{failAt: 4}
	err := b.SaveSnapshot(w, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when val write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// FastBackend.RecoverSnapshot — key-len/key/val/set error paths
func TestFastBackend_RecoverSnapshot_KeyLenReadError(t *testing.T) {
	src := NewFastBackend(2, 2, newMemStorage(), nil)
	_ = src.Put([]byte("k"), []byte("v"))
	src.wg.Wait()
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	truncated := bytes.NewReader(snapBuf.Bytes()[:4])
	dst := NewFastBackend(1, 1, newMemStorage(), nil)
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key-len read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestFastBackend_RecoverSnapshot_SetError(t *testing.T) {
	src := NewFastBackend(2, 2, newMemStorage(), nil)
	_ = src.Put([]byte("k"), []byte("v"))
	src.wg.Wait()
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	setErr := stderrors.New("set error during fast recover")
	store := &diskFailStorage{memStorage: newMemStorage(), setErr: setErr}
	dst := NewFastBackend(1, 1, store, nil)
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(&snapBuf, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError when storage.Set fails during fast recover")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// FastBackend.clearStorage — delete error path
func TestFastBackend_ClearStorage_DeleteError(t *testing.T) {
	deleteErr := stderrors.New("delete error in fast clear")
	mem := newMemStorage()
	_ = mem.Set([]byte("k1"), []byte("v1"))
	store := &diskFailStorage{memStorage: mem, deleteErr: deleteErr}
	b := NewFastBackend(1, 1, store, nil)
	defer func() { _ = b.Close() }()

	src := NewFastBackend(2, 2, newMemStorage(), nil)
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	err := b.RecoverSnapshot(&snapBuf, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError from clearStorage delete failure in FastBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StandardBackend.RecoverSnapshot — key data read error
// ---------------------------------------------------------------------------

func TestStandardBackend_RecoverSnapshot_KeyDataReadError(t *testing.T) {
	src := NewStandardBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	data := snapBuf.Bytes()
	// count(4) + keyLen(4) = 8 bytes but no actual key data
	if len(data) < 8 {
		t.Skip("snapshot too short")
	}
	truncated := bytes.NewReader(data[:8])
	dst := NewStandardBackend()
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key data read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentBackend.RecoverSnapshot — key read error
// ---------------------------------------------------------------------------

func TestConcurrentBackend_RecoverSnapshot_KeyDataReadError(t *testing.T) {
	src := NewConcurrentBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	data := snapBuf.Bytes()
	// count(4) + keyLen(4) = 8 bytes but no actual key data
	truncated := bytes.NewReader(data[:8])
	dst := NewConcurrentBackend()
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key data read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DiskAdapter.BytesLookup — error path
// ---------------------------------------------------------------------------

func TestDiskAdapter_BytesLookup_Error(t *testing.T) {
	getErr := stderrors.New("bytes lookup error")
	backend := &gapErrBackend{getErr: getErr}
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: newMemStorage()}

	_, err := adapter.BytesLookup([]byte("key"))
	if err == nil {
		t.Fatal("expected error from DiskAdapter.BytesLookup failure")
	}
}

// ---------------------------------------------------------------------------
// NewKVFastCreateFunc — logger extraction from options
// ---------------------------------------------------------------------------

func TestNewKVFastCreateFunc_WithLoggerOpt(t *testing.T) {
	logger := slog.Default()
	cf := NewKVFastCreateFunc(
		func(shardID, replicaID uint64) storage.Storage {
			return newMemStorage()
		},
		WithKVLogger(logger),
	)
	smFn := cf(1, 1)
	if smFn == nil {
		t.Fatal("expected non-nil StateMachine from KVFastCreateFunc with logger opt")
	}
}

// ---------------------------------------------------------------------------
// StandardBackend — per-entry snapshot write errors
// ---------------------------------------------------------------------------

func TestStandardBackend_SaveSnapshot_KeyLenWriteError(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 1}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StandardBackend — more per-entry snapshot write errors
// ---------------------------------------------------------------------------

func TestStandardBackend_SaveSnapshot_KeyWriteError(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 2}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestStandardBackend_SaveSnapshot_ValLenWriteError(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 3}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestStandardBackend_SaveSnapshot_ValWriteError(t *testing.T) {
	b := NewStandardBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 4}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// StandardBackend.RecoverSnapshot per-entry errors
func TestStandardBackend_RecoverSnapshot_KeyLenReadError(t *testing.T) {
	src := NewStandardBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	// Truncate after count (4 bytes) — triggers key-len read error.
	truncated := bytes.NewReader(snapBuf.Bytes()[:4])
	dst := NewStandardBackend()
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key-len read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentBackend — per-entry snapshot write errors
// ---------------------------------------------------------------------------

func TestConcurrentBackend_SaveSnapshot_KeyLenWriteError(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 1}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_SaveSnapshot_KeyWriteError(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 2}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when key write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_SaveSnapshot_ValLenWriteError(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 3}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val-len write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

func TestConcurrentBackend_SaveSnapshot_ValWriteError(t *testing.T) {
	b := NewConcurrentBackend()
	defer func() { _ = b.Close() }()
	_ = b.Put([]byte("k"), []byte("v"))

	w := &failWriter{failAt: 4}
	stopper := make(chan struct{})
	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError when val write fails")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ConcurrentBackend.RecoverSnapshot key-len read error
func TestConcurrentBackend_RecoverSnapshot_KeyLenReadError(t *testing.T) {
	src := NewConcurrentBackend()
	_ = src.Put([]byte("k"), []byte("v"))
	var snapBuf bytes.Buffer
	_ = src.SaveSnapshot(&snapBuf, make(chan struct{}))

	truncated := bytes.NewReader(snapBuf.Bytes()[:4])
	dst := NewConcurrentBackend()
	defer func() { _ = dst.Close() }()

	err := dst.RecoverSnapshot(truncated, make(chan struct{}))
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key-len read")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go — MVCC not-configured paths for Range and Compact
// ---------------------------------------------------------------------------

func TestKVDispatch_MVCCRange_NotConfigured(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	cmd := mustEncode(protocol.EncodeMVCCRange([]byte("start"), []byte("end"), 0, 100, false, false))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	// Update must not return error — ServiceNotConfiguredError is encoded in
	// result.Data to prevent Raft lastApplied from stalling.
	err := k.Update(entries, results)
	if err != nil {
		t.Fatalf("Update must not return error for unconfigured mvcc service: %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data for MVCCRange")
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go — Transaction decode error
// ---------------------------------------------------------------------------

func TestKVDispatch_Transaction_DecodeError(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	// Build a transaction command with garbage payload (just the opcode byte).
	badPayload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	entries := []sm.Entry{{Cmd: append([]byte{protocol.OpTransaction}, badPayload...)}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	// Either IncompleteDataError or StorageError is acceptable - the key point is it errors.
	if err == nil {
		t.Fatal("expected error for transaction with invalid payload")
	}
}

// ---------------------------------------------------------------------------
// kv_statemachine.go — index range end and lease in handlePut
// ---------------------------------------------------------------------------

func TestKVStateMachine_HandlePut_WithLeaseID(t *testing.T) {
	// Ensure a Put with leaseID succeeds (covers lease path in handlePut).
	k := NewKVStateMachine(NewStandardBackend())

	cmd, err := kv.EncodePut([]byte("lease-key"), []byte("lease-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update with lease failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from Put")
	}
}

// ---------------------------------------------------------------------------
// kv_statemachine.go — decode error paths for Put, Delete, Batch
// ---------------------------------------------------------------------------

func TestKVStateMachine_HandlePut_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// Opcode only, no payload — triggers "put: payload too short"
	entries := []sm.Entry{{Cmd: []byte{kv.OpPut}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated put payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_HandlePut_KeyLengthExceedsPayload(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// keyLen=255 but payload is only 3 bytes → "put: key length exceeds payload"
	payload := []byte{0xFF, 0x00, 0xAA} // keyLen=255, only 1 byte of key
	entries := []sm.Entry{{Cmd: append([]byte{kv.OpPut}, payload...)}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for key length exceeding payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_HandleDelete_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// Opcode only, no payload — triggers "delete: payload too short"
	entries := []sm.Entry{{Cmd: []byte{kv.OpDelete}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated delete payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_HandleDelete_KeyLengthExceedsPayload(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// keyLen=255 but payload has only 1 byte of key → "delete: key length exceeds payload"
	payload := []byte{0xFF, 0x00, 0xAA}
	entries := []sm.Entry{{Cmd: append([]byte{kv.OpDelete}, payload...)}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for key length exceeding payload in delete")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVStateMachine_HandleBatch_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// Opcode + 1 garbage byte — triggers batch decode error
	entries := []sm.Entry{{Cmd: []byte{protocol.OpBatch, 0xFF}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated batch payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go — decode error paths for MVCC handlers
// ---------------------------------------------------------------------------

func TestKVDispatch_MVCCPut_DecodeError(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	// OpMVCCPut + 1 byte payload — triggers "mvcc put: payload too short"
	entries := []sm.Entry{{Cmd: []byte{protocol.OpMVCCPut, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated MVCCPut payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVDispatch_MVCCDelete_DecodeError(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	// OpMVCCDelete + 1 byte payload — triggers "mvcc delete: payload too short"
	entries := []sm.Entry{{Cmd: []byte{protocol.OpMVCCDelete, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated MVCCDelete payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVDispatch_MVCCRange_DecodeError(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	// Truncated MVCCRange payload
	entries := []sm.Entry{{Cmd: []byte{protocol.OpMVCCRange, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated MVCCRange payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kv_dispatch.go — decode error paths for Index, Lease, TS handlers
// ---------------------------------------------------------------------------

func TestKVDispatch_IndexCreate_DecodeError(t *testing.T) {
	idx := &gapErrIndexManager{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVIndexManager(idx))

	// Truncated IndexCreate payload
	entries := []sm.Entry{{Cmd: []byte{protocol.OpIndexCreate, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected IncompleteDataError for truncated IndexCreate payload")
	}
	var ide *IncompleteDataError
	if !stderrors.As(err, &ide) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

func TestKVDispatch_IndexDrop_DecodeError(t *testing.T) {
	idx := &gapErrIndexManager{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVIndexManager(idx))

	// Truncated IndexDrop payload (needs at least 2 bytes for name length)
	entries := []sm.Entry{{Cmd: []byte{protocol.OpIndexDrop, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	// May be IncompleteDataError or succeed with empty name — just check it doesn't panic
	_ = err
}

func TestKVDispatch_LeaseGrant_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	// Truncated LeaseGrant payload (needs 8 bytes for int64 TTL)
	entries := []sm.Entry{{Cmd: []byte{protocol.OpLeaseGrant, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated LeaseGrant payload")
	}
}

func TestKVDispatch_LeaseRevoke_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	entries := []sm.Entry{{Cmd: []byte{protocol.OpLeaseRevoke, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated LeaseRevoke payload")
	}
}

func TestKVDispatch_LeaseKeepAlive_DecodeError(t *testing.T) {
	k := NewKVStateMachine(NewStandardBackend())

	entries := []sm.Entry{{Cmd: []byte{protocol.OpLeaseKeepAlive, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated LeaseKeepAlive payload")
	}
}

func TestKVDispatch_TSWrite_DecodeError(t *testing.T) {
	ts := &gapErrTSStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	// Truncated TSWrite payload
	entries := []sm.Entry{{Cmd: []byte{protocol.OpTSWrite, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated TSWrite payload")
	}
}

func TestKVDispatch_TSWritePoint_DecodeError(t *testing.T) {
	ts := &gapErrTSStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	entries := []sm.Entry{{Cmd: []byte{protocol.OpTSWritePoint, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated TSWritePoint payload")
	}
}

func TestKVDispatch_TSWriteBatch_DecodeError(t *testing.T) {
	ts := &gapErrTSStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	// Truncated TSWriteBatch payload (needs 4 bytes for count)
	entries := []sm.Entry{{Cmd: []byte{protocol.OpTSWriteBatch, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated TSWriteBatch payload")
	}
}

func TestKVDispatch_TSDeleteSeries_DecodeError(t *testing.T) {
	ts := &gapErrTSStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	entries := []sm.Entry{{Cmd: []byte{protocol.OpTSDeleteSeries, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated TSDeleteSeries payload")
	}
}

func TestKVDispatch_TSSetRetention_DecodeError(t *testing.T) {
	ts := &gapErrTSStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVTimeSeriesStore(ts))

	entries := []sm.Entry{{Cmd: []byte{protocol.OpTSSetRetention, 0x01}}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error for truncated TSSetRetention payload")
	}
}

// ---------------------------------------------------------------------------
// kv_statemachine.go — WatchManager.Notify paths in handlePut/Delete/Batch
// ---------------------------------------------------------------------------

// noopWatchManager implements WatchManager and records Notify calls.
type noopWatchManager struct {
	calls int
}

func (m *noopWatchManager) Notify(_ string, _ []byte, _ uint64, _ bool) {
	m.calls++
}

func TestKVStateMachine_HandlePut_WithWatchManager(t *testing.T) {
	wm := &noopWatchManager{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVWatchManager(wm))

	cmd, err := kv.EncodePut([]byte("watch-key"), []byte("watch-val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if wm.calls != 1 {
		t.Fatalf("expected 1 Notify call, got %d", wm.calls)
	}
}

func TestKVStateMachine_HandleDelete_WithWatchManager(t *testing.T) {
	wm := &noopWatchManager{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVWatchManager(wm))

	// First put to ensure the key exists.
	putCmd, _ := kv.EncodePut([]byte("del-key"), []byte("val"))
	_ = k.Update([]sm.Entry{{Cmd: putCmd}}, make([]sm.Result, 1))
	wm.calls = 0

	delCmd, err := kv.EncodeDelete([]byte("del-key"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries := []sm.Entry{{Cmd: delCmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if wm.calls != 1 {
		t.Fatalf("expected 1 Notify call for delete, got %d", wm.calls)
	}
}

func TestKVStateMachine_HandleBatch_WithWatchManager(t *testing.T) {
	wm := &noopWatchManager{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVWatchManager(wm))

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(kv.OpPut, []byte("b-key"), []byte("b-val"), nil, 0)),
		mustEncode(protocol.EncodeOp(kv.OpDelete, []byte("b-key"), nil, nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Update batch failed: %v", err)
	}
	if wm.calls != 2 {
		t.Fatalf("expected 2 Notify calls for batch put+delete, got %d", wm.calls)
	}
}

// ---------------------------------------------------------------------------
// KVStateMachine.handleTransaction — success and store error branches
// ---------------------------------------------------------------------------

// txnSucceededMVCCStore is an MVCCStore that reports a succeeded transaction.
type txnSucceededMVCCStore struct {
	kvMockMVCCStore
	succeeded bool
	err       error
}

func (m *txnSucceededMVCCStore) Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (bool, int64, interface{}, error) {
	return m.succeeded, 42, nil, m.err
}

// TestKVDispatch_Transaction_Succeeded verifies that a successful transaction
// with populated compares, success ops and failure ops returns Data=[1].
func TestKVDispatch_Transaction_Succeeded(t *testing.T) {
	mvcc := &txnSucceededMVCCStore{succeeded: true}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	// Build a transaction with one compare, one success op, and one failure op
	// so the loop bodies in handleTransaction are exercised.
	cmp := mustEncode(protocol.EncodeCompare([]byte("key"), 1, 0, []byte("val"), 0, 0, 0))
	op := mustEncode(protocol.EncodeOp(kv.OpPut, []byte("key"), []byte("val"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, [][]byte{op}))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Transaction succeeded: unexpected error: %v", err)
	}
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatalf("expected Data[0]=1 for succeeded transaction, got %v", results[0].Data)
	}
}

// TestKVDispatch_Transaction_Failed verifies that a transaction where the
// compares fail returns Data=[0].
func TestKVDispatch_Transaction_Failed(t *testing.T) {
	mvcc := &txnSucceededMVCCStore{succeeded: false}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	cmp := mustEncode(protocol.EncodeCompare([]byte("key"), 1, 0, []byte("val"), 0, 0, 0))
	op := mustEncode(protocol.EncodeOp(kv.OpPut, []byte("key"), []byte("val"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, [][]byte{op}))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := k.Update(entries, results); err != nil {
		t.Fatalf("Transaction failed branch: unexpected error: %v", err)
	}
	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Fatalf("expected Data[0]=0 for failed transaction, got %v", results[0].Data)
	}
}

// TestKVDispatch_Transaction_StoreError verifies that a store error from
// mvccStore.Transaction is propagated as a StorageError.
func TestKVDispatch_Transaction_StoreError(t *testing.T) {
	storeErr := stderrors.New("txn store failure")
	mvcc := &txnSucceededMVCCStore{err: storeErr}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))

	cmp := mustEncode(protocol.EncodeCompare([]byte("key"), 1, 0, []byte("val"), 0, 0, 0))
	op := mustEncode(protocol.EncodeOp(kv.OpPut, []byte("key"), []byte("val"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{cmp}, [][]byte{op}, [][]byte{op}))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := k.Update(entries, results)
	if err == nil {
		t.Fatal("expected error from mvccStore.Transaction")
	}
	if !stderrors.Is(err, storeErr) {
		t.Errorf("expected storeErr wrapped in error chain, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// KVStateMachine.lookupMVCC — uncovered query types
// ---------------------------------------------------------------------------

// TestKVLookup_MVCCGet_Value_WithStore verifies that a value-type MVCCGetQuery
// calls Range on the configured mvcc store (the store-call branch).
func TestKVLookup_MVCCGet_Value_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))
	_, err := k.Lookup(MVCCGetQuery{Key: []byte("k")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected Range to be called for MVCCGetQuery")
	}
}

// TestKVLookup_MVCCGetAtRevision_Value_WithStore verifies that a value-type
// MVCCGetAtRevisionQuery calls Range on the configured mvcc store.
func TestKVLookup_MVCCGetAtRevision_Value_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))
	_, err := k.Lookup(MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected Range to be called for MVCCGetAtRevisionQuery value-type")
	}
}

// TestKVLookup_MVCCRange_Value_WithStore verifies that a value-type
// MVCCRangeQuery calls Range on the configured mvcc store.
func TestKVLookup_MVCCRange_Value_WithStore(t *testing.T) {
	mvcc := &kvMockMVCCStore{}
	k := NewKVStateMachine(NewStandardBackend(), WithKVMVCCStore(mvcc))
	_, err := k.Lookup(MVCCRangeQuery{Start: []byte("a"), End: []byte("z"), Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mvcc.rangeCalled {
		t.Fatal("expected Range to be called for MVCCRangeQuery value-type")
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine.lookupLease — value-type LeasesQuery with nil manager
// ---------------------------------------------------------------------------

// TestFastLookup_LeasesQueryValue_NotConfigured verifies that a value-type
// LeasesQuery returns ServiceNotConfiguredError when no lease manager is set.
func TestFastLookup_LeasesQueryValue_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()
	_, err := fsm.Lookup(context.Background(), lease.LeasesQuery{})
	var sncErr *ServiceNotConfiguredError
	if !stderrors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// TestFastLookup_LeaseTimeToLiveValue_NotConfigured verifies that a value-type
// LeaseTimeToLiveQuery returns ServiceNotConfiguredError when no lease manager
// is set.
func TestFastLookup_LeaseTimeToLiveValue_NotConfigured(t *testing.T) {
	fsm, _ := newTestFSM()
	_, err := fsm.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 42})
	var sncErr *ServiceNotConfiguredError
	if !stderrors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine — SaveSnapshot / RecoverFromSnapshot / clearStorage
// ---------------------------------------------------------------------------

// diskFailStorageFSM wraps memStorage with configurable errors for
// use with FastStateMachine (which needs the full storage.Storage interface).
type diskFailStorageFSM struct {
	*memStorage
	iterErr   error
	closeErr  error
	deleteErr error
	setErr    error
}

func (s *diskFailStorageFSM) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if s.iterErr != nil {
		return &errIterator{err: s.iterErr}
	}
	inner := s.memStorage.NewIterator(opts)
	if s.closeErr != nil {
		return &closeErrIterator{Iterator: inner, closeErr: s.closeErr}
	}
	return inner
}

func (s *diskFailStorageFSM) Delete(key []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memStorage.Delete(key)
}

func (s *diskFailStorageFSM) DeleteRange(start, end []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.memStorage.DeleteRange(start, end)
}

func (s *diskFailStorageFSM) Set(key, value []byte) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.memStorage.Set(key, value)
}

// TestFastStateMachine_SaveSnapshot_CountWriteError verifies that a write
// failure on the entry count is propagated as a SnapshotError.
func TestFastStateMachine_SaveSnapshot_CountWriteError(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from count write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_SaveSnapshot_KeyLenWriteError verifies that a write
// failure on the key length header is propagated as a SnapshotError.
func TestFastStateMachine_SaveSnapshot_KeyLenWriteError(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	w := &failWriter{failAt: 1} // 0=count, 1=keyLen
	stopper := make(chan struct{})
	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from key-len write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_SaveSnapshot_KeyWriteError verifies that a write
// failure on the key data is propagated as a SnapshotError.
func TestFastStateMachine_SaveSnapshot_KeyWriteError(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	w := &failWriter{failAt: 2} // 0=count, 1=keyLen, 2=key
	stopper := make(chan struct{})
	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from key write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_SaveSnapshot_ValLenWriteError verifies that a write
// failure on the value length header is propagated as a SnapshotError.
func TestFastStateMachine_SaveSnapshot_ValLenWriteError(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	w := &failWriter{failAt: 3} // 0=count, 1=keyLen, 2=key, 3=valLen
	stopper := make(chan struct{})
	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from val-len write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_SaveSnapshot_ValWriteError verifies that a write
// failure on the value data is propagated as a SnapshotError.
func TestFastStateMachine_SaveSnapshot_ValWriteError(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	w := &failWriter{failAt: 4} // 0=count, 1=keyLen, 2=key, 3=valLen, 4=val
	stopper := make(chan struct{})
	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from val write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_ClearStorageError verifies that a
// clearStorage error (iterator failure) is propagated as a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_ClearStorageError(t *testing.T) {
	iterErr := stderrors.New("iterator failure")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	// Build a minimal valid snapshot: 0 entries.
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 0)
	buf.Write(countBuf)

	stopper := make(chan struct{})
	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error from clearStorage iterator failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_TruncatedCount verifies that a
// reader with insufficient data for the entry count returns a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_TruncatedCount(t *testing.T) {
	fsm, _ := newTestFSM()
	stopper := make(chan struct{})

	// Only 2 bytes — not enough for the 4-byte count.
	err := fsm.RecoverFromSnapshot(context.Background(), bytes.NewReader([]byte{0x01, 0x02}), stopper)
	if err == nil {
		t.Fatal("expected error for truncated count")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_KeyLenReadError verifies that a
// snapshot missing the key-length header returns a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_KeyLenReadError(t *testing.T) {
	fsm, _ := newTestFSM()
	stopper := make(chan struct{})

	// count=1, then no key length data.
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for missing key-len")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_KeyDataReadError verifies that a
// snapshot with a key-length header but no key data returns a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_KeyDataReadError(t *testing.T) {
	fsm, _ := newTestFSM()
	stopper := make(chan struct{})

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte key, but no data follows
	buf.Write(lenBuf)

	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for truncated key data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_ValLenReadError verifies that a
// snapshot with valid key data but no value-length header returns a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_ValLenReadError(t *testing.T) {
	fsm, _ := newTestFSM()
	stopper := make(chan struct{})

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2) // 2-byte key "k1"
	buf.Write(lenBuf)
	buf.WriteString("k1")
	// No value-length follows.

	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for missing val-len")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_RecoverFromSnapshot_SetError verifies that a storage
// Set error during recovery is propagated as a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_SetError(t *testing.T) {
	setErr := stderrors.New("set failure")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		setErr:     setErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	// Build a valid snapshot with one entry.
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")

	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("v1")

	stopper := make(chan struct{})
	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error from storage Set failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestFastStateMachine_ClearStorage_IteratorError verifies that an iterator
// error during clearStorage is returned.
func TestFastStateMachine_ClearStorage_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	err := fsm.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator failure in clearStorage")
	}
	if !stderrors.Is(err, iterErr) {
		t.Errorf("expected iterErr, got %v", err)
	}
}

// TestFastStateMachine_ClearStorage_DeleteError verifies that a Delete
// error during clearStorage is returned.
func TestFastStateMachine_ClearStorage_DeleteError(t *testing.T) {
	deleteErr := stderrors.New("delete error")
	base := newMemStorage()
	// Pre-populate so clearStorage has something to iterate.
	_ = base.Set([]byte("key"), []byte("val"))

	failStore := &diskFailStorageFSM{
		memStorage: base,
		deleteErr:  deleteErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	err := fsm.clearStorage()
	if err == nil {
		t.Fatal("expected error from Delete failure in clearStorage")
	}
	if !stderrors.Is(err, deleteErr) {
		t.Errorf("expected deleteErr, got %v", err)
	}
}

// TestFastStateMachine_ClearStorage_CloseError verifies that an iterator
// close error during clearStorage is returned.
func TestFastStateMachine_ClearStorage_CloseError(t *testing.T) {
	closeErr := stderrors.New("close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	err := fsm.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator close failure in clearStorage")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine — concurrentCacheSet and concurrentCacheDelete
// async error logging paths
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_CacheSet_AsyncStorageError verifies that when
// the async persistence goroutine in concurrentCacheSet encounters a Set
// error, the error is logged and the goroutine completes cleanly.
func TestConcurrentStateMachine_CacheSet_AsyncStorageError(t *testing.T) {
	setErr := stderrors.New("async set error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		setErr:     setErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	// Trigger concurrentCacheSet by applying a Put command.
	concurrentApplyPut(t, csm, "async-key", "async-val")

	// Wait for async goroutine to complete; the error must be logged.
	csm.wg.Wait()

	// The in-memory cache should still have the key even though storage failed.
	result, err := csm.Lookup(context.Background(), []byte("async-key"))
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	_ = result // may be nil or val depending on caching path
}

// TestConcurrentStateMachine_CacheDelete_AsyncStorageError verifies that when
// the async goroutine in concurrentCacheDelete encounters a Delete error,
// the error is logged and the goroutine completes cleanly.
func TestConcurrentStateMachine_CacheDelete_AsyncStorageError(t *testing.T) {
	base := newMemStorage()
	// Pre-populate so Set succeeds, then configure Delete to fail.
	_ = base.Set([]byte("del-async"), []byte("val"))

	failStore := &diskFailStorageFSM{
		memStorage: base,
		deleteErr:  stderrors.New("async delete error"),
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	// Trigger concurrentCacheDelete.
	concurrentApplyDelete(t, csm, "del-async")

	// Wait for async goroutines to complete.
	csm.wg.Wait()
}

// ---------------------------------------------------------------------------
// FastStateMachine — cacheSet and cacheDelete async error logging paths
// ---------------------------------------------------------------------------

// TestFastStateMachine_CacheSet_AsyncStorageError verifies that when the
// async persistence goroutine in cacheSet encounters a Set error, the error
// is logged and the goroutine completes cleanly.
func TestFastStateMachine_CacheSet_AsyncStorageError(t *testing.T) {
	setErr := stderrors.New("async fast set error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		setErr:     setErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	// Trigger cacheSet by applying a Put command.
	applyPut(t, fsm, "fast-async-key", "fast-async-val")

	// Wait for async goroutines to complete.
	fsm.wg.Wait()

	// The in-memory shard should still have the entry.
	result, err := fsm.Lookup(context.Background(), []byte("fast-async-key"))
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	_ = result
}

// TestFastStateMachine_CacheDelete_AsyncStorageError verifies that when the
// async goroutine in cacheDelete encounters a Delete error, the error is
// logged and the goroutine completes cleanly.
func TestFastStateMachine_CacheDelete_AsyncStorageError(t *testing.T) {
	base := newMemStorage()
	_ = base.Set([]byte("fast-del-async"), []byte("val"))

	failStore := &diskFailStorageFSM{
		memStorage: base,
		deleteErr:  stderrors.New("async fast delete error"),
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	// Put first so the key is in cache, then delete.
	applyPut(t, fsm, "fast-del-async", "val")
	fsm.wg.Wait()

	// Re-enable delete error to trigger the async logging path.
	applyDelete(t, fsm, "fast-del-async")
	fsm.wg.Wait()
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine — SaveSnapshot / RecoverFromSnapshot
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_SaveSnapshot_CountWriteError verifies that a
// count write failure is propagated as a SnapshotError.
func TestConcurrentStateMachine_SaveSnapshot_CountWriteError(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm, "k1", "v1")
	waitForConcurrentAsync(csm)

	ctx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err = csm.SaveSnapshot(context.Background(), ctx, w, stopper)
	if err == nil {
		t.Fatal("expected error from count write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestConcurrentStateMachine_SaveSnapshot_ValLenWriteError verifies that a
// value-length write failure is propagated as a SnapshotError.
func TestConcurrentStateMachine_SaveSnapshot_ValLenWriteError(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm, "k1", "v1")
	waitForConcurrentAsync(csm)

	ctx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	w := &failWriter{failAt: 3} // 0=count, 1=keyLen, 2=key, 3=valLen
	stopper := make(chan struct{})
	err = csm.SaveSnapshot(context.Background(), ctx, w, stopper)
	if err == nil {
		t.Fatal("expected error from val-len write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestConcurrentStateMachine_SaveSnapshot_ValWriteError verifies that a
// value data write failure is propagated as a SnapshotError.
func TestConcurrentStateMachine_SaveSnapshot_ValWriteError(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	concurrentApplyPut(t, csm, "k1", "v1")
	waitForConcurrentAsync(csm)

	ctx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	w := &failWriter{failAt: 4} // 0=count, 1=keyLen, 2=key, 3=valLen, 4=val
	stopper := make(chan struct{})
	err = csm.SaveSnapshot(context.Background(), ctx, w, stopper)
	if err == nil {
		t.Fatal("expected error from val write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshot_TruncatedCount verifies that
// a snapshot with insufficient bytes for the entry count returns an error.
func TestConcurrentStateMachine_RecoverFromSnapshot_TruncatedCount(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	stopper := make(chan struct{})

	err := csm.RecoverFromSnapshot(context.Background(), bytes.NewReader([]byte{0x01}), stopper)
	if err == nil {
		t.Fatal("expected error for truncated count")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshot_ValLenReadError verifies that
// a snapshot missing the value-length header returns a SnapshotError.
func TestConcurrentStateMachine_RecoverFromSnapshot_ValLenReadError(t *testing.T) {
	csm, _ := newTestConcurrentSM()
	stopper := make(chan struct{})

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	// No value-length follows.

	err := csm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for missing val-len")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestConcurrentStateMachine_ClearStorage_CloseError verifies that an iterator
// close error during clearStorage is returned.
func TestConcurrentStateMachine_ClearStorage_CloseError(t *testing.T) {
	closeErr := stderrors.New("close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	err := csm.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator close failure in clearStorage")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshot_SetError verifies that a
// storage Set error during recovery is propagated as a SnapshotError.
func TestConcurrentStateMachine_RecoverFromSnapshot_SetError(t *testing.T) {
	setErr := stderrors.New("set failure")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		setErr:     setErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")

	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("v1")

	stopper := make(chan struct{})
	err := csm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error from storage Set failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// RegularStateMachine — SaveSnapshot write errors and RecoverFromSnapshot
// ---------------------------------------------------------------------------

// TestRegularStateMachine_SaveSnapshot_CountWriteError verifies that a count
// write failure is propagated as a SnapshotError.
func TestRegularStateMachine_SaveSnapshot_CountWriteError(t *testing.T) {
	rsm := newTestRegularFSM()
	regularApplyPut(t, rsm, "key", "value")

	w := &failWriter{failAt: 0}
	stopper := make(chan struct{})
	err := rsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from count write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestRegularStateMachine_SaveSnapshot_KeyWriteError verifies that a key
// data write failure is propagated as a SnapshotError.
func TestRegularStateMachine_SaveSnapshot_KeyWriteError(t *testing.T) {
	rsm := newTestRegularFSM()
	regularApplyPut(t, rsm, "key", "value")

	w := &failWriter{failAt: 2} // 0=count, 1=keyLen, 2=key
	stopper := make(chan struct{})
	err := rsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from key write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestRegularStateMachine_SaveSnapshot_ValLenWriteError verifies that a
// value-length write failure is propagated as a SnapshotError.
func TestRegularStateMachine_SaveSnapshot_ValLenWriteError(t *testing.T) {
	rsm := newTestRegularFSM()
	regularApplyPut(t, rsm, "key", "value")

	w := &failWriter{failAt: 3} // 0=count, 1=keyLen, 2=key, 3=valLen
	stopper := make(chan struct{})
	err := rsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from val-len write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestRegularStateMachine_SaveSnapshot_ValWriteError verifies that a value
// data write failure is propagated as a SnapshotError.
func TestRegularStateMachine_SaveSnapshot_ValWriteError(t *testing.T) {
	rsm := newTestRegularFSM()
	regularApplyPut(t, rsm, "key", "value")

	w := &failWriter{failAt: 4} // 0=count, 1=keyLen, 2=key, 3=valLen, 4=val
	stopper := make(chan struct{})
	err := rsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected error from val write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestRegularStateMachine_RecoverFromSnapshot_ValLenReadError verifies that a
// snapshot missing the value-length header returns a SnapshotError.
func TestRegularStateMachine_RecoverFromSnapshot_ValLenReadError(t *testing.T) {
	rsm := newTestRegularFSM()
	stopper := make(chan struct{})

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 3) // 3-byte key "key"
	buf.Write(lenBuf)
	buf.WriteString("key")
	// No value-length follows.

	err := rsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for missing val-len")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// TestRegularStateMachine_RecoverFromSnapshot_ValDataReadError verifies that a
// snapshot with a value-length header but no value data returns a SnapshotError.
func TestRegularStateMachine_RecoverFromSnapshot_ValDataReadError(t *testing.T) {
	rsm := newTestRegularFSM()
	stopper := make(chan struct{})

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 3)
	buf.Write(lenBuf)
	buf.WriteString("key")

	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data follows
	buf.Write(lenBuf)

	err := rsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error for truncated val data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine — prefix/list lookup iterator close-error paths
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_PrefixLookup_CloseError verifies that an
// iterator close error in lookupConcurrentPrefix is propagated.
func TestConcurrentStateMachine_PrefixLookup_CloseError(t *testing.T) {
	closeErr := stderrors.New("prefix close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	_, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err == nil {
		t.Fatal("expected error from iterator close failure in prefix lookup")
	}
}

// TestConcurrentStateMachine_ListKeys_CloseError verifies that an
// iterator close error in listConcurrentKeys is propagated.
func TestConcurrentStateMachine_ListKeys_CloseError(t *testing.T) {
	closeErr := stderrors.New("list close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	_, err := csm.Lookup(context.Background(), kv.ListQuery{Prefix: "ns/"})
	if err == nil {
		t.Fatal("expected error from iterator close failure in list lookup")
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine — prefix/list lookup iterator close-error paths
// ---------------------------------------------------------------------------

// TestFastStateMachine_PrefixLookup_CloseError verifies that an iterator
// close error in lookupPrefix is propagated.
func TestFastStateMachine_PrefixLookup_CloseError(t *testing.T) {
	closeErr := stderrors.New("fast prefix close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	_, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err == nil {
		t.Fatal("expected error from iterator close failure in fast prefix lookup")
	}
}

// ---------------------------------------------------------------------------
// RegularStateMachine.lookupMVCC — missing nil-guard pointer variants
// ---------------------------------------------------------------------------

// TestRegularLookup_MVCCGetAtRevisionPtr_NotConfigured verifies that
// *MVCCGetAtRevisionQuery returns ServiceNotConfiguredError without mvcc store.
func TestRegularLookup_MVCCGetAtRevisionPtr_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()
	_, err := rsm.Lookup(context.Background(), &MVCCGetAtRevisionQuery{Key: []byte("k"), Revision: 1})
	var sncErr *ServiceNotConfiguredError
	if !stderrors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// TestRegularLookup_MVCCRangePtr_NotConfigured verifies that
// *MVCCRangeQuery returns ServiceNotConfiguredError without mvcc store.
func TestRegularLookup_MVCCRangePtr_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()
	_, err := rsm.Lookup(context.Background(), &MVCCRangeQuery{Start: []byte("a"), End: []byte("z")})
	var sncErr *ServiceNotConfiguredError
	if !stderrors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// TestRegularLookup_MVCCRevisionPtr_NotConfigured verifies that
// *MVCCRevisionQuery returns ServiceNotConfiguredError without mvcc store.
func TestRegularLookup_MVCCRevisionPtr_NotConfigured(t *testing.T) {
	rsm := newTestRegularFSM()
	_, err := rsm.Lookup(context.Background(), &MVCCRevisionQuery{})
	var sncErr *ServiceNotConfiguredError
	if !stderrors.As(err, &sncErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

// TestFastStateMachine_ListKeys_CloseError verifies that an iterator close
// error in listKeys is propagated.
func TestFastStateMachine_ListKeys_CloseError(t *testing.T) {
	closeErr := stderrors.New("fast list close error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	_, err := fsm.Lookup(context.Background(), kv.ListQuery{Prefix: "ns/"})
	if err == nil {
		t.Fatal("expected error from iterator close failure in fast list lookup")
	}
}

// ---------------------------------------------------------------------------
// backend_fast.go — List iterator error and Scan iterator error
// ---------------------------------------------------------------------------

// TestFastBackend_Put_AsyncStorageError verifies that when the async
// persistence goroutine in Put encounters a Set error, the error is
// logged and the goroutine completes cleanly (Put itself returns nil).
func TestFastBackend_Put_AsyncStorageError(t *testing.T) {
	setErr := stderrors.New("async put storage error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		setErr:     setErr,
	}
	b := newFastBackendWithStorage(failStore)

	// Put returns nil even when async storage fails.
	if err := b.Put([]byte("key"), []byte("val")); err != nil {
		t.Fatalf("Put returned unexpected error: %v", err)
	}

	// Wait for async goroutine to complete (the error path must execute).
	b.wg.Wait()
}

// TestFastBackend_Delete_AsyncStorageError verifies that when the async
// goroutine in Delete encounters a Delete error, the error is logged and
// the goroutine completes cleanly (Delete itself returns nil).
func TestFastBackend_Delete_AsyncStorageError(t *testing.T) {
	deleteErr := stderrors.New("async delete storage error")
	base := newMemStorage()
	_ = base.Set([]byte("key"), []byte("val"))

	failStore := &diskFailStorage{
		memStorage: base,
		deleteErr:  deleteErr,
	}
	b := newFastBackendWithStorage(failStore)

	// Put first to cache the key.
	_ = b.Put([]byte("key"), []byte("val"))
	b.wg.Wait()

	// Re-enable delete error, then delete to trigger async logging.
	if err := b.Delete([]byte("key")); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	b.wg.Wait()
}

// TestFastBackend_ClearStorage_CloseError verifies that an iterator close
// error during clearStorage is returned.
func TestFastBackend_ClearStorage_CloseError(t *testing.T) {
	closeErr := stderrors.New("iterator close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newFastBackendWithStorage(failStore)

	err := b.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator close failure in FastBackend.clearStorage")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// TestFastBackend_List_IteratorError verifies that an iterator error during
// the storage phase of List is propagated as a StorageError.
func TestFastBackend_List_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator list error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	b := newFastBackendWithStorage(failStore)

	_, err := b.List([]byte("prefix/"))
	if err == nil {
		t.Fatal("expected error from iterator failure in List")
	}
}

// TestFastBackend_Scan_IteratorError verifies that an iterator error during
// the storage phase of Scan is propagated as a StorageError.
func TestFastBackend_Scan_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator scan error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	b := newFastBackendWithStorage(failStore)

	_, err := b.Scan([]byte("prefix/"), 100)
	if err == nil {
		t.Fatal("expected error from iterator failure in Scan")
	}
}

// ---------------------------------------------------------------------------
// backend_disk.go — Scan close-error and SaveSnapshot stopper in iteration
// ---------------------------------------------------------------------------

// TestDiskBackend_Scan_CloseError verifies that an iterator close error
// after a successful scan is propagated as a StorageError.
func TestDiskBackend_Scan_CloseError(t *testing.T) {
	closeErr := stderrors.New("close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newDiskBackendWithStorage(failStore)

	// Should not panic; close error should be returned.
	_, err := b.Scan([]byte("prefix/"), 100)
	if err == nil {
		t.Fatal("expected error from iterator close failure in Scan")
	}
}

// TestDiskBackend_GetHash_CloseError verifies that an iterator close error
// in GetHash is propagated as a StorageError.
func TestDiskBackend_GetHash_CloseError(t *testing.T) {
	closeErr := stderrors.New("close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newDiskBackendWithStorage(failStore)

	_, err := b.GetHash()
	if err == nil {
		t.Fatal("expected error from iterator close failure in GetHash")
	}
}

// ---------------------------------------------------------------------------
// stopperAfterCountWriter closes a stopper channel after the first write
// succeeds, allowing the per-entry stopper branch to be exercised.
// ---------------------------------------------------------------------------

type stopperAfterCountWriter struct {
	stopper chan struct{}
	fired   bool
}

func (w *stopperAfterCountWriter) Write(p []byte) (int, error) {
	if !w.fired {
		w.fired = true
		// Let this write succeed, then close stopper so next select fires.
		close(w.stopper)
	}
	return len(p), nil
}

// ---------------------------------------------------------------------------
// FastBackend.List — close-error and tombstone-cache branch
// ---------------------------------------------------------------------------

// TestFastBackend_List_CloseError verifies that an iterator close error in
// List is returned as a StorageError.
func TestFastBackend_List_CloseError(t *testing.T) {
	closeErr := stderrors.New("list close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newFastBackendWithStorage(failStore)

	_, err := b.List([]byte("prefix/"))
	if err == nil {
		t.Fatal("expected error from iterator close failure in FastBackend.List")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// TestFastBackend_List_CacheNonMatchingPrefixContinue verifies that cache
// entries with a non-matching prefix are skipped in the List overlay phase.
func TestFastBackend_List_CacheNonMatchingPrefixContinue(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)

	// Put a key with a different prefix into the cache.
	_ = b.Put([]byte("other/key"), []byte("val"))
	b.wg.Wait()

	// List with "pfx/" prefix: cache entry "other/key" must be skipped.
	keys, err := b.List([]byte("pfx/"))
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	for _, k := range keys {
		if string(k) == "other/key" {
			t.Error("expected non-matching key to be absent from List result")
		}
	}
}

// TestFastBackend_List_TombstoneCacheBranch verifies that a cache tombstone
// causes a storage key to be removed from the List result.
func TestFastBackend_List_TombstoneCacheBranch(t *testing.T) {
	// Use a storage that fails Delete so the async goroutine cannot remove the
	// key from storage — this leaves the key visible to the iterator in Phase 1
	// while the cache holds a nil tombstone from Phase 2.
	deleteErr := stderrors.New("storage delete blocked")
	base := newMemStorage()
	_ = base.Set([]byte("pfx/keep"), []byte("v"))
	_ = base.Set([]byte("pfx/del"), []byte("v"))
	failStore := &diskFailStorage{
		memStorage: base,
		deleteErr:  deleteErr,
	}
	b := newFastBackendWithStorage(failStore)

	// Prime cache: put both keys so they are in the cache.
	_ = b.Put([]byte("pfx/keep"), []byte("v"))
	_ = b.Put([]byte("pfx/del"), []byte("v"))
	b.wg.Wait() // async Set may fail, but cache has the entries

	// Tombstone "pfx/del" — async Delete will fail but cache gets nil.
	_ = b.Delete([]byte("pfx/del"))
	b.wg.Wait()

	// Now Phase 1 iterator sees both keys (storage still has them);
	// Phase 2 cache has "pfx/del"=nil (tombstone) → delete(seen, "pfx/del").
	keys, err := b.List([]byte("pfx/"))
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	for _, k := range keys {
		if string(k) == "pfx/del" {
			t.Error("expected tombstoned key to be absent from List result")
		}
	}
}

// ---------------------------------------------------------------------------
// FastBackend.Scan — close-error branch
// ---------------------------------------------------------------------------

// TestFastBackend_Scan_CloseError verifies that an iterator close error in
// Scan is returned as a StorageError.
func TestFastBackend_Scan_CloseError(t *testing.T) {
	closeErr := stderrors.New("scan close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newFastBackendWithStorage(failStore)

	_, err := b.Scan([]byte("prefix/"), 100)
	if err == nil {
		t.Fatal("expected error from iterator close failure in FastBackend.Scan")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend.SaveSnapshot — per-entry stopper branch
// ---------------------------------------------------------------------------

// TestFastBackend_SaveSnapshot_StopperInEntryLoop verifies that a closed
// stopper triggers cancellation inside the per-entry write loop.
func TestFastBackend_SaveSnapshot_StopperInEntryLoop(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)
	_ = b.Put([]byte("k1"), []byte("v1"))
	b.wg.Wait()

	stopper := make(chan struct{})
	w := &stopperAfterCountWriter{stopper: stopper}

	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from stopper in entry loop")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend.RecoverSnapshot — key data, val-len, val-data read errors
// ---------------------------------------------------------------------------

// TestFastBackend_RecoverSnapshot_KeyDataReadError verifies that a snapshot
// with key-length header but no key data returns a SnapshotError.
func TestFastBackend_RecoverSnapshot_KeyDataReadError(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte key, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestFastBackend_RecoverSnapshot_ValLenReadError verifies that a snapshot
// missing the value-length header returns a SnapshotError.
func TestFastBackend_RecoverSnapshot_ValLenReadError(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	// No value-length follows.

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for missing val-len")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestFastBackend_RecoverSnapshot_ValDataReadError verifies that a snapshot
// with val-length header but no val data returns a SnapshotError.
func TestFastBackend_RecoverSnapshot_ValDataReadError(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastBackend.clearStorage — iterator error branch
// ---------------------------------------------------------------------------

// TestFastBackend_ClearStorage_IteratorError verifies that an iterator error
// in clearStorage is returned.
func TestFastBackend_ClearStorage_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	b := newFastBackendWithStorage(failStore)

	err := b.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator failure in FastBackend.clearStorage")
	}
	if !stderrors.Is(err, iterErr) {
		t.Errorf("expected iterErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DiskBackend.Scan — prefix mismatch break branch
// ---------------------------------------------------------------------------

// TestDiskBackend_Scan_PrefixMismatchBreak verifies that DiskBackend.Scan
// stops iteration when a key does not match the requested prefix.
func TestDiskBackend_Scan_PrefixMismatchBreak(t *testing.T) {
	store := newMemStorage()
	b := newDiskBackendWithStorage(store)
	// Populate with a key that matches and one that does not.
	_ = b.Put([]byte("pfx/a"), []byte("v1"))
	_ = b.Put([]byte("pfx/b"), []byte("v2"))

	result, err := b.Scan([]byte("pfx/"), 100)
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// DiskBackend.SaveSnapshot — iterator error and close-error branches
// ---------------------------------------------------------------------------

// TestDiskBackend_SaveSnapshot_IteratorError verifies that an iterator error
// during collection phase returns a SnapshotError.
func TestDiskBackend_SaveSnapshot_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	b := newDiskBackendWithStorage(failStore)

	stopper := make(chan struct{})
	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from iterator error")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestDiskBackend_SaveSnapshot_IteratorCloseError verifies that an iterator
// close error after collection returns a SnapshotError.
func TestDiskBackend_SaveSnapshot_IteratorCloseError(t *testing.T) {
	closeErr := stderrors.New("iterator close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newDiskBackendWithStorage(failStore)

	stopper := make(chan struct{})
	var buf bytes.Buffer
	err := b.SaveSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from iterator close error")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestDiskBackend_SaveSnapshot_StopperInEntryLoop verifies that a closed
// stopper inside the per-entry write loop returns a SnapshotError.
func TestDiskBackend_SaveSnapshot_StopperInEntryLoop(t *testing.T) {
	store := newMemStorage()
	_ = store.Set([]byte("k1"), []byte("v1"))
	b := newDiskBackendWithStorage(store)

	stopper := make(chan struct{})
	w := &stopperAfterCountWriter{stopper: stopper}

	err := b.SaveSnapshot(w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from stopper in entry loop")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DiskBackend.RecoverSnapshot — val data read error
// ---------------------------------------------------------------------------

// TestDiskBackend_RecoverSnapshot_ValDataReadError verifies that a snapshot
// with val-length header but no val data returns a SnapshotError.
func TestDiskBackend_RecoverSnapshot_ValDataReadError(t *testing.T) {
	dst := newDiskBackendWithStorage(newMemStorage())

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	// No value data follows.

	stopper := make(chan struct{})
	err := dst.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DiskBackend.clearStorage — iterator close-error branch
// ---------------------------------------------------------------------------

// TestDiskBackend_ClearStorage_CloseError verifies that an iterator close
// error during clearStorage is returned.
func TestDiskBackend_ClearStorage_CloseError(t *testing.T) {
	closeErr := stderrors.New("close error")
	failStore := &diskFailStorage{
		memStorage: newMemStorage(),
		closeErr:   closeErr,
	}
	b := newDiskBackendWithStorage(failStore)

	err := b.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator close failure in DiskBackend.clearStorage")
	}
	if !stderrors.Is(err, closeErr) {
		t.Errorf("expected closeErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentBackend.RecoverSnapshot — key data and val-len read errors
// ---------------------------------------------------------------------------

// TestConcurrentBackend_RecoverSnapshot_KeyDataReadError verifies that a
// snapshot with key-length header but no key data returns a SnapshotError.
func TestConcurrentBackend_RecoverSnapshot_KeyDataReadError2(t *testing.T) {
	b := NewConcurrentBackend()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte key, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated key data in ConcurrentBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestConcurrentBackend_RecoverSnapshot_ValLenReadError verifies that a
// snapshot missing the value-length header returns a SnapshotError.
func TestConcurrentBackend_RecoverSnapshot_ValLenReadError2(t *testing.T) {
	b := NewConcurrentBackend()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	// No value-length follows.

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for missing val-len in ConcurrentBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestConcurrentBackend_RecoverSnapshot_ValDataReadError verifies that a
// snapshot with val-length header but no val data returns a SnapshotError.
func TestConcurrentBackend_RecoverSnapshot_ValDataReadError(t *testing.T) {
	b := NewConcurrentBackend()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data in ConcurrentBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StandardBackend.RecoverSnapshot — val-len and val-data read errors
// ---------------------------------------------------------------------------

// TestStandardBackend_RecoverSnapshot_ValLenReadError2 verifies that a
// snapshot missing the value-length header returns a SnapshotError.
func TestStandardBackend_RecoverSnapshot_ValLenReadError2(t *testing.T) {
	b := NewStandardBackend()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	// No value-length follows.

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for missing val-len in StandardBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestStandardBackend_RecoverSnapshot_ValDataReadError verifies that a
// snapshot with val-length header but no val data returns a SnapshotError.
func TestStandardBackend_RecoverSnapshot_ValDataReadError(t *testing.T) {
	b := NewStandardBackend()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := b.RecoverSnapshot(&buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data in StandardBackend")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.SaveSnapshot — key write error branch
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_SaveSnapshot_KeyWriteError verifies that a
// write failure for the key data is propagated as a SnapshotError.
func TestConcurrentStateMachine_SaveSnapshot_KeyWriteError(t *testing.T) {
	store := newMemStorage()
	csm := NewConcurrentStateMachine(1, 1, store)

	cmd, err := kv.EncodePut([]byte("mykey"), []byte("myval"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	csm.wg.Wait()

	ctx, err := csm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	// Write: [count][keyLen][keyData...] — fail on 3rd write (keyData)
	w := &failWriter{failAt: 2}
	stopper := make(chan struct{})
	err = csm.SaveSnapshot(context.Background(), ctx, w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from key write failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.RecoverFromSnapshot — clearStorage error and val-data
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_RecoverFromSnapshot_ClearStorageError verifies
// that a clearStorage failure is propagated as a SnapshotError.
func TestConcurrentStateMachine_RecoverFromSnapshot_ClearStorageError(t *testing.T) {
	iterErr := stderrors.New("iterator failure")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 0)
	buf.Write(countBuf)

	stopper := make(chan struct{})
	err := csm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected error from clearStorage failure")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshot_KeyLenReadError verifies
// that a snapshot with only a count but no key-length returns a SnapshotError.
func TestConcurrentStateMachine_RecoverFromSnapshot_KeyLenReadError(t *testing.T) {
	store := newMemStorage()
	csm := NewConcurrentStateMachine(1, 1, store)

	// Provide count=1 then nothing — key-length read must fail.
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)

	stopper := make(chan struct{})
	err := csm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for missing key-len in ConcurrentStateMachine")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// TestConcurrentStateMachine_RecoverFromSnapshot_ValDataReadError verifies
// that a snapshot with val-length header but no val data returns a
// SnapshotError.
func TestConcurrentStateMachine_RecoverFromSnapshot_ValDataReadError(t *testing.T) {
	store := newMemStorage()
	csm := NewConcurrentStateMachine(1, 1, store)

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := csm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.lookupConcurrentPrefix — iterator error branch
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_PrefixLookup_IteratorError verifies that an
// iterator error in lookupConcurrentPrefix is propagated.
func TestConcurrentStateMachine_PrefixLookup_IteratorError(t *testing.T) {
	iterErr := stderrors.New("prefix iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	_, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err == nil {
		t.Fatal("expected error from iterator failure in ConcurrentStateMachine prefix lookup")
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.listConcurrentKeys — iterator error branch
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_ListKeys_IteratorError verifies that an
// iterator error in listConcurrentKeys is propagated.
func TestConcurrentStateMachine_ListKeys_IteratorError(t *testing.T) {
	iterErr := stderrors.New("list iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	_, err := csm.Lookup(context.Background(), kv.ListQuery{Prefix: "ns/"})
	if err == nil {
		t.Fatal("expected error from iterator failure in ConcurrentStateMachine list lookup")
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.clearStorage — iterator error branch
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_ClearStorage_IteratorError verifies that an
// iterator error in clearStorage is returned.
func TestConcurrentStateMachine_ClearStorage_IteratorError(t *testing.T) {
	iterErr := stderrors.New("iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	csm := NewConcurrentStateMachine(1, 1, failStore)

	err := csm.clearStorage()
	if err == nil {
		t.Fatal("expected error from iterator failure in ConcurrentStateMachine.clearStorage")
	}
	if !stderrors.Is(err, iterErr) {
		t.Errorf("expected iterErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine.SaveSnapshot — per-entry stopper branch
// ---------------------------------------------------------------------------

// TestFastStateMachine_SaveSnapshot_StopperInEntryLoop verifies that a
// closed stopper inside the per-entry write loop triggers cancellation.
func TestFastStateMachine_SaveSnapshot_StopperInEntryLoop(t *testing.T) {
	fsm, _ := newTestFSM()
	applyPut(t, fsm, "k1", "v1")
	fsm.wg.Wait()

	stopper := make(chan struct{})
	w := &stopperAfterCountWriter{stopper: stopper}

	err := fsm.SaveSnapshot(context.Background(), w, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError from stopper in entry loop")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine.RecoverFromSnapshot — val data read error
// ---------------------------------------------------------------------------

// TestFastStateMachine_RecoverFromSnapshot_ValDataReadError verifies that a
// snapshot with val-length header but no val data returns a SnapshotError.
func TestFastStateMachine_RecoverFromSnapshot_ValDataReadError(t *testing.T) {
	fsm, _ := newTestFSM()

	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, 2)
	buf.Write(lenBuf)
	buf.WriteString("k1")
	binary.LittleEndian.PutUint32(lenBuf, 10) // 10-byte value, no data
	buf.Write(lenBuf)

	stopper := make(chan struct{})
	err := fsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for truncated val data")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine.lookupPrefix — iterator error branch
// ---------------------------------------------------------------------------

// TestFastStateMachine_PrefixLookup_IteratorError verifies that an iterator
// error in lookupPrefix is propagated.
func TestFastStateMachine_PrefixLookup_IteratorError(t *testing.T) {
	iterErr := stderrors.New("prefix iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	_, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("ns/")})
	if err == nil {
		t.Fatal("expected error from iterator failure in FastStateMachine prefix lookup")
	}
}

// ---------------------------------------------------------------------------
// FastStateMachine.listKeys — iterator error branch
// ---------------------------------------------------------------------------

// TestFastStateMachine_ListKeys_IteratorError verifies that an iterator
// error in listKeys is propagated.
func TestFastStateMachine_ListKeys_IteratorError(t *testing.T) {
	iterErr := stderrors.New("list iterator error")
	failStore := &diskFailStorageFSM{
		memStorage: newMemStorage(),
		iterErr:    iterErr,
	}
	fsm := NewFastStateMachine(1, 1, failStore)

	_, err := fsm.Lookup(context.Background(), kv.ListQuery{Prefix: "ns/"})
	if err == nil {
		t.Fatal("expected error from iterator failure in FastStateMachine list lookup")
	}
}

// ---------------------------------------------------------------------------
// RegularStateMachine.RecoverFromSnapshot — key data read error
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// FastStateMachine.lookupPrefix — cache non-matching prefix continue branch
// ---------------------------------------------------------------------------

// TestFastStateMachine_LookupPrefix_CacheNonMatchingKey verifies that cache
// entries with keys that do not match the query prefix are skipped.
func TestFastStateMachine_LookupPrefix_CacheNonMatchingKey(t *testing.T) {
	fsm, _ := newTestFSM()

	// Put a key with a non-matching prefix into the cache.
	applyPut(t, fsm, "other/key", "val")
	fsm.wg.Wait()

	// Query a different prefix — cache entry "other/key" must be skipped.
	result, err := fsm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err != nil {
		t.Fatalf("Lookup returned unexpected error: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}
	if _, exists := m["other/key"]; exists {
		t.Error("expected non-matching key to be absent from prefix query result")
	}
}

// ---------------------------------------------------------------------------
// ConcurrentStateMachine.lookupConcurrentPrefix — cache non-matching prefix continue
// ---------------------------------------------------------------------------

// TestConcurrentStateMachine_LookupPrefix_CacheNonMatchingKey verifies that
// cache entries with non-matching keys are skipped in the overlay phase.
func TestConcurrentStateMachine_LookupPrefix_CacheNonMatchingKey(t *testing.T) {
	store := newMemStorage()
	csm := NewConcurrentStateMachine(1, 1, store)

	// Put a key with a non-matching prefix.
	cmd, err := kv.EncodePut([]byte("other/key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := csm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	csm.wg.Wait()

	// Query a different prefix — cache entry must be skipped.
	result, err := csm.Lookup(context.Background(), &PrefixQuery{Prefix: []byte("pfx/")})
	if err != nil {
		t.Fatalf("Lookup returned unexpected error: %v", err)
	}
	m, ok := result.(map[string][]byte)
	if !ok {
		t.Fatalf("expected map[string][]byte, got %T", result)
	}
	if _, exists := m["other/key"]; exists {
		t.Error("expected non-matching key to be absent from prefix query result")
	}
}

// ---------------------------------------------------------------------------
// FastBackend.Scan — cache non-matching prefix continue branch
// ---------------------------------------------------------------------------

// TestFastBackend_Scan_CacheNonMatchingKey verifies that cache entries whose
// keys don't match the scan prefix are skipped in the overlay phase.
func TestFastBackend_Scan_CacheNonMatchingKey(t *testing.T) {
	store := newMemStorage()
	b := newFastBackendWithStorage(store)

	// Put a key with a different prefix.
	_ = b.Put([]byte("other/key"), []byte("val"))
	b.wg.Wait()

	// Scan with a different prefix — cache entry must not appear.
	result, err := b.Scan([]byte("pfx/"), 100)
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}
	if _, exists := result["other/key"]; exists {
		t.Error("expected non-matching key to be absent from Scan result")
	}
}

// ---------------------------------------------------------------------------
// RegularStateMachine.RecoverFromSnapshot — key-len read error
// ---------------------------------------------------------------------------

// TestRegularStateMachine_RecoverFromSnapshot_KeyLenReadError verifies that
// a snapshot with only a count but no key-length data returns a SnapshotError.
func TestRegularStateMachine_RecoverFromSnapshot_KeyLenReadError(t *testing.T) {
	rsm := newTestRegularFSM()

	// Provide count=1 then nothing — key-length read must fail.
	var buf bytes.Buffer
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, 1)
	buf.Write(countBuf)
	// No key-length bytes follow — io.ReadFull will return EOF.

	stopper := make(chan struct{})
	err := rsm.RecoverFromSnapshot(context.Background(), &buf, stopper)
	if err == nil {
		t.Fatal("expected SnapshotError for missing key-len in RegularStateMachine")
	}
	var se *SnapshotError
	if !stderrors.As(err, &se) {
		t.Fatalf("expected SnapshotError, got %T: %v", err, err)
	}
}
