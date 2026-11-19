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

package sdk

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

func TestSystemRoleIDs_KeysAreSystemRolesValuesAreSystemRoleID(t *testing.T) {
	const tenant = "acme"
	ids := SystemRoleIDs(tenant)

	want := map[string]struct{}{role.OwnerRole(tenant).Name: {}}
	for _, r := range role.BuiltInRoles(tenant) {
		want[r.Name] = struct{}{}
	}
	if len(ids) != len(want) {
		t.Fatalf("SystemRoleIDs(%q) has %d entries, want %d", tenant, len(ids), len(want))
	}
	for name := range want {
		if got := ids[name]; got != SystemRoleID(tenant, name) {
			t.Fatalf("SystemRoleIDs(%q)[%q] = %q, want %q", tenant, name, got, SystemRoleID(tenant, name))
		}
	}
}

func TestSystemRoleIDs_DisjointAcrossTenants(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range SystemRoleIDs("acme") {
		seen[id] = true
	}
	for name, id := range SystemRoleIDs("globex") {
		if seen[id] {
			t.Fatalf("globex %q reuses an acme ID %q", name, id)
		}
	}
}

func TestSystemRoleIDs_EmptyTenantIsSystemTenant(t *testing.T) {
	empty, sys := SystemRoleIDs(""), SystemRoleIDs(SystemTenantID)
	if len(empty) != len(sys) {
		t.Fatalf("len = %d, want %d", len(empty), len(sys))
	}
	for name, id := range sys {
		if empty[name] != id {
			t.Fatalf("SystemRoleIDs(\"\")[%q] = %q, want %q", name, empty[name], id)
		}
	}
}

func TestSystemRoleIDs_CallerMutationDoesNotLeak(t *testing.T) {
	first := SystemRoleIDs("acme")
	n := len(first)
	for name := range first {
		delete(first, name)
	}
	first["injected"] = "x"
	second := SystemRoleIDs("acme")
	if len(second) != n {
		t.Fatalf("second call has %d entries, want %d", len(second), n)
	}
	if _, ok := second["injected"]; ok {
		t.Fatal("the first caller's insertion leaked into the second call")
	}
}
