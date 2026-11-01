package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestManager_SharedCatalog_SecondNodeSeesCreateAndDrop is two nodes: two
// Managers over separate data directories and one catalog, as two nodes share
// the replicated store. A database created through one is found, listed and
// dropped through the other, and the drop reaches the first.
func TestManager_SharedCatalog_SecondNodeSeesCreateAndDrop(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	a := NewManager(nil, t.TempDir())
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir())
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}

	db, err := b.GetForTenant(ctx, "orders")
	if err != nil {
		t.Fatalf("GetForTenant on B: %v", err)
	}
	if db.TenantID() != "tenant-a" {
		t.Fatalf("B opened tenant %q, want tenant-a", db.TenantID())
	}
	infos, err := b.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant on B: %v", err)
	}
	if len(infos) != 1 || infos[0].Name != "orders" {
		t.Fatalf("ListForTenant on B = %+v, want [orders]", infos)
	}

	if err := b.DropForTenant(ctx, "orders"); err != nil {
		t.Fatalf("DropForTenant on B: %v", err)
	}
	var nf *NotFoundError
	if _, err := cat.Get(ctx, "tenant-a", "orders"); !errors.As(err, &nf) {
		t.Fatalf("catalog Get after the drop = %v, want *NotFoundError", err)
	}
	infos, err = a.ListForTenant(ctx)
	if err != nil {
		t.Fatalf("ListForTenant on A: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("ListForTenant on A after B dropped = %+v, want none", infos)
	}
}

func TestManager_List_OrdersOneNameByTenant(t *testing.T) {
	m := newCatalogManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	for _, tenantID := range []string{"tenant-b", "tenant-a"} {
		if _, err := m.CreateForTenant(tenantContext(tenantID), validMemoryConfig("orders")); err != nil {
			t.Fatalf("CreateForTenant(%s): %v", tenantID, err)
		}
	}
	infos := m.List()
	if len(infos) != 2 || infos[0].TenantID != "tenant-a" || infos[1].TenantID != "tenant-b" {
		t.Fatalf("List = %+v, want orders under tenant-a then tenant-b", infos)
	}
}

// TestManager_ReleaseRemovesTheDroppedDatabasesBarrier pins that a node
// forgetting a database another node dropped also forgets its barrier.
func TestManager_ReleaseRemovesTheDroppedDatabasesBarrier(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bm := newTestBarrierManager(t)
	a := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()

	ctx := tenantContext("tenant-a")
	cfg := validMemoryConfig("vault")
	cfg.TenantID = "tenant-a"
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase}
	db, err := a.CreateWithCredentials(ctx, cfg, testCreds())
	if err != nil {
		t.Fatalf("CreateWithCredentials on A: %v", err)
	}
	if bm.GetBarrier("vault", "tenant-a") == nil {
		t.Fatal("precondition: A holds no barrier for the database it created")
	}
	if err := cat.Delete(ctx, "tenant-a", "vault", db.Incarnation(), ""); err != nil {
		t.Fatalf("remove the record as a dropping node would: %v", err)
	}
	if infos, err := a.ListForTenant(ctx); err != nil || len(infos) != 0 {
		t.Fatalf("ListForTenant on A after the record went = %+v, %v; want none", infos, err)
	}
	if bm.GetBarrier("vault", "tenant-a") != nil {
		t.Fatal("A kept the barrier of a database whose record is gone")
	}
}

// TestManager_SharedCatalog_UnknownToBothIsNotFound is the negative of the
// test above: a name neither node created is not found through either.
func TestManager_SharedCatalog_UnknownToBothIsNotFound(t *testing.T) {
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	a := NewManager(nil, t.TempDir())
	a.SetCatalog(cat)
	defer func() { _ = a.Close() }()
	b := NewManager(nil, t.TempDir())
	b.SetCatalog(cat)
	defer func() { _ = b.Close() }()

	ctx := tenantContext("tenant-a")
	if _, err := a.CreateForTenant(ctx, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant on A: %v", err)
	}
	var nf *NotFoundError
	if _, err := b.GetForTenant(tenantContext("tenant-b"), "orders"); !errors.As(err, &nf) {
		t.Fatalf("another tenant's GetForTenant on B = %v, want *NotFoundError", err)
	}
	if _, err := b.GetForTenant(ctx, "invoices"); !errors.As(err, &nf) {
		t.Fatalf("GetForTenant of an unrecorded name on B = %v, want *NotFoundError", err)
	}
}

// TestManager_TwoTenantsOneName holds a name two tenants both use: each
// tenant's database opens on its own, and the name-only lookups, which cannot
// say which one is meant, refuse with *AmbiguousNameError.
func TestManager_TwoTenantsOneName(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	ctxA, ctxB := tenantContext("tenant-a"), tenantContext("tenant-b")
	if _, err := mgr.CreateForTenant(ctxA, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant(tenant-a): %v", err)
	}
	if _, err := mgr.CreateForTenant(ctxB, validMemoryConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant(tenant-b): %v", err)
	}

	for _, id := range []string{"tenant-a", "tenant-b"} {
		db, err := mgr.GetForTenant(tenantContext(id), "orders")
		if err != nil {
			t.Fatalf("GetForTenant(%s): %v", id, err)
		}
		if db.TenantID() != id {
			t.Fatalf("GetForTenant(%s) opened tenant %q", id, db.TenantID())
		}
		if db.Status() != StatusOpen {
			t.Fatalf("GetForTenant(%s) status = %q, want open", id, db.Status())
		}
	}

	var amb *AmbiguousNameError
	if _, err := mgr.Get("orders"); !errors.As(err, &amb) {
		t.Fatalf("Get(orders) = %v, want *AmbiguousNameError", err)
	}
	if err := mgr.Drop(context.Background(), "orders"); !errors.As(err, &amb) {
		t.Fatalf("Drop(orders) = %v, want *AmbiguousNameError", err)
	}

	// The refused Drop removed neither; a tenant-scoped drop removes one.
	if err := mgr.DropForTenant(ctxA, "orders"); err != nil {
		t.Fatalf("DropForTenant(tenant-a): %v", err)
	}
	db, err := mgr.Get("orders")
	if err != nil {
		t.Fatalf("Get(orders) with one tenant left: %v", err)
	}
	if db.TenantID() != "tenant-b" {
		t.Fatalf("Get(orders) opened tenant %q, want tenant-b", db.TenantID())
	}
}
