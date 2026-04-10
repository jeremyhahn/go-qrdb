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

	"github.com/cespare/xxhash/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ Backend = (*DiskBackend)(nil)

// DiskBackend wraps a storage.Storage directly for synchronous disk I/O.
// All operations delegate to the underlying storage without any caching.
type DiskBackend struct {
	store storage.Storage
}

// NewDiskBackend creates a new DiskBackend wrapping the given storage.
func NewDiskBackend(store storage.Storage) *DiskBackend {
	return &DiskBackend{store: store}
}

// Get retrieves the value for the given key from storage.
func (b *DiskBackend) Get(key []byte) ([]byte, error) {
	val, err := b.store.Get(key)
	if err != nil {
		return nil, &StorageError{Op: "get", Inner: err}
	}
	return val, nil
}

// Put stores a key-value pair synchronously.
func (b *DiskBackend) Put(key, value []byte) error {
	if err := b.store.Set(key, value); err != nil {
		return &StorageError{Op: "set", Inner: err}
	}
	return nil
}

// Delete removes a key synchronously.
func (b *DiskBackend) Delete(key []byte) error {
	if err := b.store.Delete(key); err != nil {
		return &StorageError{Op: "delete", Inner: err}
	}
	return nil
}

// List returns all keys matching the given prefix.
func (b *DiskBackend) List(prefix []byte) ([][]byte, error) {
	keys, err := b.store.List(prefix)
	if err != nil {
		return nil, &StorageError{Op: "list", Inner: err}
	}
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix.
func (b *DiskBackend) Scan(prefix []byte, limit int) (map[string][]byte, error) {
	p := string(prefix)
	if limit <= 0 {
		limit = 10000
	}

	result := make(map[string][]byte)

	upperBound := prefixUpperBound(prefix)
	opts := &storage.IteratorOptions{LowerBound: prefix}
	if upperBound != nil {
		opts.UpperBound = upperBound
	}

	iter := b.store.NewIterator(opts)
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
		return nil, &StorageError{Op: "scan", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return nil, &StorageError{Op: "scan-close", Inner: err}
	}

	return result, nil
}

// SaveSnapshot serializes all storage data to the writer.
func (b *DiskBackend) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	// Collect all entries via iterator.
	type entry struct {
		key   []byte
		value []byte
	}
	var entries []entry

	iter := b.store.NewIterator(nil)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		select {
		case <-stopper:
			_ = iter.Close()
			return &SnapshotError{Op: "save", Inner: context.Canceled}
		default:
		}
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		entries = append(entries, entry{key: k, value: v})
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return &SnapshotError{Op: "save", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return &SnapshotError{Op: "save", Inner: err}
	}

	// Write entry count.
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
		if _, err := w.Write(e.key); err != nil {
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

// RecoverSnapshot clears storage and rebuilds from the reader.
func (b *DiskBackend) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	// Clear existing data.
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

		if err := b.store.Set(keyBuf, valBuf); err != nil {
			return &SnapshotError{Op: "recover", Inner: err}
		}
	}

	return nil
}

// GetHash computes a deterministic xxhash64 hash over all key-value
// pairs in sorted key order.
func (b *DiskBackend) GetHash() (uint64, error) {
	h := xxhash.New()

	// Collect all entries sorted by key.
	type entry struct {
		key   string
		value []byte
	}
	var entries []entry

	iter := b.store.NewIterator(nil)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		k := string(iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		entries = append(entries, entry{key: k, value: v})
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return 0, &StorageError{Op: "hash", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return 0, &StorageError{Op: "hash-close", Inner: err}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key < entries[j].key
	})

	for _, e := range entries {
		_, _ = h.Write([]byte(e.key))
		_, _ = h.Write(e.value)
	}

	return h.Sum64(), nil
}

// Close releases storage resources.
func (b *DiskBackend) Close() error {
	return nil
}

// clearStorage removes all existing keys from storage.
func (b *DiskBackend) clearStorage() error {
	iter := b.store.NewIterator(nil)
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
		if err := b.store.Delete(k); err != nil {
			return err
		}
	}
	return nil
}
