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

// acmeAdminExpectations enumerates every ACME-administration and CA-hierarchy
// permission together with its expected resource, action, category, and group.
// It is the single source of truth the table-driven tests below assert against.
var acmeAdminExpectations = []struct {
	perm     Permission
	resource string
	action   string
	category string
	group    string
}{
	{ACMEAccountsRead, "acme_accounts", "read", "ACME Accounts", "Security Services"},
	{ACMEAccountsList, "acme_accounts", "list", "ACME Accounts", "Security Services"},
	{ACMEAccountsDisable, "acme_accounts", "disable", "ACME Accounts", "Security Services"},
	{ACMEOrdersRead, "acme_orders", "read", "ACME Orders", "Security Services"},
	{ACMEOrdersList, "acme_orders", "list", "ACME Orders", "Security Services"},
	{ACMEEABRead, "acme_eab", "read", "ACME External Account Bindings", "Security Services"},
	{ACMEEABList, "acme_eab", "list", "ACME External Account Bindings", "Security Services"},
	{ACMEEABCreate, "acme_eab", "create", "ACME External Account Bindings", "Security Services"},
	{ACMEEABDelete, "acme_eab", "delete", "ACME External Account Bindings", "Security Services"},
	{CACreate, "ca", "create", "Certificate Authorities", "Security Services"},
	{CARead, "ca", "read", "Certificate Authorities", "Security Services"},
	{CAList, "ca", "list", "Certificate Authorities", "Security Services"},
	{CAUpdate, "ca", "update", "Certificate Authorities", "Security Services"},
}

// TestACMEAdminScope_ParseAndRegistered verifies that every ACME-administration
// permission parses cleanly into the expected resource:action pair and is
// present in the global registry.
func TestACMEAdminScope_ParseAndRegistered(t *testing.T) {
	for _, tc := range acmeAdminExpectations {
		t.Run(string(tc.perm), func(t *testing.T) {
			parsed, err := Parse(string(tc.perm))
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", tc.perm, err)
			}
			if parsed != tc.perm {
				t.Fatalf("Parse(%q): got %q", tc.perm, parsed)
			}
			if got := Resource(tc.perm); got != tc.resource {
				t.Fatalf("Resource(%q): got %q, want %q", tc.perm, got, tc.resource)
			}
			if got := Action(tc.perm); got != tc.action {
				t.Fatalf("Action(%q): got %q, want %q", tc.perm, got, tc.action)
			}
			if !IsRegistered(tc.perm) {
				t.Fatalf("IsRegistered(%q): got false, want true", tc.perm)
			}
		})
	}
}

// TestACMEAdminScope_SelfScope verifies that the ACME-administration and
// CA-hierarchy permissions take the default ScopeSelf. This is the behavioural
// difference from the broker's acme:read / acme:write (ScopeTenantOnly): a
// system-tenant Admin must be able to administer the ACME server and its CA
// hierarchy, which ScopeTenantOnly would structurally deny.
func TestACMEAdminScope_SelfScope(t *testing.T) {
	for _, tc := range acmeAdminExpectations {
		t.Run(string(tc.perm), func(t *testing.T) {
			if got := LookupScope(tc.perm); got != ScopeSelf {
				t.Fatalf("LookupScope(%q): got %v, want %v", tc.perm, got, ScopeSelf)
			}
			if IsCrossScope(tc.perm) {
				t.Fatalf("IsCrossScope(%q): got true, want false", tc.perm)
			}
		})
	}
}

// TestACMEAdminScope_DistinctFromBrokerACME verifies that the ACME
// administration family does not collide with, or silently re-scope, the
// broker's tenant-owned acme:read / acme:write permissions.
func TestACMEAdminScope_DistinctFromBrokerACME(t *testing.T) {
	if LookupScope(ACMERead) != ScopeTenantOnly || LookupScope(ACMEWrite) != ScopeTenantOnly {
		t.Fatalf("broker acme:read/acme:write must remain ScopeTenantOnly; got %v/%v",
			LookupScope(ACMERead), LookupScope(ACMEWrite))
	}
	for _, p := range ByResource("acme") {
		if p != ACMERead && p != ACMEWrite {
			t.Fatalf("resource %q gained unexpected permission %q", "acme", p)
		}
	}
}

// TestACMEAdminScope_InAllAndByResource verifies that the permissions appear in
// All() and in ByResource() for their resource.
func TestACMEAdminScope_InAllAndByResource(t *testing.T) {
	all := make(map[Permission]struct{}, len(All()))
	for _, p := range All() {
		all[p] = struct{}{}
	}
	for _, tc := range acmeAdminExpectations {
		if _, ok := all[tc.perm]; !ok {
			t.Errorf("All() missing %q", tc.perm)
		}
		found := false
		for _, p := range ByResource(tc.resource) {
			if p == tc.perm {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ByResource(%q) missing %q", tc.resource, tc.perm)
		}
	}
}

// TestACMEAdminScope_InCategories verifies that every permission is surfaced
// under the expected category and logical group for UI rendering.
func TestACMEAdminScope_InCategories(t *testing.T) {
	type loc struct {
		name  string
		group string
	}
	byPerm := map[Permission]loc{}
	for _, cat := range Categories() {
		for _, p := range cat.Permissions {
			byPerm[p] = loc{cat.Name, cat.Group}
		}
	}
	for _, tc := range acmeAdminExpectations {
		got, ok := byPerm[tc.perm]
		if !ok {
			t.Errorf("Categories() missing %q", tc.perm)
			continue
		}
		if got.name != tc.category || got.group != tc.group {
			t.Errorf("Categories()[%q]: got (%q,%q), want (%q,%q)",
				tc.perm, got.name, got.group, tc.category, tc.group)
		}
	}
}

// TestACMEAdminScope_RejectsMalformed verifies the error path: a near-miss of
// one of the new permission names that omits the action is rejected by Parse
// and is not registered, so a typo can never resolve to a real grant.
func TestACMEAdminScope_RejectsMalformed(t *testing.T) {
	malformed := []string{"acme_accounts", "acme_eab", "ca", "", ":read", "acme_orders:"}
	for _, raw := range malformed {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%q): expected error, got nil", raw)
			}
			if IsRegistered(Permission(raw)) {
				t.Fatalf("IsRegistered(%q): got true, want false", raw)
			}
		})
	}
}
