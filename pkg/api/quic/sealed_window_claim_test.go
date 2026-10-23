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

package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sealedClaimBarrier is a barrier service recording, under its lock, the
// thumbprint each share claim reaching it was bound to. The server serves
// each request on its own goroutine.
type sealedClaimBarrier struct {
	service.BarrierServicer
	mu    sync.Mutex
	bound []string
}

func (b *sealedClaimBarrier) record(thumb string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bound = append(b.bound, thumb)
}

func (b *sealedClaimBarrier) claims() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bound...)
}

func (b *sealedClaimBarrier) BarrierShareClaimBegin(_ context.Context, _ seal.QuorumDomain, _, thumb string) (*service.BarrierShareClaimChallenge, error) {
	b.record(thumb)
	return &service.BarrierShareClaimChallenge{NonceHex: "ab"}, nil
}

func (b *sealedClaimBarrier) BarrierShareClaimComplete(_ context.Context, _ seal.QuorumDomain, _, _ string, _ []byte, thumb string) (*service.BarrierShareClaimResult, error) {
	b.record(thumb)
	return &service.BarrierShareClaimResult{ShareIndex: 1}, nil
}

// lockedClaimVerifier is a chain verifier the server consults from its
// request goroutines.
type lockedClaimVerifier struct {
	mu    sync.Mutex
	chain []*x509.Certificate
	err   error
	calls int
}

func (v *lockedClaimVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	return v.chain, v.err
}

func (v *lockedClaimVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }
func (v *lockedClaimVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return errors.New("not reached")
}

func (v *lockedClaimVerifier) consulted() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

var _ auth.IssuerVerifier = (*lockedClaimVerifier)(nil)

// sealedClaimMethods are the sealed-window share-claim methods and their
// request payloads. The tenant claims are not sealed-window verbs.
func sealedClaimMethods(t *testing.T) map[string][]byte {
	t.Helper()
	begin := mustMarshal(t, &pb.BarrierShareClaimBeginRequest{Username: "carol"})
	complete := mustMarshal(t, &pb.BarrierShareClaimCompleteRequest{Username: "carol", NonceHex: "ab", Signature: []byte("s")})
	return map[string][]byte{
		"barrier.ShareClaimBegin":            begin,
		"barrier.ShareClaimComplete":         complete,
		"barrier.RecoveryShareClaimBegin":    begin,
		"barrier.RecoveryShareClaimComplete": complete,
	}
}

// startSealedClaimServers starts the two QUIC listeners that serve a share
// claim with no authentication, each with verifier(leaf) as its chain
// verifier, leaf being the certificate the client presents: the
// boot-time sealed-window listener, and a regular listener whose node
// barrier re-sealed. Each requests a client certificate without verifying
// it, as a listener wired to the client trust pool does, so the handshake
// builds no verified chain. The regular listener's authenticator refuses
// everyone, so a claim it serves was never authenticated. It returns each
// listener's address and authenticator, the client configuration, and leaf.
func startSealedClaimServers(t *testing.T, verifier func(leaf *x509.Certificate) auth.IssuerVerifier, barrier service.BarrierServicer) (map[string]string, map[string]*quicDispatchAuthenticator, *tls.Config, *x509.Certificate) {
	t.Helper()
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequestClientCert
	clientCfg.Certificates = serverCfg.Certificates
	leaf := serverCfg.Certificates[0].Leaf
	v := verifier(leaf)
	authn := &quicDispatchAuthenticator{}
	servers := map[string]*Server{
		"boot-listener": NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
			WithBarrierService(barrier),
			WithSealedWindowListener(&fakeSealedProbe{sealed: true}),
			WithPeerChainVerifier(v)),
		"resealed-dispatch": NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
			WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
			WithBarrierService(barrier),
			WithSealedWindowProbe(&fakeSealedProbe{sealed: true}),
			WithSealedWindowDispatch(&fakeSealedProbe{sealed: true}),
			WithPeerChainVerifier(v)),
	}
	addrs := make(map[string]string, len(servers))
	for name, srv := range servers {
		if err := srv.Start(); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		t.Cleanup(srv.Stop)
		addrs[name] = srv.Addr()
	}
	authns := map[string]*quicDispatchAuthenticator{"boot-listener": {}, "resealed-dispatch": authn}
	return addrs, authns, clientCfg, leaf
}

// TestSealedWindowClaim_PoolVerifiedCertCompletes: a holder whose certificate
// the client trust pool accepted -- the handshake built no verified chain --
// completes every share-claim begin and complete on the boot-time listener
// and on a re-sealed regular listener, over a real QUIC connection, bound to
// its own leaf, with the listener's verifier consulted for each and
// authentication never reached.
func TestSealedWindowClaim_PoolVerifiedCertCompletes(t *testing.T) {
	var pool *lockedClaimVerifier
	barrier := &sealedClaimBarrier{}
	addrs, authns, clientCfg, leaf := startSealedClaimServers(t, func(leaf *x509.Certificate) auth.IssuerVerifier {
		pool = &lockedClaimVerifier{chain: []*x509.Certificate{leaf}}
		return pool
	}, barrier)
	want := mtls.Thumbprint(leaf.Raw)
	methods := sealedClaimMethods(t)
	for name, addr := range addrs {
		t.Run(name, func(t *testing.T) {
			before, consulted := len(barrier.claims()), pool.consulted()
			for method, payload := range methods {
				if status, body := credentialE2ERoundTrip(t, addr, clientCfg, method, payload); status != wire.StatusOK {
					t.Fatalf("%s: status %s: %s", method, wire.StatusName(status), body)
				}
			}
			got := barrier.claims()[before:]
			if len(got) != len(methods) {
				t.Fatalf("bound %d claims, want %d", len(got), len(methods))
			}
			for _, th := range got {
				if th != want {
					t.Fatalf("claim bound %q, want %q", th, want)
				}
			}
			if n := pool.consulted() - consulted; n != len(methods) {
				t.Fatalf("pool consulted %d times, want %d", n, len(methods))
			}
			if n := authns[name].calls.Load(); n != 0 {
				t.Fatalf("authentication reached %d times", n)
			}
		})
	}
}

// TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert: through the same
// listeners, a certificate the verifier refuses, one with no verifier to
// resolve it, and a caller presenting no certificate are all refused
// wire.StatusUnauthenticated -- the status of a
// *service.ClientCertRequiredError -- and no claim reaches the service.
func TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert(t *testing.T) {
	cases := []struct {
		name     string
		verifier func(leaf *x509.Certificate) auth.IssuerVerifier
		noCert   bool
	}{
		{name: "verifier-refuses", verifier: func(*x509.Certificate) auth.IssuerVerifier {
			return &lockedClaimVerifier{err: errors.New("x509: certificate signed by unknown authority")}
		}},
		{name: "no-verifier", verifier: func(*x509.Certificate) auth.IssuerVerifier { return nil }},
		{name: "no-certificate", noCert: true, verifier: func(leaf *x509.Certificate) auth.IssuerVerifier {
			return &lockedClaimVerifier{chain: []*x509.Certificate{leaf}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			barrier := &sealedClaimBarrier{}
			addrs, _, clientCfg, _ := startSealedClaimServers(t, tc.verifier, barrier)
			if tc.noCert {
				clientCfg.Certificates = nil
			}
			for name, addr := range addrs {
				for method, payload := range sealedClaimMethods(t) {
					if status, _ := credentialE2ERoundTrip(t, addr, clientCfg, method, payload); status != wire.StatusUnauthenticated {
						t.Errorf("%s %s: status %s, want %s", name, method, wire.StatusName(status), wire.StatusName(wire.StatusUnauthenticated))
					}
				}
			}
			if got := barrier.claims(); len(got) != 0 {
				t.Fatalf("claims reached the service bound to %v", got)
			}
		})
	}
}

// TestServer_ChainVerifier: WithPeerChainVerifier's verifier wins, and a
// listener without one falls back to its AuthMiddleware's issuer verifier,
// nil when there is no AuthMiddleware either.
func TestServer_ChainVerifier(t *testing.T) {
	pool := &lockedClaimVerifier{}
	if got := NewServer(nil, nil, "127.0.0.1:0").chainVerifier(); got != nil {
		t.Fatalf("chainVerifier() = %v with no verifier or middleware, want nil", got)
	}
	if got := NewServer(nil, nil, "127.0.0.1:0", WithPeerChainVerifier(pool)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the WithPeerChainVerifier verifier", got)
	}
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithIssuerVerifier(pool))
	if got := NewServer(nil, nil, "127.0.0.1:0", WithAuthMiddleware(mw)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the middleware's issuer verifier", got)
	}
}
