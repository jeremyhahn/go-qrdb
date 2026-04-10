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

package database

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time check that StandaloneBackend implements Backend.
var _ Backend = (*StandaloneBackend)(nil)

// RangeIteratorError is returned when the iterator encounters an error
// during a Range scan.
type RangeIteratorError struct {
	Err error
}

// Error returns a human-readable description of the iterator failure.
func (e *RangeIteratorError) Error() string {
	return "database: range iterator error: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RangeIteratorError) Unwrap() error {
	return e.Err
}

// StandaloneBackend implements the Backend interface by delegating directly
// to a storage.Storage instance. It is used for memory, file, and pebble
// backends that do not require Raft consensus.
type StandaloneBackend struct {
	store storage.Storage
}

// NewStandaloneBackend creates a new StandaloneBackend wrapping the given
// storage.Storage instance.
func NewStandaloneBackend(store storage.Storage) *StandaloneBackend {
	return &StandaloneBackend{store: store}
}

// Storage returns the underlying storage.Storage instance. This is useful
// when callers need direct access to the storage layer, for example when
// the timeseries engine requires raw PebbleDB access.
func (b *StandaloneBackend) Storage() storage.Storage {
	return b.store
}

// Get retrieves a value by key. The context is accepted for interface
// compatibility but is unused by the standalone backend.
func (b *StandaloneBackend) Get(_ context.Context, key []byte) ([]byte, error) {
	return b.store.Get(key)
}

// Put stores a key-value pair. The context is accepted for interface
// compatibility but is unused by the standalone backend.
func (b *StandaloneBackend) Put(_ context.Context, key, value []byte) error {
	return b.store.Set(key, value)
}

// Delete removes a key. The context is accepted for interface compatibility
// but is unused by the standalone backend.
func (b *StandaloneBackend) Delete(_ context.Context, key []byte) error {
	return b.store.Delete(key)
}

// List returns all keys matching the given prefix as strings. The context
// is accepted for interface compatibility but is unused by the standalone
// backend.
func (b *StandaloneBackend) List(_ context.Context, prefix string) ([]string, error) {
	rawKeys, err := b.store.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(rawKeys))
	for i, k := range rawKeys {
		keys[i] = string(k)
	}
	return keys, nil
}

// Range scans keys in [start, end) with an optional limit. A zero limit
// returns all matching keys. Defensive copies are made of the iterator's
// key and value slices since they are only valid until the next iterator
// operation.
func (b *StandaloneBackend) Range(_ context.Context, start, end []byte, limit int) ([]KeyValue, error) {
	iter := b.store.NewIterator(&storage.IteratorOptions{
		LowerBound: start,
		UpperBound: end,
	})
	defer func() { _ = iter.Close() }()

	var results []KeyValue
	for valid := iter.SeekToFirst(); valid; valid = iter.Next() {
		key := make([]byte, len(iter.Key()))
		copy(key, iter.Key())

		value := make([]byte, len(iter.Value()))
		copy(value, iter.Value())

		results = append(results, KeyValue{Key: key, Value: value})

		if limit > 0 && len(results) >= limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, &RangeIteratorError{Err: err}
	}

	return results, nil
}

// Batch applies multiple operations atomically. Each BatchOp is dispatched
// by its Op field: BatchOpPut calls Set on the batch, and BatchOpDelete
// calls Delete. If Commit fails, Rollback is attempted. The batch is
// always closed via defer regardless of outcome.
func (b *StandaloneBackend) Batch(_ context.Context, ops []BatchOp) error {
	batch := b.store.Batch()
	defer func() { _ = batch.Close() }()

	for _, op := range ops {
		var err error
		switch op.Op {
		case BatchOpPut:
			err = batch.Set(op.Key, op.Value)
		case BatchOpDelete:
			err = batch.Delete(op.Key)
		}
		if err != nil {
			_ = batch.Rollback()
			return err
		}
	}

	if err := batch.Commit(); err != nil {
		_ = batch.Rollback()
		return err
	}
	return nil
}

// Close releases all resources held by the underlying storage.
func (b *StandaloneBackend) Close() error {
	return b.store.Close()
}
