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

package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

// claimTenant records the thumbprint each tenant claim step was handed.
type claimTenant struct {
	quicHolderTenant
	peers []string
}

func (c *claimTenant) TenantBarrierShareClaimBegin(_ context.Context, _, _, peer string) (*ceremony.ChallengeResponse, error) {
	c.peers = append(c.peers, peer)
	return &ceremony.ChallengeResponse{Nonce: []byte{0xab}}, nil
}

func (c *claimTenant) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, peer string) (*ceremony.ShareClaimResult, error) {
	c.peers = append(c.peers, peer)
	return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("env")}, nil
}

// quicClaimFrames returns every share-claim step -- system, recovery and
// tenant, begin and complete -- with a reader of the thumbprints the services
// were handed.
func quicClaimFrames(t *testing.T) (map[string]func(context.Context) ([]byte, error), func() []string) {
	t.Helper()
	svc := &quicHolderSvc{}
	var bound []string
	system := systemHolderFrames(t, NewServer(nil, nil, "localhost:0", WithBarrierService(svc)))
	tenants := &claimTenant{}
	tenant := tenantHolderFrames(t, NewServer(nil, nil, "localhost:0", WithTenantService(tenants)), "acme")
	frames := map[string]func(context.Context) ([]byte, error){
		"tenant claim-begin":    tenant["claim-begin"],
		"tenant claim-complete": tenant["claim-complete"],
	}
	for _, name := range []string{"claim-begin", "claim-complete", "recovery-begin", "recovery-complete"} {
		call := system[name]
		frames[name] = func(ctx context.Context) ([]byte, error) {
			svc.peer = ""
			out, err := call(ctx)
			if svc.peer != "" {
				bound = append(bound, svc.peer)
			}
			return out, err
		}
	}
	return frames, func() []string { return append(bound, tenants.peers...) }
}

// presentedContext is a connection that presented cert with no chain the TLS
// stack verified, the connection state having bound v.
func presentedContext(cert *x509.Certificate, v auth.IssuerVerifier) context.Context {
	ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
	return auth.WithPeerChainVerifier(ctx, v)
}

// presentedCert is a certificate the connection presented, for the claim
// tests to verify or not.
func presentedCert(t *testing.T) *x509.Certificate {
	t.Helper()
	ctx, _ := quicPeerContext(t)
	return auth.TLSStateFromContext(ctx).PeerCertificates[0]
}

// A share claim never binds a certificate no trust anchor verified: with no
// verifier, a refusing one or an unreadable trust state, no claim frame
// reaches the service.
func TestQUICHolders_ClaimRefusesAnUnverifiedCertificate(t *testing.T) {
	for name, tc := range map[string]struct {
		verifier  *claimVerifier
		want      uint16
		wantCalls int
	}{
		"no verifier":            {nil, wire.StatusUnauthenticated, 0},
		"verifier refuses":       {&claimVerifier{err: errors.New("unknown issuer")}, wire.StatusUnauthenticated, 6},
		"trust state unreadable": {&claimVerifier{err: trustStateError{}}, wire.StatusUnavailable, 6},
	} {
		t.Run(name, func(t *testing.T) {
			frames, bound := quicClaimFrames(t)
			var v auth.IssuerVerifier
			if tc.verifier != nil {
				v = tc.verifier
			}
			for step, call := range frames {
				_, err := call(presentedContext(presentedCert(t), v))
				if code, _ := mapServiceError(err); err == nil || code != tc.want {
					t.Errorf("%s = %v (status %d), want status %d", step, err, code, tc.want)
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
// frame binds.
func TestQUICHolders_ClaimBindsThePoolVerifiedLeaf(t *testing.T) {
	frames, bound := quicClaimFrames(t)
	cert := presentedCert(t)
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	for step, call := range frames {
		if _, err := call(presentedContext(cert, pool)); err != nil {
			t.Errorf("%s: %v", step, err)
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
		t.Fatalf("pool calls = %d, want one per claim frame", pool.calls)
	}
}
