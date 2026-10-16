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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface assertion.
var _ Backend = (*DiskBackend)(nil)

// DiskBackend wraps a storage.Storage directly for synchronous disk I/O.
// All operations delegate to the underlying storage without any caching.
type DiskBackend struct {
	store storage.Storage

	// stamp, when set, is the applied-index marker DiskAdapter keeps, and
	// ApplyAtomic commits it in the same storage batch as the set. nil for a
	// backend used outside a DiskAdapter.
	stamp *appliedIndexStamp
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

// Put stores a key-value pair synchronously, as a one-mutation ApplyAtomic,
// so the write and the applied-index marker of the entry that made it commit
// together (see ApplyAtomic).
func (b *DiskBackend) Put(key, value []byte) error {
	return b.ApplyAtomic([]Mutation{{Key: key, Value: value}})
}

// Delete removes a key synchronously, as a one-mutation ApplyAtomic, for the
// reason Put gives.
func (b *DiskBackend) Delete(key []byte) error {
	return b.ApplyAtomic([]Mutation{{Key: key, Delete: true}})
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
// pairs in storage, visited in ascending key order.
func (b *DiskBackend) GetHash() (uint64, error) {
	return xxhashRecords(b.Records)
}

// Records visits every key-value pair in storage in ascending key order.
// Storage iteration order is backend-specific (the file backend's layout is
// hashed), so records are collected and sorted before the visit.
func (b *DiskBackend) Records(fn RecordVisitor) error {
	var recs []kvRecord
	iter := b.store.NewIterator(nil)
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		recs = append(recs, kvRecord{key: string(iter.Key()), value: v})
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return &StorageError{Op: "records", Inner: err}
	}
	if err := iter.Close(); err != nil {
		return &StorageError{Op: "records-close", Inner: err}
	}
	return visitSorted(recs, fn)
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

// PutDurable delegates to Put, and DeleteDurable to Delete.
//
// Every write on this backend already commits synchronously, on the calling
// goroutine, through ApplyAtomic, and returns that storage's error: an
// acknowledgement means the write is durable, a failure is reported rather
// than logged, and writes reach storage in the order the apply loop issued
// them. That is the guarded contract in full, and each write carries the
// applied-index marker of the entry that made it.
func (b *DiskBackend) PutDurable(key, value []byte) error { return b.Put(key, value) }

// DeleteDurable delegates to Delete — see PutDurable.
func (b *DiskBackend) DeleteDurable(key []byte) error { return b.Delete(key) }

// ApplyAtomic commits the whole set as ONE storage.Batch, whose Commit is
// atomic by the storage.Batch contract: a crash leaves storage holding all of
// it or none of it, and a reader never observes a subset. storage.Batch.Commit
// returns only once the batch has been applied, and returns its failure, which
// is the guarded pair's durability and failure promises; ordering holds because
// every write on this backend reaches storage synchronously, on the apply
// goroutine, in apply order.
//
// When the backend serves a DiskAdapter, the applied-index marker for the
// entry being applied is written in the SAME batch — for every write this
// backend makes, single-key ones included, since Put, Delete and the durable
// pair all commit through here. That is what keeps an entry from being
// applied twice: without it a crash after the write and before the marker
// replays the entry over its own result, and a guarded write can decide the
// other way. A CompareAndSwap that failed against an absent key, followed by a
// PutIfAbsent that created it, replays with the swap now SUCCEEDING and the
// create now declining — a replica diverging from the log every other replica
// applied once.
func (b *DiskBackend) ApplyAtomic(muts []Mutation) error {
	if len(muts) == 0 {
		return nil
	}
	set := muts
	if marker, ok := b.stamp.mutation(); ok {
		set = append(append(make([]Mutation, 0, len(muts)+1), muts...), marker)
	}
	if err := commitMutations(b.store, set); err != nil {
		return &StorageError{Op: "apply-atomic", Inner: err}
	}
	return nil
}

// appliedIndexStamp carries the log index of the entry a DiskAdapter is
// applying down to DiskBackend.ApplyAtomic. It is written and read only on the
// apply goroutine — go-quicraft never runs Update concurrently with itself —
// so it needs no synchronisation.
type appliedIndexStamp struct {
	index uint64
}

// mutation returns the marker write for the entry being applied, and false
// when there is none: no stamp, or no entry in flight.
func (s *appliedIndexStamp) mutation() (Mutation, bool) {
	if s == nil || s.index == 0 {
		return Mutation{}, false
	}
	return Mutation{Key: []byte(diskMetaKeyLastApplied), Value: encodeAppliedIndex(s.index)}, true
}

// commitMutations writes set to store as one storage.Batch.
func commitMutations(store storage.Storage, set []Mutation) error {
	batch := store.Batch()
	defer func() { _ = batch.Close() }()
	for _, m := range set {
		var err error
		if m.Delete {
			err = batch.Delete(m.Key)
		} else {
			err = batch.Set(m.Key, m.Value)
		}
		if err != nil {
			return err
		}
	}
	return batch.Commit()
}
