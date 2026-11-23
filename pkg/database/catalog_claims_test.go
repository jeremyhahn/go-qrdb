package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// recordFailingStore is a home store whose record write fails while fail
// is set: the root claim is written, the record is not.
type recordFailingStore struct {
	*kvstore.MemoryKVStore
	fail error
}

func (s *recordFailingStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.fail != nil {
		return false, s.fail
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func claimExists(t *testing.T, system kvstore.KVStore, tenantID, incarnation, name string) bool {
	t.Helper()
	ok, err := system.Exists(context.Background(), rootClaimKey(tenantID, incarnation, name))
	if err != nil {
		t.Fatalf("Exists claim %s/%s/%s: %v", tenantID, incarnation, name, err)
	}
	return ok
}

// putClaim writes a root claim by hand, as a crash after the claim put (or
// before the claim delete) leaves it.
func putClaim(t *testing.T, system kvstore.KVStore, meta DatabaseMeta) {
	t.Helper()
	claim, err := rootClaim(meta)
	if err != nil {
		t.Fatalf("rootClaim: %v", err)
	}
	if err := system.Put(context.Background(), rootClaimKey(meta.TenantID, meta.Incarnation, meta.Name), claim); err != nil {
		t.Fatalf("Put claim: %v", err)
	}
}

func TestKVCatalog_FailedCreateReleasesItsRootClaim(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("home write failed")
	home := &recordFailingStore{MemoryKVStore: kvstore.NewMemoryKVStore(), fail: boom}
	cat, system := newTestCatalog(t, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}})

	err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory})
	var writeErr *CatalogWriteError
	if !errors.As(err, &writeErr) || !errors.Is(err, boom) {
		t.Fatalf("Create err = %v, want *CatalogWriteError wrapping the home write error", err)
	}
	if claimExists(t, system, "iso", "i1", "app") {
		t.Fatal("the failed create left its root claim")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := cat.Create(cancelled, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i2", Backend: BackendMemory}); err == nil {
		t.Fatal("Create on a cancelled context with a failing home succeeded")
	}
	if claimExists(t, system, "iso", "i2", "app") {
		t.Fatal("the failed create on a cancelled context left its root claim")
	}
}

func TestKVCatalog_ListAllReapsAClaimWithoutARecord(t *testing.T) {
	ctx := context.Background()
	cat, system := newTestCatalog(t, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": kvstore.NewMemoryKVStore()}})
	live := DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory}
	if err := cat.Create(ctx, live); err != nil {
		t.Fatalf("Create app: %v", err)
	}
	// A claim with no record: a create that crashed after its claim put.
	putClaim(t, system, DatabaseMeta{TenantID: "iso", Name: "ghost", Incarnation: "g1", Backend: BackendMemory})
	// A claim of an older incarnation of the live name.
	putClaim(t, system, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i0", Backend: BackendMemory})

	metas, err := cat.ListAll(ctx)
	if err != nil || len(metas) != 1 || metas[0].Name != "app" {
		t.Fatalf("ListAll = %v, %v; want app", metas, err)
	}
	if claimExists(t, system, "iso", "g1", "ghost") {
		t.Fatal("the claim without a record survived ListAll")
	}
	if claimExists(t, system, "iso", "i0", "app") {
		t.Fatal("the claim of a stale incarnation survived ListAll")
	}
	if !claimExists(t, system, "iso", "i1", "app") {
		t.Fatal("ListAll reaped the claim a live record holds")
	}
}

func TestKVCatalog_CrashBetweenRecordAndClaimDeleteIsReaped(t *testing.T) {
	ctx := context.Background()
	home := kvstore.NewMemoryKVStore()
	cat, system := newTestCatalog(t, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}})
	for _, name := range []string{"app", "keep", "drop"} {
		if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: name, Incarnation: name + "-1", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	// The record delete lands, then the process dies before the claim delete.
	if err := home.Delete(ctx, CatalogKey("iso", "app")); err != nil {
		t.Fatalf("delete the record by hand: %v", err)
	}
	if !claimExists(t, system, "iso", "app-1", "app") {
		t.Fatal("setup: the claim is gone with the record")
	}

	// The next delete in the tenant reaps it.
	if err := cat.Delete(ctx, "iso", "drop", "drop-1", ""); err != nil {
		t.Fatalf("Delete drop: %v", err)
	}
	if claimExists(t, system, "iso", "app-1", "app") {
		t.Fatal("Delete did not reap the claim a crashed delete left")
	}
	if claimExists(t, system, "iso", "drop-1", "drop") {
		t.Fatal("Delete left its own claim")
	}
	if !claimExists(t, system, "iso", "keep-1", "keep") {
		t.Fatal("Delete reaped the claim a live record holds")
	}

	// A list reaps it too.
	if err := home.Delete(ctx, CatalogKey("iso", "keep")); err != nil {
		t.Fatalf("delete the record by hand: %v", err)
	}
	if _, err := cat.ListAll(ctx); err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if claimExists(t, system, "iso", "keep-1", "keep") {
		t.Fatal("ListAll did not reap the claim a crashed delete left")
	}
}

func TestKVCatalog_ReapSkipsASealedTenant(t *testing.T) {
	ctx := context.Background()
	homes := &sealableHomes{
		isolatedHomes: isolatedHomes{homes: map[string]kvstore.KVStore{"iso": kvstore.NewMemoryKVStore()}},
		sealed:        map[string]bool{},
	}
	cat, system := newTestCatalog(t, homes)
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create app: %v", err)
	}
	putClaim(t, system, DatabaseMeta{TenantID: "iso", Name: "ghost", Incarnation: "g1", Backend: BackendMemory})
	homes.sealed["iso"] = true

	_, err := cat.ListAll(ctx)
	var skipped *TenantsSkippedError
	if !errors.As(err, &skipped) {
		t.Fatalf("ListAll err = %v, want *TenantsSkippedError", err)
	}
	if !claimExists(t, system, "iso", "i1", "app") || !claimExists(t, system, "iso", "g1", "ghost") {
		t.Fatal("ListAll reaped a sealed tenant's claims")
	}
	roots, err := cat.ListRoots(ctx)
	if err != nil || len(roots) != 2 {
		t.Fatalf("ListRoots = %v, %v; want both sealed claims", roots, err)
	}
}
