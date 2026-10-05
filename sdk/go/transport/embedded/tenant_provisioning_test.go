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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The embedded half of the tenant-provisioning confinement the network
// transports enforce through tenants:create, tenants:set_quotas,
// tenants:set_entitlements and tenants:set_isolation being ScopeCross
// (go-qrdb#574): a tenant's plan is set by the platform operator only, never
// by the tenant itself, the embedded transport included, even though it runs
// no AuthMiddleware. callerCtx (cert_confinement_test.go) binds the caller's
// tenant exactly as an embedded caller presents itself.

func assertPlatformOperatorRequired(t *testing.T, verb string, err error) {
	t.Helper()
	var denied *tenant.PlatformOperatorRequiredError
	if !errors.As(err, &denied) {
		t.Fatalf("%s: err = %v (%T); want *tenant.PlatformOperatorRequiredError", verb, err, err)
	}
}

func TestTenantProvisioning_CreateTenantIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	svc := &mockTenantService{createFn: func(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
		reached = true
		return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
	}}
	tr := newExtTransport(t, WithTenantService(svc))

	_, err := tr.CreateTenant(callerCtx("acme"), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
	assertPlatformOperatorRequired(t, "a tenant admin minting a tenant", err)
	if reached {
		t.Fatal("a refused create reached the tenant service")
	}

	if _, err := tr.CreateTenant(callerCtx(tenantid.System), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("the platform operator creating a tenant: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's create never reached the tenant service")
	}

	// No identity at all is the implicit system tenant, as a trusted
	// in-process embedder with no auth wiring is treated everywhere else.
	reached = false
	if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "globex", Name: "Globex"}); err != nil {
		t.Fatalf("an unauthenticated embedder creating a tenant: %v", err)
	}
	if !reached {
		t.Fatal("an unauthenticated embedder's create never reached the tenant service")
	}
}

func TestTenantProvisioning_UpdateTenantQuotasIsPlatformOperatorOnly(t *testing.T) {
	reached := false
	svc := &mockTenantService{updateQuotasFn: func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
		reached = true
		return &tenant.TenantInfo{ID: id, Quotas: q}, nil
	}}
	tr := newExtTransport(t, WithTenantService(svc))
	req := &transport.UpdateTenantQuotasRequest{TenantID: "acme", Quotas: transport.TenantQuotas{MaxDatabases: 1000}}

	_, err := tr.UpdateTenantQuotas(callerCtx("acme"), req)
	assertPlatformOperatorRequired(t, "acme raising its own quotas", err)
	if reached {
		t.Fatal("a refused quota change reached the tenant service")
	}

	if _, err := tr.UpdateTenantQuotas(callerCtx(tenantid.System), req); err != nil {
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
	tr := newExtTransport(t, WithTenantService(svc))
	req := &transport.UpdateTenantEntitlementsRequest{TenantID: "acme", Entitlements: transport.TenantEntitlements{AllowDedicatedWAL: true}}

	_, err := tr.UpdateTenantEntitlements(callerCtx("acme"), req)
	assertPlatformOperatorRequired(t, "acme granting itself an entitlement", err)
	if reached {
		t.Fatal("a refused entitlement change reached the tenant service")
	}

	if _, err := tr.UpdateTenantEntitlements(callerCtx(tenantid.System), req); err != nil {
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
	tr := newExtTransport(t, WithTenantService(svc))

	_, err := tr.UpdateTenant(callerCtx("acme"), &transport.UpdateTenantRequest{TenantID: "acme", IsolationType: transport.TenantIsolationType("barrier")})
	assertPlatformOperatorRequired(t, "acme changing its own isolation tier", err)
	if reached {
		t.Fatal("a refused isolation change reached the tenant service")
	}

	// Renaming a tenant is still self-service: no isolation change, no
	// platform-operator check.
	reached = false
	if _, err := tr.UpdateTenant(callerCtx("acme"), &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Corp"}); err != nil {
		t.Fatalf("acme renaming itself: %v", err)
	}
	if !reached {
		t.Fatal("acme's rename never reached the tenant service")
	}

	reached = false
	if _, err := tr.UpdateTenant(callerCtx(tenantid.System), &transport.UpdateTenantRequest{TenantID: "acme", IsolationType: transport.TenantIsolationType("barrier")}); err != nil {
		t.Fatalf("the platform operator changing acme's isolation tier: %v", err)
	}
	if !reached {
		t.Fatal("the platform operator's isolation change never reached the tenant service")
	}
}
