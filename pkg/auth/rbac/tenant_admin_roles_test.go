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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The Admin role a new tenant is seeded with authorizes roles:create and
// roles:update in that tenant, through the authorizer and the stored role,
// not only in the grant table (#1276): a data-plane principal enrolled as a
// tenant's admin creates and updates that tenant's custom roles.
func TestAuthorize_NewTenantAdminCreatesAndUpdatesRolesInItsTenant(t *testing.T) {
	for _, perm := range []permission.Permission{permission.RolesCreate, permission.RolesUpdate} {
		t.Run(string(perm), func(t *testing.T) {
			if err := authorizeAgainst(t, []string{"acme", "globex"}, "acme", "acme", perm); err != nil {
				t.Fatalf("acme admin %s in acme = %v, want allowed", perm, err)
			}
		})
	}
}

// The same Admin is denied the role writes in another tenant and in the
// system tenant, and is never granted roles:delete, which stays with the
// Security Officer.
func TestAuthorize_NewTenantAdminRoleWritesConfinedToItsTenant(t *testing.T) {
	cases := []struct {
		name   string
		target string
		perm   permission.Permission
	}{
		{"create in another tenant", "globex", permission.RolesCreate},
		{"update in another tenant", "globex", permission.RolesUpdate},
		{"create in the system tenant", tenantid.System, permission.RolesCreate},
		{"update in the system tenant", tenantid.System, permission.RolesUpdate},
		{"delete in its own tenant", "acme", permission.RolesDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeAgainst(t, []string{tenantid.System, "acme", "globex"}, "acme", tc.target, tc.perm)
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("acme admin %s on %s = %v, want *PermissionDeniedError", tc.perm, tc.target, err)
			}
		})
	}
}
