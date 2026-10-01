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
	"bytes"
	"errors"
	"testing"
)

// errWriteFault is the storage fault faultWriter injects.
var errWriteFault = errors.New("injected write fault")

// faultWriter fails every write.
type faultWriter struct{}

func (faultWriter) Set(_, _ []byte) error { return errWriteFault }
func (faultWriter) Delete(_ []byte) error { return errWriteFault }

// countingWriter counts the writes it accepts.
type countingWriter struct{ writes int }

func (w *countingWriter) Set(_, _ []byte) error { w.writes++; return nil }
func (w *countingWriter) Delete(_ []byte) error { w.writes++; return nil }

// grantTwo grants two leases and returns their IDs.
func grantTwo(t *testing.T, lr *Lessor) (a, b int64) {
	t.Helper()
	la, err := lr.Grant(nopWriter{}, 60)
	if err != nil {
		t.Fatalf("Grant A: %v", err)
	}
	lb, err := lr.Grant(nopWriter{}, 60)
	if err != nil {
		t.Fatalf("Grant B: %v", err)
	}
	return la.ID, lb.ID
}

// revokedKeys revokes id and returns the keys the revoke handed back for
// deletion.
func revokedKeys(t *testing.T, lr *Lessor, id int64) [][]byte {
	t.Helper()
	keys, err := lr.Revoke(nopWriter{}, id)
	if err != nil {
		t.Fatalf("Revoke(%d): %v", id, err)
	}
	return keys
}

// The defect this file exists for: a key written under lease A and then
// written again under lease B stayed in A's key set, so revoking A deleted
// the value written under B.
func TestBind_RePutUnderAnotherLeaseLeavesTheOldLease(t *testing.T) {
	lr := NewLessor()
	a, b := grantTwo(t, lr)
	k := []byte("svc/instance-1")

	mustBind(t, lr, a, k)
	mustBind(t, lr, b, k)

	if got := revokedKeys(t, lr, a); len(got) != 0 {
		t.Fatalf("revoking the old lease returned %q for deletion; the key was re-put under another lease and must survive", got)
	}
	if got := lr.LeaseOf(k); got != b {
		t.Fatalf("LeaseOf after revoking the old lease = %d, want the new lease %d", got, b)
	}
	if got := revokedKeys(t, lr, b); len(got) != 1 || !bytes.Equal(got[0], k) {
		t.Fatalf("revoking the current lease returned %q, want [%q]", got, k)
	}
	if got := lr.LeaseOf(k); got != NoLease {
		t.Fatalf("LeaseOf after revoking the current lease = %d, want NoLease", got)
	}
}

func TestBind_RePutWithNoLeaseLeavesTheOldLease(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	k := []byte("config/flag")

	mustBind(t, lr, a, k)
	mustBind(t, lr, NoLease, k)

	if got := revokedKeys(t, lr, a); len(got) != 0 {
		t.Fatalf("revoking the old lease returned %q for deletion; the key was re-put without a lease and must survive", got)
	}
	if got := lr.LeaseOf(k); got != NoLease {
		t.Fatalf("LeaseOf = %d, want NoLease", got)
	}
}

func TestBind_TimeToLiveReportsOnlyCurrentKeys(t *testing.T) {
	lr := NewLessor()
	a, b := grantTwo(t, lr)
	mustBind(t, lr, a, []byte("moved"))
	mustBind(t, lr, a, []byte("stays"))
	mustBind(t, lr, b, []byte("moved"))

	gotA := lr.Lookup(a)
	if len(gotA.Keys) != 1 || string(gotA.Keys[0]) != "stays" {
		t.Errorf("lease A keys = %q, want [stays]", gotA.Keys)
	}
	gotB := lr.Lookup(b)
	if len(gotB.Keys) != 1 || string(gotB.Keys[0]) != "moved" {
		t.Errorf("lease B keys = %q, want [moved]", gotB.Keys)
	}
}

func TestBind_SameLeaseWritesNothing(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	k := []byte("k")
	mustBind(t, lr, a, k)

	w := &countingWriter{}
	if err := lr.Bind(w, k, a); err != nil {
		t.Fatalf("re-Bind to the same lease: %v", err)
	}
	if err := lr.Unbind(w, []byte("never-bound")); err != nil {
		t.Fatalf("Unbind of an unbound key: %v", err)
	}
	if w.writes != 0 {
		t.Errorf("idempotent binds staged %d writes, want 0", w.writes)
	}
}

func TestBind_UnknownLeaseKeepsTheExistingBinding(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	k := []byte("k")
	mustBind(t, lr, a, k)

	var nfe *NotFoundError
	if err := lr.Bind(nopWriter{}, k, 777); !errors.As(err, &nfe) {
		t.Fatalf("Bind to unknown lease error = %v, want *NotFoundError", err)
	}
	if got := lr.LeaseOf(k); got != a {
		t.Errorf("LeaseOf after refused Bind = %d, want unchanged %d", got, a)
	}
}

func TestBind_WriteFaultChangesNothing(t *testing.T) {
	lr := NewLessor()
	a, b := grantTwo(t, lr)
	k := []byte("k")
	mustBind(t, lr, a, k)

	var twe *TableWriteError
	if err := lr.Bind(faultWriter{}, k, b); !errors.As(err, &twe) || !errors.Is(err, errWriteFault) {
		t.Fatalf("Bind under a write fault error = %v, want *TableWriteError wrapping the fault", err)
	}
	if err := lr.Unbind(faultWriter{}, k); !errors.As(err, &twe) {
		t.Fatalf("Unbind under a write fault error = %v, want *TableWriteError", err)
	}
	if got := lr.LeaseOf(k); got != a {
		t.Errorf("LeaseOf after failed writes = %d, want unchanged %d", got, a)
	}
	if got := lr.Lookup(b).Keys; len(got) != 0 {
		t.Errorf("lease B gained %q from a failed Bind", got)
	}
}

func TestGrant_WriteFaultChangesNothing(t *testing.T) {
	lr := NewLessor()
	var twe *TableWriteError
	if _, err := lr.Grant(faultWriter{}, 10); !errors.As(err, &twe) {
		t.Fatalf("Grant under a write fault error = %v, want *TableWriteError", err)
	}
	if lr.Count() != 0 {
		t.Fatalf("Count after failed Grant = %d, want 0", lr.Count())
	}
	info, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if info.ID != 1 {
		t.Errorf("first successful Grant ID = %d, want 1: a failed grant must not consume an ID", info.ID)
	}
}

func TestRevoke_WriteFaultKeepsTheLease(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	mustBind(t, lr, a, []byte("k"))

	var twe *TableWriteError
	if _, err := lr.Revoke(faultWriter{}, a); !errors.As(err, &twe) {
		t.Fatalf("Revoke under a write fault error = %v, want *TableWriteError", err)
	}
	if !lr.Has(a) || lr.LeaseOf([]byte("k")) != a {
		t.Error("a failed Revoke removed the lease or its binding")
	}
}

func TestRevoke_ReturnsKeysInOrder(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	for _, k := range []string{"c", "a", "d", "b"} {
		mustBind(t, lr, a, []byte(k))
	}
	got := revokedKeys(t, lr, a)
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("Revoke returned %q, want %q", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("Revoke returned %q, want %q", got, want)
		}
	}
}

func TestHas(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	if !lr.Has(NoLease) {
		t.Error("Has(NoLease) = false, want true")
	}
	if !lr.Has(a) {
		t.Errorf("Has(%d) = false for a granted lease", a)
	}
	if lr.Has(a + 100) {
		t.Errorf("Has(%d) = true for a lease never granted", a+100)
	}
}

// nthFaultWriter accepts writes until the nth, which fails.
type nthFaultWriter struct {
	n, seen int
}

func (w *nthFaultWriter) next() error {
	w.seen++
	if w.seen == w.n {
		return errWriteFault
	}
	return nil
}

func (w *nthFaultWriter) Set(_, _ []byte) error { return w.next() }
func (w *nthFaultWriter) Delete(_ []byte) error { return w.next() }

// A grant writes the lease record, then the sequence. A fault on the second
// write must leave the lease ungranted and the sequence where it was.
func TestGrant_SequenceWriteFaultChangesNothing(t *testing.T) {
	lr := NewLessor()
	var twe *TableWriteError
	if _, err := lr.Grant(&nthFaultWriter{n: 2}, 10); !errors.As(err, &twe) {
		t.Fatalf("Grant with a failing sequence write error = %v, want *TableWriteError", err)
	}
	if lr.Count() != 0 || lr.Has(1) {
		t.Fatal("a grant whose sequence write failed left a lease behind")
	}
}

// A revoke releases the bindings, then deletes the lease record. A fault on
// the record delete must leave the lease and its bindings in place.
func TestRevoke_RecordDeleteFaultKeepsTheLease(t *testing.T) {
	lr := NewLessor()
	a, _ := grantTwo(t, lr)
	mustBind(t, lr, a, []byte("k"))

	var twe *TableWriteError
	if _, err := lr.Revoke(&nthFaultWriter{n: 2}, a); !errors.As(err, &twe) {
		t.Fatalf("Revoke with a failing record delete error = %v, want *TableWriteError", err)
	}
	if !lr.Has(a) || lr.LeaseOf([]byte("k")) != a {
		t.Fatal("a revoke whose record delete failed removed the lease or its binding")
	}
}

// The sequence is the only thing standing between a grant and an ID already
// in use. If it is ever behind the table, Grant refuses rather than
// overwriting a live lease.
func TestGrant_RefusesAnIDAlreadyHeld(t *testing.T) {
	lr := NewLessor()
	held, err := lr.Grant(nopWriter{}, 10)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	lr.mu.Lock()
	lr.idSeq = held.ID - 1
	lr.mu.Unlock()

	var ee *ExistsError
	if _, err := lr.Grant(nopWriter{}, 20); !errors.As(err, &ee) || ee.ID != held.ID {
		t.Fatalf("Grant onto a held ID error = %v, want *ExistsError{ID: %d}", err, held.ID)
	}
	if got := lr.Lookup(held.ID); got == nil || got.TTL != 10 {
		t.Fatalf("lease %d after the refused grant = %+v, want the original 10s lease", held.ID, got)
	}
}
