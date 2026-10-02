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
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// droppingCatalog is the real manager catalog with a hook that runs a drop
// of the database it is asked about, at the point resolve consults it: AFTER
// resolve's own drop refusal and BEFORE it builds the database's views. It
// forces the interleaving a concurrent drop can produce.
type droppingCatalog struct {
	*managerCatalog
	drop func()
	once bool
}

func (c *droppingCatalog) DatabaseExists(tenantID, name string) bool {
	exists := c.managerCatalog.DatabaseExists(tenantID, name)
	if !c.once {
		c.once = true
		c.drop()
	}
	return exists
}

// A drop that completes between resolve's refusal check and the moment it
// builds the database's views must not yield an UNGUARDED handle: every
// write through it would land in a keyspace the drop has already reclaimed.
func TestDropFence_DropDuringResolutionNeverYieldsAnUnguardedHandle(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()
	dbReg := NewDatabaseServiceRegistry(tenant.DefaultDatabase)
	dbReg.Register(tenant.DefaultDatabase, f.raw)
	catalog := &droppingCatalog{managerCatalog: newManagerCatalog(f.mgr)}
	catalog.drop = func() {
		if err := f.mgr.Drop(ctx, "orders"); err != nil {
			t.Errorf("Drop: %v", err)
		}
	}
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithDatabaseCatalog(catalog), withDatabaseManager(f.mgr))

	kv, err := reg.KVService(tenantid.System, "orders")
	if err != nil {
		return // refused at resolution: no handle, nothing to write through
	}
	if err := kv.Put(ctx, "k", []byte("orphan")); err == nil {
		t.Fatal("a handle resolved across a completed drop wrote into the dropped database's reclaimed keyspace")
	}
	keys, err := f.raw.KV.ListAll(ctx, tenant.ComposeNamespace(tenantid.System, "orders"))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("the dropped database's keyspace holds %v", keys)
	}
}

// A database dropped and created again under the same name must accept
// collection writes: the collection service a registry caches for the name
// must not outlive the incarnation it was built for.
func TestDropFence_CollectionsWorkAfterDropAndRecreate(t *testing.T) {
	f := newSharedStoreFixture(t)
	ctx := context.Background()

	collections := func() service.CollectionServicer {
		t.Helper()
		c, err := f.reg.CollectionService(tenantid.System, "orders")
		if err != nil {
			t.Fatalf("CollectionService: %v", err)
		}
		return c
	}
	if _, err := collections().CreateCollection(ctx, &service.CreateCollectionRequest{Database: "orders", Name: "users"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := f.mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := f.mgr.Create(database.DatabaseConfig{Name: "orders", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("re-Create: %v", err)
	}

	c := collections()
	if _, err := c.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "orders", Name: "users"}); err != nil {
		t.Fatalf("CreateCollection in the re-created database: %v", err)
	}
	if _, err := c.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "orders", Collection: "users", Document: map[string]any{"n": 1}}); err != nil {
		t.Fatalf("InsertDocument in the re-created database: %v", err)
	}
}

// failingGetKV is a placement.KV whose reads fail, as a store that has lost
// its quorum does.
type failingGetKV struct {
	*memKV
	err error
}

func (k *failingGetKV) Get(context.Context, string) ([]byte, error) {
	return nil, k.err
}

// A placement read that FAILS is not a database that does not exist. On the
// drop-fence path it must be reported, never collapsed into "unknown" --
// which the fence reads as "no store to clear" and turns into a successful
// unfence over a shard that still holds the tombstone.
func TestRaftBackendFactory_FenceServicesReportsAPlacementReadFailure(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "rt")
	boom := errors.New("placement store unreachable")
	store, err := placement.NewStore(&failingGetKV{memKV: newMemKV(), err: boom})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)

	_, err = f.FenceServices(context.Background(), "rt", "orders")
	if err == nil {
		t.Fatal("FenceServices succeeded over an unreadable placement table")
	}
	if errors.Is(err, ErrRaftDatabaseUnknown) {
		t.Fatalf("a placement read failure was reported as an unknown database: %v", err)
	}
}

// A dedicated-raft database that no node has resolved since its last
// restart -- no cached shard -- must still be droppable. The drop sets the
// local fence first, so the fence path must resolve the database without
// the refusal that fence imposes on everyone else.
func TestRaftBackendFactory_ColdDedicatedDatabaseIsDroppable(t *testing.T) {
	ctx := context.Background()
	reg := newVolumePooledRegistryWith(t, seal.NewNoneStrategy())
	tenants, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, volTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:    true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	mgr := claimingManager(t, volTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	host, _ := newSharedTestHost(t)
	raftReg := NewRaftBackendFactoryFromManager(host, sharedHostReplicaID, sharedHostRaftAddr, reg, mgr, volTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	reclaim := service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})
	reclaim.SetStoreResolver(&storeResolver{raft: raftReg, tenantSvc: tenants, systemTenant: tenantid.System})
	mgr.SetKeyspaceReclaimer(reclaim)
	dbSvc := service.NewDatabaseService(mgr, service.WithEntitlementChecker(tenants), service.WithTenantInfoLookup(tenants))

	if _, err := tenants.CreateTenant(ctx, "rt", "rt", tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(false)); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "rt", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	tctx := tenant.WithTenantID(ctx, "rt")
	if _, err := dbSvc.CreateForTenant(tctx, database.DatabaseConfig{Name: "orders", Engine: database.EngineKV}); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(raftReg.ListStarted()) != 0 {
		t.Fatal("precondition: the shard must be cold (never resolved)")
	}

	if err := dbSvc.DropForTenant(tctx, "orders"); err != nil {
		t.Fatalf("DropForTenant of a cold dedicated-raft database: %v", err)
	}
}

// When a shard cached for a dropped incarnation is evicted, its own state
// machine directory is purged -- and the directory of the database's NEW
// shard, which shares the database's data directory, survives.
func TestRaftBackendFactory_EvictionPurgesOnlyTheDroppedShard(t *testing.T) {
	store := newPlacementStore(t)
	reg := newVolumePooledRegistry(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	ctx := context.Background()
	place := func(shardID uint64) {
		t.Helper()
		if err := store.Put(ctx, &placement.Placement{
			TenantID: "rt", Database: "orders", ShardID: shardID, RF: 1, LeaderReplicaID: 1,
			Replicas: []placement.Replica{
				{ReplicaID: 1, NodeID: sharedHostRaftAddr, RaftAddr: sharedHostRaftAddr, DataRoot: dataRoot},
			},
		}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	place(5040)
	f := newPlacementRaftFactory(t, store, sharedHostRaftAddr, reg)
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services(): %v", err)
	}
	oldDir := filepath.Join(dataRoot, "shard-5040")
	newDir := filepath.Join(dataRoot, "shard-5041")
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("precondition: the first shard's directory %s: %v", oldDir, err)
	}

	place(5041)
	if _, err := f.Services(ctx, "rt", "orders"); err != nil {
		t.Fatalf("Services() for the new incarnation: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("the dropped shard's directory survived eviction (stat err %v)", err)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatalf("the new shard's directory did not survive: %v", err)
	}
}
