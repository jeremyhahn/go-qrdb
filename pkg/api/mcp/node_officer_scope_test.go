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

// The node barrier's Security Officer registry over a real MCP session, with
// the tool-authorization middleware running a REAL rbac.RBACAuthorizer over
// the REAL built-in roles. The only double is the authenticator.
//
// No build constraint: startBarrierMCP (so_dual_factor_test.go) and callCtx,
// wantToolCode (tenant_isolation_test.go) are unconstrained.

package mcp

import (
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
)

// TestNodeOfficerScope: the node barrier is a platform singleton, so only the
// system tenant may register its unseal or recovery officers or list them. A
// tenant's SO, and a tenant Admin its own tenant granted the officer
// permissions, are refused before the barrier service is reached.
func TestNodeOfficerScope(t *testing.T) {
	register := map[string]any{"username": "planted", "csr_pem": "csr"}
	ops := []struct {
		tool string
		args map[string]any
		hits func(b *authtest.Barrier) int64
	}{
		{"qrdb_barrier_officer_register", register, (*authtest.Barrier).OfficerRegistrations},
		{"qrdb_barrier_recovery_officer_register", register, (*authtest.Barrier).RecoveryOfficerRegistrations},
		{"qrdb_barrier_officer_list", nil, (*authtest.Barrier).OfficerReads},
		{"qrdb_barrier_officer_claim_status", nil, (*authtest.Barrier).OfficerReads},
	}
	for _, op := range ops {
		roles := authtest.BuiltInRoles(t, tenantid.System, authtest.NodeOfficerTenant)
		for _, c := range authtest.NodeOfficerCallers(t, roles) {
			t.Run(op.tool+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				barrier := &authtest.Barrier{}
				client := startBarrierMCP(t, authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller)), nil, barrier, &atomic.Pointer[string]{})

				req := mcpgo.CallToolRequest{}
				req.Params.Name = op.tool
				req.Params.Arguments = op.args
				res, err := client.CallTool(callCtx(t), req)
				if err != nil {
					t.Fatalf("%s: %v", op.tool, err)
				}
				if c.Admitted {
					if res.IsError || op.hits(barrier) != 1 {
						t.Fatalf("result=%+v, calls=%d; want served once", res, op.hits(barrier))
					}
					return
				}
				wantToolCode(t, op.tool, res, wire.CodePermissionDenied)
				if op.hits(barrier) != 0 {
					t.Fatalf("a refused %s reached the node barrier", op.tool)
				}
			})
		}
	}
}
