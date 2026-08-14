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

package mvccadapter

import (
	"bytes"
	goerrors "errors"
	"sort"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// In-memory storage with snapshot support (required by mvcc.RaftStorage).
// ---------------------------------------------------------------------------

type iterItem struct {
	key []byte
	val []byte
}

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
		return nil, errors.New(errors.ErrNotFound, "memStorage.Get", "key not found")
	}
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
	for k := range m.data {
		if len(prefix) == 0 || bytes.HasPrefix([]byte(k), prefix) {
			keys = append(keys, []byte(k))
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})
	return keys, nil
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
	sort.Slice(items, func(i, j int) bool {
		return bytes.Compare(items[i].key, items[j].key) < 0
	})
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

// Get reads through the batch's pending operations, honouring storage.Batch's
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

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	rs, err := mvcc.NewRaftStorage(&mvcc.RaftStorageConfig{
		Store:  newMemStorage(),
		Logger: newTestLogger(t),
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	return NewAdapter(rs)
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestAdapter_InterfaceCompliance(t *testing.T) {
	// This test verifies compile-time interface compliance.
	// The var _ statemachine.MVCCStore = (*Adapter)(nil) declaration
	// in the source file provides the actual check; this test
	// confirms the adapter can be constructed and used as MVCCStore.
	adapter := newTestAdapter(t)
	// Assign to interface variable to verify compile-time compliance.
	var store statemachine.MVCCStore = adapter
	_ = store
}

func TestAdapter_InterfaceCompliance_WithMVCCStore(t *testing.T) {
	adapter := newTestAdapter(t)
	// Verify the adapter can be passed to WithMVCCStore option.
	// We only verify construction succeeds; the option is applied
	// internally to the FastStateMachine's unexported mvccStore field.
	store := newMemStorage()
	fsm := statemachine.NewFastStateMachine(1, 1, store, statemachine.WithMVCCStore(adapter))
	if fsm == nil {
		t.Fatal("expected non-nil FastStateMachine")
	}
}

// ---------------------------------------------------------------------------
// Put
// ---------------------------------------------------------------------------

func TestAdapter_Put_Success(t *testing.T) {
	adapter := newTestAdapter(t)

	result, err := adapter.Put([]byte("key1"), []byte("value1"), 0, 10)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from Put")
	}

	resultKV, ok := result.(*service.KeyValue)
	if !ok {
		t.Fatalf("expected *service.KeyValue, got %T", result)
	}
	if !bytes.Equal(resultKV.Key, []byte("key1")) {
		t.Fatalf("expected key 'key1', got %q", resultKV.Key)
	}
	if !bytes.Equal(resultKV.Value, []byte("value1")) {
		t.Fatalf("expected value 'value1', got %q", resultKV.Value)
	}
	if resultKV.CreateRevision != 10 {
		t.Fatalf("expected CreateRevision 10, got %d", resultKV.CreateRevision)
	}
	if resultKV.ModRevision != 10 {
		t.Fatalf("expected ModRevision 10, got %d", resultKV.ModRevision)
	}
	if resultKV.Version != 1 {
		t.Fatalf("expected Version 1, got %d", resultKV.Version)
	}
}

func TestAdapter_Put_EmptyKey(t *testing.T) {
	adapter := newTestAdapter(t)

	_, err := adapter.Put([]byte{}, []byte("value"), 0, 10)
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestAdapter_Put_WithLeaseID(t *testing.T) {
	adapter := newTestAdapter(t)

	result, err := adapter.Put([]byte("leased-key"), []byte("val"), 42, 5)
	if err != nil {
		t.Fatalf("Put with leaseID failed: %v", err)
	}

	resultKV := result.(*service.KeyValue)
	if resultKV.LeaseID != 42 {
		t.Fatalf("expected LeaseID 42, got %d", resultKV.LeaseID)
	}
}

func TestAdapter_Put_UpdateExistingKey(t *testing.T) {
	adapter := newTestAdapter(t)

	// First put
	_, err := adapter.Put([]byte("key"), []byte("v1"), 0, 5)
	if err != nil {
		t.Fatalf("first Put failed: %v", err)
	}

	// Second put (update)
	result, err := adapter.Put([]byte("key"), []byte("v2"), 0, 10)
	if err != nil {
		t.Fatalf("second Put failed: %v", err)
	}

	resultKV := result.(*service.KeyValue)
	if !bytes.Equal(resultKV.Value, []byte("v2")) {
		t.Fatalf("expected value 'v2', got %q", resultKV.Value)
	}
	if resultKV.Version != 2 {
		t.Fatalf("expected Version 2 after update, got %d", resultKV.Version)
	}
	if resultKV.CreateRevision != 5 {
		t.Fatalf("expected CreateRevision 5 (preserved from first put), got %d", resultKV.CreateRevision)
	}
	if resultKV.ModRevision != 10 {
		t.Fatalf("expected ModRevision 10, got %d", resultKV.ModRevision)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestAdapter_Delete_Success(t *testing.T) {
	adapter := newTestAdapter(t)

	// First put the key.
	_, err := adapter.Put([]byte("delkey"), []byte("delval"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Delete the key.
	result, err := adapter.Delete([]byte("delkey"), 10)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from Delete (previous KV)")
	}

	prevKV, ok := result.(*service.KeyValue)
	if !ok {
		t.Fatalf("expected *service.KeyValue, got %T", result)
	}
	if !bytes.Equal(prevKV.Key, []byte("delkey")) {
		t.Fatalf("expected key 'delkey', got %q", prevKV.Key)
	}
	if !bytes.Equal(prevKV.Value, []byte("delval")) {
		t.Fatalf("expected value 'delval', got %q", prevKV.Value)
	}
}

func TestAdapter_Delete_NonexistentKey(t *testing.T) {
	adapter := newTestAdapter(t)

	result, err := adapter.Delete([]byte("missing"), 10)
	if err != nil {
		t.Fatalf("Delete of nonexistent key should not error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for nonexistent key, got %v", result)
	}
}

func TestAdapter_Delete_EmptyKey(t *testing.T) {
	adapter := newTestAdapter(t)

	_, err := adapter.Delete([]byte{}, 10)
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestAdapter_Delete_ThenGet(t *testing.T) {
	adapter := newTestAdapter(t)

	// Put, then delete, then verify range returns empty.
	_, err := adapter.Put([]byte("ephemeral"), []byte("val"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	_, err = adapter.Delete([]byte("ephemeral"), 10)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Range for the deleted key should return empty.
	result, _, err := adapter.Range([]byte("ephemeral"), nil, 0, 1, false, false)
	if err != nil {
		t.Fatalf("Range after delete failed: %v", err)
	}
	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 0 {
		t.Fatalf("expected 0 results after delete, got %d", len(kvs))
	}
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

func TestAdapter_Range_Success(t *testing.T) {
	adapter := newTestAdapter(t)

	// Insert multiple keys.
	_, err := adapter.Put([]byte("a"), []byte("va"), 0, 1)
	if err != nil {
		t.Fatalf("Put 'a' failed: %v", err)
	}
	_, err = adapter.Put([]byte("b"), []byte("vb"), 0, 2)
	if err != nil {
		t.Fatalf("Put 'b' failed: %v", err)
	}
	_, err = adapter.Put([]byte("c"), []byte("vc"), 0, 3)
	if err != nil {
		t.Fatalf("Put 'c' failed: %v", err)
	}

	// Range [a, d) should return all three.
	result, rev, err := adapter.Range([]byte("a"), []byte("d"), 0, 0, false, false)
	if err != nil {
		t.Fatalf("Range failed: %v", err)
	}

	kvs, ok := result.([]*kv.KeyValue)
	if !ok {
		t.Fatalf("expected []*kv.KeyValue, got %T", result)
	}
	if len(kvs) != 3 {
		t.Fatalf("expected 3 results, got %d", len(kvs))
	}
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}
}

func TestAdapter_Range_SingleKey(t *testing.T) {
	adapter := newTestAdapter(t)

	_, err := adapter.Put([]byte("single"), []byte("sv"), 0, 1)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// nil end returns only the key matching start.
	result, _, err := adapter.Range([]byte("single"), nil, 0, 1, false, false)
	if err != nil {
		t.Fatalf("Range single key failed: %v", err)
	}

	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 1 {
		t.Fatalf("expected 1 result, got %d", len(kvs))
	}
	if !bytes.Equal(kvs[0].Key, []byte("single")) {
		t.Fatalf("expected key 'single', got %q", kvs[0].Key)
	}
}

func TestAdapter_Range_CountOnly(t *testing.T) {
	adapter := newTestAdapter(t)

	_, err := adapter.Put([]byte("x1"), []byte("v1"), 0, 1)
	if err != nil {
		t.Fatalf("Put x1 failed: %v", err)
	}
	_, err = adapter.Put([]byte("x2"), []byte("v2"), 0, 2)
	if err != nil {
		t.Fatalf("Put x2 failed: %v", err)
	}

	result, rev, err := adapter.Range([]byte("x1"), []byte("x3"), 0, 0, true, false)
	if err != nil {
		t.Fatalf("Range countOnly failed: %v", err)
	}

	count, ok := result.(int64)
	if !ok {
		t.Fatalf("expected int64 count, got %T", result)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}
}

func TestAdapter_Range_KeysOnly(t *testing.T) {
	adapter := newTestAdapter(t)

	_, err := adapter.Put([]byte("k1"), []byte("v1"), 0, 1)
	if err != nil {
		t.Fatalf("Put k1 failed: %v", err)
	}
	_, err = adapter.Put([]byte("k2"), []byte("v2"), 0, 2)
	if err != nil {
		t.Fatalf("Put k2 failed: %v", err)
	}

	result, _, err := adapter.Range([]byte("k1"), []byte("k3"), 0, 0, false, true)
	if err != nil {
		t.Fatalf("Range keysOnly failed: %v", err)
	}

	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 2 {
		t.Fatalf("expected 2 results, got %d", len(kvs))
	}
	for _, entry := range kvs {
		if entry.Value != nil {
			t.Fatalf("expected nil Value for keysOnly, got %q for key %q", entry.Value, entry.Key)
		}
	}
}

func TestAdapter_Range_WithLimit(t *testing.T) {
	adapter := newTestAdapter(t)

	for i := 0; i < 5; i++ {
		key := []byte{byte('a' + i)}
		_, err := adapter.Put(key, []byte("v"), 0, uint64(i+1))
		if err != nil {
			t.Fatalf("Put %q failed: %v", key, err)
		}
	}

	result, _, err := adapter.Range([]byte("a"), []byte("f"), 0, 2, false, false)
	if err != nil {
		t.Fatalf("Range with limit failed: %v", err)
	}

	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 2 {
		t.Fatalf("expected 2 results with limit, got %d", len(kvs))
	}
}

func TestAdapter_Range_EmptyResult(t *testing.T) {
	adapter := newTestAdapter(t)

	result, _, err := adapter.Range([]byte("nonexistent"), nil, 0, 1, false, false)
	if err != nil {
		t.Fatalf("Range for nonexistent key failed: %v", err)
	}

	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 0 {
		t.Fatalf("expected 0 results, got %d", len(kvs))
	}
}

// ---------------------------------------------------------------------------
// Compact
// ---------------------------------------------------------------------------

func TestAdapter_Compact_Success(t *testing.T) {
	adapter := newTestAdapter(t)

	// Put some data at various revisions.
	_, err := adapter.Put([]byte("ck"), []byte("v1"), 0, 1)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	_, err = adapter.Put([]byte("ck"), []byte("v2"), 0, 5)
	if err != nil {
		t.Fatalf("Put update failed: %v", err)
	}

	// Compact at revision 3 (should remove data entries below rev 3).
	err = adapter.Compact(3)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
}

func TestAdapter_Compact_RemovesOldRevisions(t *testing.T) {
	adapter := newTestAdapter(t)

	// Write at revisions 1 and 5.
	_, err := adapter.Put([]byte("ck"), []byte("old"), 0, 1)
	if err != nil {
		t.Fatalf("Put at rev 1 failed: %v", err)
	}
	_, err = adapter.Put([]byte("ck"), []byte("new"), 0, 5)
	if err != nil {
		t.Fatalf("Put at rev 5 failed: %v", err)
	}

	// Compact at revision 4 (removes rev-1 data).
	err = adapter.Compact(4)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// The latest value (rev 5) should still be readable.
	result, _, err := adapter.Range([]byte("ck"), nil, 0, 1, false, false)
	if err != nil {
		t.Fatalf("Range after compact failed: %v", err)
	}
	kvs := result.([]*kv.KeyValue)
	if len(kvs) != 1 {
		t.Fatalf("expected 1 result after compact, got %d", len(kvs))
	}
	if !bytes.Equal(kvs[0].Value, []byte("new")) {
		t.Fatalf("expected value 'new' after compact, got %q", kvs[0].Value)
	}
}

func TestAdapter_Compact_ZeroRevision(t *testing.T) {
	adapter := newTestAdapter(t)

	// Compact at revision 0 should be a no-op.
	err := adapter.Compact(0)
	if err != nil {
		t.Fatalf("Compact(0) failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Transaction
// ---------------------------------------------------------------------------

func TestAdapter_Transaction_Success(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate a key.
	_, err := adapter.Put([]byte("txkey"), []byte("txval"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Transaction: compare version == 1, then put a new value.
	compares := []statemachine.MVCCCompare{
		{
			Key:     []byte("txkey"),
			Target:  byte(service.CompareVersion),
			Result:  byte(service.CompareEqual),
			Version: 1,
		},
	}
	successOps := []statemachine.MVCCOp{
		{
			Type:  wireOpPut,
			Key:   []byte("txkey"),
			Value: []byte("newval"),
		},
	}

	succeeded, rev, responses, err := adapter.Transaction(compares, successOps, nil)
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected transaction to succeed (version == 1)")
	}
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}
	if responses == nil {
		t.Fatal("expected non-nil responses")
	}
}

func TestAdapter_Transaction_FailedCompare(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate a key.
	_, err := adapter.Put([]byte("txkey"), []byte("txval"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Transaction: compare version == 99 (should fail, actual is 1).
	compares := []statemachine.MVCCCompare{
		{
			Key:     []byte("txkey"),
			Target:  byte(service.CompareVersion),
			Result:  byte(service.CompareEqual),
			Version: 99,
		},
	}
	successOps := []statemachine.MVCCOp{
		{
			Type:  wireOpPut,
			Key:   []byte("txkey"),
			Value: []byte("success-val"),
		},
	}
	failureOps := []statemachine.MVCCOp{
		{
			Type:  wireOpPut,
			Key:   []byte("txkey"),
			Value: []byte("failure-val"),
		},
	}

	succeeded, rev, _, err := adapter.Transaction(compares, successOps, failureOps)
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}
	if succeeded {
		t.Fatal("expected transaction to fail (version != 99)")
	}
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}
}

func TestAdapter_Transaction_EmptyOps(t *testing.T) {
	adapter := newTestAdapter(t)

	// Transaction with no compares and no ops should succeed.
	succeeded, rev, responses, err := adapter.Transaction(nil, nil, nil)
	if err != nil {
		t.Fatalf("Transaction with empty ops failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected transaction with no compares to succeed")
	}
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}

	// Responses should be empty (no ops executed).
	respSlice, ok := responses.([]*kv.KeyValue)
	if !ok {
		t.Fatalf("expected []*kv.KeyValue, got %T", responses)
	}
	if len(respSlice) != 0 {
		t.Fatalf("expected 0 responses, got %d", len(respSlice))
	}
}

func TestAdapter_Transaction_DeleteOp(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate.
	_, err := adapter.Put([]byte("del-txn"), []byte("val"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Transaction: no compare, delete op in success.
	successOps := []statemachine.MVCCOp{
		{
			Type: wireOpDelete,
			Key:  []byte("del-txn"),
		},
	}

	succeeded, _, _, err := adapter.Transaction(nil, successOps, nil)
	if err != nil {
		t.Fatalf("Transaction with delete failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected transaction to succeed")
	}
}

func TestAdapter_Transaction_GetOp(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate.
	_, err := adapter.Put([]byte("get-txn"), []byte("val"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Transaction: no compare, get op in success.
	successOps := []statemachine.MVCCOp{
		{
			Type: wireOpGet,
			Key:  []byte("get-txn"),
		},
	}

	succeeded, _, responses, err := adapter.Transaction(nil, successOps, nil)
	if err != nil {
		t.Fatalf("Transaction with get failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected transaction to succeed")
	}

	respSlice := responses.([]*kv.KeyValue)
	if len(respSlice) != 1 {
		t.Fatalf("expected 1 response, got %d", len(respSlice))
	}
	if respSlice[0] == nil {
		t.Fatal("expected non-nil response for get op")
	}
	if !bytes.Equal(respSlice[0].Value, []byte("val")) {
		t.Fatalf("expected value 'val', got %q", respSlice[0].Value)
	}
}

func TestAdapter_Transaction_InvalidOpType(t *testing.T) {
	adapter := newTestAdapter(t)

	successOps := []statemachine.MVCCOp{
		{
			Type: 99, // invalid op type
			Key:  []byte("key"),
		},
	}

	_, _, _, err := adapter.Transaction(nil, successOps, nil)
	if err == nil {
		t.Fatal("expected error for invalid op type, got nil")
	}
}

func TestAdapter_Transaction_InvalidCompareTarget(t *testing.T) {
	adapter := newTestAdapter(t)

	compares := []statemachine.MVCCCompare{
		{
			Key:    []byte("key"),
			Target: 99, // invalid target
			Result: byte(service.CompareEqual),
		},
	}

	_, _, _, err := adapter.Transaction(compares, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid compare target, got nil")
	}
}

func TestAdapter_Transaction_InvalidCompareResult(t *testing.T) {
	adapter := newTestAdapter(t)

	compares := []statemachine.MVCCCompare{
		{
			Key:    []byte("key"),
			Target: byte(service.CompareVersion),
			Result: 99, // invalid result
		},
	}

	_, _, _, err := adapter.Transaction(compares, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid compare result, got nil")
	}
}

func TestAdapter_Transaction_ValueCompare(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate.
	_, err := adapter.Put([]byte("val-cmp"), []byte("expected"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Compare value == "expected" (should succeed).
	compares := []statemachine.MVCCCompare{
		{
			Key:    []byte("val-cmp"),
			Target: byte(service.CompareValue),
			Result: byte(service.CompareEqual),
			Value:  []byte("expected"),
		},
	}

	succeeded, _, _, err := adapter.Transaction(compares, nil, nil)
	if err != nil {
		t.Fatalf("Transaction with value compare failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected value compare to succeed")
	}
}

func TestAdapter_Transaction_CreateRevisionCompare(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate.
	_, err := adapter.Put([]byte("cr-cmp"), []byte("v"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Compare create revision == 5 (should succeed).
	compares := []statemachine.MVCCCompare{
		{
			Key:            []byte("cr-cmp"),
			Target:         byte(service.CompareCreateRevision),
			Result:         byte(service.CompareEqual),
			CreateRevision: 5,
		},
	}

	succeeded, _, _, err := adapter.Transaction(compares, nil, nil)
	if err != nil {
		t.Fatalf("Transaction with create revision compare failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected create revision compare to succeed")
	}
}

func TestAdapter_Transaction_ModRevisionCompare(t *testing.T) {
	adapter := newTestAdapter(t)

	// Pre-populate.
	_, err := adapter.Put([]byte("mr-cmp"), []byte("v"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Compare mod revision == 5 (should succeed).
	compares := []statemachine.MVCCCompare{
		{
			Key:         []byte("mr-cmp"),
			Target:      byte(service.CompareModRevision),
			Result:      byte(service.CompareEqual),
			ModRevision: 5,
		},
	}

	succeeded, _, _, err := adapter.Transaction(compares, nil, nil)
	if err != nil {
		t.Fatalf("Transaction with mod revision compare failed: %v", err)
	}
	if !succeeded {
		t.Fatal("expected mod revision compare to succeed")
	}
}

func TestAdapter_Transaction_AddCompareError(t *testing.T) {
	adapter := newTestAdapter(t)

	// A compare with an empty key is structurally valid for
	// MVCCCompare but fails RaftCompare.Validate() in AddCompare.
	compares := []statemachine.MVCCCompare{
		{
			Key:     nil, // empty key triggers validation error
			Target:  byte(service.CompareVersion),
			Result:  byte(service.CompareEqual),
			Version: 1,
		},
	}

	_, _, _, err := adapter.Transaction(compares, nil, nil)
	if err == nil {
		t.Fatal("expected error for compare with empty key, got nil")
	}
}

func TestAdapter_Transaction_InvalidFailureOps(t *testing.T) {
	adapter := newTestAdapter(t)

	// Valid success ops but invalid failure ops (bad op type).
	failureOps := []statemachine.MVCCOp{
		{
			Type: 77, // invalid op type
			Key:  []byte("key"),
		},
	}

	// No compares, so success path is taken. But we still need the
	// failure ops to be converted, so let's add a failing compare
	// to force the failure path.
	_, err := adapter.Put([]byte("key"), []byte("val"), 0, 5)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	compares := []statemachine.MVCCCompare{
		{
			Key:     []byte("key"),
			Target:  byte(service.CompareVersion),
			Result:  byte(service.CompareEqual),
			Version: 999, // will fail compare
		},
	}

	_, _, _, err = adapter.Transaction(compares, nil, failureOps)
	if err == nil {
		t.Fatal("expected error for invalid failure op type, got nil")
	}
}

func TestAdapter_Transaction_BeginTxnError(t *testing.T) {
	// Create adapter with closed storage so BeginRaftTxn fails
	// (Snapshot creation will fail).
	store := newMemStorage()
	log := newTestLogger(t)
	rs, err := mvcc.NewRaftStorage(&mvcc.RaftStorageConfig{
		Store:  store,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	adapter := NewAdapter(rs)

	// Close the storage to force Snapshot() to fail in BeginRaftTxn.
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	_, _, _, err = adapter.Transaction(nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from BeginRaftTxn with closed storage, got nil")
	}

	var storageErr *statemachine.StorageError
	if !goerrors.As(err, &storageErr) {
		t.Fatalf("expected *statemachine.StorageError, got %T: %v", err, err)
	}
	if storageErr.Op != "mvcc-txn-begin" {
		t.Fatalf("expected Op 'mvcc-txn-begin', got %q", storageErr.Op)
	}
}

func TestAdapter_Range_Error(t *testing.T) {
	// Create adapter with closed storage so Range fails.
	store := newMemStorage()
	log := newTestLogger(t)
	rs, err := mvcc.NewRaftStorage(&mvcc.RaftStorageConfig{
		Store:  store,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("NewRaftStorage: %v", err)
	}
	adapter := NewAdapter(rs)

	// Close the storage to force Range to fail.
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	_, _, err = adapter.Range([]byte("key"), nil, 0, 1, false, false)
	if err == nil {
		t.Fatal("expected error from Range with closed storage, got nil")
	}
}

// ---------------------------------------------------------------------------
// convertCompare
// ---------------------------------------------------------------------------

func TestConvertCompare_AllTargets(t *testing.T) {
	targets := []struct {
		name   string
		target byte
		want   mvcc.RaftCompareTarget
	}{
		{"version", byte(service.CompareVersion), mvcc.RaftCompareVersion},
		{"create", byte(service.CompareCreateRevision), mvcc.RaftCompareCreate},
		{"mod", byte(service.CompareModRevision), mvcc.RaftCompareMod},
		{"value", byte(service.CompareValue), mvcc.RaftCompareValue},
	}

	for _, tt := range targets {
		t.Run(tt.name, func(t *testing.T) {
			cmp := statemachine.MVCCCompare{
				Key:    []byte("k"),
				Target: tt.target,
				Result: byte(service.CompareEqual),
			}
			rc, err := convertCompare(cmp)
			if err != nil {
				t.Fatalf("convertCompare failed: %v", err)
			}
			if rc.Target != tt.want {
				t.Fatalf("target: got %d, want %d", rc.Target, tt.want)
			}
		})
	}
}

func TestConvertCompare_AllResults(t *testing.T) {
	results := []struct {
		name   string
		result byte
		want   mvcc.RaftCompareResult
	}{
		{"equal", byte(service.CompareEqual), mvcc.RaftCompareEqual},
		{"greater", byte(service.CompareGreater), mvcc.RaftCompareGreater},
		{"less", byte(service.CompareLess), mvcc.RaftCompareLess},
		{"notEqual", byte(service.CompareNotEqual), mvcc.RaftCompareNotEqual},
	}

	for _, tt := range results {
		t.Run(tt.name, func(t *testing.T) {
			cmp := statemachine.MVCCCompare{
				Key:    []byte("k"),
				Target: byte(service.CompareVersion),
				Result: tt.result,
			}
			rc, err := convertCompare(cmp)
			if err != nil {
				t.Fatalf("convertCompare failed: %v", err)
			}
			if rc.Result != tt.want {
				t.Fatalf("result: got %d, want %d", rc.Result, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// convertOps
// ---------------------------------------------------------------------------

func TestConvertOps_AllTypes(t *testing.T) {
	ops := []statemachine.MVCCOp{
		{Type: wireOpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: wireOpGet, Key: []byte("k2")},
		{Type: wireOpDelete, Key: []byte("k3")},
	}

	result, err := convertOps(ops)
	if err != nil {
		t.Fatalf("convertOps failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(result))
	}

	if result[0].Type != mvcc.RaftTxnOpPut {
		t.Fatalf("op[0]: expected RaftTxnOpPut, got %d", result[0].Type)
	}
	if result[1].Type != mvcc.RaftTxnOpGet {
		t.Fatalf("op[1]: expected RaftTxnOpGet, got %d", result[1].Type)
	}
	if result[2].Type != mvcc.RaftTxnOpDelete {
		t.Fatalf("op[2]: expected RaftTxnOpDelete, got %d", result[2].Type)
	}
}

func TestConvertOps_Empty(t *testing.T) {
	result, err := convertOps(nil)
	if err != nil {
		t.Fatalf("convertOps(nil) failed: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for empty ops, got %v", result)
	}
}

func TestConvertOps_InvalidType(t *testing.T) {
	ops := []statemachine.MVCCOp{
		{Type: 42, Key: []byte("k")},
	}

	_, err := convertOps(ops)
	if err == nil {
		t.Fatal("expected error for invalid op type, got nil")
	}
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

func TestNewMVCCFastCreateFunc(t *testing.T) {
	// Verify that NewMVCCFastCreateFunc produces a non-nil CreateFunc.
	storeFactory := func(_, _ uint64) storage.Storage {
		return newMemStorage()
	}
	log := newTestLogger(t)

	createFunc := NewMVCCFastCreateFunc(storeFactory, log)
	if createFunc == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
}

func TestNewMVCCFastCreateFunc_ProducesStateMachine(t *testing.T) {
	// Verify the CreateFunc produces a working state machine.
	storeFactory := func(_, _ uint64) storage.Storage {
		return newMemStorage()
	}
	log := newTestLogger(t)

	createFunc := NewMVCCFastCreateFunc(storeFactory, log)

	// CreateFunc returns a function that, when called with shard/replica
	// IDs, produces the actual StateMachine.
	smFactory := createFunc(1, 1)
	if smFactory == nil {
		t.Fatal("expected non-nil StateMachine from CreateFunc")
	}
}
