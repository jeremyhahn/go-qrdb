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

package lease

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// persistedLessor returns a Lessor whose table writes land in st, with lease
// A (TTL 30) holding "a1" and "a2", lease B (TTL 40) holding "b1", and a
// third lease granted and revoked so the sequence runs past the live IDs.
func persistedLessor(t *testing.T, st *memory.MemoryStorage) (lr *Lessor, a, b int64) {
	t.Helper()
	lr = NewLessor()
	la, err := lr.Grant(st, 30)
	if err != nil {
		t.Fatalf("Grant A: %v", err)
	}
	lb, err := lr.Grant(st, 40)
	if err != nil {
		t.Fatalf("Grant B: %v", err)
	}
	lc, err := lr.Grant(st, 50)
	if err != nil {
		t.Fatalf("Grant C: %v", err)
	}
	for _, bind := range []struct {
		key string
		id  int64
	}{{"a1", la.ID}, {"a2", la.ID}, {"b1", la.ID}, {"b1", lb.ID}, {"c1", lc.ID}} {
		if err := lr.Bind(st, []byte(bind.key), bind.id); err != nil {
			t.Fatalf("Bind(%s, %d): %v", bind.key, bind.id, err)
		}
	}
	if _, err := lr.Revoke(st, lc.ID); err != nil {
		t.Fatalf("Revoke C: %v", err)
	}
	return lr, la.ID, lb.ID
}

func TestRestore_RoundTripsLeasesBindingsAndSequence(t *testing.T) {
	st := memory.NewStorage()
	_, a, b := persistedLessor(t, st)

	restored := NewLessor()
	if err := restored.Restore(st); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if restored.Count() != 2 {
		t.Fatalf("restored Count = %d, want 2 (the revoked lease must stay gone)", restored.Count())
	}
	for _, want := range []struct {
		id   int64
		ttl  int64
		keys []string
	}{{a, 30, []string{"a1", "a2"}}, {b, 40, []string{"b1"}}} {
		got := restored.Lookup(want.id)
		if got == nil {
			t.Fatalf("lease %d not restored", want.id)
		}
		if got.TTL != want.ttl || got.GrantedTTL != want.ttl {
			t.Errorf("lease %d TTL = %d/%d, want %d", want.id, got.TTL, got.GrantedTTL, want.ttl)
		}
		if got.RemainingTTL < want.ttl-1 {
			t.Errorf("lease %d RemainingTTL = %d, want a full TTL of %d after restore", want.id, got.RemainingTTL, want.ttl)
		}
		if len(got.Keys) != len(want.keys) {
			t.Errorf("lease %d keys = %q, want %q", want.id, got.Keys, want.keys)
		}
	}
	if got := restored.LeaseOf([]byte("b1")); got != b {
		t.Errorf("b1 restored bound to %d, want %d: a re-put moved it to lease B", got, b)
	}
	if got := restored.LeaseOf([]byte("c1")); got != NoLease {
		t.Errorf("c1 restored bound to %d, want NoLease: its lease was revoked", got)
	}

	// The re-put survives the restore: revoking A still leaves b1 alone.
	keys, err := restored.Revoke(st, a)
	if err != nil {
		t.Fatalf("Revoke A after restore: %v", err)
	}
	for _, k := range keys {
		if string(k) == "b1" {
			t.Fatal("revoking A after restore returned b1, which was re-put under B")
		}
	}

	// The sequence continues past every ID ever granted, including the
	// revoked one, so no ID is reused.
	next, err := restored.Grant(st, 10)
	if err != nil {
		t.Fatalf("Grant after restore: %v", err)
	}
	if next.ID != 4 {
		t.Errorf("Grant after restore ID = %d, want 4", next.ID)
	}
}

func TestRestore_ReplacesExistingState(t *testing.T) {
	st := memory.NewStorage()
	lr := NewLessor()
	if _, err := lr.Grant(nopWriter{}, 10); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	mustBind(t, lr, 1, []byte("stale"))

	if err := lr.Restore(st); err != nil {
		t.Fatalf("Restore from an empty table: %v", err)
	}
	if lr.Count() != 0 || lr.LeaseOf([]byte("stale")) != NoLease {
		t.Error("Restore kept state the persisted table does not hold")
	}
	info, err := lr.Grant(st, 10)
	if err != nil {
		t.Fatalf("Grant after restore: %v", err)
	}
	if info.ID != 1 {
		t.Errorf("Grant after restoring an empty table ID = %d, want 1", info.ID)
	}
}

func TestRestore_CorruptTableIsRefusedAndLeavesStateUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(st *memory.MemoryStorage) error
	}{
		{"short sequence", func(st *memory.MemoryStorage) error {
			return st.Set([]byte(sequenceKey), []byte{1})
		}},
		{"short lease record", func(st *memory.MemoryStorage) error {
			return st.Set(leaseRecordKey(1), []byte{1})
		}},
		{"non-positive TTL", func(st *memory.MemoryStorage) error {
			return st.Set(leaseRecordKey(1), encodeInt64(0))
		}},
		{"lease id above sequence", func(st *memory.MemoryStorage) error {
			return st.Set(leaseRecordKey(9), encodeInt64(10))
		}},
		{"malformed binding", func(st *memory.MemoryStorage) error {
			return st.Set(bindingKey([]byte("a1")), []byte{1, 2})
		}},
		{"binding to an absent lease", func(st *memory.MemoryStorage) error {
			return st.Set(bindingKey([]byte("orphan")), encodeInt64(3))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := memory.NewStorage()
			persistedLessor(t, st)
			if err := tc.corrupt(st); err != nil {
				t.Fatalf("corrupt: %v", err)
			}

			lr := NewLessor()
			if _, err := lr.Grant(nopWriter{}, 10); err != nil {
				t.Fatalf("Grant: %v", err)
			}
			var cte *CorruptTableError
			if err := lr.Restore(st); !errors.As(err, &cte) {
				t.Fatalf("Restore error = %v, want *CorruptTableError", err)
			}
			if lr.Count() != 1 || !lr.Has(1) {
				t.Error("a refused Restore changed the Lessor")
			}
		})
	}
}

// errReadFault is the storage fault faultReader injects.
var errReadFault = errors.New("injected read fault")

// faultReader fails the sequence read, or the iteration, on request.
type faultReader struct {
	failGet  bool
	failIter bool
}

func (r faultReader) Get(_ []byte) ([]byte, error) {
	if r.failGet {
		return nil, errReadFault
	}
	return nil, nil
}

func (r faultReader) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return faultIterator{fail: r.failIter}
}

// faultIterator is an empty iteration whose Error reports a fault on request.
type faultIterator struct{ fail bool }

func (faultIterator) SeekToFirst() bool  { return false }
func (faultIterator) SeekToLast() bool   { return false }
func (faultIterator) Seek(_ []byte) bool { return false }
func (faultIterator) Next() bool         { return false }
func (faultIterator) Prev() bool         { return false }
func (faultIterator) Valid() bool        { return false }
func (faultIterator) Key() []byte        { return nil }
func (faultIterator) Value() []byte      { return nil }
func (faultIterator) Close() error       { return nil }
func (it faultIterator) Error() error {
	if it.fail {
		return errReadFault
	}
	return nil
}

func TestRestore_ReadFaultIsReported(t *testing.T) {
	for _, r := range []faultReader{{failGet: true}, {failIter: true}} {
		lr := NewLessor()
		var tre *TableReadError
		err := lr.Restore(r)
		if !errors.As(err, &tre) || !errors.Is(err, errReadFault) {
			t.Errorf("Restore with %+v error = %v, want *TableReadError wrapping the fault", r, err)
		}
	}
}

func TestIsTableKey(t *testing.T) {
	for _, k := range [][]byte{leaseRecordKey(1), bindingKey([]byte("k")), []byte(sequenceKey)} {
		if !IsTableKey(k) {
			t.Errorf("IsTableKey(%q) = false", k)
		}
	}
	for _, k := range [][]byte{[]byte("\x00__quicraft_last_applied__"), []byte("user/key"), nil} {
		if IsTableKey(k) {
			t.Errorf("IsTableKey(%q) = true", k)
		}
	}
}

func TestTableErrorMessages(t *testing.T) {
	for _, err := range []error{
		&TableWriteError{Err: errWriteFault},
		&TableReadError{Err: errReadFault},
		&CorruptTableError{Key: sequenceKey, Reason: "bad"},
	} {
		if err.Error() == "" {
			t.Errorf("%T has an empty message", err)
		}
	}
}
