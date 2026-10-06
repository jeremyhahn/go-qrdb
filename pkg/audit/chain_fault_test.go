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

package audit

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file is the audit-replicated review's N7: fault-injection coverage
// for Restore and Query, the two Chain methods that still read storage
// after B1's fix. Prune is deliberately NOT covered here for a corrupt-key
// or iterator-error case: B1 rewrote it to read nothing from storage at
// all (see chain.go's Prune doc comment), so there is no iterator for a
// fault to inject into -- the fix itself is what makes that case
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

// TestChain_Query_IteratorError covers Query's event-range iterator
// reporting a fault.
func TestChain_Query_IteratorError(t *testing.T) {
	c := NewChain()
	_, err := c.Query(errReader{err: errStorageFault}, "acme", Filter{Limit: 10})
	var tre *TableReadError
	if !errors.As(err, &tre) || !errors.Is(err, errStorageFault) {
		t.Fatalf("Query() err = %v, want *TableReadError wrapping %v", err, errStorageFault)
	}
}

// TestChain_Query_CorruptEventRecord covers an event record present but not
// valid AuditEvent JSON: Query must refuse with *CorruptTableError rather
// than returning a zero-value event or panicking.
func TestChain_Query_CorruptEventRecord(t *testing.T) {
	stg := memoryStorageWithRawKey(t, eventKey("acme", 1), []byte("not json"))
	c := NewChain()
	_, err := c.Query(stg, "acme", Filter{Limit: 10})
	var cte *CorruptTableError
	if !errors.As(err, &cte) {
		t.Fatalf("Query() err = %v, want *CorruptTableError", err)
	}
	if cte.Key != string(eventKey("acme", 1)) {
		t.Errorf("CorruptTableError.Key = %q, want %q", cte.Key, eventKey("acme", 1))
	}
}
