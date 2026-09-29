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
	"strings"
	"testing"
)

// minRegisteredPermissions is a discovery floor. Every assertion in this file
// is inside a loop over All(), and an empty loop reports success — so the
// gate first insists the registry is at least as large as it was when the
// gate was written. Measured 2026-09-13: 364. Raise it when permissions are
// added; a drop below it is a red run, which is the point. Lowered to 362 on
// purpose when the unified share holders retired the raw-share and
// custodian-group permissions (see TestAll's expectedTotal for the ledger).
const minRegisteredPermissions = 362

// TestEveryPermissionDeclaresItsScope is the gate for go-qrdb#504: no
// permission may acquire a scope by omission.
//
// Before this gate, LookupScope answered ScopeSelf for any permission absent
// from the scope registry, and 221 of 362 built-in permissions relied on it.
// An omission and a decision were indistinguishable, and the omission was
// the one that admitted — certs:revoke reached every tenant that way
// (go-qrdb#474). Now every permission in All() must have a declared scope,
// LookupScope refuses one that does not, and this test fails the build for
// the first permission anyone adds without declaring one.
//
// The test uses only the public API, so it asserts the contract a consumer
// sees rather than the registry's internals.
func TestEveryPermissionDeclaresItsScope(t *testing.T) {
	all := All()
	if len(all) < minRegisteredPermissions {
		t.Fatalf("All() returned %d permissions, below the floor of %d — the registry shrank or discovery is broken, and an empty loop would pass every check below", len(all), minRegisteredPermissions)
	}

	var undeclared []string
	counts := map[Scope]int{}
	for _, p := range all {
		s, err := LookupScope(p)
		if err != nil {
			var u *UndeclaredScopeError
			if !errors.As(err, &u) {
				t.Errorf("LookupScope(%q): unexpected error type %T: %v", p, err, err)
				continue
			}
			undeclared = append(undeclared, string(p))
			continue
		}
		if !s.Valid() {
			t.Errorf("LookupScope(%q) = %v, which is not a valid scope", p, s)
			continue
		}
		counts[s]++
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Fatalf("%d permission(s) have no declared scope — register each, by name, in self_scope.go, tenant_scope.go or cross_scope.go (pin the member, not the family):\n  %s",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
	t.Logf("scope declarations: total=%d self=%d cross=%d tenantOnly=%d selfStrict=%d undeclared=0",
		len(all), counts[ScopeSelf], counts[ScopeCross], counts[ScopeTenantOnly], counts[ScopeSelfStrict])

	// The buckets must account for every permission. A scope added to the
	// vocabulary without a count here leaves its members invisible in this
	// census while `total` still counts them -- the tally would look right
	// and be wrong, which is the failure this gate exists to prevent.
	counted := counts[ScopeSelf] + counts[ScopeCross] + counts[ScopeTenantOnly] + counts[ScopeSelfStrict]
	if counted != len(all) {
		t.Errorf("scope census covers %d of %d permissions -- a scope exists that this census does not count", counted, len(all))
	}
}

// TestScopeRegistryHasNoStrays is the other direction: every declared scope
// belongs to a registered permission. A scope registered for a name that is
// not in All() is a typo or a stale entry, and either one hides a real
// omission behind a count that looks right.
func TestScopeRegistryHasNoStrays(t *testing.T) {
	registered := map[Permission]struct{}{}
	for _, p := range All() {
		registered[p] = struct{}{}
	}
	scopeMu.RLock()
	defer scopeMu.RUnlock()
	var strays []string
	for p := range scopeRegistry {
		if _, ok := registered[p]; !ok {
			strays = append(strays, string(p))
		}
	}
	sort.Strings(strays)
	if len(strays) > 0 {
		t.Fatalf("%d scope entr(ies) name a permission that is not registered:\n  %s", len(strays), strings.Join(strays, "\n  "))
	}
}

// selfScopedOutsideSelfScopeFile is the explicit allowlist of permissions
// declared ScopeSelf somewhere other than self_scope.go. The cert-binding
// pair is registered beside its definitions in constants.go because it was
// the first pair declared deliberately and its rationale lives there. Any
// other ScopeSelf declaration outside self_scope.go fails
// TestSelfScopedPermissionsAreExactlyThoseDeclared: the point of one file is
// that the reviewer of a scope decision knows where to look.
var selfScopedOutsideSelfScopeFile = map[Permission]struct{}{
	UsersAddCertBinding:    {},
	UsersRemoveCertBinding: {},
}

// TestSelfScopedPermissionsAreExactlyThoseDeclared pins the "no side
// effects" verification go-qrdb#504 asks for, in BOTH directions:
//
//   - every member of selfScopedPermissions() resolves to ScopeSelf, so the
//     file is not silently carrying a permission that some later
//     registration re-tagged (a permission that should be narrower moves
//     OUT of self_scope.go as its own change; it must not be quietly
//     overridden while still listed there); and
//   - every permission that resolves to ScopeSelf is listed in
//     selfScopedPermissions() or in the explicit allowlist above, so a
//     ScopeSelf declaration cannot be slipped into some other file where a
//     reviewer of scope decisions would not look for it.
//
// Without the second direction "exactly" is a one-way inclusion and the
// gate is registry coverage, not declaration provenance.
func TestSelfScopedPermissionsAreExactlyThoseDeclared(t *testing.T) {
	listed := map[Permission]struct{}{}
	for _, p := range selfScopedPermissions() {
		if _, dup := listed[p]; dup {
			t.Errorf("%q is listed twice in self_scope.go", p)
		}
		listed[p] = struct{}{}
		if !IsRegistered(p) {
			t.Errorf("%q is in self_scope.go but not registered in All()", p)
		}
		if _, exempt := selfScopedOutsideSelfScopeFile[p]; exempt {
			t.Errorf("%q is both in self_scope.go and in the outside-the-file allowlist; pick one", p)
		}
		if got := mustScope(t, p); got != ScopeSelf {
			t.Errorf("%q is listed in self_scope.go but resolves to %v — remove it from the list if that override is intended", p, got)
		}
	}
	if len(listed) == 0 {
		t.Fatal("selfScopedPermissions() is empty; the loop above asserted nothing")
	}

	// The reverse inclusion.
	var resolvedSelf int
	for _, p := range All() {
		if got := mustScope(t, p); got != ScopeSelf {
			continue
		}
		resolvedSelf++
		if _, ok := listed[p]; ok {
			continue
		}
		if _, ok := selfScopedOutsideSelfScopeFile[p]; ok {
			continue
		}
		t.Errorf("%q resolves to ScopeSelf but is declared neither in self_scope.go nor in selfScopedOutsideSelfScopeFile — a ScopeSelf declaration belongs in self_scope.go", p)
	}
	if want := len(listed) + len(selfScopedOutsideSelfScopeFile); resolvedSelf != want {
		t.Errorf("%d permissions resolve to ScopeSelf, but self_scope.go plus the allowlist declare %d", resolvedSelf, want)
	}
	for p := range selfScopedOutsideSelfScopeFile {
		if got := mustScope(t, p); got != ScopeSelf {
			t.Errorf("allowlisted %q resolves to %v, not ScopeSelf; drop it from the allowlist", p, got)
		}
	}
}
