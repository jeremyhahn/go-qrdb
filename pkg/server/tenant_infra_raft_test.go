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
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// raftTenantBootConfig creates an auto-unseal IsolationRaft tenant "rauto"
// with one dedicated-raft database "app", seals the tenant as a shutdown
// would, and returns the BuildTenantInfra config a restarting node would
// pass: the raft path is gated on Host, Manager, the barrier registry, a
// tenant service and a configured volume pool.
func raftTenantBootConfig(t *testing.T) TenantInfraConfig {
	t.Helper()
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "rauto", "Auto", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(rauto): %v", err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "rauto")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(rauto): %v", err)
	}
	mgr := claimingManager(t, reattachTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.CreateForTenant(tenant.WithTenantID(ctx, "rauto"), database.DatabaseConfig{
		Name:    "app",
		Engine:  database.EngineKV,
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
		DataDir: filepath.Join(dataRoot, "databases", "app"),
	}); err != nil {
		t.Fatalf("CreateForTenant(app): %v", err)
	}
	if err := reg.SealTenant("rauto"); err != nil {
		t.Fatalf("SealTenant(rauto): %v", err)
	}

	return TenantInfraConfig{
		Ctx:                  ctx,
		BarrierReg:           reg,
		TenantSvc:            svc,
		TenantLookup:         svc,
		VolumePoolConfigured: true,
		Logger:               reattachTestLogger(),
		Host:                 newRaftReattachHost(t),
		Manager:              mgr,
		ReplicaID:            raftReattachReplicaID,
		RaftAddr:             raftReattachRaftAddr,
	}
}

// TestBuildTenantInfra_RaftTenantShardsServeAtBoot: with no cluster sealer, a
// restarting node re-attaches its auto-unseal raft tenant's database shards
// eagerly, so the data plane serves as soon as the node does; the configured
// import-session bounds reach those shards (a second concurrent session is
// refused when the limit is one); the node reports leading the single-replica
// group; and with no placement table no reconciler is built.
func TestBuildTenantInfra_RaftTenantShardsServeAtBoot(t *testing.T) {
	cfg := raftTenantBootConfig(t)
	cfg.ImportSession = qrdbconfig.ImportSessionConfig{MaxSessionsPerTenant: 1}

	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	t.Cleanup(func() {
		_ = ti.Raft.CloseAll()
		_ = ti.Hosts.CloseAll()
	})
	if ti.Raft == nil {
		t.Fatal("BuildTenantInfra built no raft factory with a Host, Manager and volume pool")
	}
	if ti.Reconciler != nil {
		t.Fatal("a reconciler was built without a placement table to converge on")
	}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "rauto", Subject: "alice"})
	svcs, err := ti.Raft.Services(ctx, "rauto", "app")
	if err != nil {
		t.Fatalf("raft tenant shard not serving after boot: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put on the re-attached shard: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, err)
	}

	opts := service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"}
	if _, err := svcs.Batch.ImportBegin(ctx, opts); err != nil {
		t.Fatalf("first ImportBegin: %v", err)
	}
	if _, err := svcs.Batch.ImportBegin(ctx, opts); !errors.As(err, new(*service.ImportQuotaExceededError)) {
		t.Fatalf("second ImportBegin error = %T (%v), want *service.ImportQuotaExceededError -- the import-session config did not reach the raft shard", err, err)
	}

	// The Put above committed, so this single-replica group has a leader and
	// it is this node; a database it never started is answered false without
	// attaching anything.
	if !ti.Raft.LeadsDatabase("rauto", "app") {
		t.Error("LeadsDatabase(rauto, app) = false for this node's single-replica group")
	}
	if ti.Raft.LeadsDatabase("rauto", "never-created") {
		t.Error("LeadsDatabase reported leading a database this node never started")
	}
	for _, s := range ti.Raft.ListStarted() {
		if s.Database == "never-created" {
			t.Fatal("LeadsDatabase attached a shard for a database it was only asked about")
		}
	}
}

// TestBuildTenantInfra_UnderClusterSealingStartsNoRaftShardBeforeTheRecordIsRead
// is the raft-tenant twin of the volume-tenant ordering test: with a cluster
// sealer wired, no raft tenant shard starts at boot -- the seal monitor that
// would refuse a sealed tenant is installed only after BuildTenantInfra
// returns -- and, with a placement table, the gated reconciler that will start
// them is built (not started) for the caller.
func TestBuildTenantInfra_UnderClusterSealingStartsNoRaftShardBeforeTheRecordIsRead(t *testing.T) {
	cfg := raftTenantBootConfig(t)
	cfg.ClusterSealer = clusterseal.NewDeferred()
	store, err := placement.NewStore(newMemKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	cfg.PlacementStore = store

	ti, err := BuildTenantInfra(cfg)
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	t.Cleanup(func() {
		_ = ti.Raft.CloseAll()
		_ = ti.Hosts.CloseAll()
	})
	if ti.Raft == nil {
		t.Fatal("BuildTenantInfra built no raft factory")
	}
	if started := ti.Raft.ListStarted(); len(started) != 0 {
		t.Fatalf("raft shards started at boot under cluster sealing, before the seal record was read: %+v", started)
	}
	if ti.Reconciler == nil {
		t.Fatal("no reconciler built over the placement table; nothing would start the raft tenant's shards")
	}
}
