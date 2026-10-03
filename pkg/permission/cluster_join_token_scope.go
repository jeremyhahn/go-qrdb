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

// cluster:join_token is ScopeCross and deliberately absent from every
// registry-driven role-grant function (the five RoleX() constructors in
// this package): a token minted here admits a new node -- any node, not one
// scoped to a tenant -- to the replicated cluster, so only Owner (which
// holds every permission via All(), not through a role grant) may mint one
// by default. This mirrors clusterAdminPermissions' and
// clusterBackupPermissions' own "platform-only" precedent in
// cluster_admin_scope.go: no tenant's SO or Admin role carries it, and
// granting it to one would let that tenant's role admit an arbitrary node
// to the whole cluster, not just its own tenant's data.
func init() {
	RegisterScope(ScopeCross, ClusterJoinToken)
}
