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

// Tenant-administration decisions (tenant_admin.go) and the rate-limit
// half of the bootstrap fallbacks (barrier_bootstrap.go), against the REAL
// built-in roles: rbac.New over a role store reconciled exactly as a node
// reconciles it. A stubbed authorizer would only prove that a check was asked
// for; these prove that the grants the product ships refuse the request.

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// builtInMiddleware is a middleware over the real built-in roles of acme,
// globex and the system tenant.
func builtInMiddleware(t *testing.T, opts ...Option) *AuthMiddleware {
	t.Helper()
	return New(rbac.New(authtest.BuiltInRoles(t, "acme", "globex", tenantid.System)), opts...)
}

// as binds id as the authenticated caller, in its own tenant, the way
// finalizeIdentity does.
func as(id *auth.Identity) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), id.TenantID)
}

func allTenants() []tenant.TenantInfo {
	return []tenant.TenantInfo{
		{ID: tenantid.System},
		{ID: "acme", VolumePool: "fast"},
		{ID: "globex", VolumePool: "fast"},
	}
}

func tenantIDs(infos []tenant.TenantInfo) []string {
	ids := make([]string, len(infos))
	for i := range infos {
		ids[i] = infos[i].ID
	}
	return ids
}

// A tenant Admin may rename its own tenant, but changing its isolation tier
// is a platform-operator operation.
func TestAuthorizeTenantUpdate_TenantAdminMayOnlyRenameItself(t *testing.T) {
	m := builtInMiddleware(t)
	ctx := as(authtest.AdminIdentity("acme", "acme-admin"))
	if err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{Name: "Acme"}); err != nil {
		t.Fatalf("acme admin renaming acme = %v, want nil", err)
	}
	err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{IsolationType: tenant.IsolationBarrier})
	var denied *rbac.PermissionDeniedError
	if !errors.As(err, &denied) || denied.Resource+":"+denied.Action != permission.TenantsSetIsolation.String() {
		t.Fatalf("acme admin changing acme's isolation = %v, want a tenants:set_isolation denial", err)
	}
	if err := m.AuthorizeTenantUpdate(ctx, "globex", tenant.TenantPatch{Name: "x"}); err == nil {
		t.Fatal("acme admin renaming globex must be refused")
	}
}

func TestAuthorizeTenantUpdate_SystemAdminMayChangeIsolation(t *testing.T) {
	m := builtInMiddleware(t)
	ctx := as(authtest.AdminIdentity(tenantid.System, "root"))
	// A pure isolation-type change needs only tenants:set_isolation
	// (ScopeCross, platform-operator), which the system admin holds.
	if err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{IsolationType: tenant.IsolationBarrier}); err != nil {
		t.Fatalf("system admin changing acme's isolation = %v, want nil", err)
	}
	// tenants:update itself stays self-scoped (owner ruling, 2026-10-01: the
	// system-tenant lift it relied on for another tenant's name is gone),
	// but renaming another tenant is ordinary platform administration, the
	// same as reading or deleting it, so AuthorizeTenantTarget's cross
	// fallback (tenants:update:cross) admits it. A patch that renames AND
	// reisolates "acme" is admitted too: each half is authorized on its own
	// merits.
	if err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{Name: "Acme", IsolationType: tenant.IsolationBarrier}); err != nil {
		t.Fatalf("system admin renaming+reisolating acme = %v, want nil", err)
	}
}

// The plan permissions are ScopeCross: the named tenant does not matter, only
// whether the caller is the platform operator.
func TestTenantProvisioning_OnlyThePlatformOperator(t *testing.T) {
	m := builtInMiddleware(t)
	tenantAdmin := as(authtest.AdminIdentity("acme", "acme-admin"))
	operator := as(authtest.AdminIdentity(tenantid.System, "root"))
	for _, perm := range []permission.Permission{
		permission.TenantsCreate, permission.TenantsSetQuotas,
		permission.TenantsSetEntitlements, permission.TenantsSetIsolation,
	} {
		if err := m.AuthorizeTenant(tenantAdmin, "acme", perm); err == nil {
			t.Errorf("acme admin %s on its own tenant = nil, want a refusal", perm)
		}
		if err := m.AuthorizeTenant(operator, "acme", perm); err != nil {
			t.Errorf("system admin %s on acme = %v, want nil", perm, err)
		}
	}
}

func TestAuthorizeTenantTarget_TenantAdminRefusedOnAnotherTenant(t *testing.T) {
	m := builtInMiddleware(t)
	ctx := as(authtest.AdminIdentity("acme", "acme-admin"))
	for _, perm := range []permission.Permission{
		permission.TenantsRead, permission.TenantsUpdate, permission.TenantsDelete,
	} {
		if err := m.AuthorizeTenantTarget(ctx, "globex", perm); err == nil {
			t.Errorf("acme admin %s on globex = nil, want a refusal", perm)
		}
		if err := m.AuthorizeTenantTarget(ctx, "acme", perm); err != nil {
			t.Errorf("acme admin %s on acme = %v, want nil", perm, err)
		}
	}
}

func TestFilterTenantList_TenantAdminSeesOnlyItsOwnTenant(t *testing.T) {
	m := builtInMiddleware(t)
	got, err := m.FilterTenantList(as(authtest.AdminIdentity("acme", "acme-admin")), allTenants())
	if err != nil {
		t.Fatalf("FilterTenantList: %v", err)
	}
	if ids := tenantIDs(got); len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("acme admin listed %v, want [acme]", ids)
	}
}

func TestFilterTenantList_SystemAdminSeesEveryTenant(t *testing.T) {
	m := builtInMiddleware(t)
	got, err := m.FilterTenantList(as(authtest.AdminIdentity(tenantid.System, "root")), allTenants())
	if err != nil {
		t.Fatalf("FilterTenantList: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("system admin listed %v, want every tenant", tenantIDs(got))
	}
}

func TestFilterTenantList_UnauthenticatedIsRefusedNotNarrowed(t *testing.T) {
	m := builtInMiddleware(t)
	got, err := m.FilterTenantList(context.Background(), allTenants())
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want auth.ErrUnauthenticated", err)
	}
	if got != nil {
		t.Fatalf("an unauthenticated caller was answered %v", tenantIDs(got))
	}
}

// The cross-list check is a capability probe: an ordinary tenant-Admin list
// writes no denial record, and so does not fail when the audit sink cannot
// record one.
func TestFilterTenantList_ProbeIsUnaudited(t *testing.T) {
	sink := &mockAuditLogger{err: errors.New("disk full")}
	m := builtInMiddleware(t, WithAuditLogger(sink))
	got, err := m.FilterTenantList(as(authtest.AdminIdentity("acme", "acme-admin")), allTenants())
	if err != nil {
		t.Fatalf("FilterTenantList with a failing sink = %v, want the narrowed list", err)
	}
	if ids := tenantIDs(got); len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("acme admin listed %v, want [acme]", ids)
	}
	if n := sink.calls.Load(); n != 0 {
		t.Fatalf("the probe wrote %d audit records, want 0", n)
	}
}

func TestFilterTenantList_ProbeChargesNoToken(t *testing.T) {
	l := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = l.Close() })
	m := builtInMiddleware(t, WithRateLimiter(l), WithRateLimitPolicies(authtest.OneToken(permission.TenantsListCross)))
	ctx := as(authtest.AdminIdentity(tenantid.System, "root"))
	for i := 0; i < 3; i++ {
		if got, err := m.FilterTenantList(ctx, allTenants()); err != nil || len(got) != 3 {
			t.Fatalf("operator list #%d = %d tenants, %v; want 3 and no rate limit", i+1, len(got), err)
		}
	}
}

// A sealed tenant IAM is not a denial: the list is refused, not narrowed.
func TestFilterTenantList_SealedIdentityIsRefused(t *testing.T) {
	m := builtInMiddleware(t)
	_, err := m.FilterTenantList(as(authtest.SealedIdentity("acme", "acme-admin")), allTenants())
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want *StoreUnavailableError", err)
	}
}

// oneTokenMiddleware rate-limits perm to a single token over the real roles.
func oneTokenMiddleware(t *testing.T, perm permission.Permission, opts ...Option) *AuthMiddleware {
	t.Helper()
	l := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = l.Close() })
	return builtInMiddleware(t, append([]Option{WithRateLimiter(l), WithRateLimitPolicies(authtest.OneToken(perm))}, opts...)...)
}

// TestAuthorizeTenantBarrierSealedOp_RateLimitIsNotOverriddenByTheWindow: the
// limiter refuses before RBAC is consulted, so its refusal is not a denial the
// sealed window may override. Before the fix the second unseal was admitted.
func TestAuthorizeTenantBarrierSealedOp_RateLimitIsNotOverriddenByTheWindow(t *testing.T) {
	m := oneTokenMiddleware(t, permission.TenantBarrierUnseal)
	ctx := as(authtest.SealedIdentity("acme", "acme-admin"))

	if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal); err != nil {
		t.Fatalf("first sealed-window unseal = %v, want nil", err)
	}
	err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal)
	var limited *RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("second sealed-window unseal = %v, want *RateLimitedError", err)
	}
}

func TestAuthorizeTenantBarrierSealedOp_OtherPermissionsKeepTheirOwnBucket(t *testing.T) {
	m := oneTokenMiddleware(t, permission.TenantBarrierUnseal)
	ctx := as(authtest.SealedIdentity("acme", "acme-admin"))
	for i := 0; i < 3; i++ {
		if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierStatus); err != nil {
			t.Fatalf("sealed-window status #%d = %v, want nil: it has no policy", i+1, err)
		}
	}
}

// TestAuthorizeTenantBarrierInit_RateLimitIsNotOverriddenByTheWindow is the
// trust-on-first-init half of the same defect.
func TestAuthorizeTenantBarrierInit_RateLimitIsNotOverriddenByTheWindow(t *testing.T) {
	// This test exercises the rate limiter's precedence over the window, not
	// the separate first-admin-onboarding gate (see first_init_gate_test.go),
	// so it runs under the "enrolled" posture, which skips that gate when the
	// tenant has no pending-admin record.
	m := oneTokenMiddleware(t, permission.TenantBarrierInit, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	// A role-less identity: RBAC refuses, so only the window can admit it.
	ctx := as(&auth.Identity{Subject: "acme-admin", TenantID: "acme"})
	status := &fakeBarrierStatusReader{initialized: false}

	if err := m.AuthorizeTenantBarrierInit(ctx, "acme", status); err != nil {
		t.Fatalf("first trust-on-first-init = %v, want nil", err)
	}
	err := m.AuthorizeTenantBarrierInit(ctx, "acme", status)
	var limited *RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("second trust-on-first-init = %v, want *RateLimitedError", err)
	}
}

func TestIsRateLimited(t *testing.T) {
	if !isRateLimited(&RateLimitedError{Permission: permission.TenantBarrierUnseal}) {
		t.Fatal("a *RateLimitedError must be recognised")
	}
	if isRateLimited(&rbac.PermissionDeniedError{}) || isRateLimited(nil) {
		t.Fatal("only a *RateLimitedError is a rate-limit refusal")
	}
}
