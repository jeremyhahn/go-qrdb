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
	"bytes"
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time check that StandaloneBackend implements Backend.
var _ Backend = (*StandaloneBackend)(nil)

// Compile-time proof that StandaloneBackend satisfies AtomicBackend.
//
// It is the backend EmbeddedBackendFactory.Create and Manager return for every
// non-raft database, so without it BackendKVService's capability assertion
// failed and PutIfAbsent, CompareAndSwap and CompareAndDelete refused on the
// whole dedicated-store path (go-qrdb#466). The assertion sits beside the
// methods so dropping one is a compile error rather than a runtime refusal
// discovered by a caller.
var _ AtomicBackend = (*StandaloneBackend)(nil)

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
//
// # Atomicity
//
// StandaloneBackend is an AtomicBackend. The guarded verbs are a read followed
// by a write, made indivisible by writeMu, which is the genuine mechanism for a
// single-process local engine: there is no second process to race with, so
// excluding every other writer in this one is the whole of the problem.
//
// That only holds because EVERY write on the type takes the same mutex -- the
// unguarded Put, Delete and Batch as much as the three guarded verbs. A write
// that skipped it could land between a guarded operation's read and its write,
// and the guarantee would be advisory: PutIfAbsent would overwrite a value
// written after its check, and CompareAndDelete would remove a replacement it
// never compared against. An advisory guarantee is worse than a refusal,
// because a refusal is at least honest about what the caller did not get.
//
// The scope of the guarantee is writes through this Backend, which is the whole
// of the store's writers by construction: the store is created and handed to
// NewStandaloneBackend by a factory that keeps no reference to it (see
// EmbeddedBackendFactory.Create and Manager.openBarrierMetaBackend), and the
// backend is thereafter the store's sole owner.
type StandaloneBackend struct {
	store storage.Storage

	// writeMu serializes every write on this backend. See the Atomicity
	// section above: it is what makes the guarded verbs indivisible, and it is
	// only sound because the unguarded writes take it too.
	writeMu sync.Mutex
}

// NewStandaloneBackend creates a new StandaloneBackend wrapping the given
// storage.Storage instance.
func NewStandaloneBackend(store storage.Storage) *StandaloneBackend {
	return &StandaloneBackend{store: store}
}

// Storage returns the underlying storage.Storage instance. This is useful
// when callers need direct access to the storage layer, for example when
// the timeseries engine requires raw PebbleDB access.
//
// Writes issued against the returned store do NOT take writeMu and therefore
// do not participate in the atomicity guarantee described on the type. This
// method hands out no authority the caller did not already have -- whoever
// calls NewStandaloneBackend supplies the store -- so it is the ownership
// convention, not this accessor, that the guarantee rests on.
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
//
// It takes writeMu so it cannot land BETWEEN a guarded operation's read and
// its write. Without that the guarded verbs would be advisory rather than
// atomic -- see the Atomicity section on StandaloneBackend.
func (b *StandaloneBackend) Put(_ context.Context, key, value []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.store.Set(key, value)
}

// Delete removes a key. The context is accepted for interface compatibility
// but is unused by the standalone backend.
//
// It takes writeMu for the same reason Put does: an unguarded delete inside a
// guarded operation's window would un-decide what that operation decided.
func (b *StandaloneBackend) Delete(_ context.Context, key []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.store.Delete(key)
}

// PutIfAbsent stores value under key only when the key is absent, reporting
// whether this call created it.
//
// The check and the write are one operation because writeMu excludes every
// other writer on this backend for their duration. storage.Storage.Get returns
// nil, nil for a missing key, so a nil value is the absence signal -- Exists is
// not used because it would be a second trip through the store for an answer
// Get already carries.
func (b *StandaloneBackend) PutIfAbsent(_ context.Context, key, value []byte) (bool, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	current, err := b.store.Get(key)
	if err != nil {
		return false, err
	}
	if current != nil {
		return false, nil
	}
	if err := b.store.Set(key, value); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, reporting whether this call removed it. The same mutex that makes
// PutIfAbsent indivisible makes the compare and the delete one operation here.
func (b *StandaloneBackend) CompareAndDelete(_ context.Context, key, expected []byte) (bool, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	current, err := b.store.Get(key)
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := b.store.Delete(key); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndSwap replaces key's value with next only when its stored value
// still equals expected, reporting whether this call replaced it. An absent key
// is left absent -- a swap is not a create (go-qrdb#56).
func (b *StandaloneBackend) CompareAndSwap(_ context.Context, key, expected, next []byte) (bool, error) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	current, err := b.store.Get(key)
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := b.store.Set(key, next); err != nil {
		return false, err
	}
	return true, nil
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
//
// The all-or-nothing property is still the store batch's own: writeMu does not
// create it and removing the lock would not weaken it. What the lock adds is
// exclusion in the other direction -- a batch carrying key K can no longer
// commit inside a guarded operation on K, which is the window that would have
// made the guarded verbs advisory. The batch is built and committed inside the
// lock rather than only committed under it, because a batch reads its own
// writes and building it outside would let the two interleave.
func (b *StandaloneBackend) Batch(_ context.Context, ops []BatchOp) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

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
