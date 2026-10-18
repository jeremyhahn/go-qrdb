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

// This file covers the embedded-authorization fix for the tenant-management
// surface: a probe found GetTenant, ListTenants, UpdateTenant, DeleteTenant,
// ReconcileDeletedTenants and the TenantBarrier* operations performing no
// tenancy or permission check at all on the embedded transport, so a caller
// bound to tenant "acme" could delete "globex" or "system".
package embedded

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Coverage gate: every transport.TenantClient method must be accounted for.
// ---------------------------------------------------------------------------

// tenantClientAuthorizationSurface enumerates every transport.TenantClient
// method and the mechanism the embedded transport authorizes it through. A
// method added to that interface without an entry here fails
// TestTenantClientAuthorizationSurface_CoversEveryMethod immediately: a new
// tenant operation cannot ship without declaring how it is authorized.
//
// "holder provenance" names the IdentityGated-equivalent claimContext check
// shared with the per-tenant share-claim handshake: these operations
// authenticate a Security Officer or custodian through the factor it
// presents, never an RBAC role, exactly as every network transport treats
// them (permission.IdentityGated). Every other entry names the
// pkg/permission permission the embedded transport checks through
// AuthMiddleware -- the SAME table and the SAME decision (AuthorizeTarget,
// AuthorizeTenantUpdate, AuthorizeTenantBarrierInit,
// AuthorizeTenantBarrierSealedOp) every network transport uses.
var tenantClientAuthorizationSurface = map[string]string{
	"CreateTenant":                    "ambient self: " + string(permission.TenantsCreate),
	"GetTenant":                       "target: " + string(permission.TenantsRead),
	"ListTenants":                     "ambient self: " + string(permission.TenantsList) + " + FilterTenantList narrowing",
	"ListDeletedTenants":              "ambient self: " + string(permission.TenantsListDeleted),
	"ReconcileDeletedTenants":         "ambient self: " + string(permission.TenantsReconcileDeleted),
	"DeleteTenant":                    "target: " + string(permission.TenantsDelete),
	"UpdateTenant":                    "target: " + string(permission.TenantsUpdate) + " (+ " + string(permission.TenantsSetIsolation) + " on an isolation change)",
	"UpdateTenantQuotas":              "target: " + string(permission.TenantsSetQuotas),
	"UpdateTenantEntitlements":        "target: " + string(permission.TenantsSetEntitlements),
	"TenantBarrierInit":               "bootstrap: " + string(permission.TenantBarrierInit) + " (trust-on-first-init fallback)",
	"TenantBarrierUnseal":             "sealed-window: " + string(permission.TenantBarrierUnseal),
	"TenantBarrierSeal":               "target: " + string(permission.TenantBarrierSeal),
	"TenantBarrierStatus":             "sealed-window: " + string(permission.TenantBarrierStatus),
	"TenantBarrierOfficerRegister":    "target: " + string(permission.TenantBarrierOfficerRegister),
	"TenantBarrierOfficerList":        "target: " + string(permission.TenantBarrierOfficerList),
	"TenantBarrierShareClaimBegin":    "holder provenance (claimContext)",
	"TenantBarrierShareClaimComplete": "holder provenance (claimContext) + proof of possession",
	"TenantBarrierQuorumBegin":        "holder provenance (claimContext)",
	"TenantBarrierQuorumSubmit":       "holder provenance (claimContext)",
	"TenantBarrierRekeyStart":         "target: " + string(permission.TenantBarrierRekey),
	"TenantBarrierOfficerRevoke":      "target: " + string(permission.TenantBarrierOfficerRevoke),
	"AddTrustAnchors":                 "target: " + string(permission.TrustAnchorsAdd),
	"ListTrustAnchors":                "target: " + string(permission.TrustAnchorsList),
	"RemoveTrustAnchor":               "target: " + string(permission.TrustAnchorsRemove),
}

// TestTenantClientAuthorizationSurface_CoversEveryMethod is the build gate:
// transport.TenantClient and tenantClientAuthorizationSurface must name
// exactly the same method set. Adding a method to the interface without
// adding it here is a red test, not a silently-unauthorized operation; a
// stale entry for a removed method is caught the same way, so the map
// cannot drift ahead of the interface either.
func TestTenantClientAuthorizationSurface_CoversEveryMethod(t *testing.T) {
	typ := reflect.TypeOf((*transport.TenantClient)(nil)).Elem()
	seen := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		seen[name] = true
		if _, ok := tenantClientAuthorizationSurface[name]; !ok {
			t.Errorf("transport.TenantClient.%s has no entry in tenantClientAuthorizationSurface: "+
				"a new tenant operation must declare how the embedded transport authorizes it before it ships", name)
		}
	}
	for name := range tenantClientAuthorizationSurface {
		if !seen[name] {
			t.Errorf("tenantClientAuthorizationSurface declares %q, which is no longer a transport.TenantClient method", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

// newGrantingAuthMW builds an AuthMiddleware over a real RBAC authorizer
// whose role resolver grants exactly perms to every role an identity names
// -- enough for these tests, which only ever bind an identity to one
// synthetic role.
func newGrantingAuthMW(perms ...permission.Permission) *authmw.AuthMiddleware {
	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return perms, nil
		},
	}
	return authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc))
}

// identityCtx builds a ctx carrying an authenticated identity bound to
// tenantID, via the same auth.WithIdentity/tenant.WithTenantID convention
// every embedded authorization gate reads.
func identityCtx(tenantID string, roles ...string) context.Context {
	id := &auth.Identity{Subject: "caller", TenantID: tenantID, Roles: roles}
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantID)
}

// fullTenantMock extends mockTenantService with the barrier operations it
// does not already implement (rekey, officer-revoke, quorum), and a
// called-tracking flag so a test can assert the service was never reached
// when authorization refused the request.
type fullTenantMock struct {
	mockTenantService
	called bool
}

func (m *fullTenantMock) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	m.called = true
	return m.mockTenantService.GetTenant(ctx, id)
}

func (m *fullTenantMock) DeleteTenant(ctx context.Context, id string) error {
	m.called = true
	return m.mockTenantService.DeleteTenant(ctx, id)
}

func (m *fullTenantMock) UpdateTenant(ctx context.Context, id string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	m.called = true
	return m.mockTenantService.UpdateTenant(ctx, id, patch)
}

func (m *fullTenantMock) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	m.called = true
	return m.mockTenantService.UpdateQuotas(ctx, id, q)
}

func (m *fullTenantMock) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	m.called = true
	return m.mockTenantService.UpdateEntitlements(ctx, id, e)
}

func (m *fullTenantMock) TenantBarrierSeal(ctx context.Context, id string) error {
	m.called = true
	return m.mockTenantService.TenantBarrierSeal(ctx, id)
}

func (m *fullTenantMock) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	m.called = true
	return m.mockTenantService.TenantBarrierStatus(ctx, id)
}

func (m *fullTenantMock) TenantBarrierOfficerRegister(ctx context.Context, id string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	m.called = true
	return m.mockTenantService.TenantBarrierOfficerRegister(ctx, id, req)
}

func (m *fullTenantMock) TenantBarrierOfficerList(ctx context.Context, id string) ([]*ceremony.OfficerStatus, error) {
	m.called = true
	return m.mockTenantService.TenantBarrierOfficerList(ctx, id)
}

func (m *fullTenantMock) TenantBarrierRekeyStart(_ context.Context, _ string, _ int, _ seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	m.called = true
	return &seal.QuorumRoundInfo{}, nil
}

func (m *fullTenantMock) TenantBarrierOfficerRevoke(_ context.Context, _, _ string, _ int) (*seal.QuorumRoundInfo, error) {
	m.called = true
	return &seal.QuorumRoundInfo{}, nil
}

func (m *fullTenantMock) TenantBarrierQuorumBegin(_ context.Context, _ string, _ seal.QuorumOp, _, _ string) (*ceremony.QuorumChallenge, error) {
	m.called = true
	return &ceremony.QuorumChallenge{}, nil
}

func (m *fullTenantMock) TenantBarrierQuorumSubmit(_ context.Context, _ string, _ *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	m.called = true
	return &seal.QuorumOutcome{}, nil
}

// ---------------------------------------------------------------------------
// The probe scenario: a tenant-scoped identity cannot act on another tenant.
// ---------------------------------------------------------------------------

// tenantAuthzCase is one entry of the cross-tenant denial matrix: calling op
// against targetTenant, from a caller bound to callerTenant who holds perm
// everywhere RBAC will resolve it (via grantAllRoleServicer), must be
// admitted exactly when permission.LookupScope(perm) says it should be.
type tenantAuthzCase struct {
	name   string
	perm   permission.Permission
	target string
	op     func(ctx context.Context, tr *Transport, svc *fullTenantMock) error
}

func tenantAuthzCases() []tenantAuthzCase {
	const target = "globex"
	return []tenantAuthzCase{
		{"GetTenant", permission.TenantsRead, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.GetTenant(ctx, target)
			return err
		}},
		{"DeleteTenant", permission.TenantsDelete, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			return tr.DeleteTenant(ctx, target)
		}},
		{"UpdateTenant", permission.TenantsUpdate, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: target, Name: "renamed"})
			return err
		}},
		{"UpdateTenantQuotas", permission.TenantsSetQuotas, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{TenantID: target})
			return err
		}},
		{"UpdateTenantEntitlements", permission.TenantsSetEntitlements, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.UpdateTenantEntitlements(ctx, &transport.UpdateTenantEntitlementsRequest{TenantID: target})
			return err
		}},
		{"TenantBarrierSeal", permission.TenantBarrierSeal, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			return tr.TenantBarrierSeal(ctx, &transport.TenantBarrierSealRequest{TenantID: target})
		}},
		{"TenantBarrierOfficerRegister", permission.TenantBarrierOfficerRegister, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: target, Username: "officer-1"})
			return err
		}},
		{"TenantBarrierOfficerList", permission.TenantBarrierOfficerList, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.TenantBarrierOfficerList(ctx, target)
			return err
		}},
		{"TenantBarrierRekeyStart", permission.TenantBarrierRekey, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: target, Threshold: 2})
			return err
		}},
		{"TenantBarrierOfficerRevoke", permission.TenantBarrierOfficerRevoke, target, func(ctx context.Context, tr *Transport, _ *fullTenantMock) error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: target, Username: "officer-1"})
			return err
		}},
	}
}

// TestEmbeddedTenant_CrossTenantCallerDenied is the probe regression: a
// caller bound to tenant "acme" must be refused every one of these
// operations against "globex" (or the system tenant), EVEN WHILE holding
// the operation's permission -- the scope rule, not the permission grant,
// is what must deny it.
func TestEmbeddedTenant_CrossTenantCallerDenied(t *testing.T) {
	for _, tc := range tenantAuthzCases() {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := permission.LookupScope(tc.perm)
			if err != nil {
				t.Fatalf("LookupScope(%s): %v", tc.perm, err)
			}
			svc := &fullTenantMock{}
			tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW(tc.perm)))

			for _, caller := range []string{"acme", tenantid.System} {
				svc.called = false
				ctx := identityCtx(caller, "member")
				err := tc.op(ctx, tr, svc)

				// ScopeSelf has no system-tenant lift: only ScopeCross
				// admits the system tenant, regardless of target.
				// ScopeSelf and ScopeTenantOnly deny the system tenant
				// exactly like "acme".
				admitted := caller == tenantid.System && scope == permission.ScopeCross
				if admitted {
					if err != nil {
						t.Errorf("caller=%s: want admitted (scope=%v), got %v", caller, scope, err)
					}
					if !svc.called {
						t.Errorf("caller=%s: service was never reached despite admission", caller)
					}
					continue
				}
				if err == nil {
					t.Errorf("caller=%s target=%s: want denied (scope=%v), got nil", caller, tc.target, scope)
				}
				if svc.called {
					t.Errorf("caller=%s: service was reached despite a denial", caller)
				}
				if !errors.Is(err, auth.ErrUnauthorized) {
					t.Errorf("caller=%s: err = %v, want an authorization denial", caller, err)
				}
			}
		})
	}
}

// TestEmbeddedTenant_SystemCrossPermissionReachesAnotherTenant: a system-tenant
// caller holding the named tenants:<op>:cross permission reaches another
// tenant's registry record; holding only the self permission it does not.
func TestEmbeddedTenant_SystemCrossPermissionReachesAnotherTenant(t *testing.T) {
	cross := map[string]permission.Permission{
		"GetTenant":    permission.TenantsReadCross,
		"DeleteTenant": permission.TenantsDeleteCross,
		"UpdateTenant": permission.TenantsUpdateCross,
	}
	for _, tc := range tenantAuthzCases() {
		crossPerm, ok := cross[tc.name]
		if !ok {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			svc := &fullTenantMock{}
			tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW(tc.perm, crossPerm)))
			if err := tc.op(identityCtx(tenantid.System, "member"), tr, svc); err != nil {
				t.Fatalf("system caller holding %s: %v", crossPerm, err)
			}
			if !svc.called {
				t.Fatal("service was never reached despite admission")
			}

			svc.called = false
			selfOnly := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW(tc.perm)))
			if err := tc.op(identityCtx(tenantid.System, "member"), selfOnly, svc); !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("system caller holding only %s: err = %v, want an authorization denial", tc.perm, err)
			}
			if svc.called {
				t.Fatal("service was reached despite a denial")
			}
		})
	}
}

// TestEmbeddedTenant_SameTenantCallerAdmitted is the positive path: a caller
// bound to the target tenant, holding the operation's permission, is
// admitted for every ScopeSelf and ScopeTenantOnly operation -- the gate must
// refuse cross-tenant access without also refusing a tenant acting on
// itself.
func TestEmbeddedTenant_SameTenantCallerAdmitted(t *testing.T) {
	for _, tc := range tenantAuthzCases() {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := permission.LookupScope(tc.perm)
			if err != nil {
				t.Fatalf("LookupScope(%s): %v", tc.perm, err)
			}
			if scope == permission.ScopeCross {
				t.Skip("ScopeCross never admits a tenant-local caller, even on itself")
			}
			svc := &fullTenantMock{}
			tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW(tc.perm)))
			ctx := identityCtx(tc.target, "member")

			if err := tc.op(ctx, tr, svc); err != nil {
				t.Fatalf("tenant acting on itself must be admitted: %v", err)
			}
			if !svc.called {
				t.Fatal("service was never reached")
			}
		})
	}
}

// TestEmbeddedTenant_PermissionDenied asserts the ordinary RBAC
// permission-denied case: a same-tenant caller holding NO permission at all
// is refused, for every tenant-target operation.
func TestEmbeddedTenant_PermissionDenied(t *testing.T) {
	for _, tc := range tenantAuthzCases() {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fullTenantMock{}
			// No permissions granted at all.
			tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW()))
			ctx := identityCtx(tc.target, "member")

			err := tc.op(ctx, tr, svc)
			if err == nil {
				t.Fatal("want denied, got nil")
			}
			if svc.called {
				t.Error("service was reached despite a denial")
			}
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Errorf("err = %v, want an authorization denial", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ScopeCross operations: platform-operator only, target is irrelevant.
// ---------------------------------------------------------------------------

func TestEmbeddedTenant_CrossScopeOperations_SystemOnly(t *testing.T) {
	cases := []struct {
		name string
		perm permission.Permission
		op   func(ctx context.Context, tr *Transport) error
	}{
		{"CreateTenant", permission.TenantsCreate, func(ctx context.Context, tr *Transport) error {
			_, err := tr.CreateTenant(ctx, &transport.CreateTenantRequest{ID: "new-tenant", Name: "n"})
			return err
		}},
		{"ListDeletedTenants", permission.TenantsListDeleted, func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{})
			return err
		}},
		{"ReconcileDeletedTenants", permission.TenantsReconcileDeleted, func(ctx context.Context, tr *Transport) error {
			_, err := tr.ReconcileDeletedTenants(ctx)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockTenantService{}
			onboarding := &mockOnboarding{}
			tr := newExtTransport(t, WithTenantService(svc), WithTenantOnboarding(onboarding), WithAuthMiddleware(newGrantingAuthMW(tc.perm)))

			// A tenant-local caller holding the permission is still denied:
			// ScopeCross ignores the grant for anyone outside the system
			// tenant.
			if err := tc.op(identityCtx("acme", "admin"), tr); err == nil {
				t.Error("tenant-local caller must be denied a platform-operator operation")
			} else if !errors.Is(err, auth.ErrUnauthorized) {
				t.Errorf("err = %v, want an authorization denial", err)
			}

			// A system-tenant caller holding the permission is admitted.
			if err := tc.op(identityCtx(tenantid.System, "admin"), tr); err != nil {
				t.Errorf("system-tenant caller must be admitted: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ListTenants: entry check + FilterTenantList narrowing.
// ---------------------------------------------------------------------------

func TestEmbeddedListTenants_NarrowedToOwnTenantWithoutCrossGrant(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(context.Context) ([]tenant.TenantInfo, error) {
			return []tenant.TenantInfo{{ID: "acme"}, {ID: "globex"}, {ID: tenantid.System}}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(newGrantingAuthMW(permission.TenantsList)))

	resp, err := tr.ListTenants(identityCtx("acme", "member"))
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) != 1 || resp.Tenants[0].ID != "acme" {
		t.Errorf("Tenants = %+v, want only acme's own record", resp.Tenants)
	}
}

func TestEmbeddedListTenants_CrossGrantSeesEveryTenant(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(context.Context) ([]tenant.TenantInfo, error) {
			return []tenant.TenantInfo{{ID: "acme"}, {ID: "globex"}}, nil
		},
	}
	tr := newExtTransport(t, WithTenantService(svc),
		WithAuthMiddleware(newGrantingAuthMW(permission.TenantsList, permission.TenantsListCross)))

	resp, err := tr.ListTenants(identityCtx(tenantid.System, "admin"))
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Errorf("Tenants = %+v, want both tenants for a tenants:list:cross caller", resp.Tenants)
	}
}

// ---------------------------------------------------------------------------
// Unauthenticated deployment: a nil AuthMiddleware authorizes nothing.
// ---------------------------------------------------------------------------

func TestEmbeddedTenant_NoAuthMiddlewareAuthorizesEverything(t *testing.T) {
	svc := &fullTenantMock{}
	// No WithAuthMiddleware option: t.authMW stays nil, matching a network
	// transport run with --auth-enabled=false.
	tr := newExtTransport(t, WithTenantService(svc))

	ctx := identityCtx("acme", "member")
	if err := tr.DeleteTenant(ctx, "globex"); err != nil {
		t.Fatalf("an unconfigured AuthMiddleware must authorize everything, got %v", err)
	}
	if !svc.called {
		t.Fatal("service was never reached")
	}
}

// ---------------------------------------------------------------------------
// Rate limiting.
// ---------------------------------------------------------------------------

func TestEmbeddedTenant_RateLimitApplies(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.TenantsDelete, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	roleSvc := &mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.TenantsDelete}, nil
		},
	}
	mw := authmw.New(rbac.New(roleSvc), authmw.WithRoleResolver(roleSvc), authmw.WithRateLimiter(limiter), authmw.WithRateLimitPolicies(policies))

	svc := &fullTenantMock{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))
	ctx := identityCtx("acme", "admin")

	if err := tr.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("first call within burst: %v", err)
	}
	err := tr.DeleteTenant(ctx, "acme")
	var limited *authmw.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("second call over burst: err = %v, want *authmw.RateLimitedError", err)
	}
}

// ---------------------------------------------------------------------------
// Audit stamping.
// ---------------------------------------------------------------------------

type capturingAuditLogger struct {
	entries []*audit.Entry
}

func (c *capturingAuditLogger) Log(_ context.Context, entry *audit.Entry) error {
	c.entries = append(c.entries, entry)
	return nil
}

func (c *capturingAuditLogger) Close() error { return nil }

func TestEmbeddedTenant_AuditStampedWithCallerTenant(t *testing.T) {
	logger := &capturingAuditLogger{}
	mw := authmw.New(rbac.New(&mockRoleServicer{
		resolvePermsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return nil, nil // deny, so the decision is recorded
		},
	}), authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuditLogger(logger))

	svc := &fullTenantMock{}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))
	ctx := identityCtx("acme", "member")

	if err := tr.DeleteTenant(ctx, "acme"); err == nil {
		t.Fatal("want denied (no permissions granted)")
	}

	if len(logger.entries) == 0 {
		t.Fatal("want an audit entry for the refused decision")
	}
	last := logger.entries[len(logger.entries)-1]
	if last.TenantID != "acme" {
		t.Errorf("audit entry TenantID = %q, want the caller's tenant %q", last.TenantID, "acme")
	}
	if last.Result != audit.ResultDenied {
		t.Errorf("audit entry Result = %v, want ResultDenied", last.Result)
	}
}
