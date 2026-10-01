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
	"strings"
	"testing"
)

// TestScope_String_KnownValues verifies the human-readable labels for the
// declared scope constants.
// mustScope returns the declared scope of p, failing the test if p has no
// declared scope. Every built-in permission is declared (go-qrdb#504), so a
// test reaching the failure branch has found an undeclared permission, which
// is itself the defect this helper exists to surface.
func mustScope(t *testing.T, p Permission) Scope {
	t.Helper()
	s, err := LookupScope(p)
	if err != nil {
		t.Fatalf("LookupScope(%q): %v", p, err)
	}
	return s
}

// mustScopeByName is mustScope over a raw permission string.
func mustScopeByName(t *testing.T, name string) Scope {
	t.Helper()
	s, err := LookupScopeByName(name)
	if err != nil {
		t.Fatalf("LookupScopeByName(%q): %v", name, err)
	}
	return s
}

func TestScope_String_KnownValues(t *testing.T) {
	cases := []struct {
		scope Scope
		want  string
	}{
		{ScopeSelf, "self"},
		{ScopeCross, "cross"},
	}
	for _, tc := range cases {
		if got := tc.scope.String(); got != tc.want {
			t.Errorf("Scope(%d).String() = %q, want %q", tc.scope, got, tc.want)
		}
	}
}

// TestScope_String_UnknownValue ensures an unrecognized Scope value does
// not return an empty string — it must return a stable, non-empty label
// so logs and error messages remain self-describing.
func TestScope_String_UnknownValue(t *testing.T) {
	got := Scope(99).String()
	if got != "unknown" {
		t.Errorf("Scope(99).String() = %q, want %q", got, "unknown")
	}
}

// TestLookupScope_DeclaredSelfIsSelf verifies that a permission declared
// ScopeSelf resolves to ScopeSelf — because it was declared so, not because
// nothing else was said about it.
func TestLookupScope_DeclaredSelfIsSelf(t *testing.T) {
	// KVPut is a built-in permission declared ScopeSelf in self_scope.go.
	// Before go-qrdb#504 it had no declaration and answered ScopeSelf by
	// default; the answer is the same, the reason is not.
	if got := mustScope(t, KVPut); got != ScopeSelf {
		t.Errorf("LookupScope(KVPut) = %v, want %v", got, ScopeSelf)
	}
}

// TestLookupScope_UndeclaredIsAnError pins go-qrdb#504: a permission with no
// declared scope is refused, not defaulted. The previous version of this
// test asserted the opposite — "an entirely unknown permission must also
// default to ScopeSelf" — which was the defect, stated as the contract.
func TestLookupScope_UndeclaredIsAnError(t *testing.T) {
	unknown := Permission("widgets:frob")
	got, err := LookupScope(unknown)
	if err == nil {
		t.Fatalf("LookupScope(%q) = %v, nil; want *UndeclaredScopeError — an undeclared permission must not acquire a scope by omission", unknown, got)
	}
	var undeclared *UndeclaredScopeError
	if !errors.As(err, &undeclared) {
		t.Fatalf("LookupScope(%q) error = %T (%v), want *UndeclaredScopeError", unknown, err, err)
	}
	if undeclared.Permission != unknown {
		t.Errorf("UndeclaredScopeError.Permission = %q, want %q", undeclared.Permission, unknown)
	}
	if !strings.Contains(err.Error(), string(unknown)) {
		t.Errorf("error text %q does not name the permission", err.Error())
	}
	// IsCrossScope must not read an undeclared permission as anything.
	if IsCrossScope(unknown) {
		t.Errorf("IsCrossScope(%q) = true for an undeclared permission", unknown)
	}
}

// TestLookupScope_RegisteredCrossScopePermissions verifies that the
// platform-level permissions declared in cross_scope.go register as
// ScopeCross via the package init().
func TestLookupScope_RegisteredCrossScopePermissions(t *testing.T) {
	crossScopePerms := []Permission{
		TenantsListCross,
		TenantsCreateCross,
		TenantsApprove,
		AuditReadCross,
		VolumePoolCreate,
		VolumePoolList,
		VolumePoolRead,
		VolumePoolUpdate,
		VolumePoolDelete,
		RegistrationConfigRead,
		RegistrationConfigUpdate,
	}
	for _, p := range crossScopePerms {
		if got := mustScope(t, p); got != ScopeCross {
			t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeCross)
		}
		if !IsCrossScope(p) {
			t.Errorf("IsCrossScope(%q) = false, want true", p)
		}
	}
}

// TestLookupScope_SelfScopePermissionsRemainSelf is a regression guard
// that ensures the existing tenant CRUD permissions are still ScopeSelf —
// renaming or accidentally re-registering them would change RBAC
// behaviour for tenant-local admins.
func TestLookupScope_SelfScopePermissionsRemainSelf(t *testing.T) {
	selfScopePerms := []Permission{
		TenantsRead,
		TenantsUpdate,
		TenantsDelete,
		TenantsList,
		AuditRead,
		KVPut,
		KVGet,
		UsersCreate,
	}
	for _, p := range selfScopePerms {
		if got := mustScope(t, p); got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeSelf)
		}
		if IsCrossScope(p) {
			t.Errorf("IsCrossScope(%q) = true, want false", p)
		}
	}
}

// TestLookupScope_TenantProvisioningIsCrossScope pins the platform-operator
// tenant permissions to ScopeCross. As ScopeSelf, a tenant Admin could create
// tenants and raise its own quotas, entitlements and isolation tier.
func TestLookupScope_TenantProvisioningIsCrossScope(t *testing.T) {
	for _, p := range []Permission{
		TenantsCreate,
		TenantsSetQuotas,
		TenantsSetEntitlements,
		TenantsSetIsolation,
	} {
		if got := mustScope(t, p); got != ScopeCross {
			t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeCross)
		}
	}
}

// TestTenantProvisioningPermissions_NotSelfScoped guards the registration
// order: a permission in both lists would take whichever init() ran last.
func TestTenantProvisioningPermissions_NotSelfScoped(t *testing.T) {
	self := make(map[Permission]struct{})
	for _, p := range selfScopedPermissions() {
		self[p] = struct{}{}
	}
	for _, p := range tenantProvisioningPermissions() {
		if _, ok := self[p]; ok {
			t.Errorf("%q is registered both ScopeSelf and ScopeCross", p)
		}
	}
}

// TestLookupScopeByName_DelegatesToLookupScope verifies the string
// convenience wrapper agrees with the typed LookupScope on every known
// value.
func TestLookupScopeByName_DelegatesToLookupScope(t *testing.T) {
	cases := []struct {
		name string
		want Scope
	}{
		{"tenants:list:cross", ScopeCross},
		{"volumepool:create", ScopeCross},
		{"tenants:read", ScopeSelf},
	}
	for _, tc := range cases {
		if got := mustScopeByName(t, tc.name); got != tc.want {
			t.Errorf("LookupScopeByName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// And the refusal delegates too: an undeclared name is an error here
	// exactly as it is through LookupScope.
	got, err := LookupScopeByName("unknown:perm")
	var undeclared *UndeclaredScopeError
	if !errors.As(err, &undeclared) {
		t.Fatalf("LookupScopeByName(%q) = %v, %v; want *UndeclaredScopeError", "unknown:perm", got, err)
	}
	if undeclared.Permission != "unknown:perm" {
		t.Errorf("error names %q, want unknown:perm", undeclared.Permission)
	}
}

// TestRegisterScope_OverwritesPrevious verifies that re-registering a
// permission with a new scope replaces the previous entry — the registry
// is last-write-wins, not accumulating.
func TestRegisterScope_OverwritesPrevious(t *testing.T) {
	const probe Permission = "test:scope-overwrite"
	t.Cleanup(func() {
		// Best-effort cleanup. Not strictly required since later tests
		// don't depend on this permission, but keeps the registry tidy.
		scopeMu.Lock()
		delete(scopeRegistry, probe)
		scopeMu.Unlock()
	})

	RegisterScope(ScopeCross, probe)
	if got := mustScope(t, probe); got != ScopeCross {
		t.Fatalf("after first register: LookupScope(%q) = %v, want %v", probe, got, ScopeCross)
	}

	RegisterScope(ScopeSelf, probe)
	if got := mustScope(t, probe); got != ScopeSelf {
		t.Fatalf("after second register: LookupScope(%q) = %v, want %v", probe, got, ScopeSelf)
	}
}

// TestRegisterScope_MultiplePerms verifies a single RegisterScope call
// applies to every permission supplied.
func TestRegisterScope_MultiplePerms(t *testing.T) {
	probes := []Permission{
		"test:multi-a",
		"test:multi-b",
		"test:multi-c",
	}
	t.Cleanup(func() {
		scopeMu.Lock()
		for _, p := range probes {
			delete(scopeRegistry, p)
		}
		scopeMu.Unlock()
	})

	RegisterScope(ScopeCross, probes...)
	for _, p := range probes {
		if !IsCrossScope(p) {
			t.Errorf("IsCrossScope(%q) = false, want true", p)
		}
	}
}

// TestResetScopeRegistryForTesting_ClearsAll exercises the test helper
// and re-asserts the cross-scope registrations come back via init() on
// the next package load (within this process, the helper just clears;
// re-population requires a re-run).
// withEmptyScopeRegistry empties the process-global scope registry for the
// duration of the calling test and restores the previous contents on
// cleanup. It is the ONLY sanctioned way for a test to reset the registry.
//
// Restoring is not optional: a test that resets and then merely resets again
// on cleanup leaves the registry empty for every test that runs after it,
// which is how TestTenantScopeStore_FallbackPrecedence silently emptied it
// for the rest of the package until TestEveryPermissionDeclaresItsScope
// walked All() and found 364 undeclared permissions.
func withEmptyScopeRegistry(t *testing.T) {
	t.Helper()
	scopeMu.RLock()
	snapshot := make(map[Permission]Scope, len(scopeRegistry))
	for k, v := range scopeRegistry {
		snapshot[k] = v
	}
	scopeMu.RUnlock()
	t.Cleanup(func() {
		scopeMu.Lock()
		scopeRegistry = snapshot
		scopeMu.Unlock()
	})
	resetScopeRegistryForTesting()
}

func TestResetScopeRegistryForTesting_ClearsAll(t *testing.T) {
	withEmptyScopeRegistry(t)

	// After reset there is no registry entry and therefore no answer: the
	// lookup refuses. (It used to answer ScopeSelf here "by default" — the
	// fail-open go-qrdb#504 removed.)
	got, err := LookupScope(TenantsListCross)
	var undeclared *UndeclaredScopeError
	if !errors.As(err, &undeclared) {
		t.Fatalf("after reset: LookupScope(TenantsListCross) = %v, %v; want *UndeclaredScopeError", got, err)
	}
	if undeclared.Permission != TenantsListCross {
		t.Errorf("error names %q, want %q", undeclared.Permission, TenantsListCross)
	}
}
