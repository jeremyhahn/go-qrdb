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

package pebble

import (
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// pebbleIterator implements storage.Iterator backed by a PebbleDB iterator.
// Key() and Value() return defensive copies since PebbleDB invalidates the
// underlying byte slices on the next iterator movement.
type pebbleIterator struct {
	iter   *pebble.Iterator
	closed atomic.Bool
	err    error
}

// buildIterOptions translates storage.IteratorOptions to pebble.IterOptions.
func buildIterOptions(opts *storage.IteratorOptions) *pebble.IterOptions {
	iterOpts := &pebble.IterOptions{}
	if opts == nil {
		return iterOpts
	}

	if opts.LowerBound != nil {
		iterOpts.LowerBound = opts.LowerBound
	}
	if opts.UpperBound != nil {
		iterOpts.UpperBound = opts.UpperBound
	}
	if opts.OnlyKeys {
		iterOpts.KeyTypes = pebble.IterKeyTypePointsOnly
	}
	return iterOpts
}

// newIterator creates a new iterator over the database.
// Returns a sentinel (nil inner iterator) on error; the error is surfaced
// via the Error() method.
func newIterator(db *pebble.DB, opts *storage.IteratorOptions) storage.Iterator {
	iterOpts := buildIterOptions(opts)
	iter, err := db.NewIter(iterOpts)
	if err != nil {
		return &pebbleIterator{err: err}
	}
	return &pebbleIterator{iter: iter}
}

// newSnapshotIterator creates a new iterator over a snapshot.
func newSnapshotIterator(snap *pebble.Snapshot, opts *storage.IteratorOptions) storage.Iterator {
	iterOpts := buildIterOptions(opts)
	iter, err := snap.NewIter(iterOpts)
	if err != nil {
		return &pebbleIterator{err: err}
	}
	return &pebbleIterator{iter: iter}
}

// SeekToFirst positions the iterator at the first key.
func (it *pebbleIterator) SeekToFirst() bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.First()
}

// SeekToLast positions the iterator at the last key.
func (it *pebbleIterator) SeekToLast() bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.Last()
}

// Seek positions the iterator at the first key >= target.
func (it *pebbleIterator) Seek(target []byte) bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.SeekGE(target)
}

// Next advances the iterator to the next key.
func (it *pebbleIterator) Next() bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.Next()
}

// Prev moves the iterator to the previous key.
func (it *pebbleIterator) Prev() bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.Prev()
}

// Valid reports whether the iterator is positioned at a valid entry. It
// reports false once an error has been recorded, so a loop over the
// iterator ends and the caller's Error check reports the failure.
func (it *pebbleIterator) Valid() bool {
	if it.iter == nil || it.closed.Load() || it.err != nil {
		return false
	}
	return it.iter.Valid()
}

// Key returns a copy of the current key. Returns nil when the iterator
// is not positioned at a valid entry.
func (it *pebbleIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	k := it.iter.Key()
	out := make([]byte, len(k))
	copy(out, k)
	return out
}

// Value returns a copy of the current value. Returns nil when the iterator
// is not positioned at a valid entry, or when fetching the value fails: a
// value stored out of line is read lazily, and pebble.Iterator.Value panics
// when that read fails (an I/O error or a corrupt value block). Value reads
// with ValueAndErr instead and records the first such failure, after which
// Valid reports false and Error returns it.
func (it *pebbleIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	v, err := it.iter.ValueAndErr()
	if err != nil {
		it.err = err
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out
}

// Error returns any accumulated error encountered during iteration, or the
// error that prevented iterator creation.
func (it *pebbleIterator) Error() error {
	if it.err != nil {
		return &storage.IteratorError{Err: it.err}
	}
	if it.iter == nil {
		return nil
	}
	if err := it.iter.Error(); err != nil {
		return &storage.IteratorError{Err: err}
	}
	return nil
}

// Close releases resources held by the iterator. Calling Close on an
// already-closed iterator is a no-op.
func (it *pebbleIterator) Close() error {
	if !it.closed.CompareAndSwap(false, true) {
		return nil
	}
	if it.iter == nil {
		return nil
	}
	if err := it.iter.Close(); err != nil {
		return &storage.IteratorError{Err: err}
	}
	return nil
}
