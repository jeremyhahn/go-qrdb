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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
// every system role resolves to its legacy identifier, with the SAME
// permission set and System flag the canonical built-in definition carries —
// so registerBuiltInRoles' provenance check (go-qrdb#555) verifies a
// legacy-identified record exactly as it would a freshly seeded one. Only
// Admin and Owner predate the migration this fixture models: SO, Operator,
// User, Auditor, and Custodian are newer roles a legacy store never had, so
// this resolver reports them not-found like a real one would.
type legacyRoleResolver struct {
	err error
}

func (l legacyRoleResolver) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if l.err != nil {
		return nil, l.err
	}
	switch name {
	case role.RoleNameAdmin:
		return &role.Role{
			ID: legacyAdminRoleIDSystem, Name: name, TenantID: tenantID, System: true,
			Permissions: adminBuiltInRole(tenantID).Permissions,
		}, nil
	case role.RoleNameOwner:
		return &role.Role{
			ID: legacyOwnerRoleIDSystem, Name: name, TenantID: tenantID, System: true,
			Permissions: role.OwnerRole(tenantID).Permissions,
		}, nil
	}
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
}

// adminBuiltInRole returns the canonical Admin definition out of
// role.BuiltInRoles, which returns the full built-in set rather than one role
// by name.
func adminBuiltInRole(tenantID string) *role.Role {
	for _, r := range role.BuiltInRoles(tenantID) {
		if r.Name == role.RoleNameAdmin {
			return r
		}
	}
	panic("firstboot test: role.BuiltInRoles no longer includes Admin")
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
		role.SystemRoleID(tenantid.System, role.RoleNameAdmin),
		role.SystemRoleID(tenantid.System, role.RoleNameOwner),
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

// ---------------------------------------------------------------------------
// The resolver seam must not be a silent fail-open
// ---------------------------------------------------------------------------

// legacyRoleCreator is a RoleCreator that ALSO satisfies role.NameResolver,
// exactly as qrdb's own role.RoleServicer does. It reports a role as already
// present ONLY for the names its embedded legacyRoleResolver can actually
// resolve (Admin, Owner) — a real legacy store predating SO/Operator/User/
// Auditor/Custodian would create those fresh, not report them as existing
// under an identifier it has never heard of, and registerBuiltInRoles'
// provenance check (go-qrdb#555) needs a resolver answer it can verify for
// every "already exists" response this double gives.
type legacyRoleCreator struct {
	legacyRoleResolver
	createCalls int
}

func (l *legacyRoleCreator) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	l.createCalls++
	if _, err := l.legacyRoleResolver.GetRoleByName(context.Background(), req.TenantID, req.Name); err == nil {
		return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
	}
	return &role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions, System: role.IsSystemRoleName(req.Name)}, nil
}

func TestInitialize_UsesRolesAsResolverWhenRoleResolverOmitted(t *testing.T) {
	// A caller that wires only Roles — the shape every in-repo call site and
	// any consumer passing a role store has — must still bind grants to the
	// persisted identifiers. Deriving here would hand the admin two role IDs
	// that name nothing, while Initialize reported success.
	users := &fakeUsers{}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   users,
		Roles:   &legacyRoleCreator{},
		Logger:  discardLogger(),
		// RoleResolver deliberately omitted.
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	got := users.createCalls[0].RoleIDs
	want := []string{legacyAdminRoleIDSystem, legacyOwnerRoleIDSystem}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("admin RoleIDs = %v, want the persisted legacy %v", got, want)
	}
}

// roleCreatorOnly can create roles but cannot answer lookups by name — the
// adapter shape the explicit RoleResolver field exists for.
type roleCreatorOnly struct {
	exists bool
}

func (r roleCreatorOnly) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if r.exists {
		return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
	}
	return &role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID}, nil
}

func TestInitialize_RefusesToGuessWhenRolesPreexistAndNothingResolves(t *testing.T) {
	// Roles already there, nothing able to resolve them: the derived
	// identifier is a guess, and on a pre-migration cluster it is the wrong
	// one. Fail loudly instead of creating an admin who holds nothing.
	//
	// It now fails at registerBuiltInRoles itself, EARLIER than it used to:
	// go-qrdb#555's provenance check refuses to treat a name collision as
	// idempotent without a resolver to verify it against, rather than
	// accepting it blindly and discovering the same lack of a resolver only
	// later, while resolving the admin grant's role identifiers. Both are
	// "fail loudly instead of guessing"; this is the earlier, safer point at
	// which nothing can be verified.
	users := &fakeUsers{}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   users,
		Roles:   roleCreatorOnly{exists: true},
		Logger:  discardLogger(),
	})

	var registrationErr *RolesRegistrationError
	if !errors.As(err, &registrationErr) {
		t.Fatalf("err = %v (%T), want *RolesRegistrationError", err, err)
	}
	if len(users.createCalls) != 0 {
		t.Errorf("admin was created with RoleIDs %v despite unresolvable roles", users.createCalls[0].RoleIDs)
	}
}

func TestInitialize_DerivesWhenThisRunSeededTheRoles(t *testing.T) {
	// Same minimal RoleCreator, but the roles did not previously exist — this
	// run wrote them under the derived identifiers, so granting those is
	// exactly right and no resolver is needed.
	users := &fakeUsers{}
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   users,
		Roles:   roleCreatorOnly{exists: false},
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	got := users.createCalls[0].RoleIDs
	want := []string{
		role.SystemRoleID(tenantid.System, role.RoleNameAdmin),
		role.SystemRoleID(tenantid.System, role.RoleNameOwner),
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("admin RoleIDs = %v, want %v", got, want)
	}
}
