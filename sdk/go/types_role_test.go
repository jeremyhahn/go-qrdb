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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// legacyAdminRoleIDAcme is the identifier the pre-FIPS UUIDv5 (SHA-1)
// derivation produced for tenant "acme", role "admin". Frozen literal, not
// recomputed: SHA-1 panics under GODEBUG=fips140=only.
const legacyAdminRoleIDAcme = "a2dcd47f-c404-52c6-a1c2-b2ee93eb2da6"

// legacyRoleStore answers as a cluster seeded before the migration would.
type legacyRoleStore struct{}

func (legacyRoleStore) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if name != role.RoleNameAdmin {
		return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
	}
	return &role.Role{ID: legacyAdminRoleIDAcme, Name: name, TenantID: tenantID, System: true}, nil
}

func TestSystemRoleID_DerivesUUIDv8(t *testing.T) {
	got := SystemRoleID("acme", role.RoleNameAdmin)

	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("%q is not a parseable UUID: %v", got, err)
	}
	if parsed.Version() != 8 {
		t.Errorf("version = %d, want 8 (RFC 9562 §5.8)", parsed.Version())
	}
	if len(got) != 36 {
		t.Errorf("length = %d, want 36 (shape must be unchanged for consumers)", len(got))
	}
	if got == legacyAdminRoleIDAcme {
		t.Errorf("still deriving the legacy UUIDv5 identifier %s", legacyAdminRoleIDAcme)
	}
}

func TestResolveSystemRoleID_ExposesTheLegacyIdentifierToConsumers(t *testing.T) {
	// The seam a consumer needs so a grant binds to the role that exists on
	// an already-provisioned cluster rather than to a derived identifier.
	got, err := ResolveSystemRoleID(context.Background(), legacyRoleStore{}, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if got != legacyAdminRoleIDAcme {
		t.Errorf("ResolveSystemRoleID = %s, want the persisted %s", got, legacyAdminRoleIDAcme)
	}

	// With no store to consult, the derived identifier is all there is.
	derived, err := ResolveSystemRoleID(context.Background(), nil, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID (nil resolver): %v", err)
	}
	if want := SystemRoleID("acme", role.RoleNameAdmin); derived != want {
		t.Errorf("ResolveSystemRoleID = %s, want %s", derived, want)
	}
}

func TestSystemRoleIDs_ExposesBothFormsToConsumers(t *testing.T) {
	ids, err := SystemRoleIDs(context.Background(), legacyRoleStore{}, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("SystemRoleIDs: %v", err)
	}
	canonical := SystemRoleID("acme", role.RoleNameAdmin)
	if len(ids) != 2 || ids[0] != canonical || ids[1] != legacyAdminRoleIDAcme {
		t.Fatalf("SystemRoleIDs = %v, want [%s %s]", ids, canonical, legacyAdminRoleIDAcme)
	}

	// A RoleNameResolver is what the SDK asks consumers for.
	var _ RoleNameResolver = legacyRoleStore{}
}

// TestIsSystemRoleName_ReExported proves the predicate reaches consumers
// through the SDK and answers for the alias the SDK itself publishes.
//
// The negative case matters as much as the positive: a re-export that always
// said true would be indistinguishable from one that worked, and consumers use
// this to decide whether a name they are about to create is reserved.
func TestIsSystemRoleName_ReExported(t *testing.T) {
	for _, name := range []string{
		RoleNameSO, RoleNameAdmin, RoleNameOperator,
		RoleNameUser, RoleNameAuditor, RoleNameCustodian, RoleNameOwner,
	} {
		if !IsSystemRoleName(name) {
			t.Errorf("IsSystemRoleName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "editor", "viewer", "Owner"} {
		if IsSystemRoleName(name) {
			t.Errorf("IsSystemRoleName(%q) = true, want false", name)
		}
	}
}

// TestSystemRoleImmutableError_ReExported checks the alias matches the error
// the store actually returns, which is the only property a consumer relies on:
// errors.As against the SDK type must catch the pkg/role value.
func TestSystemRoleImmutableError_ReExported(t *testing.T) {
	var err error = &role.SystemRoleImmutableError{ID: "role-id", Op: "deleted"}

	var target *SystemRoleImmutableError
	if !errors.As(err, &target) {
		t.Fatal("errors.As via the SDK alias did not match the pkg/role error")
	}
	if target.ID != "role-id" || target.Op != "deleted" {
		t.Errorf("fields not carried through the alias: %+v", target)
	}
}

// TestRetiredPermissionError_ReExported checks the alias matches the error the
// role package produces, which is the only property a consumer relies on:
// errors.As against the SDK type must catch the pkg/role value.
func TestRetiredPermissionError_ReExported(t *testing.T) {
	stored := &role.Role{
		ID:                 "role-id",
		Name:               role.RoleNameAdmin,
		TenantID:           "acme",
		RetiredPermissions: []permission.Permission{"sdlc:register"},
	}

	var target *RetiredPermissionError
	if !errors.As(stored.RetiredPermissionError(), &target) {
		t.Fatal("errors.As via the SDK alias did not match the pkg/role error")
	}
	if target.RoleID != "role-id" || target.Name != role.RoleNameAdmin || target.TenantID != "acme" {
		t.Errorf("fields not carried through the alias: %+v", target)
	}
	if len(target.Permissions) != 1 || target.Permissions[0] != "sdlc:register" {
		t.Errorf("Permissions = %v, want [sdlc:register]", target.Permissions)
	}
}

// TestRetiredPermissions_ReExported checks the SDK var resolves the registry at
// call time: a registered permission is not drift, an unregistered one is, and
// a whole-segment wildcard never is.
func TestRetiredPermissions_ReExported(t *testing.T) {
	got := RetiredPermissions([]permission.Permission{
		permission.KVGet, "keys:*", "sdlc:register",
	})
	if len(got) != 1 || got[0] != "sdlc:register" {
		t.Errorf("RetiredPermissions = %v, want [sdlc:register]", got)
	}
}

// TestReconcileReport_ReExported checks the report aliases carry their fields,
// so a consumer can read a reconciliation outcome through the SDK names alone.
func TestReconcileReport_ReExported(t *testing.T) {
	var report *ReconcileReport = &role.ReconcileReport{
		TenantID: "acme",
		Roles: []RoleReconcileResult{
			{
				RoleID:  "r-admin",
				Name:    RoleNameAdmin,
				Added:   []permission.Permission{"sdlc:bind"},
				Removed: []permission.Permission{"sdlc:register"},
			},
		},
	}
	if !report.Changed() {
		t.Fatal("populated report reports no change")
	}
	if report.Roles[0].Name != RoleNameAdmin || !report.Roles[0].Changed() {
		t.Errorf("result did not carry through the alias: %+v", report.Roles[0])
	}
	if (&ReconcileReport{TenantID: "acme"}).Changed() {
		t.Error("empty report reports a change")
	}
}

// TestUserIDExistsError_IsTheStoreType pins the alias: a consumer holding a
// UserServicer through this package classifies the atomic create's id
// collision with the SDK's exported name, whatever wrapping the store added.
func TestUserIDExistsError_IsTheStoreType(t *testing.T) {
	var err error = &user.StoreError{Op: "create", Err: &user.UserIDExistsError{ID: 42, TenantID: "t1"}}

	var got *UserIDExistsError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As(%v, *sdk.UserIDExistsError) = false, want true", err)
	}
	if got.ID != 42 || got.TenantID != "t1" {
		t.Errorf("got %+v, want ID 42 in tenant t1", got)
	}
}

// TestUserIDExistsError_DoesNotMatchAUsernameConflict is the negative
// sibling: the two conflicts are distinct types, so a taken username must not
// classify as an id collision through the alias.
func TestUserIDExistsError_DoesNotMatchAUsernameConflict(t *testing.T) {
	var err error = &user.UsernameExistsError{Username: "alice", TenantID: "t1"}
	var got *UserIDExistsError
	if errors.As(err, &got) {
		t.Fatalf("errors.As(%v, *sdk.UserIDExistsError) = true, want false", err)
	}
}
