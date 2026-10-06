// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conformance

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestAddCertBinding_KeyCustodyStaysWithTheCeremony: binding a thumbprint is
// binding a key, so users:add_cert_binding holds the SAME certificate target
// rule certs:issue and certs:enroll apply (pkg/service.certTargetGuard), on
// every transport -- the built-in tenant Admin may bind a key to an
// ordinary account, but not to the tenant's Security Officer, whose
// certificates come only from the ceremony.
//
// There is no Custodian role to probe here: a custodian authenticates by
// holder certificate (pkg/holderprofile), never by user account, so it is
// never a users:add_cert_binding target to begin with -- see
// pkg/service.certTargetGuard's own doc comment. The Owner-target half of
// the guard (refused unless the CALLER itself holds Owner) is covered at
// the unit level instead
// (pkg/service.TestCertTargetGuard_RefusesSOAndOwnerTargets and
// TestCertTargetGuard_OwnerCallerMayTargetOwner): this cell's tenant, like
// every onboarding conformance cell in this file, provisions through
// bootOnboardingCell/onboardWithCSR, which does not seed an Owner role.
func TestAddCertBinding_KeyCustodyStaysWithTheCeremony(t *testing.T) {
	for _, tk := range remoteTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootOnboardingCell(t, tk)
			adminCert, adminKey := c.onboardWithCSR(t, "acme", "alice")
			alice := c.mustLogin(t, adminCert, adminKey)

			ctx := boundedCtx(t)
			svc := c.n.Services()
			mk := func(username, roleName string) uint64 {
				t.Helper()
				var roleIDs []string
				if roleName != "" {
					r, err := svc.Role.GetRoleByName(tenant.WithTenantID(ctx, "acme"), "acme", roleName)
					if err != nil {
						t.Fatalf("role %s: %v", roleName, err)
					}
					roleIDs = []string{r.ID}
				}
				u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
					WithoutEmailLoginKey: true, Username: username, TenantID: "acme", RoleIDs: roleIDs,
				})
				if err != nil {
					t.Fatalf("create %s: %v", username, err)
				}
				return u.ID
			}
			// alice holds only the built-in tenant Admin role, which carries
			// users:add_cert_binding: the custody guard alone decides.
			sam := mk("sam", role.RoleNameSO)
			uma := mk("uma", role.RoleNameUser)

			_, soErr := alice.Users().AddCertBinding(boundedCtx(t), sam, withdrawnThumbprint, 0)
			if reason := classifyCrossTenantRefusal(tk, soErr); reason != nil {
				t.Fatalf("%s: binding a key to sam: %v", tk, reason)
			}
			if soErr == nil || !strings.Contains(soErr.Error(), "holds a role certificate issuance may not target") {
				t.Fatalf("%s: binding a key to sam was refused, but not by the target rule: %v", tk, soErr)
			}
			got, err := alice.Users().AddCertBinding(boundedCtx(t), uma, retainedThumbprint, 0)
			if err != nil {
				t.Fatalf("%s: binding a key to an ordinary account: %v", tk, err)
			}
			if got == nil || got.ID != uma {
				t.Fatalf("%s: bound user = %+v, want uma", tk, got)
			}
			if _, err := svc.User.UserByCertThumbprint(tenant.WithTenantID(ctx, "acme"), withdrawnThumbprint); err == nil {
				t.Fatalf("%s: a refused binding resolves an account", tk)
			}
		})
	}
}
