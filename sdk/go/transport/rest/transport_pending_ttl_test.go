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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTCreateUser_OlderServerRefusesPendingTTL is the rolling-upgrade
// case. The double is a base-version server: it predates the pending-user TTL
// and ignores the unknown pending_ttl field, so it creates the pending account
// and never reports an expiry. A TTL-bearing create must fail with
// *CapabilityUnsupportedError and leave no immortal pending account; a create
// without a TTL still succeeds.
func TestRESTCreateUser_OlderServerRefusesPendingTTL(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var mu sync.Mutex
	live := map[uint64]bool{}
	var nextID uint64
	rs.handle(exactPath(http.MethodPost, "/users"), func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Pending  bool   `json:"pending"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		nextID++
		id := nextID
		live[id] = true
		mu.Unlock()
		lc := "active"
		if req.Pending {
			lc = "pending"
		}
		writeJSON(w, &transport.User{ID: id, Username: req.Username, Lifecycle: lc})
	})
	rs.handle(func(r *http.Request) bool {
		return r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/users/")
	}, func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/users/"), 10, 64)
		mu.Lock()
		delete(live, id)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	u, err := rt.CreateUser(context.Background(), &transport.CreateUserRequest{
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

	if _, err := rt.CreateUser(context.Background(), &transport.CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nottl", Pending: true,
	}); err != nil {
		t.Fatalf("CreateUser without a TTL on an older server: %v", err)
	}
}
