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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// grantKV grants kv:put/kv:get in tenantID to the user named username, on top
// of whatever roles it already holds. bootConfinedCell's seeded Admin does
// not itself carry data-plane KV permissions (it is scoped to administrative
// actions), and the built-in Admin role cannot be re-permissioned (it is a
// system role), so a confinement test over the KV surface needs its own,
// explicitly-granted role.
func grantKV(t *testing.T, n *node.Node, tenantID, username string) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)

	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "kv-confinement-check",
		TenantID:    tenantID,
		Permissions: []permission.Permission{permission.KVPut, permission.KVGet},
	})
	if err != nil {
		t.Fatalf("create KV role: %v", err)
	}
	u, err := svc.User.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		t.Fatalf("GetUserByUsername(%s): %v", username, err)
	}
	roleIDs := append([]string{}, u.RoleIDs...)
	roleIDs = append(roleIDs, r.ID)
	if _, err := svc.User.UpdateUser(ctx, &user.UpdateUserRequest{ID: u.ID, RoleIDs: roleIDs}); err != nil {
		t.Fatalf("grant KV role to %s: %v", username, err)
	}
}

// TestCertRecords_NotReadableThroughGenericKV proves that the CA records
// cakey.SoftwareProvider and cert.DefaultCertProvider persist --
// __sys:cert_ca/<tenantID>/... (Key B, barrier-wrapped) and
// __sys:cluster_ca/... (Key A, barrier-wrapped) -- are never readable
// through the generic KV surface, by ANY tenant, on ANY transport: not a
// tenant reading its OWN Key B record by guessing the path, and not a
// cross-tenant or cluster-level read either.
//
// pkg/service/internal_keys.go's reserved-prefix guard is explicitly a
// DISCOVERY guard on bulk enumeration, not an authorization boundary: it
// does not stop a caller that already knows a key from reading it with Get.
// So the protection here has to come from elsewhere -- tenant keyspace
// confinement (pkg/tenant) rewriting or rejecting a caller's own key before
// it ever reaches the store -- and this test is what proves that holds for
// the CA prefixes specifically, since they are exactly the keys an attacker
// with the storage layout (this architecture's own docs) would try first.
func TestCertRecords_NotReadableThroughGenericKV(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			grantKV(t, c.node, confineAcme, "matrix")

			// Force Key B material to actually exist for acme, so a
			// successful read would be a real secret disclosure and not
			// merely a not-yet-initialized namespace.
			issueThroughNode(t, c.node, "alice", confineAcme)

			// Positive control: acme's Admin CAN read and write its own
			// ordinary keys. Without this, every assertion below would also
			// pass if KV.Get refused every key unconditionally, which would
			// prove nothing about the CA prefixes specifically.
			if err := c.client.KV().Put(c.ctx, "ordinary-key", []byte("v")); err != nil {
				t.Fatalf("%s: positive control Put failed: %v", tk, err)
			}
			if got, err := c.client.KV().Get(c.ctx, "ordinary-key"); err != nil || string(got) != "v" {
				t.Fatalf("%s: positive control Get failed: got %q, err %v", tk, got, err)
			}

			keys := []string{
				// Key B: acme's OWN login CA, barrier-wrapped private key
				// and public certificate.
				"__sys:cert_ca/" + confineAcme + "/wrapped_key",
				"__sys:cert_ca/" + confineAcme + "/cert_pem",
				// Key B: a DIFFERENT tenant's login CA.
				"__sys:cert_ca/" + confineGlobex + "/wrapped_key",
				// Key A: the cluster CA.
				"__sys:cluster_ca/wrapped_key",
				"__sys:cluster_ca/cert_pem",
			}
			for _, key := range keys {
				val, err := c.client.KV().Get(c.ctx, key)
				if err == nil && len(val) > 0 {
					t.Errorf("%s: acme's Admin read %q through the generic KV surface: %q",
						tk, key, val)
				}
			}
		})
	}
}
