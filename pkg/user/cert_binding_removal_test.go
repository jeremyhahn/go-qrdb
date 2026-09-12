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

package user

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// newThumbprint mints a fresh key and returns the canonical SPKI thumbprint
// of a certificate issued for it.
func newThumbprint(t *testing.T, cn string, serial int64) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return ComputeSPKIThumbprint(makeCertForKey(t, key, cn, serial))
}

// newRemovalFixture builds a store backed by a memKVStore the test can reach
// (to install hooks and to read index keys directly) plus one enrolled user.
func newRemovalFixture(t *testing.T) (*kvUserStore, *memKVStore, *User) {
	t.Helper()
	kv := newMemKVStore()
	s := &kvUserStore{store: kv, idgen: newTestIDGen(t)}
	u, err := s.CreateUser(context.Background(), &CreateUserRequest{
		Username: "admin", TenantID: "system", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return s, kv, u
}

// ---------------------------------------------------------------------------
// The record entry and the index entry both go
// ---------------------------------------------------------------------------

// TestRemoveCertBinding_DropsRecordEntryAndScopedIndex is the base case: after
// a withdrawal the record no longer lists the thumbprint and
// UserByCertThumbprint no longer resolves it. Both halves are asserted,
// because a removal that dropped only the record would leave a live index
// entry, and one that dropped only the index would leave a record claiming a
// credential the store cannot resolve.
func TestRemoveCertBinding_DropsRecordEntryAndScopedIndex(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "admin", 1)

	if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	got, err := s.RemoveCertBinding(ctx, u.ID, tp)
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	for _, b := range got.CertBindings {
		if b.Thumbprint == tp {
			t.Fatalf("withdrawn binding %s still on the returned record", tp)
		}
	}

	if _, err := s.UserByCertThumbprint(ctx, tp); err == nil {
		t.Fatal("UserByCertThumbprint still resolves a withdrawn thumbprint")
	}
	idx, err := kv.Get(ctx, certThumbprintIndexKey("system", tp))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx != nil {
		t.Fatalf("scoped index entry survives the withdrawal: %v", idx)
	}
}

// TestRemoveCertBinding_LeavesTheOtherBindingsAlone is the companion: one key
// is withdrawn and the account's other enrolled key keeps working. A
// whole-set replacement built from a stale view is how the second one
// disappears, so it is asserted rather than assumed.
func TestRemoveCertBinding_LeavesTheOtherBindingsAlone(t *testing.T) {
	s, _, u := newRemovalFixture(t)
	ctx := context.Background()
	withdrawn := newThumbprint(t, "admin", 2)
	kept := newThumbprint(t, "admin", 3)

	for _, tp := range []string{withdrawn, kept} {
		if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
			t.Fatalf("AddCertBinding(%s): %v", tp, err)
		}
	}

	got, err := s.RemoveCertBinding(ctx, u.ID, withdrawn)
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if len(got.CertBindings) != 1 || got.CertBindings[0].Thumbprint != kept {
		t.Fatalf("bindings = %+v, want exactly the kept one", got.CertBindings)
	}
	resolved, err := s.UserByCertThumbprint(ctx, kept)
	if err != nil {
		t.Fatalf("the retained binding stopped resolving: %v", err)
	}
	if resolved.ID != u.ID {
		t.Fatalf("retained binding resolves user %d, want %d", resolved.ID, u.ID)
	}
}

// ---------------------------------------------------------------------------
// The last binding — a posture change, asserted deliberately
// ---------------------------------------------------------------------------

// TestRemoveCertBinding_LastBindingLeavesTheSetEmpty pins the consequence that
// makes withdrawal security-relevant in both directions: an account with no
// bindings states NO restriction, so withdrawing the last one returns it to
// admission on the certificate's CN and OU alone. The store must therefore
// commit an empty set rather than leaving a stale entry behind, and the
// widening must be a choice a reader can see rather than a surprise.
func TestRemoveCertBinding_LastBindingLeavesTheSetEmpty(t *testing.T) {
	s, _, u := newRemovalFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "admin", 4)

	if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	got, err := s.RemoveCertBinding(ctx, u.ID, tp)
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if len(got.CertBindings) != 0 {
		t.Fatalf("bindings = %+v, want none", got.CertBindings)
	}

	// Re-read: the committed record, not just the returned value.
	reread, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(reread.CertBindings) != 0 {
		t.Fatalf("committed bindings = %+v, want none", reread.CertBindings)
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// TestRemoveCertBinding_UnknownThumbprintReportsNotFound: a withdrawal that
// removed nothing must say so. Reporting success would read as "the key is
// withdrawn" to the operator who mistyped it while the real binding is still
// live.
func TestRemoveCertBinding_UnknownThumbprintReportsNotFound(t *testing.T) {
	s, _, u := newRemovalFixture(t)
	ctx := context.Background()
	if _, err := s.AddCertBinding(ctx, u.ID, newThumbprint(t, "admin", 5), 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	_, err := s.RemoveCertBinding(ctx, u.ID, newThumbprint(t, "admin", 6))
	var nf *CertBindingNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %T(%v), want *CertBindingNotFoundError", err, err)
	}
	if nf.UserID != u.ID {
		t.Fatalf("error names user %d, want %d", nf.UserID, u.ID)
	}
}

// TestRemoveCertBinding_RejectsEmptyThumbprint keeps the argument validation
// symmetric with AddCertBinding.
func TestRemoveCertBinding_RejectsEmptyThumbprint(t *testing.T) {
	s, _, u := newRemovalFixture(t)
	_, err := s.RemoveCertBinding(context.Background(), u.ID, "")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %T(%v), want *ValidationError", err, err)
	}
}

// TestRemoveCertBinding_UnknownUserReportsNotFound covers the by-id miss.
func TestRemoveCertBinding_UnknownUserReportsNotFound(t *testing.T) {
	s, _, _ := newRemovalFixture(t)
	_, err := s.RemoveCertBinding(context.Background(), 999999, newThumbprint(t, "ghost", 7))
	var nf *UserNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %T(%v), want *UserNotFoundError", err, err)
	}
}

// TestRemoveCertBinding_RefusesARecordWhoseDeletionHasBegun mirrors the guard
// AddCertBinding carries. A terminal record accepts no edits at all; a
// withdrawal is not an exception, because the deletion cascade has already
// swept the index the withdrawal would maintain.
func TestRemoveCertBinding_RefusesARecordWhoseDeletionHasBegun(t *testing.T) {
	s, _, u := newRemovalFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "admin", 8)
	if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if _, err := s.UpdateUser(ctx, &UpdateUserRequest{ID: u.ID, Lifecycle: LifecycleDeleting}); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	_, err := s.RemoveCertBinding(ctx, u.ID, tp)
	var lt *LifecycleTransitionError
	if !errors.As(err, &lt) {
		t.Fatalf("err = %T(%v), want *LifecycleTransitionError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Withdrawal must be able to remove what enrolment can no longer write
// ---------------------------------------------------------------------------

// TestRemoveCertBinding_WithdrawsANonCanonicalThumbprint is why withdrawal
// does NOT apply IsValidSPKIThumbprint. That validator is a write-side gate
// added after the field existed; a record written before it can still hold a
// non-canonical string. Refusing to withdraw one would make the very entries
// the gate exists to keep out the only entries nobody can remove.
func TestRemoveCertBinding_WithdrawsANonCanonicalThumbprint(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	const legacy = "not-a-canonical-thumbprint"

	// Write the record the way an older build could have: straight through
	// UpdateUser, which does not validate the thumbprint's shape.
	if _, err := s.UpdateUser(ctx, &UpdateUserRequest{
		ID:           u.ID,
		CertBindings: []CertBinding{{Thumbprint: legacy}},
	}); err != nil {
		t.Fatalf("seed legacy binding: %v", err)
	}

	got, err := s.RemoveCertBinding(ctx, u.ID, legacy)
	if err != nil {
		t.Fatalf("RemoveCertBinding(non-canonical): %v", err)
	}
	if len(got.CertBindings) != 0 {
		t.Fatalf("bindings = %+v, want none", got.CertBindings)
	}
	idx, err := kv.Get(ctx, certThumbprintIndexKey("system", legacy))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx != nil {
		t.Fatal("index entry for the non-canonical thumbprint survives")
	}
}

// TestRemoveCertBinding_ClearsTheLegacyUnscopedIndexEntry covers the
// pre-tenant-scoping global key. UserByCertThumbprint still falls back to it,
// so a withdrawal that left it in place would leave the credential resolvable
// by exactly the path the fallback exists to serve.
func TestRemoveCertBinding_ClearsTheLegacyUnscopedIndexEntry(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "admin", 9)

	if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	// Plant the legacy entry the way a pre-scoping deployment holds it.
	if err := kv.Put(ctx, legacyCertThumbprintIndexKey(tp), encodeID(u.ID)); err != nil {
		t.Fatalf("plant legacy index: %v", err)
	}

	if _, err := s.RemoveCertBinding(ctx, u.ID, tp); err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	legacy, err := kv.Get(ctx, legacyCertThumbprintIndexKey(tp))
	if err != nil {
		t.Fatalf("read legacy index: %v", err)
	}
	if legacy != nil {
		t.Fatal("legacy un-scoped index entry survives the withdrawal")
	}
	if _, err := s.UserByCertThumbprint(ctx, tp); err == nil {
		t.Fatal("the withdrawn thumbprint still resolves through the legacy fallback")
	}
}

// ---------------------------------------------------------------------------
// The race this verb exists to survive
// ---------------------------------------------------------------------------

// TestCertBindingWrites_AWithdrawalIsNotUndoneByAConcurrentEnrolment is the
// case that decides the shape of both verbs.
//
// An operator withdraws the compromised key T while an enrolment for an
// UNRELATED key U is in flight. The enrolment read the record before the
// withdrawal committed, so its view still contains T. If it commits that view,
// T is back — reinstated by a call that never asked for it, on an account
// whose operator has been told the withdrawal succeeded. That is the failure
// this issue exists to prevent, and no amount of care in the removal verb
// alone can close it: the resurrection happens on the ADD side.
//
// The interleaving is forced, not raced: afterGet lands the withdrawal in the
// window between the enrolment's read and its commit — once, so a retry that
// re-reads sees the committed world.
func TestCertBindingWrites_AWithdrawalIsNotUndoneByAConcurrentEnrolment(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	withdrawn := newThumbprint(t, "admin", 10)
	enrolled := newThumbprint(t, "admin", 11)

	if _, err := s.AddCertBinding(ctx, u.ID, withdrawn, 0); err != nil {
		t.Fatalf("seed the binding to withdraw: %v", err)
	}

	var interfered bool
	kv.afterGet = func(key string) {
		if interfered || key != primaryKey(u.ID) {
			return
		}
		interfered = true
		if _, err := s.RemoveCertBinding(ctx, u.ID, withdrawn); err != nil {
			t.Errorf("interfering withdrawal failed: %v", err)
		}
	}

	if _, err := s.AddCertBinding(ctx, u.ID, enrolled, 0); err != nil {
		t.Fatalf("AddCertBinding losing the race: %v", err)
	}
	kv.afterGet = nil
	if !interfered {
		t.Fatal("the fixture never landed the interfering withdrawal")
	}

	final, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	for _, b := range final.CertBindings {
		if b.Thumbprint == withdrawn {
			t.Fatal("the withdrawn binding was resurrected by the concurrent enrolment")
		}
	}
	if !final.HasCertBinding(enrolled) {
		t.Fatal("the enrolment that lost the race did not land")
	}

	// The index must agree with the record on BOTH: the withdrawn key resolves
	// nothing, the enrolled key resolves the user.
	if _, err := s.UserByCertThumbprint(ctx, withdrawn); err == nil {
		t.Fatal("the withdrawn thumbprint still resolves")
	}
	resolved, err := s.UserByCertThumbprint(ctx, enrolled)
	if err != nil {
		t.Fatalf("the enrolled thumbprint does not resolve: %v", err)
	}
	if resolved.ID != u.ID {
		t.Fatalf("enrolled thumbprint resolves user %d, want %d", resolved.ID, u.ID)
	}
}

// TestCertBindingWrites_AnEnrolmentIsNotUndoneByAConcurrentWithdrawal is the
// same race with the roles swapped: the WITHDRAWAL loses. It must not commit
// its stale view either, or a freshly enrolled key vanishes on an operation
// that named a different one.
func TestCertBindingWrites_AnEnrolmentIsNotUndoneByAConcurrentWithdrawal(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	withdrawn := newThumbprint(t, "admin", 12)
	enrolled := newThumbprint(t, "admin", 13)

	if _, err := s.AddCertBinding(ctx, u.ID, withdrawn, 0); err != nil {
		t.Fatalf("seed the binding to withdraw: %v", err)
	}

	var interfered bool
	kv.afterGet = func(key string) {
		if interfered || key != primaryKey(u.ID) {
			return
		}
		interfered = true
		if _, err := s.AddCertBinding(ctx, u.ID, enrolled, 0); err != nil {
			t.Errorf("interfering enrolment failed: %v", err)
		}
	}

	if _, err := s.RemoveCertBinding(ctx, u.ID, withdrawn); err != nil {
		t.Fatalf("RemoveCertBinding losing the race: %v", err)
	}
	kv.afterGet = nil
	if !interfered {
		t.Fatal("the fixture never landed the interfering enrolment")
	}

	final, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if final.HasCertBinding(withdrawn) {
		t.Fatal("the withdrawal did not take effect")
	}
	if !final.HasCertBinding(enrolled) {
		t.Fatal("the concurrent enrolment was silently dropped by the withdrawal")
	}
}

// TestRemoveCertBinding_ReportsAConflictItCannotResolve bounds the retry. A
// record under unbounded contention must eventually report
// ConcurrentUpdateError rather than spin: the caller decides whether a
// withdrawal is worth another attempt.
func TestRemoveCertBinding_ReportsAConflictItCannotResolve(t *testing.T) {
	s, kv, u := newRemovalFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "admin", 14)
	if _, err := s.AddCertBinding(ctx, u.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}

	// Every attempt loses: an unrelated writer bumps the record between the
	// read and the commit, forever.
	var reentrant bool
	kv.afterGet = func(key string) {
		if reentrant || key != primaryKey(u.ID) {
			return
		}
		reentrant = true
		defer func() { reentrant = false }()
		raw, err := kv.Get(ctx, primaryKey(u.ID))
		if err != nil || raw == nil {
			return
		}
		var rec User
		if jerr := json.Unmarshal(raw, &rec); jerr != nil {
			return
		}
		rec.UpdatedAt = rec.UpdatedAt.Add(time.Second)
		next, jerr := json.Marshal(&rec)
		if jerr != nil {
			return
		}
		if perr := kv.Put(ctx, primaryKey(u.ID), next); perr != nil {
			t.Errorf("interfering put: %v", perr)
		}
	}
	defer func() { kv.afterGet = nil }()

	_, err := s.RemoveCertBinding(ctx, u.ID, tp)
	var cu *ConcurrentUpdateError
	if !errors.As(err, &cu) {
		t.Fatalf("err = %T(%v), want *ConcurrentUpdateError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Store failures on the withdrawal path
// ---------------------------------------------------------------------------

// TestRemoveCertBinding_PropagatesAPrimaryLookupFailure: a backend that cannot
// be read must not be reported as "the binding is not there". The two answers
// mean opposite things to an operator withdrawing a compromised key.
func TestRemoveCertBinding_PropagatesAPrimaryLookupFailure(t *testing.T) {
	s := &kvUserStore{store: &errKVStore{getErr: errors.New("backend down")}, idgen: newTestIDGen(t)}

	_, err := s.RemoveCertBinding(context.Background(), 42, newThumbprint(t, "admin", 15))
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T(%v), want *StoreError", err, err)
	}
	if se.Op != "remove_cert_binding" {
		t.Fatalf("StoreError.Op = %q, want remove_cert_binding", se.Op)
	}
	var nf *CertBindingNotFoundError
	if errors.As(err, &nf) {
		t.Fatal("an unreadable backend was reported as a missing binding")
	}
}

// TestRemoveCertBinding_ReportsACorruptRecord covers the decode failure. A
// record that will not unmarshal is a store fault, not an absent binding, and
// the withdrawal must say so rather than removing the account's whole set by
// computing a withdrawal from a zero value.
func TestRemoveCertBinding_ReportsACorruptRecord(t *testing.T) {
	kv := newMemKVStore()
	s := &kvUserStore{store: kv, idgen: newTestIDGen(t)}
	const id uint64 = 77
	if err := kv.Put(context.Background(), primaryKey(id), []byte("{not json")); err != nil {
		t.Fatalf("plant corrupt record: %v", err)
	}

	_, err := s.RemoveCertBinding(context.Background(), id, newThumbprint(t, "admin", 16))
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T(%v), want *StoreError", err, err)
	}
	if se.Detail != "unmarshal" {
		t.Fatalf("StoreError.Detail = %q, want unmarshal", se.Detail)
	}
}

// TestRemoveCertBinding_RejectsAZeroUserID keeps the argument validation
// symmetric with every other by-id verb: zero is not a user id.
func TestRemoveCertBinding_RejectsAZeroUserID(t *testing.T) {
	s, _, _ := newRemovalFixture(t)
	_, err := s.RemoveCertBinding(context.Background(), 0, newThumbprint(t, "admin", 17))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %T(%v), want *ValidationError", err, err)
	}
}
