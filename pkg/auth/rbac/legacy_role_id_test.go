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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// System role identifiers moved from UUIDv5 (SHA-1) to UUIDv8 (SHA-256) for
// FIPS 140-3. A cluster provisioned before that keeps its roles — and every
// user record's RoleIDs — under the legacy identifiers, so the two controls
// that recognise a role BY IDENTIFIER must keep recognising it. Matching only
// the freshly derived identifier would fail open: the SO deny-list and the
// strict-SoD Owner strip both exist to void a grant.
//
// The literals below are frozen samples of that legacy derivation, not
// recomputed: SHA-1 panics outright under GODEBUG=fips140=only, which is the
// build these tests must pass in.
const (
	legacyOwnerIDAcme = "4150b625-9b83-58d9-8358-2feeb1832686" // acme/owner
	legacySOIDAcme    = "f8a1cb85-8397-5c8d-8838-d813a9cefe43" // acme/so
	legacyAdminIDAcme = "a2dcd47f-c404-52c6-a1c2-b2ee93eb2da6" // acme/admin
)

// legacyRole returns a role record as a pre-migration release persisted it:
// the canonical name, under the legacy identifier, in tenantID.
func legacyRole(tenantID, id, name string) *role.Role {
	return &role.Role{ID: id, Name: name, TenantID: tenantID, System: true}
}

func TestAuthorize_OwnerVoided_WhenGrantedByLegacyIdentifier(t *testing.T) {
	// The admin was provisioned before the migration, so its Owner grant
	// carries the legacy identifier. Strict separation of duties must still
	// void it.
	svc := newMockRoleService(map[string][]permission.Permission{
		legacyOwnerIDAcme: {"kv:put"},
		legacyAdminIDAcme: {"tenants:read"},
	}).withRole(legacyRole("acme", legacyOwnerIDAcme, role.RoleNameOwner))

	identity := &auth.Identity{
		Subject:  "admin",
		TenantID: "acme",
		Roles:    []string{legacyAdminIDAcme, legacyOwnerIDAcme},
	}

	// Standard posture: the legacy Owner grant is honored.
	if err := New(svc).Authorize(ctxFor("acme"), identity, "kv", "put"); err != nil {
		t.Fatalf("standard posture: expected kv:put allowed via legacy Owner grant, got %v", err)
	}

	// Strict posture: it is voided, exactly as a canonical grant would be.
	err := New(svc, WithStrictSeparation(true)).Authorize(ctxFor("acme"), identity, "kv", "put")
	if err == nil {
		t.Fatal("strict posture: legacy Owner grant survived the strict-SoD strip")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorize_SODenyList_WhenGrantedByLegacyIdentifier(t *testing.T) {
	// FIPS 140-3 AS10.03: an SO holding the pre-migration identifier is still
	// denied every cryptographic operation.
	svc := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme: {"keys:sign"},
	}).withRole(legacyRole("acme", legacySOIDAcme, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: "acme",
		Roles:    []string{legacySOIDAcme},
	}

	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("SO deny-list did not trigger for a legacy SO identifier")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorize_CanonicalIdentifiersStillRecognised(t *testing.T) {
	// The legacy lookup must not displace the canonical match: a freshly
	// provisioned cluster has no legacy record at all.
	canonicalSO := role.SystemRoleID("acme", role.RoleNameSO)
	svc := newMockRoleService(map[string][]permission.Permission{
		canonicalSO: {"keys:sign"},
	})

	identity := &auth.Identity{
		Subject:  "so",
		TenantID: "acme",
		Roles:    []string{canonicalSO},
	}

	if err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the SO deny-list to trigger on the canonical identifier, got %v", err)
	}
}

func TestAuthorize_SODenyList_FailsClosedWhenRoleStoreUnavailable(t *testing.T) {
	// The deny-list decides whether to VOID a grant, so an unresolvable role
	// store must deny rather than read as "no legacy identifier".
	svc := newMockRoleService(map[string][]permission.Permission{})
	svc.byNameErr = errors.New("role store unavailable")

	identity := &auth.Identity{
		Subject:  "someone",
		TenantID: "acme",
		Roles:    []string{"reader"},
	}

	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("expected denial when the legacy-identifier lookup fails")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResolveError, got %T (%v)", err, err)
	}
}

func TestAuthorize_OwnerStrip_FailsClosedWhenRoleStoreUnavailable(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{
		"reader": {"kv:get"},
	})
	svc.byNameErr = errors.New("role store unavailable")

	identity := &auth.Identity{
		Subject:  "someone",
		TenantID: "acme",
		Roles:    []string{"reader"},
	}

	// Standard posture performs no lookup, so it is unaffected.
	if err := New(svc).Authorize(ctxFor("acme"), identity, "kv", "get"); err != nil {
		t.Fatalf("standard posture: expected kv:get allowed, got %v", err)
	}

	err := New(svc, WithStrictSeparation(true)).Authorize(ctxFor("acme"), identity, "kv", "get")
	if err == nil {
		t.Fatal("strict posture: expected denial when the Owner-identifier lookup fails")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResolveError, got %T (%v)", err, err)
	}
}

func TestAuthorize_LegacyIdentifierLookup_UsesFallbackStore(t *testing.T) {
	// A cold or draining node rejects linearizable reads; the StaleRead
	// fallback keeps the deny-list working rather than failing the request.
	// The fallback is confined to the system tenant (see rolesFallback's doc
	// comment), so this identity must be the system tenant's too — a tenant
	// fallback never sees is covered separately (role_fallback_scope_test.go).
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		legacySOIDSystem: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDSystem, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{legacySOIDSystem},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the deny-list to trigger via the fallback store, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Role replacement: there must be no window
// ---------------------------------------------------------------------------

func TestAuthorize_ReplacementSO_IsDeniedImmediately(t *testing.T) {
	// The store does not keep seeded system roles immutable (CreateRole
	// persists System: false), so an SO role can be deleted and recreated
	// under a generated identifier. The deny-list must recognise the
	// replacement on the very next authorization decision — there is no
	// interval in which the replacement SO reaches a cryptographic
	// operation.
	const replacementSOID = "99999999-8888-8777-8666-555555555555"
	svc := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme:  {"keys:sign"},
		replacementSOID: {"keys:sign"},
	}).withRole(legacyRole("acme", legacySOIDAcme, role.RoleNameSO))
	authz := New(svc)

	// A decision against the pre-replacement store, so any cache that might
	// exist is warm.
	preIdentity := &auth.Identity{Subject: "so", TenantID: "acme", Roles: []string{legacySOIDAcme}}
	if err := authz.Authorize(ctxFor("acme"), preIdentity, "keys", "sign"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the original SO to be denied, got %v", err)
	}

	// The SO role is now recreated under a different identifier.
	svc.withRole(legacyRole("acme", replacementSOID, role.RoleNameSO))

	identity := &auth.Identity{Subject: "so-replacement", TenantID: "acme", Roles: []string{replacementSOID}}
	if err := authz.Authorize(ctxFor("acme"), identity, "keys", "sign"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("replacement SO bypassed the deny-list: %v", err)
	}
}

func TestAuthorize_ReplacementOwner_IsStrippedImmediately(t *testing.T) {
	// Same for the strict-separation-of-duties Owner strip: a replacement
	// Owner grant must be voided on the next decision, not after an interval.
	const replacementOwnerID = "77777777-6666-8555-8444-333333333333"
	svc := newMockRoleService(map[string][]permission.Permission{
		legacyOwnerIDAcme:  {"kv:put"},
		replacementOwnerID: {"kv:put"},
		legacyAdminIDAcme:  {"tenants:read"},
	}).withRole(legacyRole("acme", legacyOwnerIDAcme, role.RoleNameOwner))
	authz := New(svc, WithStrictSeparation(true))

	preIdentity := &auth.Identity{
		Subject:  "admin",
		TenantID: "acme",
		Roles:    []string{legacyAdminIDAcme, legacyOwnerIDAcme},
	}
	if err := authz.Authorize(ctxFor("acme"), preIdentity, "kv", "put"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the original Owner grant to be voided, got %v", err)
	}

	svc.withRole(legacyRole("acme", replacementOwnerID, role.RoleNameOwner))

	identity := &auth.Identity{
		Subject:  "admin-replacement",
		TenantID: "acme",
		Roles:    []string{legacyAdminIDAcme, replacementOwnerID},
	}
	if err := authz.Authorize(ctxFor("acme"), identity, "kv", "put"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("replacement Owner grant survived the strict-SoD strip: %v", err)
	}
}

func TestAuthorize_CanonicalSO_IsDeniedWithoutConsultingTheStore(t *testing.T) {
	// An identity classified by the canonical identifier is classified with
	// no store lookup at all, so a broken role store cannot suppress the
	// deny-list for the roles it covers.
	canonicalSO := role.SystemRoleID("acme", role.RoleNameSO)
	svc := newMockRoleService(map[string][]permission.Permission{canonicalSO: {"keys:sign"}})
	svc.byNameErr = errors.New("role store unavailable")

	identity := &auth.Identity{Subject: "so", TenantID: "acme", Roles: []string{canonicalSO}}
	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")

	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v (%T), want *PermissionDeniedError from the deny-list", err, err)
	}
}

func TestAuthorize_SODenyList_SkipsTheLookupForAnIdentityWithNoRoles(t *testing.T) {
	// Nothing to classify means nothing to look up: a roleless identity must
	// not be denied merely because the role store is unreachable.
	svc := newMockRoleService(map[string][]permission.Permission{})
	svc.byNameErr = errors.New("role store unavailable")

	identity := &auth.Identity{Subject: "nobody", TenantID: "acme"}
	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")

	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v (%T), want a plain permission denial", err, err)
	}
	var resolveErr *ResolveError
	if errors.As(err, &resolveErr) {
		t.Fatal("a roleless identity triggered a role-store lookup")
	}
}

// ---------------------------------------------------------------------------
// Resolver contract violation: (nil, nil) is not "no legacy identifier"
// ---------------------------------------------------------------------------

func TestAuthorize_SODenyList_FailsClosedWhenResolverReturnsNilNil(t *testing.T) {
	// A resolver that reports neither a role nor an error has broken its
	// contract. Reading that as "the role does not exist" would drop the
	// persisted identifier out of the answer, and on a legacy-seeded tenant
	// that identifier is exactly the one the deny-list must recognise — the
	// SO would reach the cryptographic operation the control exists to
	// refuse. Deny instead.
	svc := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme: {"keys:sign"},
	})
	svc.byNameNil = true

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: "acme",
		Roles:    []string{legacySOIDAcme},
	}

	err := New(svc).Authorize(ctxFor("acme"), identity, "keys", "sign")
	if err == nil {
		t.Fatal("a contract-violating resolver let a legacy SO reach keys:sign")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError — the request is refused, not answered", err, err)
	}
	var invalid *role.InvalidResolverResultError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want it to carry *role.InvalidResolverResultError", err, err)
	}
}

func TestAuthorize_OwnerStrip_FailsClosedWhenResolverReturnsNilNil(t *testing.T) {
	// Same for the strict-SoD Owner strip: an unresolvable Owner identifier
	// must not be read as "there is no Owner role", which would leave a
	// legacy Owner grant live under the posture that exists to void it.
	svc := newMockRoleService(map[string][]permission.Permission{
		legacyOwnerIDAcme: {"kv:put"},
		legacyAdminIDAcme: {"tenants:read"},
	})
	svc.byNameNil = true

	identity := &auth.Identity{
		Subject:  "admin",
		TenantID: "acme",
		Roles:    []string{legacyAdminIDAcme, legacyOwnerIDAcme},
	}

	err := New(svc, WithStrictSeparation(true)).Authorize(ctxFor("acme"), identity, "kv", "put")
	if err == nil {
		t.Fatal("a contract-violating resolver let a legacy Owner grant survive strict SoD")
	}
	var invalid *role.InvalidResolverResultError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want it to carry *role.InvalidResolverResultError", err, err)
	}
}

func TestAuthorize_ResolverContractViolation_DoesNotFallBackToTheStaleReplica(t *testing.T) {
	// The StaleRead fallback exists for a cold or draining node, not for a
	// broken resolver. A primary that violates the contract is a defect;
	// answering from the replica would hide it on a path that voids grants.
	primary := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme: {"keys:sign"},
	})
	primary.byNameNil = true
	fallback := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDAcme, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: "acme",
		Roles:    []string{legacySOIDAcme},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor("acme"), identity, "keys", "sign")
	var invalid *role.InvalidResolverResultError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want the contract violation surfaced rather than masked", err, err)
	}
}

func TestAuthorize_FallbackResolverReturningNilNil_FailsClosed(t *testing.T) {
	// The primary is unavailable, which is the case the fallback exists for,
	// but the fallback then breaks the contract. There is no resolved answer
	// either way, so the deny-list denies. The fallback is confined to the
	// system tenant (see rolesFallback's doc comment), so it must actually be
	// CONSULTED for this to exercise the fallback's own contract violation
	// rather than the tenant gate that refuses to consult it at all.
	primary := newMockRoleService(map[string][]permission.Permission{
		legacySOIDSystem: {"keys:sign"},
	})
	primary.byNameErr = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		legacySOIDSystem: {"keys:sign"},
	})
	fallback.byNameNil = true

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{legacySOIDSystem},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	if err == nil {
		t.Fatal("a contract-violating fallback let a legacy SO reach keys:sign")
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *ResolveError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Role replacement seen through the StaleRead replica
// ---------------------------------------------------------------------------
//
// The fallback keeps a cold or draining node authorizing, but it can be behind
// the very mutation the grant-voiding controls exist to catch: a system role
// deleted and recreated under a generated identifier. A stale name lookup then
// answers with the OLD identifier, and an identity carrying the new one matches
// nothing. "Not in the stale set" must therefore not be read as innocence.

// replacementSOStale is the identifier an SO role recreated after the replica's
// snapshot carries. The replica maps the name to the legacy identifier instead.
const (
	replacementSOStale    = "9f2c1d64-0000-8000-8000-aaaaaaaaaaaa"
	replacementOwnerStale = "9f2c1d64-0000-8000-8000-bbbbbbbbbbbb"
	unrelatedRoleID       = "9f2c1d64-0000-8000-8000-cccccccccccc"
)

func TestAuthorize_ReplacementSO_StaleFallbackDoesNotClearTheDenyList(t *testing.T) {
	// Primary unavailable (cold ReadIndex), replica behind the replacement:
	// it still maps "so" to the legacy identifier and has no record at all
	// for the replacement. The deny-list must not clear the grant on that.
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		replacementSOStale: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDSystem, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "replacement-so",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{replacementSOStale},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	assertDeniedByTheSODenyList(t, err)
}

// assertDeniedByTheSODenyList checks the denial came from AS10.03 rather than
// from an empty permission set, so a test cannot pass because the identity
// happened to resolve no permissions at all.
func assertDeniedByTheSODenyList(t *testing.T, err error) {
	t.Helper()
	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected *PermissionDeniedError, got %T (%v)", err, err)
	}
	if !strings.Contains(denied.Reason, "AS10.03") {
		t.Fatalf("denied for %q, want the SO deny-list — the replacement SO was not recognised", denied.Reason)
	}
}

func TestAuthorize_ReplacementSO_StaleFallbackWithRecordButNoNameIndex(t *testing.T) {
	// The replica has replicated the replacement role's RECORD but not the
	// name index entry that points at it, so the name lookup still answers
	// with the legacy identifier. Resolving from the ID side finds a record
	// named "so", which the name lookup could not see.
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		replacementSOStale: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDSystem, role.RoleNameSO)).
		withRecordOnly(legacyRole(tenant.SystemTenantID, replacementSOStale, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "replacement-so",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{replacementSOStale},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	assertDeniedByTheSODenyList(t, err)
}

func TestAuthorize_ReplacementOwner_StaleFallbackStillStripsTheGrant(t *testing.T) {
	// Same shape for the strict-SoD Owner strip: an Owner grant carrying an
	// identifier the replica cannot account for must not survive the posture
	// that exists to void it.
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		replacementOwnerStale: {"kv:put"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacyOwnerIDSystem, role.RoleNameOwner))

	identity := &auth.Identity{
		Subject:  "replacement-owner",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{replacementOwnerStale},
	}

	// Standard posture: the grant is honored, through the fallback.
	if err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "kv", "put"); err != nil {
		t.Fatalf("standard posture: expected kv:put allowed, got %v", err)
	}

	err := New(primary, WithRoleServiceFallback(fallback), WithStrictSeparation(true)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "kv", "put")
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("strict posture: replacement Owner grant survived a stale lookup, got %T (%v)", err, err)
	}
}

// TestAuthorize_StaleFallback_SparesARoleItCanAccountFor is the availability
// half of the rule. Being conservative about identifiers the replica cannot
// explain must not turn a cold node into one that denies every request: a role
// the replica knows, and knows is not the system role, is left alone.
func TestAuthorize_StaleFallback_SparesARoleItCanAccountFor(t *testing.T) {
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	primary.err = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		unrelatedRoleID: {"kv:put"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacyOwnerIDSystem, role.RoleNameOwner)).
		withRole(&role.Role{ID: unrelatedRoleID, Name: "editor", TenantID: tenant.SystemTenantID})

	identity := &auth.Identity{
		Subject:  "editor",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{unrelatedRoleID},
	}

	if err := New(primary, WithRoleServiceFallback(fallback), WithStrictSeparation(true)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "kv", "put"); err != nil {
		t.Fatalf("strict posture: an ordinary role was stripped on a cold node: %v", err)
	}

	// And the SO deny-list does not deny it either.
	soIdentity := &auth.Identity{
		Subject:  "editor",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{unrelatedRoleID},
	}
	fallback.perms[unrelatedRoleID] = []permission.Permission{"keys:sign"}
	if err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), soIdentity, "keys", "sign"); err != nil {
		t.Fatalf("deny-list denied a role the replica accounts for: %v", err)
	}
}

func TestAuthorize_StaleFallback_RoleLookupFailure_Denies(t *testing.T) {
	// The replica cannot say what the identifier is. That is not innocence.
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		replacementSOStale: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDSystem, role.RoleNameSO))
	fallback.byIDErr = errors.New("replica read failed")

	identity := &auth.Identity{
		Subject:  "replacement-so",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{replacementSOStale},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResolveError, got %T (%v)", err, err)
	}
}

func TestAuthorize_StaleFallback_RoleLookupNilNil_Denies(t *testing.T) {
	// Neither a role nor an error is a broken contract, not an absence.
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		replacementSOStale: {"keys:sign"},
	}).withRole(legacyRole(tenant.SystemTenantID, legacySOIDSystem, role.RoleNameSO))
	fallback.byIDNil = true

	identity := &auth.Identity{
		Subject:  "replacement-so",
		TenantID: tenant.SystemTenantID,
		Roles:    []string{replacementSOStale},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor(tenant.SystemTenantID), identity, "keys", "sign")
	var invalid *InvalidRoleLookupError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidRoleLookupError, got %T (%v)", err, err)
	}
	if invalid.RoleID != replacementSOStale {
		t.Errorf("RoleID = %q, want %q", invalid.RoleID, replacementSOStale)
	}
	if !strings.Contains(invalid.Error(), replacementSOStale) {
		t.Errorf("Error() = %q, want it to name the identifier", invalid.Error())
	}
}
