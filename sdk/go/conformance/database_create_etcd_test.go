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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// seedDatabaseCreateIdentity grants the matrix's client certificate exactly
// the permission "database create" needs, the same shape
// seedGuardedIdentity (harness_test.go) uses for the guarded KV plane. It
// returns the role id so the embedded cell, which authenticates nothing on
// its own, can bind the same identity into its own context.
func seedDatabaseCreateIdentity(t *testing.T, n *node.Node) (roleID string) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)

	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "conformance-db-create",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.DatabasesCreate, permission.AdminHealth, permission.AdminStatus},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "matrix",
		TenantID: tenantid.System,
		RoleIDs:  []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return r.ID
}

// TestDatabaseCreate_WithEtcd_EveryTransport is the conformance check for a
// parity bug a review found: REST's and MCP's "database create" (the REST
// server handler, the MCP server tool handler, and the MCP and embedded SDK
// clients building their respective requests) silently DROPPED an etcd
// protocol block on every build, because only MySQL and PostgreSQL were
// carried through -- gRPC, QUIC and Unix already carried it in both their
// server handlers and their SDK clients. A caller asking for etcd on REST,
// MCP or the embedded transport got a database with etcd quietly left off
// and no error: the same "config accepted, nothing happens" defect class
// pkg/service.RefuseEtcdNotCompiled exists to refuse instead of accepting
// silently -- the refusal (tested directly, with no transport axis, by
// pkg/service's database_etcd_capability_test.go, !qrdb_etcd) is only as
// good as every caller actually reaching it, which is what this test
// certifies for the opposite case: etcd requested on a build that HAS the
// tag must actually be enabled, on every transport, not silently dropped by
// one of them.
//
// This walks every SDK transport and asserts CreateDatabase with
// Protocol.Etcd set actually enables it, so the six clients cannot drift
// again without this test catching it.
func TestDatabaseCreate_WithEtcd_EveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			var roleID string
			client := bootAuthorizedCell(t, tk, func(n *node.Node) { roleID = seedDatabaseCreateIdentity(t, n) })
			ctx := boundedCtx(t)
			if tk == embeddedTransport {
				// The embedded transport authenticates nothing on its own;
				// bind the same identity and tenant the remote cells
				// authenticate as via their client certificate (the pair
				// every embedded authorization gate reads: auth.WithIdentity
				// paired with tenant.WithTenantID).
				ctx = tenant.WithTenantID(auth.WithIdentity(ctx, &auth.Identity{
					Subject:  "matrix",
					TenantID: tenantid.System,
					Roles:    []string{roleID},
				}), tenantid.System)
			}

			info, err := client.Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
				Name:    "etcd-" + string(tk),
				Backend: "memory",
				Protocol: transport.ProtocolConfig{
					Etcd: &transport.EtcdProtocolConfig{
						Enabled:    true,
						ListenAddr: "127.0.0.1:0",
					},
				},
			})
			if err != nil {
				t.Fatalf("%s: CreateDatabase with etcd: %v", tk, err)
			}
			if info.Protocol.Etcd == nil {
				t.Fatalf("%s: created database reports no etcd config at all", tk)
			}
			if !info.Protocol.Etcd.Enabled {
				t.Fatalf("%s: created database reports etcd disabled, want enabled", tk)
			}
			if info.Protocol.Etcd.ListenAddr != "127.0.0.1:0" {
				t.Fatalf("%s: etcd listen addr = %q, want %q", tk, info.Protocol.Etcd.ListenAddr, "127.0.0.1:0")
			}
		})
	}
}
