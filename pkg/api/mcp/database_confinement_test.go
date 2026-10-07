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
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestDatabaseConfinement: over a real MCP session, with the real RBAC and
// built-in roles, a system-tenant Admin, Security Officer or Auditor holding
// every database permission is answered CodeNotFound on another tenant's
// database, on every database tool -- the data and update verbs, seal, unseal
// and the share-holder ones -- exactly as for a missing database, the list
// does not name it, and the database is left as it was. There is no cross-tenant database access.
func TestDatabaseConfinement(t *testing.T) {
	tools := map[string]map[string]any{
		"qrdb_database_get":                  {"name": "vault"},
		"qrdb_database_drop":                 {"name": "vault"},
		"qrdb_database_update_sql":           {"name": "vault"},
		"qrdb_database_update_protocol":      {"name": "vault"},
		"qrdb_database_update_etcd":          {"name": "vault"},
		"qrdb_database_update_raft":          {"name": "vault"},
		"qrdb_database_update_barrier":       {"name": "vault"},
		"qrdb_database_update_connection":    {"name": "vault"},
		"qrdb_database_seal":                 {"name": "vault"},
		"qrdb_database_unseal":               {"name": "vault", "secret": "c2VjcmV0"},
		"qrdb_database_barrier_status":       {"name": "vault"},
		"qrdb_database_barrier_officer_list": {"database": "vault"},
		"qrdb_database_barrier_quorum_begin": {"database": "vault", "op": "unseal", "username": "alice"},
		"qrdb_database_barrier_rekey_start":  {"database": "vault", "threshold": 2},
	}
	for name, id := range authtest.SystemDatabaseCallers() {
		t.Run(name, func(t *testing.T) {
			fx := servicetest.NewShamirDatabaseOwnedBy(t, "acme", "vault", 2, 2)
			roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
			mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
			tlsCfg := testTLSConfig(t)
			srv := NewServer(nil, nil, "127.0.0.1:0",
				WithTLS(tlsCfg), WithTenantService(authtest.NewTenants(t, tenantid.System, "acme")),
				WithDatabaseService(fx.Service), WithAuthMiddleware(mw))
			if err := srv.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(srv.Stop)
			httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
			}}}
			client, err := mcpclient.NewStreamableHttpClient("https://"+srv.Addr()+"/mcp", transport.WithHTTPBasicClient(httpClient))
			if err != nil {
				t.Fatalf("client: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.Start(callCtx(t)); err != nil {
				t.Fatalf("client start: %v", err)
			}
			init := mcpgo.InitializeRequest{}
			init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
			init.Params.ClientInfo = mcpgo.Implementation{Name: "database-confinement-test", Version: "1"}
			if _, err := client.Initialize(callCtx(t), init); err != nil {
				t.Fatalf("initialize: %v", err)
			}

			for tool, args := range tools {
				req := mcpgo.CallToolRequest{}
				req.Params.Name, req.Params.Arguments = tool, args
				res, err := client.CallTool(callCtx(t), req)
				if err != nil {
					t.Fatalf("%s: %v", tool, err)
				}
				payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
				if !res.IsError || !ok || payload.Code != wire.CodeNotFound {
					t.Errorf("%s on acme's database = %+v, want CodeNotFound, as for a missing database", tool, res.Content)
				}
			}
			if _, err := fx.Service.Create(database.DatabaseConfig{Name: "sysdb", Backend: database.BackendMemory}); err != nil {
				t.Fatalf("create the system tenant's own database: %v", err)
			}
			// The list names the caller's own databases and only those.
			listReq := mcpgo.CallToolRequest{}
			listReq.Params.Name = "qrdb_database_list"
			list, err := client.CallTool(callCtx(t), listReq)
			if err != nil || list.IsError {
				t.Fatalf("qrdb_database_list: %v %+v", err, list)
			}
			var listed strings.Builder
			for _, c := range list.Content {
				if text, ok := c.(mcpgo.TextContent); ok {
					listed.WriteString(text.Text)
				}
			}
			if strings.Contains(listed.String(), `"vault"`) || !strings.Contains(listed.String(), `"sysdb"`) {
				t.Errorf("qrdb_database_list = %s, want the caller's sysdb and not acme's vault", listed.String())
			}
			if _, err := fx.Service.Get(fx.Name); err != nil {
				t.Fatalf("acme's database was changed: %v", err)
			}
		})
	}
}
