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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// startAuthenticatedMCP starts an MCP server whose middleware accepts only
// "Bearer good", with no bearer challenge configured, and returns its /mcp URL
// and an HTTPS client that trusts it.
func startAuthenticatedMCP(t *testing.T) (string, *http.Client) {
	t.Helper()
	mw := authmw.New(&auth.NoopAuthorizer{},
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}))
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw))
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

// TestMCP_UnauthenticatedRefusedBeforeMCPLayer: with authentication
// configured, an MCP request that authenticates as nobody is refused at the
// HTTP layer, before mcp-go reads it. It used to be refused only at the tool
// gate, so initialize, ping and tools/list -- which enumerates the whole admin
// tool surface -- were answered anonymously, and mcp-go parsed a body of any
// size to do it.
func TestMCP_UnauthenticatedRefusedBeforeMCPLayer(t *testing.T) {
	url, client := startAuthenticatedMCP(t)
	session := ""
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"anon","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	} {
		req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			session = id
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous %s: status %d, want 401; body %q", body[:40], resp.StatusCode, got)
		}
		if strings.Contains(string(got), "qrdb_") {
			t.Errorf("anonymous %s disclosed the tool surface: %q", body[:40], got)
		}
	}
}

// streamingBody is a JSON-RPC request whose one string argument is n bytes of
// 'A', produced on demand, counting what the client managed to send.
type streamingBody struct {
	prefix, suffix []byte
	fill           int64
	sent           atomic.Int64
}

func newStreamingBody(fill int64) *streamingBody {
	return &streamingBody{
		prefix: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"qrdb_put","arguments":{"key":"k","value":"`),
		suffix: []byte(`"}}}`),
		fill:   fill,
	}
}

func (b *streamingBody) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		switch {
		case len(b.prefix) > 0:
			c := copy(p[n:], b.prefix)
			b.prefix = b.prefix[c:]
			n += c
		case b.fill > 0:
			c := int64(len(p) - n)
			if c > b.fill {
				c = b.fill
			}
			for i := int64(0); i < c; i++ {
				p[n+int(i)] = 'A'
			}
			b.fill -= c
			n += int(c)
		case len(b.suffix) > 0:
			c := copy(p[n:], b.suffix)
			b.suffix = b.suffix[c:]
			n += c
		default:
			b.sent.Add(int64(n))
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}
	}
	b.sent.Add(int64(n))
	return n, nil
}

// TestMCP_AuthenticatedBodyBounded: an authenticated caller's request body is
// read under a bound. It used to be handed to mcp-go unbounded.
func TestMCP_AuthenticatedBodyBounded(t *testing.T) {
	url, client := startAuthenticatedMCP(t)
	const total = int64(200 << 20)
	body := newStreamingBody(total)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer good")
	resp, err := client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("a %d-byte request was answered 200", total)
		}
	}
	if sent := body.sent.Load(); sent >= total {
		t.Fatalf("the server read the whole %d-byte body; want it refused under a bound", sent)
	}
}

// TestAuthenticateHandler_NoChallenge401: without a configured bearer
// challenge an unauthenticated request is still refused 401 before the MCP
// handler, just without a WWW-Authenticate header.
func TestAuthenticateHandler_NoChallenge401(t *testing.T) {
	s := newBearerTestServer(t, "")
	reached := false
	h := s.authenticateHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q, want none without a configured challenge", got)
	}
	if reached {
		t.Fatal("an unauthenticated request reached the MCP handler")
	}
}

// slowUserStore is a user store whose tenant lookup overruns the middleware's
// lookup budget -- a ReadIndex stalled under load -- reported at once rather
// than waited out. Only GetUserByUsername is reachable from the
// authentication path.
type slowUserStore struct{ user.UserServicer }

func (slowUserStore) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	return nil, context.DeadlineExceeded
}

// TestAuthenticateHandler_StoreUnavailable503: a valid credential whose
// tenant user lookup times out is answered 503, not 401. The fallback store
// covers only the system tenant, so for every other tenant the timeout stands
// as a retryable StoreUnavailableError -- which REST answers 503 and MCP
// answered 401, telling a correctly enrolled tenant caller its credential was
// bad. The bearer challenge stays off a response that never judged it.
func TestAuthenticateHandler_StoreUnavailable503(t *testing.T) {
	s := &Server{}
	WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{},
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}),
		authmw.WithUserService(slowUserStore{})))(s)
	WithBearerChallenge(`Bearer realm="mcp"`)(s)

	for _, tc := range []struct {
		name, bearer string
		want         int
	}{
		{"valid credential, store unavailable", "Bearer good", http.StatusServiceUnavailable},
		{"bad credential", "Bearer bad", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := s.authenticateHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
			req.Header.Set("Authorization", tc.bearer)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %q", rec.Code, tc.want, rec.Body.String())
			}
			challenged := rec.Header().Get("WWW-Authenticate") != ""
			if challenged != (tc.want == http.StatusUnauthorized) {
				t.Errorf("WWW-Authenticate present = %v on a %d", challenged, rec.Code)
			}
			if reached {
				t.Fatal("a refused request reached the MCP handler")
			}
		})
	}
}

// TestWriteAuthenticationRefusal_RateLimited: a throttled caller gets 429 and
// the delay in Retry-After, as REST answers it.
func TestWriteAuthenticationRefusal_RateLimited(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthenticationRefusal(rec, &authmw.RateLimitedError{RetryAfter: 1500 * time.Millisecond}, `Bearer realm="mcp"`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want \"2\"", got)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q on a 429", got)
	}
}

// TestBoundedBodyHandler caps the body the wrapped handler can read.
func TestBoundedBodyHandler(t *testing.T) {
	var readErr error
	var n int
	h := boundedBodyHandler(8, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		n, readErr = len(b), err
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("0123456789")))
	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) || n > 8 {
		t.Fatalf("read %d bytes, err %v; want at most 8 and *http.MaxBytesError", n, readErr)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("01234567")))
	if readErr != nil || n != 8 {
		t.Fatalf("a body at the cap: read %d bytes, err %v", n, readErr)
	}
}
