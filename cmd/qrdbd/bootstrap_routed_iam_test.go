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

// This is a mock-based unit test, not an integration test: it shares
// newMockKVStore with the other !integration files in this package, so
// without the tag `go test -tags integration ./cmd/qrdbd/` does not compile.

//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newBarrierIAMFixture builds a TenantService backed by a pooled barrier
// registry whose system barrier is initialized + unsealed, and whose
// entitlements permit self-managed IsolationBarrier tenants. It mirrors
// newReattachFixture but adds the barrier isolation mode so a self-managed
// barrier tenant (whose per-tenant barrier is registered SEALED) can be
// created for the fail-closed wiring assertion below.
func newBarrierIAMFixture(t *testing.T) (*tenant.TenantService, *seal.BarrierRegistry) {
	t.Helper()

	dir := t.TempDir()
	c := local.New()
	if err := c.Configure(map[string]string{"path": dir}); err != nil {
		t.Fatalf("configure local backend: %v", err)
	}
	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })
	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	mgr := objpool.NewManager()
	if err := mgr.Register("default", []common.Storage{c}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	sysStrat, err := seal.NewSoftwareStrategy([]byte("barrier-iam-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(reattachTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeBarrier},
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// TestBootstrapUserRoleStores_ServedIAMFailsClosedForSealedBarrierTenant
// verifies the daemon wiring: bootstrapUserRoleStores exposes StoreResolver-
// routed served services (servedUserSvc/servedRoleSvc) on the API surface —
// distinct from the plain shared-store services used by the internal auth
// plane — so IAM against a SEALED barrier tenant fails closed with
// tenant.ErrTenantSealed instead of leaking through the shared system store,
// while system-tenant IAM still routes to the shared store and works.
func TestBootstrapUserRoleStores_ServedIAMFailsClosedForSealedBarrierTenant(t *testing.T) {
	ctx := context.Background()
	svc, reg := newBarrierIAMFixture(t)

	// A self-managed IsolationBarrier tenant: its per-tenant barrier is
	// registered SEALED and left uninitialized (registerDedicatedBarrier).
	const sealedTenant = "sealed-iam-tenant"
	if _, err := svc.CreateTenant(ctx, sealedTenant, "Sealed IAM", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}

	store := make(map[string][]byte)
	mock := newMockKVStore(store)
	node := &mockRaftNode{} // default: leader ready

	mgmt, err := bootstrapUserRoleStores(ctx, mock, mock, node, 1, 1, svc, false, reg, seal.StrategyPassphrase, nil)
	if err != nil {
		t.Fatalf("bootstrapUserRoleStores: %v", err)
	}

	// The served surface must be the routed decorator, never the plain
	// shared-store service the internal auth plane keeps.
	if mgmt.servedRoleSvc == mgmt.roleSvc {
		t.Fatal("servedRoleSvc must be the routed service, not the plain shared-store roleSvc")
	}
	if mgmt.servedUserSvc == mgmt.userSvc {
		t.Fatal("servedUserSvc must be the routed service, not the plain shared-store userSvc")
	}

	// Direction 1 (fail closed): IAM against the sealed barrier tenant is
	// refused with ErrTenantSealed and never falls back to the shared store.
	if _, err := mgmt.servedRoleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		Name: "r", TenantID: sealedTenant, Permissions: []permission.Permission{permission.KVGet},
	}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("servedRoleSvc.CreateRole(sealed) = %v, want ErrTenantSealed", err)
	}
	if _, err := mgmt.servedUserSvc.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "u", TenantID: sealedTenant,
	}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("servedUserSvc.CreateUser(sealed) = %v, want ErrTenantSealed", err)
	}
	if _, err := mgmt.servedRoleSvc.ListRoles(ctx, &role.ListRolesRequest{
		TenantID: sealedTenant,
	}); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("servedRoleSvc.ListRoles(sealed) = %v, want ErrTenantSealed", err)
	}

	// System-tenant IAM (no explicit tenant) routes to the shared store and
	// remains fully live while the barrier tenant is sealed.
	if _, err := mgmt.servedRoleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		Name: "sys-role", Permissions: []permission.Permission{permission.KVGet},
	}); err != nil {
		t.Fatalf("servedRoleSvc.CreateRole(system) must succeed while a tenant is sealed: %v", err)
	}

	// Direction 2 (succeeds after unseal): initializing the tenant's barrier
	// transitions it to unsealed, and the same IAM op now resolves to the
	// tenant's own barrier-backed store.
	if err := reg.InitializeTenant(ctx, sealedTenant, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", sealedTenant, err)
	}
	if _, err := mgmt.servedRoleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		Name: "r", TenantID: sealedTenant, Permissions: []permission.Permission{permission.KVGet},
	}); err != nil {
		t.Fatalf("servedRoleSvc.CreateRole(unsealed) must succeed: %v", err)
	}
	if _, err := mgmt.servedUserSvc.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "u", TenantID: sealedTenant,
	}); err != nil {
		t.Fatalf("servedUserSvc.CreateUser(unsealed) must succeed: %v", err)
	}
}
