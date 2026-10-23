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

package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// sealedClaimMethods maps each sealed-window share-claim RPC to its call in
// grpcClaimCalls. The tenant claims are not sealed-window verbs.
var sealedClaimMethods = map[string]string{
	pb.QRDBService_BarrierShareClaimBegin_FullMethodName:            "system-begin",
	pb.QRDBService_BarrierShareClaimComplete_FullMethodName:         "system-complete",
	pb.QRDBService_BarrierRecoveryShareClaimBegin_FullMethodName:    "recovery-begin",
	pb.QRDBService_BarrierRecoveryShareClaimComplete_FullMethodName: "recovery-complete",
}

// grpcPeerOnly is the context gRPC hands a server's interceptors: the peer's
// TLS info and nothing bound by qrdb. A nil state is a peer with no TLS.
func grpcPeerOnly(state *tls.ConnectionState) context.Context {
	p := &grpcpeer.Peer{}
	if state != nil {
		p.AuthInfo = credentials.TLSInfo{State: *state}
	}
	return grpcpeer.NewContext(context.Background(), p)
}

// sealedClaimServers builds the two sealed-window admission chains a share
// claim reaches with no authentication, each with v as the listener's
// verifier: the boot-time listener, and a regular listener whose node barrier
// re-sealed. The regular listener's authenticator refuses everyone, so a
// claim it admits was never authenticated.
func sealedClaimServers(t *testing.T, v auth.IssuerVerifier) map[string]func() (grpc.UnaryServerInterceptor, *countingAuthenticator) {
	t.Helper()
	return map[string]func() (grpc.UnaryServerInterceptor, *countingAuthenticator){
		"boot-listener": func() (grpc.UnaryServerInterceptor, *countingAuthenticator) {
			s := &Server{}
			WithSealedWindowListener(newProbe(true))(s)
			WithPeerChainVerifier(v)(s)
			unary, _ := s.admissionChains()
			return unary, &countingAuthenticator{}
		},
		"resealed-dispatch": func() (grpc.UnaryServerInterceptor, *countingAuthenticator) {
			authn := &countingAuthenticator{}
			s := &Server{}
			WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn)))(s)
			WithSealedWindowProbe(newProbe(true))(s)
			WithSealedWindowDispatch(newProbe(true))(s)
			WithPeerChainVerifier(v)(s)
			unary, _ := s.admissionChains()
			return unary, authn
		},
	}
}

// claimThroughChain runs the share claim fullMethod names through unary to
// the real RPC handler, from a caller whose connection is ctx.
func claimThroughChain(unary grpc.UnaryServerInterceptor, calls map[string]func(context.Context) error, ctx context.Context, fullMethod string) error {
	call := calls[sealedClaimMethods[fullMethod]]
	_, err := unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: fullMethod}, func(ctx context.Context, _ any) (any, error) {
		return nil, call(ctx)
	})
	return err
}

// TestSealedWindowClaim_PoolVerifiedCertCompletes: a holder whose certificate
// the client trust pool accepted -- the handshake built no verified chain --
// completes every share-claim begin and complete on the boot-time listener
// and on a re-sealed regular listener, bound to its own leaf, with the
// listener's verifier consulted for each and authentication never reached.
func TestSealedWindowClaim_PoolVerifiedCertCompletes(t *testing.T) {
	cert := holderCert(t)
	want := mtls.Thumbprint(cert.Raw)
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	for name, build := range sealedClaimServers(t, pool) {
		t.Run(name, func(t *testing.T) {
			pool.calls = 0
			unary, authn := build()
			calls, bound := grpcClaimCalls()
			for method := range sealedClaimMethods {
				ctx := grpcPeerOnly(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
				if err := claimThroughChain(unary, calls, ctx, method); err != nil {
					t.Fatalf("%s: %v", method, err)
				}
			}
			got := bound()
			if len(got) != len(sealedClaimMethods) {
				t.Fatalf("bound %d claims, want %d", len(got), len(sealedClaimMethods))
			}
			for _, th := range got {
				if th != want {
					t.Fatalf("claim bound %q, want %q", th, want)
				}
			}
			if pool.calls != len(sealedClaimMethods) {
				t.Fatalf("pool consulted %d times, want %d", pool.calls, len(sealedClaimMethods))
			}
			if n := authn.calls.Load(); n != 0 {
				t.Fatalf("authentication reached %d times", n)
			}
		})
	}
}

// TestSealedWindowClaim_HandshakeVerifiedCertCompletesWithoutVerifier: on a
// listener with no trust pool, a chain crypto/tls verified is enough, so the
// sealed window binds the TLS state even when there is no verifier to bind.
func TestSealedWindowClaim_HandshakeVerifiedCertCompletesWithoutVerifier(t *testing.T) {
	cert := holderCert(t)
	for name, build := range sealedClaimServers(t, nil) {
		t.Run(name, func(t *testing.T) {
			unary, _ := build()
			calls, bound := grpcClaimCalls()
			for method := range sealedClaimMethods {
				ctx := grpcPeerOnly(&tls.ConnectionState{
					PeerCertificates: []*x509.Certificate{cert},
					VerifiedChains:   [][]*x509.Certificate{{cert}},
				})
				if err := claimThroughChain(unary, calls, ctx, method); err != nil {
					t.Fatalf("%s: %v", method, err)
				}
			}
			if got := bound(); len(got) != len(sealedClaimMethods) {
				t.Fatalf("bound %d claims, want %d", len(got), len(sealedClaimMethods))
			}
		})
	}
}

// TestSealedWindowClaim_RefusesUnverifiedOrAbsentCert: through the same
// chains, a certificate the listener's verifier refuses, one with no
// verifier to resolve it, and a caller with no certificate or no TLS are all
// refused Unauthenticated -- the status of a *service.ClientCertRequiredError
// -- and no claim reaches the service.
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
		for name, build := range sealedClaimServers(t, tc.verifier) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				unary, _ := build()
				calls, bound := grpcClaimCalls()
				for method := range sealedClaimMethods {
					err := claimThroughChain(unary, calls, grpcPeerOnly(tc.state), method)
					if got := status.Code(err); got != codes.Unauthenticated {
						t.Errorf("%s: code = %s (%v), want %s", method, got, err, codes.Unauthenticated)
					}
				}
				if got := bound(); len(got) != 0 {
					t.Fatalf("claims reached the service bound to %v", got)
				}
			})
		}
	}
}

// TestServer_ChainVerifier: WithPeerChainVerifier's verifier wins, and a
// listener without one falls back to its AuthMiddleware's issuer verifier,
// nil when there is no AuthMiddleware either.
func TestServer_ChainVerifier(t *testing.T) {
	pool := &claimVerifier{}
	s := &Server{}
	if got := s.chainVerifier(); got != nil {
		t.Fatalf("chainVerifier() = %v with no verifier or middleware, want nil", got)
	}
	WithPeerChainVerifier(pool)(s)
	if got := s.chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the WithPeerChainVerifier verifier", got)
	}
	WithPeerChainVerifier(nil)(s)
	WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithIssuerVerifier(pool)))(s)
	if got := s.chainVerifier(); got != pool {
		t.Fatalf("chainVerifier() = %v, want the middleware's issuer verifier", got)
	}
}
