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

// clusterAdminPermissions change, or expose, the node's Raft membership and
// shards: adding and removing a replica, draining the node, requesting a
// shard snapshot, starting and stopping a shard, and reading a shard's state
// or the node's whole topology.
//
// The cluster is a platform asset. The shards these act on -- the system
// shard every tenant's prefix-isolated data lives on, and every
// raft-isolated tenant's own group -- are addressed by shard id, and no route
// names a tenant. These permissions were ScopeSelf, and ScopeSelf compares
// the caller's tenant with the request's tenant, which for a request naming
// none is the caller's own: the check was vacuous. The built-in Admin and SO
// roles grant add_member, remove_member, drain and snapshot in every tenant,
// so any tenant's Admin could remove replicas from the system shard and break
// quorum for every tenant, or drain the node. AdminService does no tenant
// check of its own.
//
// They are ScopeCross: only an identity of the system tenant may exercise
// them, on every transport. The topology reads go with them because they
// enumerate every tenant's raft group and node address; the web console
// already treats a refused topology probe as a standalone server.
// admin:peers is NOT here: the etcd listener's Cluster.MemberList is a
// tenant-facing read authorized against the database's owning tenant.
//
// rebalance:* is here for the same reason: rebalance:execute physically moves
// shards between nodes, and the stats, plan and report reads describe every
// shard on the cluster. No built-in role below Owner carries them, but a
// tenant's SO holds roles:create and roles:assign, so under ScopeSelf it could
// grant itself rebalance:execute and move the system shard.
func clusterAdminPermissions() []Permission {
	return []Permission{
		AdminAddMember,
		AdminDrain,
		AdminRemoveMember,
		AdminShardState,
		AdminSnapshot,
		AdminStartShard,
		AdminStopShard,
		AdminTopology,
		RebalanceExecute,
		RebalancePlan,
		RebalanceReport,
		RebalanceStats,
	}
}

func init() {
	RegisterScope(ScopeCross, clusterAdminPermissions()...)
}
