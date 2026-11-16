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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_CreateUser_OlderServerRefusesPendingTTL is the rolling-upgrade
// case. The double is a base-version server: it predates the pending-user TTL
// and ignores the unknown pendingTtl field, so it creates the pending account
// and never reports an expiry. A TTL-bearing create must fail with
// *CapabilityUnsupportedError and leave no immortal pending account; a create
// without a TTL still succeeds.
func TestE2E_CreateUser_OlderServerRefusesPendingTTL(t *testing.T) {
	srv, tr := startE2EServer(t)
	var mu sync.Mutex
	live := map[uint64]bool{}
	var nextID uint64
	srv.SetResponder("user.Create", func(payload []byte) (uint16, []byte) {
		var req struct {
			Username string `json:"username"`
			Pending  bool   `json:"pending"`
		}
		_ = json.Unmarshal(payload, &req)
		mu.Lock()
		defer mu.Unlock()
		nextID++
		live[nextID] = true
		lc := "active"
		if req.Pending {
			lc = "pending"
		}
		return wire.StatusOK, userJSON(&userWireResponse{ID: nextID, Username: req.Username, Lifecycle: lc})
	})
	srv.SetResponder("user.Delete", func(payload []byte) (uint16, []byte) {
		var req deleteUserWireRequest
		_ = json.Unmarshal(payload, &req)
		mu.Lock()
		defer mu.Unlock()
		delete(live, req.ID)
		return wire.StatusOK, nil
	})

	u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "ttl", Pending: true, PendingTTL: "24h",
	})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("CreateUser(pending_ttl=24h) on an older server = %+v, %v; want CapabilityUnsupportedError", u, err)
	}
	mu.Lock()
	n := len(live)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d pending account(s) survive with no expiry; want 0", n)
	}

	if _, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nottl", Pending: true,
	}); err != nil {
		t.Fatalf("CreateUser without a TTL on an older server: %v", err)
	}
}
