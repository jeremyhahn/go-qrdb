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

package middleware

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// poolVerifier is a trust pool that verifies every presented leaf, so a
// handler can read a verified certificate only if the transport bound it.
type poolVerifier struct{}

func (poolVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, _ []*x509.Certificate) ([]*x509.Certificate, error) {
	return []*x509.Certificate{leaf}, nil
}
func (poolVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }
func (poolVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return errors.New("not reached")
}

var _ auth.IssuerVerifier = poolVerifier{}

// verifiedLeafRecorder is an authenticator that records the verified client
// certificate its context resolves, then refuses: what it saw is what any
// handler behind the transport's auth chain would see.
type verifiedLeafRecorder struct {
	leaf *x509.Certificate
	err  error
}

func (r *verifiedLeafRecorder) Authenticate(ctx context.Context) (*auth.Identity, error) {
	r.leaf, r.err = auth.VerifiedPeerCertificate(ctx)
	return nil, auth.ErrNoPeerCertificate
}

func (r *verifiedLeafRecorder) Name() string { return "verified-leaf-recorder" }

// unverifiedState is a connection that presented leaf with no chain the TLS
// stack verified: the shape of a listener whose trust pool verifies chains.
func unverifiedState(t *testing.T) (*tls.ConnectionState, *x509.Certificate) {
	t.Helper()
	ca := newIssuerCA(t, "presented-ca")
	leaf, _ := ca.nodeLeaf(t, obNodeID)
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}, leaf
}

// Every transport's auth chain binds the middleware's chain verifier into the
// request context, so a handler reading the caller's verified certificate
// resolves it through the trust pool; with none wired, a presented but
// unverified certificate resolves to nothing.
func TestPeerChainVerifier_BoundByEveryTransport(t *testing.T) {
	transports := map[string]func(m *AuthMiddleware, state *tls.ConnectionState){
		"rest": func(m *AuthMiddleware, state *tls.ConnectionState) {
			r := httptest.NewRequest(http.MethodGet, "/kv/foo", nil)
			r.TLS = state
			m.RESTMiddleware(testRESTRoutes(), http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
		},
		"grpc unary": func(m *AuthMiddleware, state *tls.ConnectionState) {
			ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: *state}})
			_, _ = m.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Get"},
				func(context.Context, any) (any, error) { return nil, nil })
		},
		"grpc stream": func(m *AuthMiddleware, state *tls.ConnectionState) {
			ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: *state}})
			_ = m.StreamInterceptor()(nil, &wrappedStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"},
				func(any, grpc.ServerStream) error { return nil })
		},
	}
	for name, run := range transports {
		t.Run(name, func(t *testing.T) {
			state, leaf := unverifiedState(t)
			rec := &verifiedLeafRecorder{}
			run(newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(rec), WithIssuerVerifier(poolVerifier{})), state)
			if rec.err != nil || rec.leaf != leaf {
				t.Fatalf("with the pool wired: leaf %v, err %v; want the pool-verified leaf", rec.leaf, rec.err)
			}

			rec = &verifiedLeafRecorder{}
			run(newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(rec)), state)
			if rec.leaf != nil || !errors.Is(rec.err, auth.ErrInvalidCertificate) {
				t.Fatalf("with no verifier: leaf %v, err %v; want ErrInvalidCertificate", rec.leaf, rec.err)
			}
		})
	}
}
