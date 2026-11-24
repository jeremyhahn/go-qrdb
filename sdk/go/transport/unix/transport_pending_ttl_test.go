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

package unix

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A negative TTL is refused before it is cut to whole seconds, and the
// server is never asked: "-500ms" would otherwise truncate to 0 on the wire
// and create an account that never expires.
func TestUnixRT_CreateUser_RefusesNegativePendingTTL(t *testing.T) {
	var calls atomic.Int32
	usvc := &mockUserSvc{createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
		calls.Add(1)
		return &user.User{ID: 1, Username: req.Username}, nil
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	for _, ttl := range []string{"-500ms", "-1ns", "-2s"} {
		_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
			WithoutEmailLoginKey: true, Username: "neg", Pending: true, PendingTTL: ttl,
		})
		var neg *transport.NegativePendingTTLError
		if !errors.As(err, &neg) {
			t.Fatalf("CreateUser(pending_ttl=%s) err = %v, want NegativePendingTTLError", ttl, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("server CreateUser called %d times for refused TTLs", n)
	}
}

// The unix wire carries whole seconds, so a positive TTL that is not a whole
// number of seconds would truncate: early expiry, or none under one second. It
// is refused before the request is sent, and a whole-second TTL still reaches
// the server.
func TestUnixRT_CreateUser_RefusesFractionalPendingTTL(t *testing.T) {
	var calls atomic.Int32
	var gotTTL atomic.Int64
	usvc := &mockUserSvc{createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
		calls.Add(1)
		gotTTL.Store(int64(req.PendingTTL))
		// A current server honours the TTL and reports the expiry.
		at := time.Unix(1700000000, 0).Add(req.PendingTTL)
		return &user.User{ID: 1, Username: req.Username, Lifecycle: user.LifecyclePending, PendingExpiresAt: &at}, nil
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	for _, ttl := range []string{"500ms", "1ns", "999ms", "1500ms", "1999ms"} {
		_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
			WithoutEmailLoginKey: true, Username: "sub", Pending: true, PendingTTL: ttl,
		})
		var sub *transport.FractionalPendingTTLError
		if !errors.As(err, &sub) {
			t.Fatalf("CreateUser(pending_ttl=%s) err = %v, want FractionalPendingTTLError", ttl, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("server CreateUser called %d times for refused TTLs", n)
	}
	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "whole", Pending: true, PendingTTL: "2s",
	}); err != nil {
		t.Fatalf("CreateUser(pending_ttl=2s): %v", err)
	}
	if got := time.Duration(gotTTL.Load()); got != 2*time.Second {
		t.Fatalf("server saw pending TTL %v, want 2s", got)
	}
}

// TestUnixRT_CreateUser_OlderServerRefusesPendingTTL is the rolling-upgrade
// case. The double is a base-version server: it predates the pending-user TTL
// (protobuf drops pending_ttl_seconds), so it creates the pending account and
// never sets an expiry. A TTL-bearing create must fail with
// *CapabilityUnsupportedError and leave no immortal pending account; a create
// without a TTL still succeeds.
func TestUnixRT_CreateUser_OlderServerRefusesPendingTTL(t *testing.T) {
	var creates, deletes atomic.Int32
	usvc := &mockUserSvc{
		createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			id := uint64(creates.Add(1))
			lc := user.LifecycleActive
			if req.Pending {
				lc = user.LifecyclePending
			}
			return &user.User{ID: id, Username: req.Username, Lifecycle: lc}, nil
		},
		deleteFn: func(_ context.Context, _ uint64) error {
			deletes.Add(1)
			return nil
		},
	}
	// The older server's capabilities carry no pendingUserTTL bit.
	older := newMockAdminService()
	older.olderServer = true
	tr, cleanup := startAuthServerWithAdmin(t, older, unixserver.WithUserService(usvc))
	defer cleanup()

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "ttl", Pending: true, PendingTTL: "24h",
	})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("CreateUser(pending_ttl=24h) on an older server = %+v, %v; want CapabilityUnsupportedError", u, err)
	}
	if c, d := creates.Load(), deletes.Load(); c != 0 || d != 0 {
		t.Fatalf("older server saw %d CreateUser and %d DeleteUser request(s); want 0 and 0 (refused before sending)", c, d)
	}

	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nottl", Pending: true,
	}); err != nil {
		t.Fatalf("CreateUser without a TTL on an older server: %v", err)
	}
}
