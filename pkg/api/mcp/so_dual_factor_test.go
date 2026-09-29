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

// The SO dual-factor gate over a real MCP session: streamable HTTP over TLS,
// driven by mcp-go's own client, which sends the SO PIN in the X-QRDB-SO-PIN
// header. Every call passes the tool-authorization middleware, a REAL
// rbac.RBACAuthorizer over the REAL built-in roles and the REAL
// SOAuthenticator. The only double is the authenticator, standing in for the
// mTLS handshake.
//
// No build constraint: testTLSConfig (server_test.go) and callCtx,
// wantToolCode (tenant_isolation_test.go) are unconstrained.

package mcp

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync/atomic"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	soTestTenant  = tenantid.System
	soOtherTenant = "acme"
	soTestPIN     = "correct horse battery staple"
)

type soFixture struct {
	client  *mcpclient.Client
	caller  *authtest.FixedAuthenticator
	barrier *authtest.Barrier
	pin     *atomic.Pointer[string]
}

func newSOFixture(t *testing.T, gated bool) *soFixture {
	t.Helper()
	caller := authtest.NewFixedAuthenticator(authtest.SOIdentity(soTestTenant, "officer"))
	roles := authtest.BuiltInRoles(t, soTestTenant, soOtherTenant)
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	var gate *authmw.SOGate
	if gated {
		gate = authmw.NewSOGate(roles, authtest.SOVerifier(t, soTestPIN, soTestTenant, soOtherTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	}
	barrier := &authtest.Barrier{}
	pin := &atomic.Pointer[string]{}
	client := startBarrierMCP(t, mw, gate, barrier, pin)
	return &soFixture{client: client, caller: caller, barrier: barrier, pin: pin}
}

// startBarrierMCP starts an MCP server over barrier with mw as its auth
// middleware and gate as its SO gate (nil: off), and returns an initialized
// client that presents the PIN pin holds, if any, in the SO PIN header.
func startBarrierMCP(t *testing.T, mw *authmw.AuthMiddleware, gate *authmw.SOGate, barrier *authtest.Barrier, pin *atomic.Pointer[string]) *mcpclient.Client {
	t.Helper()
	return startMCP(t, nil, pin, WithAuthMiddleware(mw), WithBarrierService(barrier), WithSOGate(gate))
}

// startMCP starts an MCP server over admin (which may be nil) with opts, and
// returns an initialized client that presents the PIN pin holds, if any, in
// the SO PIN header.
func startMCP(t *testing.T, admin service.AdminServicer, pin *atomic.Pointer[string], opts ...ServerOption) *mcpclient.Client {
	t.Helper()
	tlsCfg := testTLSConfig(t)
	srv := NewServer(nil, admin, "127.0.0.1:0", append([]ServerOption{WithTLS(tlsCfg)}, opts...)...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient("https://"+srv.Addr()+"/mcp",
		transport.WithHTTPBasicClient(httpClient),
		transport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
			if p := pin.Load(); p != nil && *p != "" {
				return map[string]string{authmw.SOPINHeader: *p}
			}
			return nil
		}))
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
	init.Params.ClientInfo = mcpgo.Implementation{Name: "so-dual-factor-test", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return client
}

// rekey calls qrdb_barrier_rekey as id, presenting pin in the header.
func (f *soFixture) rekey(t *testing.T, id *auth.Identity, pin string) *mcpgo.CallToolResult {
	t.Helper()
	f.caller.Set(id)
	f.pin.Store(&pin)
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_barrier_rekey"
	req.Params.Arguments = map[string]any{"threshold": 2, "total": 3}
	res, err := f.client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("qrdb_barrier_rekey: %v", err)
	}
	return res
}

func TestSODualFactor(t *testing.T) {
	officer := authtest.SOIdentity(soTestTenant, "officer")
	token := authtest.GrantedNonSOIdentity(soTestTenant, "token", permission.SealRekey)

	t.Run("officer with PIN is served", func(t *testing.T) {
		f := newSOFixture(t, true)
		if res := f.rekey(t, officer, soTestPIN); res.IsError {
			t.Fatalf("rekey failed: %+v", res.Content)
		}
		if f.barrier.Rekeys() != 1 {
			t.Fatalf("rekeys = %d, want 1", f.barrier.Rekeys())
		}
	})
	for _, tc := range []struct {
		name string
		id   *auth.Identity
		pin  string
		code wire.Code
	}{
		{"officer without PIN is refused", officer, "", wire.CodeUnauthenticated},
		{"officer with wrong PIN is refused", officer, "wrong", wire.CodeUnauthenticated},
		{"authorized non-SO caller is refused", token, soTestPIN, wire.CodePermissionDenied},
		// The node barrier is a platform singleton: a tenant's SO with its own
		// tenant's valid PIN must not rekey it (go-qrdb#569 N2).
		{"a tenant's SO is refused on the node barrier", authtest.SOIdentity(soOtherTenant, "acme-officer"), soTestPIN, wire.CodePermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSOFixture(t, true)
			wantToolCode(t, tc.name, f.rekey(t, tc.id, tc.pin), tc.code)
			if f.barrier.Rekeys() != 0 {
				t.Fatal("a refused rekey reached the service")
			}
		})
	}
	t.Run("gate off serves the same non-SO caller", func(t *testing.T) {
		f := newSOFixture(t, false)
		if res := f.rekey(t, token, ""); res.IsError {
			t.Fatalf("rekey with the gate off failed: %+v", res.Content)
		}
		if f.barrier.Rekeys() != 1 {
			t.Fatalf("rekeys = %d, want 1", f.barrier.Rekeys())
		}
	})
}

// TestBarrierSeal_TenantAdminRefused: the node barrier is platform scope, so
// a tenant's Admin -- who holds seal:seal in its own tenant -- may not seal
// it, while the system tenant's Admin may.
func TestBarrierSeal_TenantAdminRefused(t *testing.T) {
	seal := func(t *testing.T, id *auth.Identity) (*mcpgo.CallToolResult, *authtest.Barrier) {
		t.Helper()
		f := newSOFixture(t, false)
		f.caller.Set(id)
		req := mcpgo.CallToolRequest{}
		req.Params.Name = "qrdb_barrier_seal"
		res, err := f.client.CallTool(callCtx(t), req)
		if err != nil {
			t.Fatalf("qrdb_barrier_seal: %v", err)
		}
		return res, f.barrier
	}
	res, barrier := seal(t, authtest.AdminIdentity(soOtherTenant, "acme-admin"))
	if barrier.Seals() != 0 {
		t.Fatal("a tenant Admin sealed the node barrier")
	}
	wantToolCode(t, "tenant admin seal", res, wire.CodePermissionDenied)

	res, barrier = seal(t, authtest.AdminIdentity(tenantid.System, "root"))
	if res.IsError || barrier.Seals() != 1 {
		t.Fatalf("system Admin seal: %+v, seals=%d", res.Content, barrier.Seals())
	}
}

// TestSODualFactor_GateWithoutAuthFailsClosed: with nothing to authenticate
// the caller there is no SO to admit, so the gate refuses rather than being
// skipped (go-qrdb#569 N5).
func TestSODualFactor_GateWithoutAuthFailsClosed(t *testing.T) {
	roles := authtest.BuiltInRoles(t, soTestTenant)
	gate := authmw.NewSOGate(roles, authtest.SOVerifier(t, soTestPIN, soTestTenant), audit.NewRingBuffer(64), audit.FailClosed, nil)
	barrier := &authtest.Barrier{}
	tlsCfg := testTLSConfig(t)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(tlsCfg), WithBarrierService(barrier), WithSOGate(gate))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient("https://"+srv.Addr()+"/mcp",
		transport.WithHTTPBasicClient(httpClient),
		transport.WithHTTPHeaders(map[string]string{authmw.SOPINHeader: soTestPIN}))
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
	init.Params.ClientInfo = mcpgo.Implementation{Name: "so-gate-no-auth", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_barrier_rekey"
	req.Params.Arguments = map[string]any{"threshold": 2, "total": 3}
	res, err := client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("qrdb_barrier_rekey: %v", err)
	}
	if barrier.Rekeys() != 0 {
		t.Fatal("the gate was skipped for want of an auth middleware")
	}
	wantToolCode(t, "rekey through a gate with no auth", res, wire.CodePermissionDenied)
}
