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

// cluster:join_identity_retire is ScopeCross and deliberately absent from
// every registry-driven role-grant function (the five RoleX() constructors
// in this package), mirroring cluster_join_token_scope.go's own precedent:
// retiring a node identity is a platform decision, not one scoped to a
// tenant, so only Owner (which holds every permission via All(), not
// through a role grant) may retire one by default.
func init() {
	RegisterScope(ScopeCross, ClusterJoinIdentityRetire)
}
