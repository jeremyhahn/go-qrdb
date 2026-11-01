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
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientLimitStream is a grpc.ServerStream with a fixed context.
type clientLimitStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s clientLimitStream) Context() context.Context { return s.ctx }

func unixPeer(md ...string) context.Context {
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Net: "unix"}})
	if len(md) > 0 {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(md...))
	}
	return ctx
}

// TestClientLimit_Unix: the socket's callers share one budget, and a call
// past it is refused with ResourceExhausted before authentication runs --
// a forwarded-for header, which no Unix peer is trusted to send, included.
func TestClientLimit_Unix(t *testing.T) {
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: 2}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	authn := &dispatchAuthenticator{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-client-limit.sock",
		WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
		WithClientLimiter(l))
	unary, stream := srv.admissionChains()
	info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_Get_FullMethodName}
	noop := func(context.Context, any) (any, error) { return nil, nil }

	for i := 0; i < 2; i++ {
		if _, err := unary(unixPeer(), nil, info, noop); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("call %d: err = %v, want Unauthenticated", i, err)
		}
	}
	if _, err := unary(unixPeer("x-forwarded-for", "203.0.113.9"), nil, info, noop); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over the budget, forwarded-for: err = %v, want ResourceExhausted", err)
	}
	err = stream(nil, clientLimitStream{ctx: unixPeer()}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"},
		func(any, grpc.ServerStream) error { return nil })
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream over the budget: err = %v, want ResourceExhausted", err)
	}
	if authn.calls != 2 {
		t.Fatalf("authentications = %d, want 2 (refusals never authenticate)", authn.calls)
	}
}

// TestClientLimit_UnixUnlimitedByDefault: without WithClientLimiter the
// socket applies no client-address limit.
func TestClientLimit_UnixUnlimitedByDefault(t *testing.T) {
	authn := &dispatchAuthenticator{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-client-limit.sock",
		WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))))
	unary, _ := srv.admissionChains()
	info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_Get_FullMethodName}
	for i := 0; i < 5; i++ {
		if _, err := unary(unixPeer(), nil, info, func(context.Context, any) (any, error) { return nil, nil }); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("call %d: err = %v, want Unauthenticated", i, err)
		}
	}
}
