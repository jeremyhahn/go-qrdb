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
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// activationPermissions are the verbs the activation suite calls: creating
// and disabling the target, reading it back, and the activation itself, which
// is authorized as users:add_cert_binding.
var activationPermissions = []permission.Permission{
	permission.UsersCreate, permission.UsersRead, permission.UsersDisable,
	permission.UsersAddCertBinding,
}

// activationRacers is how many callers race to activate one pending account.
const activationRacers = 8

// activationThumbprint is a distinct, well-formed SPKI thumbprint (base64url
// of 32 bytes) per i, so racers never collide on the binding itself.
func activationThumbprint(i int) string {
	b := make([]byte, 32)
	b[0] = byte(i + 1)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TestActivateWithCertBinding_Matrix drives UserClient.ActivateWithCertBinding
// against a real node on every harness transport, standalone and raft
// (ruling 1350): of N concurrent activations of one pending account exactly
// one commits and every other caller learns of the lost race as a typed
// *transport.AccountNotPendingError; an account disabled first is refused and
// keeps no binding.
func TestActivateWithCertBinding_Matrix(t *testing.T) {
	for _, c := range coordinationCells() {
		// The objstore identity store cannot hold a user record at all (see
		// coordinationCells), so it has no account to activate.
		if c.backend == database.BackendObjstore {
			continue
		}
		t.Run(c.name(), func(t *testing.T) {
			c.parallel(t)
			client, ctx := c.bootClientGranting(t, activationPermissions)
			users := client.Users()
			t.Run("concurrent-race", func(t *testing.T) { assertActivationRace(t, subtestCtx(t, ctx), users) })
			t.Run("disabled-refused", func(t *testing.T) { assertDisabledNotActivated(t, subtestCtx(t, ctx), users) })
		})
	}
}

func createPendingTarget(t *testing.T, ctx context.Context, users transport.UserClient, name string) uint64 {
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

func assertActivationRace(t *testing.T, ctx context.Context, users transport.UserClient) {
	id := createPendingTarget(t, ctx, users, "activation-race")
	notAfter := time.Now().Add(24 * time.Hour).Unix()

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)
	start.Add(1)
	results := make([]error, activationRacers)
	winners := make([]*transport.User, activationRacers)
	for i := range activationRacers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			winners[i], results[i] = users.ActivateWithCertBinding(ctx, id, activationThumbprint(i), notAfter)
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
		if notPending.ID != id {
			t.Errorf("racer %d: AccountNotPendingError.ID = %d, want %d", i, notPending.ID, id)
		}
		// The winner's commit moved the account to active, so a loser is
		// refused on its lifecycle (HasCredential is only set for an account
		// still pending that already holds a binding).
		if notPending.Lifecycle != "active" || notPending.HasCredential {
			t.Errorf("racer %d: AccountNotPendingError = %#v, want lifecycle active, HasCredential false", i, *notPending)
		}
	}
	if won < 0 {
		t.Fatalf("no racer activated account %d: %v", id, results)
	}

	w := winners[won]
	if w == nil || w.Lifecycle != "active" || len(w.CertBindings) != 1 ||
		w.CertBindings[0].Thumbprint != activationThumbprint(won) {
		t.Fatalf("winner %d result = %+v, want active with its one binding", won, w)
	}
	got, err := users.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	if got.Lifecycle != "active" || len(got.CertBindings) != 1 ||
		got.CertBindings[0].Thumbprint != activationThumbprint(won) {
		t.Fatalf("GetUser(%d) = lifecycle %q bindings %+v, want active with only the winner's binding",
			id, got.Lifecycle, got.CertBindings)
	}
}

func assertDisabledNotActivated(t *testing.T, ctx context.Context, users transport.UserClient) {
	id := createPendingTarget(t, ctx, users, "activation-disabled")
	if err := users.DisableUser(ctx, id); err != nil {
		t.Fatalf("DisableUser(%d): %v", id, err)
	}

	u, err := users.ActivateWithCertBinding(ctx, id, activationThumbprint(0), time.Now().Add(time.Hour).Unix())
	var notPending *transport.AccountNotPendingError
	if err == nil || !errors.As(err, &notPending) {
		t.Fatalf("ActivateWithCertBinding(disabled) = %+v, %v; want *transport.AccountNotPendingError", u, err)
	}
	if notPending.ID != id || notPending.Lifecycle != "disabled" || notPending.HasCredential {
		t.Errorf("AccountNotPendingError = %#v, want {ID:%d Lifecycle:disabled HasCredential:false}", notPending, id)
	}

	got, err := users.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser(%d): %v", id, err)
	}
	if got.Lifecycle != "disabled" || len(got.CertBindings) != 0 {
		t.Fatalf("GetUser(%d) = lifecycle %q bindings %+v, want disabled with no binding",
			id, got.Lifecycle, got.CertBindings)
	}
}
