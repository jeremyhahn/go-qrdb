// Copyright 2026 Jeremy Hahn
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

// No build constraint: startMCP (so_dual_factor_test.go), mockAdminService
// (server_test.go) and callCtx, wantToolCode (tenant_isolation_test.go) are
// unconstrained.

package mcp

import (
	"sync/atomic"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestClusterAdminScope: a tenant's Admin or SO may not add or remove a
// replica, request a snapshot or drain the node over MCP. The cluster is a
// platform asset addressed by shard id, and the built-in Admin and SO roles
// grant these in every tenant.
func TestClusterAdminScope(t *testing.T) {
	ops := []struct {
		tool string
		args map[string]any
	}{
		{"qrdb_add_member", map[string]any{"shard_id": 1, "replica_id": 9, "raft_addr": "127.0.0.1:1"}},
		{"qrdb_remove_member", map[string]any{"shard_id": 1, "replica_id": 2}},
		{"qrdb_snapshot", map[string]any{"shard_id": 1}},
		{"qrdb_drain", nil},
		// The node keyspace's compaction policy: the platform's, and its
		// compaction discards every tenant's raw history on the node.
		{"qrdb_node_compaction", nil},
		{"qrdb_update_node_compaction", map[string]any{"mode": "revision", "retainRevisions": 10}},
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, authtest.ClusterAdminTenant)
	for _, op := range ops {
		for _, c := range authtest.ClusterAdminCallers(t) {
			t.Run(op.tool+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				admin := &authtest.Admin{AdminServicer: &mockAdminService{}}
				client := startMCP(t, admin, &atomic.Pointer[string]{},
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))))

				req := mcpgo.CallToolRequest{}
				req.Params.Name = op.tool
				req.Params.Arguments = op.args
				res, err := client.CallTool(callCtx(t), req)
				if err != nil {
					t.Fatalf("%s: %v", op.tool, err)
				}
				if c.Admitted {
					if res.IsError || admin.ClusterChanges() != 1 {
						t.Fatalf("result=%+v, calls=%d; want served once", res, admin.ClusterChanges())
					}
					return
				}
				wantToolCode(t, op.tool, res, wire.CodePermissionDenied)
				if admin.ClusterChanges() != 0 {
					t.Fatalf("a refused %s reached the admin service", op.tool)
				}
			})
		}
	}
}
