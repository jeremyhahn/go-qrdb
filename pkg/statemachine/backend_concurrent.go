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

	"github.com/cespare/xxhash/v2"
)

const kvConcurrentShardCount = 16

// Compile-time interface assertion.
var _ Backend = (*ConcurrentBackend)(nil)

// kvConcurrentShard is a single partition of the in-memory key-value data
// for ConcurrentBackend, protected by its own read-write mutex.
type kvConcurrentShard struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// ConcurrentBackend is a 16-shard in-memory key-value backend. Writes
// lock only the target shard, enabling high concurrency for concurrent
// state machine lookups.
type ConcurrentBackend struct {
	shards [kvConcurrentShardCount]kvConcurrentShard
}

// NewConcurrentBackend creates a new ConcurrentBackend with initialized
// shard maps.
func NewConcurrentBackend() *ConcurrentBackend {
	b := &ConcurrentBackend{}
	for i := range b.shards {
		b.shards[i].data = make(map[string][]byte)
	}
	return b
}

// shardFor returns the shard for the given key using xxhash64 with
// bitmask selection.
func (b *ConcurrentBackend) shardFor(key string) *kvConcurrentShard {
	h := xxhash.Sum64String(key)
	return &b.shards[h&(kvConcurrentShardCount-1)]
}

// Get retrieves the value for the given key from its shard.
func (b *ConcurrentBackend) Get(key []byte) ([]byte, error) {
	k := string(key)
	s := b.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if !found {
		return nil, nil
	}

	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, nil
}

// Put stores a key-value pair in the appropriate shard.
func (b *ConcurrentBackend) Put(key, value []byte) error {
	k := string(key)
	owned := make([]byte, len(value))
	copy(owned, value)

	s := b.shardFor(k)
	s.mu.Lock()
	s.data[k] = owned
	s.mu.Unlock()
	return nil
}

// Delete removes a key from the appropriate shard.
func (b *ConcurrentBackend) Delete(key []byte) error {
	k := string(key)
	s := b.shardFor(k)

	s.mu.Lock()
	delete(s.data, k)
	s.mu.Unlock()
	return nil
}

// List returns all keys matching the given prefix across all shards.
func (b *ConcurrentBackend) List(prefix []byte) ([][]byte, error) {
	p := string(prefix)

	var keys [][]byte
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k := range s.data {
			if len(prefix) == 0 || strings.HasPrefix(k, p) {
				keys = append(keys, []byte(k))
			}
		}
		s.mu.RUnlock()
	}

	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix across all
// shards. When limit > 0, the scan stops after collecting that many entries.
func (b *ConcurrentBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	p := string(prefix)
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !strings.HasPrefix(k, p) {
				continue
			}
			cp := make([]byte, len(v))
			copy(cp, v)
			result[k] = cp
			if len(result) >= limit {
				s.mu.RUnlock()
				return result, nil
			}
		}
		s.mu.RUnlock()
	}

	return result, nil
}

// SaveSnapshot captures a consistent point-in-time deep copy of all
// shards by locking them in order, then serializes to the writer.
func (b *ConcurrentBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	// Lock all shards in order.
	for i := range b.shards {
		b.shards[i].mu.RLock()
	}

	clone := make(map[string][]byte)
	for i := range b.shards {
		for k, v := range b.shards[i].data {
			cp := make([]byte, len(v))
			copy(cp, v)
			clone[k] = cp
		}
	}

	for i := range b.shards {
		b.shards[i].mu.RUnlock()
	}

	// Write entry count.
	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(clone)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	buf := make([]byte, 4)
	for k, v := range clone {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		keyBytes := []byte(k)
		binary.LittleEndian.PutUint32(buf, uint32(len(keyBytes)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(keyBytes); err != nil {
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

// RecoverSnapshot clears all shards and rebuilds from the reader.
func (b *ConcurrentBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	// Clear all shards.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		s.data = make(map[string][]byte)
		s.mu.Unlock()
	}

	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, countBuf); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
	}
	entryCount := binary.LittleEndian.Uint32(countBuf)

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

		key := string(keyBuf)
		s := b.shardFor(key)
		s.mu.Lock()
		s.data[key] = valBuf
		s.mu.Unlock()
	}

	return nil
}

// GetHash computes a deterministic xxhash64 hash over all key-value
// pairs across all shards in sorted key order.
func (b *ConcurrentBackend) GetHash() (uint64, error) {
	h := xxhash.New()

	var allKeys []string
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k := range s.data {
			allKeys = append(allKeys, k)
		}
		s.mu.RUnlock()
	}
	sort.Strings(allKeys)

	for _, k := range allKeys {
		s := b.shardFor(k)
		s.mu.RLock()
		_, _ = h.Write([]byte(k))
		_, _ = h.Write(s.data[k])
		s.mu.RUnlock()
	}

	return h.Sum64(), nil
}

// Close clears all shard data.
func (b *ConcurrentBackend) Close() error {
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		s.data = nil
		s.mu.Unlock()
	}
	return nil
}
