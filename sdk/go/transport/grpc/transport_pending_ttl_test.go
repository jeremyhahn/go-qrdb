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

package grpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A negative TTL is refused before it is cut to whole seconds: "-500ms"
// would otherwise truncate to 0 on the wire and create an account that never
// expires.
func TestGRPCUser_CreateUser_RefusesNegativePendingTTL(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	for _, ttl := range []string{"-500ms", "-1ns", "-2s"} {
		t.Run(ttl, func(t *testing.T) {
			u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "neg", TenantID: "system",
				Pending: true, PendingTTL: ttl,
			})
			var neg *transport.NegativePendingTTLError
			if !errors.As(err, &neg) {
				t.Fatalf("CreateUser(pending_ttl=%s) = %v, %v; want NegativePendingTTLError", ttl, u, err)
			}
		})
	}
}

// The gRPC wire carries whole seconds, so a positive TTL that is not a whole
// number of seconds would truncate: early expiry, or none under one second.
// The client refuses it before sending.
func TestGRPCUser_CreateUser_RefusesFractionalPendingTTL(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	for _, ttl := range []string{"500ms", "1ns", "999ms", "1500ms", "1999ms"} {
		t.Run(ttl, func(t *testing.T) {
			u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "sub", TenantID: "system",
				Pending: true, PendingTTL: ttl,
			})
			var sub *transport.FractionalPendingTTLError
			if !errors.As(err, &sub) {
				t.Fatalf("CreateUser(pending_ttl=%s) = %v, %v; want FractionalPendingTTLError", ttl, u, err)
			}
		})
	}
}

// olderUserServicer stands in for a base-version server, which predates the
// pending-user TTL: protobuf drops the unknown pending_ttl_seconds, so it
// creates the pending account the request asks for and never sets an expiry.
// It keeps the accounts it holds so a test can see what survives.
type olderUserServicer struct {
	fakeUserServicer
	mu     sync.Mutex
	nextID uint64
	live   map[uint64]*user.User
}

func (o *olderUserServicer) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nextID++
	lc := user.LifecycleActive
	if req.Pending {
		lc = user.LifecyclePending
	}
	u := &user.User{ID: o.nextID, Username: req.Username, TenantID: req.TenantID, Lifecycle: lc,
		CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}
	if o.live == nil {
		o.live = map[uint64]*user.User{}
	}
	o.live[u.ID] = u
	return u, nil
}

func (o *olderUserServicer) DeleteUser(_ context.Context, id uint64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.live, id)
	return nil
}

func (o *olderUserServicer) survivors() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.live)
}

// TestGRPCUser_CreateUser_OlderServerRefusesPendingTTL is the rolling-upgrade
// case: a TTL-bearing create sent to a server that cannot honour the TTL must
// fail with *CapabilityUnsupportedError and leave no immortal pending account,
// while a create without a TTL still succeeds against the same server.
func TestGRPCUser_CreateUser_OlderServerRefusesPendingTTL(t *testing.T) {
	older := &olderUserServicer{}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterUserService(older))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	tr, cleanup := connectUserTransport(t, lis.Addr().String())
	defer cleanup()

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "ttl", TenantID: "system",
		Pending: true, PendingTTL: "24h",
	})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("CreateUser(pending_ttl=24h) on an older server = %+v, %v; want CapabilityUnsupportedError", u, err)
	}
	if n := older.survivors(); n != 0 {
		t.Fatalf("%d pending account(s) survive with no expiry; want 0", n)
	}

	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nottl", TenantID: "system", Pending: true,
	}); err != nil {
		t.Fatalf("CreateUser without a TTL on an older server: %v", err)
	}
}
