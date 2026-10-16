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
	"context"
	"encoding/binary"
	"io"
	"sort"
	"strings"
	"sync"
)

// Compile-time interface assertion.
var _ Backend = (*StandardBackend)(nil)

// StandardBackend is a simple in-memory key-value backend backed by a
// single map protected by a read-write mutex. It mirrors the storage
// model of RegularStateMachine.
type StandardBackend struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewStandardBackend creates a new StandardBackend with an empty map.
func NewStandardBackend() *StandardBackend {
	return &StandardBackend{
		data: make(map[string][]byte),
	}
}

// Get retrieves the value for the given key. Returns a defensive copy.
func (b *StandardBackend) Get(key []byte) ([]byte, error) {
	b.mu.RLock()
	val, found := b.data[string(key)]
	b.mu.RUnlock()

	if !found {
		return nil, nil
	}

	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// Put stores a key-value pair. The value is defensively copied.
func (b *StandardBackend) Put(key, value []byte) error {
	owned := make([]byte, len(value))
	copy(owned, value)

	b.mu.Lock()
	b.data[string(key)] = owned
	b.mu.Unlock()
	return nil
}

// Delete removes a key from the map.
func (b *StandardBackend) Delete(key []byte) error {
	b.mu.Lock()
	delete(b.data, string(key))
	b.mu.Unlock()
	return nil
}

// List returns all keys matching the given prefix. An empty prefix
// returns all keys.
func (b *StandardBackend) List(prefix []byte) ([][]byte, error) {
	p := string(prefix)

	b.mu.RLock()
	defer b.mu.RUnlock()

	var keys [][]byte
	for k := range b.data {
		if len(prefix) == 0 || strings.HasPrefix(k, p) {
			keys = append(keys, []byte(k))
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix. When
// limit > 0, the scan stops after collecting that many entries.
func (b *StandardBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	p := string(prefix)
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)

	b.mu.RLock()
	for k, v := range b.data {
		if !strings.HasPrefix(k, p) {
			continue
		}
		cp := make([]byte, len(v))
		copy(cp, v)
		result[k] = cp
		if len(result) >= limit {
			break
		}
	}
	b.mu.RUnlock()

	return result, nil
}

// SaveSnapshot serializes the map to the writer under a read lock.
//
// Format: [entryCount:4 LE][entries...]
// Each entry: [keyLen:4 LE][key][valueLen:4 LE][value].
func (b *StandardBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	// Deep-copy under read lock.
	b.mu.RLock()
	clone := make(map[string][]byte, len(b.data))
	for k, v := range b.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		clone[k] = cp
	}
	b.mu.RUnlock()

	// Write entry count.
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(clone)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	// Write each entry.
	buf := make([]byte, 4)
	for k, v := range clone {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(k)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write([]byte(k)); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(v)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(v); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
	}

	return nil
}

// RecoverSnapshot replaces the map with data from the reader.
func (b *StandardBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, countBuf); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}
	entryCount := binary.LittleEndian.Uint32(countBuf)

	newData := make(map[string][]byte, int(entryCount))
	lenBuf := make([]byte, 4)

	for i := uint32(0); i < entryCount; i++ {
		select {
		case <-stopper:
			return &SnapshotError{Op: "recover", Inner: context.Canceled}
		default:
		}

		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		keyLen := binary.LittleEndian.Uint32(lenBuf)
		keyBuf := make([]byte, keyLen)
		if _, err := io.ReadFull(r, keyBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
		valLen := binary.LittleEndian.Uint32(lenBuf)
		valBuf := make([]byte, valLen)
		if _, err := io.ReadFull(r, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}

		newData[string(keyBuf)] = valBuf
	}

	b.mu.Lock()
	b.data = newData
	b.mu.Unlock()

	return nil
}

// GetHash computes a deterministic xxhash64 hash over all key-value
// pairs, visited in ascending key order.
func (b *StandardBackend) GetHash() (uint64, error) {
	return xxhashRecords(b.Records)
}

// Records visits every key-value pair in ascending key order under the
// backend's read lock.
func (b *StandardBackend) Records(fn RecordVisitor) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	recs := make([]kvRecord, 0, len(b.data))
	for k, v := range b.data {
		recs = append(recs, kvRecord{key: k, value: v})
	}
	return visitSorted(recs, fn)
}

// Close releases all resources. For the standard backend this clears
// the map.
func (b *StandardBackend) Close() error {
	b.mu.Lock()
	b.data = nil
	b.mu.Unlock()
	return nil
}

// PutDurable delegates to Put, and DeleteDurable to Delete.
//
// This backend is a plain map under a mutex: the write has taken effect before
// Put returns, it cannot fail, and there is no asynchronous path for an
// earlier write to arrive late on. All three properties Backend's guarded pair
// asks for therefore already hold of the unguarded pair, so delegating is the
// honest implementation rather than a shortcut.
//
// There is no durability to promise because there is no storage. That
// SATISFIES the contract rather than dodging it: what the guarded caller needs
// is that nothing can contradict the acknowledgement afterwards, and on a
// process-local map nothing can.
func (b *StandardBackend) PutDurable(key, value []byte) error { return b.Put(key, value) }

// DeleteDurable delegates to Delete — see PutDurable.
func (b *StandardBackend) DeleteDurable(key []byte) error { return b.Delete(key) }

// ApplyAtomic applies the whole set under ONE acquisition of the map's write
// lock, so a concurrent reader sees the map before the set or after it and
// never in between. Values are copied before the lock is taken, which is also
// the only step that could otherwise fail part-way: nothing inside the lock
// can, so there is no partial state to roll back.
func (b *StandardBackend) ApplyAtomic(muts []Mutation) error {
	owned := ownMutations(muts)

	b.mu.Lock()
	for _, m := range owned {
		if m.Delete {
			delete(b.data, string(m.Key))
			continue
		}
		b.data[string(m.Key)] = m.Value
	}
	b.mu.Unlock()
	return nil
}
