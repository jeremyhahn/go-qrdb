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

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func newPendingReaperUsers(t *testing.T) user.UserServicer {
	t.Helper()
	idGen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	users, err := user.NewStore(kvstore.NewMemoryKVStore(), idGen)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return users
}

// TestServer_PendingUserReaperStartStop drives the node's reaper through the
// tick hook: off-leader ticks delete nothing, a leader tick deletes the
// expired pending user, and stop ends the goroutine.
func TestServer_PendingUserReaperStartStop(t *testing.T) {
	users := newPendingReaperUsers(t)
	ctx := context.Background()
	u, err := users.CreateUser(ctx, &user.CreateUserRequest{
		Username:   "pam",
		Email:      "pam@example.com",
		TenantID:   tenantid.System,
		Pending:    true,
		PendingTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var leader atomic.Bool
	now := time.Now().UTC().Add(time.Hour)
	ticks := make(chan time.Time)
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithUserService(users),
		WithPendingUserReaper(0, leader.Load),
		withPendingUserTicks(ticks, func() time.Time { return now }),
	)
	srv.startPendingUserReaper()
	if srv.pendingReaperCancel == nil {
		t.Fatal("reaper not started")
	}

	// The second send blocks until Run has finished the first tick.
	ticks <- now
	ticks <- now
	if _, err := users.GetUser(ctx, u.ID); err != nil {
		t.Fatalf("off-leader tick reaped the user: %v", err)
	}

	leader.Store(true)
	ticks <- now
	ticks <- now
	var nf *user.UserNotFoundError
	if _, err := users.GetUser(ctx, u.ID); !errors.As(err, &nf) {
		t.Fatalf("leader tick did not reap the expired pending user: %v", err)
	}

	srv.stopPendingUserReaper()
	if srv.pendingReaperCancel != nil {
		t.Fatal("stop left the cancel func set")
	}
	srv.wg.Wait()
	srv.stopPendingUserReaper() // idempotent
}

func TestServer_PendingUserReaperDisabled(t *testing.T) {
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithUserService(newPendingReaperUsers(t)),
		WithPendingUserReaper(-1, nil),
	)
	srv.startPendingUserReaper()
	if srv.pendingReaperCancel != nil {
		t.Fatal("a negative interval started the reaper")
	}

	srv = New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})
	srv.startPendingUserReaper()
	if srv.pendingReaperCancel != nil {
		t.Fatal("the reaper started without a user service")
	}
}
