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

import "testing"

// TestScope_String_KnownValues verifies the human-readable labels for the
// declared scope constants.
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

// TestLookupScope_DefaultsToSelf verifies that a permission absent from
// the scope registry resolves to ScopeSelf — the safe default that
// confines the permission to the identity's own tenant.
func TestLookupScope_DefaultsToSelf(t *testing.T) {
	// KVPut is a built-in permission with no explicit scope registration.
	if got := LookupScope(KVPut); got != ScopeSelf {
		t.Errorf("LookupScope(KVPut) = %v, want %v", got, ScopeSelf)
	}

	// An entirely unknown permission must also default to ScopeSelf.
	unknown := Permission("widgets:frob")
	if got := LookupScope(unknown); got != ScopeSelf {
		t.Errorf("LookupScope(unknown) = %v, want %v", got, ScopeSelf)
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
		if got := LookupScope(p); got != ScopeCross {
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
		TenantsCreate,
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
		if got := LookupScope(p); got != ScopeSelf {
			t.Errorf("LookupScope(%q) = %v, want %v", p, got, ScopeSelf)
		}
		if IsCrossScope(p) {
			t.Errorf("IsCrossScope(%q) = true, want false", p)
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
		{"unknown:perm", ScopeSelf},
	}
	for _, tc := range cases {
		if got := LookupScopeByName(tc.name); got != tc.want {
			t.Errorf("LookupScopeByName(%q) = %v, want %v", tc.name, got, tc.want)
		}
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
	if got := LookupScope(probe); got != ScopeCross {
		t.Fatalf("after first register: LookupScope(%q) = %v, want %v", probe, got, ScopeCross)
	}

	RegisterScope(ScopeSelf, probe)
	if got := LookupScope(probe); got != ScopeSelf {
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
func TestResetScopeRegistryForTesting_ClearsAll(t *testing.T) {
	// Snapshot current registry so we can restore it post-test.
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

	// After reset, every permission lookup must default to ScopeSelf.
	if got := LookupScope(TenantsListCross); got != ScopeSelf {
		t.Errorf("after reset: LookupScope(TenantsListCross) = %v, want %v (default)", got, ScopeSelf)
	}
}
