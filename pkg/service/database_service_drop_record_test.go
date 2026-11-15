package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestDropForTenant_ResumesWithANewPlacementStoreAfterCatalogRemoval: the
// first drop removes the catalog record, then fails to delete the placement
// entry. The retry runs in a new process -- a newly constructed placement
// Store, which learns names only from the catalog and so cannot name the
// entry -- and still finds and removes it, through the tenant's drop record,
// which it then clears.
func TestDropForTenant_ResumesWithANewPlacementStoreAfterCatalogRemoval(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 100}}
	svc, _, kv := newFlakyPlacementDatabaseService(t, raftTenant(t), f, healthyNodes("n1"))
	ctx := tenant.WithTenantID(context.Background(), "rt")

	cfg := testDBConfig("orders")
	cfg.Replicas = 1
	created, err := svc.CreateForTenant(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	kv.failNextDelete = true
	if err := svc.DropForTenant(ctx, "orders"); err == nil {
		t.Fatal("first DropForTenant should have failed on the placement delete")
	}
	mgr := svc.(*DatabaseService).manager
	if id, err := mgr.DroppedID(ctx, "rt", "orders"); err != nil || id != created.ID {
		t.Fatalf("drop record after the failed teardown = (%q, %v), want %q", id, err, created.ID)
	}

	fresh, err := placement.NewStore(kv)
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	if _, err := fresh.GetByName(ctx, "rt", "orders"); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("a new Store named the entry without the catalog (err = %v); the test would not exercise the drop record", err)
	}
	scheduler, err := placement.NewScheduler(&staticTopology{nodes: healthyNodes("n1")})
	if err != nil {
		t.Fatalf("placement.NewScheduler: %v", err)
	}
	restarted := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: raftTenant(t)}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
		WithPlacementStore(fresh),
		WithPlacementScheduler(scheduler),
		WithDatabaseLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)

	if err := restarted.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("resumed DropForTenant: %v", err)
	}
	if _, err := fresh.Get(ctx, "rt", created.ID); !errors.Is(err, placement.ErrPlacementNotFound) {
		t.Fatalf("placement entry after the resumed drop: err = %v, want ErrPlacementNotFound", err)
	}
	if id, err := mgr.DroppedID(ctx, "rt", "orders"); err != nil || id != "" {
		t.Fatalf("drop record after the resumed drop = (%q, %v), want cleared", id, err)
	}
}
