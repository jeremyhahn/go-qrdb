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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ownerTestTenants are the tenants newLoginRoleStore seeds.
var ownerTestTenants = []string{tenantid.System, "acme", "globex"}

// newLoginRoleStore is the real role store over memory, with every
// ownerTestTenants tenant's built-in roles seeded the way bootstrap seeds
// them, so TenantOwner is resolved exactly as in production.
func newLoginRoleStore(t *testing.T) role.RoleServicer {
	t.Helper()
	svc, err := role.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	for _, tid := range ownerTestTenants {
		if _, err := svc.ReconcileBuiltInRoles(tenant.WithTenantID(context.Background(), tid), tid); err != nil {
			t.Fatalf("ReconcileBuiltInRoles(%s): %v", tid, err)
		}
	}
	return svc
}

// roleID returns the id the store persisted for tenantID's role name.
func roleID(t *testing.T, svc role.RoleServicer, tenantID, name string) string {
	t.Helper()
	r, err := svc.GetRoleByName(tenant.WithTenantID(context.Background(), tenantID), tenantID, name)
	if err != nil {
		t.Fatalf("GetRoleByName(%s, %s): %v", tenantID, name, err)
	}
	return r.ID
}

// ownerLogin runs a full BeginLogin/FinishLogin for the principal lookup
// currently maps user 1 to, on a transport over roles.
type ownerLogin struct {
	t      *testing.T
	lookup *memUserLookup
	tr     *Transport
	cred   []byte
}

func newOwnerLogin(t *testing.T, roles role.RoleServicer, principal *auth.Principal) *ownerLogin {
	t.Helper()
	svc := newMemUserAuthFactorService()
	cred := []byte("owner-cred")
	enrolledFactor(t, svc, 1, auth.AuthFactorFIDO2, cred)
	lookup := &memUserLookup{principals: map[uint64]*auth.Principal{1: principal}}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithUserAuthFactorService(svc),
		WithLoginResolver(auth.NewLoginResolver(svc, lookup)),
		WithSessionIssuer(newMemSessionIssuer()),
		WithRoleService(roles),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &ownerLogin{t: t, lookup: lookup, tr: tr, cred: cred}
}

func (o *ownerLogin) login(tenantID string) *transport.FinishLoginResponse {
	o.t.Helper()
	lc := o.tr.LoginClient()
	begin, err := lc.BeginLogin(context.Background(), transport.BeginLoginRequest{Type: transport.FactorTypeFIDO2, TenantID: tenantID})
	if err != nil {
		o.t.Fatalf("BeginLogin: %v", err)
	}
	finish, err := lc.FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypeFIDO2,
		LookupKey: o.cred,
	})
	if err != nil {
		o.t.Fatalf("FinishLogin: %v", err)
	}
	return finish
}

func TestDefaultIdentityHydrator_TenantAdminIsOwner(t *testing.T) {
	roles := newLoginRoleStore(t)
	h := &defaultIdentityHydrator{roles: roles}
	id, err := h.Hydrate(context.Background(),
		&auth.Principal{ID: 1, TenantID: "acme", Roles: []string{roleID(t, roles, "acme", role.RoleNameAdmin)}, Active: true}, nil)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if !id.TenantOwner {
		t.Fatal("a tenant Admin must hydrate with TenantOwner")
	}
}

func TestDefaultIdentityHydrator_NonAdminIsNotOwner(t *testing.T) {
	roles := newLoginRoleStore(t)
	h := &defaultIdentityHydrator{roles: roles}
	id, err := h.Hydrate(context.Background(),
		&auth.Principal{ID: 1, TenantID: "acme", Roles: []string{roleID(t, roles, "acme", role.RoleNameOperator)}, Active: true}, nil)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if id.TenantOwner {
		t.Fatal("an Operator must not hydrate with TenantOwner")
	}
}

// Without a role store the hydrator cannot decide TenantOwner, and refuses
// rather than issue an identity that says "not owner" by default.
func TestDefaultIdentityHydrator_NoRoleStoreRefuses(t *testing.T) {
	h := &defaultIdentityHydrator{}
	_, err := h.Hydrate(context.Background(), &auth.Principal{ID: 1, TenantID: "acme", Active: true}, nil)
	var want *role.ResolverRequiredError
	if !errors.As(err, &want) {
		t.Fatalf("want *role.ResolverRequiredError, got %v", err)
	}
}

func TestDefaultIdentityHydrator_StoreErrorAbortsLogin(t *testing.T) {
	h := &defaultIdentityHydrator{roles: failingNameResolver{}}
	if _, err := h.Hydrate(context.Background(), &auth.Principal{ID: 1, TenantID: "acme", Roles: []string{"r-1"}, Active: true}, nil); err == nil {
		t.Fatal("a role store error must abort hydration")
	}
}

type failingNameResolver struct{}

func (failingNameResolver) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	return nil, errors.New("role store unavailable")
}

func TestLoginClient_FinishLogin_TenantAdminIsOwnerOnEveryProjection(t *testing.T) {
	roles := newLoginRoleStore(t)
	o := newOwnerLogin(t, roles, &auth.Principal{
		ID: 1, Username: "alice", TenantID: "acme", Active: true,
		Roles: []string{roleID(t, roles, "acme", role.RoleNameAdmin)},
	})
	finish := o.login("acme")
	if !finish.Identity.TenantOwner {
		t.Fatalf("FinishLogin identity: want TenantOwner, got %+v", finish.Identity)
	}
	if _, ok := finish.Identity.Metadata["tenantOwner"]; ok {
		t.Fatal("TenantOwner must not be carried in Metadata")
	}

	viaLogin, err := o.tr.LoginClient().ResolveIdentity(context.Background(), finish.Bearer)
	if err != nil || !viaLogin.TenantOwner {
		t.Fatalf("LoginClient.ResolveIdentity: id=%+v err=%v", viaLogin, err)
	}
	viaSession, err := o.tr.SessionClient().ResolveIdentity(context.Background(), finish.Bearer)
	if err != nil || !viaSession.TenantOwner {
		t.Fatalf("SessionClient.ResolveIdentity: id=%+v err=%v", viaSession, err)
	}
}

func TestLoginClient_FinishLogin_NonAdminIsNotOwner(t *testing.T) {
	roles := newLoginRoleStore(t)
	o := newOwnerLogin(t, roles, &auth.Principal{
		ID: 1, Username: "bob", TenantID: "acme", Active: true,
		Roles: []string{roleID(t, roles, "acme", role.RoleNameOperator)},
	})
	finish := o.login("acme")
	if finish.Identity.TenantOwner {
		t.Fatalf("an Operator must not be the tenant owner, got %+v", finish.Identity)
	}
	resolved, err := o.tr.SessionClient().ResolveIdentity(context.Background(), finish.Bearer)
	if err != nil || resolved.TenantOwner {
		t.Fatalf("SessionClient.ResolveIdentity: id=%+v err=%v", resolved, err)
	}
}

func TestLoginClient_FinishLogin_SystemAdminOwnsOnlySystemTenant(t *testing.T) {
	roles := newLoginRoleStore(t)
	sysAdmin := roleID(t, roles, tenantid.System, role.RoleNameAdmin)

	sys := newOwnerLogin(t, roles, &auth.Principal{ID: 1, Username: "root", TenantID: tenantid.System, Active: true, Roles: []string{sysAdmin}})
	finish := sys.login(tenantid.System)
	if !finish.Identity.TenantOwner || finish.Identity.TenantID != tenantid.System {
		t.Fatalf("system Admin must own the system tenant, got %+v", finish.Identity)
	}

	// The system Admin's role id carried by a principal of another tenant
	// makes it owner of nothing there.
	other := newOwnerLogin(t, roles, &auth.Principal{ID: 1, Username: "mallory", TenantID: "acme", Active: true, Roles: []string{sysAdmin}})
	if got := other.login("acme").Identity; got.TenantOwner {
		t.Fatalf("a system Admin grant must confer no ownership of acme, got %+v", got)
	}
}

// TenantOwner is decided when the identity is established: withdrawing the
// Admin role from the principal shows at its next login, and granting it
// back shows at the one after.
func TestLoginClient_FinishLogin_RoleChangeReflectedAtNextLogin(t *testing.T) {
	roles := newLoginRoleStore(t)
	admin := roleID(t, roles, "acme", role.RoleNameAdmin)
	operator := roleID(t, roles, "acme", role.RoleNameOperator)
	principal := &auth.Principal{ID: 1, Username: "carol", TenantID: "acme", Active: true, Roles: []string{operator, admin}}
	o := newOwnerLogin(t, roles, principal)

	if !o.login("acme").Identity.TenantOwner {
		t.Fatal("first login as Admin: want TenantOwner")
	}
	principal.Roles = []string{operator}
	if o.login("acme").Identity.TenantOwner {
		t.Fatal("login after the Admin role was removed: want no TenantOwner")
	}
	principal.Roles = []string{admin}
	if !o.login("acme").Identity.TenantOwner {
		t.Fatal("login after the Admin role was granted back: want TenantOwner")
	}
}

func TestIdentityToTransport_ProjectsTenantOwner(t *testing.T) {
	for _, owner := range []bool{true, false} {
		got := identityToTransport(&auth.Identity{Subject: "1", TenantID: "acme", TenantOwner: owner})
		if got.TenantOwner != owner {
			t.Fatalf("identityToTransport(TenantOwner=%v) = %v", owner, got.TenantOwner)
		}
	}
}

// A caller-built transport.Identity cannot assert ownership into the domain.
func TestIdentityToDomain_DoesNotTrustCallerTenantOwner(t *testing.T) {
	got := identityToDomain(&transport.Identity{Subject: "1", TenantID: "acme", TenantOwner: true})
	if got.TenantOwner {
		t.Fatal("identityToDomain must not carry a caller-supplied TenantOwner")
	}
	if got.Subject != "1" || got.TenantID != "acme" {
		t.Fatalf("identityToDomain dropped other fields: %+v", got)
	}
}
