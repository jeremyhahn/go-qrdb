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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// checkFirstInitCertificate / WithPendingTenantAdmins -- the trust-on-
// first-init window's admission rule for a tenant onboarded with a pending
// admin (barrier_bootstrap.go). These use the REAL tenantadmin.KVStore over
// an in-process kvstore.MemoryKVStore, and real certificates minted with
// mintCert (cert_binding_test.go, same package), rather than a hand-rolled
// double that could assert its own idea of SPKI matching instead of
// user.ComputeSPKIThumbprint's actual behaviour.
// ---------------------------------------------------------------------------

// newPendingStore returns a real tenantadmin.Store over an empty in-process
// store.
func newPendingStore(t *testing.T) tenantadmin.Store {
	t.Helper()
	return tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())
}

// erroringPendingStore is a PendingTenantAdmins whose Get always fails, for
// the fail-closed case checkFirstInitCertificate must take when the store
// itself is unavailable.
type erroringPendingStore struct{ err error }

func (e *erroringPendingStore) Get(context.Context, string) (*tenantadmin.Pending, error) {
	return nil, e.err
}

// TestCheckFirstInitCertificate_PendingMatch_Allowed proves a pending
// record's admin, presenting the certificate it was onboarded with, passes.
func TestCheckFirstInitCertificate_PendingMatch_Allowed(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(ck.cert),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}

	if err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); err != nil {
		t.Errorf("expected the onboarded admin's own certificate to be admitted, got: %v", err)
	}
}

// TestCheckFirstInitCertificate_PendingMismatchSPKI_Refused proves a
// DIFFERENT certificate (even one naming the same username) is refused with
// *FirstInitCertificateMismatchError.
func TestCheckFirstInitCertificate_PendingMismatchSPKI_Refused(t *testing.T) {
	onboarded := mintCert(t, "alice", "acme", nil, "")
	presented := mintCert(t, "alice", "acme", nil, "") // different key -> different SPKI
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(onboarded.cert),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}

	err := m.checkFirstInitCertificate(mtlsContext(presented), caller, "acme")
	if err == nil {
		t.Fatal("expected a certificate other than the onboarded one to be refused")
	}
	var mismatch *FirstInitCertificateMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *FirstInitCertificateMismatchError, got %T: %v", err, err)
	}
}

// TestCheckFirstInitCertificate_MatchingSPKIWrongUsername_Refused proves
// that presenting the RIGHT key is not enough on its own: the caller's
// identity must also name the onboarded admin.
func TestCheckFirstInitCertificate_MatchingSPKIWrongUsername_Refused(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(ck.cert),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	// Same certificate (matching SPKI), but the caller identity names a
	// different subject than the onboarded admin.
	caller := &auth.Identity{Subject: "mallory", TenantID: "acme"}

	err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme")
	if err == nil {
		t.Fatal("expected a matching SPKI with the wrong subject to be refused")
	}
	var mismatch *FirstInitCertificateMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *FirstInitCertificateMismatchError, got %T: %v", err, err)
	}
}

// TestCheckFirstInitCertificate_NoRecord_Required_Refused proves that under
// the "required" binding posture, a tenant onboarded with no pending admin
// admits no certificate at all.
func TestCheckFirstInitCertificate_NoRecord_Required_Refused(t *testing.T) {
	store := newPendingStore(t) // empty: no pending record for "acme"
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	ck := mintCert(t, "anyone", "acme", nil, "")
	caller := &auth.Identity{Subject: "anyone", TenantID: "acme"}

	err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme")
	if err == nil {
		t.Fatal("expected no pending record under \"required\" to refuse every certificate")
	}
	var notOnboarded *FirstInitNotOnboardedError
	if !errors.As(err, &notOnboarded) {
		t.Fatalf("expected *FirstInitNotOnboardedError, got %T: %v", err, err)
	}
}

// TestCheckFirstInitCertificate_NoRecord_Enrolled_Unchanged proves the
// "enrolled" posture (the default) keeps the pre-existing trust-on-first-
// init behaviour for a tenant with no pending admin record: any
// authenticated identity of the tenant passes.
func TestCheckFirstInitCertificate_NoRecord_Enrolled_Unchanged(t *testing.T) {
	store := newPendingStore(t) // empty
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	ck := mintCert(t, "anyone", "acme", nil, "")
	caller := &auth.Identity{Subject: "anyone", TenantID: "acme"}

	if err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); err != nil {
		t.Errorf("expected the pre-existing TOFI behaviour (no record, \"enrolled\") to admit any identity of the tenant, got: %v", err)
	}
}

// TestCheckFirstInitCertificate_StoreError_FailsClosed proves a store that
// cannot answer refuses the request rather than falling through to "no
// record".
func TestCheckFirstInitCertificate_StoreError_FailsClosed(t *testing.T) {
	want := errors.New("replicated store unreachable")
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(&erroringPendingStore{err: want}), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	ck := mintCert(t, "anyone", "acme", nil, "")
	caller := &auth.Identity{Subject: "anyone", TenantID: "acme"}

	err := m.checkFirstInitCertificate(mtlsContext(ck), caller, "acme")
	if err == nil {
		t.Fatal("expected a store failure to refuse, fail-closed")
	}
	var su *StoreUnavailableError
	if !errors.As(err, &su) {
		t.Fatalf("expected *StoreUnavailableError, got %T: %v", err, err)
	}
	if !errors.Is(err, want) {
		t.Errorf("expected the underlying store error to be reachable via errors.Is, got: %v", err)
	}
}

// TestCheckFirstInitCertificate_NilStore_TreatedAsNoRecord proves a
// middleware with NO pendingAdmins configured at all behaves exactly like
// one whose store answered "no record" -- under "required" it refuses, and
// under "enrolled" it keeps admitting any identity of the tenant.
func TestCheckFirstInitCertificate_NilStore_TreatedAsNoRecord(t *testing.T) {
	ck := mintCert(t, "anyone", "acme", nil, "")
	caller := &auth.Identity{Subject: "anyone", TenantID: "acme"}

	required := New(&mockAuthorizer{}, WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	err := required.checkFirstInitCertificate(mtlsContext(ck), caller, "acme")
	var notOnboarded *FirstInitNotOnboardedError
	if !errors.As(err, &notOnboarded) {
		t.Fatalf("expected *FirstInitNotOnboardedError with a nil store under \"required\", got %T: %v", err, err)
	}

	enrolled := New(&mockAuthorizer{}, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	if err := enrolled.checkFirstInitCertificate(mtlsContext(ck), caller, "acme"); err != nil {
		t.Errorf("expected a nil store under \"enrolled\" to admit the identity, got: %v", err)
	}
}

// TestClassifyAuthError_FirstInitErrorsAreUnauthorized proves both new error
// types classify as AuthFailureUnauthorized: the caller DID establish who it
// is (a verified certificate reached the window), and that identity may not
// initialize this barrier -- 403, never 401 or 503.
func TestClassifyAuthError_FirstInitErrorsAreUnauthorized(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"mismatch", &FirstInitCertificateMismatchError{TenantID: "acme", Username: "alice"}},
		{"not onboarded", &FirstInitNotOnboardedError{TenantID: "acme"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthError(tc.err); got != AuthFailureUnauthorized {
				t.Errorf("ClassifyAuthError(%T) = %v, want %v", tc.err, got, AuthFailureUnauthorized)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AuthorizeTenantBarrierInit end-to-end: the full fallback wiring, not just
// checkFirstInitCertificate in isolation.
// ---------------------------------------------------------------------------

// TestAuthorizeTenantBarrierInit_PendingAdminMatch_GrantsFallback proves the
// full call -- RBAC denial (no role can exist yet), never-initialized
// status, pending record, matching certificate -- ends in a grant.
func TestAuthorizeTenantBarrierInit_PendingAdminMatch_GrantsFallback(t *testing.T) {
	ck := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(ck.cert),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := New(&mockAuthorizer{err: errors.New("no role can satisfy tenant_barrier:init yet")},
		WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	identity := &auth.Identity{Subject: "alice", TenantID: "acme"}
	ctx := auth.WithIdentity(mtlsContext(ck), identity)
	status := &fakeBarrierStatusReader{initialized: false}

	if err := m.AuthorizeTenantBarrierInit(ctx, "acme", status); err != nil {
		t.Errorf("expected the trust-on-first-init window to grant the onboarded admin, got: %v", err)
	}
}

// TestAuthorizeTenantBarrierInit_WrongCertificate_Refused proves the same
// call with a DIFFERENT certificate is refused end-to-end, not merely at the
// unit level.
func TestAuthorizeTenantBarrierInit_WrongCertificate_Refused(t *testing.T) {
	onboarded := mintCert(t, "alice", "acme", nil, "")
	presented := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(onboarded.cert),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	m := New(&mockAuthorizer{err: errors.New("no role can satisfy tenant_barrier:init yet")},
		WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired))
	identity := &auth.Identity{Subject: "alice", TenantID: "acme"}
	ctx := auth.WithIdentity(mtlsContext(presented), identity)
	status := &fakeBarrierStatusReader{initialized: false}

	err := m.AuthorizeTenantBarrierInit(ctx, "acme", status)
	if err == nil {
		t.Fatal("expected a certificate other than the onboarded one to be refused end-to-end")
	}
	var mismatch *FirstInitCertificateMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *FirstInitCertificateMismatchError, got %T: %v", err, err)
	}
}
