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

package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleBarrierEnableEncryption_AuthorizesPerScope drives the registered
// qrdb_barrier_enable_encryption tool, behind real RBAC, through the shared
// per-scope case table (go-qrdb#791): the tool gate admits any authenticated
// caller, and the handler authorizes seal:enable_encryption for the node's
// barrier and tenant_barrier:enable_encryption for the named tenant's, before
// the migrator is reached.
func TestHandleBarrierEnableEncryption_AuthorizesPerScope(t *testing.T) {
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			roles := authtest.BuiltInRoles(t)
			mig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
			mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
			srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
				WithAuthMiddleware(mw), WithEncryptionMigrator(mig))
			ctx := auth.WithIdentity(context.Background(), c.Caller)
			tool := srv.mcpSrv.GetTool("qrdb_barrier_enable_encryption")
			if tool == nil {
				t.Fatal("qrdb_barrier_enable_encryption is not registered")
			}

			if err := mw.AuthorizeMCP(ctx, srv.toolPerms, "qrdb_barrier_enable_encryption"); err != nil {
				t.Fatalf("tool gate refused an authenticated caller: %v", err)
			}
			res, err := tool.Handler(ctx, buildCallToolRequest(t, "qrdb_barrier_enable_encryption", map[string]any{
				"tenant_id": c.TenantID, "strategy": "passphrase", "passphrase": "pw",
			}))

			if !c.Admit {
				if err != nil || res == nil || !res.IsError {
					t.Fatalf("result = %+v, err = %v, want an authorization refusal", res, err)
				}
				if n := len(mig.ch); n != 0 {
					t.Fatalf("a refused call reached the migrator %d time(s)", n)
				}
				return
			}
			if err != nil || res == nil || res.IsError {
				t.Fatalf("admitted call failed: result = %+v, err = %v", res, err)
			}
			want := service.BarrierScope{Kind: service.ScopeSystem}
			if c.TenantID != "" {
				want = service.BarrierScope{Kind: service.ScopeTenant, TenantID: c.TenantID}
			}
			select {
			case call := <-mig.ch:
				if call.scope != want {
					t.Fatalf("migrator scope = %+v, want %+v", call.scope, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("an admitted call did not reach the migrator")
			}
		})
	}
}
