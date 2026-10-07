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

// cluster:join_token is ScopeCross: a token minted here admits a new node --
// any node, not one scoped to a tenant -- to the replicated cluster. It is
// granted to exactly the cluster ADMIN built-in role (owner ruling,
// 2026-10-06, registerAdminGrants in builtin_role_grants.go) and, through
// permission.All() rather than a role grant, Owner. No other built-in role
// (SO, Operator, User, Auditor) carries it. This mirrors
// clusterAdminPermissions' and clusterBackupPermissions' own "platform-only"
// precedent in cluster_admin_scope.go: the grant is harmless on a tenant's
// own Admin, because ScopeCross admits only a system-tenant caller, so
// granting it there would let that tenant's role admit an arbitrary node to
// the whole cluster, not just its own tenant's data.
//
// Before the Admin grant, strict separation of duties -- always on in a
// FIPS build, which strips the Owner role id outright
// (rbac.RBACAuthorizer.stripOwnerRole) -- left NO identity able to mint a
// join token through the default role set at all, including the system
// tenant's own Owner: ClusterJoinToken had no other built-in-role grant, so
// voiding Owner voided the only path to it. See
// docs/seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips.
// The Admin grant restores the capability under that posture without
// reopening FIPS 140-3 AS10.03: Admin is never a Security Officer and never
// a custodian, and a tenant's own Admin still cannot exercise it.
func init() {
	RegisterScope(ScopeCross, ClusterJoinToken)
}
