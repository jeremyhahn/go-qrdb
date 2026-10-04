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

package rbac

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// countingRoleService wraps a role.RoleServicer and counts GetRoleByName
// calls, so a test can prove systemRoleIDs' tenant gate was actually
// CROSSED (or not) rather than merely that the final outcome happened to
// come out right -- a mutation that deleted the gate but left every
// assertion passing by coincidence would otherwise go unnoticed.
type countingRoleService struct {
	role.RoleServicer
	getRoleByNameCalls atomic.Int64
}

func (s *countingRoleService) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	s.getRoleByNameCalls.Add(1)
	return s.RoleServicer.GetRoleByName(ctx, tenantID, name)
}

// newSystemConfinedRoleStore builds the fallback role store exactly as
// qrdbd does (cmd/qrdbd: bootRoleSvc): the real role store over a kvstore
// confined to the SYSTEM tenant's identity namespace. It is the faithful
// double for the production fallback because it IS the production
// construction — it can hold system-tenant roles and nothing else.
func newSystemConfinedRoleStore(t *testing.T) role.RoleServicer {
	t.Helper()
	confined, err := kvstore.NewNamespacedKVStore(kvstore.NewMemoryKVStore(),
		tenant.IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatalf("NewNamespacedKVStore: %v", err)
	}
	svc, err := role.NewStore(confined)
	if err != nil {
		t.Fatalf("role.NewStore: %v", err)
	}
	return svc
}

// seedBuiltInRole seeds tenantID's canonical built-in role named name through
// ReconcileBuiltInRoles — the exact seeding path runFirstBoot uses — and
// returns the resulting record. A reserved built-in name (the six canonical
// roles) can only be created this way: CreateRole refuses to persist one
// directly unless it is byte-for-byte the canonical record already
// (requireCanonicalSystemRole), to keep a caller from squatting the name a
// grant-voiding control resolves through.
func seedBuiltInRole(t *testing.T, svc role.RoleServicer, tenantID, name string) *role.Role {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	if _, err := svc.ReconcileBuiltInRoles(ctx, tenantID); err != nil {
		t.Fatalf("ReconcileBuiltInRoles(%s): %v", tenantID, err)
	}
	r, err := svc.GetRoleByName(ctx, tenantID, name)
	if err != nil {
		t.Fatalf("GetRoleByName(%s, %s) after seeding: %v", tenantID, name, err)
	}
	return r
}

// ---------------------------------------------------------------------------
// Path 1: resolvePermissionSet — the fallback must not answer for a grant.
// ---------------------------------------------------------------------------

// TestResolvePermissionSet_NeverConsultsFallbackForAnotherTenant is the
// regression for go-qrdb strict-isolation phase 0 review finding B1 (path 1):
// resolvePermissionSet consulted the StaleRead role fallback for every
// tenant's role ids. qrdbd's fallback is confined to the system tenant's
// identity namespace, so for any other tenant it cannot answer about what a
// role grants at all. The primary's transient error must stand — modelled on
// TestFallbackUserStore_NeverAnswersForAnotherTenant
// (pkg/auth/middleware/user_fallback_scope_test.go).
func TestResolvePermissionSet_NeverConsultsFallbackForAnotherTenant(t *testing.T) {
	primary := newMockRoleService(nil)
	wantErr := errors.New("read index not ready")
	primary.err = wantErr
	fallback := newMockRoleService(map[string][]permission.Permission{
		"acme-reader-role": {"kv:get"},
	})

	identity := &auth.Identity{Subject: "reader", TenantID: "acme", Roles: []string{"acme-reader-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor("acme"), identity, "kv", "get")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for tenant acme; the fallback is "+
			"confined to the system tenant and must not be consulted for any other", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError: a fallback confined to the system tenant cannot "+
			"say that tenant acme's roles grant nothing", err, err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveError should carry the primary's transient error, got %v", err)
	}
}

// TestResolvePermissionSet_StillConsultsFallbackForTheSystemTenant pins the
// half of the contract that must not move: TestAuthorize_RoleResolveFallbackOnTransientError
// (rbac_test.go) already proves the system tenant is still authorized through
// the fallback; this proves the fallback is actually the thing that answered,
// not an accident of the primary succeeding.
func TestResolvePermissionSet_StillConsultsFallbackForTheSystemTenant(t *testing.T) {
	primary := newMockRoleService(nil)
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		"admin": {"admin:drain"},
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"admin"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")
	if err != nil {
		t.Fatalf("expected the fallback to authorize the system tenant, got %v", err)
	}
	if fallback.callCount.Load() != 1 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for the system tenant, want 1", fallback.callCount.Load())
	}
}

// TestResolvePermissionSet_DefinitiveRoleNotFound_NeverConsultsFallback is the
// regression for review finding X1: a DEFINITIVE answer from the primary
// (linearizable) store -- the role was deleted, or never existed -- is not a
// transient condition the StaleRead fallback exists for. Before this fix,
// resolvePermissionSet retried EVERY primary error against the fallback when
// one was configured, including this one; for the SYSTEM tenant (the one the
// fallback actually holds) a replica that had not yet caught up to the
// deletion could still answer with the just-deleted role's permissions,
// resurrecting a revoked grant. This holds even for the system tenant --
// there is no tenant for which a RoleNotFoundError is worth retrying.
func TestResolvePermissionSet_DefinitiveRoleNotFound_NeverConsultsFallback(t *testing.T) {
	primary := newMockRoleService(nil)
	notFound := &role.RoleNotFoundError{ID: "deleted-role"}
	primary.err = notFound
	fallback := newMockRoleService(map[string][]permission.Permission{
		"deleted-role": {"admin:drain"}, // stale: the deletion has not replicated here yet
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"deleted-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for a DEFINITIVE RoleNotFoundError; "+
			"it must never be consulted, even for the system tenant -- a stale replica could still "+
			"grant the just-deleted role's permissions", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
	if !errors.Is(err, notFound) {
		t.Fatalf("ResolveError should carry the primary's RoleNotFoundError, got %v", err)
	}
}

// TestResolvePermissionSet_DefinitiveValidationError_NeverConsultsFallback is
// X1's companion for a malformed request (an empty role id) rather than an
// absent role: also definitive, also never worth asking a replica about.
func TestResolvePermissionSet_DefinitiveValidationError_NeverConsultsFallback(t *testing.T) {
	primary := newMockRoleService(nil)
	validationErr := &role.ValidationError{Field: "id", Reason: "must not be empty"}
	primary.err = validationErr
	fallback := newMockRoleService(map[string][]permission.Permission{
		"bad-role": {"admin:drain"},
	})

	identity := &auth.Identity{Subject: "admin", TenantID: tenantid.System, Roles: []string{"bad-role"}}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "admin", "drain")

	if fallback.callCount.Load() != 0 {
		t.Fatalf("fallback.ResolvePermissions was called %d times for a DEFINITIVE ValidationError; "+
			"it must never be consulted", fallback.callCount.Load())
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
	if !errors.Is(err, validationErr) {
		t.Fatalf("ResolveError should carry the primary's ValidationError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Path 2: the SO deny-list and the strict-SoD Owner strip.
// ---------------------------------------------------------------------------

// TestSODenyList_StaleFallback_NeverAnswersForAnotherTenant is the regression
// for B1 (path 2): systemRoleIDs treated the fallback's "no persisted legacy
// override" as license to return the freshly-derived canonical identifier,
// even when the fallback is blind to the tenant entirely. staleMayDenote then
// could not rule out an ordinary tenant role, and the SO deny-list wrongly
// fired — a tenant user loses its roles under exactly the FIPS 140-3 AS10.03
// control that is supposed to protect against privilege, not revoke it.
//
// A REAL role store confined to the system tenant's identity namespace is
// used for the fallback (the same construction as qrdbd's bootRoleSvc,
// cmd/qrdbd/start.go), because a flat test double that answers for any tenant
// cannot reproduce this: the bug is specifically that the real store cannot
// see tenant acme's roles at all.
func TestSODenyList_StaleFallback_NeverAnswersForAnotherTenant(t *testing.T) {
	fallback := &countingRoleService{RoleServicer: newSystemConfinedRoleStore(t)}
	// The fallback holds nothing for tenant acme -- not even acme's own SO
	// role -- exactly as qrdbd's bootRoleSvc never does.

	primary := newMockRoleService(map[string][]permission.Permission{
		"acme-reader-role": {"kv:get"},
	})
	primaryErr := errors.New("read index not ready")
	primary.byNameErr = primaryErr

	identity := &auth.Identity{
		Subject:  "reader",
		TenantID: "acme",
		Roles:    []string{"acme-reader-role"}, // an ordinary, non-SO role
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor("acme"), identity, "keys", "sign")

	var denied *PermissionDeniedError
	if errors.As(err, &denied) {
		t.Fatalf("an ordinary tenant role was denied by the SO deny-list via a fallback that does not "+
			"hold tenant acme at all: %v", err)
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError: a fallback confined to the system tenant cannot "+
			"say tenant acme has no SO role", err, err)
	}
	// Proves the gate was CROSSED (or not), not merely that the outcome
	// happened to be right: systemRoleIDs' tenant check must refuse before
	// ever asking the fallback by name.
	if calls := fallback.getRoleByNameCalls.Load(); calls != 0 {
		t.Fatalf("fallback.GetRoleByName was called %d times for tenant acme; systemRoleIDs' tenant "+
			"gate must refuse before ever asking the fallback", calls)
	}
	// And the error must be the PRIMARY's -- proving systemRoleIDs' own gate
	// caught this, not staleMayDenote's defense-in-depth CrossTenantFallbackError
	// (which would mean the first gate was bypassed and the second one happened
	// to catch it).
	if !errors.Is(err, primaryErr) {
		t.Fatalf("ResolveError should carry the primary's error, got %v", err)
	}
	var crossTenant *CrossTenantFallbackError
	if errors.As(err, &crossTenant) {
		t.Fatalf("err wraps *CrossTenantFallbackError, meaning systemRoleIDs' own gate did not fire "+
			"and staleMayDenote's defense-in-depth caught it instead: %v", err)
	}
}

// TestSODenyList_StaleFallback_StillServesTheSystemTenant pins the half of
// the contract that must not move: for the tenant the fallback holds, a cold
// primary still fails over to it and the deny-list still fires on a genuine
// system-tenant SO role.
func TestSODenyList_StaleFallback_StillServesTheSystemTenant(t *testing.T) {
	fallback := newSystemConfinedRoleStore(t)
	soRole := seedBuiltInRole(t, fallback, tenantid.System, role.RoleNameSO)

	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")

	identity := &auth.Identity{
		Subject:  "so",
		TenantID: tenantid.System,
		Roles:    []string{soRole.ID},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenantid.System), identity, "keys", "sign")
	assertDeniedByTheSODenyList(t, err)
}

// TestOwnerStrip_StaleFallback_NeverAnswersForAnotherTenant is path 2's
// strict-separation-of-duties half: the same wrong "canonical, no override"
// answer from a fallback blind to the tenant would strip an ordinary tenant
// role's grants outright (every unmatched identifier reads as "may be the
// Owner role").
func TestOwnerStrip_StaleFallback_NeverAnswersForAnotherTenant(t *testing.T) {
	fallback := &countingRoleService{RoleServicer: newSystemConfinedRoleStore(t)}
	// Nothing for tenant acme is in the fallback.

	primary := newMockRoleService(map[string][]permission.Permission{
		"acme-editor-role": {"kv:put"},
	})
	primaryErr := errors.New("read index not ready")
	primary.byNameErr = primaryErr

	identity := &auth.Identity{
		Subject:  "editor",
		TenantID: "acme",
		Roles:    []string{"acme-editor-role"}, // an ordinary, non-Owner role
	}

	err := New(primary, WithRoleServiceFallback(fallback), WithStrictSeparation(true)).
		Authorize(ctxFor("acme"), identity, "kv", "put")

	if err == nil {
		t.Fatal("expected a transient failure, not a silent grant of kv:put from an unconsulted fallback")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError: a fallback confined to the system tenant cannot "+
			"say tenant acme has no Owner role", err, err)
	}
	if calls := fallback.getRoleByNameCalls.Load(); calls != 0 {
		t.Fatalf("fallback.GetRoleByName was called %d times for tenant acme; systemRoleIDs' tenant "+
			"gate must refuse before ever asking the fallback", calls)
	}
	if !errors.Is(err, primaryErr) {
		t.Fatalf("ResolveError should carry the primary's error, got %v", err)
	}
	var crossTenant *CrossTenantFallbackError
	if errors.As(err, &crossTenant) {
		t.Fatalf("err wraps *CrossTenantFallbackError, meaning systemRoleIDs' own gate did not fire "+
			"and staleMayDenote's defense-in-depth caught it instead: %v", err)
	}
}

// ---------------------------------------------------------------------------
// staleMayDenote's own defense-in-depth gate.
// ---------------------------------------------------------------------------

// TestStaleMayDenote_RefusesANonSystemTenant exercises the unexported guard
// directly: even if a caller somehow reached staleMayDenote for a non-system
// tenant, it must refuse rather than consult a fallback that cannot hold that
// tenant's roles.
func TestStaleMayDenote_RefusesANonSystemTenant(t *testing.T) {
	fallback := newMockRoleService(map[string][]permission.Permission{}).
		withRole(&role.Role{ID: "some-id", Name: role.RoleNameSO, TenantID: "acme"})
	a := New(newMockRoleService(nil), WithRoleServiceFallback(fallback))

	ctx := tenant.WithTenantID(context.Background(), "acme")
	mayDenote, err := a.staleMayDenote(ctx, "some-id", role.RoleNameSO)
	if mayDenote {
		t.Fatal("staleMayDenote answered for a non-system tenant instead of refusing")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
	var crossTenant *CrossTenantFallbackError
	if !errors.As(err, &crossTenant) {
		t.Fatalf("err = %v (%T), want it to carry *CrossTenantFallbackError", err, err)
	}
}
