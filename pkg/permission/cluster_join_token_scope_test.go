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

// TestClusterJoinToken_IsCrossScope pins that minting a member-side CSR
// join token is ScopeCross: the token admits a node to the whole cluster,
// not to one tenant.
func TestClusterJoinToken_IsCrossScope(t *testing.T) {
	if got, err := LookupScope(ClusterJoinToken); err != nil || got != ScopeCross {
		t.Errorf("%s: scope = %v (err %v), want ScopeCross", ClusterJoinToken, got, err)
	}
}

// TestClusterJoinToken_IsPlatformOnly pins that cluster:join_token is granted
// to exactly the platform-admin role among the built-ins: the cluster ADMIN
// role (owner ruling, 2026-10-06) by a registered grant, and Owner through
// permission.All() rather than a role grant. No other built-in role --
// Security Officer, Operator, User or Auditor -- carries it by default. The
// grant is still platform-only in effect: ScopeCross admits only a
// system-tenant caller, so a tenant's own Admin holds the permission but can
// never exercise it.
func TestClusterJoinToken_IsPlatformOnly(t *testing.T) {
	if !HasRoleGrant(RoleAdmin, ClusterJoinToken) {
		t.Fatalf("role %q does not carry cluster:join_token by default, want the platform-admin grant", RoleAdmin)
	}
	for _, role := range []string{RoleSO, RoleOperator, RoleUser, RoleAuditor} {
		for _, p := range RolePermissions(role) {
			if p == ClusterJoinToken {
				t.Fatalf("role %q carries cluster:join_token by default, want Admin/Owner only", role)
			}
		}
	}
}
