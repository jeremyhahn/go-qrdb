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
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ctxFor binds id both as the authenticated identity and the ambient tenant,
// the same pair finalizeIdentity binds on every network transport.
func ctxFor(id *auth.Identity) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), id.TenantID)
}

// TestEmbeddedTenant_RealAdminRole_CannotDeleteAnotherTenant is the probe
// scenario end to end, wired to the product's REAL built-in Admin role
// (authtest.BuiltInRoles) rather than a synthetic permission grant: a caller
// bound to tenant "acme", holding its own tenant's built-in Admin role --
// which carries tenants:delete -- must still be refused DeleteTenant against
// "globex" or "system".
func TestEmbeddedTenant_RealAdminRole_CannotDeleteAnotherTenant(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme", "globex", tenantid.System)
	mw := authmw.New(rbac.New(roles))
	svc := &fullTenantMock{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	acmeAdmin := authtest.AdminIdentity("acme", "alice")

	for _, target := range []string{"globex", tenantid.System} {
		svc.called = false
		if err := tr.DeleteTenant(ctxFor(acmeAdmin), target); err == nil {
			t.Errorf("acme's Admin deleted %q, which is exactly the probe finding this fix closes", target)
		} else if !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("target=%s: err = %v, want an authorization denial", target, err)
		}
		if svc.called {
			t.Errorf("target=%s: service was reached despite a denial", target)
		}
	}

	// Acting on its own tenant must still work: the fix must refuse
	// cross-tenant access without also refusing self-service.
	if err := tr.DeleteTenant(ctxFor(acmeAdmin), "acme"); err != nil {
		t.Fatalf("acme's Admin must be able to delete its own tenant: %v", err)
	}
	if !svc.called {
		t.Fatal("service was never reached")
	}
}

// TestEmbeddedTenant_RealAdminRole_SystemCanActOnAnyTenant is the mirror
// check: the platform operator -- Admin in the system tenant -- retains
// cross-tenant reach for the ScopeSelf tenant-record operations.
func TestEmbeddedTenant_RealAdminRole_SystemCanActOnAnyTenant(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme", tenantid.System)
	mw := authmw.New(rbac.New(roles))
	svc := &fullTenantMock{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	systemAdmin := authtest.AdminIdentity(tenantid.System, "root")
	if _, err := tr.GetTenant(ctxFor(systemAdmin), "acme"); err != nil {
		t.Fatalf("system Admin must be able to read any tenant: %v", err)
	}
}

// TestEmbeddedTenantBarrierInit_TrustOnFirstInit asserts the bootstrap
// fallback survives the embedded wiring: before the tenant barrier has ever
// been initialized, the tenant's own authenticated identity is admitted even
// holding no role at all, because no role able to satisfy
// tenant_barrier:init could structurally exist yet.
func TestEmbeddedTenantBarrierInit_TrustOnFirstInit(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	// This test exercises the bootstrap window itself, not the separate
	// first-admin-onboarding gate (pkg/auth/middleware/first_init_gate_test.go),
	// so it runs under the "enrolled" posture, which skips that gate when the
	// tenant has no pending-admin record.
	mw := authmw.New(rbac.New(roles), authmw.WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	svc := &mockTenantService{
		barrierStatusFn: func(context.Context, string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{TenantID: "acme", Initialized: false}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	bareIdentity := &auth.Identity{Subject: "bootstrap", TenantID: "acme"}
	_, err := tr.TenantBarrierInit(ctxFor(bareIdentity), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("trust-on-first-init must admit the tenant's own identity before any role exists: %v", err)
	}
}

// TestEmbeddedTenantBarrierInit_DeniedOnceInitialized asserts the bootstrap
// window closes: once the barrier reports Initialized, the SAME bare
// identity is refused, and the full RBAC check applies.
func TestEmbeddedTenantBarrierInit_DeniedOnceInitialized(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	mw := authmw.New(rbac.New(roles))
	svc := &mockTenantService{
		barrierStatusFn: func(context.Context, string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{TenantID: "acme", Initialized: true}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	bareIdentity := &auth.Identity{Subject: "bootstrap", TenantID: "acme"}
	_, err := tr.TenantBarrierInit(ctxFor(bareIdentity), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("an initialized barrier must require the full RBAC grant; the bootstrap window closed")
	}
}

// TestEmbeddedTenantBarrierUnseal_SealedWindowFallback asserts the sealed
// window's own fallback survives the embedded wiring: an identity the
// middleware marked as authenticated during a tenant-IAM outage
// (authtest.SealedIdentity) is admitted for TenantBarrierUnseal even holding
// no role, because the tenant's roles are encrypted behind the barrier being
// unsealed.
func TestEmbeddedTenantBarrierUnseal_SealedWindowFallback(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	mw := authmw.New(rbac.New(roles))
	svc := &mockTenantService{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	sealed := authtest.SealedIdentity("acme", "alice")
	err := tr.TenantBarrierUnseal(ctxFor(sealed), &transport.TenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("k")})
	if err != nil {
		t.Fatalf("the sealed-window fallback must admit the tenant's own sealed identity: %v", err)
	}
}

// TestEmbeddedTenantBarrierUnseal_OrdinaryIdentityNeedsRBAC asserts an
// ordinary (non-sealed) identity with no role is refused: the fallback is
// for the sealed window only, never a general bypass.
func TestEmbeddedTenantBarrierUnseal_OrdinaryIdentityNeedsRBAC(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	mw := authmw.New(rbac.New(roles))
	svc := &mockTenantService{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))

	bare := &auth.Identity{Subject: "alice", TenantID: "acme"}
	err := tr.TenantBarrierUnseal(ctxFor(bare), &transport.TenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("k")})
	if err == nil {
		t.Fatal("an ordinary identity holding no role must be refused tenant_barrier:unseal")
	}
}
