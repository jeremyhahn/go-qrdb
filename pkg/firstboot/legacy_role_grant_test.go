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

package firstboot

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// System role identifiers moved from UUIDv5 (SHA-1) to UUIDv8 (SHA-256) for
// FIPS 140-3. A cluster seeded before that keeps its roles under the legacy
// identifiers — role seeding is name-unique and tolerates already-exists, so
// they are never replaced — and the admin grant must bind to those records
// rather than to a freshly derived identifier that names nothing.
//
// The literals are frozen samples of the legacy derivation, not recomputed:
// SHA-1 panics outright under GODEBUG=fips140=only.
const (
	legacyAdminRoleIDSystem = "f4a582da-2194-50db-8331-59de31072fa6" // system/admin
	legacyOwnerRoleIDSystem = "bf03d0b7-9353-5107-9100-2c5a107f0c9f" // system/owner
)

// legacyRoleResolver answers as a store seeded before the migration would:
// every system role resolves to its legacy identifier.
type legacyRoleResolver struct {
	err error
}

func (l legacyRoleResolver) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if l.err != nil {
		return nil, l.err
	}
	ids := map[string]string{
		role.RoleNameAdmin: legacyAdminRoleIDSystem,
		role.RoleNameOwner: legacyOwnerRoleIDSystem,
	}
	id, ok := ids[name]
	if !ok {
		return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
	}
	return &role.Role{ID: id, Name: name, TenantID: tenantID, System: true}, nil
}

func TestInitialize_GrantsLegacyRoleIdentifiersWhenAlreadySeeded(t *testing.T) {
	users := &fakeUsers{}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants:      &fakeTenants{},
		Users:        users,
		Roles:        &fakeRoles{},
		RoleResolver: legacyRoleResolver{},
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(users.createCalls) == 0 {
		t.Fatal("no user was created")
	}

	got := users.createCalls[0].RoleIDs
	want := []string{legacyAdminRoleIDSystem, legacyOwnerRoleIDSystem}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("admin RoleIDs = %v, want the persisted legacy %v (a derived grant would name no role)", got, want)
	}
}

func TestInitialize_GrantsDerivedIdentifiersWithoutAResolver(t *testing.T) {
	// A store this bootstrap seeds itself has nothing legacy to prefer.
	users := &fakeUsers{}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   users,
		Roles:   &fakeRoles{},
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	got := users.createCalls[0].RoleIDs
	want := []string{
		role.SystemRoleID(tenant.SystemTenantID, role.RoleNameAdmin),
		role.SystemRoleID(tenant.SystemTenantID, role.RoleNameOwner),
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("admin RoleIDs = %v, want %v", got, want)
	}
}

func TestInitialize_FailsWhenRoleIdentifierResolutionFails(t *testing.T) {
	// Binding the admin to the wrong identifier silently strips its
	// permissions, so an unresolvable store must fail the bootstrap.
	sentinel := errors.New("role store unavailable")
	_, err := Initialize(context.Background(), Dependencies{
		Tenants:      &fakeTenants{},
		Users:        &fakeUsers{},
		Roles:        &fakeRoles{},
		RoleResolver: legacyRoleResolver{err: sentinel},
		Logger:       discardLogger(),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
}
