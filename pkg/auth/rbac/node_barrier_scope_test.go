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
// the REAL built-in roles. For every node-barrier permission a built-in role
// carries, the role's holder in the system tenant is admitted and the same
// role's holder in a tenant is denied: the node barrier is a platform
// singleton, and none of these operations names a tenant the authorizer could
// compare. Every transport authorizes through this decision.
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
	}
	exercised := 0
	for _, name := range []string{role.RoleNameSO, role.RoleNameAdmin, role.RoleNameOperator} {
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
