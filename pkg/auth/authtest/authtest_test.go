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

package authtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestSealedIdentity_CarriesTheMiddlewareMarker pins the marker key against
// the middleware's own predicate: if the key drifts, SealedIdentity stops
// exercising the sealed window and every test built on it goes vacuous.
func TestSealedIdentity_CarriesTheMiddlewareMarker(t *testing.T) {
	if !authmw.IdentityIAMSealed(authtest.SealedIdentity("acme", "a")) {
		t.Fatal("SealedIdentity is not recognised as sealed by the middleware")
	}
	if authmw.IdentityIAMSealed(authtest.AdminIdentity("acme", "a")) {
		t.Fatal("AdminIdentity must not carry the sealed marker")
	}
}

func TestSealedIdentity_HasNoRoles(t *testing.T) {
	if got := authtest.SealedIdentity("acme", "a").Roles; len(got) != 0 {
		t.Fatalf("roles = %v, want none: a sealed tenant's roles are unreadable", got)
	}
}

// authorize runs the real RBAC decision for id against target.
func authorize(t *testing.T, authz auth.Authorizer, id *auth.Identity, target string, perm permission.Permission) error {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), target)
	return authz.Authorize(ctx, id, permission.Resource(perm), permission.Action(perm))
}

func TestAdminIdentity_HoldsTheBuiltInAdminGrantInItsOwnTenant(t *testing.T) {
	authz := rbac.New(authtest.BuiltInRoles(t, "acme"))
	if err := authorize(t, authz, authtest.AdminIdentity("acme", "a"), "acme", permission.TenantsRead); err != nil {
		t.Fatalf("acme admin reading acme = %v, want nil", err)
	}
}

func TestAdminIdentity_CrossScopeRefusedOutsideTheSystemTenant(t *testing.T) {
	authz := rbac.New(authtest.BuiltInRoles(t, "acme", tenantid.System))
	var denied *rbac.PermissionDeniedError
	err := authorize(t, authz, authtest.AdminIdentity("acme", "a"), tenantid.System, permission.TenantsCreateCross)
	if !errors.As(err, &denied) {
		t.Fatalf("acme admin tenants:create:cross = %v, want *rbac.PermissionDeniedError", err)
	}
	if err := authorize(t, authz, authtest.AdminIdentity(tenantid.System, "root"), tenantid.System, permission.TenantsCreateCross); err != nil {
		t.Fatalf("system admin tenants:create:cross = %v, want nil", err)
	}
}

// TestEnableEncryptionScopeCases_VerdictsAreRBACs pins the table every
// transport is tested against to the authorizer itself: each verdict is what
// RBAC decides for the permission and target the per-scope rule names, so a
// transport test that agrees with the table agrees with RBAC.
func TestEnableEncryptionScopeCases_VerdictsAreRBACs(t *testing.T) {
	authz := rbac.New(authtest.BuiltInRoles(t))
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			target, perm := tenantid.System, permission.SealEnableEncryption
			if c.TenantID != "" {
				target, perm = c.TenantID, permission.TenantBarrierEnableEncryption
			}
			err := authorize(t, authz, c.Caller, target, perm)
			if c.Admit && err != nil {
				t.Fatalf("%s on %q = %v, want admitted", perm, target, err)
			}
			var denied *rbac.PermissionDeniedError
			if !c.Admit && !errors.As(err, &denied) {
				t.Fatalf("%s on %q = %v, want *rbac.PermissionDeniedError", perm, target, err)
			}
		})
	}
}

// TestEnableEncryptionScopeCases_CoverBothScopesBothWays guards the table's
// shape: both scopes carry an admitted and a refused caller, so no transport
// test can pass by admitting or refusing everything.
func TestEnableEncryptionScopeCases_CoverBothScopesBothWays(t *testing.T) {
	seen := map[[2]bool]bool{}
	for _, c := range authtest.EnableEncryptionScopeCases() {
		seen[[2]bool{c.TenantID != "", c.Admit}] = true
	}
	for _, k := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		if !seen[k] {
			t.Fatalf("no case with tenant-scope=%v admit=%v", k[0], k[1])
		}
	}
}

func TestBuiltInRoles_UnseededTenantHasNoRoles(t *testing.T) {
	authz := rbac.New(authtest.BuiltInRoles(t, "acme"))
	if err := authorize(t, authz, authtest.AdminIdentity("globex", "g"), "globex", permission.TenantsRead); err == nil {
		t.Fatal("an identity whose role was never seeded must not be granted")
	}
}

func TestCustomRole_GrantsExactlyItsPermissions(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	id := &auth.Identity{Subject: "d", TenantID: "acme", Roles: []string{
		authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard),
	}}
	authz := rbac.New(roles)
	if err := authorize(t, authz, id, "acme", permission.DevicesOnboard); err != nil {
		t.Fatalf("the custom role's own permission: %v", err)
	}
	if err := authorize(t, authz, id, "acme", permission.OTAActivate); err == nil {
		t.Fatal("a permission the custom role does not carry was granted")
	}
}

func TestCustomRole_BelongsToItsTenant(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme", "globex")
	acmeID := authtest.CustomRole(t, roles, "acme", "device-onboarder", permission.DevicesOnboard)
	globexID := authtest.CustomRole(t, roles, "globex", "device-onboarder", permission.DevicesOnboard)
	if acmeID == globexID {
		t.Fatalf("one role name in two tenants yielded one ID %q", acmeID)
	}
	got, err := roles.GetRole(context.Background(), globexID)
	if err != nil || got.TenantID != "globex" {
		t.Fatalf("GetRole(%q) = %+v, %v; want a globex role", globexID, got, err)
	}
}

func TestFixedAuthenticator_ReturnsACopy(t *testing.T) {
	a := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "a"))
	got, err := a.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	got.TenantID = "globex"
	again, err := a.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if again.TenantID != "acme" {
		t.Fatal("mutating the authenticated identity changed the fixed one")
	}
	if a.Name() == "" {
		t.Fatal("Name must identify the authenticator")
	}
}

func TestFixedAuthenticator_SetIsSafeAgainstConcurrentAuthenticate(t *testing.T) {
	a := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "a"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if _, err := a.Authenticate(context.Background()); err != nil {
				t.Errorf("Authenticate: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		a.Set(authtest.AdminIdentity("globex", "g"))
	}
	<-done
	got, err := a.Authenticate(context.Background())
	if err != nil || got.TenantID != "globex" {
		t.Fatalf("after Set: %+v, %v; want globex", got, err)
	}
}

func TestFixedAuthenticator_ZeroValueIsUnauthenticated(t *testing.T) {
	var a authtest.FixedAuthenticator
	if _, err := a.Authenticate(context.Background()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("zero value = %v, want auth.ErrUnauthenticated", err)
	}
}

// Users resolves the authenticator's CURRENT identity, by username and by
// id, to an active record carrying its roles; any other account is not
// found. Set moves the record with the identity.
func TestFixedAuthenticator_UsersResolvesCurrentIdentity(t *testing.T) {
	ctx := context.Background()
	alice := authtest.AdminIdentity("acme", "alice")
	alice.RoleBindings = []auth.RoleBinding{{RoleID: "r1", TenantID: "acme"}}
	alice.Attributes = map[string]string{"dept": "ops"}
	fa := authtest.NewFixedAuthenticator(alice)
	users := fa.Users()

	rec, err := users.GetUserByUsername(ctx, "acme", "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername(alice): %v", err)
	}
	if rec.ID == 0 || rec.Username != "alice" || rec.TenantID != "acme" || rec.Lifecycle != user.LifecycleActive {
		t.Fatalf("record = %+v, want active alice in acme with a non-zero id", rec)
	}
	if len(rec.RoleIDs) != len(alice.Roles) || rec.RoleIDs[0] != alice.Roles[0] {
		t.Fatalf("RoleIDs = %v, want %v", rec.RoleIDs, alice.Roles)
	}
	if len(rec.RoleBindings) != 1 || rec.RoleBindings[0].RoleID != "r1" || rec.Attributes["dept"] != "ops" {
		t.Fatalf("bindings/attributes = %v/%v, want copied", rec.RoleBindings, rec.Attributes)
	}
	byID, err := users.GetUser(ctx, rec.ID)
	if err != nil || byID.Username != "alice" {
		t.Fatalf("GetUser(%d) = %v, %v; want alice", rec.ID, byID, err)
	}
	again, _ := users.GetUserByUsername(ctx, "acme", "alice")
	if again.ID != rec.ID {
		t.Fatalf("id not deterministic: %d then %d", rec.ID, again.ID)
	}

	var notFound *user.UserNotFoundError
	if _, err := users.GetUserByUsername(ctx, "acme", "bob"); !errors.As(err, &notFound) {
		t.Fatalf("GetUserByUsername(bob) = %v, want UserNotFoundError", err)
	}
	if _, err := users.GetUserByUsername(ctx, "other", "alice"); !errors.As(err, &notFound) {
		t.Fatalf("alice in another tenant = %v, want UserNotFoundError", err)
	}
	if _, err := users.GetUser(ctx, rec.ID+1); !errors.As(err, &notFound) {
		t.Fatalf("GetUser(other id) = %v, want UserNotFoundError", err)
	}

	fa.Set(&auth.Identity{Subject: "bob", TenantID: "acme"})
	if _, err := users.GetUserByUsername(ctx, "acme", "alice"); !errors.As(err, &notFound) {
		t.Fatalf("after Set(bob), alice = %v, want UserNotFoundError", err)
	}
	bob, err := users.GetUserByUsername(ctx, "acme", "bob")
	if err != nil || len(bob.RoleIDs) != 0 {
		t.Fatalf("after Set(bob), bob = %v, %v; want a role-less record", bob, err)
	}
}

func TestOneToken_AdmitsOnceThenRefuses(t *testing.T) {
	policies := authtest.OneToken(permission.TenantBarrierUnseal)
	policy, ok := policies.Lookup(permission.TenantBarrierUnseal)
	if !ok {
		t.Fatal("OneToken declared no policy for its permission")
	}
	l := ratelimit.NewTokenBucketLimiter()
	defer l.Close()
	if ok, _ := l.Allow("k", policy); !ok {
		t.Fatal("the first request must be admitted")
	}
	if ok, _ := l.Allow("k", policy); ok {
		t.Fatal("the second request must be refused")
	}
	if _, ok := policies.Lookup(permission.TenantBarrierStatus); ok {
		t.Fatal("OneToken must declare a policy for its permission only")
	}
}

func TestNewTenants_HoldsTheNamedTenants(t *testing.T) {
	svc := authtest.NewTenants(t, tenantid.System, "acme")
	got, err := svc.ListTenants(context.Background())
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d tenants, want 2", len(got))
	}
	if _, err := svc.GetTenant(context.Background(), "globex"); err == nil {
		t.Fatal("a tenant that was not named must not exist")
	}
}

func TestTenants_CountsUnseals(t *testing.T) {
	svc := authtest.NewTenants(t, "acme")
	if svc.Unseals() != 0 {
		t.Fatal("a fresh service has seen no unseal")
	}
	for i := 0; i < 2; i++ {
		if err := svc.TenantBarrierUnseal(context.Background(), "acme", []byte("k")); err != nil {
			t.Fatalf("TenantBarrierUnseal: %v", err)
		}
	}
	if svc.Unseals() != 2 {
		t.Fatalf("Unseals = %d, want 2", svc.Unseals())
	}
}

func TestTenants_HoldsDataDrivesTheIsolationPrecondition(t *testing.T) {
	svc := authtest.NewTenants(t, "acme")
	svc.HoldsData(t, true)
	if _, err := svc.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{IsolationType: tenant.IsolationVolume}); !errors.Is(err, tenant.ErrTenantHasData) {
		t.Fatalf("with data = %v, want ErrTenantHasData", err)
	}
	svc.HoldsData(t, false)
	if _, err := svc.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{IsolationType: tenant.IsolationVolume}); err != nil {
		t.Fatalf("without data = %v, want nil", err)
	}
}
