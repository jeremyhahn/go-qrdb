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

// brokerScopeExpectations enumerates every broker-domain permission added in
// Stage 0 together with its expected resource, action, category, group, and
// tenancy scope. It is the single source of truth the table-driven tests
// below assert against.
var brokerScopeExpectations = []struct {
	perm     Permission
	resource string
	action   string
	category string
	group    string
}{
	{SecretsRead, "secrets", "read", "Secrets", "Security Services"},
	{SecretsWrite, "secrets", "write", "Secrets", "Security Services"},
	{SecretsDelete, "secrets", "delete", "Secrets", "Security Services"},
	{SecretsList, "secrets", "list", "Secrets", "Security Services"},
	{TrustRead, "trust", "read", "Trust", "Security Services"},
	{TrustWrite, "trust", "write", "Trust", "Security Services"},
	{ACMERead, "acme", "read", "ACME", "Security Services"},
	{ACMEWrite, "acme", "write", "ACME", "Security Services"},
	{MCPTools, "mcp", "tools", "MCP Transport", "Transport Services"},
	{MCPResources, "mcp", "resources", "MCP Transport", "Transport Services"},
	{MCPAdmin, "mcp", "admin", "MCP Transport", "Transport Services"},
	// 3-segment scopes: Parse splits on the FIRST colon, so resource=agent
	// and action="runtime:spawn". This is the intended canonical form.
	{AgentRuntimeSpawn, "agent", "runtime:spawn", "Agent Runtime", "AI Services"},
	{AgentRuntimePrompt, "agent", "runtime:prompt", "Agent Runtime", "AI Services"},
	{AgentRuntimeStatus, "agent", "runtime:status", "Agent Runtime", "AI Services"},
}

// TestBrokerScope_ParseAndRegistered verifies that every broker-domain
// permission parses cleanly and is present in the global registry.
func TestBrokerScope_ParseAndRegistered(t *testing.T) {
	for _, tc := range brokerScopeExpectations {
		t.Run(string(tc.perm), func(t *testing.T) {
			parsed, err := Parse(string(tc.perm))
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", tc.perm, err)
			}
			if parsed != tc.perm {
				t.Fatalf("Parse(%q): got %q", tc.perm, parsed)
			}
			if Resource(tc.perm) != tc.resource {
				t.Fatalf("Resource(%q): got %q, want %q", tc.perm, Resource(tc.perm), tc.resource)
			}
			if Action(tc.perm) != tc.action {
				t.Fatalf("Action(%q): got %q, want %q", tc.perm, Action(tc.perm), tc.action)
			}
			if !IsRegistered(tc.perm) {
				t.Fatalf("IsRegistered(%q): got false, want true", tc.perm)
			}
		})
	}
}

// TestBrokerScope_TenantOnlyScope verifies that every broker-domain
// permission is tagged ScopeTenantOnly in the built-in scope layer.
func TestBrokerScope_TenantOnlyScope(t *testing.T) {
	for _, tc := range brokerScopeExpectations {
		t.Run(string(tc.perm), func(t *testing.T) {
			if got := mustScope(t, tc.perm); got != ScopeTenantOnly {
				t.Fatalf("LookupScope(%q): got %v, want %v", tc.perm, got, ScopeTenantOnly)
			}
			if IsCrossScope(tc.perm) {
				t.Fatalf("IsCrossScope(%q): got true, want false", tc.perm)
			}
		})
	}
}

// TestBrokerScope_InAllAndByResource verifies that the broker-domain
// permissions appear in All() and in ByResource() for their resource.
func TestBrokerScope_InAllAndByResource(t *testing.T) {
	all := make(map[Permission]struct{}, len(All()))
	for _, p := range All() {
		all[p] = struct{}{}
	}
	for _, tc := range brokerScopeExpectations {
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

// TestBrokerScope_InCategories verifies that every broker-domain permission
// is surfaced under the expected category and logical group.
func TestBrokerScope_InCategories(t *testing.T) {
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
	for _, tc := range brokerScopeExpectations {
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

// TestBrokerScope_NoDuplicateRegistration verifies that registering the
// broker permissions again is idempotent and does not inflate the registry
// (Register is map-backed, so duplicates collapse rather than panic).
func TestBrokerScope_NoDuplicateRegistration(t *testing.T) {
	before := len(All())
	Register(brokerScopePermissions()...)
	if after := len(All()); after != before {
		t.Fatalf("re-registering broker perms changed count: before %d, after %d", before, after)
	}
}

// TestBrokerScope_WildcardStillParses guards that whole-segment wildcards on
// the new broker resources are recognized as wildcards (so role grants like
// "secrets:*" or "*:read" continue to validate) while a bare "*" is still
// rejected by Parse.
func TestBrokerScope_WildcardStillParses(t *testing.T) {
	for _, w := range []Permission{"secrets:*", "trust:*", "acme:*", "mcp:*", "agent:*", "*:read", "*:*"} {
		if !IsWildcard(w) {
			t.Errorf("IsWildcard(%q): got false, want true", w)
		}
	}
	if _, err := Parse("*"); err == nil {
		t.Fatal("Parse(\"*\"): expected error for bare wildcard, got nil")
	}
}
