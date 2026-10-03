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
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestFirstBoot_ConcurrentNodesConverge runs first boot from several
// "nodes" at once against ONE replicated state machine — the situation every
// node of a fresh cluster is in, each racing the others through the same
// steps. Composed the way qrdbd composes it: guarded-batch store over the
// real KV and transaction services, an identity index over the same store,
// and role and user stores with atomic identity.
//
// Every run must succeed (a node that loses a race must find the winner's
// record and accept it, not abort its boot), and the outcome must be exactly
// one system tenant, one copy of each built-in role and one admin user.
func TestFirstBoot_ConcurrentNodesConverge(t *testing.T) {
	ctx := context.Background()
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{Host: host, WatchHub: hub, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	shared, err := lifecycle.NewGuardedKVStoreAdapter(node.KV, nil, node.Transaction)
	if err != nil {
		t.Fatalf("NewGuardedKVStoreAdapter: %v", err)
	}
	system, err := kvstore.NewNamespacedKVStore(shared, tenant.IdentityNamespace(tenantid.System))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := tenant.NewIdentityIndex(system)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := role.NewStore(system, role.WithIdentityIndex(idx), role.WithAtomicIdentity())
	if err != nil {
		t.Fatal(err)
	}

	const nodes = 6
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	errs := make([]error, nodes)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range nodes {
		gen, gerr := dao.NewSnowflakeGenerator(uint64(i + 1))
		if gerr != nil {
			t.Fatal(gerr)
		}
		users, uerr := user.NewStore(system, gen, user.WithIdentityIndex(idx), user.WithAtomicIdentity())
		if uerr != nil {
			t.Fatal(uerr)
		}
		wg.Add(1)
		go func(i int, users user.UserServicer) {
			defer wg.Done()
			<-start
			_, errs[i] = firstboot.Initialize(ctx, firstboot.Dependencies{
				Tenants: node.Tenant,
				Users:   users,
				Roles:   roles,
				Logger:  logger,
				// This test is about convergence under concurrent races, not
				// certificate binding, and wires no CertBinder -- "enrolled"
				// is the correct, explicit choice (go-qrdb#548).
				MTLSBindingPolicy: auth.MTLSBindingEnrolled,
			})
		}(i, users)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("node %d: first boot failed: %v", i+1, err)
		}
	}

	if _, err := node.Tenant.GetTenant(ctx, tenantid.System); err != nil {
		t.Errorf("system tenant missing: %v", err)
	}
	listed, err := roles.ListRoles(ctx, &role.ListRolesRequest{TenantID: tenantid.System, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, r := range listed.Roles {
		names[r.Name]++
	}
	want := append(role.BuiltInRoles(tenantid.System), role.OwnerRole(tenantid.System))
	for _, r := range want {
		if names[r.Name] != 1 {
			t.Errorf("role %q present %d times, want exactly once", r.Name, names[r.Name])
		}
	}
	if len(listed.Roles) != len(want) {
		t.Errorf("%d roles, want %d", len(listed.Roles), len(want))
	}
	users, err := user.NewStore(system, mustSnowflake(t), user.WithIdentityIndex(idx), user.WithAtomicIdentity())
	if err != nil {
		t.Fatal(err)
	}
	all, err := users.ListUsers(ctx, &user.ListUsersRequest{TenantID: tenantid.System, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Users) != 1 || all.Users[0].Username != "admin" {
		t.Errorf("users after concurrent first boot = %d, want exactly the one admin", len(all.Users))
	}
}

func mustSnowflake(t *testing.T) *dao.SnowflakeGenerator {
	t.Helper()
	g, err := dao.NewSnowflakeGenerator(99)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
