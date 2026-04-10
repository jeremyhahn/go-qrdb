// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

// Storage is the unified key-value storage interface used by all backends.
// Implementations include PebbleDB, in-memory, file-backed, and encrypted
// wrappers. The interface is compatible with go-quicraft's seal.StorageBackend
// via Go duck typing.
type Storage interface {
	// Get retrieves a value by key. Returns nil, nil when key does not exist.
	Get(key []byte) ([]byte, error)

	// Set stores a key-value pair.
	Set(key, value []byte) error

	// Delete removes a key.
	Delete(key []byte) error

	// List returns all keys matching the given prefix.
	// An empty prefix returns all keys.
	List(prefix []byte) ([][]byte, error)

	// Exists reports whether the key exists in storage.
	Exists(key []byte) (bool, error)

	// Batch creates a new write batch for atomic multi-key operations.
	Batch() Batch

	// NewIterator creates an iterator for scanning key ranges.
	NewIterator(opts *IteratorOptions) Iterator

	// Snapshot creates a point-in-time snapshot for consistent reads.
	Snapshot() (Snapshot, error)

	// Close releases all resources held by the storage.
	Close() error
}

// Batch groups multiple writes into an atomic operation.
type Batch interface {
	// Set stores a key-value pair in the batch.
	Set(key, value []byte) error

	// Delete removes a key in the batch.
	Delete(key []byte) error

	// Commit applies all batched operations atomically.
	Commit() error

	// Rollback discards all batched operations without applying them.
	Rollback() error

	// Close releases resources held by the batch.
	Close() error
}

// Iterator scans key-value pairs within a range.
type Iterator interface {
	// SeekToFirst positions the iterator at the first key.
	SeekToFirst() bool

	// SeekToLast positions the iterator at the last key.
	SeekToLast() bool

	// Seek positions the iterator at the first key >= target.
	Seek(target []byte) bool

	// Next advances the iterator to the next key.
	Next() bool

	// Prev moves the iterator to the previous key.
	Prev() bool

	// Valid reports whether the iterator is positioned at a valid entry.
	Valid() bool

	// Key returns the current key. The returned slice is only valid
	// until the next iterator operation.
	Key() []byte

	// Value returns the current value. The returned slice is only valid
	// until the next iterator operation.
	Value() []byte

	// Error returns any accumulated error encountered during iteration.
	Error() error

	// Close releases resources held by the iterator.
	Close() error
}

// Snapshot provides a consistent point-in-time view of the storage.
type Snapshot interface {
	// Get retrieves a value by key from the snapshot.
	// Returns nil, nil when key does not exist.
	Get(key []byte) ([]byte, error)

	// NewIterator creates an iterator over the snapshot.
	NewIterator(opts *IteratorOptions) Iterator

	// Close releases resources held by the snapshot.
	Close() error
}

// IteratorOptions configures iterator behavior.
type IteratorOptions struct {
	// LowerBound sets the inclusive lower bound of the key range.
	LowerBound []byte

	// UpperBound sets the exclusive upper bound of the key range.
	UpperBound []byte

	// Reverse iterates in descending key order when true.
	Reverse bool

	// OnlyKeys skips reading values when true, improving scan performance
	// when only keys are needed.
	OnlyKeys bool
}
