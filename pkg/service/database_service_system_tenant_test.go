package service

// System-tenant create tests (#923): a system-tenant database takes the
// tenant create path, so a raft database is placed on its replicas and
// stored under the tenant root; only the quota and entitlement checks are
// skipped for it.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

var errSystemTenantGate = errors.New("system tenant consulted a tenant gate")

// refusingQuota fails every quota call and counts them.
type refusingQuota struct{ calls atomic.Int64 }

func (r *refusingQuota) Check(context.Context, string, quota.Resource) error {
	r.calls.Add(1)
	return errSystemTenantGate
}

func (r *refusingQuota) CheckN(context.Context, string, quota.Resource, int64) error {
	r.calls.Add(1)
	return errSystemTenantGate
}

func (r *refusingQuota) Usage(context.Context, string, quota.Resource) (int64, error) {
	r.calls.Add(1)
	return 0, errSystemTenantGate
}

func (r *refusingQuota) IncrementQuotaUsage(context.Context, string, quota.Resource, int64) (*tenant.TenantInfo, error) {
	r.calls.Add(1)
	return nil, errSystemTenantGate
}

func (r *refusingQuota) DecrementQuotaUsage(context.Context, string, quota.Resource, int64) (*tenant.TenantInfo, error) {
	r.calls.Add(1)
	return nil, errSystemTenantGate
}

// newSystemTenantDatabaseService builds a DatabaseService over a placement
// store and a scheduler on the given nodes, with an entitlement checker, a
// quota checker and a quota usage recorder that refuse every call.
func newSystemTenantDatabaseService(t *testing.T, nodes []placement.NodeLoad) (DatabaseServicer, *placement.Store, *refusingQuota, *atomic.Int64) {
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

	q := &refusingQuota{}
	var entitlementCalls atomic.Int64
	refuse := func(context.Context, string, string) error {
		entitlementCalls.Add(1)
		return errSystemTenantGate
	}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{isolationFn: refuse, featureFn: refuse}),
		WithQuotaChecker(q),
		WithQuotaUsageRecorder(q),
		WithPlacementStore(store),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(logger),
	)
	return svc, store, q, &entitlementCalls
}

func systemRaftConfig(name string) database.DatabaseConfig {
	cfg := testDBConfig(name)
	cfg.Backend = database.BackendRaft
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftDistributed}
	cfg.Replicas = 3
	return cfg
}

func TestDatabaseService_CreateSystemTenantRaftDistributed_PersistsPlacement(t *testing.T) {
	svc, store, _, _ := newSystemTenantDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	info, err := svc.CreateForTenant(ctx, systemRaftConfig("orders"))
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	p, err := store.Get(ctx, tenantid.System, "orders")
	if err != nil {
		t.Fatalf("placement Get: %v", err)
	}
	if p.RF != 3 || len(p.Replicas) != 3 {
		t.Errorf("placement RF = %d replicas = %d, want 3/3", p.RF, len(p.Replicas))
	}
	seen := map[string]bool{}
	for _, r := range p.Replicas {
		seen[r.NodeID] = true
	}
	if len(seen) != 3 {
		t.Errorf("placement nodes = %v, want three distinct nodes", seen)
	}
	if p.ShardID != info.ShardID {
		t.Errorf("placement shard = %d, want %d (db shard)", p.ShardID, info.ShardID)
	}
	if info.Replicas != 3 {
		t.Errorf("info replicas = %d, want 3", info.Replicas)
	}
}

func TestDatabaseService_CreateSystemTenant_SkipsQuotaAndEntitlement(t *testing.T) {
	svc, _, q, entitlementCalls := newSystemTenantDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	if _, err := svc.CreateForTenant(ctx, systemRaftConfig("ledger")); err != nil {
		t.Fatalf("CreateForTenant raft: %v", err)
	}
	if _, err := svc.CreateForTenant(ctx, testDBConfig("plain")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if n := q.calls.Load(); n != 0 {
		t.Errorf("quota consulted %d times for the system tenant, want 0", n)
	}
	if n := entitlementCalls.Load(); n != 0 {
		t.Errorf("entitlements consulted %d times for the system tenant, want 0", n)
	}
}

func TestDatabaseService_DropSystemTenantRaft_RemovesPlacement(t *testing.T) {
	svc, store, q, _ := newSystemTenantDatabaseService(t, healthyNodes("n1", "n2", "n3"))
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)

	if _, err := svc.CreateForTenant(ctx, systemRaftConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := store.Get(ctx, tenantid.System, "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Errorf("placement after the drop: err = %v, want not found", err)
	}
	if n := q.calls.Load(); n != 0 {
		t.Errorf("quota consulted %d times for the system tenant, want 0", n)
	}
}
