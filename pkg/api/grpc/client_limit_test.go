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
	"net"
	"net/http"
	"net/http/httptest"
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

// clientLimitedServer is a listener's admission chains, as pkg/server
// composes them, behind a client-address limiter whose bucket holds burst
// requests and does not refill within a test.
func clientLimitedServer(t *testing.T, authn *countingAuthenticator, burst int) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	t.Helper()
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := &Server{}
	WithAuthMiddleware(authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn)))(s)
	WithClientLimiter(l)(s)
	return s.admissionChains()
}

func fromPeer(ip string, md ...string) context.Context {
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}})
	if len(md) > 0 {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(md...))
	}
	return ctx
}

// TestClientLimit_RefusesBeforeAuthentication: an address past its burst is
// refused with ResourceExhausted before authentication runs, on unary calls
// and streams alike, while another address is still authenticated.
func TestClientLimit_RefusesBeforeAuthentication(t *testing.T) {
	authn := &countingAuthenticator{}
	unary, stream := clientLimitedServer(t, authn, 2)
	info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_Get_FullMethodName}
	streamInfo := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}

	for i := 0; i < 2; i++ {
		if _, err := unary(fromPeer("192.0.2.1"), nil, info, dummyHandler); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("call %d: err = %v, want Unauthenticated", i, err)
		}
	}
	if _, err := unary(fromPeer("192.0.2.1"), nil, info, dummyHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over the burst: err = %v, want ResourceExhausted", err)
	}
	err := stream(nil, &ctxServerStream{ctx: fromPeer("192.0.2.1")}, streamInfo, dummyStreamHandler)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream over the burst: err = %v, want ResourceExhausted", err)
	}
	if n := authn.calls.Load(); n != 2 {
		t.Fatalf("authentications = %d, want 2 (refusals never authenticate)", n)
	}

	if _, err := unary(fromPeer("198.51.100.7"), nil, info, dummyHandler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("other address: err = %v, want Unauthenticated", err)
	}
	err = stream(nil, &ctxServerStream{ctx: fromPeer("198.51.100.7")}, streamInfo, dummyStreamHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("other address, stream: err = %v, want Unauthenticated", err)
	}
	if n := authn.calls.Load(); n != 4 {
		t.Fatalf("authentications = %d, want 4", n)
	}
}

// TestClientLimit_UntrustedForwardedHeaderKeepsTheKey: with no trusted
// proxy, a forwarded-for header naming a fresh client does not move the
// request to a fresh bucket.
func TestClientLimit_UntrustedForwardedHeaderKeepsTheKey(t *testing.T) {
	authn := &countingAuthenticator{}
	unary, _ := clientLimitedServer(t, authn, 1)
	info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_Get_FullMethodName}
	if _, err := unary(fromPeer("192.0.2.1"), nil, info, dummyHandler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("first call: err = %v, want Unauthenticated", err)
	}
	for _, md := range [][]string{
		{"x-forwarded-for", "203.0.113.9"},
		{"forwarded", "for=203.0.113.9"},
	} {
		if _, err := unary(fromPeer("192.0.2.1", md...), nil, info, dummyHandler); status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("%s from an untrusted peer: err = %v, want ResourceExhausted", md[0], err)
		}
	}
	if n := authn.calls.Load(); n != 1 {
		t.Fatalf("authentications = %d, want 1", n)
	}
}

// TestClientLimit_NilLimiterAdmitsEverything: without a limiter the chain
// is the plain admission chain.
func TestClientLimit_NilLimiterAdmitsEverything(t *testing.T) {
	unary := ClientLimitUnaryInterceptor(nil)
	stream := ClientLimitStreamInterceptor(nil)
	for i := 0; i < 3; i++ {
		if _, err := unary(fromPeer("192.0.2.1"), nil, &grpc.UnaryServerInfo{}, dummyHandler); err != nil {
			t.Fatalf("unary: %v", err)
		}
		if err := stream(nil, &ctxServerStream{ctx: fromPeer("192.0.2.1")}, &grpc.StreamServerInfo{}, dummyStreamHandler); err != nil {
			t.Fatalf("stream: %v", err)
		}
	}
}

// chargedAtHTTP returns a context l marked charged through HTTPOnce, as the
// gRPC-Web translator's request carries it, with the call's peer address.
func chargedAtHTTP(t *testing.T, l *clientlimit.Limiter, ip string) context.Context {
	t.Helper()
	var ctx context.Context
	h := l.HTTPOnce(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() }))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = net.JoinHostPort(ip, "40000")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if ctx == nil {
		t.Fatal("HTTPOnce refused the charge")
	}
	return peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}})
}

// TestClientLimit_CallChargedAtHTTPIsChargedOnce is the second finding of
// PR #1253's interim review, gRPC side: a gRPC-Web call the HTTP limiter
// charged is not charged again by the interceptors sharing that limiter,
// while an unmarked call from the same address still is.
func TestClientLimit_CallChargedAtHTTPIsChargedOnce(t *testing.T) {
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: 2}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	unary, stream := ClientLimitUnaryInterceptor(l), ClientLimitStreamInterceptor(l)
	marked := chargedAtHTTP(t, l, "192.0.2.1")
	for i := 0; i < 3; i++ {
		if _, err := unary(marked, nil, &grpc.UnaryServerInfo{}, dummyHandler); err != nil {
			t.Fatalf("unary %d charged at HTTP: %v", i, err)
		}
		if err := stream(nil, &ctxServerStream{ctx: marked}, &grpc.StreamServerInfo{}, dummyStreamHandler); err != nil {
			t.Fatalf("stream %d charged at HTTP: %v", i, err)
		}
	}
	if _, err := unary(fromPeer("192.0.2.1"), nil, &grpc.UnaryServerInfo{}, dummyHandler); err != nil {
		t.Fatalf("unmarked call within the burst: %v", err)
	}
	if _, err := unary(fromPeer("192.0.2.1"), nil, &grpc.UnaryServerInfo{}, dummyHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("unmarked call past the burst = %v, want ResourceExhausted", err)
	}
}

// TestClientLimit_OtherLimitersMarkIsCharged: a mark left by a different
// limiter does not stand for this one's charge.
func TestClientLimit_OtherLimitersMarkIsCharged(t *testing.T) {
	newL := func() *clientlimit.Limiter {
		l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: 1}})
		if err != nil {
			t.Fatalf("clientlimit.New: %v", err)
		}
		t.Cleanup(func() { _ = l.Close() })
		return l
	}
	other, own := newL(), newL()
	marked := chargedAtHTTP(t, other, "192.0.2.1")
	unary := ClientLimitUnaryInterceptor(own)
	if _, err := unary(marked, nil, &grpc.UnaryServerInfo{}, dummyHandler); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := unary(marked, nil, &grpc.UnaryServerInfo{}, dummyHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second call = %v, want ResourceExhausted", err)
	}
}
