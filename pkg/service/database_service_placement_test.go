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

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memPlacementKV is an in-memory placement.KV for service-layer placement tests.
type memPlacementKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemPlacementKV() *memPlacementKV { return &memPlacementKV{data: map[string][]byte{}} }

func (m *memPlacementKV) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memPlacementKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (m *memPlacementKV) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// CompareAndSwap implements placement.AtomicKV so scale and rebalance tests
// built on memPlacementKV exercise the same CAS-guarded write path
// addObserverReplica/removeReplicaFromGroup now use in production, rather
// than a weaker double that would silently let a stale write win. A nil
// expected requires the key to be currently absent (a create).
func (m *memPlacementKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if expected == nil {
		if ok {
			return false, nil
		}
	} else if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	cp := make([]byte, len(next))
	copy(cp, next)
	m.data[key] = cp
	return true, nil
}

func (m *memPlacementKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	m.mu.Lock()
	snapshot := make(map[string][]byte)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			snapshot[k] = v
		}
	}
	m.mu.Unlock()
	for k, v := range snapshot {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// staticTopology is a mock placement.TopologyProvider for service tests.
type staticTopology struct {
	nodes []placement.NodeLoad
}

func (s *staticTopology) Nodes() []placement.NodeLoad { return s.nodes }

// newPlacementDatabaseService builds a DatabaseService for an IsolationRaft
// tenant wired with a placement store + scheduler over the given topology.
func newPlacementDatabaseService(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota, nodes []placement.NodeLoad) (DatabaseServicer, *placement.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)
	return svc, store
}

func healthyNodes(ids ...string) []placement.NodeLoad {
	out := make([]placement.NodeLoad, 0, len(ids))
	for _, id := range ids {
		out = append(out, placement.NodeLoad{NodeID: id, RaftAddr: id + ":1", Healthy: true})
	}
	return out
}

// TestCreateForTenant_PersistsPlacement verifies a dedicated-raft database's
// placement is computed and persisted on create.
func TestCreateForTenant_PersistsPlacement(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 3
	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if p.ShardID != info.ShardID {
		t.Errorf("placement shard = %d, want %d (db shard)", p.ShardID, info.ShardID)
	}
	if p.RF != 3 || len(p.Replicas) != 3 {
		t.Errorf("placement RF = %d replicas = %d, want 3/3", p.RF, len(p.Replicas))
	}
	if p.LeaderReplicaID == 0 {
		t.Errorf("placement leader replica ID unset")
	}
	// No replica names a path: each node resolves its own directory from the
	// tenant's volume pool.
	for _, r := range p.Replicas {
		if r.DataRoot != "" {
			t.Errorf("replica %d records the path %q; placement records none", r.ReplicaID, r.DataRoot)
		}
	}
}

// newPlacementDatabaseServiceWithLocalNode is like newPlacementDatabaseService
// but wires a localNodeID via WithLocalNodeID. The node ID must appear in the
// topology so the scheduler can assign a replica to it.
func newPlacementDatabaseServiceWithLocalNode(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota, nodes []placement.NodeLoad, localNodeID string) (DatabaseServicer, *placement.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithLocalNodeID(localNodeID),
	)
	return svc, store
}

// TestCreateForTenant_PersistsPlacementWithNoPathForAnyReplica verifies the
// creating node records no path even for its own replica: the placement table
// is replicated, a path names the creating node's disk, and every node -- this
// one included -- resolves its own replica's directory from the tenant's pool.
func TestCreateForTenant_PersistsPlacementWithNoPathForAnyReplica(t *testing.T) {
	const localNode = "local:1"
	nodes := healthyNodes(localNode, "remote:1", "remote:2")
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseServiceWithLocalNode(t, raftTenant(t), f, nodes, localNode)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("bug1db")
	cfg.Replicas = 3
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	p, err := store.Get(ctx, "rt", "bug1db")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if len(p.Replicas) != 3 {
		t.Fatalf("expected 3 replicas, got %d", len(p.Replicas))
	}

	localCount := 0
	for _, r := range p.Replicas {
		if r.NodeID == localNode {
			localCount++
		}
		if r.DataRoot != "" {
			t.Errorf("replica on %q records the path %q; placement records none", r.NodeID, r.DataRoot)
		}
	}
	if localCount != 1 {
		t.Errorf("local replica count = %d, want 1", localCount)
	}
}

// TestDropForTenant_RemovesPlacement verifies the placement entry is removed on
// drop.
func TestDropForTenant_RemovesPlacement(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 2
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); err != nil {
		t.Fatalf("placement should exist after create: %v", err)
	}

	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after drop error = %v, want ErrPlacementNotFound", err)
	}
}

// TestCreateForTenant_ClampsRFToClusterSize verifies the requested replication
// factor is clamped to the live cluster size before metering and placement.
func TestCreateForTenant_ClampsRFToClusterSize(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 0, MaxTotalReplicas: 100}}
	// Cluster of two nodes; request five replicas.
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1", "n2"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 5
	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if info.Replicas != 2 {
		t.Errorf("persisted Replicas = %d, want 2 (clamped to cluster size)", info.Replicas)
	}
	if f.incTotal != 2 {
		t.Errorf("metered replicas = %d, want 2 (clamped)", f.incTotal)
	}
	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d, want 2", p.RF)
	}
}

// TestCreateForTenant_PlacementFailureRollsBack verifies that when placement
// cannot be computed (no healthy nodes), the create is rolled back: the database
// is dropped and the quota reservations are released.
func TestCreateForTenant_PlacementFailureRollsBack(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	// One unhealthy node => ClusterSize 1 (no clamp below 1), but Place fails
	// with ErrInsufficientNodes because no node is healthy.
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f,
		[]placement.NodeLoad{{NodeID: "n1", RaftAddr: "n1:1", Healthy: false}})
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	_, err := svc.CreateForTenant(ctx, cfg)
	if !errors.Is(err, placement.ErrInsufficientNodes) {
		t.Fatalf("CreateForTenant error = %v, want ErrInsufficientNodes", err)
	}
	// Database rolled back.
	if _, gerr := svc.GetForTenant(ctx, "orders"); gerr == nil {
		t.Errorf("database should have been dropped after placement failure")
	}
	// No placement entry persisted.
	if _, gerr := store.Get(ctx, "rt", "orders"); !errors.Is(gerr, placement.ErrPlacementNotFound) {
		t.Errorf("no placement entry expected, got err = %v", gerr)
	}
	// Quota reservations released.
	if f.incRaft != f.decRaft || f.incTotal != f.decTotal {
		t.Errorf("quota not balanced after rollback: incRaft=%d decRaft=%d incTotal=%d decTotal=%d",
			f.incRaft, f.decRaft, f.incTotal, f.decTotal)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482: the replicated drop fence (Placement.Dropping), owner-ruled
// as the fix for the raft-isolation gap in the original database-drop fence
// (which is node-local: database.Manager's own Dropping flag, checked by
// Manager.Get, is invisible to a replica whose local catalog does not carry
// the entry -- see docs/databases/architecture.md). DropForTenant/Drop now
// commit this marker to the REPLICATED placement table before reclaiming
// anything, so every node's own resolution (RaftBackendFactory.Services,
// tested independently in pkg/server/raft_backend_placement_test.go) sees
// it identically. These tests cover the service-layer half: that the
// marker is actually committed, survives a failure exactly where a crash
// or leader change would leave it, and that a retry completes the job.
// ---------------------------------------------------------------------------

// flakyDeleteKV wraps a placement.KV and can be armed to fail exactly the
// next Delete call, standing in for a crash (or a leader change stranding
// the in-flight proposal) between the manager-level drop completing and
// the placement entry's removal committing.
type flakyDeleteKV struct {
	placement.KV
	failNextDelete bool
	deleteCalls    int
}

func (f *flakyDeleteKV) Delete(ctx context.Context, key string) error {
	f.deleteCalls++
	if f.failNextDelete {
		f.failNextDelete = false
		return errors.New("simulated: node failed before the placement delete committed")
	}
	return f.KV.Delete(ctx, key)
}

// CompareAndSwap delegates to the embedded KV when it implements AtomicKV
// (memPlacementKV, this double's only real caller, does). Embedding the
// placement.KV INTERFACE, as this type does, promotes only that interface's
// own method set -- never the concrete value's extra methods -- so without
// this passthrough, placement.Store.CompareAndSwapPlacement's own
// s.kv.(AtomicKV) assertion silently fails against a *flakyDeleteKV even
// though the memPlacementKV underneath it fully supports compare-and-swap.
func (f *flakyDeleteKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	atomicKV, ok := f.KV.(placement.AtomicKV)
	if !ok {
		return false, errors.New("flakyDeleteKV: embedded KV does not implement AtomicKV")
	}
	return atomicKV.CompareAndSwap(ctx, key, expected, next)
}

// newFlakyPlacementDatabaseService is newPlacementDatabaseService with a
// placement KV that can be armed to fail its next Delete, so a test can
// simulate a crash landing exactly between DropForTenant's manager-level
// drop and its placement-entry removal.
func newFlakyPlacementDatabaseService(t *testing.T, info *tenant.TenantInfo, f *fakeReplicaQuota, nodes []placement.NodeLoad) (DatabaseServicer, *placement.Store, *flakyDeleteKV) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	kv := &flakyDeleteKV{KV: newMemPlacementKV()}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)
	return svc, store, kv
}

// TestDropForTenant_PlacementDeletionFailureLeavesDatabaseFenced is the
// owner-specified test: a placement-deletion failure at the end of the drop
// must leave the database fenced (Placement.Dropping still true, the entry
// still present), not silently both gone from the local catalog AND
// unrefused in placement. See
// pkg/server/raft_backend_placement_test.go for the companion proof that a
// Dropping=true placement entry is what makes RaftBackendFactory.Services
// -- the resolution seam an actual write reaches -- refuse.
func TestDropForTenant_PlacementDeletionFailureLeavesDatabaseFenced(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store, kv := newFlakyPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	kv.failNextDelete = true
	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("DropForTenant with a failing placement delete = nil error, want the delete failure")
	}

	// The manager-level drop (local catalog, shared-store reclaim) already
	// completed -- only the placement teardown failed.
	if _, err := svc.GetForTenant(ctx, "orders"); err == nil {
		t.Fatal("local catalog entry should be gone: the manager-level drop completed before the placement delete failed")
	}

	// The placement entry -- and its fence -- must survive the failed
	// delete. This is the "fenced, not writable" state: still there,
	// still marked dropping, so every node's own placement-based
	// resolution keeps refusing new operations against it.
	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement entry should survive a failed delete, got: %v", err)
	}
	if !p.Dropping {
		t.Error("placement.Dropping = false after a failed delete, want true (the database must stay fenced)")
	}
}

// TestDropForTenant_RetryAfterPlacementDeletionFailureCompletes drives the
// interruption from TestDropForTenant_PlacementDeletionFailureLeavesDatabaseFenced
// end to end: the retry an operator (or an automatic resume) issues must
// finish the placement teardown, even though the local catalog entry the
// first attempt already removed is gone and the second DropForTenant call
// can no longer see it as a "raft database" through the manager.
func TestDropForTenant_RetryAfterPlacementDeletionFailureCompletes(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store, kv := newFlakyPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	kv.failNextDelete = true
	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("first DropForTenant should have failed on the placement delete")
	}

	// Retry: the local catalog is already gone (manager.DropForTenant now
	// answers NotFoundError), so this exercises the branch that recognizes
	// "already dropped, finish the placement teardown" rather than
	// treating that as a fresh failure.
	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("retry DropForTenant: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after the completed retry error = %v, want ErrPlacementNotFound", err)
	}
	if kv.deleteCalls != 2 {
		t.Errorf("placement Delete calls = %d, want 2 (the failed attempt and the completed retry)", kv.deleteCalls)
	}
}

// TestDrop_CommitsAndRemovesPlacementFence is Drop's (the non-tenant,
// system-initiated path) equivalent of TestDropForTenant_RemovesPlacement:
// it shares dropRaftPlacement/finishRaftPlacementTeardown with
// DropForTenant, so the fence is committed and the whole entry removed the
// same way, using the local catalog (rather than a tenant-scoped context)
// to resolve which tenant's placement to touch.
func TestDrop_CommitsAndRemovesPlacementFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	if err := svc.Drop(context.Background(), "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after Drop error = %v, want ErrPlacementNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3: two Codex findings on the round-2 fence.
//
// Finding 4 -- concurrent drops clear each other's replicated fence: a
// second Drop attempt that finds the fence already set must never be the
// one to clear it on failure, since it never set it. dropRaftPlacement's
// owned return value is exactly this signal (see its doc comment).
//
// Finding 5 -- a legitimate drop request handled by a node that never had
// the local catalog record (a non-creator replica, not a retry) must not be
// mistaken for "an earlier attempt already finished tearing this database
// down": Placement.DropCommitted (set by markRaftPlacementDropCommitted
// right before the manager-level drop's success is followed by the
// placement teardown) is the signal that tells the two apart.
// ---------------------------------------------------------------------------

// TestDropRaftPlacement_ConcurrentCallsOnlyOneOwnsTheFence proves finding 4's
// core guarantee directly at dropRaftPlacement: of two calls racing for the
// same (tenantID, name), only the one that actually transitions
// Dropping false->true is reported as the fence's owner. The second must
// report hadPlacement=true (there is something to be fenced) but owned=false,
// so its caller's rollback path -- gated on owned -- never fires.
func TestDropRaftPlacement_ConcurrentCallsOnlyOneOwnsTheFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	hadPlacementA, ownedA, errA := svc.dropRaftPlacement(ctx, "rt", "orders")
	if errA != nil {
		t.Fatalf("first dropRaftPlacement: %v", errA)
	}
	if !hadPlacementA || !ownedA {
		t.Fatalf("first call: hadPlacement=%v owned=%v, want true/true (it must be the one to claim the fence)", hadPlacementA, ownedA)
	}

	hadPlacementB, ownedB, errB := svc.dropRaftPlacement(context.Background(), "rt", "orders")
	if errB != nil {
		t.Fatalf("second dropRaftPlacement: %v", errB)
	}
	if !hadPlacementB {
		t.Error("second call: hadPlacement = false, want true (the entry still exists, fenced by the first call)")
	}
	if ownedB {
		t.Error("second call: owned = true, want false (the first call already claimed the fence)")
	}

	// The second (losing) call's manager-level drop fails -- e.g. with
	// *database.DropInProgressError, exactly what the finding describes.
	// Its rollback is gated on ownedB, so it must never run.
	if ownedB && isReclamationFailure(errors.New("simulated manager failure")) {
		svc.clearRaftPlacementFence(ctx, "rt", "orders")
	}

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !p.Dropping {
		t.Error("Dropping = false, want true (the first call's fence must survive the second call's unowned, failed attempt)")
	}
}

// TestDropForTenant_NonCreatorReplica_NeverFinishesTeardownWithoutCommitting
// proves round-3 findings 5 and 6: a DropForTenant call handled by a node
// whose local database.Manager never had the database's record at all --
// not a retry of a drop that already ran to completion elsewhere -- must
// not be treated as "finish the placement teardown". Round 3's second
// review strengthened this further: such a request must be refused
// UPFRONT, via GetForTenantIgnoringFence, before the replicated fence is
// ever touched at all -- claiming it first and discovering the local
// absence only afterward left a live database permanently fenced
// cluster-wide for an ordinary client with no guaranteed route back to the
// node that actually holds the record. The placement entry must therefore
// survive COMPLETELY UNTOUCHED (not merely fenced), so a retry against the
// node that actually owns the local record can complete the real drop.
func TestDropForTenant_NonCreatorReplica_NeverFinishesTeardownWithoutCommitting(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	creator, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := creator.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	// A second DatabaseService, standing in for a different node's replica:
	// it shares the SAME replicated placement store (as every node's
	// placement resolution does), but has its OWN, entirely empty
	// database.Manager -- it never created "orders" and has no local
	// catalog record for it at all.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	nonCreatorMgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = nonCreatorMgr.Close() })
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	nonCreator := NewDatabaseService(nonCreatorMgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)

	err = nonCreator.DropForTenant(ctx, "orders")
	var notFound *database.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("DropForTenant on a non-creator replica error = %v (%T), want *database.NotFoundError", err, err)
	}

	// The placement entry must survive COMPLETELY UNTOUCHED: nothing was
	// actually reclaimed or released, and the fence was never claimed in
	// the first place, so silently fencing (let alone deleting) it here
	// would misrepresent a drop that never happened.
	p, gerr := store.Get(ctx, "rt", "orders")
	if gerr != nil {
		t.Fatalf("placement entry should survive: %v", gerr)
	}
	if p.Dropping {
		t.Error("Dropping = true, want false (the fence must never be claimed by a request this node cannot service)")
	}
	if p.DropCommitted {
		t.Error("DropCommitted = true, want false (no node has actually completed the local drop yet)")
	}

	// The retry against the actual creator must still be able to complete
	// the real drop.
	if err := creator.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on the creator after the non-creator's failed attempt: %v", err)
	}
	if _, gerr := store.Get(ctx, "rt", "orders"); !errors.Is(gerr, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after the creator's completed drop error = %v, want ErrPlacementNotFound", gerr)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3, finding 7: only a RECLAMATION failure clears the
// replicated fence, matching database.Manager.Drop's own local fence, which
// already only clears on a KeyspaceReclaimError (step 1). Every other
// manager-level failure (a backend close, a directory removal) must leave
// the replicated fence set exactly as it leaves the local one set, so the
// database stays refused everywhere -- not unrefused on other nodes while
// still locally fenced -- until a retry actually finishes tearing it down.
// ---------------------------------------------------------------------------

// alwaysFailReclaimer is a database.KeyspaceReclaimer that always fails,
// standing in for a shared-store reclamation error.
type alwaysFailReclaimer struct {
	err error

	// unfenceErr, when set, makes the rollback's store unfence fail too.
	unfenceErr error
}

func (r *alwaysFailReclaimer) ReclaimDatabaseKeyspace(_ context.Context, _, _, _ string) error {
	return r.err
}

// FenceDatabaseKeyspace succeeds: the failure under test is the reclamation.
func (r *alwaysFailReclaimer) FenceDatabaseKeyspace(_ context.Context, _, _ string) error {
	return nil
}

// UnfenceDatabaseKeyspace succeeds unless unfenceErr is set, so by default
// a failed drop rolls back cleanly.
func (r *alwaysFailReclaimer) UnfenceDatabaseKeyspace(_ context.Context, _, _, _ string) error {
	return r.unfenceErr
}

// TestIsReclamationFailure proves isReclamationFailure's classification
// directly: it must match ONLY *database.KeyspaceReclaimError (wrapped or
// not), never the other manager-level failures Drop/DropForTenant can see.
func TestIsReclamationFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"reclaim failure, rolled back", &database.KeyspaceReclaimError{Name: "orders", TenantID: "rt", Err: errors.New("boom"), RolledBack: true}, true},
		{"wrapped reclaim failure, rolled back", fmt.Errorf("wrap: %w", &database.KeyspaceReclaimError{Name: "orders", Err: errors.New("boom"), RolledBack: true}), true},
		{"fence failure, rolled back", &database.KeyspaceFenceError{Name: "orders", TenantID: "rt", Err: errors.New("boom"), RolledBack: true}, true},
		{"reclaim failure whose store fence could not be cleared", &database.KeyspaceReclaimError{Name: "orders", Err: errors.New("boom")}, false},
		{"fence failure whose store fence could not be cleared", &database.KeyspaceFenceError{Name: "orders", Err: errors.New("boom")}, false},
		{"local fence never persisted", &database.LocalFenceError{Name: "orders", Err: errors.New("disk")}, true},
		{"drop error (close/removeall)", &database.DropError{Name: "orders", Err: errors.New("close failed")}, false},
		{"not found", &database.NotFoundError{Name: "orders"}, false},
		{"drop in progress", &database.DropInProgressError{Name: "orders"}, false},
		{"generic error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReclamationFailure(tt.err); got != tt.want {
				t.Errorf("isReclamationFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestDrop_ReclaimFailureClearsTheReplicatedFence proves the positive case:
// a genuine reclamation failure DOES roll back the replicated fence, so the
// database stays resolvable and retryable everywhere -- matching
// database.Manager.Drop's own local fence, which also clears on exactly
// this failure.
func TestDrop_ReclaimFailureClearsTheReplicatedFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	svc.manager.SetKeyspaceReclaimer(&alwaysFailReclaimer{err: errors.New("shared store unavailable")})

	if err := svc.Drop(context.Background(), "orders"); err == nil {
		t.Fatal("Drop with a failing reclaimer = nil error, want the reclaim failure")
	}

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement entry should survive a reclaim failure: %v", err)
	}
	if p.Dropping {
		t.Error("Dropping = true after a reclamation failure, want false (the fence must be rolled back, matching the local manager's own fence)")
	}
}

// TestDrop_ResumesAfterRestartDespiteTheLocalFence is finding 7's second
// half: after a restart, Manager.Get keeps returning *database.DroppingError
// for a database whose local fence survived the restart (Open restores
// Dropping from meta.json) -- the exact resolution path Drop's tenantID
// lookup used to depend on. Before the fix this left tenantID empty,
// dropRaftPlacement was never called at all, and a manager-level drop that
// went on to succeed anyway left the replicated placement entry (and its
// fence) permanently stranded. This proves Drop instead recovers the tenant
// ID from the DroppingError itself and completes the placement teardown.
func TestDrop_ResumesAfterRestartDespiteTheLocalFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	// Simulate a restart that reopened the database with its local fence
	// already set (see database.Manager.Open restoring Dropping from
	// meta.json), WITHOUT touching the replicated placement entry at all --
	// exactly the state a crash between dropRaftPlacement and
	// manager.Drop's own completion would leave.
	db, err := svc.manager.Get("orders")
	if err != nil {
		t.Fatalf("manager.Get before simulating the restart: %v", err)
	}
	db.SetDropping(true)
	if _, err := svc.manager.Get("orders"); err == nil {
		t.Fatal("manager.Get should now refuse with DroppingError, precondition for this test not met")
	}

	if err := svc.Drop(context.Background(), "orders"); err != nil {
		t.Fatalf("Drop after a simulated restart: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after the resumed drop error = %v, want ErrPlacementNotFound (the retry must finish tearing down the replicated entry, not strand it)", err)
	}
}

// TestDropForTenant_ResumesAfterRestartAndReleasesQuota is finding 9: a
// resumed tenant-scoped drop must still recover Backend/Replicas/
// DedicatedWAL through the local fence (GetForTenantIgnoringFence, not
// GetForTenant, which would itself refuse with DroppingError), so the
// three quota decrements a successful drop always performs are not
// silently skipped just because the local record was still fenced when
// this retry started.
func TestDropForTenant_ResumesAfterRestartAndReleasesQuota(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	decRaftBefore, decTotalBefore := f.decRaft, f.decTotal

	// Simulate a restart that reopened the database with its local fence
	// already set, exactly as TestDrop_ResumesAfterRestartDespiteTheLocalFence
	// does for the unscoped path.
	db, err := svc.manager.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant before simulating the restart: %v", err)
	}
	db.SetDropping(true)
	if _, err := svc.manager.GetForTenant(ctx, "orders"); err == nil {
		t.Fatal("GetForTenant should now refuse with DroppingError, precondition for this test not met")
	}

	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant after a simulated restart: %v", err)
	}
	if _, err := store.Get(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement after the resumed drop error = %v, want ErrPlacementNotFound", err)
	}
	if f.decRaft != decRaftBefore+1 {
		t.Errorf("raft-group quota decrement = %d, want %d (the resumed drop must still release it)", f.decRaft, decRaftBefore+1)
	}
	if f.decTotal != decTotalBefore+1 {
		t.Errorf("total-replica quota decrement = %d, want %d (the resumed drop must still release it)", f.decTotal, decTotalBefore+1)
	}
}

// failNthCompareAndSwapKV wraps a memPlacementKV whose CompareAndSwap fails
// with a fixed error on exactly the Nth call (1-indexed), succeeding
// normally on every other call.
type failNthCompareAndSwapKV struct {
	*memPlacementKV
	failOnCallN int
	callCount   int
	err         error
}

func (f *failNthCompareAndSwapKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	f.callCount++
	if f.callCount == f.failOnCallN {
		return false, f.err
	}
	return f.memPlacementKV.CompareAndSwap(ctx, key, expected, next)
}

// TestDrop_MarkDropCommittedFailureFailsTheDrop proves finding 8: a failure
// persisting Placement.DropCommitted must fail the whole Drop call, not be
// logged and discarded on the way to finishRaftPlacementTeardown regardless
// -- otherwise a subsequent (or concurrent) failure tearing down the
// placement entry would strand it with DropCommitted still false forever,
// exactly as a later retry landing on a different node cannot distinguish
// from "this node never had the local record" (finding 5).
func TestDrop_MarkDropCommittedFailureFailsTheDrop(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	boom := errors.New("kv: unavailable")
	kv := &failNthCompareAndSwapKV{memPlacementKV: newMemPlacementKV(), failOnCallN: 2, err: boom}
	store, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	).(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	// Call 1: TryMarkDropping's own CompareAndSwap (inside dropRaftPlacement).
	// Call 2 (armed to fail): MarkDropCommitted's CompareAndSwap.
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	err = svc.Drop(context.Background(), "orders")
	if !errors.Is(err, boom) {
		t.Fatalf("Drop error = %v, want to wrap %v", err, boom)
	}

	// The placement entry must survive, still fenced and NOT DropCommitted:
	// finishRaftPlacementTeardown must never have run.
	p, gerr := store.Get(ctx, "rt", "orders")
	if gerr != nil {
		t.Fatalf("placement entry should survive: %v", gerr)
	}
	if !p.Dropping {
		t.Error("Dropping = false, want true (unchanged)")
	}
	if p.DropCommitted {
		t.Error("DropCommitted = true, want false (the failed persist must not silently be treated as having succeeded)")
	}
}

// The default database cannot be dropped, and both service entry points
// refuse it before touching anything -- the replicated placement fence
// included.
func TestDatabaseService_RefusesToDropTheDefaultDatabase(t *testing.T) {
	svc := NewDatabaseService(database.NewManager(nil, t.TempDir()))
	var refused *database.DefaultDatabaseDropError

	if err := svc.Drop(context.Background(), tenant.DefaultDatabase); !errors.As(err, &refused) {
		t.Fatalf("Drop(%q) error = %v, want *database.DefaultDatabaseDropError", tenant.DefaultDatabase, err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	if err := svc.DropForTenant(ctx, tenant.DefaultDatabase); !errors.As(err, &refused) {
		t.Fatalf("DropForTenant(%q) error = %v, want *database.DefaultDatabaseDropError", tenant.DefaultDatabase, err)
	}
}

// ctxHonoringPlacementKV fails every operation once its context is done, as
// a real replicated KV does.
type ctxHonoringPlacementKV struct {
	*memPlacementKV
}

func (k *ctxHonoringPlacementKV) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return k.memPlacementKV.Put(ctx, key, value)
}

func (k *ctxHonoringPlacementKV) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return k.memPlacementKV.Get(ctx, key)
}

func (k *ctxHonoringPlacementKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return k.memPlacementKV.CompareAndSwap(ctx, key, expected, next)
}

// A drop most often fails because its request context was cancelled. The
// replicated fence's rollback must still run, or the database is refused
// on every node while it looks live.
func TestClearRaftPlacementFence_RunsAfterTheRequestIsCancelled(t *testing.T) {
	store, err := placement.NewStore(&ctxHonoringPlacementKV{memPlacementKV: newMemPlacementKV()})
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, &placement.Placement{
		TenantID: "rt", Database: "orders", ShardID: 7, RF: 1, Dropping: true,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1"}},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc := NewDatabaseService(database.NewManager(nil, t.TempDir()), WithPlacementStore(store)).(*DatabaseService)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	svc.clearRaftPlacementFence(cancelled, "rt", "orders")

	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Dropping {
		t.Fatal("the replicated fence survived the rollback of a cancelled drop")
	}
}

// A failed drop whose store fence could NOT be cleared leaves the database
// fenced in its store, and so locally. Its replicated placement fence must
// stay set as well: clearing it would route every node's traffic to a
// database whose store refuses every write.
func TestDrop_UnclearedStoreFenceKeepsThePlacementFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	svc.manager.SetKeyspaceReclaimer(&alwaysFailReclaimer{
		err:        errors.New("shared store unavailable"),
		unfenceErr: errors.New("store still unavailable"),
	})

	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("DropForTenant with a failing reclaimer = nil error, want the reclaim failure")
	}
	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement entry should survive a failed drop: %v", err)
	}
	if !p.Dropping {
		t.Fatal("the placement fence was cleared although the store fence could not be: the database is routed to while its store refuses writes")
	}
}

// A failed drop that changed nothing -- its local fence could not even be
// persisted -- must roll back the placement fence it owns, or the database
// is refused on every node for a drop that never started.
func TestDrop_LocalFenceWriteFailureRollsBackThePlacementFence(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svcer, store := newPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	svc := svcer.(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	// Remove the database's catalog directory, so persisting the local
	// fence to meta.json fails -- the one failure before anything is fenced.
	if err := os.RemoveAll(filepath.Join(svc.manager.DataDir(), "databases", "orders")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("DropForTenant = nil error, want the local fence write failure")
	}
	p, err := store.Get(ctx, "rt", "orders")
	if err != nil {
		t.Fatalf("placement entry should survive a failed drop: %v", err)
	}
	if p.Dropping {
		t.Fatal("the placement fence of a drop that never started was left set: the database is refused on every node")
	}
}
