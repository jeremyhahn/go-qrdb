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

// This file holds the two handles a closed storage engine hands out.
//
// Batch and NewIterator return a value, not (value, error), so a backend that
// has been closed cannot refuse by returning an error the way Get and Set do.
// Before these types existed each backend improvised: memory and file handed
// back a live handle over their still-readable state, and PebbleDB handed back
// one wired to a released DB, which panics `pebble: closed` several frames
// later inside Commit (go-qrdb#371).
//
// They live here rather than in each backend so "a closed engine reports
// ErrStorageClosed" is one implementation shared by all of them, and cannot
// drift into four dialects of the same answer.

// closedBatch is the Batch a closed storage engine returns. Every method
// reports the closure; Close succeeds, because closing nothing cannot fail.
type closedBatch struct{}

// Compile-time interface assertion.
var _ Batch = (*closedBatch)(nil)

// NewClosedBatch returns a Batch that reports the engine's closure from every
// operation. Backends return it from Batch when they are closed.
func NewClosedBatch() Batch { return &closedBatch{} }

// Get reports the closure.
func (b *closedBatch) Get(_ []byte) ([]byte, error) {
	return nil, &BatchError{Op: "get", Err: &ClosedError{}}
}

// NewIterator returns an iterator that reports the closure.
func (b *closedBatch) NewIterator(_ *IteratorOptions) Iterator { return NewClosedIterator() }

// Set reports the closure. A closed engine refuses the write outright rather
// than buffering one that Commit could only throw away.
func (b *closedBatch) Set(_, _ []byte) error {
	return &BatchError{Op: "set", Err: &ClosedError{}}
}

// Delete reports the closure.
func (b *closedBatch) Delete(_ []byte) error {
	return &BatchError{Op: "delete", Err: &ClosedError{}}
}

func (b *closedBatch) DeleteRange(_, _ []byte) error {
	return &BatchError{Op: "delete_range", Err: &ClosedError{}}
}

// Commit reports the closure.
func (b *closedBatch) Commit() error {
	return &BatchError{Op: "commit", Err: &ClosedError{}}
}

// Rollback reports the closure.
func (b *closedBatch) Rollback() error {
	return &BatchError{Op: "rollback", Err: &ClosedError{}}
}

// Close is a no-op: the batch holds nothing to release.
func (b *closedBatch) Close() error { return nil }

// closedIterator is the Iterator a closed storage engine returns. It is
// permanently invalid and reports the closure through Error, so a caller that
// checks Error — the contract for a failed scan — learns why the range was
// empty instead of concluding there were no keys.
type closedIterator struct{}

// Compile-time interface assertion.
var _ Iterator = (*closedIterator)(nil)

// NewClosedIterator returns an Iterator that reports the engine's closure.
// Backends return it from NewIterator when they are closed.
func NewClosedIterator() Iterator { return &closedIterator{} }

// SeekToFirst reports no entry.
func (it *closedIterator) SeekToFirst() bool { return false }

// SeekToLast reports no entry.
func (it *closedIterator) SeekToLast() bool { return false }

// Seek reports no entry.
func (it *closedIterator) Seek(_ []byte) bool { return false }

// Next reports no entry.
func (it *closedIterator) Next() bool { return false }

// Prev reports no entry.
func (it *closedIterator) Prev() bool { return false }

// Valid reports that the iterator is not positioned.
func (it *closedIterator) Valid() bool { return false }

// Key returns nil: the iterator is never positioned.
func (it *closedIterator) Key() []byte { return nil }

// Value returns nil: the iterator is never positioned.
func (it *closedIterator) Value() []byte { return nil }

// Error reports the closure.
func (it *closedIterator) Error() error { return &ClosedError{} }

// Close is a no-op: the iterator holds nothing to release.
func (it *closedIterator) Close() error { return nil }
