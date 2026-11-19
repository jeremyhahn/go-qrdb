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

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// countAction counts the events the ring holds with the given action.
func countAction(ring *audit.RingBuffer, action string) int {
	n := 0
	for _, ev := range ring.Snapshot() {
		if ev.Action == action {
			n++
		}
	}
	return n
}

// The SDK path hands the node's audit logger to the user stores it builds,
// so the pending-user reaper running over the SDK-built user service audits
// each reap (pending_reaped) instead of deleting the account silently.
func TestBuildServices_PendingReapIsAudited(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	al := audit.NewRingBuffer(64)
	ctx := context.Background()
	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:        host,
		WatchHub:    hub,
		DataDir:     t.TempDir(),
		AuditLogger: al,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	ctx = tenant.WithTenantID(ctx, "acme")
	u, err := node.User.CreateUser(ctx, &user.CreateUserRequest{
		Username:   "lapsed",
		Email:      "lapsed@example.com",
		TenantID:   "acme",
		Pending:    true,
		PendingTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	later := time.Now().UTC().Add(time.Hour)
	r, err := user.NewPendingReaper(user.PendingReaperConfig{
		Users: node.User,
		Now:   func() time.Time { return later },
	})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1, nil", n, err)
	}
	if _, err := node.User.GetUser(ctx, u.ID); err == nil {
		t.Fatal("expired pending account survived the sweep")
	}
	if got := countAction(al, user.AuditActionPendingReaped); got != 1 {
		t.Fatalf("%s audits = %d, want 1", user.AuditActionPendingReaped, got)
	}
}
