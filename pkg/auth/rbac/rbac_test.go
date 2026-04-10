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
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ auth.Authorizer = (*RBACAuthorizer)(nil)

// ---------------------------------------------------------------------------
// Mock RoleServicer
// ---------------------------------------------------------------------------

// mockRoleService is a minimal RoleServicer stub for testing. Only
// ResolvePermissions is exercised; other methods panic if called.
type mockRoleService struct {
	perms     map[string][]permission.Permission // roleID -> permissions
	callCount atomic.Int64
	err       error // if non-nil, ResolvePermissions returns this error
}

func newMockRoleService(perms map[string][]permission.Permission) *mockRoleService {
	return &mockRoleService{perms: perms}
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

func (m *mockRoleService) GetRole(_ context.Context, _ string) (*role.Role, error) {
	panic("not implemented")
}

func (m *mockRoleService) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	panic("not implemented")
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
// Authorize tests
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

func TestAuthorize_SuperadminBypass(t *testing.T) {
	svc := newMockRoleService(nil) // no permissions configured
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "root",
		TenantID: "tenant-1",
		Roles:    []string{"superadmin"},
	}

	err := authz.Authorize(context.Background(), identity, "kv", "delete")
	if err != nil {
		t.Fatalf("expected superadmin to bypass, got error: %v", err)
	}
}

func TestAuthorize_SuperadminBypass_NoRoleResolution(t *testing.T) {
	svc := newMockRoleService(nil)
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "root",
		TenantID: "tenant-1",
		Roles:    []string{"viewer", "superadmin"},
	}

	_ = authz.Authorize(context.Background(), identity, "admin", "shutdown")
	if svc.callCount.Load() != 0 {
		t.Fatal("ResolvePermissions should not be called for superadmin")
	}
}

func TestAuthorize_Allowed(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"writer": {"kv:put", "kv:get"},
	})
	authz := New(svc)

	identity := &auth.Identity{
		Subject:  "user-1",
		TenantID: "tenant-1",
		Roles:    []string{"writer"},
	}

	err := authz.Authorize(context.Background(), identity, "kv", "put")
	if err != nil {
		t.Fatalf("expected authorization to succeed, got error: %v", err)
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

	err := authz.Authorize(context.Background(), identity, "kv", "put")
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
		err := authz.Authorize(context.Background(), identity, "kv", action)
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
	_ = authz.Authorize(context.Background(), identity, "kv", "get")
	// Second call: should hit cache.
	_ = authz.Authorize(context.Background(), identity, "kv", "get")

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
	_ = authz.Authorize(context.Background(), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected 1 call after first authorize, got %d", svc.callCount.Load())
	}

	// Wait for cache to expire.
	time.Sleep(20 * time.Millisecond)

	// Second call: cache expired, should resolve again.
	_ = authz.Authorize(context.Background(), identity, "kv", "get")
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

	err := authz.Authorize(context.Background(), identity, "kv", "get")
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
	err := authz.Authorize(context.Background(), tenant1, "admin", "health")
	if err != nil {
		t.Fatalf("expected tenant-a to have admin:health, got: %v", err)
	}

	// Tenant B: only reader permissions - should not inherit tenant A's cache.
	err = authz.Authorize(context.Background(), tenant2, "admin", "health")
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

	err := authz.Authorize(context.Background(), identity, "kv", "get")
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

	_ = authz.Authorize(context.Background(), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected ResolvePermissions to be called for empty roles, got %d", svc.callCount.Load())
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
	_ = authz.Authorize(context.Background(), identity, "kv", "get")
	if svc.callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", svc.callCount.Load())
	}

	// Invalidate and authorize again.
	authz.Invalidate("tenant-1", "user-7")
	_ = authz.Authorize(context.Background(), identity, "kv", "get")
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

	_ = authz.Authorize(context.Background(), user1, "kv", "get")
	_ = authz.Authorize(context.Background(), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 initial calls, got %d", svc.callCount.Load())
	}

	// Invalidate only user-a.
	authz.Invalidate("t1", "user-a")

	// user-b should still be cached.
	_ = authz.Authorize(context.Background(), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected user-b to be cached (2 calls), got %d", svc.callCount.Load())
	}

	// user-a should resolve again.
	_ = authz.Authorize(context.Background(), user1, "kv", "get")
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

	_ = authz.Authorize(context.Background(), user1, "kv", "get")
	_ = authz.Authorize(context.Background(), user2, "kv", "get")
	if svc.callCount.Load() != 2 {
		t.Fatalf("expected 2 initial calls, got %d", svc.callCount.Load())
	}

	authz.InvalidateAll()

	// Both should resolve again.
	_ = authz.Authorize(context.Background(), user1, "kv", "get")
	_ = authz.Authorize(context.Background(), user2, "kv", "get")
	if svc.callCount.Load() != 4 {
		t.Fatalf("expected 4 total calls after InvalidateAll, got %d", svc.callCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface test
// ---------------------------------------------------------------------------

func TestAuthorize_CompileTimeInterface(t *testing.T) {
	// The var _ auth.Authorizer = (*RBACAuthorizer)(nil) declaration above
	// handles compile-time checking. This test ensures it is exercised
	// at runtime as well.
	var authz auth.Authorizer = New(newMockRoleService(nil))
	err := authz.Authorize(context.Background(), &auth.Identity{
		Subject: "test",
		Roles:   []string{"superadmin"},
	}, "any", "action")
	if err != nil {
		t.Fatalf("expected superadmin pass-through via interface, got: %v", err)
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
	_, ok := c.get("t", "s")
	if ok {
		t.Fatal("expected cache miss for empty cache")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t", "s", []permission.Permission{"kv:get", "kv:put"})

	perms, ok := c.get("t", "s")
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
	c.set("t", "s", []permission.Permission{"kv:get"})

	_, ok := c.get("t", "s")
	if !ok {
		t.Fatal("expected cache hit before expiry")
	}

	time.Sleep(20 * time.Millisecond)

	_, ok = c.get("t", "s")
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestCache_Invalidate(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t", "s1", []permission.Permission{"kv:get"})
	c.set("t", "s2", []permission.Permission{"kv:put"})

	c.invalidate("t", "s1")

	_, ok := c.get("t", "s1")
	if ok {
		t.Fatal("expected s1 to be invalidated")
	}
	_, ok = c.get("t", "s2")
	if !ok {
		t.Fatal("expected s2 to remain cached")
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	c := newPermissionCache(time.Minute)
	c.set("t1", "s1", []permission.Permission{"kv:get"})
	c.set("t2", "s2", []permission.Permission{"kv:put"})

	c.invalidateAll()

	_, ok := c.get("t1", "s1")
	if ok {
		t.Fatal("expected t1:s1 to be cleared")
	}
	_, ok = c.get("t2", "s2")
	if ok {
		t.Fatal("expected t2:s2 to be cleared")
	}
}

func TestCacheKey(t *testing.T) {
	key := cacheKey("tenant-1", "user-a")
	expected := "tenant-1:user-a"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}

func TestCacheKey_EmptyTenant(t *testing.T) {
	key := cacheKey("", "user-a")
	expected := ":user-a"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}
