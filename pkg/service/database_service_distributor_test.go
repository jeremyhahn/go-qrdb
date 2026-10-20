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
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// captureDistributor is a test KeyDistributor that records every
// DistributeTenantKey call for assertion.
type captureDistributor struct {
	calls []captureDistributorCall
}

type captureDistributorCall struct {
	plan          *placement.Placement
	leaderBarrier *seal.StorageBarrier
	selfNodeID    string
}

func (c *captureDistributor) DistributeTenantKey(
	_ context.Context,
	p *placement.Placement,
	lb *seal.StorageBarrier,
	nodeID string,
) error {
	c.calls = append(c.calls, captureDistributorCall{
		plan:          p,
		leaderBarrier: lb,
		selfNodeID:    nodeID,
	})
	return nil
}

// fakeTenantBarrierLookup implements TenantBarrierLookup using a
// pre-constructed *seal.TenantBarrier.
type fakeTenantBarrierLookup struct {
	tb *seal.TenantBarrier
}

func (f *fakeTenantBarrierLookup) GetTenant(_ string) (*seal.TenantBarrier, error) {
	return f.tb, nil
}

// newTestTenantBarrier creates a minimal *seal.TenantBarrier backed by an in-memory
// storage backend. It is not initialized/unsealed — the captureDistributor does
// not actually use the barrier's crypto; it only records that it received the value.
func newTestTenantBarrier(t *testing.T, tenantID string) *seal.TenantBarrier {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tb, err := seal.NewTenantBarrier(tenantID, logger, seal.NewMemoryBackend(), seal.BarrierConfig{}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewTenantBarrier: %v", err)
	}
	return tb
}

// newDistributorDatabaseService builds a DatabaseService for an IsolationRaft
// tenant with placement + distributor wired in.
func newDistributorDatabaseService(
	t *testing.T,
	tenantInfo *tenant.TenantInfo,
	dist KeyDistributor,
	lookup TenantBarrierLookup,
	localNodeID string,
	nodes []placement.NodeLoad,
) (DatabaseServicer, *placement.Store) {
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

	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: tenantInfo}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
		WithKeyDistributor(dist),
		WithBarrierLookup(lookup),
		WithLocalNodeID(localNodeID),
	)
	return svc, store
}

// TestCreateForTenant_DistributorCalledOnPlacement verifies that when a
// dedicated-raft database is created and placement succeeds the KeyDistributor
// is called with the computed Placement, the leader's StorageBarrier and the
// local node ID.
func TestCreateForTenant_DistributorCalledOnPlacement(t *testing.T) {
	t.Parallel()

	dist := &captureDistributor{}
	tb := newTestTenantBarrier(t, "rt")
	lookup := &fakeTenantBarrierLookup{tb: tb}
	localNode := "node1:63001"

	raftInfo := &tenant.TenantInfo{
		ID:            "rt",
		IsolationType: tenant.IsolationRaft,
	}
	svc, _ := newDistributorDatabaseService(t, raftInfo, dist, lookup, localNode,
		healthyNodes("node1", "node2", "node3"))

	ctx := tenant.WithTenantID(context.Background(), "rt")
	cfg := testDBConfig("payments")
	cfg.Replicas = 3

	info, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	if len(dist.calls) != 1 {
		t.Fatalf("DistributeTenantKey called %d times, want 1", len(dist.calls))
	}
	call := dist.calls[0]
	if call.plan == nil {
		t.Fatal("plan must not be nil")
	}
	if call.plan.ShardID != info.ShardID {
		t.Errorf("plan.ShardID = %d, want %d", call.plan.ShardID, info.ShardID)
	}
	if call.plan.TenantID != "rt" {
		t.Errorf("plan.TenantID = %q, want %q", call.plan.TenantID, "rt")
	}
	if call.leaderBarrier != tb.Barrier() {
		t.Errorf("leaderBarrier not the expected *StorageBarrier from TenantBarrier.Barrier()")
	}
	if call.selfNodeID != localNode {
		t.Errorf("selfNodeID = %q, want %q", call.selfNodeID, localNode)
	}
}

// TestCreateForTenant_PlatformCustodySkipsDistribution verifies that a
// platform-custody (BarrierAutoUnseal) tenant's database create never ships a
// key: every replica self-initializes its own barrier under its own key, so
// there is nothing to deliver.
func TestCreateForTenant_PlatformCustodySkipsDistribution(t *testing.T) {
	t.Parallel()

	dist := &captureDistributor{}
	tb := newTestTenantBarrier(t, "platform-rt")
	lookup := &fakeTenantBarrierLookup{tb: tb}

	raftInfo := &tenant.TenantInfo{
		ID:                "platform-rt",
		IsolationType:     tenant.IsolationRaft,
		BarrierAutoUnseal: true,
	}
	svc, _ := newDistributorDatabaseService(t, raftInfo, dist, lookup, "leader:63001",
		healthyNodes("leader", "follower1"))

	ctx := tenant.WithTenantID(context.Background(), "platform-rt")
	cfg := testDBConfig("orders")
	cfg.Replicas = 2
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}

	if len(dist.calls) != 0 {
		t.Fatalf("DistributeTenantKey called %d times for a platform-custody tenant, want 0", len(dist.calls))
	}
}

// TestCreateForTenant_DistributorSkippedWithoutPrerequisites verifies that
// when the distributor, lookup, or localNodeID is absent the create succeeds
// without calling DistributeTenantKey.
func TestCreateForTenant_DistributorSkippedWithoutPrerequisites(t *testing.T) {
	t.Parallel()

	raftInfo := &tenant.TenantInfo{
		ID:            "rt",
		IsolationType: tenant.IsolationRaft,
	}

	t.Run("no distributor", func(t *testing.T) {
		t.Parallel()
		// No distributor wired → succeeds, nothing called.
		dist := &captureDistributor{}
		tb := newTestTenantBarrier(t, "rt")
		lookup := &fakeTenantBarrierLookup{tb: tb}
		// Build service WITHOUT WithKeyDistributor.
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		mgr := claimingManager(t, logger, t.TempDir())
		t.Cleanup(func() { _ = mgr.Close() })
		store, _ := placement.NewStore(newMemPlacementKV())
		scheduler, _ := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2")})
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
		svc := NewDatabaseService(mgr,
			WithEntitlementChecker(&mockEntitlementChecker{}),
			WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftInfo}),
			WithQuotaChecker(newReplicaQuotaChecker(t, f)),
			WithQuotaUsageRecorder(f),
			WithPlacementStore(store),
			WithPlacementScheduler(scheduler),
			WithDatabaseLogger(logger),
			// NO WithKeyDistributor
			WithBarrierLookup(lookup),
			WithLocalNodeID("n1:63001"),
		)
		ctx := tenant.WithTenantID(context.Background(), "rt")
		if _, err := svc.CreateForTenant(ctx, testDBConfig("db1")); err != nil {
			t.Fatalf("CreateForTenant: %v", err)
		}
		if len(dist.calls) != 0 {
			t.Errorf("expected 0 distributor calls, got %d", len(dist.calls))
		}
	})

	t.Run("no local node ID", func(t *testing.T) {
		t.Parallel()
		dist := &captureDistributor{}
		tb := newTestTenantBarrier(t, "rt")
		lookup := &fakeTenantBarrierLookup{tb: tb}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		mgr := claimingManager(t, logger, t.TempDir())
		t.Cleanup(func() { _ = mgr.Close() })
		store, _ := placement.NewStore(newMemPlacementKV())
		scheduler, _ := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1")})
		f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
		svc := NewDatabaseService(mgr,
			WithEntitlementChecker(&mockEntitlementChecker{}),
			WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftInfo}),
			WithQuotaChecker(newReplicaQuotaChecker(t, f)),
			WithQuotaUsageRecorder(f),
			WithPlacementStore(store),
			WithPlacementScheduler(scheduler),
			WithDatabaseLogger(logger),
			WithKeyDistributor(dist),
			WithBarrierLookup(lookup),
			// NO WithLocalNodeID
		)
		ctx := tenant.WithTenantID(context.Background(), "rt")
		if _, err := svc.CreateForTenant(ctx, testDBConfig("db2")); err != nil {
			t.Fatalf("CreateForTenant: %v", err)
		}
		if len(dist.calls) != 0 {
			t.Errorf("expected 0 distributor calls, got %d", len(dist.calls))
		}
	})
}

// TestSetDistributor_PostConstruction verifies that SetDistributor successfully
// injects the distributor, lookup, and nodeID after construction, and that a
// subsequent CreateForTenant calls the distributor.
func TestSetDistributor_PostConstruction(t *testing.T) {
	t.Parallel()

	raftInfo := &tenant.TenantInfo{
		ID:            "rt",
		IsolationType: tenant.IsolationRaft,
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	store, _ := placement.NewStore(newMemPlacementKV())
	scheduler, _ := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1", "n2")})
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}

	// Construct service WITHOUT distributor.
	svcI := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftInfo}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)

	// Inject via SetDistributor.
	svc, ok := svcI.(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService must return *DatabaseService")
	}
	dist := &captureDistributor{}
	tb := newTestTenantBarrier(t, "rt")
	svc.SetDistributor(dist, &fakeTenantBarrierLookup{tb: tb}, "n1:63001")

	ctx := tenant.WithTenantID(context.Background(), "rt")
	if _, err := svcI.CreateForTenant(ctx, testDBConfig("payments")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if len(dist.calls) != 1 {
		t.Fatalf("DistributeTenantKey called %d times, want 1", len(dist.calls))
	}
	if dist.calls[0].selfNodeID != "n1:63001" {
		t.Errorf("selfNodeID = %q, want %q", dist.calls[0].selfNodeID, "n1:63001")
	}
}
