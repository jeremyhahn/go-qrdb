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

// Tenant isolation on the tenant-administration surface, over a real MCP
// session: streamable HTTP over TLS, driven by mcp-go's own client.
//
// Every call passes the tool-authorization middleware, a REAL
// rbac.RBACAuthorizer over the REAL built-in roles and a REAL in-memory tenant
// service. The only double is the authenticator, standing in for the mTLS
// handshake.
//
// No build constraint: testTLSConfig (server_test.go) is unconstrained, and
// this surface ships in every build.

package mcp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// isolationFixture is one started MCP server holding the system tenant, acme
// and globex, with an acme Admin as the caller until as() rebinds it.
type isolationFixture struct {
	client  *mcpclient.Client
	tenants *authtest.Tenants
	caller  *authtest.FixedAuthenticator
}

func newIsolationFixture(t *testing.T, opts ...authmw.Option) *isolationFixture {
	t.Helper()
	tenants := authtest.NewTenants(t, tenantid.System, "acme", "globex")
	caller := authtest.NewFixedAuthenticator(authtest.AdminIdentity("acme", "acme-admin"))
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme", "globex")
	mw := authmw.New(rbac.New(roles), append([]authmw.Option{authmw.WithAuthenticator(caller)}, opts...)...)

	tlsCfg := testTLSConfig(t)
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithTLS(tlsCfg), WithTenantService(tenants), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient("https://"+srv.Addr()+"/mcp",
		transport.WithHTTPBasicClient(httpClient))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := callCtx(t)
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "tenant-isolation-test", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return &isolationFixture{client: client, tenants: tenants, caller: caller}
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (f *isolationFixture) as(id *auth.Identity) { f.caller.Set(id) }

func (f *isolationFixture) operator() { f.as(authtest.AdminIdentity(tenantid.System, "root")) }

// call invokes one tool and returns its result; a protocol-level failure is
// fatal, because every refusal here must arrive as a coded tool result.
func (f *isolationFixture) call(t *testing.T, tool string, args map[string]any) *mcpgo.CallToolResult {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := f.client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

// wantOK fails unless res is a success.
func wantOK(t *testing.T, what string, res *mcpgo.CallToolResult) {
	t.Helper()
	if res.IsError {
		t.Fatalf("%s failed: %+v", what, res.Content)
	}
}

// wantToolCode fails unless res is an error carrying code.
func wantToolCode(t *testing.T, what string, res *mcpgo.CallToolResult, code wire.Code) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("%s succeeded, want %v", what, code)
	}
	p, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatalf("%s: no coded error payload in %+v", what, res)
	}
	if p.Code != code {
		t.Fatalf("%s = %v (%s), want %v", what, p.Code, p.Message, code)
	}
}

// byIDTools returns the tenant-by-id tool calls against id.
func byIDTools(id string) []struct {
	name string
	args map[string]any
} {
	return []struct {
		name string
		args map[string]any
	}{
		{"qrdb_tenant_get", map[string]any{"id": id}},
		{"qrdb_tenant_update", map[string]any{"id": id, "name": "pwned"}},
		{"qrdb_tenant_update_quotas", map[string]any{"id": id, "max_databases": 1000}},
		{"qrdb_tenant_update_entitlements", map[string]any{"id": id, "allow_dedicated_wal": true}},
		{"qrdb_tenant_delete", map[string]any{"id": id}},
	}
}

// unchanged fails unless id still exists with its original name and no plan.
func (f *isolationFixture) unchanged(t *testing.T, id string) {
	t.Helper()
	got, err := f.tenants.GetTenant(context.Background(), id)
	if err != nil {
		t.Fatalf("%s after a refused call: %v", id, err)
	}
	if got.Name != id || got.Quotas != nil || got.Entitlements != nil || got.IsolationType != tenant.IsolationPrefix {
		t.Fatalf("a refused call changed %s: %+v", id, got)
	}
}

func TestTenantIsolation_TenantAdminCannotActOnAnotherTenant(t *testing.T) {
	for _, tc := range byIDTools("globex") {
		t.Run(tc.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			wantToolCode(t, "acme admin "+tc.name+" globex", f.call(t, tc.name, tc.args), wire.CodePermissionDenied)
			f.unchanged(t, "globex")
		})
	}
}

func TestTenantIsolation_TenantAdminCannotChangeItsOwnPlan(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
	}{
		{"quotas", "qrdb_tenant_update_quotas", map[string]any{"id": "acme", "max_databases": 1000}},
		{"entitlements", "qrdb_tenant_update_entitlements", map[string]any{"id": "acme", "allow_dedicated_wal": true}},
		{"isolation", "qrdb_tenant_update", map[string]any{"id": "acme", "isolation_type": string(tenant.IsolationBarrier)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIsolationFixture(t)
			wantToolCode(t, "acme admin setting acme's "+tc.name, f.call(t, tc.tool, tc.args), wire.CodePermissionDenied)
			f.unchanged(t, "acme")
		})
	}
}

func TestTenantIsolation_TenantAdminServedOnItsOwnTenant(t *testing.T) {
	f := newIsolationFixture(t)
	wantOK(t, "acme admin get acme", f.call(t, "qrdb_tenant_get", map[string]any{"id": "acme"}))
	wantOK(t, "acme admin renaming acme", f.call(t, "qrdb_tenant_update", map[string]any{"id": "acme", "name": "Acme Corp"}))
}

func TestTenantIsolation_PlatformOperatorSetsAPlanAndCreates(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	wantOK(t, "operator setting acme's quotas", f.call(t, "qrdb_tenant_update_quotas", map[string]any{"id": "acme", "max_databases": 1000}))
	wantOK(t, "operator granting acme entitlements", f.call(t, "qrdb_tenant_update_entitlements", map[string]any{"id": "acme", "allow_dedicated_wal": true}))
	wantOK(t, "operator creating a tenant", f.call(t, "qrdb_tenant_create", map[string]any{"id": "initech", "name": "initech"}))

	got, err := f.tenants.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Quotas == nil || got.Quotas.MaxDatabases != 1000 || got.Entitlements == nil || !got.Entitlements.AllowDedicatedWAL {
		t.Fatalf("the operator's plan did not reach acme: %+v", got)
	}
}

func TestTenantIsolation_IsolationChangeRefusedWhileTenantHoldsData(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	args := map[string]any{"id": "acme", "isolation_type": string(tenant.IsolationVolume)}
	f.tenants.HoldsData(t, true)
	wantToolCode(t, "operator changing a data-bearing tenant's isolation", f.call(t, "qrdb_tenant_update", args), wire.CodeFailedPrecondition)
	f.unchanged(t, "acme")
	f.tenants.HoldsData(t, false)
	wantOK(t, "operator changing an empty tenant's isolation", f.call(t, "qrdb_tenant_update", args))
}

func TestTenantIsolation_TenantAdminCannotCreateATenant(t *testing.T) {
	f := newIsolationFixture(t)
	wantToolCode(t, "acme admin create", f.call(t, "qrdb_tenant_create", map[string]any{"id": "initech", "name": "initech"}), wire.CodePermissionDenied)
	if _, err := f.tenants.GetTenant(context.Background(), "initech"); err == nil {
		t.Fatal("a refused create left a tenant behind")
	}
}

func listedTenantIDs(t *testing.T, f *isolationFixture) []string {
	t.Helper()
	res := f.call(t, "qrdb_tenant_list", nil)
	wantOK(t, "list", res)
	text, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("list content is %T, want text", res.Content[0])
	}
	var infos []tenant.TenantInfo
	if err := json.Unmarshal([]byte(text.Text), &infos); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]string, len(infos))
	for i := range infos {
		ids[i] = infos[i].ID
	}
	return ids
}

func TestTenantIsolation_ListShowsATenantAdminOnlyItsOwnTenant(t *testing.T) {
	f := newIsolationFixture(t)
	if ids := listedTenantIDs(t, f); len(ids) != 1 || ids[0] != "acme" {
		t.Fatalf("acme admin listed %v, want [acme]", ids)
	}
	f.operator()
	if ids := listedTenantIDs(t, f); len(ids) != 3 {
		t.Fatalf("operator listed %v, want all three tenants", ids)
	}
}

func TestTenantIsolation_SlashInTenantIDIsInvalidArgument(t *testing.T) {
	f := newIsolationFixture(t)
	f.operator()
	for _, tc := range byIDTools("acme/evil") {
		wantToolCode(t, tc.name+" acme/evil", f.call(t, tc.name, tc.args), wire.CodeInvalidArgument)
	}
	wantToolCode(t, "create acme/evil", f.call(t, "qrdb_tenant_create", map[string]any{"id": "acme/evil", "name": "x"}), wire.CodeInvalidArgument)
}

// TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit: the sealed
// window admits a tenant's own role-less identity to unseal, but it must not
// admit it past the limiter. Before the fix the second unseal reached the
// service.
func TestTenantIsolation_SealedWindowHonoursTheUnsealRateLimit(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	f := newIsolationFixture(t,
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(permission.TenantBarrierUnseal)))
	f.as(authtest.SealedIdentity("acme", "acme-admin"))

	args := map[string]any{"tenant_id": "acme", "key": "a2V5"}
	wantOK(t, "first unseal", f.call(t, "qrdb_tenant_barrier_unseal", args))
	wantToolCode(t, "second unseal", f.call(t, "qrdb_tenant_barrier_unseal", args), wire.CodeResourceExhausted)
	if n := f.tenants.Unseals(); n != 1 {
		t.Fatalf("unseals reaching the service = %d, want 1", n)
	}
}
