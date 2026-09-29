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

package provider

import (
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestQRDBSessionTenantID_FoldsAbsentTenant proves the third of the boundaries
// tenantid.IsSystem's precondition depends on actually folds "".
//
// It matters more here than the name suggests: pkg/sql/privilege.principalOf
// reads this method and marks the system tenant EXEMPT from every SQL grant
// check. A session that returned "" would be handed to a caller that folds it
// again — two copies of the same rule, one of which could drift — so this
// method owns the fold and uses the canonical one.
func TestQRDBSessionTenantID_FoldsAbsentTenant(t *testing.T) {
	cases := []struct {
		name     string
		identity *auth.Identity
		want     string
	}{
		{"nil identity", nil, tenantid.System},
		{"identity with no tenant", &auth.Identity{Subject: "u"}, tenantid.System},
		{"identity in the system tenant", &auth.Identity{Subject: "u", TenantID: tenantid.System}, tenantid.System},
		{"identity in a real tenant", &auth.Identity{Subject: "u", TenantID: "acme"}, "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession(sqle.NewBaseSession(), tc.identity)
			got := s.TenantID()
			if got != tc.want {
				t.Fatalf("TenantID() = %q, want %q", got, tc.want)
			}
			if got == "" {
				t.Fatal("TenantID() returned the empty string; the SQL privilege plane would fold it a second time")
			}
			if got != tenantid.Normalize(got) {
				t.Fatalf("TenantID() = %q is not already normalized", got)
			}
		})
	}
}
