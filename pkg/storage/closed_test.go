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

package storage_test

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestNewClosedBatchRefusesEveryOperation asserts each method of the closed
// batch reports the closure with an error that matches the sentinel.
func TestNewClosedBatchRefusesEveryOperation(t *testing.T) {
	b := storage.NewClosedBatch()

	cases := map[string]func() error{
		"Get":         func() error { _, err := b.Get([]byte("k")); return err },
		"Set":         func() error { return b.Set([]byte("k"), []byte("v")) },
		"Delete":      func() error { return b.Delete([]byte("k")) },
		"DeleteRange": func() error { return b.DeleteRange([]byte("a"), []byte("b")) },
		"Commit":      func() error { return b.Commit() },
		"Rollback":    func() error { return b.Rollback() },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, storage.ErrStorageClosed) {
				t.Fatalf("%s = %v, want ErrStorageClosed", name, err)
			}
			var batchErr *storage.BatchError
			if !errors.As(err, &batchErr) {
				t.Fatalf("%s = %v, want a *storage.BatchError", name, err)
			}
		})
	}
}

// TestNewClosedBatchIteratorAndClose asserts the two methods that do not
// return an error directly: the iterator reports the closure through Error,
// and Close succeeds because there is nothing to release.
func TestNewClosedBatchIteratorAndClose(t *testing.T) {
	b := storage.NewClosedBatch()

	it := b.NewIterator(nil)
	if it.SeekToFirst() {
		t.Error("SeekToFirst on a closed batch's iterator positioned it")
	}
	if !errors.Is(it.Error(), storage.ErrStorageClosed) {
		t.Errorf("iterator Error() = %v, want ErrStorageClosed", it.Error())
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestNewClosedIteratorIsPermanentlyInvalid asserts every positioning method
// reports no entry and that the accessors return nothing, so a caller that
// ignores Error still reads an empty range rather than stale bytes.
func TestNewClosedIteratorIsPermanentlyInvalid(t *testing.T) {
	it := storage.NewClosedIterator()

	positions := map[string]func() bool{
		"SeekToFirst": it.SeekToFirst,
		"SeekToLast":  it.SeekToLast,
		"Seek":        func() bool { return it.Seek([]byte("k")) },
		"Next":        it.Next,
		"Prev":        it.Prev,
		"Valid":       it.Valid,
	}
	for name, call := range positions {
		if call() {
			t.Errorf("%s = true, want false", name)
		}
	}
	if it.Key() != nil {
		t.Errorf("Key() = %q, want nil", it.Key())
	}
	if it.Value() != nil {
		t.Errorf("Value() = %q, want nil", it.Value())
	}
}

// TestNewClosedIteratorReportsTheClosure asserts Error carries the sentinel
// and Close is a successful no-op.
func TestNewClosedIteratorReportsTheClosure(t *testing.T) {
	it := storage.NewClosedIterator()

	if !errors.Is(it.Error(), storage.ErrStorageClosed) {
		t.Errorf("Error() = %v, want ErrStorageClosed", it.Error())
	}
	var closed *storage.ClosedError
	if !errors.As(it.Error(), &closed) {
		t.Errorf("Error() = %v, want a *storage.ClosedError", it.Error())
	}
	if err := it.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

// TestClosedErrorUnwrapsToTheSentinel is the property the whole fix rests on:
// the struct form and the sentinel form of "the store is closed" are one
// condition, so a caller needs one predicate and never a string.
func TestClosedErrorUnwrapsToTheSentinel(t *testing.T) {
	closed := &storage.ClosedError{}

	if got := closed.Unwrap(); got != error(storage.ErrStorageClosed) {
		t.Fatalf("Unwrap() = %v, want ErrStorageClosed", got)
	}
	if !errors.Is(closed, storage.ErrStorageClosed) {
		t.Error("errors.Is(&ClosedError{}, ErrStorageClosed) = false, want true")
	}
	if errors.Is(closed, storage.ErrKeyNotFound) {
		t.Error("a ClosedError matched ErrKeyNotFound; the sentinels are not distinct")
	}
}

// TestClosedErrorMatchesThroughEveryWrapper asserts the sentinel survives each
// wrapper this package puts around a backend failure. Losing it in one of them
// is how a caller ends up matching text again.
func TestClosedErrorMatchesThroughEveryWrapper(t *testing.T) {
	wrapped := map[string]error{
		"OpError":       &storage.OpError{Op: "set", Err: &storage.ClosedError{}},
		"BatchError":    &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}},
		"IteratorError": &storage.IteratorError{Err: &storage.ClosedError{}},
		"SnapshotError": &storage.SnapshotError{Op: "get", Err: &storage.ClosedError{}},
		"nested":        &storage.OpError{Op: "apply", Err: &storage.BatchError{Op: "commit", Err: &storage.ClosedError{}}},
	}
	for name, err := range wrapped {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, storage.ErrStorageClosed) {
				t.Errorf("errors.Is(%v, ErrStorageClosed) = false, want true", err)
			}
			if !storage.IsClosed(err) {
				t.Errorf("storage.IsClosed(%v) = false, want true", err)
			}
		})
	}
}

// TestIsClosedRejectsUnrelatedErrors asserts the predicate is not a catch-all:
// a nil error and other storage failures must not read as a closed store, or
// a shutdown race and a real fault become indistinguishable.
func TestIsClosedRejectsUnrelatedErrors(t *testing.T) {
	cases := map[string]error{
		"nil":             nil,
		"key not found":   storage.ErrKeyNotFound,
		"empty key":       storage.ErrEmptyKey,
		"invalid path":    storage.ErrInvalidPath,
		"unrelated":       errors.New("disk on fire"),
		"wrapped unrelat": &storage.OpError{Op: "set", Err: errors.New("disk on fire")},
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if storage.IsClosed(err) {
				t.Errorf("IsClosed(%v) = true, want false", err)
			}
		})
	}
}
