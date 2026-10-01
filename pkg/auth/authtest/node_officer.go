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
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// NodeOfficerTenant is the non-system tenant NodeOfficerCallers draws its
// refused callers from.
const NodeOfficerTenant = "acme"

// NodeOfficerCaller is one caller of the node barrier's Security Officer
// registry, and whether the registry must admit it.
type NodeOfficerCaller struct {
	Name     string
	Identity *auth.Identity
	Admitted bool
}

// NodeOfficerCallers returns the callers every transport's node-officer scope
// test drives. The node barrier is a platform singleton, so only the system
// tenant's officer is admitted. A tenant's SO holds seal:officer_register and
// seal:officer_list through its own built-in role, and a tenant Admin can be
// granted them by a custom role of its own tenant; both are refused, because
// enrolling an officer into the node barrier's unseal or recovery domain hands
// that officer a share of the key protecting every tenant.
//
// roles must hold the built-in roles of the system tenant and of
// NodeOfficerTenant: BuiltInRoles(t, tenant.SystemTenantID, NodeOfficerTenant).
func NodeOfficerCallers(t testing.TB, roles role.RoleServicer) []NodeOfficerCaller {
	t.Helper()
	return []NodeOfficerCaller{
		{Name: "the system tenant's SO is served", Identity: SOIdentity(tenant.SystemTenantID, "officer"), Admitted: true},
		{Name: "a tenant's SO is refused", Identity: SOIdentity(NodeOfficerTenant, "acme-officer")},
		{
			Name:     "a tenant Admin granted the officer permissions is refused",
			Identity: GrantedAdminIdentity(t, roles, NodeOfficerTenant, "acme-admin", permission.SealOfficerRegister, permission.SealOfficerList),
		},
	}
}

// CustodianIdentity returns an identity holding the built-in Custodian role of
// its own tenant, by the identifier BuiltInRoles persists it under, and
// nothing else.
func CustodianIdentity(tenantID, subject string) *auth.Identity {
	return &auth.Identity{
		Subject:  subject,
		TenantID: tenantID,
		Roles:    []string{role.SystemRoleID(tenantID, role.RoleNameCustodian)},
	}
}

// GrantedAdminIdentity returns an identity holding the built-in Admin role of
// tenantID and a custom role of tenantID, created in roles, that grants perms:
// a tenant Admin whose own tenant granted it more than the built-in role does.
func GrantedAdminIdentity(t testing.TB, roles role.RoleServicer, tenantID, subject string, perms ...permission.Permission) *auth.Identity {
	t.Helper()
	return Grant(t, roles, AdminIdentity(tenantID, subject), perms...)
}

// Grant creates, in roles, a custom role of id's tenant granting perms, and
// returns id holding it as well as its existing roles: an identity its own
// tenant granted more than its built-in role does.
func Grant(t testing.TB, roles role.RoleServicer, id *auth.Identity, perms ...permission.Permission) *auth.Identity {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), id.TenantID)
	r, err := roles.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        id.Subject + "-grants",
		TenantID:    id.TenantID,
		Permissions: perms,
	})
	if err != nil {
		t.Fatalf("authtest: create the %q grant role in %q: %v", id.Subject, id.TenantID, err)
	}
	granted := *id
	granted.Roles = append(append([]string(nil), id.Roles...), r.ID)
	return &granted
}
