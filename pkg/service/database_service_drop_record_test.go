package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// deleteRefusingCatalog fails every Delete, so a refused create's cleanup
// cannot remove its provisional record and strands it in the catalog.
type deleteRefusingCatalog struct {
	database.Catalog
	err error
}

func (c deleteRefusingCatalog) Delete(context.Context, string, string, string, string) error {
	return c.err
}

// A drop on a service with no placement store reclaims a stranded
// provisional record instead of answering NotFound for ever, so the name
// can be created again.
func TestDropForTenant_ReclaimsAStrandedProvisionalRecord(t *testing.T) {
	mgr := newCoverageDatabaseManager(t)
	svc := NewDatabaseService(mgr)
	cat := databasetest.Catalog(mgr.DataDir())

	mgr.SetCatalog(deleteRefusingCatalog{Catalog: cat, err: errors.New("catalog delete timed out")})
	refused := database.WithCreateGuard(context.Background(), func(context.Context) error { return errors.New("refused") })
	_, err := mgr.CreateWithCredentials(refused,
		database.DatabaseConfig{TenantID: "acme", Name: "orders", Backend: database.BackendMemory}, seal.Credentials{})
	var orphan *database.CatalogOrphanError
	if !errors.As(err, &orphan) {
		t.Fatalf("refused create = %v, want *database.CatalogOrphanError", err)
	}
	mgr.SetCatalog(cat)
	meta, err := cat.Get(context.Background(), "acme", "orders")
	if err != nil {
		t.Fatalf("catalog Get of the stranded record: %v", err)
	}
	if !meta.Provisional {
		t.Fatal("stranded record is not provisional; the test would not exercise the reclaim")
	}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	if err := svc.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant of the stranded name: %v", err)
	}
	var notFound *database.NotFoundError
	if _, err := cat.Get(context.Background(), "acme", "orders"); !errors.As(err, &notFound) {
		t.Fatalf("catalog Get after the reclaim = %v, want *database.NotFoundError", err)
	}
	if err := svc.DropForTenant(ctx, "orders"); !errors.As(err, &notFound) {
		t.Fatalf("second DropForTenant = %v, want *database.NotFoundError", err)
	}
	if _, err := svc.CreateForTenant(ctx, testDBConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant after the reclaim: %v", err)
	}
}
