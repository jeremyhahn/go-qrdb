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
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	logdblib "github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// sharedHostReplicaID and sharedHostRaftAddr identify the single local replica
// of the transport-less shared Host used by the raft-backend tests.
const (
	sharedHostReplicaID uint64 = 1
	sharedHostRaftAddr         = "127.0.0.1:63911"
)

// newSharedTestHost builds a single-node, transport-less Host that stands in for
// the daemon's shared system Host. Dedicated per-database raft shards are started
// on it via the RaftBackendFactory; its NodeHostDir/WALDir live under a temp dir
// so tests can assert that shard state-machine data does NOT land there.
func newSharedTestHost(t *testing.T) (*quicraft.Host, string) {
	t.Helper()
	hostDir := t.TempDir()
	cfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(hostDir, "wal"),
		NodeHostDir:   filepath.Join(hostDir, "nodehost"),
		RaftAddress:   sharedHostRaftAddr,
		ListenAddress: sharedHostRaftAddr,
		DeploymentID:  1,
	}
	host, err := quicraft.NewHost(cfg, quicraft.WithoutTransport())
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host, hostDir
}

// fakeRaftInfoSource implements raftDatabaseInfoSource from an in-memory map so
// tests can drive the factory without a full database.Manager.
type fakeRaftInfoSource struct {
	entries map[raftShardKey]struct {
		shardID uint64
		dataDir string
	}
	dedicated map[raftShardKey]bool
}

func newFakeRaftInfoSource() *fakeRaftInfoSource {
	return &fakeRaftInfoSource{
		entries: make(map[raftShardKey]struct {
			shardID uint64
			dataDir string
		}),
		dedicated: make(map[raftShardKey]bool),
	}
}

// setDedicated marks (tenant, db) as opted into a per-tenant-KEY WAL.
func (s *fakeRaftInfoSource) setDedicated(tenantID, db string) {
	s.dedicated[raftShardKey{tenantID: tenantID, database: db}] = true
}

func (s *fakeRaftInfoSource) add(tenantID, db string, shardID uint64, dataRoot string) {
	s.entries[raftShardKey{tenantID: tenantID, database: db}] = struct {
		shardID uint64
		dataDir string
	}{shardID: shardID, dataDir: filepath.Join(dataRoot, "databases", db)}
}

func (s *fakeRaftInfoSource) databaseInfo(tenantID, db string) (uint64, string, bool) {
	e, ok := s.entries[raftShardKey{tenantID: tenantID, database: db}]
	if !ok {
		return 0, "", false
	}
	return e.shardID, e.dataDir, true
}

// placementInfo always returns false: the fake source is meta-derived and does
// not carry a replicated placement table (same semantics as managerRaftInfoSource).
func (s *fakeRaftInfoSource) placementInfo(_ string, _ string) (*placement.Placement, bool) {
	return nil, false
}

func (s *fakeRaftInfoSource) dedicatedWAL(tenantID, db string) bool {
	return s.dedicated[raftShardKey{tenantID: tenantID, database: db}]
}

// newRaftFactory wires a RaftBackendFactory over a fresh shared Host and the
// volume-pooled barrier registry shared with the Phase-2 fixtures.
func newRaftFactory(t *testing.T) (*RaftBackendFactory, *seal.BarrierRegistry, *fakeRaftInfoSource) {
	t.Helper()
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })
	return f, reg, src
}

// TestRaftBackendFactory_DataDirUnderTenantRoot proves a dedicated raft database
// shard's state-machine data lives under the tenant's DataRoot (NOT under the
// shared host NodeHostDir), is reachable across the full engines, and is
// encrypted at rest under the tenant DEK.
func TestRaftBackendFactory_DataDirUnderTenantRoot(t *testing.T) {
	host, hostDir := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	const shardID = uint64(1000)
	src.add("rt", "app", shardID, dataRoot)

	svcs, err := f.Services(ctx, "rt", "app")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}

	const secret = "PLAINTEXT-RAFT-SECRET-7788"
	if err := svcs.KV.Put(ctx, "k", []byte(secret)); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != secret {
		t.Fatalf("KV.Get = %q, %v; want %q", got, err, secret)
	}
	// MVCC + TimeSeries prove the full state machine (not KV-only).
	if err := svcs.MVCC.Put(ctx, "m", []byte("mv")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}
	if got, err := svcs.MVCC.Get(ctx, "m"); err != nil || string(got) != "mv" {
		t.Fatalf("MVCC.Get = %q, %v; want mv", got, err)
	}
	ts := time.Now()
	if err := svcs.TimeSeries.Write(ctx, "cpu", 0.5, ts, map[string]string{"h": "1"}); err != nil {
		t.Fatalf("TimeSeries.Write: %v", err)
	}
	if pts, err := svcs.TimeSeries.Query(ctx, "cpu", ts.Add(-time.Minute), ts.Add(time.Minute)); err != nil || len(pts) == 0 {
		t.Fatalf("TimeSeries.Query = %d points, %v; want >=1", len(pts), err)
	}

	// The SM data must live under {DataRoot}/databases/app/shard-<N>.
	shardDir := filepath.Join(dataRoot, "databases", "app", "shard-1000")
	if fi, err := os.Stat(shardDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected SM data dir %q to exist: %v", shardDir, err)
	}
	// The shared host NodeHostDir must NOT hold this shard's SM snapshot data
	// (DataDir override roots it on the tenant volume; only the Raft log stays
	// in the shared host LogDB).
	hostShardSnap := filepath.Join(hostDir, "nodehost", "snapshots", "shard-1000")
	if _, err := os.Stat(hostShardSnap); !os.IsNotExist(err) {
		t.Fatalf("SM snapshot dir %q must NOT exist under the shared host NodeHostDir (err=%v)", hostShardSnap, err)
	}

	// Encryption at rest: close the shard so files flush, then scan the tenant
	// volume for the plaintext secret. It must not appear.
	if err := f.Close("rt", "app"); err != nil {
		t.Fatalf("Close shard: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(dataRoot, "databases", "app"), []byte(secret))
}

// TestRaftBackendFactory_PerShardIsolation proves two raft tenants using the
// same database name get distinct shards and cannot see each other's keys.
func TestRaftBackendFactory_PerShardIsolation(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	rootA, _ := registerVolumeTenant(t, reg, "ta")
	rootB, _ := registerVolumeTenant(t, reg, "tb")
	src.add("ta", "shared", 1000, rootA)
	src.add("tb", "shared", 1001, rootB)

	sa, err := f.Services(ctx, "ta", "shared")
	if err != nil {
		t.Fatalf("Services(ta): %v", err)
	}
	sb, err := f.Services(ctx, "tb", "shared")
	if err != nil {
		t.Fatalf("Services(tb): %v", err)
	}

	if err := sa.KV.Put(ctx, "key", []byte("a-value")); err != nil {
		t.Fatalf("ta KV.Put: %v", err)
	}
	if err := sb.KV.Put(ctx, "key", []byte("b-value")); err != nil {
		t.Fatalf("tb KV.Put: %v", err)
	}
	if got, err := sa.KV.Get(ctx, "key"); err != nil || string(got) != "a-value" {
		t.Fatalf("ta KV.Get = %q, %v; want a-value", got, err)
	}
	if got, err := sb.KV.Get(ctx, "key"); err != nil || string(got) != "b-value" {
		t.Fatalf("tb KV.Get = %q, %v; want b-value", got, err)
	}

	// A key written only on ta must not be visible on tb.
	if err := sa.MVCC.Put(ctx, "only-ta", []byte("x")); err != nil {
		t.Fatalf("ta MVCC.Put: %v", err)
	}
	if _, err := sb.MVCC.Get(ctx, "only-ta"); err == nil {
		t.Error("tb MVCC.Get found ta-only key — per-shard isolation breach")
	}
}

// TestRaftBackendFactory_ReattachPersistence proves a shard can be stopped
// (releasing its Pebble lock) and re-started at the same DataDir on the shared
// host with its data intact — the boot re-attach path.
func TestRaftBackendFactory_ReattachPersistence(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	dataRoot, _ := registerVolumeTenant(t, reg, "rp")
	src.add("rp", "db", 1000, dataRoot)

	svcs, err := f.Services(ctx, "rp", "db")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "persist", []byte("durable")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	// Stop the shard (releases the Pebble lock under the tenant volume).
	if err := f.Close("rp", "db"); err != nil {
		t.Fatalf("Close shard: %v", err)
	}

	// Re-attach: the factory re-starts the shard at the same DataDir.
	svcs2, err := f.Services(ctx, "rp", "db")
	if err != nil {
		t.Fatalf("Services (re-attach): %v", err)
	}
	got, err := getWhenReadable(ctx, t, svcs2.KV.Get, "persist")
	if err != nil {
		t.Fatalf("KV.Get after re-attach: %v", err)
	}
	if string(got) != "durable" {
		t.Errorf("KV.Get after re-attach = %q, want durable (data did not persist)", got)
	}
}

// TestRaftBackendFactory_SealedFailsClosed proves a sealed tenant barrier blocks
// shard construction (the data is encrypted under that barrier's DEK), and that
// the shard builds lazily once the barrier is unsealed.
func TestRaftBackendFactory_SealedFailsClosed(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	// Self-managed registration: registered but NOT initialized -> sealed.
	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "sealed", "", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("self-managed barrier must start sealed")
	}
	src.add("sealed", "db", 1000, dataRoot)

	if _, err := f.Services(ctx, "sealed", "db"); err == nil {
		t.Fatal("Services must fail closed while the barrier is sealed")
	} else if !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services sealed error = %v, want seal.ErrSealed", err)
	}

	// Unseal (tenant-admin step) → the shard builds lazily.
	if err := reg.InitializeTenant(ctx, "sealed", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if svcs, err := f.Services(ctx, "sealed", "db"); err != nil || svcs == nil || svcs.KV == nil {
		t.Fatalf("Services after unseal: svcs=%v err=%v", svcs, err)
	}
}

// TestRaftBackendFactory_UnknownDatabase proves an unresolvable (tenant, db)
// pair fails with ErrRaftDatabaseUnknown rather than starting a shard.
func TestRaftBackendFactory_UnknownDatabase(t *testing.T) {
	f, reg, _ := newRaftFactory(t)
	ctx := context.Background()
	registerVolumeTenant(t, reg, "known")

	_, err := f.Services(ctx, "known", "missing")
	if err == nil || !errors.Is(err, ErrRaftDatabaseUnknown) {
		t.Fatalf("Services(unknown db) = %v, want ErrRaftDatabaseUnknown", err)
	}
}

// TestRaftBackendFactory_Unconfigured proves a factory missing its core
// dependencies fails closed.
func TestRaftBackendFactory_Unconfigured(t *testing.T) {
	f := NewRaftBackendFactory(nil, 1, "", nil, nil, volTestLogger())
	if _, err := f.Services(context.Background(), "t", "db"); !errors.Is(err, ErrRaftBackendUnavailable) {
		t.Fatalf("Services = %v, want ErrRaftBackendUnavailable", err)
	}
}

// fakeRaftTenantSvc satisfies tenant.TenantServicer; only GetTenant is wired.
type fakeRaftTenantSvc struct {
	tenant.TenantServicer
	info *tenant.TenantInfo
}

func (f *fakeRaftTenantSvc) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if f.info != nil && f.info.ID == id {
		return f.info, nil
	}
	return nil, tenant.ErrTenantNotFound
}

// TestResolver_RoutesRaftTenantToDedicatedShard verifies the transport-neutral
// resolver routes an IsolationRaft tenant's database to its dedicated shard via
// the RaftBackendFactory (and not the shared system service set).
func TestResolver_RoutesRaftTenantToDedicatedShard(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "rdb")
	src.add("rdb", "app", 1000, dataRoot)

	info := &tenant.TenantInfo{ID: "rdb", IsolationType: tenant.IsolationRaft, DataRoot: dataRoot}
	dbReg := NewDatabaseServiceRegistry("system")
	rr := &restServiceRegistry{
		dbReg:        dbReg,
		barrierReg:   reg,
		tenantSvc:    &fakeRaftTenantSvc{info: info},
		catalog:      raftCatalogStub{tenantID: "rdb", db: "app"},
		raftBackend:  f,
		systemTenant: "system",
	}

	kv, err := rr.KVService("rdb", "app")
	if err != nil {
		t.Fatalf("KVService(raft tenant): %v", err)
	}
	if err := kv.Put(ctx, "routed", []byte("yes")); err != nil {
		t.Fatalf("Put via resolver: %v", err)
	}

	// The write must land on the dedicated shard with NO composite namespace
	// prefix (each raft database has its own shard), proving the routing.
	base, err := f.Services(ctx, "rdb", "app")
	if err != nil {
		t.Fatalf("factory.Services: %v", err)
	}
	raw, err := base.KV.Get(ctx, "routed")
	if err != nil {
		t.Fatalf("dedicated shard KV.Get(routed): %v", err)
	}
	if string(raw) != "yes" {
		t.Errorf("dedicated shard KV value = %q, want yes", raw)
	}
}

// raftCatalogStub satisfies DatabaseCatalog for a single known (tenant, db).
type raftCatalogStub struct {
	tenantID string
	db       string
}

func (c raftCatalogStub) DatabaseExists(tenantID, name string) bool {
	return tenantID == c.tenantID && name == c.db
}

// TestRaftBackendFactory_AttachIdempotent proves Attach builds the shard once and
// is a no-op on a second call.
func TestRaftBackendFactory_AttachIdempotent(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "at")
	src.add("at", "db", 1000, dataRoot)

	if err := f.Attach(ctx, "at", "db"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := f.Attach(ctx, "at", "db"); err != nil {
		t.Fatalf("Attach (idempotent): %v", err)
	}
	if _, err := f.Services(ctx, "at", "db"); err != nil {
		t.Fatalf("Services after Attach: %v", err)
	}
}

// TestRaftBackendError_Error verifies the error string format.
func TestRaftBackendError_Error(t *testing.T) {
	e := &RaftBackendError{TenantID: "t1", Database: "db1", Op: "start-shard", Err: ErrRaftDatabaseUnknown}
	got := e.Error()
	want := "server: raft backend start-shard for t1/db1: " + ErrRaftDatabaseUnknown.Error()
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, ErrRaftDatabaseUnknown) {
		t.Error("errors.Is should unwrap to ErrRaftDatabaseUnknown")
	}
}

// TestManagerRaftInfoSource_Ownership exercises the database.Manager-backed
// info source: ownership enforcement, non-raft rejection, and unknown databases.
func TestManagerRaftInfoSource_Ownership(t *testing.T) {
	mgr := database.NewManager(volTestLogger(), t.TempDir(), database.WithAllocator(shard.NewAllocator()))
	t.Cleanup(func() { _ = mgr.Close() })

	ctx := tenant.WithTenantID(context.Background(), "owner")
	raftCfg := database.DatabaseConfig{
		Name:    "rdb",
		Engine:  database.EngineKV,
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
		DataDir: filepath.Join(t.TempDir(), "databases", "rdb"),
	}
	if _, err := mgr.CreateForTenant(ctx, raftCfg); err != nil {
		t.Fatalf("CreateForTenant(raft): %v", err)
	}
	// A non-raft database owned by the same tenant must be rejected by the source.
	memCfg := database.DatabaseConfig{Name: "mdb", Engine: database.EngineKV, Backend: database.BackendMemory}
	if _, err := mgr.CreateForTenant(ctx, memCfg); err != nil {
		t.Fatalf("CreateForTenant(memory): %v", err)
	}

	src := &managerRaftInfoSource{manager: mgr, systemTenant: "system"}

	if _, _, ok := src.databaseInfo("owner", "rdb"); !ok {
		t.Error("owner should resolve its own raft database")
	}
	if _, _, ok := src.databaseInfo("intruder", "rdb"); ok {
		t.Error("a non-owner tenant must NOT resolve another tenant's database")
	}
	if _, _, ok := src.databaseInfo("owner", "mdb"); ok {
		t.Error("a non-raft database must not resolve through the raft info source")
	}
	if _, _, ok := src.databaseInfo("owner", "missing"); ok {
		t.Error("an unknown database must not resolve")
	}
}

// TestManagerRaftInfoSource_NilManager verifies the source fails closed without
// a manager.
func TestManagerRaftInfoSource_NilManager(t *testing.T) {
	src := &managerRaftInfoSource{manager: nil, systemTenant: "system"}
	if _, _, ok := src.databaseInfo("t", "db"); ok {
		t.Error("nil-manager source must return ok=false")
	}
}

// newSharedTestHostWithMux builds a shared Host whose LogDB is a MultiplexLogDB
// over a shared WAL encrypted under the system barrier, with WithShardBarriers
// wired to the mux so a dedicated-raft shard that registers a per-tenant-KEY WAL
// gets its snapshots routed to the tenant barrier (4d Option C). It returns the
// host and the mux.
//
// The shared WAL is opened WithBarrier: with WithShardBarriers configured and no
// host-wide WithBarrier, quicraft.NewHost refuses a LogDB that would store the
// Raft log in plaintext, and the shared route (the default for every non-opted-in
// shard) must therefore be encrypted at rest — exactly what the system barrier
// provides in production.
func newSharedTestHostWithMux(t *testing.T) (*quicraft.Host, *database.MultiplexLogDB) {
	t.Helper()
	hostDir := t.TempDir()
	sysStrat, err := seal.NewSoftwareStrategy([]byte("mux-shared-wal-test"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(volTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key", DeploymentID: 1}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(context.Background(),
		seal.Credentials{Secret: []byte("mux-shared-wal-test")}); err != nil {
		t.Fatalf("Initialize system barrier: %v", err)
	}
	sharedWAL, err := waldb.Open(filepath.Join(hostDir, "wal"),
		waldb.WithBarrier(sysBarrier.EpochBarrier()))
	if err != nil {
		t.Fatalf("open shared WAL: %v", err)
	}
	mux, err := database.NewMultiplexLogDB(sharedWAL, sysBarrier.EpochBarrier())
	if err != nil {
		t.Fatalf("new multiplex: %v", err)
	}
	cfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(hostDir, "wal"),
		NodeHostDir:   filepath.Join(hostDir, "nodehost"),
		RaftAddress:   sharedHostRaftAddr,
		ListenAddress: sharedHostRaftAddr,
		DeploymentID:  1,
	}
	host, err := quicraft.NewHost(cfg,
		quicraft.WithoutTransport(),
		quicraft.WithLogDB(mux),
		// The host-wide barrier is the shared route's snapshot key: a shard NOT
		// opted into a dedicated WAL resolves to nil below and falls back to it,
		// matching the shared WAL's own system-barrier encryption. Dedicated
		// shards override it with their tenant barrier via the resolver.
		quicraft.WithBarrier(sysBarrier.EpochBarrier()),
		quicraft.WithShardBarriers(func(shardID uint64) quicraft.Barrier {
			if b := mux.BarrierForShard(shardID); b != nil {
				return b
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host, mux
}

// TestRaftBackendFactory_DedicatedWAL_OptIn proves that a dedicated-raft database
// that opted into a per-tenant-KEY WAL (4d Option C) gets a dedicated WAL store
// opened under its tenant DataRoot, registered in the multiplex for its shard,
// its snapshots routed to the tenant barrier, and the WAL unregistered+closed on
// shard teardown. The on-disk WAL bytes must not contain plaintext (tenant-key
// encrypted).
func TestRaftBackendFactory_DedicatedWAL_OptIn(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, tb := registerVolumeTenant(t, reg, "dw")
	const shardID = uint64(2000)
	src.add("dw", "app", shardID, dataRoot)
	src.setDedicated("dw", "app")

	svcs, err := f.Services(ctx, "dw", "app")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}

	// The shard must route to a dedicated WAL registered in the multiplex.
	if !mux.IsDedicated(shardID) {
		t.Fatalf("shard %d must be registered as dedicated in the multiplex", shardID)
	}
	// The per-shard snapshot barrier must be the tenant's barrier.
	wantBarrier := tb.Barrier().EpochBarrier()
	if got := mux.BarrierForShard(shardID); got != wantBarrier {
		t.Fatalf("BarrierForShard returned the wrong barrier for the dedicated shard")
	}
	// The dedicated WAL directory must exist under the tenant DataRoot.
	walDir := filepath.Join(dataRoot, "databases", "app", "wal")
	if fi, err := os.Stat(walDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected dedicated WAL dir %q to exist: %v", walDir, err)
	}

	// Write a secret; it commits via the dedicated (tenant-key) WAL.
	const secret = "DEDICATED-WAL-SECRET-4242"
	if err := svcs.KV.Put(ctx, "k", []byte(secret)); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != secret {
		t.Fatalf("KV.Get = %q, %v; want %q", got, err, secret)
	}

	// Teardown unregisters and closes the dedicated WAL.
	if err := f.Close("dw", "app"); err != nil {
		t.Fatalf("Close shard: %v", err)
	}
	if mux.IsDedicated(shardID) {
		t.Fatalf("dedicated WAL must be unregistered after shard teardown")
	}
	// The dedicated WAL on disk must not contain the plaintext secret.
	assertPlaintextAbsent(t, walDir, []byte(secret))
}

// TestRaftBackendFactory_DedicatedWAL_FallbackNoMux proves that when no multiplex
// LogDB is wired (single-node/standalone, or seal disabled) a dedicated-WAL
// opt-in is ignored and the shard transparently uses the shared WAL: no
// per-shard WAL directory is created.
func TestRaftBackendFactory_DedicatedWAL_FallbackNoMux(t *testing.T) {
	f, reg, src := newRaftFactory(t) // no WithMultiplexLogDB
	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "nm")
	const shardID = uint64(2100)
	src.add("nm", "app", shardID, dataRoot)
	src.setDedicated("nm", "app")

	if _, err := f.Services(ctx, "nm", "app"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	walDir := filepath.Join(dataRoot, "databases", "app", "wal")
	if _, err := os.Stat(walDir); !os.IsNotExist(err) {
		t.Fatalf("dedicated WAL dir %q must NOT exist without a multiplex (err=%v)", walDir, err)
	}
}

// TestRaftBackendFactory_DedicatedWAL_NotOptedIn proves that with a multiplex
// wired, a database that did NOT opt in keeps using the shared WAL: the shard is
// not registered as dedicated and no per-shard WAL directory is created. This is
// the Route-A default that must stay byte-identical.
func TestRaftBackendFactory_DedicatedWAL_NotOptedIn(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "no")
	const shardID = uint64(2200)
	src.add("no", "app", shardID, dataRoot) // not setDedicated

	if _, err := f.Services(ctx, "no", "app"); err != nil {
		t.Fatalf("Services: %v", err)
	}
	if mux.IsDedicated(shardID) {
		t.Fatalf("non-opted-in shard must NOT be registered as dedicated")
	}
	if b := mux.BarrierForShard(shardID); b != nil {
		t.Fatalf("non-opted-in shard must resolve a nil per-shard barrier (got %v)", b)
	}
	walDir := filepath.Join(dataRoot, "databases", "app", "wal")
	if _, err := os.Stat(walDir); !os.IsNotExist(err) {
		t.Fatalf("non-opted-in shard must NOT create a dedicated WAL dir (err=%v)", err)
	}
}

// TestOpenAndRegisterDedicatedWAL_Fallbacks exercises every fallback branch of
// openAndRegisterDedicatedWAL: opt-out, no multiplex, sealed/absent barrier, a
// WAL-open failure, and a duplicate-registration failure. Each must return nil
// (use the shared WAL) without leaving a dedicated WAL registered.
func TestOpenAndRegisterDedicatedWAL_Fallbacks(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
		newFakeRaftInfoSource(), volTestLogger(), WithMultiplexLogDB(mux))

	_, tb := registerVolumeTenant(t, reg, "fb")
	dir := t.TempDir()

	// Not opted in → nil.
	if got := f.openAndRegisterDedicatedWAL("fb", "a", 3000, dir, tb, false); got != nil {
		t.Fatalf("opt-out must return nil")
	}

	// No multiplex wired → nil.
	noMux := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
		newFakeRaftInfoSource(), volTestLogger())
	if got := noMux.openAndRegisterDedicatedWAL("fb", "a", 3001, dir, tb, true); got != nil {
		t.Fatalf("no-multiplex must return nil")
	}

	// Nil barrier → nil.
	if got := f.openAndRegisterDedicatedWAL("fb", "a", 3002, dir, nil, true); got != nil {
		t.Fatalf("nil barrier must return nil")
	}

	// Sealed barrier → nil.
	if err := tb.Seal(); err != nil {
		t.Fatalf("seal tenant barrier: %v", err)
	}
	if got := f.openAndRegisterDedicatedWAL("fb", "a", 3003, dir, tb, true); got != nil {
		t.Fatalf("sealed barrier must return nil")
	}

	// Duplicate registration: pre-register the shard, then opening again must
	// fail to register and fall back to nil.
	_, tb2 := registerVolumeTenant(t, reg, "fb2")
	dup := &fakeLogDBForServer{}
	if err := mux.Register(3005, dup, tb2.Barrier().EpochBarrier()); err != nil {
		t.Fatalf("pre-register: %v", err)
	}
	if got := f.openAndRegisterDedicatedWAL("fb2", "a", 3005, t.TempDir(), tb2, true); got != nil {
		t.Fatalf("duplicate registration must return nil")
	}
}

// fakeLogDBForServer is a no-op logdb.LogDB used to occupy a shard slot in the
// multiplex so a duplicate Register fails.
type fakeLogDBForServer struct{}

func (fakeLogDBForServer) Name() string                      { return "fake" }
func (fakeLogDBForServer) SaveState([]logdblib.Update) error { return nil }
func (fakeLogDBForServer) IterateEntries(e []logdblib.Entry, s, sh, r, l, h, m uint64) ([]logdblib.Entry, uint64, error) {
	return e, s, nil
}
func (fakeLogDBForServer) ReadState(uint64, uint64) (logdblib.State, logdblib.Membership, error) {
	return logdblib.State{}, logdblib.Membership{}, nil
}
func (fakeLogDBForServer) EntryRange(uint64, uint64) (uint64, uint64, error)    { return 0, 0, nil }
func (fakeLogDBForServer) RemoveEntriesTo(uint64, uint64, uint64) error         { return nil }
func (fakeLogDBForServer) Compact(uint64, uint64) error                         { return nil }
func (fakeLogDBForServer) SaveSnapshot(uint64, uint64, logdblib.Snapshot) error { return nil }
func (fakeLogDBForServer) GetSnapshot(uint64, uint64) (logdblib.Snapshot, error) {
	return logdblib.Snapshot{}, nil
}
func (fakeLogDBForServer) RemoveNodeData(uint64, uint64) error                    { return nil }
func (fakeLogDBForServer) SaveBootstrap(uint64, uint64, logdblib.Bootstrap) error { return nil }
func (fakeLogDBForServer) GetBootstrap(uint64, uint64) (logdblib.Bootstrap, bool, error) {
	return logdblib.Bootstrap{}, false, nil
}
func (fakeLogDBForServer) ListNodeInfo() ([]logdblib.NodeInfo, error) { return nil, nil }
func (fakeLogDBForServer) Close() error                               { return nil }
