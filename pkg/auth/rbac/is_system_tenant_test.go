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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestIsSystemTenant(t *testing.T) {
	tests := []struct {
		name     string
		identity *auth.Identity
		want     bool
	}{
		{"nil identity is system-scoped", nil, true},
		{"empty TenantID resolves to system", &auth.Identity{Subject: "s", TenantID: ""}, true},
		{"literal system tenant matches", &auth.Identity{Subject: "s", TenantID: tenant.SystemTenantID}, true},
		{"ordinary tenant does not match", &auth.Identity{Subject: "s", TenantID: "acme-corp"}, false},
		{"capitalized lookalike does not match", &auth.Identity{Subject: "s", TenantID: "System"}, false},
		{"trailing-space lookalike does not match", &auth.Identity{Subject: "s", TenantID: "system "}, false},
		{"suffixed lookalike does not match", &auth.Identity{Subject: "s", TenantID: "system-2"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSystemTenant(tc.identity)
			if got != tc.want {
				t.Fatalf("IsSystemTenant(%+v) = %v, want %v", tc.identity, got, tc.want)
			}
		})
	}
}

// TestIsSystemTenantDelegatesToTenantPackage proves the identity-level
// predicate never re-derives the string comparison: it must agree with
// tenant.IsSystemTenant for every TenantID value, since tenant.IsSystemTenant
// is the single source of truth.
func TestIsSystemTenantDelegatesToTenantPackage(t *testing.T) {
	ids := []string{"", tenant.SystemTenantID, "acme-corp", "System", "system "}
	for _, id := range ids {
		identity := &auth.Identity{Subject: "s", TenantID: id}
		got := IsSystemTenant(identity)
		want := tenant.IsSystemTenant(id)
		if got != want {
			t.Fatalf("IsSystemTenant(TenantID=%q) = %v, want %v (tenant.IsSystemTenant)", id, got, want)
		}
	}
}
