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
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// isolationPolicies is the IsolationPolicyReader of the operator-access
// tests: acme allows operator access under the cluster floor, globex is
// strict.
type isolationPolicies struct{}

func (isolationPolicies) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	switch id {
	case "acme":
		return &tenant.TenantInfo{ID: id, IsolationType: tenant.IsolationRaft, IsolationPolicy: tenant.TenantIsolationPolicyInherit}, nil
	case "globex":
		return &tenant.TenantInfo{ID: id, IsolationType: tenant.IsolationRaft, IsolationPolicy: tenant.TenantIsolationPolicyStrict}, nil
	}
	return nil, tenant.ErrTenantNotFound
}

func (isolationPolicies) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

// TestEmbeddedOperatorIdentityAccess_HonoursTheTenantQualifier: through the
// embedded transport, over the real user and role services, a platform
// operator's tenant-qualified request (transport.WithTenantQualifier)
// reaches the qualifier's tenant exactly as on every network transport --
// admitted by AuthorizeOperatorIdentityAccess under the tenant's
// operator-access policy and recorded in both trails -- and a strict tenant
// refuses it. Without a qualifier the operator works in its own tenant.
func TestEmbeddedOperatorIdentityAccess_HonoursTheTenantQualifier(t *testing.T) {
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	platform, tenantTrail := audit.NewRingBuffer(64), audit.NewRingBuffer(64)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithUserService(users),
		authmw.WithAuditLogger(audit.NewDecisionLogger(platform)), authmw.WithOperatorAccessTenantTrail(tenantTrail),
		authmw.WithIsolationPolicyReader(isolationPolicies{}))
	tr := newAuthTransport(t, WithUserService(users), WithRoleService(role.NewGrantGuard(roles, mw.Authorizer())),
		WithAuthMiddleware(mw))

	operator := ctxFor(authtest.AdminIdentity(tenantid.System, "operator"))
	acme := transport.WithTenantQualifier(operator, "acme")

	u, err := tr.CreateUser(acme, &transport.CreateUserRequest{Username: "ann", WithoutEmailLoginKey: true})
	if err != nil {
		t.Fatalf("qualified CreateUser = %v", err)
	}
	if u.TenantID != "acme" {
		t.Fatalf("qualified CreateUser made a user in tenant %q, want acme", u.TenantID)
	}
	r, err := tr.CreateRole(acme, &transport.CreateRoleRequest{Name: "assigners", Permissions: []string{string(permission.RolesAssign)}})
	if err != nil {
		t.Fatalf("qualified CreateRole = %v", err)
	}
	if r.TenantID != "acme" {
		t.Fatalf("qualified CreateRole made a role in tenant %q, want acme", r.TenantID)
	}
	if err := tr.AssignRole(acme, u.ID, r.ID); err != nil {
		t.Fatalf("qualified AssignRole = %v", err)
	}
	got, err := tr.GetUser(acme, u.ID)
	if err != nil {
		t.Fatalf("qualified GetUser = %v", err)
	}
	if !slices.Contains(got.RoleIDs, r.ID) {
		t.Fatalf("acme user roles = %v, want %s assigned", got.RoleIDs, r.ID)
	}
	list, err := tr.ListUsers(acme, &transport.ListUsersRequest{})
	if err != nil {
		t.Fatalf("qualified ListUsers = %v", err)
	}
	if len(list.Users) != 1 || list.Users[0].ID != u.ID {
		t.Fatalf("qualified ListUsers = %+v, want only acme's ann", list.Users)
	}
	if len(platform.Snapshot()) == 0 || len(tenantTrail.Snapshot()) == 0 {
		t.Fatalf("operator access recorded %d platform and %d tenant events, want both trails written",
			len(platform.Snapshot()), len(tenantTrail.Snapshot()))
	}

	globex := transport.WithTenantQualifier(operator, "globex")
	if _, err := tr.CreateUser(globex, &transport.CreateUserRequest{Username: "bob", WithoutEmailLoginKey: true}); err == nil {
		t.Fatal("qualified CreateUser in strict globex succeeded, want a refusal")
	}

	own, err := tr.CreateUser(operator, &transport.CreateUserRequest{Username: "carol", WithoutEmailLoginKey: true})
	if err != nil {
		t.Fatalf("unqualified CreateUser = %v", err)
	}
	if tenantid.Normalize(own.TenantID) != tenantid.System {
		t.Fatalf("unqualified CreateUser made a user in tenant %q, want the operator's own", own.TenantID)
	}
}

// TestEmbeddedOperatorIdentityAccess_CertThumbprintLookupIsDecided is the
// regression for GetUserByCertThumbprint skipping the operator-access
// decision: a platform operator's qualified lookup into another tenant is
// now decided and recorded on both trails like every other identity read.
func TestEmbeddedOperatorIdentityAccess_CertThumbprintLookupIsDecided(t *testing.T) {
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatal(err)
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	platform, tenantTrail := audit.NewRingBuffer(64), audit.NewRingBuffer(64)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithUserService(users),
		authmw.WithAuditLogger(audit.NewDecisionLogger(platform)), authmw.WithOperatorAccessTenantTrail(tenantTrail),
		authmw.WithIsolationPolicyReader(isolationPolicies{}))
	tr := newAuthTransport(t, WithUserService(users), WithRoleService(role.NewGrantGuard(roles, mw.Authorizer())),
		WithAuthMiddleware(mw))

	operator := ctxFor(authtest.GrantedAdminIdentity(t, roles, tenantid.System, "operator", permission.UsersGetByCertThumbprint))
	before := len(tenantTrail.Snapshot())
	_, lookupErr := tr.GetUserByCertThumbprint(transport.WithTenantQualifier(operator, "acme"), "AABBCC")
	if got := len(tenantTrail.Snapshot()); got <= before {
		t.Fatalf("qualified GetUserByCertThumbprint (err %v) recorded no operator access on the tenant trail (%d -> %d)",
			lookupErr, before, got)
	}
}
