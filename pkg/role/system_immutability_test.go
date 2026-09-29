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

package role

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The tests in this file cover go-qrdb#17: every seeded system role was
// persisted with System: false, so the immutability guards in DeleteRole and
// UpdateRole never fired for a single built-in.
//
// They all seed THROUGH CreateRoleRequest, the way pkg/bootstrap's
// seedTenantRoles does, and never construct a Role literal with System: true.
// That is load-bearing. The defect was precisely that the flag survives on the
// value BuiltInRoles returns and is lost on the way to disk, so a test that
// sets the flag itself asserts the one thing that was never in question — and
// TestDeleteRole_SystemRole, which did exactly that, passed throughout the
// entire lifetime of the bug.

// seedBuiltIns creates the tenant's built-in roles the way pkg/bootstrap does:
// through CreateRoleRequest, carrying only the fields that request type has.
func seedBuiltIns(t *testing.T, svc RoleServicer, tenantID string) []*Role {
	t.Helper()
	ctx := context.Background()
	seeded := make([]*Role, 0, len(BuiltInRoles(tenantID))+1)
	for _, r := range append(BuiltInRoles(tenantID), OwnerRole(tenantID)) {
		created, err := svc.CreateRole(ctx, &CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		if err != nil {
			t.Fatalf("seed %q: %v", r.Name, err)
		}
		seeded = append(seeded, created)
	}
	return seeded
}

// TestSeededSystemRoles_PersistSystemTrue is the root assertion: the flag must
// be true of the record ON DISK, not merely of the value BuiltInRoles returns.
//
// It re-reads through GetRole rather than trusting CreateRole's return value,
// because a derivation applied only to the returned struct and not to the
// marshalled record would reproduce the defect exactly.
func TestSeededSystemRoles_PersistSystemTrue(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	for _, seeded := range seedBuiltIns(t, svc, "acme") {
		got, err := svc.GetRole(ctx, seeded.ID)
		if err != nil {
			t.Fatalf("GetRole(%q): %v", seeded.Name, err)
		}
		if !got.System {
			t.Errorf("role %q: persisted System=false; a seeded built-in must be a system role", got.Name)
		}
	}
}

// TestSeededSystemRoles_CannotBeDeleted is decision 2: DeleteRole's guard has
// existed since the package was written and never fired for a real built-in.
func TestSeededSystemRoles_CannotBeDeleted(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	for _, seeded := range seedBuiltIns(t, svc, "acme") {
		err := svc.DeleteRole(ctx, seeded.ID)
		if err == nil {
			t.Errorf("role %q: DeleteRole succeeded; a system role must be undeletable", seeded.Name)
			continue
		}
		var immutable *SystemRoleImmutableError
		if !errors.As(err, &immutable) {
			t.Errorf("role %q: DeleteRole error = %T (%v), want *SystemRoleImmutableError",
				seeded.Name, err, err)
			continue
		}

		// The refusal must not have been a partial delete that failed
		// somewhere else: the role is still readable afterwards.
		if _, gerr := svc.GetRole(ctx, seeded.ID); gerr != nil {
			t.Errorf("role %q: refused delete did not leave the role intact: %v", seeded.Name, gerr)
		}
	}
}

// TestSeededSystemRoles_CannotBeRenamed is decision 3: UpdateRole's doc
// comment promised this guard and the code never carried it.
func TestSeededSystemRoles_CannotBeRenamed(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	for _, seeded := range seedBuiltIns(t, svc, "acme") {
		_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
			ID:   seeded.ID,
			Name: seeded.Name + "-renamed",
		})
		if err == nil {
			t.Errorf("role %q: UpdateRole rename succeeded; a system role must be unrenameable", seeded.Name)
			continue
		}
		var immutable *SystemRoleImmutableError
		if !errors.As(err, &immutable) {
			t.Errorf("role %q: rename error = %T (%v), want *SystemRoleImmutableError",
				seeded.Name, err, err)
			continue
		}

		// The name must still resolve — a refused rename that had already
		// deleted the old name index entry would leave the role unreachable
		// by the name the RBAC authorizer resolves it through.
		got, gerr := svc.GetRoleByName(ctx, "acme", seeded.Name)
		if gerr != nil {
			t.Errorf("role %q: refused rename broke name resolution: %v", seeded.Name, gerr)
			continue
		}
		if got.ID != seeded.ID {
			t.Errorf("role %q: name now resolves to %q, want %q", seeded.Name, got.ID, seeded.ID)
		}
	}
}

// TestSystemRole_PermissionsAreFixed enforces the other half of the contract
// docs/firstboot/user.md states to operators: "they are undeletable and their
// permissions are fixed. Create additional roles instead."
//
// An earlier revision of this file asserted the OPPOSITE — that permissions
// stayed updatable — on the reasoning that the immutability being added was of
// the role's identity. Review was right that this locks in a policy hole:
// rewriting the custodian role's single barrier:unseal-share grant, or
// widening Auditor past read-only, changes the compliance posture while the
// record still presents as a canonical system role.
func TestSystemRole_PermissionsAreFixed(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	for _, seeded := range seedBuiltIns(t, svc, "acme") {
		before, err := svc.GetRole(ctx, seeded.ID)
		if err != nil {
			t.Fatalf("GetRole(%q): %v", seeded.Name, err)
		}

		_, err = svc.UpdateRole(ctx, &UpdateRoleRequest{
			ID:          seeded.ID,
			Permissions: []permission.Permission{permission.KVGet},
		})
		if err == nil {
			t.Errorf("role %q: permission update succeeded; a system role's grants are fixed", seeded.Name)
			continue
		}
		var immutable *SystemRoleImmutableError
		if !errors.As(err, &immutable) {
			t.Errorf("role %q: error = %T (%v), want *SystemRoleImmutableError", seeded.Name, err, err)
			continue
		}

		// The refusal must leave the PERSISTED grants untouched, not merely
		// return an error after writing.
		after, err := svc.GetRole(ctx, seeded.ID)
		if err != nil {
			t.Fatalf("GetRole(%q) after refusal: %v", seeded.Name, err)
		}
		if !permissionSetsEqual(before.Permissions, after.Permissions) {
			t.Errorf("role %q: refused permission update still changed the stored grants:\n before %v\n after  %v",
				seeded.Name, before.Permissions, after.Permissions)
		}
	}
}

// TestOrdinaryRole_PermissionsRemainUpdatable is the control: the guard above
// must key on System, not disable permission updates for everyone.
func TestOrdinaryRole_PermissionsRemainUpdatable(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")
	updated, err := svc.UpdateRole(ctx, &UpdateRoleRequest{
		ID:          r.ID,
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("UpdateRole(permissions) on an ordinary role: %v", err)
	}
	if len(updated.Permissions) != 1 || updated.Permissions[0] != permission.KVGet {
		t.Errorf("Permissions: got %v, want [%s]", updated.Permissions, permission.KVGet)
	}
}

// TestOrdinaryRole_IsNotSystem is the negative control for the derivation, and
// the mutant catcher for "System is hardcoded true".
//
// It also stands in for the API-settable case: CreateRoleRequest has no System
// field to set, so the strongest available statement is that no input a caller
// can provide produces a system role except the reserved name itself.
func TestOrdinaryRole_IsNotSystem(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "acme")
	if r.System {
		t.Fatal("an ordinary role must not be a system role")
	}

	got, err := svc.GetRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if got.System {
		t.Error("an ordinary role was persisted with System=true")
	}

	// And it really is mutable, which is what System=false has to mean.
	if _, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: r.ID, Name: "editor2"}); err != nil {
		t.Errorf("rename of an ordinary role: %v", err)
	}
	if err := svc.DeleteRole(ctx, r.ID); err != nil {
		t.Errorf("delete of an ordinary role: %v", err)
	}
}

// TestReservedName_CannotBeTakenByRenaming closes the other half of the
// invariant. Without it a role could carry a canonical name while its stored
// System flag stayed false — deletable by DeleteRole, and reserved as far as
// IsSystemRoleName is concerned.
//
// The tenant deliberately has NO built-ins seeded: with them present, name
// uniqueness alone would refuse the rename and the test would pass without the
// guard under test ever running.
func TestReservedName_CannotBeTakenByRenaming(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	r := createTestRole(t, svc, "editor", "unseeded-tenant")

	_, err := svc.UpdateRole(ctx, &UpdateRoleRequest{ID: r.ID, Name: RoleNameAdmin})
	if err == nil {
		t.Fatal("rename into a reserved system role name succeeded")
	}
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T (%v), want *ValidationError", err, err)
	}

	got, gerr := svc.GetRole(ctx, r.ID)
	if gerr != nil {
		t.Fatalf("GetRole after refused rename: %v", gerr)
	}
	if got.Name != "editor" {
		t.Errorf("Name after refused rename: got %q, want %q", got.Name, "editor")
	}
}

// TestReservedName_RefusedOnTheOrdinaryCreatePath is the fix for the trap that
// name-derived immutability opened on its own.
//
// Deriving System from the name made a squatted built-in UNDELETABLE: a caller
// holding roles:create could persist "so" with a random id and one unrelated
// permission, occupying the unique name bootstrap needs and the authorizer
// resolves through, with no way to remove it. That is a worse failure mode
// than the mutable roles being fixed.
func TestReservedName_RefusedOnTheOrdinaryCreatePath(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	_, err := svc.CreateRole(ctx, &CreateRoleRequest{
		Name:        RoleNameSO,
		TenantID:    "acme",
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err == nil {
		t.Fatal("an arbitrary role was created under a reserved name")
	}
	var reserved *ReservedRoleNameError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %T (%v), want *ReservedRoleNameError", err, err)
	}

	// Nothing may have been persisted: the name must still be free for the
	// canonical seed that comes later.
	if _, gerr := svc.GetRoleByName(ctx, "acme", RoleNameSO); gerr == nil {
		t.Error("the refused create left a record behind, squatting the name")
	}
}

// TestReservedName_RefusedOnEachHalfOfTheIdentity checks the id and the
// permission set separately, so a guard that enforced only one would be
// caught. A canonical-id role carrying arbitrary grants is still a malformed
// built-in.
func TestReservedName_RefusedOnEachHalfOfTheIdentity(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	canonical := canonicalSystemRole("acme", RoleNameAuditor)
	if canonical == nil {
		t.Fatal("no canonical role for auditor")
	}

	tests := []struct {
		name string
		req  *CreateRoleRequest
	}{
		{
			name: "canonical permissions, wrong id",
			req: &CreateRoleRequest{
				ID: "not-the-canonical-id", Name: canonical.Name,
				TenantID: "acme", Permissions: canonical.Permissions,
			},
		},
		{
			name: "canonical id, wrong permissions",
			req: &CreateRoleRequest{
				ID: canonical.ID, Name: canonical.Name,
				TenantID: "acme", Permissions: []permission.Permission{permission.KVGet},
			},
		},
		{
			name: "canonical id, no permissions at all",
			req: &CreateRoleRequest{
				ID: canonical.ID, Name: canonical.Name, TenantID: "acme",
			},
		},
		{
			name: "no id supplied, so one would be generated",
			req: &CreateRoleRequest{
				Name: canonical.Name, TenantID: "acme", Permissions: canonical.Permissions,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateRole(ctx, tt.req)
			var reserved *ReservedRoleNameError
			if !errors.As(err, &reserved) {
				t.Fatalf("error = %T (%v), want *ReservedRoleNameError", err, err)
			}
		})
	}
}

// TestCanonicalSystemRole_IsAccepted is the positive half: the strictness must
// not break the seeding paths it exists to protect. pkg/bootstrap and
// pkg/firstboot both pass exactly the constructor's id and permissions.
func TestCanonicalSystemRole_IsAccepted(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()

	canonical := canonicalSystemRole("acme", RoleNameCustodian)
	r, err := svc.CreateRole(ctx, &CreateRoleRequest{
		ID:          canonical.ID,
		Name:        canonical.Name,
		TenantID:    canonical.TenantID,
		Permissions: canonical.Permissions,
	})
	if err != nil {
		t.Fatalf("the canonical seed was refused: %v", err)
	}
	if !r.System {
		t.Error("the canonical seed is not a system role")
	}
	if r.ID != canonical.ID {
		t.Errorf("ID: got %q, want the canonical %q", r.ID, canonical.ID)
	}
}

// TestIsSystemRoleName_DerivedFromConstructors checks the predicate against the
// constructors it is derived from rather than against a second hand-written
// list — a duplicated list is the failure mode this derivation exists to
// prevent, and repeating it in the test would reintroduce it.
func TestIsSystemRoleName_DerivedFromConstructors(t *testing.T) {
	for _, r := range append(BuiltInRoles(tenantid.System), OwnerRole(tenantid.System)) {
		if !IsSystemRoleName(r.Name) {
			t.Errorf("IsSystemRoleName(%q) = false, want true", r.Name)
		}
	}

	for _, name := range []string{"", "editor", "viewer", "Admin", "admin ", "so2"} {
		if IsSystemRoleName(name) {
			t.Errorf("IsSystemRoleName(%q) = true, want false", name)
		}
	}
}

// TestIsSystemRoleName_TenantIndependent pins that the reservation is global.
// A per-tenant answer would leave every non-system tenant's built-ins mutable.
func TestIsSystemRoleName_TenantIndependent(t *testing.T) {
	for _, r := range BuiltInRoles("globex") {
		if !IsSystemRoleName(r.Name) {
			t.Errorf("IsSystemRoleName(%q) = false for a non-system tenant's built-in", r.Name)
		}
	}
}
