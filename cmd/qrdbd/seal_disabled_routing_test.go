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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	userpkg "github.com/jeremyhahn/go-qrdb/pkg/user"
)

// prefixTenantServicer reports every tenant as unknown, which the resolver
// treats as a logical-prefix tenant — the ordinary case for a deployment that
// has not registered tenant metadata.
type prefixTenantServicer struct{ mockTenantServicer }

func (p *prefixTenantServicer) GetTenant(context.Context, string) (*tenantpkg.TenantInfo, error) {
	return nil, tenantpkg.ErrTenantNotFound
}

// THE DAEMON'S seal-disabled path. With sealing off the caller passes a nil
// barrier registry, and the served IAM services used to stay the PLAIN stores
// confined only to "system/" — which select no tenant namespace and do not
// route by-id through OwningTenant. A tenant caller holding another tenant's
// user id reached the shared primary record instead of its own namespace.
//
// This is the same fail-open pkg/server/lifecycle had, in the daemon, and it
// survived the lifecycle fix because that fix was not swept to its sibling.
// Keyspace isolation is not a property of encryption.
func TestBootstrapUserRoleStores_SealDisabled_StillRoutesByTenant(t *testing.T) {
	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	tx := newMockTransactionServicer(store)

	mgmt, err := bootstrapUserRoleStores(
		context.Background(), mock, mock, tx, &mockRaftNode{}, testLeaderWait,
		1, 1,
		&prefixTenantServicer{}, false,
		nil, // NO barrier registry: sealing is disabled
		"", nil,
		auth.MTLSBindingEnrolled, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("bootstrapUserRoleStores: %v", err)
	}

	// The identity is acme's own (the system-tenant lift was removed, owner
	// ruling 2026-10-01), so the by-id reach proven below is acme reaching
	// its OWN record through the identity index, not a system-tenant reach
	// into another tenant.
	ctx := tenantpkg.WithTenantID(context.Background(), "acme")
	acme, err := mgmt.servedUserSvc.CreateUser(ctx, &userpkg.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice", TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser(acme): %v", err)
	}

	// A caller in globex must not reach acme's user BY ID. Before the fix the
	// served service was the plain system-confined store, so this returned
	// acme's record.
	globex := tenantpkg.WithTenantID(ctx, "globex")
	got, gerr := mgmt.servedUserSvc.GetUser(globex, acme.ID)
	if gerr == nil {
		t.Fatalf("globex read acme's user by id (%+v) — the daemon serves "+
			"unrouted IAM when sealing is disabled", got)
	}
	var notFound *userpkg.UserNotFoundError
	if !errors.As(gerr, &notFound) {
		t.Fatalf("cross-tenant by-id read = %v, want UserNotFoundError", gerr)
	}

	// Nor mutate it.
	if derr := mgmt.servedUserSvc.DeleteUser(globex, acme.ID); derr == nil {
		t.Fatal("globex deleted acme's user by id")
	}

	// ...while acme still reaches its own, by id and by username.
	if same, serr := mgmt.servedUserSvc.GetUser(ctx, acme.ID); serr != nil || same.Username != "alice" {
		t.Fatalf("acme cannot read its own user by id: (%+v, %v)", same, serr)
	}
	if same, serr := mgmt.servedUserSvc.GetUserByUsername(ctx, "acme", "alice"); serr != nil || same.ID != acme.ID {
		t.Fatalf("acme cannot read its own user by username: (%+v, %v)", same, serr)
	}
}
