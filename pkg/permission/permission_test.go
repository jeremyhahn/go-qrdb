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

package permission

import (
	"errors"
	"sort"
	"testing"
)

// ---------------------------------------------------------------------------
// Permission.String
// ---------------------------------------------------------------------------

func TestPermission_String(t *testing.T) {
	p := Permission("kv:put")
	if p.String() != "kv:put" {
		t.Fatalf("String(): got %q, want %q", p.String(), "kv:put")
	}
}

func TestPermission_String_Empty(t *testing.T) {
	p := Permission("")
	if p.String() != "" {
		t.Fatalf("String(): got %q, want %q", p.String(), "")
	}
}

// ---------------------------------------------------------------------------
// Resource
// ---------------------------------------------------------------------------

func TestResource(t *testing.T) {
	tests := []struct {
		name string
		perm Permission
		want string
	}{
		{"simple", "kv:put", "kv"},
		{"deep_action", "admin:add_member", "admin"},
		{"no_colon", "invalid", "invalid"},
		{"empty_resource", ":action", ""},
		{"multiple_colons", "a:b:c", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resource(tt.perm)
			if got != tt.want {
				t.Fatalf("Resource(%q): got %q, want %q", tt.perm, got, tt.want)
			}
		})
	}
}

func TestResource_AllConstants(t *testing.T) {
	// Every registered permission must have a non-empty resource.
	for _, p := range All() {
		if Resource(p) == "" {
			t.Fatalf("Resource(%q) returned empty string", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Action
// ---------------------------------------------------------------------------

func TestAction(t *testing.T) {
	tests := []struct {
		name string
		perm Permission
		want string
	}{
		{"simple", "kv:put", "put"},
		{"underscore", "mvcc:put_with_lease", "put_with_lease"},
		{"no_colon", "invalid", ""},
		{"empty_action", "resource:", ""},
		{"multiple_colons", "a:b:c", "b:c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Action(tt.perm)
			if got != tt.want {
				t.Fatalf("Action(%q): got %q, want %q", tt.perm, got, tt.want)
			}
		})
	}
}

func TestAction_AllConstants(t *testing.T) {
	// Every registered permission must have a non-empty action.
	for _, p := range All() {
		if Action(p) == "" {
			t.Fatalf("Action(%q) returned empty string", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

func TestParse_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Permission
	}{
		{"kv_put", "kv:put", KVPut},
		{"admin_health", "admin:health", AdminHealth},
		{"mvcc_put_with_lease", "mvcc:put_with_lease", MVCCPutWithLease},
		{"custom", "custom:action", Permission("custom:action")},
		{"multiple_colons", "a:b:c", Permission("a:b:c")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q): got %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"no_colon", "kvput"},
		{"empty_string", ""},
		{"empty_resource", ":action"},
		{"empty_action", "resource:"},
		{"colon_only", ":"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err == nil {
				t.Fatalf("Parse(%q): expected error, got nil", tt.input)
			}
			var target *InvalidPermissionError
			if !errors.As(err, &target) {
				t.Fatalf("Parse(%q): expected *InvalidPermissionError, got %T", tt.input, err)
			}
			if target.Value != tt.input {
				t.Fatalf("InvalidPermissionError.Value: got %q, want %q", target.Value, tt.input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Register / All / IsRegistered
// ---------------------------------------------------------------------------

func TestAll_Sorted(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned empty slice")
	}
	if !sort.SliceIsSorted(all, func(i, j int) bool {
		return all[i] < all[j]
	}) {
		t.Fatal("All() returned unsorted slice")
	}
}

func TestAll_NoDuplicates(t *testing.T) {
	all := All()
	seen := make(map[Permission]struct{}, len(all))
	for _, p := range all {
		if _, ok := seen[p]; ok {
			t.Fatalf("All() contains duplicate: %q", p)
		}
		seen[p] = struct{}{}
	}
}

func TestIsRegistered_Exists(t *testing.T) {
	tests := []Permission{
		KVPut, KVGet, KVDelete,
		AdminHealth, AdminStatus,
		MVCCPutWithLease, MVCCCompact,
		TransactionExecute,
		BatchExecute,
		WatchWatch, WatchWatchPrefix,
		LeaseGrant, LeaseRevoke,
		IndexCreate, IndexDrop,
		TSWrite, TSQuery,
		VectorUpsert, VectorSearchSimilar,
		UsersCreate, RolesCreate,
		CertsGenerate, SealBootstrap,
		TenantsCreate,
	}
	for _, p := range tests {
		t.Run(string(p), func(t *testing.T) {
			if !IsRegistered(p) {
				t.Fatalf("IsRegistered(%q): expected true", p)
			}
		})
	}
}

func TestIsRegistered_NotExists(t *testing.T) {
	tests := []Permission{
		"nonexistent:permission",
		"kv:destroy",
		"admin:reboot",
	}
	for _, p := range tests {
		t.Run(string(p), func(t *testing.T) {
			if IsRegistered(p) {
				t.Fatalf("IsRegistered(%q): expected false", p)
			}
		})
	}
}

// TestIdentityGated_NotRegistered guards the identity-gated sentinel's core
// invariant: it must never be registered, so it can never be granted to a
// role, validated as a grant, or surfaced in the permission catalog. Claim
// routes mapped to IdentityGated are authorized at the handler layer
// (registry membership + thumbprint + signature/factor verification), not by
// the RBAC policy decision point.
func TestIdentityGated_NotRegistered(t *testing.T) {
	if IsRegistered(IdentityGated) {
		t.Fatalf("IsRegistered(%q): the identity-gated sentinel must never be registered", IdentityGated)
	}
}

// TestIdentityGated_NotInAll verifies the sentinel never appears in the
// grantable-permission catalog returned by All().
func TestIdentityGated_NotInAll(t *testing.T) {
	for _, p := range All() {
		if p == IdentityGated {
			t.Fatalf("All() contains the identity-gated sentinel %q; it must never be grantable", IdentityGated)
		}
	}
}

// TestIdentityGated_NotInByResource verifies the sentinel never appears in
// the resource-grouped catalog used by the UI.
func TestIdentityGated_NotInByResource(t *testing.T) {
	for _, p := range ByResource(Resource(IdentityGated)) {
		if p == IdentityGated {
			t.Fatalf("ByResource(%q) contains the identity-gated sentinel %q", Resource(IdentityGated), IdentityGated)
		}
	}
}

func TestRegister_Custom(t *testing.T) {
	custom := Permission("test_custom:do_thing")

	// Precondition: not registered.
	if IsRegistered(custom) {
		t.Fatal("precondition: custom permission should not be registered")
	}

	Register(custom)
	defer func() {
		// Clean up: remove from registry so other tests are not affected.
		mu.Lock()
		delete(registry, custom)
		mu.Unlock()
	}()

	if !IsRegistered(custom) {
		t.Fatal("Register: custom permission should be registered after Register()")
	}

	// Verify it appears in All().
	found := false
	for _, p := range All() {
		if p == custom {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Register: custom permission should appear in All()")
	}
}

func TestRegister_Idempotent(t *testing.T) {
	before := len(All())
	Register(KVPut, KVGet) // Already registered.
	after := len(All())
	if before != after {
		t.Fatalf("Register(existing): expected count %d, got %d", before, after)
	}
}

// ---------------------------------------------------------------------------
// All – spot-check known constants
// ---------------------------------------------------------------------------

func TestAll_ContainsAllConstants(t *testing.T) {
	expected := []Permission{
		KVPut, KVGet, KVDelete, KVList, KVExists, KVRange,
		MVCCPutWithLease, MVCCGetRevision, MVCCGetAtRevision, MVCCRange, MVCCCompact, MVCCCurrentRevision,
		TransactionExecute,
		BatchExecute,
		WatchWatch, WatchWatchPrefix,
		LeaseGrant, LeaseRevoke, LeaseKeepAlive, LeaseTTL, LeaseList,
		IndexCreate, IndexDrop, IndexQuery, IndexScan,
		TSWrite, TSWriteBatch, TSQuery, TSAggregate,
		VectorNamespaceCreate, VectorNamespaceDelete, VectorNamespaceList, VectorNamespaceInfo,
		VectorUpsert, VectorGet, VectorDelete, VectorBatchUpsert, VectorBatchDelete, VectorList,
		VectorUpdateMetadata, VectorSearchSimilar, VectorSearchByMetadata, VectorHybridSearch,
		VectorSearchByID, VectorRebuildIndex, VectorGetRebuildProg, VectorGetIndexStats,
		VectorCompact, VectorGetCompactProg,
		AdminHealth, AdminStatus, AdminPeers, AdminAddMember, AdminRemoveMember,
		AdminSnapshot, AdminDrain, AdminCapabilities,
		UsersCreate, UsersRead, UsersUpdate, UsersDelete, UsersList, UsersEnable, UsersDisable,
		RolesCreate, RolesRead, RolesUpdate, RolesDelete, RolesList, RolesAssign, RolesUnassign,
		CertsGenerate, CertsRevoke, CertsList,
		SealBootstrap, SealUnseal, SealSeal, SealStatus,
		TenantsCreate, TenantsRead, TenantsUpdate, TenantsDelete, TenantsList,
	}
	allSet := make(map[Permission]struct{}, len(All()))
	for _, p := range All() {
		allSet[p] = struct{}{}
	}
	for _, p := range expected {
		if _, ok := allSet[p]; !ok {
			t.Fatalf("All() missing expected permission: %q", p)
		}
	}
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

func TestCategories_NonEmpty(t *testing.T) {
	cats := Categories()
	if len(cats) == 0 {
		t.Fatal("Categories() returned empty slice")
	}
}

func TestCategories_AllGrouped(t *testing.T) {
	for _, cat := range Categories() {
		if cat.Name == "" {
			t.Fatal("category has empty Name")
		}
		if cat.Group == "" {
			t.Fatalf("category %q has empty Group", cat.Name)
		}
		if len(cat.Permissions) == 0 {
			t.Fatalf("category %q has no permissions", cat.Name)
		}
	}
}

func TestCategories_CoverAllRegistered(t *testing.T) {
	// Every registered permission should appear in at least one category.
	catPerms := make(map[Permission]struct{})
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			catPerms[p] = struct{}{}
		}
	}
	for _, p := range All() {
		if _, ok := catPerms[p]; !ok {
			t.Fatalf("registered permission %q not found in any category", p)
		}
	}
}

func TestCategories_NoDuplicatePermissions(t *testing.T) {
	seen := make(map[Permission]string)
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			if prev, ok := seen[p]; ok {
				t.Fatalf("permission %q appears in both %q and %q", p, prev, cat.Name)
			}
			seen[p] = cat.Name
		}
	}
}

func TestCategories_AllPermissionsRegistered(t *testing.T) {
	// Every permission in a category should be registered.
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			if !IsRegistered(p) {
				t.Fatalf("category %q contains unregistered permission %q", cat.Name, p)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// ByResource
// ---------------------------------------------------------------------------

func TestByResource(t *testing.T) {
	kvPerms := ByResource("kv")
	if len(kvPerms) != 6 {
		t.Fatalf("ByResource(\"kv\"): expected 6 permissions, got %d", len(kvPerms))
	}
	expected := map[Permission]struct{}{
		KVPut: {}, KVGet: {}, KVDelete: {},
		KVList: {}, KVExists: {}, KVRange: {},
	}
	for _, p := range kvPerms {
		if _, ok := expected[p]; !ok {
			t.Fatalf("ByResource(\"kv\"): unexpected permission %q", p)
		}
	}
}

func TestByResource_Sorted(t *testing.T) {
	perms := ByResource("admin")
	if !sort.SliceIsSorted(perms, func(i, j int) bool {
		return perms[i] < perms[j]
	}) {
		t.Fatal("ByResource(\"admin\") returned unsorted slice")
	}
}

func TestByResource_NoMatch(t *testing.T) {
	perms := ByResource("nonexistent_resource")
	if len(perms) != 0 {
		t.Fatalf("ByResource(\"nonexistent_resource\"): expected 0, got %d", len(perms))
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestInvalidPermissionError_Error(t *testing.T) {
	err := &InvalidPermissionError{Value: "bad"}
	want := "permission: invalid format: bad (expected resource:action)"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestInvalidPermissionError_As(t *testing.T) {
	var err error = &InvalidPermissionError{Value: "test"}
	var target *InvalidPermissionError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *InvalidPermissionError")
	}
	if target.Value != "test" {
		t.Fatalf("Value: got %q, want %q", target.Value, "test")
	}
}

func TestUnregisteredPermissionError_Error(t *testing.T) {
	err := &UnregisteredPermissionError{Permission: "foo:bar"}
	want := "permission: not registered: foo:bar"
	if err.Error() != want {
		t.Fatalf("Error(): got %q, want %q", err.Error(), want)
	}
}

func TestUnregisteredPermissionError_As(t *testing.T) {
	var err error = &UnregisteredPermissionError{Permission: "x:y"}
	var target *UnregisteredPermissionError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *UnregisteredPermissionError")
	}
	if target.Permission != "x:y" {
		t.Fatalf("Permission: got %q, want %q", target.Permission, "x:y")
	}
}

// ---------------------------------------------------------------------------
// Total registered count sanity check
// ---------------------------------------------------------------------------

func TestAll_TotalCount(t *testing.T) {
	// 6 KV + 6 MVCC + 1 Transaction + 1 Batch + 2 Watch + 5 Lease +
	// 4 Index + 10 TimeSeries + 20 Vector + 8 Admin + 9 Users + 8 Roles +
	// 5 Certs + 5 Tenants = 90 existing non-compliance perms
	// (Certs: generate/export/revoke/list/read;
	//  Users: create/read/update/delete/list/enable/disable +
	//  get_by_cert_thumbprint/add_cert_binding)
	// + 14 Keys (11 + import/export/derive) + 2 Backends (list/read)
	// + 2 PIN (read/write) + 4 PIV (read/write/use/delete)
	// + 6 Passwords (read/write/delete/generate/lock/unlock)
	// + 10 Seal (4 existing + 6 new) + 8 Ceremony + 3 Audit
	// + 5 AuthFactor = 144 self-scope perms.
	// + 11 cross-scope perms (3 Tenants:*:cross + 1 AuditReadCross
	//   + 5 VolumePool + 2 RegistrationConfig) = 155. (CertsRevokeCross is
	//   a twelfth cross-scope perm, added at the bottom of this tally.)
	// + 4 tenant-only perms (tenant_barrier:init/unseal/seal/status) = 159.
	// + 43 authorization-hardening perms = 202 total:
	//   5 Databases + 7 Collections + 5 Locks + 8 Credentials + 4 Shard-control
	//   (admin:start_shard/stop_shard/shard_state/topology) + 7 UserAuthFactors
	//   + 1 IndexList + 2 Embedding + 4 RAG.
	// + 10 cluster-ops perms (6 Backup + 4 Rebalance) = 212.
	// + 3 database barrier perms (Seal/Unseal/BarrierStatus) = 215.
	// + 2 credential perms (Store/Retrieve) = 217.
	// + 1 permissions:resolve perm = 218 total.
	// + 14 broker-domain perms (4 secrets + 2 trust + 2 acme + 3 mcp
	//   + 3 agent:runtime) = 232 total.
	// + 96 broker data-plane perms (20 objstore + 32 orchestrator
	//   + 9 messaging + 11 scheduler + 5 parameters + 8 dynamic_secrets
	//   + 7 project + 4 ollama) = 328 total.
	// + 7 SO share-distribution perms (3 seal:officer_*/submit_seal_vote
	//   + 4 tenant_barrier:officer_*/claim_share/seal_vote) = 335 total.
	// + 5 multi-tenant quota/entitlement perms (tenants:set_isolation,
	//   tenants:set_entitlements, tenants:set_quotas, databases:create_raft,
	//   databases:create_volume) = 340 total.
	// + 1 enable-encryption migration perm (seal:enable_encryption) = 341 total.
	// + 1 tenant barrier enable-encryption perm (tenant_barrier:enable_encryption) = 342 total.
	// + 13 ACME-administration / CA-hierarchy perms (3 acme_accounts + 2
	//   acme_orders + 4 acme_eab + 4 ca) = 355 total.
	// + 3 SSH key vault perms (ssh:read/write/delete) = 358 total.
	// + 1 elastic-scaling perm (databases:scale) = 359 total.
	// + 1 selective point-in-time restore perm (mvcc:restore) = 360 total.
	// + 2 orchestrator task-lifecycle perms (orchestrator:task_readmit,
	//   orchestrator:task_block) = 362 total.
	// + 1 CA-operator cross-scope perm (certs:revoke:cross) = 363 total.
	const expectedTotal = 363
	all := All()
	if len(all) != expectedTotal {
		t.Fatalf("All() count: got %d, want %d", len(all), expectedTotal)
	}
}

// TestUnregistered covers the bulk registry screen the role read path uses:
// registered permissions and whole-segment wildcards are never reported,
// unknown ones are, and input order is preserved.
func TestUnregistered(t *testing.T) {
	Register("unregtest:known", "unregtest:also")

	got := Unregistered([]Permission{
		"unregtest:known",
		"unregtest:retired",
		"unregtest:*",
		"*:read",
		"unregtest:also",
		"unregtest:gone",
	})

	want := []Permission{"unregtest:retired", "unregtest:gone"}
	if len(got) != len(want) {
		t.Fatalf("Unregistered = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Unregistered[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestUnregistered_EmptyAndAllKnown covers the two no-work cases: an empty
// input, and a list the registry fully defines. Both must answer nil, because
// the caller renders the result as "there is no drift".
func TestUnregistered_EmptyAndAllKnown(t *testing.T) {
	if got := Unregistered(nil); got != nil {
		t.Errorf("Unregistered(nil) = %v, want nil", got)
	}
	if got := Unregistered([]Permission{}); got != nil {
		t.Errorf("Unregistered(empty) = %v, want nil", got)
	}

	Register("unregtest2:known")
	if got := Unregistered([]Permission{"unregtest2:known", "keys:*"}); got != nil {
		t.Errorf("Unregistered(all known) = %v, want nil", got)
	}
}
