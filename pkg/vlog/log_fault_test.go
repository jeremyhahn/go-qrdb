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

package vlog

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file is the audit-replicated review's N7 fault-injection coverage for
// Restore, the one Chain method here that reads storage, and for the staging
// writes of Append and Prune. Prune is deliberately NOT covered for a
// corrupt-key or iterator-error case: B1 rewrote it to read nothing from
// storage at all (see log.go's Prune doc comment), so there is no iterator
// for a fault to inject into -- the fix itself is what makes that case
// unreachable, not an oversight.

// memoryStorageWithRawKey returns a memory.Storage with exactly one raw key
// set directly (bypassing Chain entirely), for tests that need a
// deliberately malformed record already on disk.
func memoryStorageWithRawKey(t *testing.T, key, value []byte) storage.Storage {
	t.Helper()
	stg := memory.NewStorage()
	if err := stg.Set(key, value); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
	return stg
}

// errStorageFault is the sentinel every fault below wraps, so tests can
// assert errors.Is(err, errStorageFault) in addition to the wrapping type.
var errStorageFault = errors.New("storage fault injected by test")

// errIterator is a storage.Iterator that reports no entries and a fixed
// error from Error(), simulating a backend whose range scan fails outright
// (a disk read error, a closed snapshot) rather than returning corrupt data.
type errIterator struct{ err error }

func (errIterator) SeekToFirst() bool { return false }
func (errIterator) SeekToLast() bool  { return false }
func (errIterator) Seek([]byte) bool  { return false }
func (errIterator) Next() bool        { return false }
func (errIterator) Prev() bool        { return false }
func (errIterator) Valid() bool       { return false }
func (errIterator) Key() []byte       { return nil }
func (errIterator) Value() []byte     { return nil }
func (it errIterator) Error() error   { return it.err }
func (errIterator) Close() error      { return nil }

// errReader is a storage.Reader every iterator of which fails with err.
type errReader struct{ err error }

func (r errReader) Get([]byte) ([]byte, error) { return nil, r.err }
func (r errReader) NewIterator(*storage.IteratorOptions) storage.Iterator {
	return errIterator{err: r.err}
}

// failingWriter is a Writer whose Set or Delete fails with errStorageFault
// for every key starting with the matching prefix; nil fails nothing.
type failingWriter struct {
	Writer
	failSet    []byte
	failDelete []byte
}

func (w failingWriter) Set(key, value []byte) error {
	if w.failSet != nil && bytes.HasPrefix(key, w.failSet) {
		return errStorageFault
	}
	return w.Writer.Set(key, value)
}

func (w failingWriter) Delete(key []byte) error {
	if w.failDelete != nil && bytes.HasPrefix(key, w.failDelete) {
		return errStorageFault
	}
	return w.Writer.Delete(key)
}

// TestChain_Restore_IteratorError covers Restore's head-scan iterator
// reporting a fault: Restore must report *TableReadError wrapping the
// underlying fault rather than silently restoring an empty (wrong) set of
// heads.
func TestChain_Restore_IteratorError(t *testing.T) {
	c := NewChain()
	err := c.Restore(errReader{err: errStorageFault})
	var tre *TableReadError
	if !errors.As(err, &tre) || !errors.Is(err, errStorageFault) {
		t.Fatalf("Restore() err = %v, want *TableReadError wrapping %v", err, errStorageFault)
	}
}

// TestChain_Restore_CorruptHeadRecord covers a head record present but not
// decodeHead-shaped: Restore must refuse with *CorruptTableError naming the
// offending key, not panic or silently treat it as absent.
func TestChain_Restore_CorruptHeadRecord(t *testing.T) {
	stg := memoryStorageWithRawKey(t, headKey("acme"), []byte{1, 2, 3})
	c := NewChain()
	err := c.Restore(stg)
	var cte *CorruptTableError
	if !errors.As(err, &cte) {
		t.Fatalf("Restore() err = %v, want *CorruptTableError", err)
	}
	if cte.Key != string(headKey("acme")) {
		t.Errorf("CorruptTableError.Key = %q, want %q", cte.Key, headKey("acme"))
	}
}

// TestChain_Append_TableWriteFault covers both staging writes of Append: a
// fault on either is a *TableWriteError wrapping it, and the in-memory head
// does not advance past a record that was never staged.
func TestChain_Append_TableWriteFault(t *testing.T) {
	for name, failSet := range map[string][]byte{
		"event record": []byte(eventPrefix),
		"head record":  []byte(headPrefix),
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			w := failingWriter{Writer: memory.NewStorage(), failSet: failSet}
			_, err := c.Append(w, "acme", fixedRecord(0))
			var twe *TableWriteError
			if !errors.As(err, &twe) || !errors.Is(err, errStorageFault) {
				t.Fatalf("Append() err = %v, want *TableWriteError wrapping %v", err, errStorageFault)
			}
			if seq, _, _ := c.Head("acme"); seq != 0 {
				t.Fatalf("head seq after a failed Append = %d, want 0", seq)
			}
		})
	}
}

// TestChain_Prune_TableWriteFault covers Prune's event deletes and its head
// write: a fault on either is a *TableWriteError, and the in-memory
// low-watermark stays where it was.
func TestChain_Prune_TableWriteFault(t *testing.T) {
	for name, fault := range map[string]failingWriter{
		"event delete": {failDelete: []byte(eventPrefix)},
		"head write":   {failSet: []byte(headPrefix)},
	} {
		t.Run(name, func(t *testing.T) {
			store := memory.NewStorage()
			c := NewChain()
			for i := 0; i < 3; i++ {
				if _, err := c.Append(store, "acme", fixedRecord(i)); err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
			}
			fault.Writer = store
			_, err := c.Prune(fault, "acme", 3)
			var twe *TableWriteError
			if !errors.As(err, &twe) || !errors.Is(err, errStorageFault) {
				t.Fatalf("Prune() err = %v, want *TableWriteError wrapping %v", err, errStorageFault)
			}
			if _, lo, _ := c.Head("acme"); lo != 1 {
				t.Fatalf("low-watermark after a failed Prune = %d, want 1", lo)
			}
		})
	}
}
