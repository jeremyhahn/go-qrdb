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

package unix

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sealedClaimMethods are the sealed-window share-claim RPCs. The tenant
// claims are not sealed-window verbs.
var sealedClaimMethods = []string{
	pb.QRDBService_BarrierShareClaimBegin_FullMethodName,
	pb.QRDBService_BarrierShareClaimComplete_FullMethodName,
	pb.QRDBService_BarrierRecoveryShareClaimBegin_FullMethodName,
	pb.QRDBService_BarrierRecoveryShareClaimComplete_FullMethodName,
}

// claimVerifier is a chain verifier returning chain, or err, for every
// certificate the socket hands it.
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

// holderCert is a holder's self-issued certificate, standing in for one the
// client trust pool accepts.
func holderCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "carol"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

// peerOnly is the context gRPC hands the socket's interceptors: the peer's
// TLS info and nothing bound by qrdb. A nil state is a peer with no TLS.
func peerOnly(state *tls.ConnectionState) context.Context {
	p := &grpcpeer.Peer{}
	if state != nil {
		p.AuthInfo = credentials.TLSInfo{State: *state}
	}
	return grpcpeer.NewContext(context.Background(), p)
}

// sealedClaimChains builds the two sealed-window admission chains a share
// claim reaches with no authentication, each with v as the socket's
// verifier: the boot-time socket, and a regular socket whose node barrier
// re-sealed. The regular socket's authenticator refuses everyone, so a claim
// it admits was never authenticated.
func sealedClaimChains(t *testing.T, v auth.IssuerVerifier) map[string]func() (grpc.UnaryServerInterceptor, *dispatchAuthenticator) {
	t.Helper()
	return map[string]func() (grpc.UnaryServerInterceptor, *dispatchAuthenticator){
		"boot-listener": func() (grpc.UnaryServerInterceptor, *dispatchAuthenticator) {
			srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-claim.sock",
				WithSealedWindowListener(&fakeSealedProbe{sealed: true}),
				WithPeerChainVerifier(v))
			unary, _ := srv.admissionChains()
			return unary, &dispatchAuthenticator{}
		},
		"resealed-dispatch": func() (grpc.UnaryServerInterceptor, *dispatchAuthenticator) {
			authn := &dispatchAuthenticator{}
			srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-claim.sock",
				WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
				WithSealedWindowProbe(&fakeSealedProbe{sealed: true}),
				WithSealedWindowDispatch(&fakeSealedProbe{sealed: true}),
				WithPeerChainVerifier(v))
			unary, _ := srv.admissionChains()
			return unary, authn
		},
	}
}

// claimThroughChain runs fullMethod through unary to a handler that binds
// the caller as every share-claim handler does, returning the thumbprint it
// bound.
func claimThroughChain(unary grpc.UnaryServerInterceptor, ctx context.Context, fullMethod string) (string, error) {
	var bound string
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: fullMethod}, func(ctx context.Context, _ any) (any, error) {
		thumb, err := service.VerifiedClientCertThumbprint(ctx)
		if err != nil {
			return nil, err
		}
		bound = thumb
		return nil, nil
	})
	return bound, err
}

// TestSealedWindowClaim_PoolVerifiedCertCompletes: a holder whose certificate
// the client trust pool accepted -- the handshake built no verified chain --
// completes every share-claim begin and complete on the boot-time socket and
// on a re-sealed regular socket, bound to its own leaf, with the socket's
// verifier consulted for each and authentication never reached.
func TestSealedWindowClaim_PoolVerifiedCertCompletes(t *testing.T) {
	cert := holderCert(t)
	want := mtls.Thumbprint(cert.Raw)
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	for name, build := range sealedClaimChains(t, pool) {
		t.Run(name, func(t *testing.T) {
			pool.calls = 0
			unary, authn := build()
			for _, method := range sealedClaimMethods {
				got, err := claimThroughChain(unary, peerOnly(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}), method)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				if got != want {
					t.Fatalf("%s bound %q, want %q", method, got, want)
				}
			}
			if pool.calls != len(sealedClaimMethods) {
				t.Fatalf("pool consulted %d times, want %d", pool.calls, len(sealedClaimMethods))
			}
			if authn.calls != 0 {
				t.Fatalf("authentication reached %d times", authn.calls)
			}
		})
	}
}

// TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert: through the same
// chains, a certificate the verifier refuses, one with no verifier to
// resolve it, and a caller with no certificate or no TLS are all refused with
// a *service.ClientCertRequiredError, and no claim binds a thumbprint.
func TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert(t *testing.T) {
	cert := holderCert(t)
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
		for name, build := range sealedClaimChains(t, tc.verifier) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				unary, _ := build()
				for _, method := range sealedClaimMethods {
					got, err := claimThroughChain(unary, peerOnly(tc.state), method)
					var required *service.ClientCertRequiredError
					if !errors.As(err, &required) || got != "" {
						t.Errorf("%s: err = %v, bound %q; want *service.ClientCertRequiredError and nothing bound", method, err, got)
					}
				}
			})
		}
	}
}

// TestServer_ChainVerifier: WithPeerChainVerifier's verifier wins, and a
// socket without one falls back to its AuthMiddleware's issuer verifier,
// nil when there is no AuthMiddleware either.
func TestServer_ChainVerifier(t *testing.T) {
	pool := &claimVerifier{}
	if got := NewServer(nil, nil, "/tmp/test-claim.sock").chainVerifier(); got != nil {
		t.Fatalf("chainVerifier() = %v with no verifier or middleware, want nil", got)
	}
	if got := NewServer(nil, nil, "/tmp/test-claim.sock", WithPeerChainVerifier(pool)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the WithPeerChainVerifier verifier", got)
	}
	mw := authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithIssuerVerifier(pool))
	if got := NewServer(nil, nil, "/tmp/test-claim.sock", WithAuthMiddleware(mw)).chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the middleware's issuer verifier", got)
	}
}
