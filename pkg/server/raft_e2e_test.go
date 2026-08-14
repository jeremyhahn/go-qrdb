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

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// alwaysAbsentCatalog is a DatabaseCatalog that reports every database as
// absent, simulating a node whose local catalog (meta.json) never recorded a
// dedicated-raft database created on another node.
type alwaysAbsentCatalog struct{}

func (alwaysAbsentCatalog) DatabaseExists(string, string) bool { return false }

// TestRaftE2E_ResolveIgnoresLocalCatalog proves that an IsolationRaft database
// resolves through the replicated placement-backed RaftBackendFactory even when
// the node-local catalog has no record of it — the post-restart follower / leader
// change case where only the creating node holds a local meta.json record. A
// genuinely unknown database for the raft tenant still surfaces as a 404.
func TestRaftE2E_ResolveIgnoresLocalCatalog(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()
	stack.createRaftTenantDB(t, "acme", "orders")

	// Simulate a node whose local catalog never recorded the database.
	stack.resolver.catalog = alwaysAbsentCatalog{}

	// The dedicated-raft database still resolves and serves data via the raft
	// backend, despite the local catalog reporting it absent.
	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolve with absent local catalog: %v", err)
	}
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, gerr := kv.Get(ctx, "k"); gerr != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, gerr)
	}

	// A genuinely unknown database for the raft tenant is a 404.
	_, err = stack.resolver.KVService("acme", "does-not-exist")
	var dnf *rest.DatabaseNotFoundError
	if !errors.As(err, &dnf) {
		t.Fatalf("unknown db: got %T %v, want *rest.DatabaseNotFoundError", err, err)
	}
}

// raftE2EStack bundles the live components the daemon wires for IsolationRaft, so
// the end-to-end test exercises the real create→resolve→data-plane vertical
// through the in-process service seam (no transport/mTLS).
type raftE2EStack struct {
	reg      *seal.BarrierRegistry
	tenants  *tenant.TenantService
	manager  *database.Manager
	dbSvc    service.DatabaseServicer
	raftReg  *RaftBackendFactory
	resolver *restServiceRegistry
}

// newRaftE2EStack assembles the full IsolationRaft stack against real
// subsystems: a volume-pooled barrier registry (system barrier initialized so
// system-managed tenants auto-unseal), a tenant service entitled to raft +
// system-managed barrier, a database manager with a shard allocator, the
// quota-enforcing DatabaseService, the shared-host RaftBackendFactory, and the
// transport-neutral resolver every protocol uses.
func newRaftE2EStack(t *testing.T) *raftE2EStack {
	t.Helper()
	ctx := context.Background()

	reg := newVolumePooledRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}

	tenants, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, volTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeRaft},
			AllowDedicatedRaft:        true,
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	mgr := database.NewManager(volTestLogger(), t.TempDir(), database.WithAllocator(shard.NewAllocator()))
	t.Cleanup(func() { _ = mgr.Close() })

	// Numeric quota plane (raft-group counter reads the persisted usage record).
	quotaSvc, err := quota.NewService(tenants, volTestLogger())
	if err != nil {
		t.Fatalf("quota.NewService: %v", err)
	}
	quotaSvc.RegisterCounter(quota.ResourceRaftGroups, func(ctx context.Context, id string) (int64, error) {
		return tenants.GetQuotaUsage(ctx, id, quota.ResourceRaftGroups)
	})
	quotaSvc.RegisterCounter(quota.ResourceDatabases, func(ctx context.Context, id string) (int64, error) {
		return tenants.GetQuotaUsage(ctx, id, quota.ResourceDatabases)
	})

	dbSvc := service.NewDatabaseService(mgr,
		service.WithEntitlementChecker(tenants),
		service.WithTenantInfoLookup(tenants),
		service.WithQuotaChecker(quotaSvc),
		service.WithQuotaUsageRecorder(tenants),
	)

	host, _ := newSharedTestHost(t)
	raftReg := NewRaftBackendFactoryFromManager(
		host, sharedHostReplicaID, sharedHostRaftAddr, reg, mgr, tenant.SystemTenantID, volTestLogger())
	t.Cleanup(func() { _ = raftReg.CloseAll() })

	resolver := &restServiceRegistry{
		dbReg:        NewDatabaseServiceRegistry("system"),
		barrierReg:   reg,
		tenantSvc:    tenants,
		catalog:      newManagerCatalog(mgr, tenant.SystemTenantID),
		raftBackend:  raftReg,
		systemTenant: tenant.SystemTenantID,
	}

	return &raftE2EStack{
		reg:      reg,
		tenants:  tenants,
		manager:  mgr,
		dbSvc:    dbSvc,
		raftReg:  raftReg,
		resolver: resolver,
	}
}

// createRaftTenantDB creates a system-managed IsolationRaft tenant and a database
// for it through the real DatabaseService.CreateForTenant path, returning the
// tenant DataRoot and the database's runtime info.
func (s *raftE2EStack) createRaftTenantDB(t *testing.T, tenantID, db string) (string, *database.DatabaseInfo) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.tenants.CreateTenant(ctx, tenantID, tenantID, tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(%s): %v", tenantID, err)
	}
	info, err := s.dbSvc.CreateForTenant(tenant.WithTenantID(ctx, tenantID),
		database.DatabaseConfig{Name: db, Engine: database.EngineKV})
	if err != nil {
		t.Fatalf("CreateForTenant(%s/%s): %v", tenantID, db, err)
	}
	ti, err := s.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", tenantID, err)
	}
	return ti.DataRoot, info
}

// TestRaftE2E_FullVertical is the in-process daemon-seam proof for Phase 4b. It
// exercises the real create→resolve→data-plane vertical: an IsolationRaft tenant
// and database are created through DatabaseService.CreateForTenant; KV + MVCC +
// TimeSeries round-trip through the transport-neutral resolver; the state-machine
// Pebble files are proven to live under {DataRoot}/databases/{db}/shard-<N> (NOT
// under the shared host NodeHostDir); data is isolated from the system tenant and
// a second raft tenant; and a restart (shard stop + re-attach) preserves data.
func TestRaftE2E_FullVertical(t *testing.T) {
	stack := newRaftE2EStack(t)
	ctx := context.Background()

	dataRoot, info := stack.createRaftTenantDB(t, "acme", "orders")
	if info.Backend != database.BackendRaft {
		t.Fatalf("database backend = %q, want raft", info.Backend)
	}
	if info.ShardID < shard.UserMin {
		t.Fatalf("shard ID = %d, want >= UserMin", info.ShardID)
	}

	// Data plane via the resolver (the exact seam every transport uses).
	kv, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService: %v", err)
	}
	const secret = "ORDER-PLAINTEXT-42"
	if err := kv.Put(ctx, "order:1", []byte(secret)); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("KV.Get = %q, %v; want %q", got, err, secret)
	}

	mvcc, err := stack.resolver.MVCCService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver MVCCService: %v", err)
	}
	if err := mvcc.Put(ctx, "v:1", []byte("mvcc-val")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}
	if got, err := mvcc.Get(ctx, "v:1"); err != nil || string(got) != "mvcc-val" {
		t.Fatalf("MVCC.Get = %q, %v; want mvcc-val", got, err)
	}

	ts, err := stack.resolver.TimeSeriesService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver TimeSeriesService: %v", err)
	}
	now := time.Now()
	if err := ts.Write(ctx, "lat", 1.5, now, map[string]string{"r": "us"}); err != nil {
		t.Fatalf("TS.Write: %v", err)
	}
	if pts, err := ts.Query(ctx, "lat", now.Add(-time.Minute), now.Add(time.Minute)); err != nil || len(pts) == 0 {
		t.Fatalf("TS.Query = %d points, %v; want >=1", len(pts), err)
	}

	// On-disk proof: SM data under the tenant volume, NOT the shared host dir.
	shardDirName := "shard-" + itoa(info.ShardID)
	smDir := filepath.Join(dataRoot, "databases", "orders", shardDirName)
	if fi, err := os.Stat(smDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected SM data dir %q under tenant DataRoot: %v", smDir, err)
	}
	t.Logf("PROOF: SM Pebble data dir present at %q", smDir)

	// Second raft tenant on its own dedicated shard. (The database.Manager
	// namespaces databases by global name across all isolation modes, so the
	// second tenant uses a distinct name; same-name/different-tenant shard
	// isolation is proven directly at the factory level in
	// TestRaftBackendFactory_PerShardIsolation.)
	root2, info2 := stack.createRaftTenantDB(t, "globex", "invoices")
	if info2.ShardID == info.ShardID {
		t.Fatalf("second raft tenant got the same shard ID %d — not isolated", info2.ShardID)
	}
	kv2, err := stack.resolver.KVService("globex", "invoices")
	if err != nil {
		t.Fatalf("resolver KVService(globex): %v", err)
	}
	if err := kv2.Put(ctx, "order:1", []byte("globex-value")); err != nil {
		t.Fatalf("globex KV.Put: %v", err)
	}
	// acme's value must be unchanged, and the key must not leak across shards.
	if got, err := kv.Get(ctx, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("acme KV.Get after globex write = %q, %v; want %q (isolation breach)", got, err, secret)
	}
	if got, err := kv2.Get(ctx, "order:1"); err != nil || string(got) != "globex-value" {
		t.Fatalf("globex KV.Get = %q, %v; want globex-value", got, err)
	}
	// Cross-tenant leakage check: globex must not see acme's MVCC key.
	if _, err := mvcc.Get(ctx, "v:1"); err != nil {
		t.Fatalf("acme MVCC.Get v:1: %v", err)
	}
	mvcc2, err := stack.resolver.MVCCService("globex", "invoices")
	if err != nil {
		t.Fatalf("resolver MVCCService(globex): %v", err)
	}
	if _, err := mvcc2.Get(ctx, "v:1"); err == nil {
		t.Error("globex MVCC.Get found acme-only key v:1 — isolation breach")
	}
	t.Logf("PROOF: two raft tenants isolated under distinct shards %d/%d", info.ShardID, info2.ShardID)

	// Encryption at rest: stop acme's shard so files flush, scan the tenant
	// volume — the plaintext secret must not appear.
	if err := stack.raftReg.Close("acme", "orders"); err != nil {
		t.Fatalf("stop acme shard: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(dataRoot, "databases", "orders"), []byte(secret))
	t.Logf("PROOF: acme data encrypted at rest under %q (plaintext absent)", filepath.Join(dataRoot, "databases", "orders"))

	// Restart proof: re-attach acme's shard at the same DataDir — data persists.
	kvAfter, err := stack.resolver.KVService("acme", "orders")
	if err != nil {
		t.Fatalf("resolver KVService after restart: %v", err)
	}
	if got, err := getWhenReadable(ctx, t, kvAfter.Get, "order:1"); err != nil || string(got) != secret {
		t.Fatalf("acme KV.Get after restart = %q, %v; want %q (data did not persist)", got, err, secret)
	}
	t.Logf("PROOF: acme shard re-attached from tenant volume; data persisted across restart")

	_ = root2
}

// itoa converts a uint64 shard ID to its decimal string without importing strconv
// at call sites; kept local to this proof.
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
