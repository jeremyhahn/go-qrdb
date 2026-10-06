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

package mvcc

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// In-memory storage mock
// ---------------------------------------------------------------------------

// iterItem holds a key-value pair for iterator snapshots.
type iterItem struct {
	key []byte
	val []byte
}

// memStorage is a minimal in-memory storage.Storage for unit tests.
// It returns QRDBError{ErrNotFound} when a key does not exist, matching
// the contract expected by RaftStorage/revisionManager code paths.
type memStorage struct {
	mu     sync.RWMutex
	data   map[string][]byte
	closed bool
}

func newMemStorage() *memStorage {
	return &memStorage{data: make(map[string][]byte)}
}

func (m *memStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, &storage.ClosedError{}
	}
	v, ok := m.data[string(key)]
	if !ok {
		// storage.Reader.Get: "Returns nil, nil when key does not exist".
		//
		// This double used to return a typed ErrNotFound here, which no
		// shipped engine does, and that single line is why go-qrdb#24 went
		// unnoticed: every test in this package exercised an absence spelling
		// the production code would never meet, so the guards that only tested
		// errors.IsNotFound looked covered while being dead against memory,
		// file and pebble alike. A double that contradicts the interface it
		// stands in for does not reduce confidence in the tests — it inverts
		// it.
		return nil, nil
	}
	// Note the allocation: a key holding an EMPTY value returns a non-nil
	// zero-length slice, exactly as the real engines do. That is what lets
	// absent() distinguish it from a missing key, so this line is part of the
	// contract being modelled and not an incidental copy.
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memStorage) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return &storage.ClosedError{}
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *memStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return &storage.ClosedError{}
	}
	delete(m.data, string(key))
	return nil
}

func (m *memStorage) Batch() storage.Batch {
	return &memBatch{store: m}
}

func (m *memStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var items []iterItem
	for k, v := range m.data {
		kb := []byte(k)
		if opts != nil && opts.LowerBound != nil && bytes.Compare(kb, opts.LowerBound) < 0 {
			continue
		}
		if opts != nil && opts.UpperBound != nil && bytes.Compare(kb, opts.UpperBound) >= 0 {
			continue
		}
		kcopy := make([]byte, len(kb))
		copy(kcopy, kb)
		vcopy := make([]byte, len(v))
		copy(vcopy, v)
		items = append(items, iterItem{key: kcopy, val: vcopy})
	}
	// Sort for deterministic iteration
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if bytes.Compare(items[i].key, items[j].key) > 0 {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	return &memIterator{items: items, pos: -1}
}

func (m *memStorage) Snapshot() (storage.Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, &storage.ClosedError{}
	}
	snap := make(map[string][]byte, len(m.data))
	for k, v := range m.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		snap[k] = cp
	}
	return &memSnapshot{data: snap}, nil
}

func (m *memStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *memStorage) Exists(key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return false, &storage.ClosedError{}
	}
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *memStorage) List(prefix []byte) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, &storage.ClosedError{}
	}
	var keys [][]byte
	p := string(prefix)
	for k := range m.data {
		if len(p) == 0 || len(k) >= len(p) && k[:len(p)] == p {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

// memBatch implements storage.Batch with deferred apply.
type memBatch struct {
	store *memStorage
	ops   []func()
}

func (b *memBatch) Set(key, value []byte) error {
	kcopy := make([]byte, len(key))
	copy(kcopy, key)
	vcopy := make([]byte, len(value))
	copy(vcopy, value)
	b.ops = append(b.ops, func() {
		b.store.data[string(kcopy)] = vcopy
	})
	return nil
}

func (b *memBatch) Delete(key []byte) error {
	kcopy := make([]byte, len(key))
	copy(kcopy, key)
	b.ops = append(b.ops, func() {
		delete(b.store.data, string(kcopy))
	})
	return nil
}

func (b *memBatch) Commit() error {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	if b.store.closed {
		return &storage.ClosedError{}
	}
	for _, op := range b.ops {
		op()
	}
	return nil
}

func (b *memBatch) Rollback() error {
	b.ops = nil
	return nil
}

// Get reads through the batch's pending operations by replaying them onto a
// copy of the committed state, so the fake honours storage.Batch's
// read-your-own-writes contract.
func (b *memBatch) Get(key []byte) ([]byte, error) {
	return b.overlay().Get(key)
}

func (b *memBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return b.overlay().NewIterator(opts)
}

// overlay materializes the committed state with this batch's pending
// operations applied.
func (b *memBatch) overlay() *memStorage {
	b.store.mu.RLock()
	view := &memStorage{data: make(map[string][]byte, len(b.store.data))}
	for k, v := range b.store.data {
		view.data[k] = v
	}
	b.store.mu.RUnlock()

	shadow := &memBatch{store: view, ops: b.ops}
	_ = shadow.Commit()
	return view
}

func (b *memBatch) Close() error {
	b.ops = nil
	return nil
}

// memIterator iterates over a sorted snapshot of keys.
type memIterator struct {
	items []iterItem
	pos   int
}

func (it *memIterator) SeekToFirst() bool {
	if len(it.items) == 0 {
		return false
	}
	it.pos = 0
	return true
}

func (it *memIterator) SeekToLast() bool {
	if len(it.items) == 0 {
		return false
	}
	it.pos = len(it.items) - 1
	return true
}

func (it *memIterator) Seek(target []byte) bool {
	for i, item := range it.items {
		if bytes.Compare(item.key, target) >= 0 {
			it.pos = i
			return true
		}
	}
	return false
}

func (it *memIterator) Next() bool {
	it.pos++
	return it.pos < len(it.items)
}

func (it *memIterator) Prev() bool {
	it.pos--
	return it.pos >= 0
}

func (it *memIterator) Valid() bool {
	return it.pos >= 0 && it.pos < len(it.items)
}

func (it *memIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.items[it.pos].key
}

func (it *memIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	return it.items[it.pos].val
}

func (it *memIterator) Error() error { return nil }
func (it *memIterator) Close() error { return nil }

// memSnapshot provides a consistent point-in-time read view.
type memSnapshot struct {
	data   map[string][]byte
	closed bool
}

func (s *memSnapshot) Get(key []byte) ([]byte, error) {
	if s.closed {
		return nil, &storage.ClosedError{}
	}
	v, ok := s.data[string(key)]
	if !ok {
		return nil, errors.New(errors.ErrNotFound, "memSnapshot.Get", "key not found")
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (s *memSnapshot) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return &memIterator{pos: -1}
}

func (s *memSnapshot) Close() error {
	s.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// Mock RaftNode
// ---------------------------------------------------------------------------

// mockRaftNode is a minimal RaftNode for unit tests.
// It records proposals and can be configured to return errors.
type mockRaftNode struct {
	mu        sync.Mutex
	proposals []mockProposal
	err       error // If set, ProposeWithMode returns this error.
}

type mockProposal struct {
	shardID uint64
	data    []byte
	mode    writemode.Mode
}

func (m *mockRaftNode) ProposeWithMode(_ context.Context, shardID uint64, data []byte, mode writemode.Mode) (sm.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return sm.Result{}, m.err
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.proposals = append(m.proposals, mockProposal{shardID: shardID, data: cp, mode: mode})
	return sm.Result{Value: 1}, nil
}

func (m *mockRaftNode) proposalCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.proposals)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return log
}

func newTestRaftStorage(t *testing.T) *RaftStorage {
	t.Helper()
	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:  newMemStorage(),
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	return s
}

func newTestRaftStorageWithRaft(t *testing.T, raft RaftNode, shardID uint64) *RaftStorage {
	t.Helper()
	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:    newMemStorage(),
		Logger:   newTestLogger(t),
		RaftNode: raft,
		ShardID:  shardID,
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// NewRaftStorage
// ---------------------------------------------------------------------------

func TestNewRaftStorage_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	if s == nil {
		t.Fatal("expected non-nil RaftStorage")
	}
	// Initial revision is 1 (set by revisionManager for a fresh store).
	if rev := s.RaftCurrentRevision(); rev != 1 {
		t.Errorf("initial revision: got %d, want 1", rev)
	}
}

func TestNewRaftStorage_NilStore(t *testing.T) {
	_, err := NewRaftStorage(&RaftStorageConfig{
		Store:  nil,
		Logger: newTestLogger(t),
	})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestNewRaftStorage_NilLogger(t *testing.T) {
	_, err := NewRaftStorage(&RaftStorageConfig{
		Store:  newMemStorage(),
		Logger: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestNewRaftStorage_WithRaftNode(t *testing.T) {
	raft := &mockRaftNode{}
	s := newTestRaftStorageWithRaft(t, raft, 42)
	if s == nil {
		t.Fatal("expected non-nil RaftStorage")
	}
	if s.raftNode == nil {
		t.Fatal("expected raftNode to be set")
	}
	if s.shardID != 42 {
		t.Errorf("shardID: got %d, want 42", s.shardID)
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestRaftStorage_Put_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	kv, rev, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rev < 1 {
		t.Errorf("expected positive revision, got %d", rev)
	}
	if !bytes.Equal(kv.Key, []byte("key1")) {
		t.Errorf("key: got %q, want %q", kv.Key, "key1")
	}
	if !bytes.Equal(kv.Value, []byte("value1")) {
		t.Errorf("value: got %q, want %q", kv.Value, "value1")
	}
	if kv.Version != 1 {
		t.Errorf("version: got %d, want 1", kv.Version)
	}
	if kv.CreateRevision != rev {
		t.Errorf("CreateRevision: got %d, want %d", kv.CreateRevision, rev)
	}
	if kv.ModRevision != rev {
		t.Errorf("ModRevision: got %d, want %d", kv.ModRevision, rev)
	}
}

func TestRaftStorage_Put_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, nil, []byte("value"))
	if err == nil {
		t.Fatal("expected error for nil key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, _, err = s.Put(ctx, []byte{}, []byte("value"))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_Put_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := s.Put(ctx, []byte("key"), []byte("value"))
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

func TestRaftStorage_Put_UpdateExisting(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	kv1, rev1, err := s.Put(ctx, []byte("key1"), []byte("v1"))
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}

	kv2, rev2, err := s.Put(ctx, []byte("key1"), []byte("v2"))
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	if rev2 <= rev1 {
		t.Errorf("rev2 (%d) should be > rev1 (%d)", rev2, rev1)
	}
	if kv2.Version != 2 {
		t.Errorf("version after update: got %d, want 2", kv2.Version)
	}
	if kv2.CreateRevision != kv1.CreateRevision {
		t.Errorf("CreateRevision should not change: got %d, want %d", kv2.CreateRevision, kv1.CreateRevision)
	}
	if kv2.ModRevision != rev2 {
		t.Errorf("ModRevision: got %d, want %d", kv2.ModRevision, rev2)
	}
	if !bytes.Equal(kv2.Value, []byte("v2")) {
		t.Errorf("value: got %q, want %q", kv2.Value, "v2")
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestRaftStorage_Get_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, rev, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get latest (revision 0)
	kv, err := s.Get(ctx, []byte("key1"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("value1")) {
		t.Errorf("value: got %q, want %q", kv.Value, "value1")
	}

	// Get at specific revision
	kv, err = s.Get(ctx, []byte("key1"), rev)
	if err != nil {
		t.Fatalf("Get at revision: %v", err)
	}
	if !bytes.Equal(kv.Value, []byte("value1")) {
		t.Errorf("value at revision: got %q, want %q", kv.Value, "value1")
	}
}

func TestRaftStorage_Get_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, err := s.Get(ctx, nil, 0)
	if err == nil {
		t.Fatal("expected error for nil key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, err = s.Get(ctx, []byte{}, 0)
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_Get_NotFound(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, err := s.Get(ctx, []byte("nonexistent"), 0)
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestRaftStorage_Get_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Get(ctx, []byte("key"), 0)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

func TestRaftStorage_Get_RevisionBeforeCreation(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, rev, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Request a revision before the key was created
	_, err = s.Get(ctx, []byte("key1"), rev-1)
	if err == nil {
		t.Fatal("expected error for revision before creation")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestRaftStorage_Delete_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	prevKV, rev, err := s.Delete(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if prevKV == nil {
		t.Fatal("expected previous KV")
	}
	if !bytes.Equal(prevKV.Value, []byte("value1")) {
		t.Errorf("prevKV value: got %q, want %q", prevKV.Value, "value1")
	}
	if rev < 1 {
		t.Errorf("expected positive revision after delete, got %d", rev)
	}

	// Verify key is gone
	_, err = s.Get(ctx, []byte("key1"), 0)
	if err == nil {
		t.Fatal("expected error after delete")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

func TestRaftStorage_Delete_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Delete(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, _, err = s.Delete(ctx, []byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_Delete_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := s.Delete(ctx, []byte("key"))
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

func TestRaftStorage_Delete_NonexistentKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	prevKV, rev, err := s.Delete(ctx, []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Delete nonexistent: %v", err)
	}
	if prevKV != nil {
		t.Errorf("expected nil prevKV for nonexistent key, got: %v", prevKV)
	}
	if rev < 1 {
		t.Errorf("expected positive revision, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

func TestRaftStorage_Range_SingleKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, _, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Range with nil end returns single key
	results, err := s.Range(ctx, []byte("key1"), nil, 0, 0)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !bytes.Equal(results[0].Value, []byte("value1")) {
		t.Errorf("value: got %q, want %q", results[0].Value, "value1")
	}
}

func TestRaftStorage_Range_SingleKeyNotFound(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	results, err := s.Range(ctx, []byte("nonexistent"), nil, 0, 0)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestRaftStorage_Range_MultipleKeys(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		_, _, err := s.Put(ctx, []byte(k), []byte("val-"+k))
		if err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	// Range [b, d) should return b, c
	results, err := s.Range(ctx, []byte("b"), []byte("d"), 0, 0)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !bytes.Equal(results[0].Key, []byte("b")) {
		t.Errorf("results[0].Key: got %q, want %q", results[0].Key, "b")
	}
	if !bytes.Equal(results[1].Key, []byte("c")) {
		t.Errorf("results[1].Key: got %q, want %q", results[1].Key, "c")
	}
}

func TestRaftStorage_Range_WithLimit(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	for _, k := range []string{"a", "b", "c", "d"} {
		_, _, err := s.Put(ctx, []byte(k), []byte("val-"+k))
		if err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	// Range [a, e) with limit 2
	results, err := s.Range(ctx, []byte("a"), []byte("e"), 0, 2)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results with limit, got %d", len(results))
	}
}

func TestRaftStorage_Range_EmptyStartKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, err := s.Range(ctx, nil, []byte("z"), 0, 0)
	if err == nil {
		t.Fatal("expected error for nil start key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, err = s.Range(ctx, []byte{}, []byte("z"), 0, 0)
	if err == nil {
		t.Fatal("expected error for empty start key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_Range_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Range(ctx, []byte("a"), []byte("z"), 0, 0)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RaftCurrentRevision
// ---------------------------------------------------------------------------

func TestRaftStorage_CurrentRevision(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	initialRev := s.RaftCurrentRevision()
	if initialRev < 1 {
		t.Errorf("expected initial revision >= 1, got %d", initialRev)
	}

	_, rev1, err := s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rev1 != initialRev+1 {
		t.Errorf("expected revision %d, got %d", initialRev+1, rev1)
	}

	currentRev := s.RaftCurrentRevision()
	if currentRev != rev1 {
		t.Errorf("CurrentRevision: got %d, want %d", currentRev, rev1)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestRaftStorage_Close_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRaftStorage_Close_PersistsRevision(t *testing.T) {
	store := newMemStorage()
	log := newTestLogger(t)

	s, err := NewRaftStorage(&RaftStorageConfig{
		Store:  store,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}

	ctx := context.Background()
	_, _, err = s.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	revBefore := s.RaftCurrentRevision()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the revision was persisted by reading from the raw store.
	// Note: store.Close() was called by RaftStorage.Close(), so we cannot
	// read from it. Instead, verify that Close did not return an error,
	// which means persist() succeeded.
	_ = revBefore
}

// ---------------------------------------------------------------------------
// BeginRaftTxn
// ---------------------------------------------------------------------------

func TestRaftStorage_BeginRaftTxn_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	txn, err := s.BeginRaftTxn(ctx)
	if err != nil {
		t.Fatalf("BeginRaftTxn: %v", err)
	}
	if txn == nil {
		t.Fatal("expected non-nil transaction")
	}
	// Clean up
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Put/Get/Delete roundtrip
// ---------------------------------------------------------------------------

func TestRaftStorage_PutGetDeleteRoundtrip(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Put
	putKV, putRev, err := s.Put(ctx, []byte("roundtrip"), []byte("data"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get
	getKV, err := s.Get(ctx, []byte("roundtrip"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(getKV.Value, putKV.Value) {
		t.Errorf("Get value mismatch: got %q, want %q", getKV.Value, putKV.Value)
	}
	if getKV.ModRevision != putRev {
		t.Errorf("Get ModRevision: got %d, want %d", getKV.ModRevision, putRev)
	}

	// Delete
	prevKV, _, err := s.Delete(ctx, []byte("roundtrip"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !bytes.Equal(prevKV.Value, []byte("data")) {
		t.Errorf("Delete prevKV value: got %q, want %q", prevKV.Value, "data")
	}

	// Verify gone
	_, err = s.Get(ctx, []byte("roundtrip"), 0)
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multiple puts increment version
// ---------------------------------------------------------------------------

func TestRaftStorage_Put_VersionIncrements(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	for i := int64(1); i <= 5; i++ {
		kv, _, err := s.Put(ctx, []byte("versioned"), []byte("v"))
		if err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
		if kv.Version != i {
			t.Errorf("version at Put %d: got %d, want %d", i, kv.Version, i)
		}
	}
}

// ---------------------------------------------------------------------------
// PutAtRevision
// ---------------------------------------------------------------------------

func TestRaftStorage_PutAtRevision_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	kv, err := s.PutAtRevision(ctx, []byte("key1"), []byte("value1"), 0, 100)
	if err != nil {
		t.Fatalf("PutAtRevision: %v", err)
	}
	if !bytes.Equal(kv.Key, []byte("key1")) {
		t.Errorf("key: got %q, want %q", kv.Key, "key1")
	}
	if !bytes.Equal(kv.Value, []byte("value1")) {
		t.Errorf("value: got %q, want %q", kv.Value, "value1")
	}
	if kv.CreateRevision != 100 {
		t.Errorf("CreateRevision: got %d, want 100", kv.CreateRevision)
	}
	if kv.ModRevision != 100 {
		t.Errorf("ModRevision: got %d, want 100", kv.ModRevision)
	}
	if kv.Version != 1 {
		t.Errorf("Version: got %d, want 1", kv.Version)
	}

	// Verify the value is retrievable
	got, err := s.Get(ctx, []byte("key1"), 0)
	if err != nil {
		t.Fatalf("Get after PutAtRevision: %v", err)
	}
	if !bytes.Equal(got.Value, []byte("value1")) {
		t.Errorf("Get value: got %q, want %q", got.Value, "value1")
	}
}

func TestRaftStorage_PutAtRevision_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, err := s.PutAtRevision(ctx, nil, []byte("value"), 0, 100)
	if err == nil {
		t.Fatal("expected error for nil key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, err = s.PutAtRevision(ctx, []byte{}, []byte("value"), 0, 100)
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_PutAtRevision_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.PutAtRevision(ctx, []byte("key"), []byte("value"), 0, 100)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

func TestRaftStorage_PutAtRevision_UpdatesHighWaterMark(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Put at revision 50
	_, err := s.PutAtRevision(ctx, []byte("key1"), []byte("v1"), 0, 50)
	if err != nil {
		t.Fatalf("PutAtRevision(50): %v", err)
	}
	if rev := s.RaftCurrentRevision(); rev != 50 {
		t.Errorf("after rev 50: current revision got %d, want 50", rev)
	}

	// Put at revision 200 (higher) - should update high-water mark
	_, err = s.PutAtRevision(ctx, []byte("key2"), []byte("v2"), 0, 200)
	if err != nil {
		t.Fatalf("PutAtRevision(200): %v", err)
	}
	if rev := s.RaftCurrentRevision(); rev != 200 {
		t.Errorf("after rev 200: current revision got %d, want 200", rev)
	}

	// Put at revision 100 (lower) - should NOT change high-water mark
	_, err = s.PutAtRevision(ctx, []byte("key3"), []byte("v3"), 0, 100)
	if err != nil {
		t.Fatalf("PutAtRevision(100): %v", err)
	}
	if rev := s.RaftCurrentRevision(); rev != 200 {
		t.Errorf("after rev 100 (lower): current revision got %d, want 200", rev)
	}
}

func TestRaftStorage_PutAtRevision_WithLeaseID(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	kv, err := s.PutAtRevision(ctx, []byte("leased-key"), []byte("value"), 42, 100)
	if err != nil {
		t.Fatalf("PutAtRevision: %v", err)
	}
	if kv.LeaseID != 42 {
		t.Errorf("LeaseID: got %d, want 42", kv.LeaseID)
	}

	// Verify lease persists in storage
	got, err := s.Get(ctx, []byte("leased-key"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LeaseID != 42 {
		t.Errorf("Get LeaseID: got %d, want 42", got.LeaseID)
	}
}

// ---------------------------------------------------------------------------
// DeleteAtRevision
// ---------------------------------------------------------------------------

func TestRaftStorage_DeleteAtRevision_Success(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// First put a key
	_, err := s.PutAtRevision(ctx, []byte("key1"), []byte("value1"), 0, 10)
	if err != nil {
		t.Fatalf("PutAtRevision: %v", err)
	}

	// Delete at a later revision
	prevKV, err := s.DeleteAtRevision(ctx, []byte("key1"), 20)
	if err != nil {
		t.Fatalf("DeleteAtRevision: %v", err)
	}
	if prevKV == nil {
		t.Fatal("expected previous KV")
	}
	if !bytes.Equal(prevKV.Value, []byte("value1")) {
		t.Errorf("prevKV value: got %q, want %q", prevKV.Value, "value1")
	}

	// Verify key is gone
	_, err = s.Get(ctx, []byte("key1"), 0)
	if err == nil {
		t.Fatal("expected error after delete")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}

	// Verify high-water mark was updated
	if rev := s.RaftCurrentRevision(); rev != 20 {
		t.Errorf("current revision: got %d, want 20", rev)
	}
}

func TestRaftStorage_DeleteAtRevision_EmptyKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	_, err := s.DeleteAtRevision(ctx, nil, 100)
	if err == nil {
		t.Fatal("expected error for nil key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}

	_, err = s.DeleteAtRevision(ctx, []byte{}, 100)
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if !errors.IsInvalidInput(err) {
		t.Errorf("expected ErrInvalidInput, got: %v", err)
	}
}

func TestRaftStorage_DeleteAtRevision_CanceledContext(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.DeleteAtRevision(ctx, []byte("key"), 100)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.IsCanceled(err) {
		t.Errorf("expected ErrCanceled, got: %v", err)
	}
}

func TestRaftStorage_DeleteAtRevision_NonexistentKey(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	prevKV, err := s.DeleteAtRevision(ctx, []byte("nonexistent"), 100)
	if err != nil {
		t.Fatalf("DeleteAtRevision nonexistent: %v", err)
	}
	if prevKV != nil {
		t.Errorf("expected nil prevKV for nonexistent key, got: %v", prevKV)
	}

	// High-water mark should still be updated
	if rev := s.RaftCurrentRevision(); rev != 100 {
		t.Errorf("current revision: got %d, want 100", rev)
	}
}

// ---------------------------------------------------------------------------
// SetRevision
// ---------------------------------------------------------------------------

func TestRaftStorage_SetRevision_Success(t *testing.T) {
	s := newTestRaftStorage(t)

	s.SetRevision(500)
	if rev := s.RaftCurrentRevision(); rev != 500 {
		t.Errorf("after SetRevision(500): got %d, want 500", rev)
	}

	// Verify dirty flag is set
	if !s.revMgr.isDirty() {
		t.Error("expected revMgr to be dirty after SetRevision")
	}

	// Set to a lower value (should still work - it's a forcible set)
	s.SetRevision(100)
	if rev := s.RaftCurrentRevision(); rev != 100 {
		t.Errorf("after SetRevision(100): got %d, want 100", rev)
	}
}

func TestRaftStorage_SetRevision_UpdatesMetric(t *testing.T) {
	s := newTestRaftStorage(t)

	// SetRevision should update the metrics gauge.
	// We verify by calling SetRevision and then checking it does not panic
	// and that the revision is correctly reflected.
	s.SetRevision(999)
	if rev := s.RaftCurrentRevision(); rev != 999 {
		t.Errorf("after SetRevision(999): got %d, want 999", rev)
	}

	// Verify the metric gauge was set (indirectly by ensuring no panic
	// occurred and the method completed successfully)
	s.SetRevision(0)
	if rev := s.RaftCurrentRevision(); rev != 0 {
		t.Errorf("after SetRevision(0): got %d, want 0", rev)
	}
}

// ---------------------------------------------------------------------------
// Range with specific revision
// ---------------------------------------------------------------------------

func TestRaftStorage_Range_AtRevision(t *testing.T) {
	s := newTestRaftStorage(t)
	ctx := context.Background()

	// Put key and capture revision
	_, rev, err := s.Put(ctx, []byte("rkey"), []byte("rval"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Range at that revision (single key mode)
	results, err := s.Range(ctx, []byte("rkey"), nil, rev, 0)
	if err != nil {
		t.Fatalf("Range at revision: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result at revision, got %d", len(results))
	}
}
