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

package authtest

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// databaseManagementPermissions are the permissions of the Database
// Management service group: every database verb, the barrier and
// share-holder ones included.
func databaseManagementPermissions() []string {
	perms := []permission.Permission{
		permission.DatabasesCreate, permission.DatabasesRead, permission.DatabasesList,
		permission.DatabasesDelete, permission.DatabasesUpdate, permission.DatabasesScale,
		permission.DatabasesSeal, permission.DatabasesUnseal, permission.DatabasesBarrierStatus,
		permission.DatabasesOfficerRegister, permission.DatabasesOfficerList, permission.DatabasesOfficerRevoke,
		permission.DatabasesRekey, permission.DatabasesInitializeShamir,
	}
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

// SystemDatabaseCallers returns system-tenant identities that hold every
// database permission on top of a built-in role -- an Admin, a Security
// Officer and a read-only Auditor -- keyed by role. They pass every database
// route's RBAC check, so what a test of them observes is the service's
// ownership rule: there is no cross-tenant database access, and another
// tenant's database must answer NotFound.
func SystemDatabaseCallers() map[string]*auth.Identity {
	callers := map[string]*auth.Identity{
		"admin":   AdminIdentity(tenantid.System, "root"),
		"so":      SOIdentity(tenantid.System, "so"),
		"auditor": {Subject: "ro", TenantID: tenantid.System, Roles: []string{role.SystemRoleID(tenantid.System, role.RoleNameAuditor)}},
	}
	for _, id := range callers {
		id.ScopePermissions = databaseManagementPermissions()
	}
	return callers
}
