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

package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestBarrierSecret_RequiresTLS: a barrier secret is never sent to an http://
// MCP server, is sent to an https:// one, and a request without one is still
// sent over http://.
func TestBarrierSecret_RequiresTLS(t *testing.T) {
	var plainReached, tlsReached atomic.Int64
	plain := httptest.NewServer(toolCallCounter(&plainReached))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(toolCallCounter(&tlsReached))
	t.Cleanup(secure.Close)
	sotest.RunBarrierSecretTLS(t, "mcp",
		sotest.SecretTarget{Client: connectMCP(t, plain.URL+"/mcp", nil), Reached: counted(&plainReached)},
		sotest.SecretTarget{Client: connectMCP(t, secure.URL+"/mcp", trustingMCP(secure)), Reached: counted(&tlsReached)},
	)
}

// TestBarrierSecret_PlaintextRedirectIsRefused: an https:// MCP server that
// redirects a barrier secret to http:// does not get it sent there.
func TestBarrierSecret_PlaintextRedirectIsRefused(t *testing.T) {
	var plainReached atomic.Int64
	plain := httptest.NewServer(toolCallCounter(&plainReached))
	t.Cleanup(plain.Close)
	session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := buffered(r)
		if !isToolCall(body) {
			// The MCP session starts here; only the tool call is redirected.
			session.ServeHTTP(w, withBody(r, body))
			return
		}
		// #nosec G710 -- the open redirect is the fixture: it sends the client to this test's own plaintext server to prove the secret does not follow.
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	tr := connectMCP(t, origin.URL+"/mcp", trustingMCP(origin))

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")})
	var tlsErr *transport.BarrierSecretRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretUnsealKey {
		t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the unseal key", err)
	}
	if n := plainReached.Load(); n != 0 {
		t.Fatalf("the unseal key followed a redirect to http:// (%d tool calls)", n)
	}
}

// TestBarrierSecret_ForeignHostRedirectIsRefused: an https:// MCP server that
// answers a barrier secret's tool call with a 307 or 308 to another https://
// host does not get the secret resent there.
func TestBarrierSecret_ForeignHostRedirectIsRefused(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var foreignRequests, foreignBytes atomic.Int64
			foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreignRequests.Add(1)
				foreignBytes.Add(int64(len(buffered(r))))
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(foreign.Close)
			session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
			origin := httptest.NewTLSServer(redirectToolCalls(session, status, func(r *http.Request) string {
				return foreign.URL + r.URL.Path
			}))
			t.Cleanup(origin.Close)
			// One trust store for both: httptest servers share a certificate.
			tr := connectMCP(t, origin.URL+"/mcp", trustingMCP(origin))

			err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")})
			var hostErr *transport.BarrierSecretForeignHostError
			if !errors.As(err, &hostErr) {
				t.Fatalf("err = %v, want *transport.BarrierSecretForeignHostError", err)
			}
			want := transport.BarrierSecretForeignHostError{
				Transport:    "mcp",
				Kind:         transport.BarrierSecretUnsealKey,
				OriginalHost: transport.ConfiguredHost(origin.URL),
				RedirectHost: transport.ConfiguredHost(foreign.URL),
			}
			if *hostErr != want {
				t.Fatalf("err = %+v, want %+v", *hostErr, want)
			}
			if n, body := foreignRequests.Load(), foreignBytes.Load(); n != 0 || body != 0 {
				t.Fatalf("the foreign host saw %d requests and %d body bytes, want none", n, body)
			}
		})
	}
}

// TestCreateTenantSOPIN_RedirectIsRefused: a redirected qrdb_tenant_create
// call does not resend a tenant's SO PIN to another host or to an http:// URL.
func TestCreateTenantSOPIN_RedirectIsRefused(t *testing.T) {
	createWithPIN := func(tr *Transport) error {
		_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
			ID: "acme", Name: "acme", AdminUsername: "admin", AdminCSRPEM: "csr",
			SOUsername: "so", SOCSRPEM: "csr", SOPin: soTLSTestPIN,
		})
		return err
	}
	targets := []struct {
		name  string
		start func(http.Handler) *httptest.Server
		want  func(error) bool
	}{
		{"another host", httptest.NewTLSServer, func(err error) bool {
			var hostErr *transport.SOPINForeignHostError
			return errors.As(err, &hostErr) && hostErr.Transport == "mcp"
		}},
		{"http", httptest.NewServer, func(err error) bool {
			var tlsErr *transport.SOPINRequiresTLSError
			return errors.As(err, &tlsErr) && tlsErr.Transport == "mcp"
		}},
	}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, target := range targets {
			t.Run(http.StatusText(status)+" to "+target.name, func(t *testing.T) {
				var reached atomic.Int64
				dest := target.start(toolCallCounter(&reached))
				t.Cleanup(dest.Close)
				session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
				origin := httptest.NewTLSServer(redirectToolCalls(session, status, func(r *http.Request) string {
					return dest.URL + r.URL.Path
				}))
				t.Cleanup(origin.Close)
				// One trust store for both: httptest servers share a certificate.
				err := createWithPIN(connectMCP(t, origin.URL+"/mcp", trustingMCP(origin)))
				if !target.want(err) {
					t.Fatalf("err = %v, want the redirect refused", err)
				}
				if n := reached.Load(); n != 0 {
					t.Fatalf("the SO PIN followed a redirect to %s (%d tool calls)", target.name, n)
				}
			})
		}
	}
}

// TestBarrierSecret_SameHostRedirectIsFollowed: a 307 or 308 of a barrier
// secret's tool call to another path on the same host is followed, and the
// secret arrives there.
func TestBarrierSecret_SameHostRedirectIsFollowed(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var movedCalls, movedBytes atomic.Int64
			session := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("redirect-probe", "1"))
			mux := http.NewServeMux()
			mux.Handle("/moved/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := buffered(r)
				if isToolCall(body) {
					movedCalls.Add(1)
					movedBytes.Add(int64(len(body)))
				}
				session.ServeHTTP(w, withBody(r, body))
			}))
			mux.Handle("/", redirectToolCalls(session, status, func(r *http.Request) string {
				return "/moved" + r.URL.Path
			}))
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)
			tr := connectMCP(t, srv.URL+"/mcp", trustingMCP(srv))

			// The probe server has no tools, so the call itself fails -- after
			// the redirected request, secret included, has arrived.
			err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")})
			var hostErr *transport.BarrierSecretForeignHostError
			var tlsErr *transport.BarrierSecretRequiresTLSError
			if errors.As(err, &hostErr) || errors.As(err, &tlsErr) {
				t.Fatalf("err = %v, want the same-host redirect followed", err)
			}
			if n, body := movedCalls.Load(), movedBytes.Load(); n != 1 || body == 0 {
				t.Fatalf("the redirect target saw %d tool calls and %d body bytes, want 1 with a body", n, body)
			}
		})
	}
}

// redirectToolCalls serves the MCP session on session and answers every
// tools/call request with a redirect of status to location(r).
func redirectToolCalls(session http.Handler, status int, location func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := buffered(r)
		if !isToolCall(body) {
			session.ServeHTTP(w, withBody(r, body))
			return
		}
		// #nosec G710 -- the open redirect is the fixture: it sends the client to this test's own httptest server to prove where the secret does and does not follow.
		http.Redirect(w, r, location(r), status)
	})
}

// toolCallCounter is an MCP server with no tools that counts every tools/call
// request reaching it: an unknown tool is answered with an error, after the
// request -- and any secret in it -- has arrived.
func toolCallCounter(reached *atomic.Int64) http.Handler {
	srv := mcpserver.NewStreamableHTTPServer(mcpserver.NewMCPServer("secret-probe", "1"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := buffered(r)
		if isToolCall(body) {
			reached.Add(1)
		}
		srv.ServeHTTP(w, withBody(r, body))
	})
}

func isToolCall(body []byte) bool {
	return bytes.Contains(body, []byte(`"tools/call"`))
}

func buffered(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	return body
}

func withBody(r *http.Request, body []byte) *http.Request {
	c := r.Clone(r.Context())
	c.Body = io.NopCloser(bytes.NewReader(body))
	return c
}

func trustingMCP(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func counted(reached *atomic.Int64) func() int {
	return func() int { return int(reached.Load()) }
}
