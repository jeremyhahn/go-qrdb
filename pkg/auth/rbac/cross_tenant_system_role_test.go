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
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Legacy (pre-UUIDv8) identifiers for the SYSTEM tenant's roles. Frozen
// samples of the UUIDv5/SHA-1 derivation, like the ones in
// legacy_role_id_test.go: recomputing them would panic under
// GODEBUG=fips140=only, which is the build these tests must pass in.
const (
	legacySOIDSystem    = "0f1c5b3a-2d4e-5a6b-8c7d-9e0f1a2b3c4d"
	legacyOwnerIDSystem = "1a2b3c4d-5e6f-5a7b-8c9d-0e1f2a3b4c5d"
	legacyAdminIDSystem = "2b3c4d5e-6f70-5a8b-9c0d-1e2f3a4b5c6d"
)

// tenantScopedRoleService wraps mockRoleService with kvRoleStore's tenant
// ownership rule, which the plain mock does not model: a lookup performed
// under a non-system context tenant cannot see a role belonging to another
// tenant, and comes back RoleNotFound rather than as an error.
//
// Without this rule the cross-tenant regression below cannot fail, because
// the plain mock ignores the context entirely and would resolve the
// system-tenant role no matter which tenant the request was bound to.
type tenantScopedRoleService struct {
	*mockRoleService
}

func (s tenantScopedRoleService) visible(ctx context.Context, r *role.Role) bool {
	ctxTenant := tenant.FromContext(ctx)
	return tenant.IsSystemTenant(ctxTenant) || r.TenantID == ctxTenant
}

func (s tenantScopedRoleService) GetRole(ctx context.Context, id string) (*role.Role, error) {
	r, err := s.mockRoleService.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.visible(ctx, r) {
		return nil, &role.RoleNotFoundError{ID: id}
	}
	return r, nil
}

func (s tenantScopedRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	r, err := s.mockRoleService.GetRoleByName(ctx, tenantID, name)
	if err != nil {
		return nil, err
	}
	if !s.visible(ctx, r) {
		return nil, &role.RoleNotFoundError{ID: name}
	}
	return r, nil
}

// TestAuthorize_SODenyList_AcrossTenantsWithLegacyIdentifier pins the SO
// deny-list for the one caller that can legitimately act on another tenant:
// a system-tenant identity. The request context is bound to the TARGET
// tenant, so resolving the identity's own system-tenant SO role under that
// context finds nothing — which, read as absence, would leave the legacy
// identifier out of the recognised set and skip FIPS 140-3 AS10.03 entirely.
//
// The identity's roles must be resolved under the identity's tenant.
func TestAuthorize_SODenyList_AcrossTenantsWithLegacyIdentifier(t *testing.T) {
	systemTenant := tenant.SystemTenantID

	svc := tenantScopedRoleService{
		newMockRoleService(map[string][]permission.Permission{
			legacySOIDSystem: {"keys:sign"},
		}).withRole(&role.Role{
			ID: legacySOIDSystem, Name: role.RoleNameSO,
			TenantID: systemTenant, System: true,
		}),
	}

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: systemTenant,
		Roles:    []string{legacySOIDSystem},
	}

	// Same-tenant request: the deny-list already held here before the fix.
	if err := New(svc).Authorize(ctxFor(systemTenant), identity, "keys", "sign"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("same-tenant: expected the SO deny-list to trigger, got %v", err)
	}

	// Cross-tenant request: the context names "acme", not the identity's
	// system tenant. This is the case that failed open.
	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("cross-tenant: SO deny-list was skipped — a legacy SO signed a key against another tenant")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("cross-tenant: expected ErrUnauthorized, got %v", err)
	}
}

// TestEffectiveRoles_OwnerStripped_AcrossTenantsWithLegacyIdentifier is the
// strict-separation half of the same defect: the Owner strip resolves the
// identity's own tenant's Owner role, so it too must not be performed under
// the target tenant's context.
func TestEffectiveRoles_OwnerStripped_AcrossTenantsWithLegacyIdentifier(t *testing.T) {
	systemTenant := tenant.SystemTenantID

	svc := tenantScopedRoleService{
		newMockRoleService(map[string][]permission.Permission{
			legacyOwnerIDSystem: {"kv:put"},
			legacyAdminIDSystem: {"tenants:read"},
		}).withRole(&role.Role{
			ID: legacyOwnerIDSystem, Name: role.RoleNameOwner,
			TenantID: systemTenant, System: true,
		}),
	}

	identity := &auth.Identity{
		Subject:  "admin",
		TenantID: systemTenant,
		Roles:    []string{legacyAdminIDSystem, legacyOwnerIDSystem},
	}

	// Standard posture: the legacy Owner grant is honored cross-tenant.
	if err := New(svc).Authorize(ctxFor("acme"), identity, "kv", "put"); err != nil {
		t.Fatalf("standard posture: expected kv:put allowed via legacy Owner grant, got %v", err)
	}

	// Strict posture: it must be voided even though the request names another
	// tenant.
	err := New(svc, WithStrictSeparation(true)).Authorize(ctxFor("acme"), identity, "kv", "put")
	if err == nil {
		t.Fatal("cross-tenant: legacy Owner grant survived the strict-SoD strip")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("cross-tenant: expected ErrUnauthorized, got %v", err)
	}
}
