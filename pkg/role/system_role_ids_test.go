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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func TestSystemRoleIDs_EverySystemRoleMapsToItsCanonicalID(t *testing.T) {
	const tenant = "acme"
	ids := SystemRoleIDs(tenant)

	want := map[string]struct{}{OwnerRole(tenant).Name: {}}
	for _, r := range BuiltInRoles(tenant) {
		want[r.Name] = struct{}{}
	}
	if len(ids) != len(want) {
		t.Fatalf("SystemRoleIDs(%q) has %d entries, want %d", tenant, len(ids), len(want))
	}
	for name := range want {
		id, ok := ids[name]
		if !ok {
			t.Fatalf("SystemRoleIDs(%q) is missing %q", tenant, name)
		}
		if id != SystemRoleID(tenant, name) {
			t.Fatalf("SystemRoleIDs(%q)[%q] = %q, want SystemRoleID = %q", tenant, name, id, SystemRoleID(tenant, name))
		}
	}
}

func TestSystemRoleIDs_TenantsAreDisjoint(t *testing.T) {
	a, b := SystemRoleIDs("acme"), SystemRoleIDs("globex")
	seen := make(map[string]string, len(a))
	for name, id := range a {
		seen[id] = name
	}
	for name, id := range b {
		if other, ok := seen[id]; ok {
			t.Fatalf("globex %q and acme %q share ID %q", name, other, id)
		}
	}
}

func TestSystemRoleIDs_EmptyTenantIsSystem(t *testing.T) {
	empty, sys := SystemRoleIDs(""), SystemRoleIDs(tenantid.System)
	if len(empty) != len(sys) {
		t.Fatalf("len(SystemRoleIDs(\"\")) = %d, want %d", len(empty), len(sys))
	}
	for name, id := range sys {
		if empty[name] != id {
			t.Fatalf("SystemRoleIDs(\"\")[%q] = %q, want %q", name, empty[name], id)
		}
	}
}

func TestSystemRoleIDs_ReturnsFreshMap(t *testing.T) {
	first := SystemRoleIDs("acme")
	want := len(first)
	for name := range first {
		first[name] = "tampered"
	}
	first["injected"] = "x"

	second := SystemRoleIDs("acme")
	if len(second) != want {
		t.Fatalf("second call has %d entries, want %d", len(second), want)
	}
	for name, id := range second {
		if id != SystemRoleID("acme", name) {
			t.Fatalf("second call [%q] = %q: the first caller's change leaked", name, id)
		}
	}
}
