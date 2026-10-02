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

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// freshRegistryFixture holds the persistent boot resources needed to rebuild a
// BarrierRegistry across a simulated restart: the same system barrier and
// volume pool survive, while a fresh per-tenant Pebble registry is opened so
// the per-tenant barriers must be re-attached from their persisted data roots.
type freshRegistryFixture struct {
	svc    *tenant.TenantService
	system *seal.StorageBarrier
	mgr    objpool.Manager
	state  objpool.StateStore
}

// newFreshRegistryFixture builds the first-boot resources and a registry, plus
// a TenantService bound to it. The returned components let a test close the
// per-tenant registry (releasing on-disk Pebble locks) and rebuild a second
// registry over the same volume — exactly what a daemon restart does.
func newFreshRegistryFixture(t *testing.T, ent *quota.Entitlements) (*freshRegistryFixture, *seal.BarrierRegistry, pebblestore.PerTenantRegistry) {
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

	sysStrat, err := seal.NewSoftwareStrategy([]byte("fresh-system-pass"))
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
	reg, err := seal.NewBarrierRegistry(reattachTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(ent))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	return &freshRegistryFixture{svc: svc, system: system, mgr: mgr, state: state}, reg, perTenantReg
}

// rebuildRegistry simulates a daemon restart: the per-tenant Pebble registry is
// fresh (knows no tenants), while the surviving system barrier and volume pool
// are reused. The returned registry therefore has no per-tenant barriers in
// memory, forcing reattach to re-register each volume tenant from disk.
func (f *freshRegistryFixture) rebuildRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	reg, err := seal.NewBarrierRegistry(reattachTestLogger(), f.system,
		seal.WithVolumePool(f.mgr, f.state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("rebuild NewBarrierRegistry: %v", err)
	}
	return reg
}

// TestReattachVolumeTenants_FreshRegistryReRegisters drives the boot
// re-registration branch: after a simulated restart the rebuilt registry has no
// in-memory per-tenant barrier, so reattachVolumeTenants must re-open it from
// the persisted data root (RegisterVolumeTenant), unseal the auto-unseal
// tenant credential-free, and re-attach its Host.
func TestReattachVolumeTenants_FreshRegistryReRegisters(t *testing.T) {
	fx, reg1, perTenantReg1 := newFreshRegistryFixture(t, &quota.Entitlements{
		AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:        true,
		AllowSystemManagedBarrier: true,
	})
	ctx := context.Background()

	if _, err := fx.svc.CreateTenant(ctx, "vfresh", "Fresh", tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Simulate shutdown: release the first registry's on-disk Pebble handle so
	// the rebuilt registry can re-open the persisted per-tenant barrier.
	_ = reg1
	if err := perTenantReg1.CloseAll(ctx); err != nil {
		t.Fatalf("CloseAll(perTenantReg1): %v", err)
	}

	reg2 := fx.rebuildRegistry(t)

	// The rebuilt registry does not yet know the tenant.
	if _, err := reg2.GetTenant("vfresh"); err == nil {
		t.Fatal("precondition: rebuilt registry must not know the tenant before reattach")
	}

	hostReg := NewTenantHostRegistryFromLookup(reg2, fx.svc, reattachTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	// Mirror the boot sequence: RestoreVolumeTenants re-registers + unseals the
	// barrier from the node-local persisted tenant-root record before the
	// attach-only reattach pass rebuilds the Host.
	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if err := reattachVolumeTenants(ctx, fx.svc, reg2, hostReg, reattachTestLogger()); err != nil {
		t.Fatalf("reattachVolumeTenants: %v", err)
	}

	// The tenant barrier is now re-registered and unsealed, and its Host built.
	tb, err := reg2.GetTenant("vfresh")
	if err != nil {
		t.Fatalf("GetTenant after reattach: %v", err)
	}
	if tb.IsSealed() {
		t.Error("re-registered auto-unseal tenant barrier should be unsealed")
	}
	if _, err := hostReg.Services(ctx, "vfresh"); err != nil {
		t.Errorf("re-attached tenant Host should be resolvable: %v", err)
	}
}

// TestReattachRaftTenants_FreshRegistryReRegisters drives the boot
// re-registration branch for IsolationRaft: after a simulated restart the
// rebuilt registry has no in-memory per-tenant barrier, so reattachRaftTenants
// must re-open it from the persisted data root (RegisterVolumeTenant), unseal
// the auto-unseal tenant credential-free, and re-start each dedicated raft
// database shard on the shared host.
func TestReattachRaftTenants_FreshRegistryReRegisters(t *testing.T) {
	fx, reg1, perTenantReg1 := newFreshRegistryFixture(t, &quota.Entitlements{
		AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
		AllowDedicatedRaft:        true,
		AllowSystemManagedBarrier: true,
	})
	ctx := context.Background()

	if _, err := fx.svc.CreateTenant(ctx, "rfresh", "Fresh Raft", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	rt, err := fx.svc.GetTenant(ctx, "rfresh")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}

	// Populate the database manager with a dedicated-raft database for the
	// tenant, mirroring dbManager.OpenAll at boot.
	mgr := database.NewManager(reattachTestLogger(), t.TempDir(), database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })
	tctx := tenant.WithTenantID(ctx, "rfresh")
	cfg := database.DatabaseConfig{
		Name:    "app",
		Engine:  database.EngineKV,
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
		DataDir: filepath.Join(rt.DataRoot, "databases", "app"),
	}
	if _, err := mgr.CreateForTenant(tctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	// Simulate shutdown: release the first registry's on-disk Pebble handle.
	_ = reg1
	if err := perTenantReg1.CloseAll(ctx); err != nil {
		t.Fatalf("CloseAll(perTenantReg1): %v", err)
	}

	reg2 := fx.rebuildRegistry(t)
	if _, err := reg2.GetTenant("rfresh"); err == nil {
		t.Fatal("precondition: rebuilt registry must not know the tenant before reattach")
	}

	host := newRaftReattachHost(t)
	raftReg := NewRaftBackendFactoryFromManager(
		host, raftReattachReplicaID, raftReattachRaftAddr, reg2, mgr, tenant.SystemTenantID, reattachTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	// Mirror the boot sequence: RestoreVolumeTenants re-registers + unseals the
	// barrier from the node-local persisted tenant-root record before the
	// attach-only reattach pass re-starts the dedicated shard.
	if err := reg2.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if err := reattachRaftTenants(ctx, fx.svc, reg2, raftReg, mgr, reattachTestLogger()); err != nil {
		t.Fatalf("reattachRaftTenants: %v", err)
	}

	tb, err := reg2.GetTenant("rfresh")
	if err != nil {
		t.Fatalf("GetTenant after reattach: %v", err)
	}
	if tb.IsSealed() {
		t.Error("re-registered auto-unseal raft tenant barrier should be unsealed")
	}
	svcs, err := raftReg.Services(ctx, "rfresh", "app")
	if err != nil {
		t.Fatalf("re-attached raft shard should be resolvable: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put on re-attached shard: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get on re-attached shard = %q, %v; want v", got, err)
	}
}
