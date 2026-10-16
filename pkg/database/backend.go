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

// Package database defines the Backend interface, the storage abstraction that
// services use to interact with a database regardless of whether it is backed
// by local storage or Raft consensus.
package database

import "context"

// KeyValue represents a key-value pair returned from range scans.
type KeyValue struct {
	Key   []byte
	Value []byte
}

// BatchOp represents a single operation within a batch.
type BatchOp struct {
	Op    BatchOpType
	Key   []byte
	Value []byte // nil for deletes
}

// BatchOpType identifies the type of batch operation.
type BatchOpType byte

const (
	// BatchOpPut indicates a put (upsert) operation.
	BatchOpPut BatchOpType = 1

	// BatchOpDelete indicates a delete operation.
	BatchOpDelete BatchOpType = 2
)

// Backend is the storage abstraction that databases expose to services.
// Standalone backends implement this directly against storage.Storage.
// The raft backend implements this via service.Host (proposals/reads through
// consensus).
type Backend interface {
	// Get retrieves a value by key. Returns nil, nil when the key does not
	// exist.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// Put stores a key-value pair.
	Put(ctx context.Context, key, value []byte) error

	// Delete removes a key.
	Delete(ctx context.Context, key []byte) error

	// DeleteRange removes keys in [start, end). A nil end removes keys from
	// start through the end of the keyspace.
	DeleteRange(ctx context.Context, start, end []byte) error

	// List returns all keys matching the given prefix as strings.
	List(ctx context.Context, prefix string) ([]string, error)

	// Range scans keys in [start, end) with an optional limit.
	// A zero limit returns all matching keys.
	Range(ctx context.Context, start, end []byte, limit int) ([]KeyValue, error)

	// Batch applies multiple operations atomically.
	Batch(ctx context.Context, ops []BatchOp) error

	// Close releases all resources held by the backend.
	Close() error
}

// AtomicBackend is the optional capability of performing a write whose outcome
// is defined under concurrency.
//
// It is discovered by type assertion rather than being part of Backend because
// the implementations do not all sit somewhere that can honour it. A
// storage-backed or barrier-wrapped backend has the primitive one call away; a
// RemoteBackend would need a new operation on the wire protocol first. Widening
// Backend would force those to ship a method that either lies about atomicity
// or always fails — and a caller cannot tell that apart from a real refusal.
//
// Callers assert for it and fail closed when it is absent:
//
//	atomic, ok := backend.(database.AtomicBackend)
//	if !ok {
//	    return ErrAtomicWriteUnsupported
//	}
type AtomicBackend interface {
	Backend

	// PutIfAbsent atomically stores value under key only if key is absent,
	// reporting whether this call created it. stored=false with a nil error
	// means the key already existed and the store is unchanged.
	PutIfAbsent(ctx context.Context, key, value []byte) (stored bool, err error)

	// CompareAndDelete atomically removes key only when its stored value
	// still equals expected, reporting whether this call removed it. It is
	// the release half of PutIfAbsent: an unguarded delete can un-decide what
	// a guarded create decided (go-qrdb#41).
	CompareAndDelete(ctx context.Context, key, expected []byte) (deleted bool, err error)

	// CompareAndSwap atomically replaces key's value with next only when its
	// stored value still equals expected, reporting whether this call replaced
	// it. An absent key is left absent — a swap is not a create.
	//
	// It is the edit half of the same set. A guarded create and a guarded
	// delete with no guarded edit between them is two thirds of a primitive,
	// and composing one from the pair leaves the key briefly ABSENT, which
	// both breaks readers and reopens the window a guarded create closed
	// (go-qrdb#56).
	CompareAndSwap(ctx context.Context, key, expected, next []byte) (swapped bool, err error)
}
