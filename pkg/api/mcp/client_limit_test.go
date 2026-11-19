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
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientFrom is an HTTPS client whose connections originate from the
// loopback address local.
func clientFrom(t *testing.T, tlsCfg *tls.Config, local string) *http.Client {
	t.Helper()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(local)}}
	c := &http.Client{Transport: &http.Transport{
		DialContext: dialer.DialContext,
		TLSClientConfig: &tls.Config{
			RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
		},
	}}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// postAnonymousCall POSTs a credential-less tools/call of qrdb_get. The
// no-login verbs (initialize, qrdb_health) are answered anonymously, so the
// request names a tool that needs a credential and is refused with 401.
func postAnonymousCall(t *testing.T, c *http.Client, url string, header ...string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"qrdb_get","arguments":{}}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// TestClientLimit_MCP: an address past its burst is refused with 429 and
// Retry-After before authentication -- which refuses an anonymous caller
// with 401 -- runs, a forwarded-for header from the untrusted peer does not
// change the key, and another address is still authenticated.
func TestClientLimit_MCP(t *testing.T) {
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: 2}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw), WithClientLimiter(l))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	url := "https://" + srv.Addr() + "/mcp"

	flooder := clientFrom(t, tlsCfg, "127.0.0.1")
	for i := 0; i < 2; i++ {
		if status, _ := postAnonymousCall(t, flooder, url); status != http.StatusUnauthorized {
			t.Fatalf("request %d: status %d, want 401", i, status)
		}
	}
	status, header := postAnonymousCall(t, flooder, url, "X-Forwarded-For", "203.0.113.9")
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" {
		t.Fatalf("over the burst: status %d Retry-After %q, want 429 with Retry-After", status, header.Get("Retry-After"))
	}
	if status, _ := postAnonymousCall(t, clientFrom(t, tlsCfg, "127.0.0.2"), url); status != http.StatusUnauthorized {
		t.Fatalf("other address: status %d, want 401", status)
	}
}

// TestClientLimit_MCPUnlimitedByDefault: without WithClientLimiter every
// anonymous request reaches authentication.
func TestClientLimit_MCPUnlimitedByDefault(t *testing.T) {
	url, client := startAuthenticatedMCP(t)
	for i := 0; i < 4; i++ {
		if status, _ := postAnonymousCall(t, client, url); status != http.StatusUnauthorized {
			t.Fatalf("request %d: status %d, want 401", i, status)
		}
	}
}
