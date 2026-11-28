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

package conformance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// activatePendingPermissions are the verbs the suite calls: creating and
// disabling the target, reading it back, and the activation itself, which is
// authorized as users:enable.
var activatePendingPermissions = []permission.Permission{
	permission.UsersCreate, permission.UsersRead, permission.UsersDisable,
	permission.UsersEnable,
}

// activatePendingRacers is how many callers race to activate one account.
const activatePendingRacers = 8

// TestActivatePending_Matrix drives UserClient.ActivatePending against a real
// node on every harness transport, standalone and raft (ruling 1350): of N
// concurrent activations of one pending account exactly one commits and every
// other caller is refused with a typed *transport.AccountNotPendingError; an
// account disabled first is refused and stays disabled.
func TestActivatePending_Matrix(t *testing.T) {
	for _, c := range coordinationCells() {
		// The objstore identity store cannot hold a user record at all (see
		// coordinationCells), so it has no account to activate.
		if c.backend == database.BackendObjstore {
			continue
		}
		t.Run(c.name(), func(t *testing.T) {
			c.parallel(t)
			client, ctx := c.bootClientGranting(t, activatePendingPermissions)
			users := client.Users()
			t.Run("concurrent-race", func(t *testing.T) {
				// The file engine's Get takes no lock against a batch commit,
				// so a concurrent reader can see the new metadata revision
				// before its data record ("mvcc: revision N not found"): a
				// storage read race outside ActivatePending, reported with
				// #1765. Pebble and memory standalone and raft cover the race.
				if c.backend == database.BackendFile {
					t.Skip("file engine: unlocked Get races a batch commit (mvcc revision not found)")
				}
				assertActivatePendingRace(t, subtestCtx(t, ctx), users)
			})
			t.Run("disabled-refused", func(t *testing.T) { assertDisabledNotActivatedPending(t, subtestCtx(t, ctx), users) })
		})
	}
}

func createPendingAccount(t *testing.T, ctx context.Context, users transport.UserClient, name string) uint64 {
	t.Helper()
	u, err := users.CreateUser(ctx, &transport.CreateUserRequest{
		Username:             name,
		TenantID:             tenantid.System,
		WithoutEmailLoginKey: true,
		Pending:              true,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s, pending): %v", name, err)
	}
	if u.Lifecycle != "pending" {
		t.Fatalf("CreateUser(%s) lifecycle = %q, want pending", name, u.Lifecycle)
	}
	return u.ID
}

func assertActivatePendingRace(t *testing.T, ctx context.Context, users transport.UserClient) {
	id := createPendingAccount(t, ctx, users, "activate-pending-race")

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	results := make([]error, activatePendingRacers)
	winners := make([]*transport.User, activatePendingRacers)
	for i := range activatePendingRacers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			winners[i], results[i] = users.ActivatePending(ctx, id)
		}()
	}
	start.Done()
	done.Wait()

	won := -1
	for i, err := range results {
		if err == nil {
			if won >= 0 {
				t.Fatalf("racers %d and %d both activated account %d", won, i, id)
			}
			won = i
			continue
		}
		var notPending *transport.AccountNotPendingError
		if !errors.As(err, &notPending) {
			t.Fatalf("racer %d: err = %T %v, want *transport.AccountNotPendingError", i, err, err)
		}
		if notPending.ID != id || notPending.Lifecycle != "active" || notPending.HasCredential {
			t.Errorf("racer %d: AccountNotPendingError = %#v, want {ID:%d Lifecycle:active}", i, *notPending, id)
		}
	}
	if won < 0 {
		t.Fatalf("no racer activated account %d: %v", id, results)
	}
	if w := winners[won]; w == nil || w.Lifecycle != "active" {
		t.Fatalf("winner %d result = %+v, want active", won, w)
	}
	got, err := users.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	if got.Lifecycle != "active" {
		t.Fatalf("GetUser(%d) lifecycle = %q, want active", id, got.Lifecycle)
	}
}

func assertDisabledNotActivatedPending(t *testing.T, ctx context.Context, users transport.UserClient) {
	id := createPendingAccount(t, ctx, users, "activate-pending-disabled")
	if err := users.DisableUser(ctx, id); err != nil {
		t.Fatalf("DisableUser(%d): %v", id, err)
	}

	u, err := users.ActivatePending(ctx, id)
	var notPending *transport.AccountNotPendingError
	if err == nil || !errors.As(err, &notPending) {
		t.Fatalf("ActivatePending(disabled) = %+v, %v; want *transport.AccountNotPendingError", u, err)
	}
	if notPending.ID != id || notPending.Lifecycle != "disabled" || notPending.HasCredential {
		t.Errorf("AccountNotPendingError = %#v, want {ID:%d Lifecycle:disabled}", notPending, id)
	}

	got, err := users.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	if got.Lifecycle != "disabled" {
		t.Fatalf("GetUser(%d) lifecycle = %q, want disabled", id, got.Lifecycle)
	}
}
