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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mockOnboarding is a service.TenantOnboardingServicer double: CreateTenant
// delegates to it exclusively (not to tenant.TenantServicer directly), so
// the platform-operator confinement tests need one wired via
// WithTenantOnboarding to ever reach the service layer at all.
type mockOnboarding struct {
	onboardFn func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error)
}

func (m *mockOnboarding) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	if m.onboardFn != nil {
		return m.onboardFn(ctx, req)
	}
	return &service.OnboardTenantResult{Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name}}, nil
}

// The embedded half of the tenant-provisioning confinement the network
// transports enforce through tenants:create, tenants:set_quotas,
// tenants:set_entitlements and tenants:set_isolation being ScopeCross
// (go-qrdb#574): a tenant's plan is set by the platform operator only, never
// by the tenant itself.
//
// There is exactly ONE mechanism for this, on every transport alike:
// AuthMiddleware.AuthorizeTarget's ScopeCross decision, reached here through
// authorizeTenantSelf/authorizeTenantTarget/authorizeTenantUpdate. There used
// to be a second, parallel one -- tenant.RequirePlatformOperator, a
// structural check with no AuthMiddleware involved at all, admitting any
// caller whenever none was configured by treating an absent identity as the
// implicit system tenant. The owner's ruling ("one pluggable auth chain on
// every transport") retired it: a request with no identity is now refused
// exactly as it would be over the network when an AuthMiddleware IS
// installed, and when NO AuthMiddleware is installed every request is
// admitted, exactly as a network transport run with no AuthMiddleware
// configured (--auth-enabled=false) admits everything. There is no third
// state where an absent identity is quietly promoted to the system tenant.
//
// The middleware here is REAL RBAC (authtest.BuiltInRoles), not a stub, so
// the denial these tests pin is the actual ScopeCross decision every network
// transport makes for the same permission, not a double that could assert
// the wrong thing and stay green.

func newProvisioningMiddleware(t *testing.T) *authmw.AuthMiddleware {
	t.Helper()
	roles := authtest.BuiltInRoles(t, "acme", tenantid.System)
	return authmw.New(rbac.New(roles))
}

func acmeAdminCtx() context.Context {
	return ctxFor(authtest.AdminIdentity("acme", "alice"))
}

func systemAdminCtx() context.Context {
	return ctxFor(authtest.AdminIdentity(tenantid.System, "root"))
}

func assertCrossScopeDenied(t *testing.T, verb string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: succeeded, want a ScopeCross permission denial", verb)
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("%s: err = %v (%T); want a permission denial wrapping auth.ErrUnauthorized", verb, err, err)
	}
}

func TestTenantProvisioning_CreateTenantIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	onboarding := &mockOnboarding{onboardFn: func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
		reached = true
		return &service.OnboardTenantResult{Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name, IsolationType: req.IsolationType}}, nil
	}}
	tr := newExtTransport(t, WithTenantOnboarding(onboarding), WithAuthMiddleware(newProvisioningMiddleware(t)))

	_, err := tr.CreateTenant(acmeAdminCtx(), &transport.CreateTenantRequest{ID: "acme2", Name: "Acme"})
	assertCrossScopeDenied(t, "a tenant admin minting a tenant", err)
	if reached {
		t.Fatal("a refused create reached the tenant service")
	}

	if _, err := tr.CreateTenant(systemAdminCtx(), &transport.CreateTenantRequest{ID: "acme2", Name: "Acme"}); err != nil {
		t.Fatalf("the platform operator creating a tenant: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's create never reached the tenant service")
	}
}

// TestTenantProvisioning_NoMiddlewareAdmitsEverything pins the OTHER half of
// the ruling: with no AuthMiddleware configured at all, RBAC is opt-in and
// every caller is admitted, exactly as a network transport run with no
// AuthMiddleware would admit everything. This is deliberately different from
// "an authenticated caller with no cross-scope grant", which the tests above
// cover -- the two must not be collapsed into one behavior.
func TestTenantProvisioning_NoMiddlewareAdmitsEverything(t *testing.T) {
	reached := false
	onboarding := &mockOnboarding{onboardFn: func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
		reached = true
		return &service.OnboardTenantResult{Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name, IsolationType: req.IsolationType}}, nil
	}}
	tr := newExtTransport(t, WithTenantOnboarding(onboarding))

	if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "globex", Name: "Globex"}); err != nil {
		t.Fatalf("an unauthenticated embedder creating a tenant with no AuthMiddleware configured: %v", err)
	}
	if !reached {
		t.Fatal("the create never reached the tenant service")
	}
}

func TestTenantProvisioning_UpdateTenantQuotasIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	svc := &mockTenantService{updateQuotasFn: func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
		reached = true
		return &tenant.TenantInfo{ID: id, Quotas: q}, nil
	}}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newProvisioningMiddleware(t)))
	req := &transport.UpdateTenantQuotasRequest{TenantID: "acme", Quotas: transport.TenantQuotas{MaxDatabases: 1000}}

	_, err := tr.UpdateTenantQuotas(acmeAdminCtx(), req)
	assertCrossScopeDenied(t, "acme raising its own quotas", err)
	if reached {
		t.Fatal("a refused quota change reached the tenant service")
	}

	if _, err := tr.UpdateTenantQuotas(systemAdminCtx(), req); err != nil {
		t.Fatalf("the platform operator setting acme's quotas: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's quota change never reached the tenant service")
	}
}

func TestTenantProvisioning_UpdateTenantEntitlementsIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	svc := &mockTenantService{updateEntFn: func(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
		reached = true
		return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
	}}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newProvisioningMiddleware(t)))
	req := &transport.UpdateTenantEntitlementsRequest{TenantID: "acme", Entitlements: transport.TenantEntitlements{AllowDedicatedWAL: true}}

	_, err := tr.UpdateTenantEntitlements(acmeAdminCtx(), req)
	assertCrossScopeDenied(t, "acme granting itself an entitlement", err)
	if reached {
		t.Fatal("a refused entitlement change reached the tenant service")
	}

	if _, err := tr.UpdateTenantEntitlements(systemAdminCtx(), req); err != nil {
		t.Fatalf("the platform operator granting acme an entitlement: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's entitlement change never reached the tenant service")
	}
}

func TestTenantProvisioning_UpdateTenantIsolationChangeIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	svc := &mockTenantService{updateFn: func(ctx context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
		reached = true
		return &tenant.TenantInfo{ID: id, Name: patch.Name, IsolationType: patch.IsolationType}, nil
	}}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newProvisioningMiddleware(t)))

	_, err := tr.UpdateTenant(acmeAdminCtx(), &transport.UpdateTenantRequest{TenantID: "acme", IsolationType: transport.TenantIsolationType("barrier")})
	assertCrossScopeDenied(t, "acme changing its own isolation tier", err)
	if reached {
		t.Fatal("a refused isolation change reached the tenant service")
	}

	// Renaming a tenant is still self-service: no isolation change, no
	// platform-operator check -- tenants:update is ScopeSelf, and acme's
	// Admin holds it in its own tenant.
	reached = false
	if _, err := tr.UpdateTenant(acmeAdminCtx(), &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Corp"}); err != nil {
		t.Fatalf("acme renaming itself: %v", err)
	}
	if !reached {
		t.Fatal("acme's rename never reached the tenant service")
	}

	reached = false
	if _, err := tr.UpdateTenant(systemAdminCtx(), &transport.UpdateTenantRequest{TenantID: "acme", IsolationType: transport.TenantIsolationType("barrier")}); err != nil {
		t.Fatalf("the platform operator changing acme's isolation tier: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's isolation change never reached the tenant service")
	}
}
