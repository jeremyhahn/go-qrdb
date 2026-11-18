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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestAuthorize_RolesReconcileAll_PerPosture pins who holds the all-tenant
// built-in role reconcile (#1129): the system-tenant Admin in the standard
// posture, the system-tenant SO only under strict separation of duties. A
// tenant SO never holds it: the permission is cross-scope.
func TestAuthorize_RolesReconcileAll_PerPosture(t *testing.T) {
	ctx := context.Background()
	sysRoles := authtest.BuiltInRoles(t, tenantid.System)
	sysSO, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}
	sysAdmin, err := sysRoles.GetRoleByName(ctx, tenantid.System, role.RoleNameAdmin)
	if err != nil {
		t.Fatal(err)
	}
	acmeRoles := authtest.BuiltInRoles(t, "acme")
	acmeSO, err := acmeRoles.GetRoleByName(ctx, "acme", role.RoleNameSO)
	if err != nil {
		t.Fatal(err)
	}

	so := &auth.Identity{Subject: "so", TenantID: tenantid.System, Roles: []string{sysSO.ID}}
	admin := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{sysAdmin.ID}}
	tenantSO := &auth.Identity{Subject: "tenant-so", TenantID: "acme", Roles: []string{acmeSO.ID}}

	for _, tc := range []struct {
		name     string
		roles    role.RoleServicer
		identity *auth.Identity
		strict   bool
		allowed  bool
	}{
		{"standard: system Admin", sysRoles, admin, false, true},
		{"standard: system SO", sysRoles, so, false, false},
		{"strict: system SO", sysRoles, so, true, true},
		{"standard: tenant SO", acmeRoles, tenantSO, false, false},
		{"strict: tenant SO", acmeRoles, tenantSO, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := New(tc.roles, WithStrictSeparation(tc.strict)).
				Authorize(ctxFor(tc.identity.TenantID), tc.identity, "roles", "reconcile_all")
			if tc.allowed && err != nil {
				t.Fatalf("roles:reconcile_all denied: %v", err)
			}
			if !tc.allowed && err == nil {
				t.Fatal("roles:reconcile_all allowed, want denied")
			}
		})
	}
}
