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

const defaultHighPerfShardCount = 256

// Compile-time interface assertion.
var _ Backend = (*HighPerfBackend)(nil)

// HighPerfConfig configures a HighPerfBackend at construction time.
type HighPerfConfig struct {
	// ShardCount is the number of shards. Must be a power of 2.
	// Defaults to 256 when zero or invalid.
	ShardCount int
}

// highPerfShard is a single partition of the in-memory key-value data
// for HighPerfBackend, protected by its own read-write mutex.
type highPerfShard struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// HighPerfBackend is a high-performance in-memory key-value backend with
// configurable shard count. It combines the concurrency model of
// ConcurrentBackend (allowing concurrent reads during writes) with the
// high shard count of FastBackend, but without any disk persistence.
//
// The shard count must be a power of 2 to enable bitmask-based shard
// selection (O(1) with no modulo). This backend is designed for
// data-plane state such as connection tracking and NAT session tables
// where low-latency in-memory access is critical and durability is
// handled by Raft replication.
type HighPerfBackend struct {
	shards    []highPerfShard
	shardMask uint64
}

// NewHighPerfBackend creates a new HighPerfBackend with the given
// configuration. When cfg is nil or ShardCount is not a valid power
// of 2, the default of 256 shards is used.
func NewHighPerfBackend(cfg *HighPerfConfig) *HighPerfBackend {
	shardCount := defaultHighPerfShardCount
	if cfg != nil && cfg.ShardCount > 0 && isPowerOfTwo(cfg.ShardCount) {
		shardCount = cfg.ShardCount
	}

	b := &HighPerfBackend{
		shards:    make([]highPerfShard, shardCount),
		shardMask: uint64(shardCount - 1),
	}
	for i := range b.shards {
		b.shards[i].data = make(map[string][]byte)
	}
	return b
}

// isPowerOfTwo returns true if n is a positive power of 2.
func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

// ShardCount returns the number of shards in this backend.
func (b *HighPerfBackend) ShardCount() int {
	return len(b.shards)
}

// shardFor returns the shard for the given key using xxhash64 with
// bitmask selection for O(1) constant-time lookup.
func (b *HighPerfBackend) shardFor(key string) *highPerfShard {
	h := xxhash.Sum64String(key)
	return &b.shards[h&b.shardMask]
}

// Get retrieves the value for the given key from its shard. Returns a
// defensive copy to prevent callers from mutating internal state.
func (b *HighPerfBackend) Get(key []byte) ([]byte, error) {
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

// Put stores a key-value pair in the appropriate shard. The value is
// defensively copied to prevent external mutation.
func (b *HighPerfBackend) Put(key, value []byte) error {
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
func (b *HighPerfBackend) Delete(key []byte) error {
	k := string(key)
	s := b.shardFor(k)

	s.mu.Lock()
	delete(s.data, k)
	s.mu.Unlock()
	return nil
}

// List returns all keys matching the given prefix across all shards,
// sorted lexicographically.
func (b *HighPerfBackend) List(prefix []byte) ([][]byte, error) {
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
// shards. When limit > 0, the scan stops after collecting that many
// entries.
func (b *HighPerfBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
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
//
// Format: [entryCount:4 LE][entries...]
// Each entry: [keyLen:4 LE][key][valueLen:4 LE][value].
func (b *HighPerfBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	// Lock all shards in order for a consistent snapshot.
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
func (b *HighPerfBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
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
func (b *HighPerfBackend) GetHash() (uint64, error) {
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

// Close clears all shard data and releases resources.
func (b *HighPerfBackend) Close() error {
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		s.data = nil
		s.mu.Unlock()
	}
	return nil
}

// BytesLookup implements sm.NALookup for zero-allocation point lookups.
// It returns the raw value bytes without interface{} boxing.
func (b *HighPerfBackend) BytesLookup(key []byte) ([]byte, error) {
	return b.Get(key)
}

// BytesLookupInto implements sm.NALookupInto for zero-allocation point
// lookups with caller-provided destination buffer.
func (b *HighPerfBackend) BytesLookupInto(key, dst []byte) ([]byte, error) {
	k := string(key)
	s := b.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if !found {
		return nil, nil
	}

	if cap(dst) >= len(val) {
		dst = dst[:len(val)]
	} else {
		dst = make([]byte, len(val))
	}
	copy(dst, val)
	return dst, nil
}

// PutDurable delegates to Put, and DeleteDurable to Delete, for the same
// reason StandardBackend's do: this backend is in-memory only, so a write has
// landed by the time Put or Delete returns and nothing arrives later to
// overwrite it.
func (b *HighPerfBackend) PutDurable(key, value []byte) error { return b.Put(key, value) }

// DeleteDurable delegates to Delete — see PutDurable.
func (b *HighPerfBackend) DeleteDurable(key []byte) error { return b.Delete(key) }
