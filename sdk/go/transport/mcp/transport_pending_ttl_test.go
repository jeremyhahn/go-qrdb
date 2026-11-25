// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// olderUserSvc stands in for a base-version server, which predates the
// pending-user TTL: it ignores the unknown pending_ttl argument, creates the
// pending account the request asks for and never sets an expiry. It keeps
// the accounts it holds so a test can see what survives.
type olderUserSvc struct {
	*mockUserSvc
	liveMu sync.Mutex
	nextID uint64
	live   map[uint64]bool
}

func (o *olderUserSvc) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	o.nextID++
	o.live[o.nextID] = true
	lc := user.LifecycleActive
	if req.Pending {
		lc = user.LifecyclePending
	}
	return &user.User{ID: o.nextID, Username: req.Username, TenantID: req.TenantID, Lifecycle: lc,
		CreatedAt: time.Unix(1700000000, 0), UpdatedAt: time.Unix(1700000000, 0)}, nil
}

func (o *olderUserSvc) DeleteUser(_ context.Context, id uint64) error {
	o.liveMu.Lock()
	defer o.liveMu.Unlock()
	delete(o.live, id)
	return nil
}

// TestTransport_CreateUser_OlderServerRefusesPendingTTL is the rolling-upgrade
// case: a TTL-bearing create sent to a server that cannot honour the TTL must
// fail with *CapabilityUnsupportedError and leave no immortal pending account,
// while a create without a TTL still succeeds against the same server.
func TestTransport_CreateUser_OlderServerRefusesPendingTTL(t *testing.T) {
	older := &olderUserSvc{mockUserSvc: newMockUserSvc(), live: map[uint64]bool{}}
	roles := newMockRoleSvc()
	roles.getResp.TenantID = "system"
	tr, cleanup := connectAuthInProcess(t, older, roles, nil)
	defer cleanup()

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "ttl", TenantID: "system",
		Pending: true, PendingTTL: "24h",
	})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("CreateUser(pending_ttl=24h) on an older server = %+v, %v; want CapabilityUnsupportedError", u, err)
	}
	older.liveMu.Lock()
	n := len(older.live)
	older.liveMu.Unlock()
	if n != 0 {
		t.Fatalf("%d pending account(s) survive with no expiry; want 0", n)
	}

	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nottl", TenantID: "system", Pending: true,
	}); err != nil {
		t.Fatalf("CreateUser without a TTL on an older server: %v", err)
	}
}

// A negative or malformed TTL is refused before the tool call leaves the
// client: an older server ignores pending_ttl, so sending it would create a
// pending account that never expires while EnforcePendingTTL, unable to
// parse the TTL, reported success.
func TestTransport_CreateUser_InvalidPendingTTLNeverSent(t *testing.T) {
	older := &olderUserSvc{mockUserSvc: newMockUserSvc(), live: map[uint64]bool{}}
	roles := newMockRoleSvc()
	roles.getResp.TenantID = "system"
	tr, cleanup := connectAuthInProcess(t, older, roles, nil)
	defer cleanup()

	for _, ttl := range []string{"-1h", "-500ms", "soon"} {
		u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
			WithoutEmailLoginKey: true, Username: "ttl", TenantID: "system",
			Pending: true, PendingTTL: ttl,
		})
		var reqErr *transport.RequestError
		if !errors.As(err, &reqErr) || u != nil {
			t.Fatalf("CreateUser(pending_ttl=%q) = %+v, %v; want a RequestError", ttl, u, err)
		}
	}
	older.liveMu.Lock()
	n := len(older.live)
	older.liveMu.Unlock()
	if n != 0 {
		t.Fatalf("%d pending account(s) created on the older server; want 0", n)
	}
}
