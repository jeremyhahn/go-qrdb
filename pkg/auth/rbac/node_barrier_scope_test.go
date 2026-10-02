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

package rbac_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNodeBarrier_TenantRoleHoldersAreDenied drives the REAL authorizer over
// the REAL built-in roles. For every node-barrier and cluster-admin permission
// a built-in role carries, the role's holder in the system tenant is admitted
// and the same role's holder in a tenant is denied: the node barrier and the
// cluster are platform singletons, and none of these operations names a
// tenant the authorizer could compare. Every transport authorizes through
// this decision.
func TestNodeBarrier_TenantRoleHoldersAreDenied(t *testing.T) {
	const other = "acme"
	roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, other)
	authz := rbac.New(roles)
	nodePerms := []permission.Permission{
		permission.SealBootstrap, permission.SealEnableEncryption, permission.SealGenerateRecovery,
		permission.SealInitializeShamir, permission.SealRecoverWithKeys, permission.SealRekey,
		permission.SealRotateRootKey, permission.SealSeal, permission.SealSubmitSealVote, permission.SealUnseal,
		permission.SealOfficerRegister, permission.SealOfficerList,
		permission.CeremonyInitialize, permission.CeremonyEnrollCustodian,
		permission.CeremonyRevokeCustodian, permission.CeremonySignCSR,
		permission.BarrierUnsealShare,
		// The cluster: Raft membership and shards, shared by every tenant.
		permission.AdminAddMember, permission.AdminRemoveMember,
		permission.AdminDrain, permission.AdminSnapshot,
		permission.AdminStartShard, permission.AdminStopShard,
		permission.AdminShardState, permission.AdminTopology,
		// The node keyspace, which holds every tenant's raw history.
		permission.AdminNodeCompaction,
	}
	exercised := 0
	for _, name := range []string{role.RoleNameSO, role.RoleNameAdmin, role.RoleNameOperator, role.RoleNameCustodian} {
		held, err := roles.ResolvePermissions(tenant.WithTenantID(context.Background(), tenant.SystemTenantID),
			[]string{role.SystemRoleID(tenant.SystemTenantID, name)})
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		grants := make(map[permission.Permission]struct{}, len(held))
		for _, p := range held {
			grants[p] = struct{}{}
		}
		for _, p := range nodePerms {
			if _, ok := grants[p]; !ok {
				continue
			}
			exercised++
			t.Run(name+"/"+string(p), func(t *testing.T) {
				for _, tid := range []string{tenant.SystemTenantID, other} {
					id := &auth.Identity{Subject: name + "@" + tid, TenantID: tid, Roles: []string{role.SystemRoleID(tid, name)}}
					err := authz.Authorize(tenant.WithTenantID(context.Background(), tid), id,
						permission.Resource(p), permission.Action(p))
					if tid == tenant.SystemTenantID {
						if err != nil {
							t.Fatalf("system-tenant %s refused %s: %v", name, p, err)
						}
						continue
					}
					var denied *rbac.PermissionDeniedError
					if !errors.As(err, &denied) {
						t.Fatalf("tenant %s holding %s: got %v, want *rbac.PermissionDeniedError", name, p, err)
					}
				}
			})
		}
	}
	// SO carries every one but seal:enable_encryption, which no built-in role
	// below Owner holds; Admin and Operator carry seal and unseal, and Admin
	// recover_with_keys. A floor guards against an empty loop.
	if exercised < len(nodePerms) {
		t.Fatalf("exercised %d (role, permission) pairs, want at least %d", exercised, len(nodePerms))
	}
}

// TestClusterAdmin_TenantGrantedPermissionsAreDenied covers the cluster
// permissions no built-in role below Owner carries. A tenant's SO holds
// roles:create and roles:assign, so it can grant them to itself through a
// custom role of its own tenant; the scope rule, not the missing built-in
// grant, is what must refuse it. The same grant in the system tenant is
// admitted.
func TestClusterAdmin_TenantGrantedPermissionsAreDenied(t *testing.T) {
	const other = "acme"
	perms := []permission.Permission{
		permission.AdminStartShard, permission.AdminStopShard,
		permission.AdminShardState, permission.AdminTopology,
		permission.RebalanceExecute, permission.RebalancePlan,
		permission.RebalanceReport, permission.RebalanceStats,
		// Shard backups hold every tenant's data on the shard.
		permission.BackupCreate, permission.BackupDelete, permission.BackupList,
		permission.BackupRead, permission.BackupRestore, permission.BackupVerify,
	}
	roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, other)
	authz := rbac.New(roles)
	for _, tid := range []string{tenant.SystemTenantID, other} {
		id := authtest.Grant(t, roles, authtest.SOIdentity(tid, "officer@"+tid), perms...)
		for _, p := range perms {
			err := authz.Authorize(tenant.WithTenantID(context.Background(), tid), id, permission.Resource(p), permission.Action(p))
			if tid == tenant.SystemTenantID {
				if err != nil {
					t.Errorf("system-tenant holder refused %s: %v", p, err)
				}
				continue
			}
			var denied *rbac.PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Errorf("tenant holder of %s: got %v, want *rbac.PermissionDeniedError", p, err)
			}
		}
	}
}
