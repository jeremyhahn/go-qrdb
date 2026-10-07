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

package rbac

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ auth.Authorizer = (*RBACAuthorizer)(nil)

// ctxFor returns a context bound to the given target tenant. Tests use
// this helper to align the request's target tenant with the identity's
// tenant when exercising ScopeSelf permissions on a single tenant.
func ctxFor(targetTenantID string) context.Context {
	return tenant.WithTenantID(context.Background(), targetTenantID)
}

// ---------------------------------------------------------------------------
// Mock RoleServicer
// ---------------------------------------------------------------------------

// mockRoleService is a minimal RoleServicer stub for testing.
// ResolvePermissions and GetRoleByName are exercised; other methods panic if
// called.
type mockRoleService struct {
	perms     map[string][]permission.Permission // roleID -> permissions
	byName    map[string]*role.Role              // "tenantID/roleName" -> role
	byID      map[string]*role.Role              // roleID -> role
	byNameErr error                              // if non-nil, GetRoleByName returns this error
	byNameNil bool                               // if true, GetRoleByName returns (nil, nil): a contract violation
	byIDErr   error                              // if non-nil, GetRole returns this error
	byIDNil   bool                               // if true, GetRole returns (nil, nil): a contract violation
	callCount atomic.Int64
	err       error // if non-nil, ResolvePermissions returns this error
}

func newMockRoleService(perms map[string][]permission.Permission) *mockRoleService {
	return &mockRoleService{perms: perms}
}

// withRole registers r under its tenant and name AND under its identifier, so
// both GetRoleByName and GetRole resolve it — modelling a role already
// persisted in the store, which occupies both key spaces.
func (m *mockRoleService) withRole(r *role.Role) *mockRoleService {
	if m.byName == nil {
		m.byName = make(map[string]*role.Role)
	}
	m.byName[r.TenantID+"/"+r.Name] = r
	return m.withRecordOnly(r)
}

// withRecordOnly registers r under its identifier but NOT under its name,
// modelling a replica that has replicated the role record without the name
// index entry that points at it.
func (m *mockRoleService) withRecordOnly(r *role.Role) *mockRoleService {
	if m.byID == nil {
		m.byID = make(map[string]*role.Role)
	}
	m.byID[r.ID] = r
	return m
}

func (m *mockRoleService) ResolvePermissions(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
	m.callCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	seen := make(map[permission.Permission]struct{})
	var out []permission.Permission
	for _, id := range roleIDs {
		for _, p := range m.perms[id] {
			if _, dup := seen[p]; !dup {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func (m *mockRoleService) CreateRole(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
	panic("not implemented")
}

func (m *mockRoleService) GetRole(_ context.Context, id string) (*role.Role, error) {
	if m.byIDErr != nil {
		return nil, m.byIDErr
	}
	if m.byIDNil {
		return nil, nil
	}
	if r, ok := m.byID[id]; ok {
		return r, nil
	}
	return nil, &role.RoleNotFoundError{ID: id}
}

func (m *mockRoleService) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if m.byNameErr != nil {
		return nil, m.byNameErr
	}
	if m.byNameNil {
		return nil, nil
	}
	if r, ok := m.byName[tenantID+"/"+name]; ok {
		return r, nil
	}
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
}

func (m *mockRoleService) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	panic("not implemented")
}

func (m *mockRoleService) DeleteRole(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockRoleService) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	panic("not implemented")
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNew_WithDefaults(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	if authz.roles != svc {
		t.Fatal("expected roles field to reference the provided service")
	}
	if authz.cache == nil {
		t.Fatal("expected cache to be initialized")
	}
	if authz.cache.ttl != defaultCacheTTL {
		t.Fatalf("expected default TTL %v, got %v", defaultCacheTTL, authz.cache.ttl)
	}
}

func TestNew_WithCustomTTL(t *testing.T) {
	svc := newMockRoleService(nil)
	ttl := 5 * time.Minute
	authz := New(svc, WithCacheTTL(ttl))

	if authz.cache.ttl != ttl {
		t.Fatalf("expected TTL %v, got %v", ttl, authz.cache.ttl)
	}
}

// ---------------------------------------------------------------------------
// Authorize tests — basic admission
// ---------------------------------------------------------------------------

func TestAuthorize_NilIdentity(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	err := authz.Authorize(context.Background(), nil, "kv", "put")
	if err == nil {
		t.Fatal("expected error for nil identity")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthorize_NilIdentity_NoResolveCall(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	_ = authz.Authorize(context.Background(), nil, "kv", "get")
	if svc.callCount.Load() != 0 {
		t.Fatal("ResolvePermissions should not be called for nil identity")
	}
}

func TestAuthorize_Allowed_SelfScope_OwnTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"writer": {"kv:put", "kv:get"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-1",
		TenantID: "tenant-1",
		Roles:    []string{"writer"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "kv", "put")
	if err != nil {
		t.Fatalf("expected authorization to succeed, got error: %v", err)
	}
}

func TestAuthorize_WildcardGrants(t *testing.T) {
	// A role granting "keys:*" satisfies any concrete keys action, but does
	// not leak across resources. A "*:read" action wildcard matches any
	// resource's read; "*:*" matches everything.
	svc := newMockRoleService(map[string][]permission.Permission{
		"keymaster": {"keys:*"},
		"reader":    {"*:read"},
		"root":      {"*:*"},
	})
	authz := New(svc)

	cases := []struct {
		name             string
		role             string
		resource, action string
		wantAllowed      bool
	}{
		{"resource-wildcard-sign", "keymaster", "keys", "sign", true},
		{"resource-wildcard-get", "keymaster", "keys", "get", true},
		{"resource-wildcard-no-cross-resource", "keymaster", "certs", "generate", false},
		{"action-wildcard-match", "reader", "backends", "read", true},
		{"action-wildcard-no-cross-action", "reader", "backends", "list", false},
		{"full-wildcard", "root", "keys", "sign", true},
		// A wildcard grant matches the NAME; it does not conjure a permission
		// that was never declared. anything:whatever has no scope, so the
		// authorizer refuses it for root too (go-qrdb#504). This used to be
		// allowed — a "*:*" holder was authorized for a permission that did
		// not exist, because the undeclared name defaulted to ScopeSelf.
		{"full-wildcard-undeclared-permission", "root", "anything", "whatever", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := &auth.Identity{Subject: "u-" + tc.name, TenantID: "tenant-1", Roles: []string{tc.role}}
			err := authz.Authorize(ctxFor("tenant-1"), identity, tc.resource, tc.action)
			if tc.wantAllowed && err != nil {
				t.Fatalf("expected allow for %s:%s, got %v", tc.resource, tc.action, err)
			}
			if !tc.wantAllowed && err == nil {
				t.Fatalf("expected deny for %s:%s, got allow", tc.resource, tc.action)
			}
		})
	}
}

func TestAuthorize_RoleResolveFallbackOnTransientError(t *testing.T) {
	// Primary role store fails as if the host were draining / ReadIndex cold.
	primary := newMockRoleService(nil)
	primary.err = errors.New("service: read failed: quicraft: host is draining")
	// Fallback (StaleRead) resolves the permission from replicated state.
	fallback := newMockRoleService(map[string][]permission.Permission{
		"admin": {"admin:drain"},
	})
	// admin:drain is platform scope, so the drainer is the system tenant's.
	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"admin"}}

	// With a fallback, authorization succeeds despite the primary failure —
	// this is what keeps an authorized Drain admissible on a draining node.
	if err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain"); err != nil {
		t.Fatalf("expected fallback to authorize, got %v", err)
	}

	// Without a fallback, the primary failure surfaces as ResolveError.
	err := New(primary).Authorize(ctxFor(tenantid.System), identity, "admin", "drain")
	if err == nil {
		t.Fatal("expected ResolveError when primary fails and no fallback is set")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResolveError, got %T", err)
	}
}

func TestAuthorize_OwnerRoleVoidedUnderStrictSeparation(t *testing.T) {
	ownerID := role.SystemRoleID("tenant-1", role.RoleNameOwner)
	adminID := role.SystemRoleID("tenant-1", role.RoleNameAdmin)
	// Owner grants the data-plane permission; the admin role does not.
	svc := newMockRoleService(map[string][]permission.Permission{
		ownerID: {"kv:put"},
		adminID: {"tenants:read"},
	})
	identity := &auth.Identity{
		Subject:  "admin",
		TenantID: "tenant-1",
		Roles:    []string{adminID, ownerID},
	}

	// Standard posture: the Owner grant is honored, so kv:put is allowed.
	if err := New(svc).Authorize(ctxFor("tenant-1"), identity, "kv", "put"); err != nil {
		t.Fatalf("standard posture: expected kv:put allowed via Owner role, got %v", err)
	}

	// Strict posture: the Owner grant is voided, so kv:put is denied even
	// though the role store still assigns Owner to the identity.
	err := New(svc, WithStrictSeparation(true)).Authorize(ctxFor("tenant-1"), identity, "kv", "put")
	if err == nil {
		t.Fatal("strict posture: expected kv:put denied (Owner role voided)")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("strict posture: expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorize_Denied(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-2",
		TenantID: "tenant-1",
		Roles:    []string{"reader"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "kv", "put")
	if err == nil {
		t.Fatal("expected authorization to be denied")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}

	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T", err)
	}
	if denied.Resource != "kv" || denied.Action != "put" || denied.Subject != "user-2" {
		t.Fatalf("unexpected denied fields: %+v", denied)
	}
}

func TestAuthorize_MultipleRoles(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
		"writer": {"kv:put"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-3",
		TenantID: "tenant-1",
		Roles:    []string{"reader", "writer"},
	}

	// Should have union of both roles' permissions.
	for _, action := range []string{"get", "put"} {
		err := authz.Authorize(ctxFor("tenant-1"), identity, "kv", action)
		if err != nil {
			t.Fatalf("expected kv:%s to be allowed for multi-role user, got: %v", action, err)
		}
	}
}

func TestAuthorize_CacheHit(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-4",
		TenantID: "tenant-1",
		Roles:    []string{"reader"},
	}

	// First call: cache miss.
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	// Second call: should hit cache.
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")

	if svc.callCount.Load() != 1 {
		t.Fatalf("expected ResolvePermissions to be called once, got %d", svc.callCount.Load())
	}
}

func TestAuthorize_CacheExpiry(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc, WithCacheTTL(10*time.Millisecond))

	identity := &auth.Identity{
		Subject:  "user-5",
		TenantID: "tenant-1",
		Roles:    []string{"reader"},
	}

	// First call: cache miss.
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected 1 call after first authorize, got %d", svc.callCount.Load())
	}

	// Wait for cache to expire.
	time.Sleep(20 * time.Millisecond)

	// Second call: cache expired, should resolve again.
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 calls after cache expiry, got %d", svc.callCount.Load())
	}
}

func TestAuthorize_CacheMiss_ResolveError(t *testing.T) {
	inner := errors.New("connection refused")
	svc := newMockRoleService(nil)
	svc.err = inner
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-6",
		TenantID: "tenant-1",
		Roles:    []string{"reader"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if err == nil {
		t.Fatal("expected error from role service")
	}

	var resolveErr *ResolveError
	if !errors.As(err, &resolveErr) {
		t.Fatalf("expected *ResolveError, got %T: %v", err, err)
	}
	if !errors.Is(resolveErr, inner) {
		t.Fatal("expected ResolveError to wrap the original error")
	}
}

func TestAuthorize_DifferentTenants(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role":  {"admin:health"},
		"reader-role": {"kv:get"},
	})
	authz := New(svc)

	tenant1 := &auth.Identity{
		Subject:  "shared-user",
		TenantID: "tenant-a",
		Roles:    []string{"admin-role"},
	}
	tenant2 := &auth.Identity{
		Subject:  "shared-user",
		TenantID: "tenant-b",
		Roles:    []string{"reader-role"},
	}

	// Tenant A: admin permissions.
	err := authz.Authorize(ctxFor("tenant-a"), tenant1, "admin", "health")
	if err != nil {
		t.Fatalf("expected tenant-a to have admin:health, got: %v", err)
	}

	// Tenant B: only reader permissions - should not inherit tenant A's cache.
	err = authz.Authorize(ctxFor("tenant-b"), tenant2, "admin", "health")
	if err == nil {
		t.Fatal("expected tenant-b to be denied admin:health")
	}

	// Verify separate cache entries resulted in two resolve calls.
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 resolve calls (one per tenant), got %d", svc.callCount.Load())
	}
}

func TestAuthorize_EmptyRoles(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "no-role-user",
		TenantID: "tenant-1",
		Roles:    []string{},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if err == nil {
		t.Fatal("expected denial for identity with no roles")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorize_EmptyRoles_ResolveCalled(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "no-role-user",
		TenantID: "tenant-1",
		Roles:    []string{},
	}

	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected ResolvePermissions to be called for empty roles, got %d", svc.callCount.Load())
	}
}

// ---------------------------------------------------------------------------
// System-tenant rule tests
// ---------------------------------------------------------------------------

// TestAuthorize_SystemTenantRule_AdminAtSystem_CanCrossScope verifies that
// an identity in the system tenant carrying a ScopeCross permission may
// exercise it on any target tenant — the canonical replacement for the
// canonical platform-admin pattern (Admin@system).
func TestAuthorize_SystemTenantRule_AdminAtSystem_CanCrossScope(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsListCross},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin-role"},
	}

	for _, target := range []string{"acme", "bob-corp", tenantid.System, "anything"} {
		err := authz.Authorize(ctxFor(target), identity, "tenants", "list:cross")
		if err != nil {
			t.Errorf("Admin@system should be allowed tenants:list:cross on target %q, got: %v", target, err)
		}
	}
}

// TestAuthorize_SystemTenantRule_AdminAtTenant_CannotCrossScope verifies
// that a tenant-local Admin holding a ScopeCross permission is rejected —
// cross-scope authority requires membership in the system tenant.
func TestAuthorize_SystemTenantRule_AdminAtTenant_CannotCrossScope(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsListCross},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin-role"},
	}

	for _, target := range []string{"acme", "bob-corp"} {
		err := authz.Authorize(ctxFor(target), identity, "tenants", "list:cross")
		if err == nil {
			t.Errorf("Admin@acme must NOT be allowed tenants:list:cross on target %q", target)
			continue
		}
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("expected *PermissionDeniedError on target %q, got %T: %v", target, err, err)
			continue
		}
		if !strings.Contains(denied.Reason, "cross-scope") {
			t.Errorf("expected cross-scope denial reason on target %q, got %q", target, denied.Reason)
		}
	}
}

// TestAuthorize_SystemTenantRule_AdminAtAcme_CanReadOwnTenant verifies
// that a tenant-local Admin can exercise ScopeSelf permissions on its
// own tenant.
func TestAuthorize_SystemTenantRule_AdminAtAcme_CanReadOwnTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsRead},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin-role"},
	}

	err := authz.Authorize(ctxFor("acme"), identity, "tenants", "read")
	if err != nil {
		t.Fatalf("Admin@acme should be allowed tenants:read on own tenant, got: %v", err)
	}
}

// TestAuthorize_SystemTenantRule_AdminAtAcme_CannotReadOtherTenant
// verifies that a tenant-local Admin cannot exercise ScopeSelf permissions
// on a foreign tenant — the self-scope filter is the per-tenant
// boundary for non-system identities.
func TestAuthorize_SystemTenantRule_AdminAtAcme_CannotReadOtherTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsRead},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin-role"},
	}

	err := authz.Authorize(ctxFor("bob-corp"), identity, "tenants", "read")
	if err == nil {
		t.Fatal("Admin@acme must NOT be allowed tenants:read on bob-corp")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
	if !strings.Contains(denied.Reason, "self-scope") {
		t.Errorf("expected self-scope denial reason, got %q", denied.Reason)
	}
}

// TestAuthorize_SystemTenantRule_AdminAtSystem_CanReadAnyTenant verifies
// the system-tenant rule's self-scope lift: a system-tenant identity may
// exercise ScopeSelf permissions on any target tenant.
func TestAuthorize_SystemTenantRule_AdminAtSystem_CanReadAnyTenant(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsRead},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin-role"},
	}

	// ScopeSelf has no system-tenant lift (owner ruling, 2026-10-01): a
	// system-tenant identity reaches tenants:read on its OWN tenant only.
	if err := authz.Authorize(ctxFor(tenantid.System), identity, "tenants", "read"); err != nil {
		t.Errorf("Admin@system should be allowed tenants:read on its own tenant, got: %v", err)
	}
	for _, target := range []string{"acme", "bob-corp"} {
		if err := authz.Authorize(ctxFor(target), identity, "tenants", "read"); err == nil {
			t.Errorf("Admin@system should NOT be allowed tenants:read on target %q via the self-scope permission alone", target)
		}
	}

	// tenants:read:cross (ScopeCross) is the named replacement: it does not
	// look at the target tenant at all, so holding it admits every target.
	crossSvc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantsReadCross},
	})
	crossAuthz := New(crossSvc)
	for _, target := range []string{"acme", "bob-corp", tenantid.System} {
		if err := crossAuthz.Authorize(ctxFor(target), identity, "tenants", "read:cross"); err != nil {
			t.Errorf("Admin@system holding tenants:read:cross should be allowed on target %q, got: %v", target, err)
		}
	}
}

// TestAuthorize_SystemTenantRule_UserAtTenant_CannotManageTenants
// verifies that a User role identity (no tenant management perms) cannot
// run any tenant management permission, regardless of scope.
func TestAuthorize_SystemTenantRule_UserAtTenant_CannotManageTenants(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"user-role": {permission.KeysSign, permission.KeysVerify},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-user",
		TenantID: "acme",
		Roles:    []string{"user-role"},
	}

	deniedActions := []struct {
		resource, action string
	}{
		{"tenants", "read"},
		{"tenants", "update"},
		{"tenants", "list:cross"},
		{"volumepool", "create"},
		{"registration_config", "read"},
	}
	for _, tc := range deniedActions {
		err := authz.Authorize(ctxFor("acme"), identity, tc.resource, tc.action)
		if err == nil {
			t.Errorf("User@acme must NOT be allowed %s:%s", tc.resource, tc.action)
		}
	}
}

// ---------------------------------------------------------------------------
// Invalidation tests
// ---------------------------------------------------------------------------

func TestInvalidate_RemovesEntry(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-7",
		TenantID: "tenant-1",
		Roles:    []string{"reader"},
	}

	// First call: populates cache.
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", svc.callCount.Load())
	}

	// Invalidate and authorize again.
	authz.Invalidate("tenant-1", "user-7")
	_ = authz.Authorize(ctxFor("tenant-1"), identity, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 calls after invalidation, got %d", svc.callCount.Load())
	}
}

func TestInvalidate_OtherEntriesUnaffected(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc)

	user1 := &auth.Identity{Subject: "user-a", TenantID: "t1", Roles: []string{"reader"}}
	user2 := &auth.Identity{Subject: "user-b", TenantID: "t1", Roles: []string{"reader"}}

	_ = authz.Authorize(ctxFor("t1"), user1, "kv", "get")
	_ = authz.Authorize(ctxFor("t1"), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 initial calls, got %d", svc.callCount.Load())
	}

	// Invalidate only user-a.
	authz.Invalidate("t1", "user-a")

	// user-b should still be cached.
	_ = authz.Authorize(ctxFor("t1"), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected user-b to be cached (2 calls), got %d", svc.callCount.Load())
	}

	// user-a should resolve again.
	_ = authz.Authorize(ctxFor("t1"), user1, "kv", "get")
	if svc.callCount.Load() != 3 {
		t.Fatalf("expected user-a to re-resolve (3 calls), got %d", svc.callCount.Load())
	}
}

func TestInvalidateAll_ClearsEverything(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	authz := New(svc)

	user1 := &auth.Identity{Subject: "user-a", TenantID: "t1", Roles: []string{"reader"}}
	user2 := &auth.Identity{Subject: "user-b", TenantID: "t2", Roles: []string{"reader"}}

	_ = authz.Authorize(ctxFor("t1"), user1, "kv", "get")
	_ = authz.Authorize(ctxFor("t2"), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 initial calls, got %d", svc.callCount.Load())
	}

	authz.InvalidateAll()

	// Both should resolve again.
	_ = authz.Authorize(ctxFor("t1"), user1, "kv", "get")
	_ = authz.Authorize(ctxFor("t2"), user2, "kv", "get")
	if svc.callCount.Load() != 4 {
		t.Fatalf("expected 4 total calls after InvalidateAll, got %d", svc.callCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface test
// ---------------------------------------------------------------------------

func TestAuthorize_CompileTimeInterface(t *testing.T) {
	// The var _ auth.Authorizer = (*RBACAuthorizer)(nil) declaration above
	// handles compile-time checking. This test ensures the interface
	// dispatch path is exercised at runtime as well, using the canonical
	// Admin@system pattern.
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.AdminHealth},
	})
	var authz auth.Authorizer = New(svc)
	// Own tenant: this test exercises interface dispatch, not cross-tenant
	// scope semantics (the system-tenant lift was removed, 2026-10-01).
	err := authz.Authorize(ctxFor(tenantid.System), &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin-role"},
	}, "admin", "health")
	if err != nil {
		t.Fatalf("expected Admin@system to pass via interface, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestPermissionDeniedError_Error(t *testing.T) {
	err := &PermissionDeniedError{
		Resource: "kv",
		Action:   "delete",
		Subject:  "user-x",
	}
	msg := err.Error()
	if !strings.Contains(msg, "permission denied") {
		t.Fatalf("expected 'permission denied' in error, got: %s", msg)
	}
	if !strings.Contains(msg, "user-x") {
		t.Fatalf("expected subject in error, got: %s", msg)
	}
	if !strings.Contains(msg, "delete") {
		t.Fatalf("expected action in error, got: %s", msg)
	}
	if !strings.Contains(msg, "kv") {
		t.Fatalf("expected resource in error, got: %s", msg)
	}
}

func TestPermissionDeniedError_Unwrap(t *testing.T) {
	err := &PermissionDeniedError{Resource: "kv", Action: "put", Subject: "u"}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("PermissionDeniedError should unwrap to auth.ErrUnauthorized")
	}
}

func TestResolveError_Error(t *testing.T) {
	inner := errors.New("timeout")
	err := &ResolveError{Err: inner}
	msg := err.Error()
	if !strings.Contains(msg, "failed to resolve permissions") {
		t.Fatalf("expected 'failed to resolve permissions' in error, got: %s", msg)
	}
	if !strings.Contains(msg, "timeout") {
		t.Fatalf("expected cause in error, got: %s", msg)
	}
}

func TestResolveError_NilErr(t *testing.T) {
	err := &ResolveError{Err: nil}
	msg := err.Error()
	if !strings.Contains(msg, "failed to resolve permissions") {
		t.Fatalf("expected message without cause, got: %s", msg)
	}
	if err.Unwrap() != nil {
		t.Fatal("Unwrap should return nil for nil Err")
	}
}

func TestResolveError_Unwrap(t *testing.T) {
	inner := errors.New("rpc error")
	err := &ResolveError{Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("ResolveError should unwrap to the inner error")
	}
}

// ---------------------------------------------------------------------------
// Cache tests (directly exercised for completeness)
// ---------------------------------------------------------------------------

func TestCache_GetMiss(t *testing.T) {
	c := newPermissionCache(time.Minute)
	_, ok := c.get("t", "s", []string{"r1"})
	if ok {
		t.Fatal("expected cache miss for empty cache")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t", "s", []string{"r1"}, []permission.Permission{"kv:get", "kv:put"})

	perms, ok := c.get("t", "s", []string{"r1"})
	if !ok {
		t.Fatal("expected cache hit after set")
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 permissions, got %d", len(perms))
	}
	if _, has := perms["kv:get"]; !has {
		t.Fatal("expected kv:get in permission set")
	}
	if _, has := perms["kv:put"]; !has {
		t.Fatal("expected kv:put in permission set")
	}
}

func TestCache_Expiry(t *testing.T) {
	c := newPermissionCache(10 * time.Millisecond)
	c.set("t", "s", []string{"r1"}, []permission.Permission{"kv:get"})

	_, ok := c.get("t", "s", []string{"r1"})
	if !ok {
		t.Fatal("expected cache hit before expiry")
	}

	time.Sleep(20 * time.Millisecond)

	_, ok = c.get("t", "s", []string{"r1"})
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestCache_Invalidate(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t", "s1", []string{"r1"}, []permission.Permission{"kv:get"})
	c.set("t", "s2", []string{"r1"}, []permission.Permission{"kv:put"})

	c.invalidate("t", "s1")

	_, ok := c.get("t", "s1", []string{"r1"})
	if ok {
		t.Fatal("expected s1 to be invalidated")
	}
	_, ok = c.get("t", "s2", []string{"r1"})
	if !ok {
		t.Fatal("expected s2 to remain cached")
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t1", "s1", []string{"r1"}, []permission.Permission{"kv:get"})
	c.set("t2", "s2", []string{"r1"}, []permission.Permission{"kv:put"})

	c.invalidateAll()

	_, ok := c.get("t1", "s1", []string{"r1"})
	if ok {
		t.Fatal("expected t1:s1 to be cleared")
	}
	_, ok = c.get("t2", "s2", []string{"r1"})
	if ok {
		t.Fatal("expected t2:s2 to be cleared")
	}
}

func TestCacheKey(t *testing.T) {
	key := cacheKey("tenant-1", "user-a", []string{"r2", "r1"})
	expected := "tenant-1:user-a:r1,r2"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}

func TestCacheKey_EmptyTenant(t *testing.T) {
	key := cacheKey("", "user-a", nil)
	expected := ":user-a:"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}

// TestCacheKey_RoleSetSeparatesEntries pins the poisoning fix: an identity
// resolved with NO roles (a sealed-window or just-created subject) must not
// answer for the same subject once enrichment yields roles.
func TestCacheKey_RoleSetSeparatesEntries(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t", "s", nil, nil) // empty resolution, cached
	if _, ok := c.get("t", "s", []string{"role-1"}); ok {
		t.Fatal("an empty-role entry must not serve an enriched identity")
	}
	c.set("t", "s", []string{"role-1"}, []permission.Permission{"kv:get"})
	perms, ok := c.get("t", "s", []string{"role-1"})
	if !ok || len(perms) != 1 {
		t.Fatalf("enriched entry expected, got ok=%v perms=%v", ok, perms)
	}
}

// ---------------------------------------------------------------------------
// SO deny-list tests (FIPS 140-3 AS10.03)
// ---------------------------------------------------------------------------

func TestAuthorize_SO_CryptoActionsDenied(t *testing.T) {
	// Crypto actions that must be denied for SO regardless of permissions.
	deniedActions := []struct {
		resource string
		action   string
	}{
		{"keys", "sign"},
		{"keys", "encrypt"},
		{"keys", "decrypt"},
		{"keys", "verify"},
		{"keys", "wrap"},
		{"keys", "unwrap"},
		// Key agreement / KDF is key use like the six above.
		{"keys", "derive"},
		// Hardware-token key use: keys:sign reached through a smart card.
		{"piv", "use"},
		// Contributed by a higher layer (go-quicbaas): extends a PCR on the
		// host TPM and returns a signed quote. Denied by the
		// resource-independent verb rule, without qrdb knowing the resource.
		{"attestation", "attest"},
	}

	for _, tc := range deniedActions {
		t.Run(tc.resource+":"+tc.action, func(t *testing.T) {
			// Intentionally grant the SO role the crypto permission (malicious grant).
			svc := newMockRoleService(map[string][]permission.Permission{
				"so-role": {permission.Permission(tc.resource + ":" + tc.action)},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "so-user",
				TenantID: "tenant-1",
				Roles:    []string{"so-role", "so"}, // role name matches SO
			}

			err := authz.Authorize(ctxFor("tenant-1"), identity, tc.resource, tc.action)
			if err == nil {
				t.Fatalf("expected SO to be denied %s:%s even with explicit permission grant", tc.resource, tc.action)
			}
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected *PermissionDeniedError, got %T", err)
			}
			if !strings.Contains(denied.Reason, "FIPS 140-3 AS10.03") {
				t.Errorf("expected FIPS 140-3 AS10.03 in denial reason, got: %q", denied.Reason)
			}
		})
	}
}

func TestAuthorize_SO_MaliciousGrant_StillDenied(t *testing.T) {
	// The most critical test: SO is erroneously granted keys:sign but must
	// still be denied (FIPS 140-3 AS10.03 defense-in-depth).
	svc := newMockRoleService(map[string][]permission.Permission{
		"so-role": {permission.KeysSign},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "so-user",
		TenantID: "tenant-1",
		Roles:    []string{"so"}, // SO role by name
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("SO with erroneously granted keys:sign must still be denied")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got: %v", err)
	}
}

func TestAuthorize_SO_GovernanceActionsAllowed(t *testing.T) {
	// SO must be able to perform governance operations (barrier:initialize,
	// etc.). The node-barrier ones are platform scope (ScopeCross), so their
	// officer is the system tenant's; the rest are the SO's own tenant's.
	allowedActions := []struct {
		resource string
		action   string
		perm     permission.Permission
		tenantID string
	}{
		{"seal", "bootstrap", permission.SealBootstrap, tenantid.System},
		{"ceremony", "initialize", permission.CeremonyInitialize, tenantid.System},
		{"roles", "create", permission.RolesCreate, "tenant-1"},
		{"users", "create", permission.UsersCreate, "tenant-1"},
		{"audit", "read", permission.AuditRead, "tenant-1"},
	}

	for _, tc := range allowedActions {
		t.Run(tc.resource+":"+tc.action, func(t *testing.T) {
			svc := newMockRoleService(map[string][]permission.Permission{
				"so-role": {tc.perm},
			})
			authz := New(svc)

			identity := &auth.Identity{
				Subject:  "so-user",
				TenantID: tc.tenantID,
				Roles:    []string{"so-role", "so"},
			}

			err := authz.Authorize(ctxFor(tc.tenantID), identity, tc.resource, tc.action)
			if err != nil {
				t.Fatalf("SO should be allowed %s:%s, got error: %v", tc.resource, tc.action, err)
			}
		})
	}
}

func TestAuthorize_SO_DenyListByDeterministicID(t *testing.T) {
	// SO deny-list must also trigger when the role is identified by its
	// deterministic UUID (not just the "so" string).
	svc := newMockRoleService(map[string][]permission.Permission{})
	authz := New(svc)

	soRoleID := role.SystemRoleID("tenant-x", role.RoleNameSO)

	identity := &auth.Identity{
		Subject:  "so-by-id",
		TenantID: "tenant-x",
		Roles:    []string{soRoleID}, // referenced by deterministic UUID
	}

	err := authz.Authorize(ctxFor("tenant-x"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("SO deny-list must trigger when role is identified by deterministic UUID")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got: %v", err)
	}
}

func TestAuthorize_User_KeysSignAllowed(t *testing.T) {
	// User role must be allowed keys:sign (unlike SO).
	svc := newMockRoleService(map[string][]permission.Permission{
		"user-role": {permission.KeysSign, permission.KeysVerify, permission.KeysEncrypt, permission.KeysDecrypt},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-1",
		TenantID: "tenant-1",
		Roles:    []string{"user-role"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "keys", "sign")
	if err != nil {
		t.Fatalf("user should be allowed keys:sign, got: %v", err)
	}
}

func TestAuthorize_Auditor_AuditReadAllowed(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"auditor-role": {permission.AuditRead, permission.AuditQuery},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "auditor-1",
		TenantID: "tenant-1",
		Roles:    []string{"auditor-role"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "audit", "read")
	if err != nil {
		t.Fatalf("auditor should be allowed audit:read, got: %v", err)
	}
}

func TestAuthorize_Auditor_UsersCreateDenied(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"auditor-role": {permission.AuditRead, permission.AuditQuery, permission.UsersList},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "auditor-1",
		TenantID: "tenant-1",
		Roles:    []string{"auditor-role"},
	}

	err := authz.Authorize(ctxFor("tenant-1"), identity, "users", "create")
	if err == nil {
		t.Fatal("auditor should be denied users:create")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isSODenied helper tests
// ---------------------------------------------------------------------------

func TestIsSODenied_DeniedActions(t *testing.T) {
	tests := []struct {
		resource, action string
		want             bool
	}{
		{"keys", "sign", true},
		{"keys", "encrypt", true},
		{"keys", "decrypt", true},
		{"keys", "verify", true},
		{"keys", "wrap", true},
		{"keys", "unwrap", true},
		{"keys", "derive", true},
		{"piv", "use", true},
		// Cryptographic primitives in any resource family, including ones
		// contributed by a higher layer that this package cannot enumerate.
		{"attestation", "attest", true},
		{"tpm", "quote", true},
		{"envelope", "wrap", true},
		{"hkdf", "derive", true},
		{"integrity", "hmac", true},
		// Key lifecycle and inventory: governance, not key use.
		{"keys", "create", false},
		{"keys", "list", false},
		{"keys", "get", false},
		{"keys", "import", false},
		{"keys", "export", false},
		// PIV slot administration, as opposed to piv:use.
		{"piv", "read", false},
		{"piv", "write", false},
		// The SO's own FIPS-assigned module lifecycle. Cryptographic by
		// mechanism, governance by duty: denying these would break bootstrap.
		{"seal", "bootstrap", false},
		{"seal", "unseal", false},
		{"seal", "seal", false},
		{"seal", "rekey", false},
		{"ceremony", "sign_csr", false},
		{"certs", "generate", false},
		// "verify" that is an integrity check over stored bytes, not key use.
		{"backup", "verify", false},
		{"objstore", "verify", false},
		{"audit", "read", false},
		{"kv", "put", false},
	}
	for _, tc := range tests {
		t.Run(tc.resource+":"+tc.action, func(t *testing.T) {
			got := isSODenied(tc.resource, tc.action)
			if got != tc.want {
				t.Errorf("isSODenied(%q, %q) = %v, want %v", tc.resource, tc.action, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ScopeTenantOnly — per-tenant barrier authorization
//
// The tenant_barrier:* permissions are tagged ScopeTenantOnly: a tenant
// admin may operate only on their OWN tenant's barrier, the system tenant is
// structurally denied, and cross-tenant access is denied.
// ---------------------------------------------------------------------------

// TestAuthorize_TenantOnly_AdminAtAcme_CanInitOwnBarrier verifies that a
// tenant-local Admin holding tenant_barrier:init may initialize their own
// tenant's barrier.
func TestAuthorize_TenantOnly_AdminAtAcme_CanInitOwnBarrier(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantBarrierInit},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin-role"},
	}

	if err := authz.Authorize(ctxFor("acme"), identity, "tenant_barrier", "init"); err != nil {
		t.Fatalf("Admin@acme should init own barrier, got: %v", err)
	}
}

// TestAuthorize_TenantOnly_AdminAtAcme_CannotInitOtherBarrier verifies the
// cross-tenant denial: a tenant-local Admin cannot init another tenant's
// barrier even though it holds the permission.
func TestAuthorize_TenantOnly_AdminAtAcme_CannotInitOtherBarrier(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantBarrierInit},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin-role"},
	}

	err := authz.Authorize(ctxFor("bob-corp"), identity, "tenant_barrier", "init")
	if err == nil {
		t.Fatal("Admin@acme must NOT init bob-corp's barrier")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
	if !strings.Contains(denied.Reason, "tenant-only") {
		t.Errorf("expected tenant-only denial reason, got %q", denied.Reason)
	}
}

// TestAuthorize_TenantOnly_AdminAtSystem_CannotInitAnyBarrier verifies that
// the system tenant is structurally denied — even on its own tenant id and
// even though the role grants the permission, the platform operator must not
// hold tenant barrier key material.
func TestAuthorize_TenantOnly_AdminAtSystem_CannotInitAnyBarrier(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantBarrierInit},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "platform-admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin-role"},
	}

	for _, target := range []string{"acme", "bob-corp", tenantid.System} {
		err := authz.Authorize(ctxFor(target), identity, "tenant_barrier", "init")
		if err == nil {
			t.Fatalf("Admin@system must NOT init barrier on target %q", target)
		}
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("target %q: expected *PermissionDeniedError, got %T: %v", target, err, err)
		}
		if !strings.Contains(denied.Reason, "system tenant") {
			t.Errorf("target %q: expected system-tenant denial reason, got %q", target, denied.Reason)
		}
	}
}

// operatorBuiltInPerms returns the live Operator built-in role permission set,
// so the data-plane authorize-path tests exercise the real grant rather than a
// hand-copied list.
func operatorBuiltInPerms(t *testing.T) []permission.Permission {
	t.Helper()
	for _, r := range role.BuiltInRoles("acme") {
		if r.Name == role.RoleNameOperator {
			return r.Permissions
		}
	}
	t.Fatal("operator built-in role not found")
	return nil
}

// TestAuthorize_TenantOnly_OperatorDataPlane_AllowDayToDay_DenyAdmin proves the
// end-to-end RBAC decision for the broker data-plane grant: an Operator in its
// own tenant passes a day-to-day verb (objstore:get) but is denied the
// administrative verb (objstore:admin) it does not hold. The perms are
// ScopeTenantOnly, so the operator's tenant must match the request target.
func TestAuthorize_TenantOnly_OperatorDataPlane_AllowDayToDay_DenyAdmin(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"operator-role": operatorBuiltInPerms(t),
	})
	authz := New(svc)

	operator := &auth.Identity{
		Subject:  "acme-operator",
		TenantID: "acme",
		Roles:    []string{"operator-role"},
	}

	// Day-to-day verb is allowed on the operator's own tenant.
	if err := authz.Authorize(ctxFor("acme"), operator, "objstore", "get"); err != nil {
		t.Fatalf("operator should be allowed objstore:get on own tenant, got: %v", err)
	}

	// Administrative verb is denied — operator does not hold objstore:admin.
	err := authz.Authorize(ctxFor("acme"), operator, "objstore", "admin")
	if err == nil {
		t.Fatal("operator must NOT be allowed objstore:admin")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError for objstore:admin, got %T: %v", err, err)
	}

	// dynamic_secrets day-to-day allowed; role management denied.
	if err := authz.Authorize(ctxFor("acme"), operator, "dynamic_secrets", "generate"); err != nil {
		t.Fatalf("operator should be allowed dynamic_secrets:generate, got: %v", err)
	}
	if err := authz.Authorize(ctxFor("acme"), operator, "dynamic_secrets", "role_create"); err == nil {
		t.Fatal("operator must NOT be allowed dynamic_secrets:role_create")
	}
}

// TestAuthorize_TenantOnly_OperatorDataPlane_CrossTenantDenied verifies the
// ScopeTenantOnly confinement still applies to the operator's data-plane grant:
// an operator cannot exercise even a day-to-day verb against another tenant.
func TestAuthorize_TenantOnly_OperatorDataPlane_CrossTenantDenied(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"operator-role": operatorBuiltInPerms(t),
	})
	authz := New(svc)

	operator := &auth.Identity{
		Subject:  "acme-operator",
		TenantID: "acme",
		Roles:    []string{"operator-role"},
	}

	err := authz.Authorize(ctxFor("bob-corp"), operator, "objstore", "get")
	if err == nil {
		t.Fatal("operator@acme must NOT exercise objstore:get on bob-corp")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T: %v", err, err)
	}
	if !strings.Contains(denied.Reason, "tenant-only") {
		t.Errorf("expected tenant-only denial reason, got %q", denied.Reason)
	}
}

// TestAuthorize_TenantOnly_OperatorTaskLifecycle_OwnTenantAllowed proves the
// end-to-end RBAC decision for the two orchestrator task-lifecycle verbs the
// Operator role holds beside task_claim: an Operator in its own tenant may
// readmit a blocked task and block a task, exactly as it may claim one.
func TestAuthorize_TenantOnly_OperatorTaskLifecycle_OwnTenantAllowed(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"operator-role": operatorBuiltInPerms(t),
	})
	authz := New(svc)

	operator := &auth.Identity{
		Subject:  "acme-operator",
		TenantID: "acme",
		Roles:    []string{"operator-role"},
	}

	for _, action := range []string{"task_claim", "task_readmit", "task_block"} {
		if err := authz.Authorize(ctxFor("acme"), operator, "orchestrator", action); err != nil {
			t.Errorf("operator should be allowed orchestrator:%s on own tenant, got: %v", action, err)
		}
	}
}

// TestAuthorize_TenantOnly_OperatorTaskLifecycle_SystemTenantDenied verifies
// that orchestrator:task_readmit and orchestrator:task_block are
// ScopeTenantOnly like every other broker data-plane verb: an identity in the
// system tenant is structurally denied them even when its role grants them,
// and the denial names the tenant-only rule rather than a missing grant.
func TestAuthorize_TenantOnly_OperatorTaskLifecycle_SystemTenantDenied(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"operator-role": operatorBuiltInPerms(t),
	})
	authz := New(svc)

	sysOperator := &auth.Identity{
		Subject:  "sys-operator",
		TenantID: tenantid.System,
		Roles:    []string{"operator-role"},
	}

	for _, action := range []string{"task_readmit", "task_block"} {
		for _, target := range []string{"acme", tenantid.System} {
			err := authz.Authorize(ctxFor(target), sysOperator, "orchestrator", action)
			if err == nil {
				t.Errorf("system tenant must NOT exercise orchestrator:%s on %q", action, target)
				continue
			}
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Errorf("orchestrator:%s on %q: expected *PermissionDeniedError, got %T: %v", action, target, err, err)
				continue
			}
			if !strings.Contains(denied.Reason, "denied to system tenant") {
				t.Errorf("orchestrator:%s on %q: expected system-tenant denial reason, got %q", action, target, denied.Reason)
			}
		}
	}
}

// TestAuthorize_TenantOnly_OperatorTaskLifecycle_CrossTenantDenied verifies
// the ScopeTenantOnly confinement on the two task-lifecycle verbs: an
// Operator at acme cannot readmit or block a task in another tenant.
func TestAuthorize_TenantOnly_OperatorTaskLifecycle_CrossTenantDenied(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"operator-role": operatorBuiltInPerms(t),
	})
	authz := New(svc)

	operator := &auth.Identity{
		Subject:  "acme-operator",
		TenantID: "acme",
		Roles:    []string{"operator-role"},
	}

	for _, action := range []string{"task_readmit", "task_block"} {
		err := authz.Authorize(ctxFor("bob-corp"), operator, "orchestrator", action)
		if err == nil {
			t.Errorf("operator@acme must NOT exercise orchestrator:%s on bob-corp", action)
			continue
		}
		var denied *PermissionDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("orchestrator:%s: expected *PermissionDeniedError, got %T: %v", action, err, err)
			continue
		}
		if !strings.Contains(denied.Reason, "identity not in target tenant") {
			t.Errorf("orchestrator:%s: expected cross-tenant denial reason, got %q", action, denied.Reason)
		}
	}
}

// TestAuthorize_TenantOnly_UnsealAndSeal verifies the seal/unseal
// tenant_barrier permissions carry the same ScopeTenantOnly semantics.
func TestAuthorize_TenantOnly_UnsealAndSeal(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.TenantBarrierUnseal, permission.TenantBarrierSeal},
	})
	authz := New(svc)

	acmeAdmin := &auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin-role"}}
	sysAdmin := &auth.Identity{Subject: "sys-admin", TenantID: tenantid.System, Roles: []string{"admin-role"}}

	// Own-tenant unseal/seal allowed for tenant admin.
	if err := authz.Authorize(ctxFor("acme"), acmeAdmin, "tenant_barrier", "unseal"); err != nil {
		t.Errorf("acme admin unseal own barrier: %v", err)
	}
	if err := authz.Authorize(ctxFor("acme"), acmeAdmin, "tenant_barrier", "seal"); err != nil {
		t.Errorf("acme admin seal own barrier: %v", err)
	}
	// Cross-tenant denied.
	if err := authz.Authorize(ctxFor("bob-corp"), acmeAdmin, "tenant_barrier", "unseal"); err == nil {
		t.Error("acme admin must not unseal bob-corp barrier")
	}
	// System tenant denied.
	if err := authz.Authorize(ctxFor("acme"), sysAdmin, "tenant_barrier", "seal"); err == nil {
		t.Error("system admin must not seal acme barrier")
	}
}

// ---------------------------------------------------------------------------
// Cross-mechanism agreement (FIPS 140-3 AS10.03)
//
// The SO invariant is enforced twice, on purpose: pkg/permission refuses the
// GRANT at registration time, and this package refuses the REQUEST at
// authorization time. The two are independent implementations — this one
// classifies the requested resource:action itself and never consults the duty
// registry — so a defect in either does not disable the other. Independence is
// only worth having if they agree, which is what these tests prove: neither
// mechanism may be narrower than the other, in either direction.
// ---------------------------------------------------------------------------

// TestSODenyList_AgreesWithPermissionDutyRegistry verifies every permission the
// registration-time guard treats as cryptographic use is also denied at
// authorization time. A permission classified in pkg/permission but missing from
// this layer's deny-list would leave an erroneous grant exploitable at runtime.
func TestSODenyList_AgreesWithPermissionDutyRegistry(t *testing.T) {
	var checked int
	for _, p := range permission.All() {
		if !permission.IsCryptoUse(p) {
			continue
		}
		checked++
		resource, action := permission.Resource(p), permission.Action(p)
		if !isSODenied(resource, action) {
			t.Errorf("%q is permission.IsCryptoUse but not in the runtime SO deny-list", p)
		}
	}
	if checked == 0 {
		t.Fatal("precondition failed: no registered permission is classified as cryptographic use")
	}
}

// TestSODenyList_NotBroaderThanDutyRegistry is the converse: every registered
// permission this layer denies to the SO must also be classified as
// cryptographic use by the registration-time guard. A permission denied here but
// grantable there would ship a role whose grants silently do not work.
func TestSODenyList_NotBroaderThanDutyRegistry(t *testing.T) {
	for _, p := range permission.All() {
		if !isSODenied(permission.Resource(p), permission.Action(p)) {
			continue
		}
		if !permission.IsCryptoUse(p) {
			t.Errorf("%q is in the runtime SO deny-list but is not permission.IsCryptoUse", p)
		}
	}
}

// TestSODenyList_CoversEverySOGrantsComplement asserts the two mechanisms do not
// contradict each other on the materialized role: nothing the Security Officer
// actually holds by default may be denied at authorization time, or the SO could
// not perform its own duties.
func TestSODenyList_CoversEverySOGrantsComplement(t *testing.T) {
	soPerms := permission.RolePermissions(permission.RoleSO)
	if len(soPerms) == 0 {
		t.Fatal("precondition failed: the Security Officer role has no grants")
	}
	for _, p := range soPerms {
		if isSODenied(permission.Resource(p), permission.Action(p)) {
			t.Errorf("SO holds %q by default but the runtime deny-list rejects it", p)
		}
	}
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (m *mockRoleService) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

// TestAuthorize_ShareHolderRefusedEverything pins ruling 8: an identity that
// authenticated with a holder certificate (the holder_profile mark the mTLS
// authenticator sets from the profile OU) is refused every RBAC permission,
// whatever roles it carries or a same-named account might carry.
func TestAuthorize_ShareHolderRefusedEverything(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin-role": {permission.KVPut, permission.SealUnseal, permission.SealOfficerRegister},
	})
	authz := New(svc)
	holder := &auth.Identity{
		Subject:  "carol",
		TenantID: "tenant-1",
		Roles:    []string{"admin-role"},
		Metadata: map[string]string{auth.HolderProfileMetadataKey: holderprofile.Custodian},
	}
	for _, rc := range []struct{ resource, action string }{{"kv", "put"}, {"seal", "unseal"}, {"seal", "officer_register"}} {
		if err := authz.Authorize(ctxFor("tenant-1"), holder, rc.resource, rc.action); err == nil {
			t.Errorf("a share holder must be refused %s:%s", rc.resource, rc.action)
		}
	}
	admin := &auth.Identity{Subject: "carol", TenantID: "tenant-1", Roles: []string{"admin-role"}}
	if err := authz.Authorize(ctxFor("tenant-1"), admin, "kv", "put"); err != nil {
		t.Errorf("the same roles on a login identity are honoured: %v", err)
	}
}
