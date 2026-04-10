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
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// testMemStorage is an in-memory implementation of storage.Storage for unit tests.
type testMemStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newTestMemStorage() *testMemStorage {
	return &testMemStorage{data: make(map[string][]byte)}
}

func (m *testMemStorage) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, errors.NotFound("testMemStorage.Get", string(key))
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *testMemStorage) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[string(key)] = cp
	return nil
}

func (m *testMemStorage) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

func (m *testMemStorage) Batch() storage.Batch {
	return &testMemBatch{store: m}
}

func (m *testMemStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var keys []string
	for k := range m.data {
		if opts != nil {
			if opts.LowerBound != nil && k < string(opts.LowerBound) {
				continue
			}
			if opts.UpperBound != nil && k >= string(opts.UpperBound) {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]testKVPair, len(keys))
	for i, k := range keys {
		v := m.data[k]
		vc := make([]byte, len(v))
		copy(vc, v)
		entries[i] = testKVPair{key: []byte(k), value: vc}
	}

	return &testMemIterator{entries: entries, pos: -1}
}

func (m *testMemStorage) Snapshot() (storage.Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := &testMemSnapshot{data: make(map[string][]byte, len(m.data))}
	for k, v := range m.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		snap.data[k] = cp
	}
	return snap, nil
}

func (m *testMemStorage) Close() error {
	return nil
}

func (m *testMemStorage) Exists(key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *testMemStorage) List(prefix []byte) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys [][]byte
	p := string(prefix)
	for k := range m.data {
		if len(p) == 0 || len(k) >= len(p) && k[:len(p)] == p {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

// testMemBatch implements storage.Batch for testMemStorage.
type testMemBatch struct {
	store *testMemStorage
	ops   []func()
}

func (b *testMemBatch) Set(key, value []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	v := make([]byte, len(value))
	copy(v, value)
	b.ops = append(b.ops, func() {
		b.store.data[string(k)] = v
	})
	return nil
}

func (b *testMemBatch) Delete(key []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	b.ops = append(b.ops, func() {
		delete(b.store.data, string(k))
	})
	return nil
}

func (b *testMemBatch) Commit() error {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	for _, op := range b.ops {
		op()
	}
	return nil
}

func (b *testMemBatch) Rollback() error {
	b.ops = nil
	return nil
}

func (b *testMemBatch) Close() error {
	b.ops = nil
	return nil
}

// testKVPair holds a single key-value pair for the in-memory iterator.
type testKVPair struct {
	key   []byte
	value []byte
}

// testMemIterator implements storage.Iterator over a sorted slice.
type testMemIterator struct {
	entries []testKVPair
	pos     int
	err     error
}

func (it *testMemIterator) SeekToFirst() bool {
	if len(it.entries) == 0 {
		return false
	}
	it.pos = 0
	return true
}

func (it *testMemIterator) SeekToLast() bool {
	if len(it.entries) == 0 {
		return false
	}
	it.pos = len(it.entries) - 1
	return true
}

func (it *testMemIterator) Seek(target []byte) bool {
	for i, e := range it.entries {
		if string(e.key) >= string(target) {
			it.pos = i
			return true
		}
	}
	return false
}

func (it *testMemIterator) Next() bool {
	it.pos++
	return it.pos < len(it.entries)
}

func (it *testMemIterator) Prev() bool {
	it.pos--
	return it.pos >= 0
}

func (it *testMemIterator) Valid() bool {
	return it.pos >= 0 && it.pos < len(it.entries)
}

func (it *testMemIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.entries[it.pos].key
}

func (it *testMemIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	return it.entries[it.pos].value
}

func (it *testMemIterator) Error() error {
	return it.err
}

func (it *testMemIterator) Close() error {
	return nil
}

// testMemSnapshot implements storage.Snapshot.
type testMemSnapshot struct {
	data map[string][]byte
}

func (s *testMemSnapshot) Get(key []byte) ([]byte, error) {
	v, ok := s.data[string(key)]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (s *testMemSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	var keys []string
	for k := range s.data {
		if opts != nil {
			if opts.LowerBound != nil && k < string(opts.LowerBound) {
				continue
			}
			if opts.UpperBound != nil && k >= string(opts.UpperBound) {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]testKVPair, len(keys))
	for i, k := range keys {
		v := s.data[k]
		vc := make([]byte, len(v))
		copy(vc, v)
		entries[i] = testKVPair{key: []byte(k), value: vc}
	}

	return &testMemIterator{entries: entries, pos: -1}
}

func (s *testMemSnapshot) Close() error {
	return nil
}

// createRevisionTestStore creates a testMemStorage for revision manager tests.
func createRevisionTestStore() storage.Storage {
	return newTestMemStorage()
}

func TestNewRevisionManager(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)
	assert.NotNil(t, rm)
	assert.Equal(t, int64(1), rm.getCurrent())
}

func TestRevisionManager_Next(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// First next should be 2
	rev := rm.next()
	assert.Equal(t, int64(2), rev)

	// Second next should be 3
	rev = rm.next()
	assert.Equal(t, int64(3), rev)

	// Current should match
	assert.Equal(t, int64(3), rm.getCurrent())
}

func TestRevisionManager_GetCurrent(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	assert.Equal(t, int64(1), rm.getCurrent())

	rm.next()
	assert.Equal(t, int64(2), rm.getCurrent())
}

func TestRevisionManager_Persist(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Advance revision
	rm.next()
	rm.next()
	rm.next()
	assert.Equal(t, int64(4), rm.getCurrent())

	// Persist
	err = rm.persist()
	require.NoError(t, err)

	// Create new manager - should load persisted revision
	rm2, err := newRevisionManager(store)
	require.NoError(t, err)
	assert.Equal(t, int64(4), rm2.getCurrent())
}

func TestRevisionManager_PersistInBatch(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Advance revision
	rm.next() // 2
	rm.next() // 3

	// Create batch and add revision
	batch := store.Batch()
	err = rm.persistInBatch(batch)
	require.NoError(t, err)

	// Commit batch
	err = batch.Commit()
	require.NoError(t, err)
	_ = batch.Close()

	// Verify revision was persisted (should be 3)
	rm2, err := newRevisionManager(store)
	require.NoError(t, err)
	assert.Equal(t, int64(3), rm2.getCurrent())
}

func TestRevisionManager_IsDirty(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Should not be dirty initially (just persisted in constructor)
	assert.False(t, rm.isDirty())

	// Should be dirty after next
	rm.next()
	assert.True(t, rm.isDirty())

	// Should not be dirty after persist
	err = rm.persist()
	require.NoError(t, err)
	assert.False(t, rm.isDirty())
}

func TestRevisionManager_Sync(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Sync when clean should succeed
	err = rm.sync(context.Background())
	require.NoError(t, err)

	// Make dirty and sync
	rm.next()
	err = rm.sync(context.Background())
	require.NoError(t, err)
	assert.False(t, rm.isDirty())
}

func TestRevisionManager_Sync_ContextCanceled(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	rm.next()

	// Canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = rm.sync(ctx)
	assert.Error(t, err)
	assert.True(t, errors.IsCanceled(err))
}

func TestRevisionManager_SetIfGreater(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Initial is 1
	assert.Equal(t, int64(1), rm.getCurrent())

	// Set to 10
	rm.setIfGreater(10)
	assert.Equal(t, int64(10), rm.getCurrent())
	assert.True(t, rm.isDirty())

	// Try to set to 5 (should not change)
	rm.setIfGreater(5)
	assert.Equal(t, int64(10), rm.getCurrent())

	// Set to 15
	rm.setIfGreater(15)
	assert.Equal(t, int64(15), rm.getCurrent())
}

func TestRevisionManager_Reset(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Advance to 5
	for i := 0; i < 4; i++ {
		rm.next()
	}
	assert.Equal(t, int64(5), rm.getCurrent())

	// Reset to 100
	err = rm.reset(100)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rm.getCurrent())

	// Verify persisted
	rm2, err := newRevisionManager(store)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rm2.getCurrent())
}

func TestRevisionManager_ConcurrentNext(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Multiple goroutines incrementing
	count := 100
	var wg sync.WaitGroup
	wg.Add(count)

	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			rm.next()
		}()
	}

	wg.Wait()

	// Should be 1 + 100 = 101
	assert.Equal(t, int64(101), rm.getCurrent())
}

func TestRevisionManager_PersistNoChange(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Persist without changes - should be no-op
	err = rm.persist()
	require.NoError(t, err)
	assert.False(t, rm.isDirty())
}

func TestRevisionManager_LoadExisting(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	// Create and advance first manager
	rm1, err := newRevisionManager(store)
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		rm1.next()
	}
	err = rm1.persist()
	require.NoError(t, err)

	// Create second manager - should load existing
	rm2, err := newRevisionManager(store)
	require.NoError(t, err)
	assert.Equal(t, int64(11), rm2.getCurrent())
}

func TestRevisionManager_ConcurrentSetIfGreater(t *testing.T) {
	store := createRevisionTestStore()
	defer func() { _ = store.Close() }()

	rm, err := newRevisionManager(store)
	require.NoError(t, err)

	// Multiple goroutines trying to set different values
	var wg sync.WaitGroup
	count := 50
	wg.Add(count)

	for i := 0; i < count; i++ {
		go func(val int) {
			rm.setIfGreater(int64(val * 10))
			wg.Done()
		}(i)
	}

	wg.Wait()

	// Should be the max value set
	assert.Equal(t, int64((count-1)*10), rm.getCurrent())
}
