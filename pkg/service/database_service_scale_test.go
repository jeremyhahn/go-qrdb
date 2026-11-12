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
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scaleTenantID is the tenant ID used throughout this file's tests.
const scaleTenantID = "scale-tenant"

// scaleRaftTenant returns a fresh IsolationRaft TenantInfo for scale tests.
func scaleRaftTenant(t *testing.T) *tenant.TenantInfo {
	t.Helper()
	return &tenant.TenantInfo{ID: scaleTenantID, IsolationType: tenant.IsolationRaft}
}

// scaleCtx returns a context carrying scaleTenantID.
func scaleCtx() context.Context {
	return tenant.WithTenantID(context.Background(), scaleTenantID)
}

// newScaleDatabaseService builds a DatabaseService wired with placement,
// quota, and (optionally) a RaftHost, suitable for ScaleForTenant tests. It
// returns the concrete service (for db-level assertions), the placement
// store, and the fakeReplicaQuota (for usage-counter assertions).
func newScaleDatabaseService(
	t *testing.T,
	nodes []placement.NodeLoad,
	quotas *quota.Quotas,
	raftHost RaftHost,
) (*DatabaseService, *placement.Store, *fakeReplicaQuota) {
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

	f := &fakeReplicaQuota{q: quotas}
	opts := []DatabaseServiceOption{
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	}
	if raftHost != nil {
		opts = append(opts, WithRaftHost(raftHost))
	}
	svc := NewDatabaseService(mgr, opts...)
	concrete, ok := svc.(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService must return *DatabaseService")
	}
	return concrete, store, f
}

// createScaleDB creates a dedicated-raft database named name with the given
// initial replica count, returning its DatabaseInfo.
func createScaleDB(t *testing.T, svc *DatabaseService, name string, replicas int) *database.DatabaseInfo {
	t.Helper()
	cfg := testDBConfig(name)
	cfg.Replicas = replicas
	info, err := svc.CreateForTenant(scaleCtx(), cfg)
	if err != nil {
		t.Fatalf("CreateForTenant(%s): %v", name, err)
	}
	return info
}

// ---------------------------------------------------------------------------
// Bounds / idempotency
// ---------------------------------------------------------------------------

// TestScaleForTenant_FloorRejection verifies a target below 1 is rejected
// with *ReplicaFloorError and no placement/quota mutation occurs.
func TestScaleForTenant_FloorRejection(t *testing.T) {
	t.Parallel()

	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 0)
	var floorErr *ReplicaFloorError
	if !errors.As(err, &floorErr) {
		t.Fatalf("ScaleForTenant(0) error = %v, want *ReplicaFloorError", err)
	}
	if floorErr.Requested != 0 {
		t.Errorf("floorErr.Requested = %d, want 0", floorErr.Requested)
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d after rejected scale, want unchanged 2", p.RF)
	}
	if f.incTotal != 2 {
		t.Errorf("no additional quota reservation expected, incTotal = %d", f.incTotal)
	}
}

// TestScaleForTenant_NegativeFloorRejection verifies a strictly negative
// target is also rejected with *ReplicaFloorError.
func TestScaleForTenant_NegativeFloorRejection(t *testing.T) {
	t.Parallel()

	svc, _, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 1)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", -3)
	var floorErr *ReplicaFloorError
	if !errors.As(err, &floorErr) {
		t.Fatalf("ScaleForTenant(-3) error = %v, want *ReplicaFloorError", err)
	}
}

// TestScaleForTenant_NoopIdempotent verifies a target equal to the current
// replication factor returns the current DatabaseInfo without touching quota.
func TestScaleForTenant_NoopIdempotent(t *testing.T) {
	t.Parallel()

	svc, _, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 3)

	incTotalBefore := f.incTotal
	info, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if err != nil {
		t.Fatalf("ScaleForTenant (no-op): %v", err)
	}
	if info.Replicas != 3 {
		t.Errorf("info.Replicas = %d, want 3", info.Replicas)
	}
	if f.incTotal != incTotalBefore {
		t.Errorf("no-op scale must not touch quota usage: incTotal before=%d after=%d", incTotalBefore, f.incTotal)
	}
}

// TestScaleForTenant_NotDedicatedRaftDatabase verifies scaling a database
// with no placement entry (e.g. a non-raft database) is rejected with
// *NotDedicatedRaftDatabaseError.
func TestScaleForTenant_NotDedicatedRaftDatabase(t *testing.T) {
	t.Parallel()

	// An IsolationPrefix tenant's isolation switch does not force the raft
	// backend (unlike IsolationRaft), so the database stays on the plain
	// (non-raft) memory backend and never gets a placement entry -- mirroring
	// TestCreateForTenant_Replicas_NonRaftIgnored.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	info := &tenant.TenantInfo{ID: "pf-scale", IsolationType: tenant.IsolationPrefix}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)
	ctx := tenant.WithTenantID(context.Background(), "pf-scale")

	cfg := testDBConfig("plainkv")
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	_, err = svc.ScaleForTenant(ctx, "plainkv", 3)
	var notDedicated *NotDedicatedRaftDatabaseError
	if !errors.As(err, &notDedicated) {
		t.Fatalf("ScaleForTenant error = %v, want *NotDedicatedRaftDatabaseError", err)
	}
}

// TestScaleForTenant_PlacementSubsystemUnavailable verifies scaling fails
// closed with *RaftDistributedUnavailableError when the placement subsystem
// is not wired on this node.
func TestScaleForTenant_PlacementSubsystemUnavailable(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithDatabaseLogger(logger),
		// No WithPlacementStore / WithPlacementScheduler.
	)

	cfg := testDBConfig("db1")
	if _, err := svc.CreateForTenant(scaleCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	var unavailable *RaftDistributedUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("ScaleForTenant error = %v, want *RaftDistributedUnavailableError", err)
	}
}

// ---------------------------------------------------------------------------
// Scale-up: quota enforcement
// ---------------------------------------------------------------------------

// TestScaleForTenant_QuotaRejection_PerGroupCap verifies a scale-up target
// above MaxReplicasPerGroup is rejected with *quota.ExceededError for
// ResourceReplicasPerGroup, and placement/quota usage are left unchanged.
func TestScaleForTenant_QuotaRejection_PerGroupCap(t *testing.T) {
	t.Parallel()

	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3", "n4", "n5"),
		&quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)
	incTotalBefore := f.incTotal

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 5)
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("ScaleForTenant error = %v, want *quota.ExceededError", err)
	}
	if ee.Resource != quota.ResourceReplicasPerGroup {
		t.Errorf("exceeded resource = %v, want ResourceReplicasPerGroup", ee.Resource)
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d after rejected scale-up, want unchanged 2", p.RF)
	}
	if f.incTotal != incTotalBefore {
		t.Errorf("quota usage leaked on rejection: before=%d after=%d", incTotalBefore, f.incTotal)
	}
}

// TestScaleForTenant_QuotaRejection_TotalBudget verifies a scale-up whose
// delta would exceed the aggregate MaxTotalReplicas budget is rejected with
// *quota.ExceededError for ResourceTotalReplicas.
func TestScaleForTenant_QuotaRejection_TotalBudget(t *testing.T) {
	t.Parallel()

	// Per-group cap unlimited (0); aggregate budget 3, already at 2 from the
	// create (RF=2). Scaling to 4 needs +2 more (delta=2), 2+2=4 > 3.
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3", "n4"),
		&quota.Quotas{MaxReplicasPerGroup: 0, MaxTotalReplicas: 3}, nil)
	createScaleDB(t, svc, "db1", 2)
	incTotalBefore := f.incTotal

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 4)
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("ScaleForTenant error = %v, want *quota.ExceededError", err)
	}
	if ee.Resource != quota.ResourceTotalReplicas {
		t.Errorf("exceeded resource = %v, want ResourceTotalReplicas", ee.Resource)
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d after rejected scale-up, want unchanged 2", p.RF)
	}
	if f.incTotal != incTotalBefore {
		t.Errorf("quota usage leaked on rejection: before=%d after=%d", incTotalBefore, f.incTotal)
	}
}

// TestScaleForTenant_QuotaReservationRollback_OnSchedulerFailure verifies
// that when PlaceAdditional fails (insufficient distinct healthy nodes), the
// ResourceTotalReplicas reservation made just before the scheduler call is
// rolled back.
func TestScaleForTenant_QuotaReservationRollback_OnSchedulerFailure(t *testing.T) {
	t.Parallel()

	// Only 2 nodes in the cluster; db1 already occupies both. A scale-up to 3
	// needs 1 more distinct healthy node, which does not exist.
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if !errors.Is(err, placement.ErrInsufficientNodes) {
		t.Fatalf("ScaleForTenant error = %v, want ErrInsufficientNodes", err)
	}

	// The create-time reservation (RF=2) is legitimate and must remain; only
	// the failed scale-up's own delta reservation (+1) must be rolled back,
	// so current usage settles back to exactly 2 -- not incTotal == decTotal
	// (incTotal also includes the create-time +2, which is never decremented
	// since db1 was never dropped).
	cur, cerr := f.q.CurrentUsage(quota.ResourceTotalReplicas)
	if cerr != nil {
		t.Fatalf("CurrentUsage: %v", cerr)
	}
	if cur != 2 {
		t.Errorf("current total-replica usage after rollback = %d, want 2 (unchanged from create)", cur)
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d after failed scale-up, want unchanged 2", p.RF)
	}
}

// ---------------------------------------------------------------------------
// Scale-up: RaftHost / placement / key-delivery ordering
// ---------------------------------------------------------------------------

// stepLog is a mutex-protected append-only log of named steps, shared across
// the key-distributor, placement-store, and RaftHost fakes so a test can
// assert the mandated per-replica ordering: key-delivery -> placement-put ->
// add-node.
type stepLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *stepLog) record(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step)
}

func (l *stepLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.steps))
	copy(out, l.steps)
	return out
}

// orderedKeyDistributor wraps captureDistributor to additionally record a
// "key-delivery" step in the shared log for every call.
type orderedKeyDistributor struct {
	captureDistributor
	log *stepLog
}

func (d *orderedKeyDistributor) DistributeTenantKey(
	ctx context.Context,
	p *placement.Placement,
	lb *seal.StorageBarrier,
	nodeID string,
) error {
	d.log.record("key-delivery")
	return d.captureDistributor.DistributeTenantKey(ctx, p, lb, nodeID)
}

// orderingPlacementKV wraps memPlacementKV to additionally record a
// "placement-put" step in the shared log for every Put call.
type orderingPlacementKV struct {
	*memPlacementKV
	log *stepLog
}

func (k *orderingPlacementKV) Put(ctx context.Context, key string, value []byte) error {
	k.log.record("placement-put")
	return k.memPlacementKV.Put(ctx, key, value)
}

// CompareAndSwap records the identical "placement-put" step as Put:
// addObserverReplica/removeReplicaFromGroup persist every scale-driven
// placement change through CompareAndSwapPlacement now, not a bare Put, so
// the ordering these tests assert (placement before key delivery before the
// Raft membership call) must still be observable from this step log.
func (k *orderingPlacementKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	k.log.record("placement-put")
	return k.memPlacementKV.CompareAndSwap(ctx, key, expected, next)
}

// TestScaleForTenant_ScaleUp_PlacementThenKeyDeliveryThenAddNodeOrdering
// verifies the per-new-replica step ordering: the placement update is
// persisted BEFORE the tenant barrier key is delivered, which in turn
// happens before the Raft membership AddObserver call (scale-up admits a
// new replica as a non-voting observer, never directly as a voter -- see
// database_service.go's scaleUp). Placement must come first because the
// receiver's placement gate
// (pkg/clusterkeys/receiver.go) only authorizes a delivery once its own copy
// of the replicated placement table lists it as the assigned replica --
// delivering the key before the entry is ever persisted anywhere means the
// receiver rejects every attempt unconditionally (this ordering was
// previously reversed, a real bug: see database_service.go's scaleUp).
func TestScaleForTenant_ScaleUp_PlacementThenKeyDeliveryThenAddNodeOrdering(t *testing.T) {
	t.Parallel()

	log := &stepLog{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	store, err := placement.NewStore(&orderingPlacementKV{memPlacementKV: newMemPlacementKV(), log: log})
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	// "node1" always sorts first (least-loaded tie-break on NodeID), so it is
	// selected as the leader -- mirroring nodeAddNodes()/leaderNodeID in
	// database_service_node_add_test.go.
	nodes := healthyNodes("node1", "node2")
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}

	host := &fakeRaftHost{leaderOK: true, onObserver: func() { log.record("add-node") }}
	dist := &orderedKeyDistributor{log: log}
	tb := newTestTenantBarrier(t, scaleTenantID)
	lookup := &fakeTenantBarrierLookup{tb: tb}

	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithKeyDistributor(dist),
		WithBarrierLookup(lookup),
		WithLocalNodeID(leaderNodeID),
		WithRaftHost(host),
	).(*DatabaseService)

	cfg := testDBConfig("db1")
	cfg.Replicas = 1
	if _, err := svc.CreateForTenant(scaleCtx(), cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	// The create-time placement-put and (skipped, single-replica) steps are
	// irrelevant to this assertion; reset the log before scaling.
	log.mu.Lock()
	log.steps = nil
	log.mu.Unlock()

	if _, err := svc.ScaleForTenant(scaleCtx(), "db1", 2); err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}

	got := log.snapshot()
	if len(got) != 3 {
		t.Fatalf("step log = %v, want exactly 3 steps", got)
	}
	if got[0] != "placement-put" || got[1] != "key-delivery" || got[2] != "add-node" {
		t.Errorf("step order = %v, want [placement-put key-delivery add-node]", got)
	}
}

// TestScaleForTenant_ScaleUp_AddObserverCalledForNewReplicas verifies
// AddObserver (never AddFollowerNode) is called exactly once per newly
// added replica, with the correct shardID/replicaID/raftAddr, and that the
// manager's replica count is updated afterward. Each new replica is admitted
// as a non-voting observer so scale-up never transiently breaks the group's
// existing voting quorum (see database_service.go's scaleUp).
func TestScaleForTenant_ScaleUp_AddObserverCalledForNewReplicas(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderOK: true}
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3", "n4", "n5"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	info := createScaleDB(t, svc, "db1", 2)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 5)
	if err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if out.Replicas != 5 {
		t.Errorf("out.Replicas = %d, want 5", out.Replicas)
	}

	if len(host.calls) != 0 {
		t.Errorf("AddFollowerNode called %d times, want 0 (scale-up must never call AddFollowerNode)", len(host.calls))
	}
	if len(host.observerCalls) != 3 {
		t.Fatalf("AddObserver called %d times, want 3", len(host.observerCalls))
	}
	for _, c := range host.observerCalls {
		if c.shardID != info.ShardID {
			t.Errorf("call.shardID = %d, want %d", c.shardID, info.ShardID)
		}
		if c.replicaID <= 2 {
			t.Errorf("call.replicaID = %d, want > 2 (new replicas only)", c.replicaID)
		}
		if c.raftAddr == "" {
			t.Error("call.raftAddr must not be empty")
		}
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 5 || len(p.Replicas) != 5 {
		t.Errorf("placement RF=%d replicas=%d, want 5/5", p.RF, len(p.Replicas))
	}
	for _, r := range p.Replicas {
		if r.ReplicaID > 2 && r.State != placement.ReplicaStateObserver {
			t.Errorf("new replica %+v State = %v, want ReplicaStateObserver", r, r.State)
		}
	}
	if f.incTotal != 2+3 {
		t.Errorf("incTotal = %d, want 5 (2 from create + 3 from scale)", f.incTotal)
	}
}

// TestScaleForTenant_ScaleUp_AddObserverErrorFailsCall verifies that a
// failing AddObserver fails the whole scale-up call: unlike key delivery
// (which the follower reconciler retries independently), nothing else in
// the system retries a config change RequestAddObserver itself reported as
// failed, so silently continuing would return a false success to the caller
// while the replica never actually joined the group (mirrors scale-down's
// identical RemoveFollowerNode-is-fatal behavior). The placement-put for the
// failed replica has already been persisted by the time AddObserver is
// attempted (placement-then-membership ordering), so it stays in place for
// a retry to pick up; the unconsumed portion of the quota reservation (this
// replica was never counted "added") is rolled back.
func TestScaleForTenant_ScaleUp_AddObserverErrorFailsCall(t *testing.T) {
	t.Parallel()

	addErr := errors.New("shard not yet started")
	host := &fakeRaftHost{leaderOK: true, observerErr: addErr}
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	createScaleDB(t, svc, "db1", 2)
	decTotalBefore := f.decTotal

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if !errors.Is(err, addErr) {
		t.Fatalf("ScaleForTenant error = %v, want it to wrap %v", err, addErr)
	}
	if len(host.observerCalls) != 1 {
		t.Errorf("expected 1 AddObserver attempt, got %d", len(host.observerCalls))
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 3 {
		t.Errorf("placement RF = %d, want 3 (placement-put already persisted before the failed AddObserver call)", p.RF)
	}
	if f.decTotal != decTotalBefore+1 {
		t.Errorf("unconsumed quota reservation not rolled back: decTotal before=%d after=%d", decTotalBefore, f.decTotal)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round 3, finding 5: a snapshot read after the fence was set
// must never let a scale mutate quota or Raft membership for a database
// that is mid-drop.
// ---------------------------------------------------------------------------

// TestScaleForTenant_RejectsDroppingPlacement proves ScaleForTenant refuses
// with *database.DroppingError, before any quota reservation or placement
// mutation, when the placement entry it resolves already has Dropping set.
func TestScaleForTenant_RejectsDroppingPlacement(t *testing.T) {
	t.Parallel()

	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}}
	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"), f.q, nil)
	created := createScaleDB(t, svc, "db1", 2)

	if err := store.SetDropping(scaleCtx(), scaleTenantID, created.ID, true); err != nil {
		t.Fatalf("SetDropping: %v", err)
	}
	incTotalBefore := f.incTotal

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("ScaleForTenant error = %v (%T), want *database.DroppingError", err, err)
	}
	if f.incTotal != incTotalBefore {
		t.Errorf("quota reservation attempted (incTotal %d -> %d), want untouched", incTotalBefore, f.incTotal)
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d, want 2 (unchanged)", p.RF)
	}
}

// TestScaleForTenant_ScaleUp_SkippedWhenRaftHostNil verifies scale-up
// succeeds (placement + quota + meta updated) when no RaftHost is wired.
func TestScaleForTenant_ScaleUp_SkippedWhenRaftHostNil(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if out.Replicas != 3 {
		t.Errorf("out.Replicas = %d, want 3", out.Replicas)
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 3 {
		t.Errorf("placement RF = %d, want 3", p.RF)
	}
}

// ---------------------------------------------------------------------------
// Scale-down
// ---------------------------------------------------------------------------

// TestScaleForTenant_ScaleDown_RemovesReplicasAndDecrementsQuota verifies a
// scale-down calls RemoveFollowerNode for each evicted replica, updates the
// placement and manager replica count, and decrements ResourceTotalReplicas
// usage by the number actually removed.
func TestScaleForTenant_ScaleDown_RemovesReplicasAndDecrementsQuota(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderOK: true}
	svc, store, f := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3", "n4", "n5"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	info := createScaleDB(t, svc, "db1", 5)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 2)
	if err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if out.Replicas != 2 {
		t.Errorf("out.Replicas = %d, want 2", out.Replicas)
	}
	if len(host.removeCalls) != 3 {
		t.Fatalf("RemoveFollowerNode called %d times, want 3", len(host.removeCalls))
	}
	for _, c := range host.removeCalls {
		if c.shardID != info.ShardID {
			t.Errorf("removeCall.shardID = %d, want %d", c.shardID, info.ShardID)
		}
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 || len(p.Replicas) != 2 {
		t.Errorf("placement RF=%d replicas=%d, want 2/2", p.RF, len(p.Replicas))
	}
	if f.decTotal != 3 {
		t.Errorf("decTotal = %d, want 3", f.decTotal)
	}
}

// countingChecker wraps a quota.Checker and counts Check/CheckN invocations,
// so a test can assert quota enforcement is (or is not) consulted at all,
// independent of whether the underlying check would pass or fail.
type countingChecker struct {
	inner       quota.Checker
	checkCalls  int
	checkNCalls int
}

func (c *countingChecker) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	c.checkCalls++
	return c.inner.Check(ctx, tenantID, resource)
}

func (c *countingChecker) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	c.checkNCalls++
	return c.inner.CheckN(ctx, tenantID, resource, n)
}

func (c *countingChecker) Usage(ctx context.Context, tenantID string, resource quota.Resource) (int64, error) {
	return c.inner.Usage(ctx, tenantID, resource)
}

// TestScaleForTenant_ScaleDown_NoQuotaCheck verifies a scale-down never
// consults the quota.Checker at all (freeing capacity never violates a cap,
// per the design's §A.1.3 "no quota check needed" rule) -- distinct from
// merely happening to pass a check with a generous cap.
func TestScaleForTenant_ScaleDown_NoQuotaCheck(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2", "n3")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}}
	counting := &countingChecker{inner: newReplicaQuotaChecker(t, f)}
	host := &fakeRaftHost{leaderOK: true}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(counting),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithRaftHost(host),
	).(*DatabaseService)

	createScaleDB(t, svc, "db1", 3)
	checkNCallsAfterCreate := counting.checkNCalls

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	if err != nil {
		t.Fatalf("ScaleForTenant (scale-down): %v", err)
	}
	if out.Replicas != 1 {
		t.Errorf("out.Replicas = %d, want 1", out.Replicas)
	}
	if counting.checkNCalls != checkNCallsAfterCreate {
		t.Errorf("scale-down called CheckN %d additional time(s), want 0",
			counting.checkNCalls-checkNCallsAfterCreate)
	}
}

// TestScaleForTenant_ScaleDown_RemoveFollowerNodeErrorFailsCall verifies a
// RemoveFollowerNode failure fails the whole ScaleForTenant call (mirrors
// scale-up's AddObserver failure handling).
func TestScaleForTenant_ScaleDown_RemoveFollowerNodeErrorFailsCall(t *testing.T) {
	t.Parallel()

	removeErr := errors.New("config change rejected")
	host := &fakeRaftHost{leaderOK: true, removeErr: removeErr}
	svc, _, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	createScaleDB(t, svc, "db1", 3)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	if !errors.Is(err, removeErr) {
		t.Fatalf("ScaleForTenant error = %v, want it to wrap %v", err, removeErr)
	}
	if len(host.removeCalls) != 1 {
		t.Errorf("expected 1 RemoveFollowerNode attempt, got %d", len(host.removeCalls))
	}
}

// TestScaleForTenant_ScaleDown_SkippedWhenRaftHostNil verifies scale-down
// still updates placement/meta when no RaftHost is wired (no
// RemoveFollowerNode call attempted).
func TestScaleForTenant_ScaleDown_SkippedWhenRaftHostNil(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 3)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	if err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if out.Replicas != 1 {
		t.Errorf("out.Replicas = %d, want 1", out.Replicas)
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 1 {
		t.Errorf("placement RF = %d, want 1", p.RF)
	}
}

// ---------------------------------------------------------------------------
// selectReplicasToRemove
// ---------------------------------------------------------------------------

func TestSelectReplicasToRemove(t *testing.T) {
	t.Parallel()

	replicas := func(ids ...uint64) []placement.Replica {
		out := make([]placement.Replica, len(ids))
		for i, id := range ids {
			out[i] = placement.Replica{ReplicaID: id, NodeID: "n", RaftAddr: "n:1"}
		}
		return out
	}

	tests := []struct {
		name            string
		replicaIDs      []uint64
		removeCount     int
		leaderReplicaID uint64
		wantIDs         []uint64 // exact ordered slice of removed IDs
	}{
		{
			name:            "descending order, no leader match",
			replicaIDs:      []uint64{1, 2, 3, 4, 5},
			removeCount:     2,
			leaderReplicaID: 0,
			wantIDs:         []uint64{5, 4},
		},
		{
			name:            "leader is highest ID, skipped until forced",
			replicaIDs:      []uint64{1, 2, 3},
			removeCount:     2,
			leaderReplicaID: 3,
			// Descending non-leader order is [2, 1]; removeCount=2 exhausts
			// them without ever reaching the leader.
			wantIDs: []uint64{2, 1},
		},
		{
			name:            "leader is lowest ID, still last",
			replicaIDs:      []uint64{1, 2, 3},
			removeCount:     2,
			leaderReplicaID: 1,
			wantIDs:         []uint64{3, 2},
		},
		{
			name:            "leader is middle ID, still last",
			replicaIDs:      []uint64{1, 2, 3},
			removeCount:     2,
			leaderReplicaID: 2,
			wantIDs:         []uint64{3, 1},
		},
		{
			name:            "removing everything puts the leader last in the ordering",
			replicaIDs:      []uint64{1, 2, 3},
			removeCount:     3,
			leaderReplicaID: 2,
			wantIDs:         []uint64{3, 1, 2},
		},
		{
			name:            "removeCount clamped to replica count",
			replicaIDs:      []uint64{1, 2},
			removeCount:     10,
			leaderReplicaID: 1,
			wantIDs:         []uint64{2, 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &placement.Placement{Replicas: replicas(tt.replicaIDs...)}
			got := selectReplicasToRemove(p, tt.removeCount, tt.leaderReplicaID)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("selectReplicasToRemove returned %d replicas, want %d", len(got), len(tt.wantIDs))
			}
			for i, r := range got {
				if r.ReplicaID != tt.wantIDs[i] {
					t.Errorf("got[%d].ReplicaID = %d, want %d (full got=%v, want=%v)",
						i, r.ReplicaID, tt.wantIDs[i], idsOf(got), tt.wantIDs)
				}
			}
		})
	}
}

// idsOf extracts the ReplicaID sequence from a []placement.Replica, used for
// readable table-test failure messages.
func idsOf(rs []placement.Replica) []uint64 {
	out := make([]uint64, len(rs))
	for i, r := range rs {
		out[i] = r.ReplicaID
	}
	return out
}

// ---------------------------------------------------------------------------
// deliverTenantKeyToReplicaWithRetry
// ---------------------------------------------------------------------------

// errFlakyKeyDistributor is returned by flakyKeyDistributor's configured
// failures, mirroring clusterkeys.Sender.Deliver's single collapsed
// ErrDelivery sentinel (deliverTenantKeyToReplicaWithRetry cannot and does
// not attempt to distinguish a transient placement-gate race from a
// permanent failure -- see its doc comment).
var errFlakyKeyDistributor = errors.New("flakyKeyDistributor: delivery failed")

// flakyKeyDistributor fails the first failCount calls to DistributeTenantKey,
// then succeeds. If cancel is non-nil it is invoked after recording the
// FIRST call, letting tests assert that a cancelled context aborts the
// retry loop early instead of exhausting every attempt.
type flakyKeyDistributor struct {
	failCount int
	calls     int
	cancel    context.CancelFunc
}

func (f *flakyKeyDistributor) DistributeTenantKey(_ context.Context, _ *placement.Placement, _ *seal.StorageBarrier, _ string) error {
	f.calls++
	if f.cancel != nil && f.calls == 1 {
		f.cancel()
	}
	if f.calls <= f.failCount {
		return errFlakyKeyDistributor
	}
	return nil
}

// TestDeliverTenantKeyToReplicaWithRetry_SucceedsAfterTransientFailures
// verifies the retry loop converges once the underlying KeyDistributor
// starts succeeding, within the bound scaleUpKeyDeliveryMaxAttempts allows.
func TestDeliverTenantKeyToReplicaWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()

	dist := &flakyKeyDistributor{failCount: scaleUpKeyDeliveryMaxAttempts - 1}
	lookup := &fakeTenantBarrierLookup{tb: newTestTenantBarrier(t, scaleTenantID)}
	p := &placement.Placement{TenantID: scaleTenantID, DatabaseID: "db1", Database: "db1", ShardID: 1000}
	dst := placement.Replica{ReplicaID: 2, NodeID: "node2", RaftAddr: "node2:1"}

	err := deliverTenantKeyToReplicaWithRetry(scaleCtx(), dist, lookup, p, dst, "node1")
	if err != nil {
		t.Fatalf("deliverTenantKeyToReplicaWithRetry error = %v, want nil after eventual success", err)
	}
	if dist.calls != scaleUpKeyDeliveryMaxAttempts {
		t.Errorf("dist.calls = %d, want %d (fails on every attempt but the last)", dist.calls, scaleUpKeyDeliveryMaxAttempts)
	}
}

// TestDeliverTenantKeyToReplicaWithRetry_GivesUpAfterMaxAttempts verifies
// that a KeyDistributor which never succeeds is retried exactly
// scaleUpKeyDeliveryMaxAttempts times and the final error is returned (so
// scaleUp's existing non-fatal Warn-log behavior is unchanged).
func TestDeliverTenantKeyToReplicaWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	dist := &flakyKeyDistributor{failCount: scaleUpKeyDeliveryMaxAttempts + 5}
	lookup := &fakeTenantBarrierLookup{tb: newTestTenantBarrier(t, scaleTenantID)}
	p := &placement.Placement{TenantID: scaleTenantID, DatabaseID: "db1", Database: "db1", ShardID: 1000}
	dst := placement.Replica{ReplicaID: 2, NodeID: "node2", RaftAddr: "node2:1"}

	err := deliverTenantKeyToReplicaWithRetry(scaleCtx(), dist, lookup, p, dst, "node1")
	if !errors.Is(err, errFlakyKeyDistributor) {
		t.Fatalf("deliverTenantKeyToReplicaWithRetry error = %v, want errFlakyKeyDistributor", err)
	}
	if dist.calls != scaleUpKeyDeliveryMaxAttempts {
		t.Errorf("dist.calls = %d, want exactly %d (bounded retry)", dist.calls, scaleUpKeyDeliveryMaxAttempts)
	}
}

// TestDeliverTenantKeyToReplicaWithRetry_ContextCancellationAbortsEarly
// verifies that a context cancelled mid-retry stops the loop immediately
// (returning the last delivery error) rather than sleeping out the full
// retry budget.
func TestDeliverTenantKeyToReplicaWithRetry_ContextCancellationAbortsEarly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(scaleCtx())
	dist := &flakyKeyDistributor{failCount: scaleUpKeyDeliveryMaxAttempts + 5, cancel: cancel}
	lookup := &fakeTenantBarrierLookup{tb: newTestTenantBarrier(t, scaleTenantID)}
	p := &placement.Placement{TenantID: scaleTenantID, DatabaseID: "db1", Database: "db1", ShardID: 1000}
	dst := placement.Replica{ReplicaID: 2, NodeID: "node2", RaftAddr: "node2:1"}

	err := deliverTenantKeyToReplicaWithRetry(ctx, dist, lookup, p, dst, "node1")
	if err == nil {
		t.Fatal("deliverTenantKeyToReplicaWithRetry error = nil, want non-nil after cancellation")
	}
	if dist.calls != 1 {
		t.Errorf("dist.calls = %d, want exactly 1 (retry loop must abort on the first post-failure cancellation check)", dist.calls)
	}
}

// ---------------------------------------------------------------------------
// Leader routing (resolveScaleLeader / cross-node forwarding)
// ---------------------------------------------------------------------------

// newScaleDatabaseServiceWithLocalNode is newScaleDatabaseService plus a
// localNodeID. newScaleDatabaseService never sets one, which keeps
// resolveScaleLeader's leader-routing gate a no-op (raftHost == nil ||
// localNodeID == "") for every other test in this file; these tests need
// the gate active.
func newScaleDatabaseServiceWithLocalNode(
	t *testing.T,
	nodes []placement.NodeLoad,
	quotas *quota.Quotas,
	raftHost RaftHost,
	localNodeID string,
) (*DatabaseService, *placement.Store, *fakeReplicaQuota) {
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

	f := &fakeReplicaQuota{q: quotas}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithLocalNodeID(localNodeID),
		WithRaftHost(raftHost),
	)
	concrete, ok := svc.(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService must return *DatabaseService")
	}
	return concrete, store, f
}

// TestScaleForTenant_NotLeader_LocalNodeNotMember verifies that when the
// local node hosts no replica of the dedicated group at all (the RF=1 bug
// this feature fixes: an RF=1 placement's sole replica landed on a
// different node than the one handling the API request), ScaleForTenant
// returns *NotLeaderError pointing at a placement member instead of calling
// raftHost -- which would otherwise fail with "shard not found" -- or
// mutating quota/placement.
func TestScaleForTenant_NotLeader_LocalNodeNotMember(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	// "node1" is the only node in the topology at create time, so RF=1
	// places the sole replica there. localNodeID "node2" simulates the
	// scale request landing on a node that never hosted this group's
	// replica.
	svc, store, f := newScaleDatabaseServiceWithLocalNode(t, healthyNodes("node1"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, host, "node2")
	createScaleDB(t, svc, "db1", 1)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 2)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("ScaleForTenant error = %v, want *NotLeaderError", err)
	}
	if nle.LeaderAddress != "node1:1" {
		t.Errorf("nle.LeaderAddress = %q, want %q", nle.LeaderAddress, "node1:1")
	}

	// Neither raftHost nor quota/placement must have been touched.
	if len(host.calls) != 0 || len(host.observerCalls) != 0 || len(host.removeCalls) != 0 {
		t.Errorf("raftHost must not be called when not the leader: %d add, %d observer, %d remove",
			len(host.calls), len(host.observerCalls), len(host.removeCalls))
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 1 {
		t.Errorf("placement RF = %d after not-leader rejection, want unchanged 1", p.RF)
	}
	if f.incTotal != 1 {
		t.Errorf("no additional quota reservation expected, incTotal = %d", f.incTotal)
	}
}

// TestScaleForTenant_NotLeader_LocalIsMemberButNotLeader verifies that when
// the local node hosts a replica of the dedicated group but raftHost
// reports a different replica as the live Raft leader, ScaleForTenant
// returns *NotLeaderError pointing at that leader's RaftAddr (resolved from
// the placement table), not the local node's own address, and does not
// invoke any membership change.
func TestScaleForTenant_NotLeader_LocalIsMemberButNotLeader(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseServiceWithLocalNode(t, healthyNodes("node1", "node2"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, nil, "node2")
	createScaleDB(t, svc, "db1", 2)

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	var localReplicaID, otherReplicaID uint64
	var otherAddr string
	for _, r := range p.Replicas {
		if r.NodeID == "node2" {
			localReplicaID = r.ReplicaID
		} else {
			otherReplicaID = r.ReplicaID
			otherAddr = r.RaftAddr
		}
	}
	if localReplicaID == 0 || otherReplicaID == 0 {
		t.Fatalf("expected both replicas assigned, got local=%d other=%d", localReplicaID, otherReplicaID)
	}

	host := &fakeRaftHost{leaderID: otherReplicaID, leaderOK: true}
	svc.SetRaftHost(host)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	var nle *NotLeaderError
	if !errors.As(err, &nle) {
		t.Fatalf("ScaleForTenant error = %v, want *NotLeaderError", err)
	}
	if nle.LeaderAddress != otherAddr {
		t.Errorf("nle.LeaderAddress = %q, want %q", nle.LeaderAddress, otherAddr)
	}
	if len(host.calls) != 0 || len(host.observerCalls) != 0 {
		t.Errorf("raftHost.AddObserver must not be called when not the leader, got %d add / %d observer calls",
			len(host.calls), len(host.observerCalls))
	}
}

// TestScaleForTenant_ExecutesLocally_WhenLocalIsLiveLeader verifies that
// when raftHost.LeaderReplicaID confirms the local node is the current
// leader, ScaleForTenant executes the membership change locally instead of
// returning *NotLeaderError.
func TestScaleForTenant_ExecutesLocally_WhenLocalIsLiveLeader(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseServiceWithLocalNode(t, healthyNodes("node1", "node2"),
		&quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}, nil, "node1")
	createScaleDB(t, svc, "db1", 2)

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	var localReplicaID uint64
	for _, r := range p.Replicas {
		if r.NodeID == "node1" {
			localReplicaID = r.ReplicaID
		}
	}
	if localReplicaID == 0 {
		t.Fatalf("expected local replica assigned, got 0")
	}

	host := &fakeRaftHost{leaderID: localReplicaID, leaderOK: true}
	svc.SetRaftHost(host)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	if err != nil {
		t.Fatalf("ScaleForTenant must execute locally for the confirmed leader, got: %v", err)
	}
	if out.Replicas != 1 {
		t.Errorf("out.Replicas = %d, want 1", out.Replicas)
	}
	if len(host.removeCalls) != 1 {
		t.Errorf("RemoveFollowerNode called %d times, want 1", len(host.removeCalls))
	}
}

// ---------------------------------------------------------------------------
// Bug 1: placement (not the per-node singleton manager) is the authority
// ---------------------------------------------------------------------------

// TestScaleForTenant_ManagerMissesDB_PlacementIsAuthoritative verifies
// ScaleForTenant succeeds when the LOCAL per-node manager singleton has no
// entry for the database at all -- the normal, expected situation when a
// scale request is forwarded to the dedicated group's current Raft leader,
// which very often is not the node that originally ran CreateForTenant.
// Placement is persisted directly here (bypassing CreateForTenant
// entirely) to simulate exactly that node.
func TestScaleForTenant_ManagerMissesDB_PlacementIsAuthoritative(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)

	plan, err := svc.placementScheduler.Place(scaleCtx(), placement.PlaceRequest{
		TenantID:   scaleTenantID,
		DatabaseID: "db1",
		Database:   "db1",
		ShardID:    4242,
		RF:         2,
	})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if err := store.Put(scaleCtx(), plan); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	if _, gerr := svc.manager.GetForTenant(scaleCtx(), "db1"); gerr == nil {
		t.Fatal("test setup invalid: the local manager must NOT have db1 registered")
	}

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if err != nil {
		t.Fatalf("ScaleForTenant must succeed sourcing truth from placement alone, got: %v", err)
	}
	if out.Replicas != 3 {
		t.Errorf("out.Replicas = %d, want 3", out.Replicas)
	}
	if out.Backend != database.BackendRaft {
		t.Errorf("out.Backend = %q, want %q", out.Backend, database.BackendRaft)
	}
	if out.ShardID != 4242 {
		t.Errorf("out.ShardID = %d, want 4242", out.ShardID)
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 3 {
		t.Errorf("placement RF = %d, want 3", p.RF)
	}
}

// TestScaleForTenant_ManagerMissesDB_ScaleDownAlsoWorks is the scale-down
// counterpart of TestScaleForTenant_ManagerMissesDB_PlacementIsAuthoritative.
func TestScaleForTenant_ManagerMissesDB_ScaleDownAlsoWorks(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)

	plan, err := svc.placementScheduler.Place(scaleCtx(), placement.PlaceRequest{
		TenantID:   scaleTenantID,
		DatabaseID: "db1",
		Database:   "db1",
		ShardID:    4243,
		RF:         3,
	})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if err := store.Put(scaleCtx(), plan); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	if err != nil {
		t.Fatalf("ScaleForTenant must succeed sourcing truth from placement alone, got: %v", err)
	}
	if out.Replicas != 1 {
		t.Errorf("out.Replicas = %d, want 1", out.Replicas)
	}
}

// ---------------------------------------------------------------------------
// Bug 2: leader-wait before a membership change
// ---------------------------------------------------------------------------

// flakyLeaderRaftHost wraps fakeRaftHost so LeaderReplicaID reports "unknown"
// for the first readyAfter-1 calls and only then delegates to the embedded
// fake's configured leaderID/leaderOK -- simulating a shard whose sole
// replica has not yet campaigned and won its own single-voter election at
// the instant ScaleForTenant executes.
type flakyLeaderRaftHost struct {
	*fakeRaftHost
	readyAfter  int
	leaderCalls int
}

func (h *flakyLeaderRaftHost) LeaderReplicaID(ctx context.Context, shardID uint64) (uint64, bool, error) {
	h.leaderCalls++
	if h.leaderCalls < h.readyAfter {
		return 0, false, nil
	}
	return h.fakeRaftHost.LeaderReplicaID(ctx, shardID)
}

// newFlakyLeaderScaleService builds a DatabaseService wired exactly like
// newScaleDatabaseService, but with a short, test-friendly
// WithScaleLeaderWaitTimeout/WithScaleLeaderWaitInterval so a test exercising
// the wait (or timeout) path never blocks for the 10s production default.
func newFlakyLeaderScaleService(
	t *testing.T,
	nodes []placement.NodeLoad,
	quotas *quota.Quotas,
	raftHost RaftHost,
) (*DatabaseService, *placement.Store, *fakeReplicaQuota) {
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

	f := &fakeReplicaQuota{q: quotas}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: scaleRaftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithRaftHost(raftHost),
		WithScaleLeaderWaitTimeout(2*time.Second),
		WithScaleLeaderWaitInterval(5*time.Millisecond),
	).(*DatabaseService)
	return svc, store, f
}

// TestScaleForTenant_ScaleUp_WaitsForLeaderBeforeAddObserver verifies
// scaleUp polls until the shard reports a known leader (simulating the RF=1
// post-election-not-yet-settled window) before calling AddObserver, and
// then succeeds once the election completes.
func TestScaleForTenant_ScaleUp_WaitsForLeaderBeforeAddObserver(t *testing.T) {
	t.Parallel()

	host := &flakyLeaderRaftHost{fakeRaftHost: &fakeRaftHost{leaderOK: true, leaderID: 1}, readyAfter: 4}
	svc, store, _ := newFlakyLeaderScaleService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	createScaleDB(t, svc, "db1", 2)

	out, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	if err != nil {
		t.Fatalf("ScaleForTenant: %v", err)
	}
	if out.Replicas != 3 {
		t.Errorf("out.Replicas = %d, want 3", out.Replicas)
	}
	if host.leaderCalls < host.readyAfter {
		t.Errorf("leaderCalls = %d, want >= %d (must poll until the leader becomes known)",
			host.leaderCalls, host.readyAfter)
	}
	if len(host.observerCalls) != 1 {
		t.Errorf("AddObserver calls = %d, want 1 (only after the leader became known)", len(host.observerCalls))
	}

	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 3 {
		t.Errorf("placement RF = %d, want 3", p.RF)
	}
}

// TestScaleForTenant_ScaleUp_LeaderNeverKnownTimesOut verifies that when the
// shard never reports a known leader, scaleUp fails closed with
// *ShardLeaderTimeoutError without ever calling AddObserver or mutating
// quota/placement.
func TestScaleForTenant_ScaleUp_LeaderNeverKnownTimesOut(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderOK: false}
	svc, store, f := newFlakyLeaderScaleService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	svc.scaleLeaderWaitTimeout = 30 * time.Millisecond
	svc.scaleLeaderWaitInterval = 5 * time.Millisecond
	createScaleDB(t, svc, "db1", 2)
	incTotalBefore := f.incTotal

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 3)
	var timeoutErr *ShardLeaderTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("ScaleForTenant error = %v, want *ShardLeaderTimeoutError", err)
	}
	if len(host.observerCalls) != 0 {
		t.Errorf("AddObserver must not be called when the leader never becomes known, got %d calls", len(host.observerCalls))
	}
	if f.incTotal != incTotalBefore {
		t.Errorf("no quota reservation expected on leader-wait timeout, incTotal before=%d after=%d",
			incTotalBefore, f.incTotal)
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 2 {
		t.Errorf("placement RF = %d after leader-wait timeout, want unchanged 2", p.RF)
	}
}

// TestScaleForTenant_ScaleDown_LeaderNeverKnownTimesOut is the scale-down
// counterpart of TestScaleForTenant_ScaleUp_LeaderNeverKnownTimesOut.
func TestScaleForTenant_ScaleDown_LeaderNeverKnownTimesOut(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderOK: false}
	svc, store, _ := newFlakyLeaderScaleService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, host)
	svc.scaleLeaderWaitTimeout = 30 * time.Millisecond
	svc.scaleLeaderWaitInterval = 5 * time.Millisecond
	createScaleDB(t, svc, "db1", 3)

	_, err := svc.ScaleForTenant(scaleCtx(), "db1", 1)
	var timeoutErr *ShardLeaderTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("ScaleForTenant error = %v, want *ShardLeaderTimeoutError", err)
	}
	if len(host.removeCalls) != 0 {
		t.Errorf("RemoveFollowerNode must not be called when the leader never becomes known, got %d calls", len(host.removeCalls))
	}
	p, gerr := store.GetByName(scaleCtx(), scaleTenantID, "db1")
	if gerr != nil {
		t.Fatalf("placement Get: %v", gerr)
	}
	if p.RF != 3 {
		t.Errorf("placement RF = %d after leader-wait timeout, want unchanged 3", p.RF)
	}
}

// ---------------------------------------------------------------------------
// GetForTenant / ListForTenant: RF sourced from placement, not stale manager
// meta (the elastic-scaling design's GetForTenant-consistency requirement)
// ---------------------------------------------------------------------------

// bumpPlacementRF loads the placement entry for (scaleTenantID, name),
// overwrites RF directly (bypassing ScaleForTenant and, therefore, the local
// manager's catalog record), and persists it -- simulating a scale that executed
// on a different node's placement view, exactly as a real ScaleForTenant
// call forwarded to the dedicated group's leader would leave this node's own
// manager meta.
func bumpPlacementRF(t *testing.T, store *placement.Store, name string, rf int) {
	t.Helper()
	p, err := store.GetByName(scaleCtx(), scaleTenantID, name)
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	updated := *p
	updated.RF = rf
	if err := store.Put(scaleCtx(), &updated); err != nil {
		t.Fatalf("placement Put: %v", err)
	}
}

// TestGetForTenant_ReplicasSourcedFromPlacementWhenManagerStale verifies
// GetForTenant reports the placement table's RF, not local manager meta,
// once a placement entry exists -- the manager entry here is deliberately
// left stale by mutating placement directly instead of going through
// ScaleForTenant.
func TestGetForTenant_ReplicasSourcedFromPlacementWhenManagerStale(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)
	bumpPlacementRF(t, store, "db1", 5)

	info, err := svc.GetForTenant(scaleCtx(), "db1")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if info.Replicas != 5 {
		t.Errorf("info.Replicas = %d, want 5 (sourced from placement, not stale manager meta)", info.Replicas)
	}
}

// TestListForTenant_ReplicasSourcedFromPlacementWhenManagerStale is the
// ListForTenant counterpart of
// TestGetForTenant_ReplicasSourcedFromPlacementWhenManagerStale.
func TestListForTenant_ReplicasSourcedFromPlacementWhenManagerStale(t *testing.T) {
	t.Parallel()

	svc, store, _ := newScaleDatabaseService(t, healthyNodes("n1", "n2", "n3"),
		&quota.Quotas{MaxReplicasPerGroup: 10, MaxTotalReplicas: 100}, nil)
	createScaleDB(t, svc, "db1", 2)
	bumpPlacementRF(t, store, "db1", 4)

	infos, err := svc.ListForTenant(scaleCtx())
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	var found bool
	for _, info := range infos {
		if info.Name != "db1" {
			continue
		}
		found = true
		if info.Replicas != 4 {
			t.Errorf("info.Replicas = %d, want 4 (sourced from placement, not stale manager meta)", info.Replicas)
		}
	}
	if !found {
		t.Fatal("db1 not found in ListForTenant results")
	}
}

// TestGetForTenant_NonRaftDatabase_ReplicasUntouchedByPlacementLookup
// verifies refreshReplicasFromPlacement is a no-op for a non-raft database
// (no placement entry ever exists for it): GetForTenant must not error and
// must return the manager-sourced Replicas (0) unchanged.
func TestGetForTenant_NonRaftDatabase_ReplicasUntouchedByPlacementLookup(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	info := &tenant.TenantInfo{ID: "pf-get-scale", IsolationType: tenant.IsolationPrefix}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	).(*DatabaseService)
	ctx := tenant.WithTenantID(context.Background(), "pf-get-scale")

	if _, err := svc.CreateForTenant(ctx, testDBConfig("plainkv")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	got, err := svc.GetForTenant(ctx, "plainkv")
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if got.Replicas != 0 {
		t.Errorf("info.Replicas = %d, want 0 (non-raft database, no placement entry)", got.Replicas)
	}
}

// ---------------------------------------------------------------------------
// transferLeadershipAwayFromRemoved
// ---------------------------------------------------------------------------

// transferPlacement returns a 2-replica placement (replica 1 Active, replica
// 2 Active) for shard 500, used by transferLeadershipAwayFromRemoved tests.
func transferPlacement() *placement.Placement {
	return &placement.Placement{
		TenantID:   "acme",
		DatabaseID: "db1",
		Database:   "db1",
		ShardID:    500,
		RF:         2,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100", State: placement.ReplicaStateActive},
			{ReplicaID: 2, NodeID: "node-b:9100", RaftAddr: "node-b:9100", State: placement.ReplicaStateActive},
		},
	}
}

// TestTransferLeadershipAwayFromRemoved_NoopWhenNotLeader verifies the
// function is a no-op (no RequestLeaderTransfer call) when replicaID is not
// the shard's current leader.
func TestTransferLeadershipAwayFromRemoved_NoopWhenNotLeader(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	if err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, transferPlacement(), 500, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls, got %d", len(host.transferCalls))
	}
}

// TestTransferLeadershipAwayFromRemoved_NoopWhenLeaderUnknown verifies the
// function is a no-op when the shard's leader cannot be determined.
func TestTransferLeadershipAwayFromRemoved_NoopWhenLeaderUnknown(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderOK: false}
	if err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, transferPlacement(), 500, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls, got %d", len(host.transferCalls))
	}
}

// TestTransferLeadershipAwayFromRemoved_NoopWhenNoOtherVoter verifies the
// function is a no-op when replicaID is the leader but no other ACTIVE
// (voting) replica exists to transfer to.
func TestTransferLeadershipAwayFromRemoved_NoopWhenNoOtherVoter(t *testing.T) {
	t.Parallel()

	solo := &placement.Placement{
		TenantID:   "acme",
		DatabaseID: "db1",
		Database:   "db1",
		ShardID:    500,
		RF:         1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100", State: placement.ReplicaStateActive},
		},
	}
	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	if err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, solo, 500, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls, got %d", len(host.transferCalls))
	}
}

// TestTransferLeadershipAwayFromRemoved_SkipsNonVotingCandidates verifies an
// OBSERVER replica is never chosen as a transfer target, even when it is the
// only OTHER replica besides the leader being removed.
func TestTransferLeadershipAwayFromRemoved_SkipsNonVotingCandidates(t *testing.T) {
	t.Parallel()

	p := &placement.Placement{
		TenantID:   "acme",
		DatabaseID: "db1",
		Database:   "db1",
		ShardID:    500,
		RF:         2,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: "node-a:9100", RaftAddr: "node-a:9100", State: placement.ReplicaStateActive},
			{ReplicaID: 2, NodeID: "node-b:9100", RaftAddr: "node-b:9100", State: placement.ReplicaStateObserver},
		},
	}
	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	if err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, p, 500, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls (only candidate is a non-voting observer), got %d",
			len(host.transferCalls))
	}
}

// TestTransferLeadershipAwayFromRemoved_TransfersToOtherActiveVoter is the
// direct regression test for the bug this function fixes: when replicaID IS
// the current leader, it requests a transfer to another ACTIVE (voting)
// replica and waits until LeaderReplicaID confirms the transfer completed.
func TestTransferLeadershipAwayFromRemoved_TransfersToOtherActiveVoter(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	if err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, transferPlacement(), 500, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.transferCalls) != 1 {
		t.Fatalf("expected 1 RequestLeaderTransfer call, got %d", len(host.transferCalls))
	}
	if host.transferCalls[0].shardID != 500 {
		t.Errorf("transfer shardID = %d, want 500", host.transferCalls[0].shardID)
	}
	if host.transferCalls[0].targetReplicaID != 2 {
		t.Errorf("transfer target = %d, want 2 (the other active voter)", host.transferCalls[0].targetReplicaID)
	}
	if host.leaderID != 2 {
		t.Errorf("leaderID after transfer = %d, want 2", host.leaderID)
	}
}

// TestTransferLeadershipAwayFromRemoved_RequestErrorPropagates verifies a
// RequestLeaderTransfer failure is returned to the caller.
func TestTransferLeadershipAwayFromRemoved_RequestErrorPropagates(t *testing.T) {
	t.Parallel()

	transferErr := errors.New("shard busy")
	host := &fakeRaftHost{leaderID: 1, leaderOK: true, transferErr: transferErr}
	err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, transferPlacement(), 500, 1)
	if !errors.Is(err, transferErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, transferErr)
	}
}

// TestTransferLeadershipAwayFromRemoved_TimesOutWhenTransferNeverCompletes
// verifies a transfer request that is accepted but never actually completes
// (LeaderReplicaID keeps reporting the replica being removed as leader)
// fails closed with *LeaderTransferTimeoutError rather than proceeding to
// let the caller evict the still-current leader.
func TestTransferLeadershipAwayFromRemoved_TimesOutWhenTransferNeverCompletes(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderID: 1, leaderOK: true, transferNoop: true}
	err := transferLeadershipAwayFromRemoved(context.Background(), host, nil, nil, transferPlacement(), 500, 1)
	var lte *LeaderTransferTimeoutError
	if !errors.As(err, &lte) {
		t.Fatalf("expected *LeaderTransferTimeoutError, got %T (%v)", err, err)
	}
	if lte.ShardID != 500 || lte.FromReplicaID != 1 {
		t.Errorf("LeaderTransferTimeoutError = %+v, want ShardID=500 FromReplicaID=1", lte)
	}
	if len(host.transferCalls) != 1 {
		t.Errorf("expected exactly 1 RequestLeaderTransfer call, got %d", len(host.transferCalls))
	}
}

// TestRemoveReplicaFromGroup_NeverCallsLeaderTransfer verifies
// removeReplicaFromGroup itself never attempts a leadership transfer, even
// when the replica being removed is the shard's current leader: that case
// must be handled by the CALLER before removeReplicaFromGroup is ever
// invoked (see RebalanceService.resolveMoveLeader), because RaftHost only
// exposes the LOCAL node's Raft state -- once the local node transferred
// leadership away from itself it could no longer complete the removal
// itself. See removeReplicaFromGroup's doc comment for the full rationale.
func TestRemoveReplicaFromGroup_NeverCallsLeaderTransfer(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	p := transferPlacement()
	if err := store.Put(context.Background(), p); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	updated, placementFailed, err := removeReplicaFromGroup(context.Background(), host, store, p, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if placementFailed {
		t.Error("placementFailed = true, want false")
	}
	if len(updated.Replicas) != 1 || updated.Replicas[0].ReplicaID != 2 {
		t.Fatalf("updated.Replicas = %+v, want only replica 2", updated.Replicas)
	}
	if len(host.transferCalls) != 0 {
		t.Errorf("expected 0 RequestLeaderTransfer calls, got %d", len(host.transferCalls))
	}
	if len(host.removeCalls) != 1 || host.removeCalls[0].replicaID != 1 {
		t.Fatalf("removeCalls = %+v, want exactly 1 call removing replica 1", host.removeCalls)
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round-3 finding (3): addObserverReplica/removeReplicaFromGroup
// must persist their placement mutation via a compare-and-swap against the
// exact snapshot they were handed, not an unconditional Put -- otherwise a
// concurrent mutation (another scale step, a reconciler promotion, a Drop's
// fence) committed between that snapshot's read and this write is silently
// discarded.
// ---------------------------------------------------------------------------

// TestAddObserverReplica_RejectsStaleCurrentSnapshot proves addObserverReplica
// refuses to persist when the placement entry has changed since current was
// read: it must report a placement conflict, leave the stored entry exactly
// as the concurrent writer left it, and never attempt AddObserver at all
// (the config change must not run against a config the store itself just
// rejected as stale).
func TestAddObserverReplica_RejectsStaleCurrentSnapshot(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{}
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	current := transferPlacement()
	if err := store.Put(context.Background(), current); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	// A concurrent mutation lands after `current` was read but before
	// addObserverReplica's own compare-and-swap: bump RF the way a
	// concurrent scale-up plan would, through the store directly (never
	// through `current`, which addObserverReplica still holds the
	// now-stale copy of).
	racing := *current
	racing.RF = current.RF + 1
	if err := store.Put(context.Background(), &racing); err != nil {
		t.Fatalf("racing Put: %v", err)
	}

	r := placement.Replica{ReplicaID: 3, NodeID: "node3", RaftAddr: "node3:1", State: placement.ReplicaStateObserver}
	updated, kerr, placementFailed, err := addObserverReplica(context.Background(), host, store, nil, nil, current, r, "local")
	if kerr != nil {
		t.Fatalf("unexpected key-delivery error: %v", kerr)
	}
	if !placementFailed {
		t.Error("placementFailed = false, want true (the write must be attributed to the placement step)")
	}
	if cerr := (*placement.ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want to wrap *placement.ConflictError", err)
	}
	if updated != current {
		t.Error("updated must equal the caller's stale snapshot on a rejected swap, not a mutated copy")
	}
	if len(host.observerCalls) != 0 {
		t.Errorf("AddObserver called %d times, want 0 (a rejected placement write must never reach Raft)", len(host.observerCalls))
	}

	got, err := store.Get(context.Background(), current.TenantID, current.DatabaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RF != racing.RF {
		t.Errorf("stored RF = %d, want %d (the racing writer's change must survive untouched)", got.RF, racing.RF)
	}
	for _, rep := range got.Replicas {
		if rep.ReplicaID == r.ReplicaID {
			t.Error("the rejected observer replica must not appear in the stored placement")
		}
	}
}

// TestRemoveReplicaFromGroup_RejectsStaleCurrentSnapshot is
// TestAddObserverReplica_RejectsStaleCurrentSnapshot's mirror for the
// scale-down / MOVE-source removal path.
func TestRemoveReplicaFromGroup_RejectsStaleCurrentSnapshot(t *testing.T) {
	t.Parallel()

	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	current := transferPlacement()
	if err := store.Put(context.Background(), current); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	racing := *current
	racing.RF = current.RF + 1
	if err := store.Put(context.Background(), &racing); err != nil {
		t.Fatalf("racing Put: %v", err)
	}

	updated, placementFailed, err := removeReplicaFromGroup(context.Background(), host, store, current, 2)
	if !placementFailed {
		t.Error("placementFailed = false, want true")
	}
	if cerr := (*placement.ConflictError)(nil); !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want to wrap *placement.ConflictError", err)
	}
	if updated != current {
		t.Error("updated must equal the caller's stale snapshot on a rejected swap, not a mutated copy")
	}
	if len(host.removeCalls) != 0 {
		t.Errorf("RemoveFollowerNode called %d times, want 0 (a rejected placement write must never reach Raft)", len(host.removeCalls))
	}

	got, err := store.Get(context.Background(), current.TenantID, current.DatabaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RF != racing.RF {
		t.Errorf("stored RF = %d, want %d (the racing writer's change must survive untouched)", got.RF, racing.RF)
	}
	if len(got.Replicas) != 2 {
		t.Errorf("stored Replicas = %+v, want both replicas still present (the removal must not have applied)", got.Replicas)
	}
}

// TestRemoveReplicaFromGroup_EvictsBeforeDroppingPlacement proves the Raft
// eviction runs while the placement still lists the replica. The placement
// is what stops a replica's shard on its node, and the eviction commits only
// with the votes of the configuration it leaves -- in a group of two voters,
// the removed replica's own. Dropping the placement first stopped that voter
// and left the removal uncommittable.
func TestRemoveReplicaFromGroup_EvictsBeforeDroppingPlacement(t *testing.T) {
	t.Parallel()

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	current := transferPlacement()
	if err := store.Put(context.Background(), current); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	listedAtEviction := false
	host := &fakeRaftHost{leaderID: 1, leaderOK: true}
	host.onRemove = func() {
		got, gerr := store.Get(context.Background(), current.TenantID, current.DatabaseID)
		if gerr != nil {
			t.Errorf("Get during eviction: %v", gerr)
			return
		}
		_, listedAtEviction = got.ReplicaForNode("node-b:9100")
	}

	updated, placementFailed, err := removeReplicaFromGroup(context.Background(), host, store, current, 2)
	if err != nil || placementFailed {
		t.Fatalf("removeReplicaFromGroup = (placementFailed=%v, err=%v), want success", placementFailed, err)
	}
	if !listedAtEviction {
		t.Error("the replica was dropped from the placement before its Raft eviction")
	}
	if updated.RF != 1 || len(updated.Replicas) != 1 || updated.Replicas[0].ReplicaID != 1 {
		t.Errorf("updated = RF %d replicas %+v, want RF 1 with only replica 1", updated.RF, updated.Replicas)
	}
	got, err := store.Get(context.Background(), current.TenantID, current.DatabaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.ReplicaForNode("node-b:9100"); ok || got.RF != 1 {
		t.Errorf("stored = RF %d replicas %+v, want replica 2 gone and RF 1", got.RF, got.Replicas)
	}
}

// TestRemoveReplicaFromGroup_KeepsChangeCommittedDuringEviction proves a
// placement change committed while the eviction runs -- a reconciler
// promoting another replica -- neither fails the removal nor is lost: once
// Raft has evicted the replica, the placement must record it.
func TestRemoveReplicaFromGroup_KeepsChangeCommittedDuringEviction(t *testing.T) {
	t.Parallel()

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	current := transferPlacement()
	current.Replicas = append(current.Replicas, placement.Replica{
		ReplicaID: 3, NodeID: "node-c:9100", RaftAddr: "node-c:9100", State: placement.ReplicaStateObserver,
	})
	current.RF = len(current.Replicas)
	if err := store.Put(context.Background(), current); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	host := &fakeRaftHost{leaderID: 2, leaderOK: true}
	host.onRemove = func() {
		promoted := *current
		promoted.Replicas = append([]placement.Replica(nil), current.Replicas...)
		promoted.Replicas[2].State = placement.ReplicaStateActive
		if perr := store.Put(context.Background(), &promoted); perr != nil {
			t.Errorf("racing Put: %v", perr)
		}
	}

	updated, placementFailed, err := removeReplicaFromGroup(context.Background(), host, store, current, 1)
	if err != nil || placementFailed {
		t.Fatalf("removeReplicaFromGroup = (placementFailed=%v, err=%v), want success", placementFailed, err)
	}
	if len(host.removeCalls) != 1 {
		t.Fatalf("removeCalls = %+v, want exactly 1", host.removeCalls)
	}
	if _, ok := updated.ReplicaForNode("node-a:9100"); ok {
		t.Error("replica 1 must be dropped from the returned placement")
	}
	got, err := store.Get(context.Background(), current.TenantID, current.DatabaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.ReplicaForNode("node-a:9100"); ok {
		t.Error("replica 1 must be dropped from the stored placement")
	}
	r, ok := got.ReplicaForNode("node-c:9100")
	if !ok || r.State != placement.ReplicaStateActive {
		t.Errorf("replica 3 = %+v (listed %v), want the concurrent promotion to Active kept", r, ok)
	}
	if got.RF != 2 {
		t.Errorf("stored RF = %d, want 2", got.RF)
	}
}

// TestRemoveReplicaFromGroup_EvictionFailureKeepsPlacement proves a failed
// RemoveFollowerNode leaves the placement listing the replica: it is still a
// member of the group, so its node must keep it running.
func TestRemoveReplicaFromGroup_EvictionFailureKeepsPlacement(t *testing.T) {
	t.Parallel()

	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	current := transferPlacement()
	if err := store.Put(context.Background(), current); err != nil {
		t.Fatalf("initial Put: %v", err)
	}
	removeErr := errors.New("config change rejected")
	host := &fakeRaftHost{leaderID: 1, leaderOK: true, removeErr: removeErr}

	updated, placementFailed, err := removeReplicaFromGroup(context.Background(), host, store, current, 2)
	if !errors.Is(err, removeErr) {
		t.Fatalf("err = %v, want %v", err, removeErr)
	}
	if placementFailed {
		t.Error("placementFailed = true, want false (the eviction failed, not the placement)")
	}
	if updated != current {
		t.Error("updated must be the caller's snapshot when the eviction fails")
	}
	got, err := store.Get(context.Background(), current.TenantID, current.DatabaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Replicas) != 2 || got.RF != 2 {
		t.Errorf("stored = RF %d replicas %+v, want both replicas kept", got.RF, got.Replicas)
	}
}
