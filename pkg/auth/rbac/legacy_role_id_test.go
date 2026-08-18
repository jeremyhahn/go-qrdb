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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
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
// the canonical name, under the legacy identifier.
func legacyRole(id, name string) *role.Role {
	return &role.Role{ID: id, Name: name, TenantID: "acme", System: true}
}

func TestAuthorize_OwnerVoided_WhenGrantedByLegacyIdentifier(t *testing.T) {
	// The admin was provisioned before the migration, so its Owner grant
	// carries the legacy identifier. Strict separation of duties must still
	// void it.
	svc := newMockRoleService(map[string][]permission.Permission{
		legacyOwnerIDAcme: {"kv:put"},
		legacyAdminIDAcme: {"tenants:read"},
	}).withRole(legacyRole(legacyOwnerIDAcme, role.RoleNameOwner))

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
	}).withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))

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
	primary := newMockRoleService(map[string][]permission.Permission{})
	primary.byNameErr = errors.New("read index not ready")
	fallback := newMockRoleService(map[string][]permission.Permission{
		legacySOIDAcme: {"keys:sign"},
	}).withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))

	identity := &auth.Identity{
		Subject:  "so-by-legacy-id",
		TenantID: "acme",
		Roles:    []string{legacySOIDAcme},
	}

	err := New(primary, WithRoleServiceFallback(fallback)).
		Authorize(ctxFor("acme"), identity, "keys", "sign")
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the deny-list to trigger via the fallback store, got %v", err)
	}
}

func TestSystemRoleIDs_CachedWithinTTL(t *testing.T) {
	svc := newMockRoleService(map[string][]permission.Permission{}).
		withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))
	authz := New(svc)

	first, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs: %v", err)
	}

	svc.byNameErr = errors.New("role store unavailable")
	second, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs (cached): %v", err)
	}
	if len(first) != 2 || len(second) != 2 || first[1] != second[1] || second[1] != legacySOIDAcme {
		t.Fatalf("cached lookup = %v, want the same %v including %s", second, first, legacySOIDAcme)
	}
}

func TestSystemRoleIDs_ReresolvesAfterTTL(t *testing.T) {
	// The store does NOT enforce system-role immutability — CreateRole
	// persists System: false regardless of the request — so a seeded SO role
	// can be deleted and recreated under a fresh identifier. A permanent
	// cache would keep recognising the replaced one for the process
	// lifetime and stop denying the replacement SO its crypto operations.
	svc := newMockRoleService(map[string][]permission.Permission{}).
		withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))
	authz := New(svc)
	authz.sysRoleIDs = newSystemRoleIDCache(0) // every entry is already stale

	if _, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO); err != nil {
		t.Fatalf("systemRoleIDs: %v", err)
	}

	// The SO role is recreated under a different identifier.
	const replacementSOID = "11111111-2222-8333-8444-555555555555"
	svc.withRole(legacyRole(replacementSOID, role.RoleNameSO))

	got, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs (after TTL): %v", err)
	}
	if len(got) != 2 || got[1] != replacementSOID {
		t.Fatalf("systemRoleIDs = %v, want the replacement %s", got, replacementSOID)
	}

	// And the deny-list follows it: the replacement SO is denied crypto.
	identity := &auth.Identity{
		Subject:  "so-replacement",
		TenantID: "acme",
		Roles:    []string{replacementSOID},
	}
	if err := authz.Authorize(ctxFor("acme"), identity, "keys", "sign"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expected the deny-list to trigger for the replacement SO, got %v", err)
	}
}

func TestInvalidateAll_ClearsCachedSystemRoleIDs(t *testing.T) {
	// The operational escape hatch must reach the new cache too, so a
	// reclassification does not have to wait out the TTL.
	svc := newMockRoleService(map[string][]permission.Permission{}).
		withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))
	authz := New(svc)

	if _, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO); err != nil {
		t.Fatalf("systemRoleIDs: %v", err)
	}

	const replacementSOID = "11111111-2222-8333-8444-555555555555"
	svc.withRole(legacyRole(replacementSOID, role.RoleNameSO))

	// Still the old answer until invalidated.
	cached, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs: %v", err)
	}
	if cached[1] != legacySOIDAcme {
		t.Fatalf("cached = %v, want the pre-invalidation %s", cached, legacySOIDAcme)
	}

	authz.InvalidateAll()

	got, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs (after InvalidateAll): %v", err)
	}
	if got[1] != replacementSOID {
		t.Fatalf("systemRoleIDs = %v, want the replacement %s", got, replacementSOID)
	}
}

func TestSystemRoleIDs_FailureIsNotCached(t *testing.T) {
	// A transient store error must not pin a fail-open answer.
	svc := newMockRoleService(map[string][]permission.Permission{})
	svc.byNameErr = errors.New("read index not ready")
	authz := New(svc)

	if _, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO); err == nil {
		t.Fatal("expected the lookup to fail")
	}

	svc.byNameErr = nil
	svc.withRole(legacyRole(legacySOIDAcme, role.RoleNameSO))

	got, err := authz.systemRoleIDs(ctxFor("acme"), "acme", role.RoleNameSO)
	if err != nil {
		t.Fatalf("systemRoleIDs after recovery: %v", err)
	}
	if len(got) != 2 || got[1] != legacySOIDAcme {
		t.Fatalf("systemRoleIDs = %v, want the legacy %s once the store recovered", got, legacySOIDAcme)
	}
}
