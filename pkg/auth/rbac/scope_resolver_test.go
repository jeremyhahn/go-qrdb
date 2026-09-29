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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// stubScopeResolver is a tenant-aware scope resolver test double. For each
// (tenantID, permission) pair present in scopes it returns the configured
// scope; otherwise it falls back to the built-in global layer, mirroring
// *permission.TenantScopeStore precedence. The scope is always resolved in
// the IDENTITY's own tenant — exactly as Authorize calls it. A tenantID
// listed in fail returns failErr, mirroring a sealed barrier tenant
// (tenant.ErrTenantSealed).
type stubScopeResolver struct {
	scopes  map[string]map[permission.Permission]permission.Scope
	fail    map[string]bool
	failErr error
}

func newStubScopeResolver() *stubScopeResolver {
	return &stubScopeResolver{
		scopes:  make(map[string]map[permission.Permission]permission.Scope),
		fail:    make(map[string]bool),
		failErr: tenant.ErrTenantSealed,
	}
}

func (s *stubScopeResolver) set(tenantID string, p permission.Permission, scope permission.Scope) {
	if s.scopes[tenantID] == nil {
		s.scopes[tenantID] = make(map[permission.Permission]permission.Scope)
	}
	s.scopes[tenantID][p] = scope
}

func (s *stubScopeResolver) ResolveScope(_ context.Context, tenantID string, p permission.Permission) (permission.Scope, error) {
	if s.fail[tenantID] {
		return permission.ScopeSelf, s.failErr
	}
	if byTenant, ok := s.scopes[tenantID]; ok {
		if scope, ok := byTenant[p]; ok {
			return scope, nil
		}
	}
	return permission.LookupScope(p)
}

var _ permission.ScopeResolver = (*stubScopeResolver)(nil)

// (d) RBAC Authorize uses the tenant-resolved scope. The custom permission
// "billing:export" is declared ScopeSelf in the global layer — every
// permission declares its scope; there is no default (go-qrdb#504) — and
// overridden to ScopeCross in the SYSTEM tenant's store only. A system
// identity holding the grant passes (system-tenant rule for ScopeCross). A
// tenant-local identity in "acme" — which has NO override for the same name —
// resolves the scope in its OWN store, falls through to the global ScopeSelf
// declaration, and is admitted only against its own tenant. Same name,
// different scope per tenant, no collision.
func TestAuthorize_TenantResolvedScope_PerTenantCross(t *testing.T) {
	const perm permission.Permission = "billing:export"
	permission.RegisterScope(permission.ScopeSelf, perm)
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {perm},
	})
	resolver := newStubScopeResolver()
	// Only the system tenant overrides this name to cross-scope.
	resolver.set(tenantid.System, perm, permission.ScopeCross)

	authz := New(svc, WithScopeResolver(resolver))

	// A system identity with the cross-scope override passes for any target.
	sysIdentity := &auth.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor("acme"), sysIdentity, "billing", "export"); err != nil {
		t.Fatalf("system identity with cross override should pass: %v", err)
	}

	// A tenant-local identity in acme has NO override -> resolves in its own
	// store -> falls through to the global ScopeSelf declaration. It is
	// admitted against its OWN tenant...
	acmeIdentity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor("acme"), acmeIdentity, "billing", "export"); err != nil {
		t.Fatalf("acme identity (global ScopeSelf) should pass for own tenant: %v", err)
	}

	// ...but NOT against another tenant (ScopeSelf, not the system tenant's
	// cross scope — proving no collision on the shared name).
	err := authz.Authorize(ctxFor("other"), acmeIdentity, "billing", "export")
	if err == nil {
		t.Fatal("acme identity must NOT cross into another tenant on a self-scope declaration")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %v", err)
	}
}

// (d, cont.) A custom permission set to ScopeTenantOnly in an identity's own
// tenant store denies that identity even when it is the SYSTEM tenant — the
// genuine tenant-only-denies-system property, resolved from the per-tenant
// store rather than a global table. The same name carries a different scope
// (the global ScopeSelf declaration) for a tenant with no override.
func TestAuthorize_TenantResolvedScope_PerTenantTenantOnly(t *testing.T) {
	const perm permission.Permission = "billing:export"
	permission.RegisterScope(permission.ScopeSelf, perm)
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {perm},
	})
	resolver := newStubScopeResolver()
	// The system tenant's own store marks the name tenant-only.
	resolver.set(tenantid.System, perm, permission.ScopeTenantOnly)

	authz := New(svc, WithScopeResolver(resolver))

	// The system identity resolves the scope in the SYSTEM store -> tenant-only
	// -> denied (the system-tenant rule does NOT lift ScopeTenantOnly).
	sysIdentity := &auth.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor(tenantid.System), sysIdentity, "billing", "export"); err == nil {
		t.Fatal("system identity must be denied a tenant-only permission resolved in its own store")
	}

	// The acme identity has NO override -> falls through to the global
	// ScopeSelf declaration -> admitted against its own tenant. Same name,
	// different effective scope.
	acmeIdentity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor("acme"), acmeIdentity, "billing", "export"); err != nil {
		t.Fatalf("acme admin should pass global self-scope declaration for own tenant: %v", err)
	}
}

// (e) A scope-resolution error (sealed barrier tenant) fails safe: Authorize
// denies rather than admitting or leaking.
func TestAuthorize_ScopeResolverErrorFailsSafe(t *testing.T) {
	const perm permission.Permission = "billing:export"
	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {perm},
	})
	resolver := newStubScopeResolver()
	resolver.fail["acme"] = true

	authz := New(svc, WithScopeResolver(resolver))

	identity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}
	err := authz.Authorize(ctxFor("acme"), identity, "billing", "export")
	if err == nil {
		t.Fatal("a sealed-tenant scope-resolution error must fail safe (deny)")
	}
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %v", err)
	}
}

// Without a resolver installed the authorizer uses the built-in global scope
// layer (back-compat): a globally cross-scoped permission still requires the
// system tenant.
func TestAuthorize_NoResolverUsesGlobalLayer(t *testing.T) {
	const perm permission.Permission = "platform:audit"
	permission.RegisterScope(permission.ScopeCross, perm)

	svc := newMockRoleService(map[string][]permission.Permission{
		"admin": {perm},
	})
	authz := New(svc) // no WithScopeResolver

	sysIdentity := &auth.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor("acme"), sysIdentity, "platform", "audit"); err != nil {
		t.Fatalf("system identity should pass global cross-scope perm: %v", err)
	}

	tenantIdentity := &auth.Identity{
		Subject:  "acme-admin",
		TenantID: "acme",
		Roles:    []string{"admin"},
	}
	if err := authz.Authorize(ctxFor("acme"), tenantIdentity, "platform", "audit"); err == nil {
		t.Fatal("tenant-local identity must be denied a global cross-scope perm")
	}
}
