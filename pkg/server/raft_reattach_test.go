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
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	raftReattachReplicaID uint64 = 1
	raftReattachRaftAddr         = "127.0.0.1:63921"
)

// newRaftReattachFixture builds a TenantService whose default entitlements
// permit raft isolation and the system-managed barrier, backed by a pooled,
// system-managed barrier registry whose system barrier is initialized and
// unsealed.
func newRaftReattachFixture(t *testing.T) (*tenant.TenantService, *seal.BarrierRegistry) {
	t.Helper()

	sysStrat, err := seal.NewSoftwareStrategy(context.Background(), []byte("raft-reattach-system-pass"))
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

	reg, _, allocator := sealtest.PooledRegistry(t, system)

	raftSvc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:        true,
			AllowSystemManagedBarrier: true,
			AllowedVolumePools:        []string{"default"},
		}),
		tenant.WithVolumeAllocator(allocator),
	)
	if err != nil {
		t.Fatalf("NewTenantService(raft): %v", err)
	}
	return raftSvc, reg
}

// newRaftReattachHost builds a single-node, transport-less shared Host for
// the reattach test.
func newRaftReattachHost(t *testing.T) *quicraft.Host {
	t.Helper()
	dir := t.TempDir()
	host, err := quicraft.NewHost(quicraftconfig.HostConfig{
		WALDir:        filepath.Join(dir, "wal"),
		NodeHostDir:   filepath.Join(dir, "nodehost"),
		RaftAddress:   raftReattachRaftAddr,
		ListenAddress: raftReattachRaftAddr,
		DeploymentID:  1,
	}, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

// TestReattachRaftTenants_AutoStartsShard_SelfStaysSealed verifies the boot
// re-attach branch for IsolationRaft: a system-managed raft tenant is unsealed
// credential-free and each of its dedicated database shards is re-started on
// the shared host, while a self-managed raft tenant is left sealed (no shard
// started).
func TestReattachRaftTenants_AutoStartsShard_SelfStaysSealed(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "rauto", "Auto", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(auto raft): %v", err)
	}
	if _, err := svc.CreateTenant(ctx, "rself", "Self", tenant.IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(self raft): %v", err)
	}

	if _, err := svc.GetTenant(ctx, "rauto"); err != nil {
		t.Fatalf("GetTenant(rauto): %v", err)
	}
	rautoDataRoot, err := reg.LocalTenantDataRoot(ctx, "rauto")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(rauto): %v", err)
	}

	// Populate the database manager with a dedicated-raft database for the
	// auto tenant, mirroring dbManager.OpenAll at boot.
	mgr := claimingManager(t, reattachTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	autoCtx := tenant.WithTenantID(ctx, "rauto")
	cfg := database.DatabaseConfig{
		Name:    "app",
		Engine:  database.EngineKV,
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
		DataDir: filepath.Join(rautoDataRoot, "databases", "app"),
	}
	if _, err := mgr.CreateForTenant(autoCtx, cfg); err != nil {
		t.Fatalf("CreateForTenant(raft db): %v", err)
	}

	// Simulate shutdown: seal the auto tenant's barrier.
	if err := reg.SealTenant("rauto"); err != nil {
		t.Fatalf("SealTenant(rauto): %v", err)
	}

	host := newRaftReattachHost(t)
	raftReg := NewRaftBackendFactoryFromManager(
		host, raftReattachReplicaID, raftReattachRaftAddr, reg, mgr, reattachTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	if err := reattachRaftTenants(ctx, svc, reg, raftReg, mgr, reattachTestLogger()); err != nil {
		t.Fatalf("reattachRaftTenants: %v", err)
	}

	// Auto tenant: barrier unsealed and its database shard started (resolvable)
	// with a working data plane.
	autoTB, err := reg.GetTenant("rauto")
	if err != nil {
		t.Fatalf("GetTenant(rauto): %v", err)
	}
	if autoTB.IsSealed() {
		t.Error("auto raft tenant barrier should be unsealed after reattach")
	}
	svcs, err := raftReg.Services(ctx, "rauto", "app")
	if err != nil {
		t.Fatalf("auto raft tenant shard should be started after reattach: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put on re-attached shard: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get on re-attached shard = %q, %v; want v", got, err)
	}

	// Self-managed tenant: barrier still sealed.
	selfTB, err := reg.GetTenant("rself")
	if err != nil {
		t.Fatalf("GetTenant(rself): %v", err)
	}
	if !selfTB.IsSealed() {
		t.Error("self-managed raft tenant barrier must remain sealed after reattach")
	}
}

// TestReattachRaftTenants_AttachError verifies that reattachRaftTenants
// returns *RaftReattachError when raftReg.Attach fails because the
// RaftBackendFactory has a nil host (ErrRaftBackendUnavailable).
func TestReattachRaftTenants_AttachError(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	// Create a system-managed (auto-unseal) raft tenant.
	info, err := svc.CreateTenant(ctx, "rattach-err", "AttachErr",
		tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true))
	if err != nil {
		t.Fatalf("CreateTenant(rattach-err): %v", err)
	}

	// Build a database.Manager with a Raft-backed placeholder database owned
	// by the tenant.
	mgr := claimingManager(t, slog.Default(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	if _, cerr := mgr.Create(database.DatabaseConfig{
		Name:     "raft-db",
		TenantID: info.ID,
		Engine:   database.EngineKV,
		Backend:  database.BackendRaft,
	}); cerr != nil {
		t.Fatalf("manager.Create(raft-db): %v", cerr)
	}

	// Build a RaftBackendFactory with nil host so Attach returns
	// ErrRaftBackendUnavailable without attempting shard start.
	raftReg := NewRaftBackendFactoryFromManager(
		(*quicraft.Host)(nil), 1, "127.0.0.1:0",
		reg, mgr, slog.Default(),
	)

	// Seal the tenant barrier to simulate a daemon restart.
	if err := reg.SealTenant("rattach-err"); err != nil {
		t.Fatalf("SealTenant(rattach-err): %v", err)
	}

	rerr := reattachRaftTenants(ctx, svc, reg, raftReg, mgr, reattachTestLogger())
	if rerr == nil {
		t.Fatal("reattachRaftTenants must return an error when Attach fails")
	}
	var rrErr *RaftReattachError
	if !errors.As(rerr, &rrErr) {
		t.Errorf("expected *RaftReattachError from Attach failure, got %T: %v", rerr, rerr)
	}
}
