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
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeInvitations is a faithful service.InvitationServicer double: each
// method returns exactly what was configured.
type fakeInvitations struct {
	inviteFn func(context.Context, *service.InviteUserRequest) (*service.Invitation, error)
	redeemFn func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error)
}

var _ service.InvitationServicer = (*fakeInvitations)(nil)

func (f *fakeInvitations) InviteUser(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	return f.inviteFn(ctx, req)
}

func (f *fakeInvitations) RedeemInvitation(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	return f.redeemFn(ctx, req)
}

// startEnrollMCP starts an MCP server whose /mcp accepts only "Bearer good",
// serving inv, and returns its base URL and an HTTPS client that trusts it
// and presents nothing.
func startEnrollMCP(t *testing.T, inv service.InvitationServicer) (string, *http.Client) {
	t.Helper()
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw), WithInvitations(inv))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(client.CloseIdleConnections)
	return "https://" + srv.Addr(), client
}

// anonymousSession opens an initialized MCP session on url presenting no
// credential.
func anonymousSession(t *testing.T, url string, client *http.Client) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(url, mcptransport.WithHTTPBasicClient(client))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "anon", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize %s: %v", url, err)
	}
	return c
}

func TestMCP_EnrollEndpointServesRedemptionAnonymously(t *testing.T) {
	var got *service.RedeemInvitationRequest
	base, client := startEnrollMCP(t, &fakeInvitations{
		redeemFn: func(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			if auth.IdentityFromContext(ctx) != nil {
				t.Error("the redemption carried an identity")
			}
			if _, ok := auth.RemoteAddrFromContext(ctx); !ok {
				t.Error("the redemption carried no source address to throttle by")
			}
			got = req
			return &service.EnrolledCertificate{UserID: 9, Username: "bob", TenantID: req.TenantID, CertPEM: []byte("pem"), Serial: "s", Thumbprint: "tp", NotAfter: time.Now()}, nil
		},
	})
	c := anonymousSession(t, base+EnrollPath, client)

	tools, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != toolNameInvitationRedeem {
		t.Fatalf("the enrolment endpoint lists %v, want only %s", tools.Tools, toolNameInvitationRedeem)
	}

	call := mcp.CallToolRequest{}
	call.Params.Name = toolNameInvitationRedeem
	call.Params.Arguments = map[string]any{"tenant_id": "acme", "token": "tok", "csr_pem": "csr", "cert_pem": "c", "chain_pem": "ch"}
	res, err := c.CallTool(context.Background(), call)
	if err != nil || res.IsError {
		t.Fatalf("redeem: %v, %+v", err, res)
	}
	if got.TenantID != "acme" || got.Token != "tok" || string(got.CSRPEM) != "csr" || string(got.CertPEM) != "c" || string(got.ChainPEM) != "ch" {
		t.Fatalf("service request = %+v", got)
	}
	var out enrolledCertResult
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &out); err != nil || out.Username != "bob" {
		t.Fatalf("result = %+v, %v", out, err)
	}

	// Nothing else is reachable on the enrolment endpoint.
	call.Params.Name = "qrdb_put"
	if res, err := c.CallTool(context.Background(), call); err == nil && !res.IsError {
		t.Fatal("the enrolment endpoint served another tool")
	}
}

func TestMCP_EnrollEndpointCarriesTheOpaqueRefusal(t *testing.T) {
	base, client := startEnrollMCP(t, &fakeInvitations{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			return nil, &service.InvitationInvalidError{}
		},
	})
	c := anonymousSession(t, base+EnrollPath, client)
	call := mcp.CallToolRequest{}
	call.Params.Name = toolNameInvitationRedeem
	call.Params.Arguments = map[string]any{"tenant_id": "acme", "token": "x", "csr_pem": "c"}
	res, err := c.CallTool(context.Background(), call)
	if err == nil && (res == nil || !res.IsError) {
		t.Fatal("an invalid token was redeemed")
	}
	if err == nil && !strings.Contains(res.Content[0].(mcp.TextContent).Text, "invalid, expired or already used") {
		t.Fatalf("refusal = %+v", res.Content)
	}
}

// The authenticated endpoint stays closed to the anonymous caller the
// enrolment endpoint serves.
func TestMCP_EnrollEndpointDoesNotOpenMCP(t *testing.T) {
	base, client := startEnrollMCP(t, &fakeInvitations{})
	// /mcp admits an anonymous caller only for publicverbs.Table's tools;
	// the enrol endpoint must not widen that to a data tool.
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(callToolBody("qrdb_get")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /mcp: status %d, want 401", resp.StatusCode)
	}
}

func TestMCP_NoEnrollEndpointWithoutInvitations(t *testing.T) {
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13}}}
	t.Cleanup(client.CloseIdleConnections)
	resp, err := client.Post("https://"+srv.Addr()+EnrollPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("enrolment endpoint without invitations: status %d, want 404", resp.StatusCode)
	}
}

func TestUserInviteTool(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.InviteUserRequest
	h := handleUserInvite(&fakeInvitations{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			got = req
			return &service.Invitation{ID: 3, Token: "tok", ExpiresAt: expires, UserID: 9, Username: req.Username, TenantID: req.TenantID, Created: true}, nil
		},
	}, nil, nil)
	call := mcp.CallToolRequest{}
	call.Params.Arguments = map[string]any{"username": "bob", "email": "bob@example.com", "name": "Bob"}
	res, err := h(tenant.WithTenantID(context.Background(), "acme"), call)
	if err != nil || res.IsError {
		t.Fatalf("invite: %v, %+v", err, res)
	}
	if got.TenantID != "acme" || got.Username != "bob" || got.Email != "bob@example.com" || got.Name != "Bob" {
		t.Fatalf("service request = %+v", got)
	}
	var out invitationResult
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &out); err != nil || out.Token != "tok" || !out.ExpiresAt.Equal(expires) {
		t.Fatalf("result = %+v, %v", out, err)
	}

	call.Params.Arguments = map[string]any{}
	if res, _ := h(context.Background(), call); res == nil || !res.IsError {
		t.Fatal("an invite with no username was accepted")
	}
	call.Params.Arguments = map[string]any{"username": "bob", "role_ids": "r1, ,r2"}
	if _, err := h(context.Background(), call); err == nil {
		t.Fatal("roles were accepted with no role service to validate them")
	}
	boom := errors.New("refused")
	failing := handleUserInvite(&fakeInvitations{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) { return nil, boom },
	}, nil, nil)
	call.Params.Arguments = map[string]any{"username": "bob"}
	if _, err := failing(context.Background(), call); !errors.Is(err, boom) {
		t.Fatalf("service failure: %v", err)
	}
}

func TestSplitRoleIDs(t *testing.T) {
	if got := splitRoleIDs(" a, ,b,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitRoleIDs = %v", got)
	}
	if got := splitRoleIDs(""); got != nil {
		t.Fatalf("splitRoleIDs(\"\") = %v", got)
	}
}
