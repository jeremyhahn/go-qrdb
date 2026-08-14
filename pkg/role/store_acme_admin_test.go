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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// permSetForRole returns the permission set of the named built-in role for the
// given tenant, failing the test when the role is absent.
func permSetForRole(t *testing.T, tenantID, name string) map[permission.Permission]struct{} {
	t.Helper()
	r := roleByName(BuiltInRoles(tenantID), name)
	if r == nil {
		t.Fatalf("built-in role %q not found for tenant %q", name, tenantID)
	}
	set := make(map[permission.Permission]struct{}, len(r.Permissions))
	for _, p := range r.Permissions {
		set[p] = struct{}{}
	}
	return set
}

// acmeAdminReadOnly is the ACME-administration / CA-hierarchy read surface —
// the slice every role above User is expected to hold.
var acmeAdminReadOnly = []permission.Permission{
	permission.ACMEAccountsRead, permission.ACMEAccountsList,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
	permission.CARead, permission.CAList,
}

// acmeAdminMutating is the ACME-administration / CA-hierarchy write surface —
// the slice reserved for Admin and SO.
var acmeAdminMutating = []permission.Permission{
	permission.ACMEAccountsDisable,
	permission.ACMEEABCreate, permission.ACMEEABDelete,
	permission.CACreate, permission.CAUpdate,
}

// TestBuiltInRoles_AdminReachesACMEAdminSurface is the regression test for the
// unification bug: before the ACME admin surface authorized through qrdb, a
// user granted the canonical "admin" role held no ACME administration rights
// at all. Admin must now carry the entire family.
func TestBuiltInRoles_AdminReachesACMEAdminSurface(t *testing.T) {
	set := permSetForRole(t, "acme-tenant", RoleNameAdmin)
	for _, p := range append(append([]permission.Permission{}, acmeAdminReadOnly...), acmeAdminMutating...) {
		if _, ok := set[p]; !ok {
			t.Errorf("admin missing ACME administration grant %q", p)
		}
	}
}

// TestBuiltInRoles_SOReachesACMEAdminSurface verifies the Security Officer —
// the role that governs CA operations — carries the full family too.
func TestBuiltInRoles_SOReachesACMEAdminSurface(t *testing.T) {
	set := permSetForRole(t, "acme-tenant", RoleNameSO)
	for _, p := range append(append([]permission.Permission{}, acmeAdminReadOnly...), acmeAdminMutating...) {
		if _, ok := set[p]; !ok {
			t.Errorf("so missing ACME administration grant %q", p)
		}
	}
}

// TestBuiltInRoles_OperatorACMEAdminIsReadOnly verifies Operator holds the read
// surface and none of the mutating grants — minting an External Account
// Binding or creating a CA stays with Admin / SO.
func TestBuiltInRoles_OperatorACMEAdminIsReadOnly(t *testing.T) {
	set := permSetForRole(t, "acme-tenant", RoleNameOperator)
	for _, p := range acmeAdminReadOnly {
		if _, ok := set[p]; !ok {
			t.Errorf("operator missing ACME administration read grant %q", p)
		}
	}
	for _, p := range acmeAdminMutating {
		if _, ok := set[p]; ok {
			t.Errorf("operator must not hold mutating ACME administration grant %q", p)
		}
	}
}

// TestBuiltInRoles_AuditorACMEAdminIsReadOnly verifies Auditor sees the
// registration and hierarchy metadata but can change nothing (FIPS 140-3
// AS10.10).
func TestBuiltInRoles_AuditorACMEAdminIsReadOnly(t *testing.T) {
	set := permSetForRole(t, "acme-tenant", RoleNameAuditor)
	for _, p := range acmeAdminReadOnly {
		if _, ok := set[p]; !ok {
			t.Errorf("auditor missing ACME administration read grant %q", p)
		}
	}
	for _, p := range acmeAdminMutating {
		if _, ok := set[p]; ok {
			t.Errorf("auditor must not hold mutating ACME administration grant %q", p)
		}
	}
}

// TestBuiltInRoles_UserAndCustodianHaveNoACMEAdmin verifies the two least
// privileged canonical roles reach none of the ACME administration surface.
func TestBuiltInRoles_UserAndCustodianHaveNoACMEAdmin(t *testing.T) {
	all := append(append([]permission.Permission{}, acmeAdminReadOnly...), acmeAdminMutating...)
	for _, name := range []string{RoleNameUser, RoleNameCustodian} {
		set := permSetForRole(t, "acme-tenant", name)
		for _, p := range all {
			if _, ok := set[p]; ok {
				t.Errorf("%s must not hold ACME administration grant %q", name, p)
			}
		}
	}
}

// TestBuiltInRoles_ACMEAdminGrantsValidate verifies every ACME-administration
// grant on the built-in roles passes validatePermissions — the same gate
// CreateRole and UpdateRole apply — so the seeded roles are legal.
func TestBuiltInRoles_ACMEAdminGrantsValidate(t *testing.T) {
	all := append(append([]permission.Permission{}, acmeAdminReadOnly...), acmeAdminMutating...)
	if err := validatePermissions(all); err != nil {
		t.Fatalf("validatePermissions: unexpected error %v", err)
	}
}

// TestBuiltInRoles_ACMEAdminRejectsUnregistered is the error-path counterpart:
// a plausible-looking but unregistered near-duplicate of the new vocabulary is
// rejected, so a role can never be seeded with a permission that resolves to
// nothing.
func TestBuiltInRoles_ACMEAdminRejectsUnregistered(t *testing.T) {
	for _, bogus := range []permission.Permission{
		"acme_account:read", // singular resource
		"acme_eab:endorse",  // unregistered action
		"ca:delete",         // unregistered action
		"tenant-ca:create",  // the deleted xkms-local vocabulary
		"certificates:read", // the deleted xkms-local vocabulary
	} {
		if err := validatePermissions([]permission.Permission{bogus}); err == nil {
			t.Errorf("validatePermissions(%q): expected error, got nil", bogus)
		}
	}
}
