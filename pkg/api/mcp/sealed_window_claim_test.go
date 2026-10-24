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
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sealedClaimTools are the sealed-window share-claim tools. The tenant claims
// are not sealed-window verbs.
var sealedClaimTools = []string{
	"qrdb_barrier_share_claim_begin",
	"qrdb_barrier_share_claim_complete",
	"qrdb_barrier_recovery_share_claim_begin",
	"qrdb_barrier_recovery_share_claim_complete",
}

// sealedClaimServers builds the two endpoints that serve a share claim with
// no authentication, each with v as its chain verifier: the boot-time
// sealed-window listener, and a regular listener whose node barrier
// re-sealed. The regular listener's authenticator refuses everyone, so a
// claim it serves was never authenticated.
func sealedClaimServers(t *testing.T, v auth.IssuerVerifier, barrier *holderSvc) map[string]func() (http.Handler, *mcpDispatchAuthenticator) {
	t.Helper()
	return map[string]func() (http.Handler, *mcpDispatchAuthenticator){
		"boot-listener": func() (http.Handler, *mcpDispatchAuthenticator) {
			srv := NewSealedWindowServer("127.0.0.1:0", barrier, nil, nil, &fakeMCPSealedProbe{sealed: true}, WithPeerChainVerifier(v))
			return srv.endpoint(), &mcpDispatchAuthenticator{}
		},
		"resealed-dispatch": func() (http.Handler, *mcpDispatchAuthenticator) {
			authn := &mcpDispatchAuthenticator{}
			srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
				WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
				WithBarrierService(barrier),
				WithSealedWindowProbe(&fakeMCPSealedProbe{sealed: true}),
				WithSealedWindowDispatch(&fakeMCPSealedProbe{sealed: true}),
				WithPeerChainVerifier(v))
			return srv.endpoint(), authn
		},
	}
}

// sealedClaimPost posts body to h over a connection whose TLS state is
// state, nil for a plaintext connection, carrying the session forward.
func sealedClaimPost(t *testing.T, h http.Handler, session *string, body string, state *tls.ConnectionState) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if *session != "" {
		req.Header.Set("Mcp-Session-Id", *session)
	}
	req.TLS = state
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if id := rr.Header().Get("Mcp-Session-Id"); id != "" {
		*session = id
	}
	got, _ := io.ReadAll(rr.Body)
	return rr.Code, string(got)
}

// sealedClaimCall is the tools/call body of the share-claim tool name.
func sealedClaimCall(t *testing.T, name string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": claimToolArgs()[name]},
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	return string(body)
}

// claimRefused reports whether a tools/call answer refused the call.
func claimRefused(status int, body string) bool {
	return status != http.StatusOK || strings.Contains(body, `"isError":true`) || strings.Contains(body, `"error":`)
}

// TestSealedWindowClaim_PoolVerifiedCertCompletes: a holder whose certificate
// the client trust pool accepted -- the handshake built no verified chain --
// completes every share-claim begin and complete on the boot-time listener
// and on a re-sealed regular listener, bound to its own leaf, with the
// listener's verifier consulted for each and authentication never reached.
func TestSealedWindowClaim_PoolVerifiedCertCompletes(t *testing.T) {
	ca, caKey := claimCert(t, "ca", true, nil, nil)
	leaf, _ := claimCert(t, "carol", false, ca, caKey)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	want := mtls.Thumbprint(leaf.Raw)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	pool := &rootsVerifier{roots: roots}
	barrier := &holderSvc{}
	for name, build := range sealedClaimServers(t, pool, barrier) {
		t.Run(name, func(t *testing.T) {
			pool.calls, barrier.claims = 0, 0
			h, authn := build()
			var session string
			if status, body := sealedClaimPost(t, h, &session, initializeBody, state); status != http.StatusOK {
				t.Fatalf("initialize: status %d, body %q", status, body)
			}
			for _, tool := range sealedClaimTools {
				barrier.peer = ""
				if status, body := sealedClaimPost(t, h, &session, sealedClaimCall(t, tool), state); claimRefused(status, body) {
					t.Fatalf("%s: refused, status %d, body %q", tool, status, body)
				}
				if barrier.peer != want {
					t.Fatalf("%s bound %q, want %q", tool, barrier.peer, want)
				}
			}
			if barrier.claims != len(sealedClaimTools) {
				t.Fatalf("claims reaching the service = %d, want %d", barrier.claims, len(sealedClaimTools))
			}
			if pool.calls != len(sealedClaimTools) {
				t.Fatalf("pool consulted %d times, want %d", pool.calls, len(sealedClaimTools))
			}
			if n := authn.calls.Load(); n != 0 {
				t.Fatalf("authentication reached %d times", n)
			}
		})
	}
}

// TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert: through the same
// endpoints, a certificate the verifier refuses, one with no verifier to
// resolve it, and a caller with no certificate or no TLS are all refused
// with a *service.ClientCertRequiredError, and no claim reaches the service.
func TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert(t *testing.T) {
	certRequired := (&service.ClientCertRequiredError{}).Error()
	ca, caKey := claimCert(t, "ca", true, nil, nil)
	leaf, _ := claimCert(t, "carol", false, ca, caKey)
	other, _ := claimCert(t, "other-ca", true, nil, nil)
	foreign := x509.NewCertPool()
	foreign.AddCert(other)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	presented := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	cases := []struct {
		name     string
		verifier auth.IssuerVerifier
		state    *tls.ConnectionState
	}{
		{"verifier-refuses", &rootsVerifier{roots: foreign}, presented},
		{"no-verifier", nil, presented},
		{"no-certificate", &rootsVerifier{roots: roots}, &tls.ConnectionState{}},
		{"no-tls", &rootsVerifier{roots: roots}, nil},
	}
	for _, tc := range cases {
		barrier := &holderSvc{}
		for name, build := range sealedClaimServers(t, tc.verifier, barrier) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				h, _ := build()
				var session string
				if status, body := sealedClaimPost(t, h, &session, initializeBody, tc.state); status != http.StatusOK {
					t.Fatalf("initialize: status %d, body %q", status, body)
				}
				for _, tool := range sealedClaimTools {
					status, body := sealedClaimPost(t, h, &session, sealedClaimCall(t, tool), tc.state)
					if !claimRefused(status, body) || !strings.Contains(body, certRequired) {
						t.Errorf("%s: status %d, body %q, want the refusal %q", tool, status, body, certRequired)
					}
				}
				if barrier.claims != 0 {
					t.Fatalf("%d claims reached the service bound to %q", barrier.claims, barrier.peer)
				}
			})
		}
	}
}

// TestServer_ChainVerifier: WithPeerChainVerifier's verifier wins, and a
// listener without one falls back to its AuthMiddleware's issuer verifier,
// nil when there is no AuthMiddleware either.
func TestServer_ChainVerifier(t *testing.T) {
	pool := &rootsVerifier{}
	if got := NewServer(nil, nil, "127.0.0.1:0").chainVerifier(); got != nil {
		t.Fatalf("chainVerifier() = %v with no verifier or middleware, want nil", got)
	}
	if got := NewServer(nil, nil, "127.0.0.1:0", WithPeerChainVerifier(pool)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the WithPeerChainVerifier verifier", got)
	}
	mw := authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithIssuerVerifier(pool))
	if got := NewServer(nil, nil, "127.0.0.1:0", WithAuthMiddleware(mw)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the middleware's issuer verifier", got)
	}
}

// TestSealedWindowServer_InheritsChainVerifier: a regular listener hands its
// verifier to the sealed path it dispatches a re-sealed node's calls to.
func TestSealedWindowServer_InheritsChainVerifier(t *testing.T) {
	pool := &rootsVerifier{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithBarrierService(&holderSvc{}),
		WithSealedWindowProbe(&fakeMCPSealedProbe{sealed: true}),
		WithSealedWindowDispatch(&fakeMCPSealedProbe{sealed: true}),
		WithPeerChainVerifier(pool))
	if srv.sealedPath == nil {
		t.Fatal("no sealed path built for a dispatching listener")
	}
	if got := srv.sealedPath.chainVerifier(); got != pool {
		t.Fatalf("sealed path chainVerifier() = %v, want the listener's", got)
	}
}
