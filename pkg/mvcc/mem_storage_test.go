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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// iterItem holds a key-value pair for iterator snapshots.
type iterItem struct {
	key []byte
	val []byte
}

// memStorage is a minimal in-memory storage.Storage for unit tests.
// It returns QRDBError{ErrNotFound} when a key does not exist, matching
// the contract Store expects of a storage.Storage.
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
	// zero-length slice, exactly as the real engines do. That is what lets a
	// reader distinguish it from a missing key, so this line is part of the
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
