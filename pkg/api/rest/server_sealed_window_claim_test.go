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

package rest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sealedClaimPaths are the sealed-window share-claim routes and their bodies.
// The tenant claims are not sealed-window verbs.
var sealedClaimPaths = map[string]string{
	"/v1/barrier/officers/alice/claim/begin":             ``,
	"/v1/barrier/officers/alice/claim/complete":          `{"nonce_hex":"abcd","signature":"c2ln"}`,
	"/v1/barrier/recovery/officers/alice/claim/begin":    ``,
	"/v1/barrier/recovery/officers/alice/claim/complete": `{"nonce_hex":"abcd","signature":"c2ln"}`,
}

// sealedClaimBarrier is a barrier service recording the thumbprint each
// share claim reaching it was bound to.
func sealedClaimBarrier() (*mockBarrierService, func() []string) {
	var bound []string
	return &mockBarrierService{
		claimBeginFn: func(_ context.Context, _ seal.QuorumDomain, _, thumb string) (*service.BarrierShareClaimChallenge, error) {
			bound = append(bound, thumb)
			return &service.BarrierShareClaimChallenge{NonceHex: "abcd"}, nil
		},
		claimCompleteFn: func(_ context.Context, _ seal.QuorumDomain, _, _ string, _ []byte, thumb string) (*service.BarrierShareClaimResult, error) {
			bound = append(bound, thumb)
			return &service.BarrierShareClaimResult{ShareIndex: 1}, nil
		},
	}, func() []string { return bound }
}

// sealedClaimServers builds the two listeners that serve a share claim with
// no authentication, each with v as its chain verifier: the boot-time
// sealed-window listener, and a regular listener whose node barrier
// re-sealed. The regular listener's authenticator refuses everyone, so a
// claim it serves was never authenticated.
func sealedClaimServers(t *testing.T, v auth.IssuerVerifier, barrier service.BarrierServicer) map[string]func() (http.Handler, *dispatchAuthenticator) {
	t.Helper()
	return map[string]func() (http.Handler, *dispatchAuthenticator){
		"boot-listener": func() (http.Handler, *dispatchAuthenticator) {
			srv := NewSealedWindowServer("127.0.0.1:0", barrier, nil, nil, newFakeSealed(true), WithPeerChainVerifier(v))
			return srv.server.Handler, &dispatchAuthenticator{}
		},
		"resealed-dispatch": func() (http.Handler, *dispatchAuthenticator) {
			authn := &dispatchAuthenticator{}
			srv := NewServer(nil, nil, "127.0.0.1:0",
				WithBarrierService(barrier),
				WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
				WithSealedWindowProbe(newFakeSealed(true)),
				WithSealedWindowDispatch(newFakeSealed(true)),
				WithPeerChainVerifier(v))
			return srv.server.Handler, authn
		},
	}
}

// sealedClaimPost posts a share claim to h over a connection whose TLS state
// is state, nil for a plaintext connection.
func sealedClaimPost(h http.Handler, path, body string, state *tls.ConnectionState) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = state
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSealedWindowClaim_PoolVerifiedCertCompletes: a holder whose certificate
// the client trust pool accepted -- the handshake built no verified chain --
// completes every share-claim begin and complete on the boot-time listener
// and on a re-sealed regular listener, bound to its own leaf, with the
// listener's verifier consulted for each and authentication never reached.
func TestSealedWindowClaim_PoolVerifiedCertCompletes(t *testing.T) {
	cert := presentedCert()
	want := mtls.Thumbprint(cert.Raw)
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	barrier, bound := sealedClaimBarrier()
	for name, build := range sealedClaimServers(t, pool, barrier) {
		t.Run(name, func(t *testing.T) {
			pool.calls = 0
			before := len(bound())
			h, authn := build()
			for path, body := range sealedClaimPaths {
				rec := sealedClaimPost(h, path, body, &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
				if rec.Code != http.StatusOK {
					t.Fatalf("%s: status %d, want 200: %s", path, rec.Code, rec.Body)
				}
			}
			got := bound()[before:]
			if len(got) != len(sealedClaimPaths) {
				t.Fatalf("bound %d claims, want %d", len(got), len(sealedClaimPaths))
			}
			for _, th := range got {
				if th != want {
					t.Fatalf("claim bound %q, want %q", th, want)
				}
			}
			if pool.calls != len(sealedClaimPaths) {
				t.Fatalf("pool consulted %d times, want %d", pool.calls, len(sealedClaimPaths))
			}
			if authn.calls != 0 {
				t.Fatalf("authentication reached %d times", authn.calls)
			}
		})
	}
}

// TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert: through the same
// listeners, a certificate the verifier refuses, one with no verifier to
// resolve it, and a caller with no certificate or no TLS are all refused
// 401 -- the status of a *service.ClientCertRequiredError -- and no claim
// reaches the service.
func TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert(t *testing.T) {
	cert := presentedCert()
	presented := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	cases := []struct {
		name     string
		verifier auth.IssuerVerifier
		state    *tls.ConnectionState
	}{
		{"verifier-refuses", &claimVerifier{err: errors.New("x509: certificate signed by unknown authority")}, presented},
		{"no-verifier", nil, presented},
		{"no-certificate", &claimVerifier{chain: []*x509.Certificate{cert}}, &tls.ConnectionState{}},
		{"no-tls", &claimVerifier{chain: []*x509.Certificate{cert}}, nil},
	}
	for _, tc := range cases {
		barrier, bound := sealedClaimBarrier()
		for name, build := range sealedClaimServers(t, tc.verifier, barrier) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				h, _ := build()
				for path, body := range sealedClaimPaths {
					if rec := sealedClaimPost(h, path, body, tc.state); rec.Code != http.StatusUnauthorized {
						t.Errorf("%s: status %d, want 401: %s", path, rec.Code, rec.Body)
					}
				}
				if got := bound(); len(got) != 0 {
					t.Fatalf("claims reached the service bound to %v", got)
				}
			})
		}
	}
}

// TestPeerTLSMiddleware_BindsStateAndVerifier: the handler behind the
// middleware resolves a certificate the handshake built no chain for through
// the bound verifier.
func TestPeerTLSMiddleware_BindsStateAndVerifier(t *testing.T) {
	cert := presentedCert()
	var got *x509.Certificate
	var err error
	h := peerTLSMiddleware(&claimVerifier{chain: []*x509.Certificate{cert}}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, err = auth.VerifiedPeerCertificate(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if err != nil || got != cert {
		t.Fatalf("VerifiedPeerCertificate = %v, %v; want the pool-verified leaf", got, err)
	}
}

// TestPeerTLSMiddleware_PlaintextAndNilVerifier: a plaintext request reaches
// the handler with nothing bound, and a nil verifier binds the TLS state
// alone, leaving an unverified certificate unresolved.
func TestPeerTLSMiddleware_PlaintextAndNilVerifier(t *testing.T) {
	cert := presentedCert()
	var state *tls.ConnectionState
	var err error
	h := peerTLSMiddleware(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		state = auth.TLSStateFromContext(r.Context())
		_, err = auth.VerifiedPeerCertificate(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if state != nil {
		t.Fatalf("plaintext request bound TLS state %v", state)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if state != req.TLS {
		t.Fatalf("TLS state = %v, want the request's", state)
	}
	if err == nil {
		t.Fatal("an unverified certificate resolved with no verifier")
	}
}

// TestServer_ChainVerifier: WithPeerChainVerifier's verifier wins, and a
// listener without one falls back to its AuthMiddleware's issuer verifier,
// nil when there is no AuthMiddleware either.
func TestServer_ChainVerifier(t *testing.T) {
	pool := &claimVerifier{}
	if got := NewServer(nil, nil, "127.0.0.1:0").chainVerifier(); got != nil {
		t.Fatalf("chainVerifier() = %v with no verifier or middleware, want nil", got)
	}
	if got := NewServer(nil, nil, "127.0.0.1:0", WithPeerChainVerifier(pool)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the WithPeerChainVerifier verifier", got)
	}
	mw := authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithIssuerVerifier(pool))
	if got := NewServer(nil, nil, "127.0.0.1:0", WithAuthMiddleware(mw)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the middleware's issuer verifier", got)
	}
}
