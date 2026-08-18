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

package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// legacyAdminRoleIDAcme is the identifier the pre-FIPS UUIDv5 (SHA-1)
// derivation produced for tenant "acme", role "admin". Frozen literal, not
// recomputed: SHA-1 panics under GODEBUG=fips140=only.
const legacyAdminRoleIDAcme = "a2dcd47f-c404-52c6-a1c2-b2ee93eb2da6"

// legacyRoleResolver answers as a tenant seeded before the migration would.
type legacyRoleResolver struct {
	err error
}

func (l legacyRoleResolver) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if l.err != nil {
		return nil, l.err
	}
	if name != role.RoleNameAdmin {
		return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
	}
	return &role.Role{ID: legacyAdminRoleIDAcme, Name: name, TenantID: tenantID, System: true}, nil
}

func TestProvisionTenant_GrantsLegacyAdminRoleIdentifier(t *testing.T) {
	// Re-provisioning a tenant seeded before the migration must bind the new
	// tenant admin to the Admin role that exists — the legacy one — not to a
	// freshly derived identifier that names no record.
	tenants, users := &fakeTenants{}, &fakeUsers{}
	roles := &fakeRoles{existsByName: map[string]bool{role.RoleNameAdmin: true}}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme",
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		RoleResolver: legacyRoleResolver{},
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}

	got := users.createCalls[0].RoleIDs
	if len(got) != 1 || got[0] != legacyAdminRoleIDAcme {
		t.Errorf("admin RoleIDs = %v, want [%s]", got, legacyAdminRoleIDAcme)
	}
}

func TestProvisionTenant_GrantsDerivedAdminRoleIdentifierWithoutAResolver(t *testing.T) {
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme",
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}

	got := users.createCalls[0].RoleIDs
	want := role.SystemRoleID("acme", role.RoleNameAdmin)
	if len(got) != 1 || got[0] != want {
		t.Errorf("admin RoleIDs = %v, want [%s]", got, want)
	}
}

func TestProvisionTenant_FailsWhenAdminRoleIdentifierResolutionFails(t *testing.T) {
	// Binding the admin to the wrong identifier silently strips its
	// permissions, so an unresolvable role store must fail provisioning.
	sentinel := errors.New("role store unavailable")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID: "acme",
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		RoleResolver: legacyRoleResolver{err: sentinel},
		Logger:       discardLogger(),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
	var adminErr *TenantAdminUserCreationError
	if !errors.As(err, &adminErr) {
		t.Fatalf("err = %T, want *TenantAdminUserCreationError", err)
	}
}
