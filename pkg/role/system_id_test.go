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

package role

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// legacySystemRoleIDs are the identifiers the pre-FIPS UUIDv5 (name-based,
// SHA-1) derivation produced. They are frozen literals, NOT recomputed: the
// whole point of the migration is that SHA-1 cannot be called at all in a
// binary built with GODEBUG=fips140=only, so a test that derived them would
// panic in exactly the environment it needs to run in.
//
// The values come from uuid.NewSHA1(ns, tenantID+"/"+name) with ns =
// uuid.FromBytes(sha256("go-qrdb:role-namespace")[:16]).
var legacySystemRoleIDs = map[string]string{
	"system/so":        "b50837ff-f9c3-50a2-88c1-7f24afa5c7ab",
	"system/admin":     "f4a582da-2194-50db-8331-59de31072fa6",
	"system/operator":  "200672f9-80e1-5c7b-96aa-8ef555d843e6",
	"system/user":      "49d8a2ac-d314-595a-9bdd-d5967fc46099",
	"system/auditor":   "3c4f1478-8069-5d91-b8ca-fb1eefad2092",
	"system/custodian": "86f13e61-8249-5808-ad79-c4c8c06fd8f7",
	"system/owner":     "bf03d0b7-9353-5107-9100-2c5a107f0c9f",
	"acme/admin":       "a2dcd47f-c404-52c6-a1c2-b2ee93eb2da6",
	"acme/owner":       "4150b625-9b83-58d9-8358-2feeb1832686",
	"acme/so":          "f8a1cb85-8397-5c8d-8838-d813a9cefe43",
}

// legacyRoleID returns the frozen pre-migration identifier for a role.
func legacyRoleID(t *testing.T, tenantID, roleName string) string {
	t.Helper()
	id, ok := legacySystemRoleIDs[tenantID+"/"+roleName]
	if !ok {
		t.Fatalf("no frozen legacy ID for %s/%s", tenantID, roleName)
	}
	return id
}

// seedLegacyRole writes a role record and both secondary indexes exactly as a
// pre-migration release would have: keyed on the UUIDv5 identifier. It is the
// stand-in for an already-provisioned cluster.
func seedLegacyRole(t *testing.T, kv *memKVStore, tenantID, roleName string, perms []permission.Permission) *Role {
	t.Helper()
	id := legacyRoleID(t, tenantID, roleName)
	now := time.Now().UTC()
	r := &Role{
		ID:          id,
		Name:        roleName,
		TenantID:    tenantID,
		Permissions: perms,
		System:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal legacy role: %v", err)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, primaryKey(id), data); err != nil {
		t.Fatalf("put legacy primary: %v", err)
	}
	if err := kv.Put(ctx, tenantIndexKey(tenantID, id), []byte(id)); err != nil {
		t.Fatalf("put legacy tenant index: %v", err)
	}
	if err := kv.Put(ctx, nameIndexKey(tenantID, roleName), []byte(id)); err != nil {
		t.Fatalf("put legacy name index: %v", err)
	}
	return r
}

// ---------------------------------------------------------------------------
// Derivation: RFC 9562 §5.8 UUIDv8 over SHA-256
// ---------------------------------------------------------------------------

func TestSystemRoleID_IsUUIDv8(t *testing.T) {
	for _, name := range []string{RoleNameSO, RoleNameAdmin, RoleNameOperator, RoleNameUser, RoleNameAuditor, RoleNameCustodian, RoleNameOwner} {
		got := SystemRoleID(tenant.SystemTenantID, name)

		parsed, err := uuid.Parse(got)
		if err != nil {
			t.Fatalf("%s: not a parseable UUID: %v", name, err)
		}
		if parsed.Version() != 8 {
			t.Errorf("%s: version = %d, want 8 (RFC 9562 §5.8)", name, parsed.Version())
		}
		if parsed.Variant() != uuid.RFC4122 {
			t.Errorf("%s: variant = %v, want RFC4122", name, parsed.Variant())
		}
		if len(got) != 36 {
			t.Errorf("%s: length = %d, want 36 (shape must be unchanged)", name, len(got))
		}
	}
}

func TestSystemRoleID_NoLongerMatchesLegacyDerivation(t *testing.T) {
	// The migration changes the value; nothing may silently keep producing
	// the SHA-1 one.
	for key, legacy := range legacySystemRoleIDs {
		tenantID, roleName, ok := splitKey(key)
		if !ok {
			t.Fatalf("malformed fixture key %q", key)
		}
		if got := SystemRoleID(tenantID, roleName); got == legacy {
			t.Errorf("%s: still derives the legacy UUIDv5 identifier %s", key, legacy)
		}
	}
}

func TestSystemRoleID_DeterministicAndDistinct(t *testing.T) {
	first := SystemRoleID("acme", RoleNameAdmin)
	if second := SystemRoleID("acme", RoleNameAdmin); first != second {
		t.Errorf("not deterministic: %s != %s", first, second)
	}
	if other := SystemRoleID("acme", RoleNameOperator); first == other {
		t.Errorf("admin and operator collide on %s", first)
	}
	if otherTenant := SystemRoleID("acme2", RoleNameAdmin); first == otherTenant {
		t.Errorf("tenants acme and acme2 collide on %s", first)
	}
}

func TestSystemRoleID_EmptyTenantDefaultsToSystem(t *testing.T) {
	if got, want := SystemRoleID("", RoleNameAdmin), SystemRoleID(tenant.SystemTenantID, RoleNameAdmin); got != want {
		t.Errorf("empty tenant = %s, want %s", got, want)
	}
}

func TestSystemRoleID_LengthPrefixIsInjective(t *testing.T) {
	// Without length framing, ("a/b", "c") and ("a", "b/c") would hash the
	// same bytes and two distinct roles would share one identifier.
	if a, b := SystemRoleID("a/b", "c"), SystemRoleID("a", "b/c"); a == b {
		t.Errorf(`SystemRoleID("a/b","c") and SystemRoleID("a","b/c") both = %s`, a)
	}
}

// ---------------------------------------------------------------------------
// Dual read: identifiers persisted before the migration keep resolving
// ---------------------------------------------------------------------------

func TestGetRole_ResolvesLegacyPersistedIdentifier(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want := seedLegacyRole(t, kv, tenant.SystemTenantID, RoleNameAdmin, []permission.Permission{permission.KeysList})
	ctx := context.Background()

	got, err := svc.GetRole(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetRole(%s): %v", want.ID, err)
	}
	if got.ID != want.ID || got.Name != RoleNameAdmin {
		t.Errorf("GetRole = %+v, want ID %s name %s", got, want.ID, RoleNameAdmin)
	}

	byName, err := svc.GetRoleByName(ctx, tenant.SystemTenantID, RoleNameAdmin)
	if err != nil {
		t.Fatalf("GetRoleByName: %v", err)
	}
	if byName.ID != want.ID {
		t.Errorf("GetRoleByName ID = %s, want the persisted legacy %s", byName.ID, want.ID)
	}

	perms, err := svc.ResolvePermissions(ctx, []string{want.ID})
	if err != nil {
		t.Fatalf("ResolvePermissions: %v", err)
	}
	if len(perms) != 1 || perms[0] != permission.KeysList {
		t.Errorf("ResolvePermissions = %v, want [%s]", perms, permission.KeysList)
	}

	list, err := svc.ListRoles(ctx, &ListRolesRequest{TenantID: tenant.SystemTenantID})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if list.Total != 1 || list.Roles[0].ID != want.ID {
		t.Errorf("ListRoles = %+v, want the single legacy role %s", list, want.ID)
	}
}

func TestResolveSystemRoleID_PrefersPersistedLegacyIdentifier(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	legacy := seedLegacyRole(t, kv, "acme", RoleNameAdmin, nil).ID

	got, err := ResolveSystemRoleID(context.Background(), svc, "acme", RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if got != legacy {
		t.Errorf("ResolveSystemRoleID = %s, want the persisted %s (a grant against the derived ID would name no role)", got, legacy)
	}
}

func TestResolveSystemRoleID_FallsBackToCanonicalWhenUnseeded(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got, err := ResolveSystemRoleID(context.Background(), svc, "acme", RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if want := SystemRoleID("acme", RoleNameAdmin); got != want {
		t.Errorf("ResolveSystemRoleID = %s, want canonical %s", got, want)
	}
}

func TestResolveSystemRoleID_NilResolverYieldsCanonical(t *testing.T) {
	got, err := ResolveSystemRoleID(context.Background(), nil, "", RoleNameSO)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if want := SystemRoleID(tenant.SystemTenantID, RoleNameSO); got != want {
		t.Errorf("ResolveSystemRoleID = %s, want %s", got, want)
	}
}

func TestResolveSystemRoleID_PropagatesStoreError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	_, err := ResolveSystemRoleID(context.Background(), errNameResolver{err: sentinel}, "acme", RoleNameAdmin)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v (an unresolvable store must not read as an absent role)", err, sentinel)
	}
}

func TestSystemRoleIDs_ReturnsBothFormsWhenLegacySeeded(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	legacy := seedLegacyRole(t, kv, "acme", RoleNameOwner, nil).ID
	canonical := SystemRoleID("acme", RoleNameOwner)

	ids, err := SystemRoleIDs(context.Background(), svc, "acme", RoleNameOwner)
	if err != nil {
		t.Fatalf("SystemRoleIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != canonical || ids[1] != legacy {
		t.Fatalf("SystemRoleIDs = %v, want [%s %s]", ids, canonical, legacy)
	}
}

func TestSystemRoleIDs_CanonicalOnlyWhenUnseeded(t *testing.T) {
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ids, err := SystemRoleIDs(context.Background(), svc, "acme", RoleNameOwner)
	if err != nil {
		t.Fatalf("SystemRoleIDs: %v", err)
	}
	if want := SystemRoleID("acme", RoleNameOwner); len(ids) != 1 || ids[0] != want {
		t.Errorf("SystemRoleIDs = %v, want [%s]", ids, want)
	}
}

func TestSystemRoleIDs_PropagatesStoreError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	if _, err := SystemRoleIDs(context.Background(), errNameResolver{err: sentinel}, "acme", RoleNameSO); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// Seeding onto an already-provisioned cluster
// ---------------------------------------------------------------------------

func TestBuiltInRoles_SeedingKeepsLegacyRecord(t *testing.T) {
	// Re-seeding a cluster provisioned before the migration must not create a
	// second Admin role under the canonical identifier: the name index makes
	// creation fail, the legacy record survives, and the resolved grant still
	// points at it.
	kv := newMemKVStore()
	svc, err := NewStore(kv)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	legacy := seedLegacyRole(t, kv, tenant.SystemTenantID, RoleNameAdmin, []permission.Permission{permission.KeysList}).ID
	ctx := context.Background()

	for _, r := range BuiltInRoles(tenant.SystemTenantID) {
		_, createErr := svc.CreateRole(ctx, &CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		var nameExists *RoleNameExistsError
		if createErr != nil && !errors.As(createErr, &nameExists) {
			t.Fatalf("seed %s: %v", r.Name, createErr)
		}
	}

	admin, err := svc.GetRoleByName(ctx, tenant.SystemTenantID, RoleNameAdmin)
	if err != nil {
		t.Fatalf("GetRoleByName after re-seed: %v", err)
	}
	if admin.ID != legacy {
		t.Errorf("admin ID = %s, want the surviving legacy %s", admin.ID, legacy)
	}
	if _, err := svc.GetRole(ctx, SystemRoleID(tenant.SystemTenantID, RoleNameAdmin)); err == nil {
		t.Error("a duplicate Admin role was created under the canonical identifier")
	}

	resolved, err := ResolveSystemRoleID(ctx, svc, tenant.SystemTenantID, RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if resolved != legacy {
		t.Errorf("resolved grant = %s, want %s", resolved, legacy)
	}
}

// ---------------------------------------------------------------------------
// Resolver contract violations
// ---------------------------------------------------------------------------

func TestResolveSystemRoleID_NilRoleWithNilError_IsRefused(t *testing.T) {
	// A resolver that reports neither a role nor an error has broken its
	// contract. Reading that as "no such role" would hand back the canonical
	// identifier, which on a legacy-seeded cluster names no record: a grant
	// written against it confers nothing, and a control matching against it
	// stops recognising the grant it exists to void.
	ctx := context.Background()

	got, err := ResolveSystemRoleID(ctx, nilNameResolver{}, "acme", RoleNameAdmin)
	if err == nil {
		t.Fatalf("ResolveSystemRoleID = %q, want an error for a contract-violating resolver", got)
	}
	var invalid *InvalidResolverResultError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want *InvalidResolverResultError", err, err)
	}
	if invalid.Name != RoleNameAdmin || invalid.TenantID != "acme" {
		t.Errorf("error names %s in %s, want %s in acme", invalid.Name, invalid.TenantID, RoleNameAdmin)
	}
	if got != "" {
		t.Errorf("ResolveSystemRoleID = %q, want an empty identifier alongside the error", got)
	}
}

func TestSystemRoleIDs_NilRoleWithNilError_IsRefused(t *testing.T) {
	// SystemRoleIDs feeds the SO deny-list and the strict-SoD Owner strip, so
	// it must not answer with a partial set when the store cannot be trusted
	// to have been consulted.
	ids, err := SystemRoleIDs(context.Background(), nilNameResolver{}, "acme", RoleNameSO)
	if err == nil {
		t.Fatalf("SystemRoleIDs = %v, want an error for a contract-violating resolver", ids)
	}
	var invalid *InvalidResolverResultError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v (%T), want *InvalidResolverResultError", err, err)
	}
	if ids != nil {
		t.Errorf("SystemRoleIDs = %v, want no identifiers alongside the error", ids)
	}
}

func TestInvalidResolverResultError_Message(t *testing.T) {
	err := &InvalidResolverResultError{Name: RoleNameSO, TenantID: "acme"}
	msg := err.Error()
	if !strings.Contains(msg, RoleNameSO) || !strings.Contains(msg, "acme") {
		t.Errorf("Error() = %q, want it to name both the role and the tenant", msg)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nilNameResolver is a NameResolver that breaks its contract by reporting
// neither a role nor an error.
type nilNameResolver struct{}

func (nilNameResolver) GetRoleByName(_ context.Context, _, _ string) (*Role, error) {
	return nil, nil
}

// errNameResolver is a NameResolver whose every lookup fails.
type errNameResolver struct{ err error }

func (e errNameResolver) GetRoleByName(_ context.Context, _, _ string) (*Role, error) {
	return nil, e.err
}

// splitKey splits a "tenantID/roleName" fixture key on its single separator.
func splitKey(key string) (tenantID, roleName string, ok bool) {
	for i := range key {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}
