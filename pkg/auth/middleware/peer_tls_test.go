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

package middleware

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// peerTLSTestStream is a grpc.ServerStream whose context is ctx.
type peerTLSTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *peerTLSTestStream) Context() context.Context { return s.ctx }

// peerOnlyContext is the context gRPC hands an interceptor for a peer whose
// connection state is state, nil for a peer with no TLS.
func peerOnlyContext(state *tls.ConnectionState) context.Context {
	p := &peer.Peer{}
	if state != nil {
		p.AuthInfo = credentials.TLSInfo{State: *state}
	}
	return peer.NewContext(context.Background(), p)
}

// TestInjectGRPCPeerTLS_BindsStateAndVerifier: the peer's TLS state and the
// verifier are both bound, so a certificate the handshake built no chain for
// resolves through the verifier.
func TestInjectGRPCPeerTLS_BindsStateAndVerifier(t *testing.T) {
	state, leaf := unverifiedState(t)
	ctx := InjectGRPCPeerTLS(peerOnlyContext(state), poolVerifier{})
	if got := auth.TLSStateFromContext(ctx); got == nil || len(got.PeerCertificates) != 1 || got.PeerCertificates[0] != leaf {
		t.Fatalf("TLS state = %v, want the peer's", got)
	}
	got, err := auth.VerifiedPeerCertificate(ctx)
	if err != nil || got != leaf {
		t.Fatalf("VerifiedPeerCertificate = %v, %v; want the pool-verified leaf", got, err)
	}
}

// TestInjectGRPCPeerTLS_NilVerifierAndNoTLS: a nil verifier binds the state
// alone, so an unverified certificate stays unresolved, and a peer with no
// TLS -- or no peer at all -- binds no state.
func TestInjectGRPCPeerTLS_NilVerifierAndNoTLS(t *testing.T) {
	state, _ := unverifiedState(t)
	ctx := InjectGRPCPeerTLS(peerOnlyContext(state), nil)
	if auth.TLSStateFromContext(ctx) == nil {
		t.Fatal("TLS state not bound with a nil verifier")
	}
	if _, err := auth.VerifiedPeerCertificate(ctx); err == nil {
		t.Fatal("an unverified certificate resolved with no verifier")
	}
	for name, ctx := range map[string]context.Context{
		"no-tls":  peerOnlyContext(nil),
		"no-peer": context.Background(),
	} {
		if got := auth.TLSStateFromContext(InjectGRPCPeerTLS(ctx, poolVerifier{})); got != nil {
			t.Fatalf("%s: TLS state = %v, want none", name, got)
		}
	}
}

// TestPeerTLSUnaryInterceptor_HandlerSeesBinding: the handler a unary call
// reaches sees the peer's TLS state and the verifier.
func TestPeerTLSUnaryInterceptor_HandlerSeesBinding(t *testing.T) {
	state, leaf := unverifiedState(t)
	var got error
	_, err := PeerTLSUnaryInterceptor(poolVerifier{})(peerOnlyContext(state), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		cert, err := auth.VerifiedPeerCertificate(ctx)
		if err == nil && cert != leaf {
			err = errors.New("resolved a certificate other than the leaf")
		}
		got = err
		return nil, nil
	})
	if err != nil || got != nil {
		t.Fatalf("interceptor = %v, handler resolution = %v", err, got)
	}
}

// TestPeerTLSUnaryInterceptor_PassesHandlerResult: the interceptor returns
// what the handler returns, and a peer without TLS reaches it unbound.
func TestPeerTLSUnaryInterceptor_PassesHandlerResult(t *testing.T) {
	want := errors.New("handler refused")
	resp, err := PeerTLSUnaryInterceptor(nil)(peerOnlyContext(nil), "req", &grpc.UnaryServerInfo{}, func(ctx context.Context, req any) (any, error) {
		if auth.TLSStateFromContext(ctx) != nil {
			t.Fatal("TLS state bound for a peer with no TLS")
		}
		return req, want
	})
	if resp != "req" || !errors.Is(err, want) {
		t.Fatalf("interceptor = %v, %v; want the handler's result", resp, err)
	}
}

// TestPeerTLSStreamInterceptor_HandlerSeesBinding: the stream a streaming
// handler reaches carries the peer's TLS state and the verifier.
func TestPeerTLSStreamInterceptor_HandlerSeesBinding(t *testing.T) {
	state, leaf := unverifiedState(t)
	var got error
	err := PeerTLSStreamInterceptor(poolVerifier{})(nil, &peerTLSTestStream{ctx: peerOnlyContext(state)}, &grpc.StreamServerInfo{}, func(_ any, ss grpc.ServerStream) error {
		cert, err := auth.VerifiedPeerCertificate(ss.Context())
		if err == nil && cert != leaf {
			err = errors.New("resolved a certificate other than the leaf")
		}
		got = err
		return nil
	})
	if err != nil || got != nil {
		t.Fatalf("interceptor = %v, handler resolution = %v", err, got)
	}
}

// TestPeerTLSStreamInterceptor_PassesHandlerError: the interceptor returns
// the handler's error, and a peer without TLS reaches it unbound.
func TestPeerTLSStreamInterceptor_PassesHandlerError(t *testing.T) {
	want := errors.New("stream refused")
	err := PeerTLSStreamInterceptor(nil)(nil, &peerTLSTestStream{ctx: peerOnlyContext(nil)}, &grpc.StreamServerInfo{}, func(_ any, ss grpc.ServerStream) error {
		if auth.TLSStateFromContext(ss.Context()) != nil {
			t.Fatal("TLS state bound for a peer with no TLS")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("interceptor = %v, want the handler's error", err)
	}
}
