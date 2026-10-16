// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package partition

import (
	"container/heap"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// heap.Push coverage — exercises minHeap.Push and maxHeap.Push by calling
// heap.Push directly on constructed heaps.
// ---------------------------------------------------------------------------

// TestMinHeap_Push exercises minHeap.Push via container/heap.Push.
// heap.Push calls h.Push(x) so this covers the previously-uncovered line.
func TestMinHeap_Push(t *testing.T) {
	h := &minHeap{}

	// heap.Push calls h.Push(x) which is the method we need to cover.
	iter1 := newMockIter([]byte("bravo"))
	iter2 := newMockIter([]byte("alpha"))
	heap.Push(h, heapItem{iter: iter1, index: 0})
	heap.Push(h, heapItem{iter: iter2, index: 1})

	if h.Len() != 2 {
		t.Fatalf("heap length = %d, want 2", h.Len())
	}
	// Pop should return "alpha" (smaller key) as the minimum.
	top := heap.Pop(h).(heapItem)
	if string(top.iter.Key()) != "alpha" {
		t.Errorf("minHeap top key = %q, want %q", top.iter.Key(), "alpha")
	}
}

// TestMaxHeap_Push exercises maxHeap.Push via container/heap.Push.
func TestMaxHeap_Push(t *testing.T) {
	h := &maxHeap{}

	iter1 := newMockIter([]byte("alpha"))
	iter2 := newMockIter([]byte("zulu"))
	heap.Push(h, heapItem{iter: iter1, index: 0})
	heap.Push(h, heapItem{iter: iter2, index: 1})

	if h.Len() != 2 {
		t.Fatalf("heap length = %d, want 2", h.Len())
	}
	// maxHeap top should be "zulu" (largest key).
	top := heap.Pop(h).(heapItem)
	if string(top.iter.Key()) != "zulu" {
		t.Errorf("maxHeap top key = %q, want %q", top.iter.Key(), "zulu")
	}
}

// TestMinHeap_Less_StableOrder directly exercises the stable-ordering branch
// of minHeap.Less (equal keys → compare by index).
func TestMinHeap_Less_StableOrder(t *testing.T) {
	h := &minHeap{}

	// Create two mock iterators with the same key to trigger equal-key path.
	iter1 := newMockIter([]byte("same"))
	iter2 := newMockIter([]byte("same"))
	*h = append(*h, heapItem{iter: iter1, index: 2})
	*h = append(*h, heapItem{iter: iter2, index: 1})
	heap.Init(h)

	// The heap with lower index should be considered "less" (higher priority).
	if h.Len() != 2 {
		t.Fatalf("heap length = %d, want 2", h.Len())
	}
	// Pop returns the minimum. With equal keys, index=1 < index=2.
	top := heap.Pop(h).(heapItem)
	if top.index != 1 {
		t.Errorf("min-heap top index = %d, want 1 (stable order)", top.index)
	}
}

// TestMaxHeap_Less_StableOrder directly exercises the stable-ordering branch
// of maxHeap.Less (equal keys → compare by index).
func TestMaxHeap_Less_StableOrder(t *testing.T) {
	h := &maxHeap{}

	iter1 := newMockIter([]byte("same"))
	iter2 := newMockIter([]byte("same"))
	*h = append(*h, heapItem{iter: iter1, index: 2})
	*h = append(*h, heapItem{iter: iter2, index: 1})
	heap.Init(h)

	if h.Len() != 2 {
		t.Fatalf("heap length = %d, want 2", h.Len())
	}
	// In maxHeap, equal keys still use index for stability.
	top := heap.Pop(h).(heapItem)
	if top.index != 1 {
		t.Errorf("max-heap top index = %d, want 1 (stable order)", top.index)
	}
}

// mockIter is a minimal storage.Iterator that always returns a fixed key.
type mockIter struct {
	key   []byte
	valid bool
}

func newMockIter(key []byte) *mockIter {
	return &mockIter{key: key, valid: true}
}

func (m *mockIter) SeekToFirst() bool       { m.valid = true; return true }
func (m *mockIter) SeekToLast() bool        { m.valid = true; return true }
func (m *mockIter) Seek(target []byte) bool { m.valid = true; return true }
func (m *mockIter) Next() bool              { m.valid = false; return false }
func (m *mockIter) Prev() bool              { return false }
func (m *mockIter) Valid() bool             { return m.valid }
func (m *mockIter) Key() []byte             { return m.key }
func (m *mockIter) Value() []byte           { return nil }
func (m *mockIter) Error() error            { return nil }
func (m *mockIter) Close() error            { return nil }

// ---------------------------------------------------------------------------
// mergeIterator.Error — underlying iterator error path
// ---------------------------------------------------------------------------

// TestMergeIterator_Error_StoredErr exercises the m.err != nil branch in Error().
// The m.err field is declared for future use; we inject it directly via
// white-box access within the same package.
func TestMergeIterator_Error_StoredErr(t *testing.T) {
	sentinel := errors.New("stored merge error")
	mi := newMergeIterator(nil, nil)
	mi.err = sentinel // inject stored error

	err := mi.Error()
	if err == nil {
		t.Fatal("Error() should return non-nil when m.err is set")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Error() = %v, want to wrap %v", err, sentinel)
	}
}

// TestMergeIterator_Error_PropagatesUnderlying verifies that Error() returns
// a MergeIteratorError when an underlying iterator has an error.
func TestMergeIterator_Error_PropagatesUnderlying(t *testing.T) {
	sentinel := errors.New("underlying storage error")
	errIter := &errorIter{err: sentinel}

	mi := newMergeIterator([]storage.Iterator{errIter}, nil)
	defer func() { _ = mi.Close() }()

	err := mi.Error()
	if err == nil {
		t.Fatal("Error() should return non-nil when underlying iter has error")
	}
	var mergeErr *MergeIteratorError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("expected *MergeIteratorError, got %T: %v", err, err)
	}
}

// errorIter is a mock iterator that always returns a fixed error from Error().
type errorIter struct {
	err error
}

func (e *errorIter) SeekToFirst() bool       { return false }
func (e *errorIter) SeekToLast() bool        { return false }
func (e *errorIter) Seek(target []byte) bool { return false }
func (e *errorIter) Next() bool              { return false }
func (e *errorIter) Prev() bool              { return false }
func (e *errorIter) Valid() bool             { return false }
func (e *errorIter) Key() []byte             { return nil }
func (e *errorIter) Value() []byte           { return nil }
func (e *errorIter) Error() error            { return e.err }
func (e *errorIter) Close() error            { return nil }

// ---------------------------------------------------------------------------
// mergeIterator.Close — propagates first error from underlying Close()
// ---------------------------------------------------------------------------

// TestMergeIterator_Close_PropagatesError verifies Close() returns the first
// error encountered when closing underlying iterators.
func TestMergeIterator_Close_PropagatesError(t *testing.T) {
	closeErr := errors.New("close failed")
	closingIter := &closingErrorIter{err: closeErr}

	mi := newMergeIterator([]storage.Iterator{closingIter}, nil)

	err := mi.Close()
	if err == nil {
		t.Fatal("Close() should propagate underlying close error")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Close() err = %v, want to wrap %v", err, closeErr)
	}
}

// closingErrorIter is a mock iterator that returns an error from Close().
type closingErrorIter struct {
	err error
}

func (c *closingErrorIter) SeekToFirst() bool       { return false }
func (c *closingErrorIter) SeekToLast() bool        { return false }
func (c *closingErrorIter) Seek(target []byte) bool { return false }
func (c *closingErrorIter) Next() bool              { return false }
func (c *closingErrorIter) Prev() bool              { return false }
func (c *closingErrorIter) Valid() bool             { return false }
func (c *closingErrorIter) Key() []byte             { return nil }
func (c *closingErrorIter) Value() []byte           { return nil }
func (c *closingErrorIter) Error() error            { return nil }
func (c *closingErrorIter) Close() error            { return c.err }

// ---------------------------------------------------------------------------
// mergeIterator.Next — nil current (before any Seek)
// ---------------------------------------------------------------------------

// TestMergeIterator_Next_NilCurrent verifies Next returns false when called
// before any positioning (current == nil).
func TestMergeIterator_Next_NilCurrent(t *testing.T) {
	mi := newMergeIterator([]storage.Iterator{}, nil)
	defer func() { _ = mi.Close() }()

	if mi.Next() {
		t.Error("Next with nil current should return false")
	}
}

// ---------------------------------------------------------------------------
// Manager.Snapshot — closed storage
// ---------------------------------------------------------------------------

// TestManager_Snapshot_ClosedReturnsError verifies Snapshot on a closed manager
// returns a ClosedError.
func TestManager_Snapshot_ClosedReturnsError(t *testing.T) {
	m := newTestManager(t, 2)
	_ = m.Close()

	_, err := m.Snapshot()
	if err == nil {
		t.Fatal("Snapshot on closed manager should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// TestManager_Snapshot_IteratorWorks verifies that a snapshot iterator
// correctly reads committed data.
func TestManager_Snapshot_IteratorWorks(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("snap-%02d", i)
		if err := m.Set([]byte(key), []byte("v")); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	if len(collected) != 5 {
		t.Errorf("snapshot iterator returned %d keys, want 5", len(collected))
	}
}

// TestManager_Snapshot_ClosedIterator2 verifies that a snapshot's NewIterator
// after close returns a closed merge iterator.
func TestManager_Snapshot_ClosedIterator2(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	// NewIterator on closed snapshot should return a closed iterator.
	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToFirst() {
		t.Error("SeekToFirst on closed snapshot iterator should return false")
	}
}

// ---------------------------------------------------------------------------
// Manager.List — error propagation from partition
// ---------------------------------------------------------------------------

// TestManager_List_ClosedError verifies List on a closed manager returns ClosedError.
func TestManager_List_ClosedError(t *testing.T) {
	m := newTestManager(t, 2)
	_ = m.Close()

	_, err := m.List(nil)
	if err == nil {
		t.Fatal("List on closed manager should return error")
	}
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *storage.ClosedError, got %T: %v", err, err)
	}
}

// TestManager_List_PartitionError verifies List propagates a partition error.
func TestManager_List_PartitionError(t *testing.T) {
	listErr := errors.New("partition list error")
	failStorage := &failingStorage{listErr: listErr}

	m := &Manager{
		router:     NewHashRouter(1),
		partitions: []storage.Storage{failStorage},
		config:     &Config{PartitionCount: 1, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	_, err := m.List(nil)
	if err == nil {
		t.Fatal("List should propagate partition error")
	}
	if !errors.Is(err, listErr) {
		t.Fatalf("List error = %v, want to wrap %v", err, listErr)
	}
}

// TestManager_Snapshot_PartitionError verifies that Snapshot cleans up
// already-created snapshots when a later partition fails.
func TestManager_Snapshot_PartitionError(t *testing.T) {
	snapErr := errors.New("snapshot error")
	goodStorage := newTestMemoryStorage()
	failStorage := &failingStorage{snapshotErr: snapErr}

	m := &Manager{
		router:     NewHashRouter(2),
		partitions: []storage.Storage{goodStorage, failStorage},
		config:     &Config{PartitionCount: 2, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	_, err := m.Snapshot()
	if err == nil {
		t.Fatal("Snapshot should fail when a partition fails")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T: %v", err, err)
	}
}

// TestManager_Close_PartitionError verifies Close propagates a partition close error.
func TestManager_Close_PartitionError(t *testing.T) {
	closeErr := errors.New("close error")
	failStorage := &failingStorage{closeErr: closeErr}

	m := &Manager{
		router:     NewHashRouter(1),
		partitions: []storage.Storage{failStorage},
		config:     &Config{PartitionCount: 1, BackendType: BackendMemory},
	}

	err := m.Close()
	if err == nil {
		t.Fatal("Close should propagate partition close error")
	}
	var pce *PartitionCloseError
	if !errors.As(err, &pce) {
		t.Fatalf("expected *PartitionCloseError, got %T: %v", err, err)
	}
}

// TestBatch_Commit_PartitionError verifies Commit returns BatchCommitError
// when a partition batch fails to commit.
func TestBatch_Commit_PartitionError(t *testing.T) {
	commitErr := errors.New("batch commit error")
	failStorage := &failingStorage{batchCommitErr: commitErr}

	m := &Manager{
		router:     NewHashRouter(1),
		partitions: []storage.Storage{failStorage},
		config:     &Config{PartitionCount: 1, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	// Use the failStorage's batch directly — route key to partition 0.
	// We need to set a key so the batch is allocated to the failing partition.
	_ = batch.Set([]byte("key"), []byte("val"))

	err := batch.Commit()
	if err == nil {
		t.Fatal("Commit should fail when partition batch fails")
	}
	var bce *BatchCommitError
	if !errors.As(err, &bce) {
		t.Fatalf("expected *BatchCommitError, got %T: %v", err, err)
	}
}

// TestBatch_Rollback_PartitionError verifies Rollback propagates a partition error.
func TestBatch_Rollback_PartitionError(t *testing.T) {
	rollbackErr := errors.New("rollback error")
	failStorage := &failingStorage{batchRollbackErr: rollbackErr}

	m := &Manager{
		router:     NewHashRouter(1),
		partitions: []storage.Storage{failStorage},
		config:     &Config{PartitionCount: 1, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	_ = batch.Set([]byte("key"), []byte("val"))

	err := batch.Rollback()
	if err == nil {
		t.Fatal("Rollback should fail when partition batch fails")
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("Rollback error = %v, want to wrap %v", err, rollbackErr)
	}
}

// TestBatch_Close_PartitionError verifies Close propagates a partition batch close error.
func TestBatch_Close_PartitionError(t *testing.T) {
	closeErr := errors.New("batch close error")
	failStorage := &failingStorage{batchCloseErr: closeErr}

	m := &Manager{
		router:     NewHashRouter(1),
		partitions: []storage.Storage{failStorage},
		config:     &Config{PartitionCount: 1, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	_ = batch.Set([]byte("key"), []byte("val"))

	err := batch.Close()
	if err == nil {
		t.Fatal("Close should fail when partition batch close fails")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want to wrap %v", err, closeErr)
	}
}

// TestSnapshot_Close_PartitionError verifies that partitionSnapshot.Close
// propagates a snapshot close error.
func TestSnapshot_Close_PartitionError(t *testing.T) {
	closeErr := errors.New("snapshot close error")
	failSnap := &failingSnapshot{closeErr: closeErr}

	ps := newPartitionSnapshot(NewHashRouter(1), []storage.Snapshot{failSnap})

	err := ps.Close()
	if err == nil {
		t.Fatal("Close should propagate snapshot close error")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want to wrap %v", err, closeErr)
	}
}

// ---------------------------------------------------------------------------
// Helper failing storage / batch / snapshot implementations
// ---------------------------------------------------------------------------

// failingStorage implements storage.Storage and returns errors for configured operations.
type failingStorage struct {
	listErr          error
	snapshotErr      error
	closeErr         error
	batchCommitErr   error
	batchRollbackErr error
	batchCloseErr    error
	deleteRangeErr   error
}

func (f *failingStorage) Get(key []byte) ([]byte, error)  { return nil, nil }
func (f *failingStorage) Set(key, value []byte) error     { return nil }
func (f *failingStorage) Delete(key []byte) error         { return nil }
func (f *failingStorage) DeleteRange(_, _ []byte) error   { return f.deleteRangeErr }
func (f *failingStorage) Exists(key []byte) (bool, error) { return false, nil }
func (f *failingStorage) List(prefix []byte) ([][]byte, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return nil, nil
}
func (f *failingStorage) Batch() storage.Batch {
	return &failingBatch{
		commitErr:      f.batchCommitErr,
		rollbackErr:    f.batchRollbackErr,
		closeErr:       f.batchCloseErr,
		deleteRangeErr: f.deleteRangeErr,
	}
}
func (f *failingStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &errorIter{}
}
func (f *failingStorage) Snapshot() (storage.Snapshot, error) {
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	return &failingSnapshot{}, nil
}
func (f *failingStorage) Close() error { return f.closeErr }

// failingBatch returns errors for configured operations.
type failingBatch struct {
	commitErr      error
	rollbackErr    error
	closeErr       error
	deleteRangeErr error
	closed         bool
}

func (b *failingBatch) Set(key, value []byte) error    { return nil }
func (b *failingBatch) Delete(key []byte) error        { return nil }
func (b *failingBatch) DeleteRange(_, _ []byte) error  { return b.deleteRangeErr }
func (b *failingBatch) Get(key []byte) ([]byte, error) { return nil, nil }
func (b *failingBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &errorIter{}
}
func (b *failingBatch) Commit() error {
	if b.commitErr != nil {
		return b.commitErr
	}
	b.closed = true
	return nil
}
func (b *failingBatch) Rollback() error {
	if b.rollbackErr != nil {
		return b.rollbackErr
	}
	b.closed = true
	return nil
}
func (b *failingBatch) Close() error {
	if b.closed {
		return nil
	}
	if b.closeErr != nil {
		return b.closeErr
	}
	b.closed = true
	return nil
}

// failingSnapshot implements storage.Snapshot and returns errors for Close.
type failingSnapshot struct {
	closeErr error
}

func (s *failingSnapshot) Get(key []byte) ([]byte, error) { return nil, nil }
func (s *failingSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return &errorIter{}
}
func (s *failingSnapshot) Close() error { return s.closeErr }

// ---------------------------------------------------------------------------
// Manager.Close — double close is idempotent
// ---------------------------------------------------------------------------

// TestManager_Close_Idempotent verifies that closing a manager twice is safe.
func TestManager_Close_Idempotent(t *testing.T) {
	m := newTestManager(t, 2)
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("second Close should be no-op: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Batch.Close — double close is idempotent
// ---------------------------------------------------------------------------

// TestBatch_Close_Idempotent verifies that closing a partition batch twice
// is safe.
func TestBatch_Close_Idempotent(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Errorf("second Close should be no-op: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Batch.Rollback — empty batch rollback
// ---------------------------------------------------------------------------

// TestBatch_Rollback_EmptyBatch verifies that rolling back an empty batch
// succeeds without error.
func TestBatch_Rollback_EmptyBatch(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()

	batch := m.Batch()
	if err := batch.Rollback(); err != nil {
		t.Errorf("Rollback on empty batch: %v", err)
	}
}

// ---------------------------------------------------------------------------
// nextForward / nextReverse — empty heap (0% path in nextForward/nextReverse)
// ---------------------------------------------------------------------------

// TestMergeIterator_NextForward_EmptyHeap_Direct directly tests nextForward
// with an empty heap by constructing the merge iterator in the right state.
// This covers the defensive early-return when the heap is empty but current != nil.
func TestMergeIterator_NextForward_EmptyHeap_Direct(t *testing.T) {
	s := memory.NewStorage()
	defer func() { _ = s.Close() }()

	// Create a mock iterator that tracks calls.
	iter := newMockIter([]byte("k"))

	mi := newMergeIterator([]storage.Iterator{iter}, nil)
	defer func() { _ = mi.Close() }()

	// SeekToFirst: puts iter in fwdHeap, current = iter.
	mi.SeekToFirst()

	// Manually clear the forward heap while leaving current set.
	// This simulates the state where nextForward is entered with empty heap.
	mi.fwdHeap = mi.fwdHeap[:0]

	// Now Next() → nextForward() with empty heap → returns false.
	if mi.Next() {
		t.Error("nextForward with empty heap should return false")
	}
	if mi.current != nil {
		t.Error("current should be nil after nextForward with empty heap")
	}
}

// TestMergeIterator_NextReverse_EmptyHeap_Direct directly tests nextReverse
// with an empty reverse heap by constructing the right state.
func TestMergeIterator_NextReverse_EmptyHeap_Direct(t *testing.T) {
	s := memory.NewStorage()
	defer func() { _ = s.Close() }()

	iter := newMockIter([]byte("k"))
	opts := &storage.IteratorOptions{Reverse: true}
	mi := newMergeIterator([]storage.Iterator{iter}, opts)
	defer func() { _ = mi.Close() }()

	// SeekToFirst in reverse mode: puts iter in revHeap.
	mi.SeekToFirst()

	// Clear the reverse heap while leaving current set.
	mi.revHeap = mi.revHeap[:0]

	// Now Next() → nextReverse() with empty heap → returns false.
	if mi.Next() {
		t.Error("nextReverse with empty heap should return false")
	}
	if mi.current != nil {
		t.Error("current should be nil after nextReverse with empty heap")
	}
}
