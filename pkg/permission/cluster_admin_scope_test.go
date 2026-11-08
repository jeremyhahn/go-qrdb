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

// TestClusterAdminPermissions_AreCrossScope pins, by name, every permission
// that changes or exposes the node's Raft membership and shards. None of their
// routes names a tenant, so as ScopeSelf they admitted any tenant's Admin or
// SO -- who could remove replicas from the system shard.
func TestClusterAdminPermissions_AreCrossScope(t *testing.T) {
	for _, p := range []Permission{
		AdminAddMember, AdminRemoveMember, AdminDrain, AdminSnapshot,
		AdminStartShard, AdminStopShard, AdminShardState, AdminTopology,
		AdminReplicaState, AdminNodeCompaction, AdminStateHash,
		RebalanceExecute, RebalancePlan, RebalanceReport, RebalanceStats,
		BackupCreate, BackupDelete, BackupList, BackupRead, BackupRestore, BackupVerify,
	} {
		if got, err := LookupScope(p); err != nil || got != ScopeCross {
			t.Errorf("%s: scope = %v (err %v), want ScopeCross", p, got, err)
		}
	}
}

// TestAdminRole_HoldsStartShardOnly pins the Admin role's shard lifecycle
// grants (#1281): a cluster administrator starts shards as it adds members
// and drains nodes, so the role carries admin:start_shard; it carries no
// other shard lifecycle or topology permission.
func TestAdminRole_HoldsStartShardOnly(t *testing.T) {
	if !HasRoleGrant(RoleAdmin, AdminStartShard) {
		t.Errorf("Admin role lacks %s", AdminStartShard)
	}
	for _, p := range []Permission{AdminStopShard, AdminShardState, AdminTopology} {
		if HasRoleGrant(RoleAdmin, p) {
			t.Errorf("Admin role holds %s, want it withheld", p)
		}
	}
}

// TestClusterAdminPermissions_TenantReadsStaySelf pins the admin reads a
// tenant-facing feature needs: health, status and capabilities, and
// admin:peers, which backs the etcd listener's Cluster.MemberList and is
// authorized there against the database's owning tenant.
func TestClusterAdminPermissions_TenantReadsStaySelf(t *testing.T) {
	for _, p := range []Permission{AdminHealth, AdminStatus, AdminCapabilities, AdminPeers} {
		if got, err := LookupScope(p); err != nil || got != ScopeSelf {
			t.Errorf("%s: scope = %v (err %v), want ScopeSelf", p, got, err)
		}
	}
}
