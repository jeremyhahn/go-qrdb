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
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// claimVerifier is the listener's trust pool: it answers chain with err.
type claimVerifier struct {
	chain []*x509.Certificate
	err   error
	calls int
}

func (v *claimVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	v.calls++
	return v.chain, v.err
}
func (v *claimVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }
func (v *claimVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return errors.New("not reached")
}

var _ auth.IssuerVerifier = (*claimVerifier)(nil)

// trustStateError is a refusal made while the trust state was unreadable.
type trustStateError struct{}

func (trustStateError) Error() string               { return "trust anchors unreadable" }
func (trustStateError) TrustStateUnavailable() bool { return true }

// restClaimRoute is one share-claim step on its own server.
type restClaimRoute struct {
	srv        *Server
	path, body string
}

// restClaimRoutes returns every share-claim step -- system, recovery and
// tenant, begin and complete -- with a reader of the thumbprints the
// services were handed.
func restClaimRoutes(t *testing.T) (map[string]restClaimRoute, func() []string) {
	t.Helper()
	var bound []string
	barrier := newBarrierTestServer(t, &mockBarrierService{
		claimBeginFn: func(_ context.Context, _ seal.QuorumDomain, _, thumb string) (*service.BarrierShareClaimChallenge, error) {
			bound = append(bound, thumb)
			return &service.BarrierShareClaimChallenge{NonceHex: "abcd"}, nil
		},
		claimCompleteFn: func(_ context.Context, _ seal.QuorumDomain, _, _ string, _ []byte, thumb string) (*service.BarrierShareClaimResult, error) {
			bound = append(bound, thumb)
			return &service.BarrierShareClaimResult{ShareIndex: 1}, nil
		},
	})
	tenants := newTenantServer(&mockTenantService{
		claimBeginFn: func(_ context.Context, _, username, thumb string) (*ceremony.ChallengeResponse, error) {
			bound = append(bound, thumb)
			return &ceremony.ChallengeResponse{Nonce: []byte{0xab}, Username: username}, nil
		},
		claimCompleteFn: func(_ context.Context, _, _, _ string, _ []byte, thumb string) (*ceremony.ShareClaimResult, error) {
			bound = append(bound, thumb)
			return &ceremony.ShareClaimResult{ShareIndex: 1}, nil
		},
	})
	complete := `{"nonce_hex":"abcd","signature":"c2ln"}`
	return map[string]restClaimRoute{
		"system begin":      {barrier, "/v1/barrier/officers/alice/claim/begin", ``},
		"system complete":   {barrier, "/v1/barrier/officers/alice/claim/complete", complete},
		"recovery begin":    {barrier, "/v1/barrier/recovery/officers/alice/claim/begin", ``},
		"recovery complete": {barrier, "/v1/barrier/recovery/officers/alice/claim/complete", complete},
		"tenant begin":      {tenants, "/v1/tenants/acme/barrier/officers/carol/claim/begin", ``},
		"tenant complete":   {tenants, "/v1/tenants/acme/barrier/officers/carol/claim/complete", complete},
	}, func() []string { return bound }
}

// claimPost drives one claim step over a connection that presented cert with
// no chain the TLS stack verified, the auth chain having bound v.
func claimPost(route restClaimRoute, cert *x509.Certificate, v auth.IssuerVerifier) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(route.body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	req = req.WithContext(auth.WithPeerChainVerifier(req.Context(), v))
	rec := httptest.NewRecorder()
	route.srv.dispatch(rec, req)
	return rec
}

// presentedCert is a certificate the connection presented, for the claim
// tests to verify or not.
func presentedCert() *x509.Certificate {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	injectFakeTLS(req)
	return req.TLS.PeerCertificates[0]
}

// A share claim never binds a certificate no trust anchor verified: with no
// verifier, a refusing one or an unreadable trust state, no claim step
// reaches the service.
func TestBarrierHolders_ClaimRefusesAnUnverifiedCertificate(t *testing.T) {
	for name, tc := range map[string]struct {
		verifier  *claimVerifier
		want      int
		wantCalls int
	}{
		"no verifier":            {nil, http.StatusUnauthorized, 0},
		"verifier refuses":       {&claimVerifier{err: errors.New("unknown issuer")}, http.StatusUnauthorized, 6},
		"trust state unreadable": {&claimVerifier{err: trustStateError{}}, http.StatusServiceUnavailable, 6},
	} {
		t.Run(name, func(t *testing.T) {
			routes, bound := restClaimRoutes(t)
			var v auth.IssuerVerifier
			if tc.verifier != nil {
				v = tc.verifier
			}
			for step, route := range routes {
				if rec := claimPost(route, presentedCert(), v); rec.Code != tc.want {
					t.Errorf("%s: status %d, want %d: %s", step, rec.Code, tc.want, rec.Body)
				}
			}
			if got := bound(); len(got) != 0 {
				t.Fatalf("an unverified certificate reached the service: %v", got)
			}
			if tc.verifier != nil && tc.verifier.calls != tc.wantCalls {
				t.Fatalf("verifier calls = %d, want %d", tc.verifier.calls, tc.wantCalls)
			}
		})
	}
}

// A presented certificate the trust pool verifies is the one every claim
// step binds.
func TestBarrierHolders_ClaimBindsThePoolVerifiedLeaf(t *testing.T) {
	routes, bound := restClaimRoutes(t)
	cert := presentedCert()
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	for step, route := range routes {
		if rec := claimPost(route, cert, pool); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200: %s", step, rec.Code, rec.Body)
		}
	}
	want := mtls.Thumbprint(cert.Raw)
	got := bound()
	if len(got) != 6 {
		t.Fatalf("%d claims bound, want 6", len(got))
	}
	for _, thumb := range got {
		if thumb != want {
			t.Fatalf("bound %q, want the verified leaf's %q", thumb, want)
		}
	}
	if pool.calls != 6 {
		t.Fatalf("pool calls = %d, want one per claim step", pool.calls)
	}
}
