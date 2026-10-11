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

package mcp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// firstContactTools are an embedder's pre-authentication tools: the shape of
// a login ceremony plus liveness and readiness probes. None reaches data.
var firstContactTools = []string{
	"example_login_begin",
	"example_login_finish",
	"example_liveness",
	"example_readiness",
}

// publicToolsModule registers the first-contact tools and declares them, plus
// the given extra names, public.
type publicToolsModule struct {
	apimodule.BaseModule
	extra []string
}

func (m *publicToolsModule) Name() string { return "public-tools-module" }

func (m *publicToolsModule) RegisterMCP(srv *mcpserver.MCPServer) {
	for _, name := range firstContactTools {
		srv.AddTool(mcpgo.NewTool(name), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("first-contact-ok"), nil
		})
	}
}

func (m *publicToolsModule) PublicMCPTools() []string {
	return append(append([]string{}, firstContactTools...), m.extra...)
}

// denyAllAuthorizer refuses every grant.
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return &auth.AuthError{Code: "unauthorized", Message: "denied"}
}

// startPublicToolsMCP starts a TLS MCP server with bearer authentication,
// the given authorizer and a module declaring qrdb_health and the
// first-contact tools public.
func startPublicToolsMCP(t *testing.T, authz auth.Authorizer) (string, *http.Client) {
	t.Helper()
	mw := authmw.New(authz, authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw),
		WithServiceModules([]apimodule.ServiceModule{&publicToolsModule{extra: []string{"qrdb_health"}}}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(client.CloseIdleConnections)
	return "https://" + srv.Addr() + "/mcp", client
}

// mcpSession drives one MCP session over HTTP, carrying the session id the
// server assigns at initialize.
type mcpSession struct {
	t             *testing.T
	url           string
	client        *http.Client
	authorization string
	id            string
}

func (s *mcpSession) post(body string) (int, string) {
	s.t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.url, strings.NewReader(body))
	if err != nil {
		s.t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.authorization != "" {
		req.Header.Set("Authorization", s.authorization)
	}
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("POST: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		s.id = id
	}
	return resp.StatusCode, string(got)
}

const (
	initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	toolsListBody  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
)

func callToolBody(name string) string {
	return `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
}

func (s *mcpSession) initialize() {
	s.t.Helper()
	if status, body := s.post(initializeBody); status != http.StatusOK {
		s.t.Fatalf("initialize: status %d, body %q", status, body)
	}
}

// TestMCP_PublicTools_AnonymousFirstContact: with public tools declared, an
// anonymous caller can initialize, list exactly the public set, and call a
// public tool -- the first-contact surface an embedder needs before login.
func TestMCP_PublicTools_AnonymousFirstContact(t *testing.T) {
	url, client := startPublicToolsMCP(t, &auth.NoopAuthorizer{})
	s := &mcpSession{t: t, url: url, client: client}
	s.initialize()

	status, body := s.post(toolsListBody)
	if status != http.StatusOK {
		t.Fatalf("tools/list: status %d, body %q", status, body)
	}
	for _, name := range append(append([]string{}, firstContactTools...), "qrdb_health") {
		if !strings.Contains(body, `"`+name+`"`) {
			t.Errorf("anonymous tools/list lacks public tool %s: %q", name, body)
		}
	}
	for _, hidden := range []string{"qrdb_put", "qrdb_get", "qrdb_status"} {
		if strings.Contains(body, `"`+hidden+`"`) {
			t.Errorf("anonymous tools/list disclosed %s", hidden)
		}
	}

	for _, name := range []string{"example_login_begin", "qrdb_health"} {
		status, body = s.post(callToolBody(name))
		if status != http.StatusOK || strings.Contains(body, `"isError":true`) {
			t.Errorf("anonymous call of public %s: status %d, body %q", name, status, body)
		}
	}
}

// TestMCP_PublicTools_AnonymousDataCallRefused: declaring public tools opens
// nothing else -- an anonymous call of any other tool is still a 401 refused
// before mcp-go reads it.
func TestMCP_PublicTools_AnonymousDataCallRefused(t *testing.T) {
	url, client := startPublicToolsMCP(t, &auth.NoopAuthorizer{})
	s := &mcpSession{t: t, url: url, client: client}
	s.initialize()
	for _, name := range []string{"qrdb_put", "qrdb_get", "no_such_tool"} {
		if status, body := s.post(callToolBody(name)); status != http.StatusUnauthorized {
			t.Errorf("anonymous call of %s: status %d, want 401; body %q", name, status, body)
		}
	}
	if status, _ := s.post(`{"jsonrpc":"2.0","id":4,"method":"ping"}`); status != http.StatusUnauthorized {
		t.Errorf("anonymous ping: status %d, want 401", status)
	}
}

// TestMCP_PublicTools_AuthenticatedListFiltered: an authenticated caller sees
// the public set plus the tools its grants allow.
func TestMCP_PublicTools_AuthenticatedListFiltered(t *testing.T) {
	cases := map[string]struct {
		authz      auth.Authorizer
		wantPut    bool
		wantHealth bool
	}{
		"granted": {authz: &auth.NoopAuthorizer{}, wantPut: true, wantHealth: true},
		"denied":  {authz: denyAllAuthorizer{}, wantPut: false, wantHealth: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			url, client := startPublicToolsMCP(t, tc.authz)
			s := &mcpSession{t: t, url: url, client: client, authorization: "Bearer good"}
			s.initialize()
			status, body := s.post(toolsListBody)
			if status != http.StatusOK {
				t.Fatalf("tools/list: status %d, body %q", status, body)
			}
			if got := strings.Contains(body, `"qrdb_put"`); got != tc.wantPut {
				t.Errorf("qrdb_put listed = %v, want %v", got, tc.wantPut)
			}
			if got := strings.Contains(body, `"qrdb_health"`); got != tc.wantHealth {
				t.Errorf("qrdb_health listed = %v, want %v", got, tc.wantHealth)
			}
		})
	}
}

// TestMCP_PublicTools_DataToolRefusedAtStart: a module declaring a data tool
// public is refused with a typed error, and the server never starts.
func TestMCP_PublicTools_DataToolRefusedAtStart(t *testing.T) {
	for _, tool := range []string{"qrdb_put", "qrdb_status"} {
		t.Run(tool, func(t *testing.T) {
			srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
				WithTLS(testTLSConfig(t)), WithServiceModules([]apimodule.ServiceModule{&publicToolsModule{extra: []string{tool}}}))
			err := srv.Start()
			if err == nil {
				srv.Stop()
				t.Fatal("Start accepted a public data tool")
			}
			var refused *authmw.PublicMCPToolRefusedError
			if !errors.As(err, &refused) || refused.Tool != tool {
				t.Fatalf("want *PublicMCPToolRefusedError for %s, got %T: %v", tool, err, err)
			}
			if srv.toolPerms.IsPublic(tool) {
				t.Fatalf("%s became public despite the refusal", tool)
			}
		})
	}
}

// TestMCP_PublicTools_UnknownToolRefusedAtStart: a declared name no tool
// answers is refused with *UnknownPublicToolError.
func TestMCP_PublicTools_UnknownToolRefusedAtStart(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(testTLSConfig(t)), WithServiceModules([]apimodule.ServiceModule{&publicToolsModule{extra: []string{"example_missing"}}}))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start accepted an unregistered public tool")
	}
	var unknown *UnknownPublicToolError
	if !errors.As(err, &unknown) || unknown.Tool != "example_missing" {
		t.Fatalf("want *UnknownPublicToolError, got %T: %v", err, err)
	}
}

func TestUnknownPublicToolError_Message(t *testing.T) {
	msg := (&UnknownPublicToolError{Tool: "example_missing"}).Error()
	for _, want := range []string{"example_missing", "not registered"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

// TestRegisterPublicTools_NoListerNoPublic: a module that declares nothing
// leaves the registry with no public tools.
func TestRegisterPublicTools_NoListerNoPublic(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), ":0", WithServiceModules([]apimodule.ServiceModule{&bareModule{}}))
	if srv.publicToolsErr != nil {
		t.Fatalf("registerPublicTools: %v", srv.publicToolsErr)
	}
	if srv.toolPerms.HasPublic() {
		t.Fatal("no module declared a public tool")
	}
}

func TestRegisterPublicTools_RecordsDeclared(t *testing.T) {
	srv := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithServiceModules([]apimodule.ServiceModule{&publicToolsModule{extra: []string{"qrdb_health"}}}))
	if srv.publicToolsErr != nil {
		t.Fatalf("registerPublicTools: %v", srv.publicToolsErr)
	}
	for _, name := range append(append([]string{}, firstContactTools...), "qrdb_health") {
		if !srv.toolPerms.IsPublic(name) {
			t.Errorf("%s should be public", name)
		}
	}
}

// newPublicAdmissionServer builds an unstarted server with authentication
// and the first-contact tools declared public, for admitAnonymous.
func newPublicAdmissionServer(t *testing.T) *Server {
	t.Helper()
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	srv := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithAuthMiddleware(mw), WithServiceModules([]apimodule.ServiceModule{&publicToolsModule{}}))
	if srv.publicToolsErr != nil {
		t.Fatalf("registerPublicTools: %v", srv.publicToolsErr)
	}
	return srv
}

func TestAdmitAnonymous_Admitted(t *testing.T) {
	srv := newPublicAdmissionServer(t)
	for _, body := range []string{initializeBody, toolsListBody, callToolBody("example_readiness"),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		admitted, ok := srv.admitAnonymous(r, auth.ErrUnauthenticated)
		if !ok {
			t.Errorf("not admitted: %s", body)
			continue
		}
		if v, _ := admitted.Context().Value(anonymousAdmittedKey{}).(bool); !v {
			t.Errorf("admitted request lacks the admission mark: %s", body)
		}
		replayed, _ := io.ReadAll(admitted.Body)
		if string(replayed) != body {
			t.Errorf("body not replayed: got %q, want %q", replayed, body)
		}
	}
}

func TestAdmitAnonymous_Refused(t *testing.T) {
	srv := newPublicAdmissionServer(t)
	unavailable := &authmw.StoreUnavailableError{Err: errors.New("raft not ready")}
	cases := map[string]struct {
		method  string
		body    string
		authErr error
	}{
		"data tool":         {http.MethodPost, callToolBody("qrdb_put"), auth.ErrUnauthenticated},
		"ping":              {http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, auth.ErrUnauthenticated},
		"batch":             {http.MethodPost, "[" + toolsListBody + "]", auth.ErrUnauthenticated},
		"get":               {http.MethodGet, "", auth.ErrUnauthenticated},
		"malformed":         {http.MethodPost, "{", auth.ErrUnauthenticated},
		"oversized":         {http.MethodPost, toolsListBody + strings.Repeat(" ", int(maxAnonymousRequestBodyBytes)), auth.ErrUnauthenticated},
		"tools/list on 503": {http.MethodPost, toolsListBody, unavailable},
		"initialize on 503": {http.MethodPost, initializeBody, unavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/mcp", strings.NewReader(tc.body))
			if _, ok := srv.admitAnonymous(r, tc.authErr); ok {
				t.Fatal("admitted")
			}
		})
	}
}

// TestAdmitAnonymous_PublicCallSurvivesUnavailable: a public tool needs no
// identity, so a failure to establish one -- even a 503-class one -- does not
// stop it being served.
func TestAdmitAnonymous_PublicCallSurvivesUnavailable(t *testing.T) {
	srv := newPublicAdmissionServer(t)
	unavailable := &authmw.StoreUnavailableError{Err: errors.New("raft not ready")}
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(callToolBody("example_liveness")))
	if _, ok := srv.admitAnonymous(r, unavailable); !ok {
		t.Fatal("a public tool call must be admitted whatever the authentication failure")
	}
}

func TestAdmitAnonymous_NoPublicToolsRefusesAll(t *testing.T) {
	srv := newBearerTestServer(t, "")
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeBody))
	if _, ok := srv.admitAnonymous(r, auth.ErrUnauthenticated); ok {
		t.Fatal("a server with no public tools must admit nothing anonymously")
	}
}

// awaitingProbe reports the node is awaiting its admin credential.
type awaitingProbe bool

func (p awaitingProbe) AwaitingAdminCredential() bool { return bool(p) }

func TestVisibleTools_AnonymousSeesPublicSet(t *testing.T) {
	srv := newPublicAdmissionServer(t)
	tools := []mcpgo.Tool{mcpgo.NewTool("example_login_begin"), mcpgo.NewTool("qrdb_put")}
	got := srv.visibleTools(context.Background(), tools)
	if len(got) != 1 || got[0].Name != "example_login_begin" {
		t.Fatalf("visible = %v, want only example_login_begin", toolNames(got))
	}
}

func TestVisibleTools_AwaitingAdminCredentialListsAllowlist(t *testing.T) {
	srv := newPublicAdmissionServer(t)
	var allowed string
	for name := range AwaitingAdminCredentialAllowlist {
		allowed = name
		break
	}
	if allowed == "" {
		t.Fatal("awaiting-admin-credential allowlist is empty")
	}
	tools := []mcpgo.Tool{mcpgo.NewTool(allowed), mcpgo.NewTool("qrdb_put")}

	if got := srv.visibleTools(context.Background(), tools); len(got) != 0 {
		t.Fatalf("not awaiting: visible = %v, want none", toolNames(got))
	}
	WithAwaitingAdminCredentialProbe(awaitingProbe(true))(srv)
	got := srv.visibleTools(context.Background(), tools)
	if len(got) != 1 || got[0].Name != allowed {
		t.Fatalf("awaiting: visible = %v, want only %s", toolNames(got), allowed)
	}
}

func toolNames(tools []mcpgo.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}
