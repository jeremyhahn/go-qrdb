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

// The unix wire carries whole seconds, so a positive sub-second TTL would
// truncate to 0 -- no expiry. It is refused before the request is sent, and a
// whole-second TTL still reaches the server.
func TestUnixRT_CreateUser_RefusesSubSecondPendingTTL(t *testing.T) {
	var calls atomic.Int32
	var gotTTL atomic.Int64
	usvc := &mockUserSvc{createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
		calls.Add(1)
		gotTTL.Store(int64(req.PendingTTL))
		return &user.User{ID: 1, Username: req.Username}, nil
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc))
	defer cleanup()

	for _, ttl := range []string{"500ms", "1ns", "999ms"} {
		_, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
			WithoutEmailLoginKey: true, Username: "sub", Pending: true, PendingTTL: ttl,
		})
		var sub *transport.SubSecondPendingTTLError
		if !errors.As(err, &sub) {
			t.Fatalf("CreateUser(pending_ttl=%s) err = %v, want SubSecondPendingTTLError", ttl, err)
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
