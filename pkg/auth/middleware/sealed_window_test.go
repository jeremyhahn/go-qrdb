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
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// sealedIdentity returns an identity stamped exactly as finalizeIdentity
// stamps one whose tenant IAM was unreachable at authentication time.
func sealedIdentity(tenantID string) *auth.Identity {
	return &auth.Identity{
		Subject:  "admin",
		TenantID: tenantID,
		Metadata: map[string]string{tenantIAMSealedMetadataKey: "true"},
	}
}

// sealedCtx binds a sealed-marked identity for tenantID into a context.
func sealedCtx(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), sealedIdentity(tenantID))
}

// TestAuthorize_SealedMarkerConvertsToUnavailable pins the retryable-503
// contract: a normal permission evaluated for a sealed-marked identity is
// answered StoreUnavailableError wrapping ErrTenantSealed, never a
// definitive RBAC denial computed from the empty role set.
func TestAuthorize_SealedMarkerConvertsToUnavailable(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	err := m.AuthorizeTenant(sealedCtx("t1"), "t1", permission.KVPut)
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want *StoreUnavailableError", err)
	}
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("err = %v, want it to wrap tenant.ErrTenantSealed", err)
	}
	if authz.calls.Load() != 0 {
		t.Fatal("the RBAC authorizer must not be consulted for a sealed-marked identity")
	}
}

// TestAuthorize_SealedMarkerRefusesAdminHealth pins that the sealed window
// has no health exception: Health is a no-login verb on every transport
// (pkg/api/publicverbs), served before authorization, so admin:health is
// refused like every other permission while the tenant is sealed.
func TestAuthorize_SealedMarkerRefusesAdminHealth(t *testing.T) {
	authz := &mockAuthorizer{}
	m := newTestMiddleware(authz)

	err := m.AuthorizeTenant(sealedCtx("t1"), "t1", permission.AdminHealth)
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("admin:health during the sealed window = %v, want *StoreUnavailableError", err)
	}
	if authz.calls.Load() != 0 {
		t.Fatal("the sealed-window refusal must precede RBAC")
	}
}

// TestAuthorize_SealedMarkerPassesIdentityGated verifies the IdentityGated
// bootstrap surface stays reachable for a sealed-marked identity.
func TestAuthorize_SealedMarkerPassesIdentityGated(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if err := m.AuthorizeTenant(sealedCtx("t1"), "t1", permission.IdentityGated); err != nil {
		t.Fatalf("IdentityGated during the sealed window = %v, want nil", err)
	}
}

// TestIdentityIAMSealed verifies the exported marker predicate in both
// directions plus the nil case.
func TestIdentityIAMSealed(t *testing.T) {
	if !IdentityIAMSealed(sealedIdentity("t1")) {
		t.Fatal("marked identity must report sealed")
	}
	if IdentityIAMSealed(&auth.Identity{Subject: "admin", TenantID: "t1"}) {
		t.Fatal("unmarked identity must not report sealed")
	}
	if IdentityIAMSealed(nil) {
		t.Fatal("nil identity must not report sealed")
	}
}

// TestAuthorizeTenantBarrierSealedOp_FallbackAllowsOwnTenant verifies the
// sealed-window fallback: RBAC cannot be evaluated (marker → unavailable),
// the caller's identity names the target tenant, so status/unseal are
// admitted.
func TestAuthorizeTenantBarrierSealedOp_FallbackAllowsOwnTenant(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if err := m.AuthorizeTenantBarrierSealedOp(sealedCtx("t1"), "t1", permission.TenantBarrierUnseal); err != nil {
		t.Fatalf("sealed-window unseal by the tenant's own identity = %v, want nil", err)
	}
}

// TestAuthorizeTenantBarrierSealedOp_FallbackDeniesCrossTenant verifies the
// fallback never crosses tenants: a sealed-marked identity for another
// tenant is refused.
func TestAuthorizeTenantBarrierSealedOp_FallbackDeniesCrossTenant(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if err := m.AuthorizeTenantBarrierSealedOp(sealedCtx("t-OTHER"), "t1", permission.TenantBarrierUnseal); err == nil {
		t.Fatal("cross-tenant sealed-window fallback must be refused")
	}
}

// TestAuthorizeTenantBarrierSealedOp_NoMarkerRequiresRBAC verifies the
// fallback is unavailable once the barrier is unsealed (no marker): the
// RBAC decision stands.
func TestAuthorizeTenantBarrierSealedOp_NoMarkerRequiresRBAC(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t1"})
	if err := m.AuthorizeTenantBarrierSealedOp(ctx, "t1", permission.TenantBarrierStatus); err == nil {
		t.Fatal("without the sealed marker the RBAC denial must stand")
	}
}

// fakeBarrierStatusReader reports a fixed initialization state.
type fakeBarrierStatusReader struct {
	initialized bool
	err         error
}

func (f *fakeBarrierStatusReader) TenantBarrierStatus(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &tenant.TenantBarrierStatus{TenantID: tenantID, Initialized: f.initialized}, nil
}

// TestAuthorizeTenantBarrierInit_TrustOnFirstInit verifies the bootstrap
// window: RBAC denies (no roles exist yet), the barrier has never been
// initialized, and the tenant's own identity is admitted. This test
// exercises the window itself, not the separate first-admin-onboarding gate
// (see first_init_gate_test.go), so it runs under the "enrolled" posture,
// which skips that gate when the tenant has no pending-admin record.
func TestAuthorizeTenantBarrierInit_TrustOnFirstInit(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t1"})
	if err := m.AuthorizeTenantBarrierInit(ctx, "t1", &fakeBarrierStatusReader{initialized: false}); err != nil {
		t.Fatalf("trust-on-first-init = %v, want nil", err)
	}
}

// holderIdentity returns an identity authenticated with a holder (Security
// Officer/custodian) certificate for tenantID, stamped exactly as the mTLS
// authenticator stamps one -- auth.HolderProfileMetadataKey set to the
// profile OU. Such an identity is never enriched from the user store (see
// auth.IsHolderIdentity), so it holds no roles.
func holderIdentity(tenantID, profile string) *auth.Identity {
	return &auth.Identity{
		Subject:  "custodian",
		TenantID: tenantID,
		Metadata: map[string]string{auth.HolderProfileMetadataKey: profile},
	}
}

// TestAuthorizeTenantBarrierInit_HolderIdentityNeverOpensTheWindow proves a
// share-holder certificate can never trigger trust-on-first-init for a
// tenant barrier. Every element of
// TestAuthorizeTenantBarrierInit_TrustOnFirstInit's admitted case is
// reproduced here -- RBAC denies, the caller's own tenant, the barrier never
// initialized -- except the caller authenticated with a holder certificate
// rather than an ordinary tenant identity. A share-holder cert proves
// custody of a quorum share, not an operator identity entitled to
// initialize the tenant's IAM barrier; admitting it here would let a
// custodian who will never hold an RBAC grant stand up a tenant's entire
// identity store.
func TestAuthorizeTenantBarrierInit_HolderIdentityNeverOpensTheWindow(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), holderIdentity("t1", holderprofile.Custodian))
	if err := m.AuthorizeTenantBarrierInit(ctx, "t1", &fakeBarrierStatusReader{initialized: false}); err == nil {
		t.Fatal("a share-holder identity must never initialize a tenant barrier via trust-on-first-init")
	}
}

// TestAuthorizeTenantBarrierSealedOp_HolderIdentityNeverOpensTheWindow
// proves the sealed-window fallback (status/unseal while IAM is
// unreachable) is refused the same way for a holder identity, even when the
// middleware's own sealed marker is present on it. A custodian's whole
// reason to exist is custody of a Shamir share toward the ceremony
// quorum -- the barrier operations this window exists to unblock are not
// that path.
func TestAuthorizeTenantBarrierSealedOp_HolderIdentityNeverOpensTheWindow(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	id := holderIdentity("t1", holderprofile.SecurityOfficer)
	id.Metadata[tenantIAMSealedMetadataKey] = "true"
	ctx := auth.WithIdentity(context.Background(), id)
	if err := m.AuthorizeTenantBarrierSealedOp(ctx, "t1", permission.TenantBarrierUnseal); err == nil {
		t.Fatal("a share-holder identity must never pass the sealed-window fallback")
	}
}

// TestAuthorizeTenantBarrierInit_ClosedAfterInitialization verifies the
// window closes: once the barrier reports initialized, the RBAC denial
// stands.
func TestAuthorizeTenantBarrierInit_ClosedAfterInitialization(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t1"})
	if err := m.AuthorizeTenantBarrierInit(ctx, "t1", &fakeBarrierStatusReader{initialized: true}); err == nil {
		t.Fatal("an initialized barrier must demand the RBAC grant")
	}
}

// TestAuthorizeTenantBarrierInit_CrossTenantRefused verifies the window
// never admits a foreign tenant's identity.
func TestAuthorizeTenantBarrierInit_CrossTenantRefused(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t-OTHER"})
	if err := m.AuthorizeTenantBarrierInit(ctx, "t1", &fakeBarrierStatusReader{initialized: false}); err == nil {
		t.Fatal("cross-tenant trust-on-first-init must be refused")
	}
}

// TestAuthorizeTenantBarrierInit_SystemTenantRefused verifies neither
// bootstrap window ever opens for the system tenant: it has no per-tenant
// barrier, and the tenant-scoped RBAC rule denies it deliberately.
func TestAuthorizeTenantBarrierInit_SystemTenantRefused(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: tenantid.System})
	if err := m.AuthorizeTenantBarrierInit(ctx, tenantid.System, &fakeBarrierStatusReader{initialized: false}); err == nil {
		t.Fatal("system-tenant trust-on-first-init must be refused")
	}
}

// TestAuthorizeTenantBarrierSealedOp_SystemTenantRefused verifies the
// sealed-window fallback never opens for the system tenant either.
func TestAuthorizeTenantBarrierSealedOp_SystemTenantRefused(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if err := m.AuthorizeTenantBarrierSealedOp(sealedCtx(tenantid.System), tenantid.System, permission.TenantBarrierUnseal); err == nil {
		t.Fatal("system-tenant sealed-window fallback must be refused")
	}
}

// TestAuthorizeTenantBarrierInit_StatusErrorFailsClosed verifies an
// unreadable initialization state keeps the RBAC denial (fail closed).
func TestAuthorizeTenantBarrierInit_StatusErrorFailsClosed(t *testing.T) {
	deny := &mockAuthorizer{err: errors.New("rbac: permission denied")}
	m := newTestMiddleware(deny)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "t1"})
	if err := m.AuthorizeTenantBarrierInit(ctx, "t1", &fakeBarrierStatusReader{err: errors.New("status unavailable")}); err == nil {
		t.Fatal("an unreadable barrier status must fail closed to the RBAC denial")
	}
}
