// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embedded

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newExplicitTenantTransport is the operator-access fixture of
// TestEmbeddedOperatorIdentityAccess_HonoursTheTenantQualifier, without
// the audit trails under test there.
func newExplicitTenantTransport(t *testing.T) *Transport {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithUserService(users),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(64))),
		authmw.WithOperatorAccessTenantTrail(audit.NewRingBuffer(64)),
		authmw.WithIsolationPolicyReader(isolationPolicies{}))
	return newAuthTransport(t, WithUserService(users), WithRoleService(role.NewGrantGuard(roles, mw.Authorizer())),
		WithAuthMiddleware(mw))
}

// TestEmbeddedOperatorIdentityAccess_ExplicitRequestTenant: a request that
// names its tenant only in the request field, with no
// transport.WithTenantQualifier on the context, is decided by operator
// admission as on every network transport: a platform operator reaches
// acme's users and roles, and another tenant's admin is refused.
func TestEmbeddedOperatorIdentityAccess_ExplicitRequestTenant(t *testing.T) {
	tr := newExplicitTenantTransport(t)
	operator := ctxFor(authtest.AdminIdentity(tenantid.System, "operator"))

	u, err := tr.CreateUser(operator, &transport.CreateUserRequest{TenantID: "acme", Username: "ann", WithoutEmailLoginKey: true})
	if err != nil {
		t.Fatalf("operator CreateUser{TenantID: acme} = %v", err)
	}
	if u.TenantID != "acme" {
		t.Fatalf("operator CreateUser{TenantID: acme} made a user in tenant %q, want acme", u.TenantID)
	}
	list, err := tr.ListUsers(operator, &transport.ListUsersRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("operator ListUsers{TenantID: acme} = %v", err)
	}
	if len(list.Users) != 1 || list.Users[0].ID != u.ID {
		t.Fatalf("operator ListUsers{TenantID: acme} = %+v, want only acme's ann", list.Users)
	}
	byName, err := tr.GetUserByUsername(operator, "acme", "ann")
	if err != nil {
		t.Fatalf("operator GetUserByUsername(acme, ann) = %v", err)
	}
	if byName.ID != u.ID {
		t.Fatalf("operator GetUserByUsername(acme, ann) = %d, want %d", byName.ID, u.ID)
	}
	r, err := tr.CreateRole(operator, &transport.CreateRoleRequest{TenantID: "acme", Name: "assigners", Permissions: []string{string(permission.RolesAssign)}})
	if err != nil {
		t.Fatalf("operator CreateRole{TenantID: acme} = %v", err)
	}
	if r.TenantID != "acme" {
		t.Fatalf("operator CreateRole{TenantID: acme} made a role in tenant %q, want acme", r.TenantID)
	}
	if _, err := tr.ListRoles(operator, &transport.ListRolesRequest{TenantID: "acme"}); err != nil {
		t.Fatalf("operator ListRoles{TenantID: acme} = %v", err)
	}

	outsider := ctxFor(authtest.AdminIdentity("globex", "gadmin"))
	if _, err := tr.CreateUser(outsider, &transport.CreateUserRequest{TenantID: "acme", Username: "eve", WithoutEmailLoginKey: true}); err == nil {
		t.Fatal("globex admin CreateUser{TenantID: acme} succeeded, want a refusal")
	}
	if _, err := tr.ListUsers(outsider, &transport.ListUsersRequest{TenantID: "acme"}); err == nil {
		t.Fatal("globex admin ListUsers{TenantID: acme} succeeded, want a refusal")
	}
	if _, err := tr.GetUserByUsername(outsider, "acme", "ann"); err == nil {
		t.Fatal("globex admin GetUserByUsername(acme, ann) succeeded, want a refusal")
	}
	if _, err := tr.CreateRole(outsider, &transport.CreateRoleRequest{TenantID: "acme", Name: "takeover", Permissions: []string{string(permission.RolesAssign)}}); err == nil {
		t.Fatal("globex admin CreateRole{TenantID: acme} succeeded, want a refusal")
	}
	if _, err := tr.ListRoles(outsider, &transport.ListRolesRequest{TenantID: "acme"}); err == nil {
		t.Fatal("globex admin ListRoles{TenantID: acme} succeeded, want a refusal")
	}
}
