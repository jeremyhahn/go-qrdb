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

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

func TestBuiltInRoles_UnseededTenantHasNoRoles(t *testing.T) {
	authz := rbac.New(authtest.BuiltInRoles(t, "acme"))
	if err := authorize(t, authz, authtest.AdminIdentity("globex", "g"), "globex", permission.TenantsRead); err == nil {
		t.Fatal("an identity whose role was never seeded must not be granted")
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
