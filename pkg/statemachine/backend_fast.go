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
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const kvFastShardCount = 256

// Compile-time interface assertion.
//
// FastBackend deliberately does NOT implement GuardedWriteBackend, and this is
// the one Backend in the tree that does not. Its writes land in the cache
// synchronously and then persist on a background goroutine that is ordered
// against nothing and whose error is swallowed, so it can satisfy none of that
// interface's three promises. The guarded verbs therefore refuse on it with a
// typed GuardedWriteUnsupportedError rather than reporting a success the disk
// may contradict.
//
// Measured deterministically on this backend, with ORDINARY writes and no
// guarded verb involved (go-qrdb#224):
//
//	Put(k,"old"); Put(k,"new")  -> storage saw [set:k=new set:k=old], durable value "old"
//	Put(k,"v");   Delete(k)     -> storage saw [delete:k set:k=v], key PRESENT with "v"
//
// FastStateMachine and ConcurrentStateMachine do not have this problem: they
// own their storage and draw a persistOrder ticket under the shard lock, where
// apply order is already established (go-qrdb#86). This backend never received
// that fix. Giving it one is go-qrdb#216; do not paper over it here by making
// Put synchronous, which would change the asynchronous contract this backend
// exists to provide.
var _ Backend = (*FastBackend)(nil)

// kvFastShard is a single partition of the in-memory cache for
// FastBackend, protected by a read-write mutex. A nil value entry
// serves as a tombstone for a deleted key.
type kvFastShard struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// FastBackend is a 256-shard in-memory cache backed by persistent
// storage. Writes land in the cache first (for fast reads) and are
// flushed asynchronously to the underlying storage.Storage. This
// mirrors the FastStateMachine's storage model.
type FastBackend struct {
	shardID   uint64
	replicaID uint64
	storage   storage.Storage
	shards    [kvFastShardCount]kvFastShard
	wg        sync.WaitGroup
	logger    *slog.Logger
}

// NewFastBackend creates a new FastBackend with the given identity,
// backing storage, and logger.
func NewFastBackend(shardID, replicaID uint64, store storage.Storage, logger *slog.Logger) *FastBackend {
	if logger == nil {
		logger = slog.Default()
	}
	b := &FastBackend{
		shardID:   shardID,
		replicaID: replicaID,
		storage:   store,
		logger:    logger,
	}
	for i := range b.shards {
		b.shards[i].data = make(map[string][]byte)
	}
	return b
}

// shardFor returns the cache shard for the given key.
func (b *FastBackend) shardFor(key string) *kvFastShard {
	h := xxhash.Sum64String(key)
	return &b.shards[h%kvFastShardCount]
}

// Get retrieves the value for the given key. It checks the cache first
// and falls back to the backing storage on cache miss.
func (b *FastBackend) Get(key []byte) ([]byte, error) {
	k := string(key)
	s := b.shardFor(k)

	s.mu.RLock()
	val, found := s.data[k]
	s.mu.RUnlock()

	if found {
		if val == nil {
			return nil, nil // tombstone
		}
		cp := make([]byte, len(val))
		copy(cp, val)
		return cp, nil
	}

	// Cache miss: fall back to storage.
	stored, err := b.storage.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	return stored, nil
}

// Put writes to the cache shard and asynchronously persists to storage.
func (b *FastBackend) Put(key, value []byte) error {
	k := string(key)
	owned := make([]byte, len(value))
	copy(owned, value)

	s := b.shardFor(k)
	s.mu.Lock()
	s.data[k] = owned
	s.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := b.storage.Set([]byte(k), owned); err != nil {
			b.logger.Error("async storage set failed",
				"key", k,
				"error", err,
				"shard_id", b.shardID,
				"replica_id", b.replicaID)
		}
	}()

	return nil
}

// Delete sets a tombstone in the cache and asynchronously deletes from
// storage.
func (b *FastBackend) Delete(key []byte) error {
	k := string(key)
	s := b.shardFor(k)

	s.mu.Lock()
	s.data[k] = nil // tombstone
	s.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := b.storage.Delete([]byte(k)); err != nil {
			b.logger.Error("async storage delete failed",
				"key", k,
				"error", err,
				"shard_id", b.shardID,
				"replica_id", b.replicaID)
		}
	}()

	return nil
}

// List returns all keys matching the given prefix by merging storage
// and cache overlay. Tombstoned keys are excluded.
func (b *FastBackend) List(prefix []byte) ([][]byte, error) {
	p := string(prefix)
	seen := make(map[string]struct{})

	// Phase 1: scan storage via iterator.
	var iterOpts *storage.IteratorOptions
	if len(prefix) > 0 {
		upper := prefixUpperBound(prefix)
		iterOpts = &storage.IteratorOptions{LowerBound: prefix}
		if upper != nil {
			iterOpts.UpperBound = upper
		}
	}

	iter := b.storage.NewIterator(iterOpts)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		if p == "" || strings.HasPrefix(k, p) {
			seen[k] = struct{}{}
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "list-keys", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "list-keys-close", Inner: err}
	}

	// Phase 2: overlay cache entries.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if p != "" && !strings.HasPrefix(k, p) {
				continue
			}
			if v == nil {
				delete(seen, k)
			} else {
				seen[k] = struct{}{}
			}
		}
		s.mu.RUnlock()
	}

	keys := make([][]byte, 0, len(seen))
	for k := range seen {
		keys = append(keys, []byte(k))
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix by merging
// storage and cache overlay. Tombstoned keys are excluded.
func (b *FastBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	p := string(prefix)
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)

	// Phase 1: scan storage via iterator.
	upperBound := prefixUpperBound(prefix)
	opts := &storage.IteratorOptions{LowerBound: prefix}
	if upperBound != nil {
		opts.UpperBound = upperBound
	}

	iter := b.storage.NewIterator(opts)
	for iter.Seek(prefix); iter.Valid() && len(result) < limit; iter.Next() {
		k := string(iter.Key())
		if !strings.HasPrefix(k, p) {
			break
		}
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[k] = v
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return nil, &StorageError{Op: "prefix-scan", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "prefix-scan-close", Inner: err}
	}

	// Phase 2: overlay cache entries.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !strings.HasPrefix(k, p) {
				continue
			}
			if v == nil {
				delete(result, k)
			} else if len(result) < limit {
				result[k] = v
			}
		}
		s.mu.RUnlock()
	}

	return result, nil
}

// SaveSnapshot serializes the in-memory cache to the writer.
func (b *FastBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	type entry struct {
		key   string
		value []byte
	}
	var entries []entry

	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				entries = append(entries, entry{key: k, value: v})
			}
		}
		s.mu.RUnlock()

		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}
	}

	countBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(countBuf, uint32(len(entries)))
	if _, err := w.Write(countBuf); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	buf := make([]byte, 4)
	for _, e := range entries {
		select {
		case <-stopper:
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(e.key)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write([]byte(e.key)); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}

		binary.LittleEndian.PutUint32(buf, uint32(len(e.value)))
		if _, err := w.Write(buf); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
		if _, err := w.Write(e.value); err != nil {
			return &SnapshotError{Op: "save", Inner: err}
		}
	}

	return nil
}

// RecoverSnapshot replaces the cache and storage with data from the
// reader.
func (b *FastBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	// Wait for in-flight async persistence.
	b.wg.Wait()

	// Clear all cache shards.
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		s.data = make(map[string][]byte)
		s.mu.Unlock()
	}

	// Clear storage.
	if err := b.clearStorage(); err != nil {
		return &SnapshotError{Op: "recover", Inner: err}
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

		// Persist synchronously.
		if err := b.storage.Set(keyBuf, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
	}

	return nil
}

// GetHash computes a deterministic xxhash64 hash over non-tombstone
// cache entries in sorted key order.
func (b *FastBackend) GetHash() (uint64, error) {
	h := xxhash.New()

	var allKeys []string
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if v != nil {
				allKeys = append(allKeys, k)
			}
		}
		s.mu.RUnlock()
	}
	sort.Strings(allKeys)

	for _, k := range allKeys {
		s := b.shardFor(k)
		s.mu.RLock()
		_, _ = h.Write([]byte(k))
		if v := s.data[k]; v != nil {
			_, _ = h.Write(v)
		}
		s.mu.RUnlock()
	}

	return h.Sum64(), nil
}

// Close waits for async persistence and releases resources.
func (b *FastBackend) Close() error {
	b.wg.Wait()
	return nil
}

// clearStorage removes all existing keys from the backing storage.
func (b *FastBackend) clearStorage() error {
	iter := b.storage.NewIterator(nil)
	var keys [][]byte
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		keys = append(keys, k)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return err
	}
	if err := iter.Close(); err != nil {
		return err
	}

	for _, k := range keys {
		if err := b.storage.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
