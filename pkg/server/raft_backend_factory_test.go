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
	"strconv"
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	logdblib "github.com/jeremyhahn/go-quicraft/pkg/logdb"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
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
	host, err := quicraft.NewHost(cfg, quicraft.WithoutTransport(), quicraft.WithoutEncryption())
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
	// placements, when set for a key, is the placement entry placementInfo
	// reports — a multi-replica placement for the founding tests.
	placements map[raftShardKey]*placement.Placement
	// incarnations, when set for a key, is the incarnation the catalog
	// records for it; a key with no entry is recorded with the empty
	// incarnation of a database created before incarnations existed.
	// dropped keys are recorded as absent, and incarnationErr, when set, is
	// the catalog read failing.
	incarnations   map[raftShardKey]string
	dropped        map[raftShardKey]bool
	incarnationErr error
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

func (s *fakeRaftInfoSource) databaseInfo(tenantID, db string, _ bool) (uint64, string, bool) {
	e, ok := s.entries[raftShardKey{tenantID: tenantID, database: db}]
	if !ok {
		return 0, "", false
	}
	return e.shardID, e.dataDir, true
}

// placementInfo always returns false: the fake source is meta-derived and does
// not carry a replicated placement table (same semantics as managerRaftInfoSource).
func (s *fakeRaftInfoSource) placementInfo(tenantID, db string) (*placement.Placement, bool, error) {
	p, ok := s.placements[raftShardKey{tenantID: tenantID, database: db}]
	return p, ok, nil
}

func (s *fakeRaftInfoSource) dedicatedWAL(tenantID, db string) bool {
	return s.dedicated[raftShardKey{tenantID: tenantID, database: db}]
}

func (s *fakeRaftInfoSource) recordedIncarnation(_ context.Context, tenantID, db string) (string, bool, error) {
	if s.incarnationErr != nil {
		return "", false, s.incarnationErr
	}
	key := raftShardKey{tenantID: tenantID, database: db}
	if s.dropped[key] {
		return "", false, nil
	}
	return s.incarnations[key], true, nil
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
	assertTimeSeriesLeg(ctx, t, svcs.TimeSeries, "", "cpu")

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

// writeAndAwaitLogRange writes n KV entries through svcs and returns the
// shard's log range: it polls for up to wait until the first index rises above
// one, so a shard that does not compact reports its uncompacted range rather
// than failing here.
func writeAndAwaitLogRange(ctx context.Context, t *testing.T, host *quicraft.Host, svcs *DatabaseServices, shardID uint64, n int, wait time.Duration) quicraft.LogRange {
	t.Helper()
	for i := range n {
		if err := svcs.KV.Put(ctx, "snap-"+strconv.Itoa(i), []byte("v")); err != nil {
			t.Fatalf("KV.Put %d: %v", i, err)
		}
	}
	var lr quicraft.LogRange
	deadline := time.Now().Add(wait)
	for {
		var err error
		lr, err = host.LogRange(shardID)
		if err != nil {
			t.Fatalf("LogRange: %v", err)
		}
		if lr.FirstIndex > 1 || time.Now().After(deadline) {
			return lr
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRaftBackendFactory_WithSnapshotSettings_CompactsLog proves a dedicated
// shard takes the snapshot cadence it is given: with a snapshot every 20
// entries and 5 retained, writing well past the cadence compacts the log, so
// its first index rises above one. A replica that falls behind that index can
// only catch up by snapshot install.
func TestRaftBackendFactory_WithSnapshotSettings_CompactsLog(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger(),
		WithSnapshotSettings(20, 5))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "sc")
	const shardID = uint64(1000)
	src.add("sc", "db", shardID, dataRoot)

	svcs, err := f.Services(ctx, "sc", "db")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	lr := writeAndAwaitLogRange(ctx, t, host, svcs, shardID, 100, 10*time.Second)
	if lr.FirstIndex <= 1 {
		t.Fatalf("log range = [%d, %d]: the shard did not compact under a 20-entry snapshot cadence", lr.FirstIndex, lr.LastIndex)
	}
}

// TestRaftBackendFactory_DefaultSnapshotSettings_KeepsLog proves the zero
// value takes go-quicraft's default cadence, as it does for the system shard:
// a hundred entries is far below that default, so nothing is compacted.
func TestRaftBackendFactory_DefaultSnapshotSettings_KeepsLog(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger(),
		WithSnapshotSettings(0, 0))
	t.Cleanup(func() { _ = f.CloseAll() })
	if f.snapshotEntries != 0 || f.compactionOverhead != 0 {
		t.Fatalf("snapshot settings = (%d, %d), want the zero defaults", f.snapshotEntries, f.compactionOverhead)
	}

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "sd")
	const shardID = uint64(1000)
	src.add("sd", "db", shardID, dataRoot)

	svcs, err := f.Services(ctx, "sd", "db")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if lr := writeAndAwaitLogRange(ctx, t, host, svcs, shardID, 100, 0); lr.FirstIndex > 1 {
		t.Fatalf("log range = [%d, %d]: the default cadence compacted a hundred-entry log", lr.FirstIndex, lr.LastIndex)
	}
}

// TestRaftBackendFactory_SealedFailsClosed proves a sealed tenant barrier blocks
// shard construction (the data is encrypted under that barrier's DEK), and that
// the shard builds lazily once the barrier is unsealed.
func TestRaftBackendFactory_SealedFailsClosed(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	// Self-managed registration: registered but NOT initialized -> sealed.
	allocVal, _ := testVolumeAllocators.Load(reg)
	sealtest.ReserveVolumeTenant(t, allocVal.(volume.Allocator), "sealed")
	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "sealed", false)
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

	info := &tenant.TenantInfo{ID: "rdb", IsolationType: tenant.IsolationRaft, VolumePool: "default"}
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

// TestResolver_BarrierTenantRaftDatabaseServedFromSharedStore pins the data
// path behind the create-time DedicatedWALOffDataPathError refusal (#1517
// Codex round 4): a barrier tenant's raft-backed database, which the create
// path admits only without a dedicated WAL, is resolved to the shared system
// store under the tenant/database prefix, never to a dedicated group, so its
// KV writes land there and the raft factory holds no shard for it.
func TestResolver_BarrierTenantRaftDatabaseServedFromSharedStore(t *testing.T) {
	host, _ := newSharedTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })
	registerVolumeTenant(t, reg, "bt")

	shared, sharedSvcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", sharedSvcs)
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier}
	rr := &restServiceRegistry{
		dbReg:        dbReg,
		barrierReg:   reg,
		tenantSvc:    &fakeRaftTenantSvc{info: info},
		catalog:      raftCatalogStub{tenantID: "bt", db: "app"},
		raftBackend:  f,
		systemTenant: "system",
	}

	ctx := context.Background()
	kv, err := rr.KVService("bt", "app")
	if err != nil {
		t.Fatalf("KVService(barrier tenant): %v", err)
	}
	if err := kv.Put(ctx, "routed", []byte("yes")); err != nil {
		t.Fatalf("Put via resolver: %v", err)
	}
	if got := string(shared.store["bt/app/routed"]); got != "yes" {
		t.Fatalf("shared store bt/app/routed = %q, want yes; keys = %v", got, storeKeys(shared))
	}
	if n := len(f.ListOnHost()); n != 0 {
		t.Fatalf("raft factory holds %d shards after a barrier tenant's write, want 0", n)
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

// ---------------------------------------------------------------------------
// ShardRestarter (online backup restore) tests
// ---------------------------------------------------------------------------

// TestRaftBackendFactory_OnlineBackupRestore_RF1Success is the real,
// end-to-end proof for the ONLINE (running-shard) backup restore path,
// through the actual go-quicraft snapshot/import/StartShard/StopShard
// plumbing (no mocks): a disaster-recovery scenario where a single-replica
// dedicated shard's node was lost and re-provisioned. The REPLACEMENT node
// runs the same (tenant, db) shard at the same shard ID, already STARTED
// (satisfying the online-restore precondition) but with no data and no
// recorded snapshot of its own; online restore populates it from a backup
// captured on the ORIGINAL node.
//
// # Why this is the scenario that proves genuine data restoration
//
// *quicraft.Host.ImportSnapshot refuses an artifact that does not ADVANCE
// the target replica's own recorded snapshot (checkImportAdvancesSnapshot),
// and a successful import only compacts LOCAL log entries AT OR BELOW the
// imported index (see ImportSnapshot's log-compaction comment in
// go-quicraft/pkg/host.go) — it never discards entries ABOVE it. For an
// RF=1 shard that never went anywhere, there is no "behind" state: the sole
// replica's own log already contains everything up to and including
// whatever CreateBackup just snapshotted, so re-importing that SAME backup
// back onto the SAME replica can only ever be a no-op (see the companion
// test below, which proves and documents exactly that safe behavior). A
// REPLACEMENT replica — same shard ID, same RF=1 eligibility, but with no
// snapshot of its own yet — is the only case where the advance-check can
// pass and genuinely install different data, which is exactly the
// production disaster-recovery scenario this feature targets: a node's
// data is gone, so an operator online-restores the affected tenant's shard
// from backup without stopping the rest of the cluster.
func TestRaftBackendFactory_OnlineBackupRestore_RF1Success(t *testing.T) {
	ctx := context.Background()

	// Original node/replica: where the data was written and the backup was
	// captured.
	fSrc, regSrc, srcInfo := newRaftFactory(t)
	dataRootSrc, _ := registerVolumeTenant(t, regSrc, "or")
	const shardID = uint64(1000)
	srcInfo.add("or", "db", shardID, dataRootSrc)

	svcsSrc, err := fSrc.Services(ctx, "or", "db")
	if err != nil {
		t.Fatalf("Services (source): %v", err)
	}
	if err := svcsSrc.KV.Put(ctx, "k", []byte("restored-from-backup")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	providerSrc := service.NewHostShardSnapshotProvider(raftbackend.New(fSrc.host))
	data, err := providerSrc.SnapshotShard(ctx, shardID)
	if err != nil {
		t.Fatalf("SnapshotShard (source): %v", err)
	}

	// Replacement node/replica: a SEPARATE host (simulating re-provisioned
	// hardware) hosting the SAME (tenant, db) shard at the SAME shard ID,
	// already running but with no data of its own yet. Wired exactly as
	// production wires it: the provider is built first (mirroring
	// lifecycle.BuildServices, which runs before the IsolationRaft factory
	// exists), and the factory is wired in as the ShardRestarter afterward
	// via service.SetShardRestarter (mirroring cmd/qrdbd/start.go /
	// sdk/go/node/node.go).
	fDst, regDst, dstInfo := newRaftFactory(t)
	dataRootDst, _ := registerVolumeTenant(t, regDst, "or")
	dstInfo.add("or", "db", shardID, dataRootDst)
	if _, err := fDst.Services(ctx, "or", "db"); err != nil {
		t.Fatalf("Services (replacement): %v", err)
	}

	providerDst := service.NewHostShardSnapshotProvider(raftbackend.New(fDst.host))
	backupSvc, err := service.NewBackupService(providerDst)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	service.SetShardRestarter(backupSvc, fDst)

	online, ok := providerDst.(service.OnlineShardRestorer)
	if !ok {
		t.Fatal("expected host-backed provider to implement OnlineShardRestorer")
	}
	// RestoreShardOnline's RF guard requires a linearizable membership read
	// (ReadIndex), which can transiently fail with "read index not ready"
	// immediately after the replacement replica's leader election — the same
	// warm-up window getWhenReadable tolerates for KV reads elsewhere in this
	// file. Poll rather than assume the very first attempt lands after
	// waitForShardServingWithin merely observes a leader ID.
	restoreDeadline := time.Now().Add(15 * time.Second)
	for {
		err = online.RestoreShardOnline(ctx, shardID, data)
		if err == nil || !strings.Contains(err.Error(), "read index not ready") || time.Now().After(restoreDeadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("RestoreShardOnline: %v", err)
	}

	// The shard must be usable again post-restart. RestartAfterRestore's
	// Attach re-caches a fresh service set under the same (tenant, db) key,
	// so re-resolving through the factory (not a stale pre-restart handle)
	// is required — exactly the ReattachPersistence pattern above.
	svcsDstAfter, err := fDst.Services(ctx, "or", "db")
	if err != nil {
		t.Fatalf("Services after restore: %v", err)
	}
	got, err := getWhenReadable(ctx, t, svcsDstAfter.KV.Get, "k")
	if err != nil {
		t.Fatalf("KV.Get after restore: %v", err)
	}
	if string(got) != "restored-from-backup" {
		t.Fatalf("KV.Get after restore = %q, want restored-from-backup (online restore did not install the backed-up data)", got)
	}

	// The restored shard must still accept new writes (fully functional, not
	// wedged in some half-restarted state).
	if err := svcsDstAfter.KV.Put(ctx, "k2", []byte("post-restore-write")); err != nil {
		t.Fatalf("KV.Put after restore: %v", err)
	}
	if got, err := svcsDstAfter.KV.Get(ctx, "k2"); err != nil || string(got) != "post-restore-write" {
		t.Fatalf("KV.Get(k2) after restore = %q, %v; want post-restore-write", got, err)
	}
}

// TestRaftBackendFactory_OnlineBackupRestore_RF1_PostBackupWriteSurvivesIdempotentPath
// documents and proves a safety property discovered while writing the
// success test above: for an RF=1 shard's OWN prior backup — as opposed to
// the disaster-recovery "replacement replica" scenario above — a write
// committed AFTER the backup snapshot was taken, but before Raft's own
// auto-snapshot threshold produces a NEWER recorded snapshot, is NOT rolled
// back by re-importing that same backup. *quicraft.Host.ImportSnapshot only
// refuses OR advances based on the RECORDED SNAPSHOT index
// (checkImportAdvancesSnapshot); it never inspects or truncates the raft
// LOG. Since nothing here forced a newer snapshot, the backup's index
// exactly equals the shard's already-recorded snapshot index, so the import
// is refused with the idempotent logdb.ErrSnapshotOutOfDate ("already
// applied") — service.RestoreShardOnline treats that as success and simply
// restarts, and normal Raft recovery then replays the still-present
// post-backup log entry, landing back on the CURRENT (post-write) value.
//
// This is the correct, SAFE behavior: it is exactly what stops online
// restore from silently discarding committed data it has no way to know a
// caller did not intend to keep (see the package doc on
// RestoreShardOnline's idempotent handling). A genuine "roll back this
// shard's own live history to an older point" is deliberately NOT
// supported by this mechanism — that would require discarding committed
// Raft log entries, which neither this feature nor *quicraft.Host does.
func TestRaftBackendFactory_OnlineBackupRestore_RF1_PostBackupWriteSurvivesIdempotentPath(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	dataRoot, _ := registerVolumeTenant(t, reg, "op")
	const shardID = uint64(1000)
	src.add("op", "db", shardID, dataRoot)

	svcs, err := f.Services(ctx, "op", "db")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("before-backup")); err != nil {
		t.Fatalf("KV.Put(before-backup): %v", err)
	}

	hostAdapter := raftbackend.New(f.host)
	provider := service.NewHostShardSnapshotProvider(hostAdapter)
	backupSvc, err := service.NewBackupService(provider)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	service.SetShardRestarter(backupSvc, f)

	backup, err := backupSvc.CreateBackup(ctx, service.BackupOptions{
		Destination: "test-online-restore-idempotent",
		ShardIDs:    []uint64{shardID},
		Encryption:  service.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// A write after the snapshot, below Raft's own auto-snapshot threshold:
	// the recorded snapshot stays at the backup's own index, but the log
	// entry for this write is still present and gets replayed on any
	// restart, restore or not.
	if err := svcs.KV.Put(ctx, "k", []byte("after-backup")); err != nil {
		t.Fatalf("KV.Put(after-backup): %v", err)
	}

	result, err := backupSvc.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:       backup.ID,
		TargetShards:   []uint64{shardID},
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("RestoreBackup reported shard errors: %+v", result.Errors)
	}
	if result.ShardsRestored != 1 {
		t.Fatalf("expected 1 shard restored (idempotent no-op counts as restored), got %d", result.ShardsRestored)
	}

	svcsAfter, err := f.Services(ctx, "op", "db")
	if err != nil {
		t.Fatalf("Services after restore: %v", err)
	}
	got, err := getWhenReadable(ctx, t, svcsAfter.KV.Get, "k")
	if err != nil {
		t.Fatalf("KV.Get after restore: %v", err)
	}
	if string(got) != "after-backup" {
		t.Fatalf("KV.Get after restore = %q, want after-backup (the post-backup write must survive the idempotent no-op restore)", got)
	}
}

// TestRaftBackendFactory_OnlineBackupRestore_SystemShardRefused proves the
// system-shard guard is enforced even when a real ShardRestarter is wired
// in: shard.ShardKV is refused unconditionally, and — because the guard
// fires before StopForRestore is ever called — a dedicated shard this
// factory legitimately manages is provably untouched by the attempt.
func TestRaftBackendFactory_OnlineBackupRestore_SystemShardRefused(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	dataRoot, _ := registerVolumeTenant(t, reg, "sr")
	const shardID = uint64(1000)
	src.add("sr", "db", shardID, dataRoot)
	if _, err := f.Services(ctx, "sr", "db"); err != nil {
		t.Fatalf("Services: %v", err)
	}

	hostAdapter := raftbackend.New(f.host)
	provider := service.NewHostShardSnapshotProvider(hostAdapter, service.WithShardRestarter(f))

	online, ok := provider.(service.OnlineShardRestorer)
	if !ok {
		t.Fatal("expected host-backed provider to implement OnlineShardRestorer")
	}
	err := online.RestoreShardOnline(ctx, shard.ShardKV, []byte("data"))
	var sysErr *service.OnlineRestoreSystemShardError
	if !errors.As(err, &sysErr) {
		t.Fatalf("expected OnlineRestoreSystemShardError, got %T: %v", err, err)
	}

	// The dedicated shard this factory legitimately manages must be
	// unaffected: StopForRestore was never reached for the system shard, and
	// the dedicated shard's own cache entry (and lease-sweeper goroutine) is
	// still intact.
	f.mu.Lock()
	_, stillCached := f.shards[raftShardKey{tenantID: "sr", database: "db"}]
	f.mu.Unlock()
	if !stillCached {
		t.Fatal("dedicated shard was evicted from the factory's cache by a refused system-shard restore")
	}
}

// TestRaftBackendFactory_StopForRestore_UnmanagedShard proves StopForRestore
// fails closed with ShardNotManagedError for a shard ID this factory has
// never started (e.g. the system shard, or a typo'd ID) rather than
// attempting a Host.StopShard call the factory cannot safely follow up with
// a restart for.
func TestRaftBackendFactory_StopForRestore_UnmanagedShard(t *testing.T) {
	f, _, _ := newRaftFactory(t)

	err := f.StopForRestore(shard.ShardKV)
	var notManaged *ShardNotManagedError
	if !errors.As(err, &notManaged) {
		t.Fatalf("expected ShardNotManagedError, got %T: %v", err, err)
	}
	if notManaged.ShardID != shard.ShardKV {
		t.Fatalf("expected ShardID=%d, got %d", shard.ShardKV, notManaged.ShardID)
	}
}

// TestRaftBackendFactory_RestartAfterRestore_NoPendingStop proves
// RestartAfterRestore refuses a shard ID with no matching StopForRestore
// call rather than guessing at an (tenantID, database) identity to restart.
func TestRaftBackendFactory_RestartAfterRestore_NoPendingStop(t *testing.T) {
	f, _, _ := newRaftFactory(t)

	err := f.RestartAfterRestore(context.Background(), 1000)
	var notManaged *ShardNotManagedError
	if !errors.As(err, &notManaged) {
		t.Fatalf("expected ShardNotManagedError, got %T: %v", err, err)
	}
}

// TestRaftBackendFactory_StopForRestore_RestoresCacheOnStopFailure proves
// that if the underlying Host.StopShard call itself fails, StopForRestore
// restores the shard's cache entry rather than leaving the factory believing
// a still-running shard is stopped.
func TestRaftBackendFactory_StopForRestore_RestoresCacheOnStopFailure(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	ctx := context.Background()

	dataRoot, _ := registerVolumeTenant(t, reg, "sf")
	const shardID = uint64(1000)
	src.add("sf", "db", shardID, dataRoot)
	if _, err := f.Services(ctx, "sf", "db"); err != nil {
		t.Fatalf("Services: %v", err)
	}

	// Close the underlying host so the subsequent Host.StopShard call fails
	// with quicraft.ErrClosed, without tearing down the factory's own state.
	if err := f.host.Close(); err != nil {
		t.Fatalf("host.Close: %v", err)
	}

	err := f.StopForRestore(shardID)
	if err == nil {
		t.Fatal("expected StopForRestore to fail once the host is closed")
	}

	f.mu.Lock()
	_, stillCached := f.shards[raftShardKey{tenantID: "sf", database: "db"}]
	_, pending := f.pendingRestore[shardID]
	f.mu.Unlock()
	if !stillCached {
		t.Fatal("shard cache entry was not restored after a failed StopForRestore")
	}
	if pending {
		t.Fatal("pendingRestore must not record a shard whose stop failed")
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
	mgr := claimingManager(t, volTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	ctx := tenant.WithTenantID(context.Background(), "owner")
	raftCfg := database.DatabaseConfig{
		Name:    "rdb",
		Backend: database.BackendRaft,
		Raft:    &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal},
		DataDir: filepath.Join(t.TempDir(), "databases", "rdb"),
	}
	if _, err := mgr.CreateForTenant(ctx, raftCfg); err != nil {
		t.Fatalf("CreateForTenant(raft): %v", err)
	}
	// A non-raft database owned by the same tenant must be rejected by the source.
	memCfg := database.DatabaseConfig{Name: "mdb", Backend: database.BackendMemory}
	if _, err := mgr.CreateForTenant(ctx, memCfg); err != nil {
		t.Fatalf("CreateForTenant(memory): %v", err)
	}

	src := &managerRaftInfoSource{manager: mgr}

	if _, _, ok := src.databaseInfo("owner", "rdb", false); !ok {
		t.Error("owner should resolve its own raft database")
	}
	if _, _, ok := src.databaseInfo("intruder", "rdb", false); ok {
		t.Error("a non-owner tenant must NOT resolve another tenant's database")
	}
	if _, _, ok := src.databaseInfo("owner", "mdb", false); ok {
		t.Error("a non-raft database must not resolve through the raft info source")
	}
	if _, _, ok := src.databaseInfo("owner", "missing", false); ok {
		t.Error("an unknown database must not resolve")
	}
}

// TestManagerRaftInfoSource_NilManager verifies the source fails closed without
// a manager.
func TestManagerRaftInfoSource_NilManager(t *testing.T) {
	src := &managerRaftInfoSource{manager: nil}
	if _, _, ok := src.databaseInfo("t", "db", false); ok {
		t.Error("nil-manager source must return ok=false")
	}
}

// TestManagerRaftInfoSource_RecordedIncarnation checks the source answers
// with the catalog's incarnation for the name, and with no record once the
// drop removed it.
func TestManagerRaftInfoSource_RecordedIncarnation(t *testing.T) {
	mgr := claimingManager(t, volTestLogger(), t.TempDir())
	ctx := tenant.WithTenantID(context.Background(), "owner")
	db, err := mgr.CreateForTenant(ctx, database.DatabaseConfig{Name: "mdb", Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	src := &managerRaftInfoSource{manager: mgr}
	if got, ok, err := src.recordedIncarnation(ctx, "owner", "mdb"); err != nil || !ok || got != db.Incarnation() {
		t.Fatalf("recordedIncarnation() = (%q, %v, %v), want (%q, true, nil)", got, ok, err, db.Incarnation())
	}
	if err := mgr.DropForTenant(ctx, "mdb"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, ok, err := src.recordedIncarnation(ctx, "owner", "mdb"); err != nil || ok {
		t.Fatalf("recordedIncarnation() after the drop = (ok %v, %v), want no record", ok, err)
	}
}

// TestManagerRaftInfoSource_RecordedIncarnationNilManager checks the source
// without a manager reports an error, never "no record": FenceServices
// reads no record as the incarnation being superseded.
func TestManagerRaftInfoSource_RecordedIncarnationNilManager(t *testing.T) {
	src := &managerRaftInfoSource{}
	if _, ok, err := src.recordedIncarnation(context.Background(), "t", "db"); !errors.Is(err, ErrRaftBackendUnavailable) || ok {
		t.Fatalf("recordedIncarnation() with no manager = (ok %v, %v), want ErrRaftBackendUnavailable", ok, err)
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
	return newSharedTestHostAt(t, t.TempDir(), newTestSystemBarrier(t))
}

// newTestSystemBarrier returns an initialized, unsealed system barrier.
func newTestSystemBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	sysStrat, err := seal.NewSoftwareStrategy(context.Background(), []byte("mux-shared-wal-test"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(volTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key", DeploymentID: 1}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(context.Background(),
		seal.MustCredentials(context.Background(), []byte("mux-shared-wal-test"))); err != nil {
		t.Fatalf("Initialize system barrier: %v", err)
	}
	return sysBarrier
}

// newPlaintextSystemBarrier returns an initialized, unsealed system barrier
// under StrategyNone: the plaintext-mode barrier a node runs before
// EnableEncryption.
func newPlaintextSystemBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	sysBarrier, err := seal.NewStorageBarrier(volTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize system barrier: %v", err)
	}
	if !sysBarrier.EpochBarrier().IsPlaintextMode() {
		t.Fatal("a StrategyNone barrier does not report plaintext mode; the fixture is wrong")
	}
	return sysBarrier
}

// newSharedTestHostAt is newSharedTestHostWithMux over a caller-owned host
// directory and system barrier, so a test can reopen the shared WAL after the
// host closes.
func newSharedTestHostAt(t *testing.T, hostDir string, sysBarrier *seal.StorageBarrier) (*quicraft.Host, *database.MultiplexLogDB) {
	t.Helper()
	cfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(hostDir, "wal"),
		NodeHostDir:   filepath.Join(hostDir, "nodehost"),
		RaftAddress:   sharedHostRaftAddr,
		ListenAddress: sharedHostRaftAddr,
		DeploymentID:  1,
	}
	// The WAL is decided by database.NewRaftWAL, the path qrdbd and the SDK
	// node share, on a host with no transport: a single node gets the
	// multiplex exactly as a cluster does.
	wal, err := database.NewRaftWAL(database.RaftWALConfig{
		Dir:           cfg.WALDir,
		SystemBarrier: sysBarrier.EpochBarrier(),
	})
	if err != nil {
		t.Fatalf("NewRaftWAL: %v", err)
	}
	mux := wal.Multiplex
	if mux == nil {
		_ = wal.Close()
		t.Fatalf("a single node with a system barrier must get the multiplex")
	}
	host, err := quicraft.NewHost(cfg, append([]quicraft.HostOption{quicraft.WithoutTransport()}, wal.Options...)...)
	if err != nil {
		_ = wal.Close()
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

// TestRaftBackendFactory_DedicatedWAL_PlaintextModeHost proves a host whose
// system barrier is still in plaintext mode (StrategyNone, before
// EnableEncryption) runs the multiplex, so an isolated tenant's dedicated-WAL
// shard starts and serves on it instead of being refused with
// ErrDedicatedWALNotWired. The host and its tenants are all StrategyNone, the
// posture of a node that has not enabled encryption yet.
func TestRaftBackendFactory_DedicatedWAL_PlaintextModeHost(t *testing.T) {
	host, mux := newSharedTestHostAt(t, t.TempDir(), newPlaintextSystemBarrier(t))
	reg := newVolumePooledRegistryWith(t, seal.NewNoneStrategy())
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, tb := registerVolumeTenant(t, reg, "pt")
	const shardID = uint64(2100)
	src.add("pt", "app", shardID, dataRoot)
	src.setDedicated("pt", "app")

	svcs, err := f.Services(ctx, "pt", "app")
	if err != nil {
		t.Fatalf("Services on a plaintext-mode host: %v", err)
	}
	if !mux.IsDedicated(shardID) {
		t.Fatalf("shard %d must be registered as dedicated in the multiplex", shardID)
	}
	if got := mux.BarrierForShard(shardID); got != tb.Barrier().EpochBarrier() {
		t.Fatalf("BarrierForShard returned the wrong barrier for the dedicated shard")
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want %q", got, err, "v")
	}
}

// Sealing a tenant stops its shards and unregisters their dedicated WALs
// BEFORE the key is wiped, and leaves every other tenant's shard committing: a
// sealed WAL in a commit batch would fail the batch's other shards for good.
func TestRaftBackendFactory_SealTenant_StopsItsShardsOnly(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	rootA, tbA := registerVolumeTenant(t, reg, "sa")
	rootB, _ := registerVolumeTenant(t, reg, "sb")
	const shardA1, shardA2, shardB = uint64(2100), uint64(2101), uint64(2102)
	for _, d := range []struct {
		tenant, db string
		shard      uint64
		root       string
	}{{"sa", "one", shardA1, rootA}, {"sa", "two", shardA2, rootA}, {"sb", "app", shardB, rootB}} {
		src.add(d.tenant, d.db, d.shard, d.root)
		src.setDedicated(d.tenant, d.db)
		if _, err := f.Services(ctx, d.tenant, d.db); err != nil {
			t.Fatalf("Services(%s/%s): %v", d.tenant, d.db, err)
		}
	}
	if n := len(f.sealWatched); n != 2 {
		t.Fatalf("%d barriers watched; want one per tenant (2)", n)
	}

	if err := tbA.Seal(); err != nil {
		t.Fatalf("Seal tenant sa: %v", err)
	}

	for _, id := range []uint64{shardA1, shardA2} {
		if mux.IsDedicated(id) {
			t.Fatalf("shard %d of the sealed tenant is still registered in the multiplex", id)
		}
	}
	for _, s := range f.ListStarted() {
		if s.TenantID == "sa" {
			t.Fatalf("sealed tenant's shard %s/%s is still started", s.TenantID, s.Database)
		}
	}
	if !mux.IsDedicated(shardB) {
		t.Fatalf("sealing tenant sa unregistered tenant sb's WAL")
	}
	svcs, err := f.Services(ctx, "sb", "app")
	if err != nil {
		t.Fatalf("Services(sb/app) after sa sealed: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("still-committing")); err != nil {
		t.Fatalf("tenant sb stopped committing when sa sealed: %v", err)
	}
	if _, err := f.Services(ctx, "sa", "one"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services on a sealed tenant = %v; want seal.ErrSealed", err)
	}
}

// A tenant with no shard started on this node seals without touching the
// factory, and sealing twice stops nothing twice.
func TestRaftBackendFactory_SealTenant_Idempotent(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	_, tb := registerVolumeTenant(t, reg, "si")
	if err := f.stopTenantShards("si", nil); err != nil {
		t.Fatalf("stopTenantShards with nothing started: %v", err)
	}
	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := tb.Seal(); err != nil {
		t.Fatalf("second Seal: %v", err)
	}
}

// A tenant's shard stopped by its seal starts again when the tenant is
// unsealed, with the dedicated WAL registered again and the data written before
// the seal intact: nothing failed by the seal stays latched on the host.
func TestRaftBackendFactory_SealTenant_ShardRestartsAfterUnseal(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	root, tb := registerVolumeTenant(t, reg, "ru")
	const shardID = uint64(2300)
	src.add("ru", "app", shardID, root)
	src.setDedicated("ru", "app")
	svcs, err := f.Services(ctx, "ru", "app")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("before-seal")); err != nil {
		t.Fatalf("Put before seal: %v", err)
	}

	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := f.Services(ctx, "ru", "app"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services while sealed = %v; want seal.ErrSealed", err)
	}
	if err := reg.UnsealTenant(ctx, "ru", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealTenant: %v", err)
	}

	svcs, err = f.Services(ctx, "ru", "app")
	if err != nil {
		t.Fatalf("Services after unseal: %v", err)
	}
	if !mux.IsDedicated(shardID) {
		t.Fatalf("the dedicated WAL was not registered again after unseal")
	}
	if err := svcs.KV.Put(ctx, "k2", []byte("after-unseal")); err != nil {
		t.Fatalf("Put after unseal: %v", err)
	}
	got, err := svcs.KV.Get(ctx, "k")
	if err != nil || string(got) != "before-seal" {
		t.Fatalf("Get after unseal = %q, %v; want the value written before the seal", got, err)
	}
}

// A tenant's home group is placed DedicatedWAL, so its Raft log is written to a
// WAL under the tenant's key and the host's shared WAL never holds an entry of
// it. A database that did not opt in, on the same host, does land in the shared
// WAL: that is what makes the empty answer for the home group meaningful.
func TestRaftBackendFactory_HomeGroup_SharedWALHoldsNoEntry(t *testing.T) {
	hostDir := t.TempDir()
	sysBarrier := newTestSystemBarrier(t)
	host, mux := newSharedTestHostAt(t, hostDir, sysBarrier)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))

	ctx := context.Background()
	root, _ := registerVolumeTenant(t, reg, "hg")
	const homeShard, sharedShard = uint64(2400), uint64(2401)
	src.add("hg", database.TenantHomeName, homeShard, root)
	src.setDedicated("hg", database.TenantHomeName)
	src.add("hg", "plain", sharedShard, root)
	for _, name := range []string{database.TenantHomeName, "plain"} {
		svcs, err := f.Services(ctx, "hg", name)
		if err != nil {
			t.Fatalf("Services(%s): %v", name, err)
		}
		if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", name, err)
		}
	}
	if !mux.IsDedicated(homeShard) {
		t.Fatalf("the home group's WAL is not registered as dedicated")
	}
	held := sharedWALShards(t, f, host, sysBarrier, hostDir)
	if held[homeShard] {
		t.Fatalf("the shared host WAL holds an entry of the tenant's home group (shard %d)", homeShard)
	}
	if !held[sharedShard] {
		t.Fatalf("the shared WAL holds no entry of the non-dedicated database (shard %d): the check proves nothing", sharedShard)
	}
}

// TestRaftBackendFactory_StrictTenantDatabase_SharedWALHoldsNoEntry is the
// LogDB half of docs/multi-tenancy/engineering.md phase 5, G1: a strict
// tenant's database is created DedicatedWAL (the create path refuses it
// otherwise), so the system shard's shared WAL never holds an entry of its
// shard, while a non-dedicated database on the same host does.
func TestRaftBackendFactory_StrictTenantDatabase_SharedWALHoldsNoEntry(t *testing.T) {
	hostDir := t.TempDir()
	sysBarrier := newTestSystemBarrier(t)
	host, mux := newSharedTestHostAt(t, hostDir, sysBarrier)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))

	ctx := context.Background()
	root, _ := registerVolumeTenant(t, reg, "st")
	const strictShard, sharedShard = uint64(2410), uint64(2411)
	src.add("st", "app", strictShard, root)
	src.setDedicated("st", "app")
	src.add("st", "plain", sharedShard, root)
	for _, name := range []string{"app", "plain"} {
		svcs, err := f.Services(ctx, "st", name)
		if err != nil {
			t.Fatalf("Services(%s): %v", name, err)
		}
		if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", name, err)
		}
	}
	if !mux.IsDedicated(strictShard) {
		t.Fatalf("the strict tenant's database WAL is not registered as dedicated")
	}
	held := sharedWALShards(t, f, host, sysBarrier, hostDir)
	if held[strictShard] {
		t.Fatalf("the shared host WAL holds an entry of the strict tenant's database (shard %d)", strictShard)
	}
	if !held[sharedShard] {
		t.Fatalf("the shared WAL holds no entry of the non-dedicated database (shard %d): the check proves nothing", sharedShard)
	}
}

// sharedWALShards closes the factory and the host, reopens the host's shared
// WAL under the system barrier and returns the set of shards it holds.
func sharedWALShards(t *testing.T, f *RaftBackendFactory, host interface{ Close() error }, sysBarrier *seal.StorageBarrier, hostDir string) map[uint64]bool {
	t.Helper()
	ctx := context.Background()
	if err := f.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	// The host holds the shared WAL's directory lock until it closes; a
	// second opener is refused while it is held. Closing it seals the
	// system barrier's EpochBarrier underneath the StorageBarrier, so the
	// wrapper is sealed to match and unsealed again: the WAL needs the
	// EpochBarrier unsealed to open.
	if err := host.Close(); err != nil {
		t.Fatalf("close the host: %v", err)
	}
	if err := sysBarrier.Seal(); err != nil {
		t.Fatalf("Seal system barrier: %v", err)
	}
	if err := sysBarrier.Unseal(ctx, seal.MustCredentials(ctx, []byte("mux-shared-wal-test"))); err != nil {
		t.Fatalf("Unseal system barrier: %v", err)
	}
	shared, err := waldb.Open(filepath.Join(hostDir, "wal"), waldb.WithBarrier(sysBarrier.EpochBarrier()))
	if err != nil {
		t.Fatalf("reopen the shared WAL: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	infos, err := shared.ListNodeInfo()
	if err != nil {
		t.Fatalf("ListNodeInfo: %v", err)
	}
	held := map[uint64]bool{}
	for _, ni := range infos {
		held[ni.ShardID] = true
	}
	return held
}

// TestRaftBackendFactory_DedicatedWAL_NoMuxRefusesToStart proves that when no
// multiplex LogDB is wired a dedicated-WAL requirement is refused, not
// ignored: the shard does not start, is not cached, and no WAL directory is
// created. Its log is never written under the node key instead.
func TestRaftBackendFactory_DedicatedWAL_NoMuxRefusesToStart(t *testing.T) {
	f, reg, src := newRaftFactory(t) // no WithMultiplexLogDB
	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "nm")
	const shardID = uint64(2100)
	src.add("nm", "app", shardID, dataRoot)
	src.setDedicated("nm", "app")

	_, err := f.Services(ctx, "nm", "app")
	var unavailable *database.TenantWALUnavailableError
	if !errors.As(err, &unavailable) || unavailable.TenantID != "nm" || unavailable.ShardID != shardID {
		t.Fatalf("Services = %v; want a TenantWALUnavailableError for nm/%d", err, shardID)
	}
	if !errors.Is(err, database.ErrDedicatedWALNotWired) {
		t.Fatalf("Services = %v; want the cause ErrDedicatedWALNotWired", err)
	}
	if n := len(f.ListStarted()); n != 0 {
		t.Fatalf("%d shards started; the refused shard must not be cached", n)
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

// TestOpenAndRegisterDedicatedWAL_Refusals exercises every branch of
// openAndRegisterDedicatedWAL: opt-out returns no WAL and no error; no
// multiplex, an absent or sealed barrier, a WAL-open failure and a duplicate
// registration each return a TenantWALUnavailableError, with the cause named,
// and leave no dedicated WAL registered.
func TestOpenAndRegisterDedicatedWAL_Refusals(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
		newFakeRaftInfoSource(), volTestLogger(), WithMultiplexLogDB(mux))

	_, tb := registerVolumeTenant(t, reg, "fb")
	dir := t.TempDir()

	refused := func(t *testing.T, f *RaftBackendFactory, shardID uint64, dir string, tb *seal.TenantBarrier) error {
		t.Helper()
		got, err := f.openAndRegisterDedicatedWAL("fb", "a", shardID, dir, tb, true)
		var unavailable *database.TenantWALUnavailableError
		if got != nil || !errors.As(err, &unavailable) || unavailable.TenantID != "fb" || unavailable.ShardID != shardID {
			t.Fatalf("openAndRegisterDedicatedWAL = %v, %v; want no WAL and a TenantWALUnavailableError for fb/%d", got, err, shardID)
		}
		return err
	}

	// Not opted in: nothing to open, nothing refused.
	if got, err := f.openAndRegisterDedicatedWAL("fb", "a", 3000, dir, tb, false); got != nil || err != nil {
		t.Fatalf("opt-out = %v, %v; want nil, nil", got, err)
	}

	// No multiplex wired.
	noMux := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg,
		newFakeRaftInfoSource(), volTestLogger())
	if err := refused(t, noMux, 3001, dir, tb); !errors.Is(err, database.ErrDedicatedWALNotWired) {
		t.Fatalf("no multiplex: %v; want ErrDedicatedWALNotWired", err)
	}

	// Nil barrier.
	if err := refused(t, f, 3002, dir, nil); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("nil barrier: %v; want ErrTenantNotFound", err)
	}

	// WAL directory that cannot be created: a regular file where it must go.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, dedicatedWALSubdir), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused(t, f, 3004, blocked, tb)

	// Duplicate registration: pre-register the shard, then opening again must
	// fail to register.
	_, tb2 := registerVolumeTenant(t, reg, "fb2")
	dup := &fakeLogDBForServer{}
	if err := mux.Register(3005, dup, tb2.Barrier().EpochBarrier()); err != nil {
		t.Fatalf("pre-register: %v", err)
	}
	if err := refused(t, f, 3005, t.TempDir(), tb2); !errors.Is(err, database.ErrShardAlreadyDedicated) {
		t.Fatalf("duplicate registration: %v; want ErrShardAlreadyDedicated", err)
	}

	// Sealed barrier.
	if err := tb.Seal(); err != nil {
		t.Fatalf("seal tenant barrier: %v", err)
	}
	if err := refused(t, f, 3003, dir, tb); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("sealed barrier: %v; want seal.ErrSealed", err)
	}

	for _, id := range []uint64{3001, 3002, 3003, 3004} {
		if mux.IsDedicated(id) {
			t.Fatalf("refused shard %d left a dedicated WAL registered", id)
		}
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
func (fakeLogDBForServer) NodeRemoved(uint64, uint64) (bool, error)               { return false, nil }
func (fakeLogDBForServer) SaveBootstrap(uint64, uint64, logdblib.Bootstrap) error { return nil }
func (fakeLogDBForServer) GetBootstrap(uint64, uint64) (logdblib.Bootstrap, bool, error) {
	return logdblib.Bootstrap{}, false, nil
}
func (fakeLogDBForServer) ListNodeInfo() ([]logdblib.NodeInfo, error) { return nil, nil }
func (fakeLogDBForServer) Close() error                               { return nil }

// staticGroups is a featuregate.MembershipSource over fixed groups.
type staticGroups map[uint64]featuregate.GroupMembership

func (g staticGroups) GroupMembership(shardID uint64) (featuregate.GroupMembership, error) {
	return g[shardID], nil
}

// The factory's shards answer the feature gate the composer wires in, so a
// dedicated shard placed with a node that does not advertise guarded writes
// is never sent one.
func TestRaftBackendFactory_ShardsAnswerTheFeatureGate(t *testing.T) {
	host, addr := newSharedTestHost(t)
	gate, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1},
		SystemShardID: 1,
		Members: staticGroups{
			1:   {ConfigChangeID: 1, Replicas: map[uint64]string{1: addr, 2: "peer:1"}},
			900: {ConfigChangeID: 1, Replicas: map[uint64]string{50: addr, 51: "peer:1"}},
		},
	})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	f := NewRaftBackendFactory(host, 1, addr, nil, &fakeRaftInfoSource{}, nil, WithFeatureGate(gate))
	if f.adapter.FeatureEnabled(900, featuregate.GuardedWrites) {
		t.Fatal("a shard placed with a node that advertises nothing was enabled")
	}
	ungated := NewRaftBackendFactory(host, 1, addr, nil, &fakeRaftInfoSource{}, nil)
	if !ungated.adapter.FeatureEnabled(900, featuregate.GuardedWrites) {
		t.Fatal("a transportless host needs no gate")
	}
}
