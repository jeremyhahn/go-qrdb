//go:build qrdb_etcd

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

package etcd

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientLimitedEtcd returns the admission chains of an etcd listener whose
// client-address limiter holds burst tokens per address and never refills
// within a test.
func clientLimitedEtcd(t *testing.T, authz *recordingAuthorizer, burst int) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	t.Helper()
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	s := NewServer("db", ":0", WithRequestAuthorizer(authz), WithClientLimiter(l))
	return s.admissionChains()
}

func etcdPeer(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 2379}})
}

func TestClientLimit_RefusesBeforeAuthorization(t *testing.T) {
	authz := &recordingAuthorizer{}
	unary, stream := clientLimitedEtcd(t, authz, 2)
	info := &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Range"}
	streamInfo := &grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch", IsServerStream: true, IsClientStream: true}
	unaryHandler := func(context.Context, any) (any, error) { return "ok", nil }
	streamHandler := func(any, grpc.ServerStream) error { return nil }

	for i := 0; i < 2; i++ {
		if _, err := unary(etcdPeer("192.0.2.1"), nil, info, unaryHandler); err != nil {
			t.Fatalf("call %d within the burst: %v", i, err)
		}
	}
	if _, err := unary(etcdPeer("192.0.2.1"), nil, info, unaryHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over the burst: err = %v, want ResourceExhausted", err)
	}
	if err := stream(nil, &fakeStream{ctx: etcdPeer("192.0.2.1")}, streamInfo, streamHandler); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream over the burst: err = %v, want ResourceExhausted", err)
	}
	if n := len(authz.asked); n != 2 {
		t.Fatalf("authorizations = %d, want 2 (refusals never authorize)", n)
	}

	if _, err := unary(etcdPeer("198.51.100.7"), nil, info, unaryHandler); err != nil {
		t.Fatalf("other address: %v", err)
	}
	if err := stream(nil, &fakeStream{ctx: etcdPeer("198.51.100.7")}, streamInfo, streamHandler); err != nil {
		t.Fatalf("other address, stream: %v", err)
	}
}

func TestClientLimit_NilLimiterAdmitsEveryCall(t *testing.T) {
	authz := &recordingAuthorizer{}
	s := NewServer("db", ":0", WithRequestAuthorizer(authz), WithClientLimiter(nil))
	unary, _ := s.admissionChains()
	info := &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Range"}
	for i := 0; i < 5; i++ {
		if _, err := unary(etcdPeer("192.0.2.1"), nil, info, func(context.Context, any) (any, error) { return nil, nil }); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := len(authz.asked); n != 5 {
		t.Fatalf("authorizations = %d, want 5", n)
	}
}
