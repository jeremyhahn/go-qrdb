package database

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// faultStore is a real memory store that fails one method for the keys
// (or, for Scan, the prefixes) under prefix.
type faultStore struct {
	*kvstore.MemoryKVStore
	method string
	prefix string
	err    error
}

func newFaultStore() *faultStore { return &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore()} }

func (s *faultStore) failOn(method, prefix string, err error) {
	s.method, s.prefix, s.err = method, prefix, err
}

func (s *faultStore) fault(method, key string) error {
	if s.err != nil && s.method == method && strings.HasPrefix(key, s.prefix) {
		return s.err
	}
	return nil
}

func (s *faultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.fault("Get", key); err != nil {
		return nil, err
	}
	return s.MemoryKVStore.Get(ctx, key)
}

func (s *faultStore) Put(ctx context.Context, key string, value []byte) error {
	if err := s.fault("Put", key); err != nil {
		return err
	}
	return s.MemoryKVStore.Put(ctx, key, value)
}

func (s *faultStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := s.fault("PutIfAbsent", key); err != nil {
		return false, err
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (s *faultStore) Delete(ctx context.Context, key string) error {
	if err := s.fault("Delete", key); err != nil {
		return err
	}
	return s.MemoryKVStore.Delete(ctx, key)
}

func (s *faultStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := s.fault("CompareAndDelete", key); err != nil {
		return false, err
	}
	return s.MemoryKVStore.CompareAndDelete(ctx, key, expected)
}

func (s *faultStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := s.fault("Exists", key); err != nil {
		return false, err
	}
	return s.MemoryKVStore.Exists(ctx, key)
}

func (s *faultStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if err := s.fault("Scan", prefix); err != nil {
		return err
	}
	return s.MemoryKVStore.Scan(ctx, prefix, fn)
}

// faultHomes is CatalogHomes over named home stores whose InHome, Resolve
// and TenantIDs can fail; extra ids are listed without a home.
type faultHomes struct {
	homes     map[string]kvstore.KVStore
	inHomeErr map[string]error
	extra     []string
	idsErr    error
}

func (h *faultHomes) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := h.homes[tenantID]; ok {
		return s, nil
	}
	return nil, errors.New("no home for " + tenantID)
}

func (h *faultHomes) InHome(_ context.Context, tenantID string) (bool, error) {
	if err := h.inHomeErr[tenantID]; err != nil {
		return false, err
	}
	_, ok := h.homes[tenantID]
	return ok, nil
}

func (h *faultHomes) TenantIDs(context.Context) ([]string, error) {
	if h.idsErr != nil {
		return nil, h.idsErr
	}
	ids := append([]string(nil), h.extra...)
	for id := range h.homes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// newFaultCatalog is a KVCatalog over a fault system store with one
// isolated tenant "iso" whose home is a fault store.
func newFaultCatalog(t *testing.T) (*KVCatalog, *faultStore, *faultStore, *faultHomes) {
	t.Helper()
	system, home := newFaultStore(), newFaultStore()
	homes := &faultHomes{homes: map[string]kvstore.KVStore{"iso": home}}
	cat, err := NewKVCatalog(system, homes)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	return cat, system, home, homes
}

// putRecord writes meta's record straight into store, as a record the
// catalog did not create (no claim, no index marker).
func putRecord(t *testing.T, store kvstore.KVStore, meta DatabaseMeta) {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := store.Put(context.Background(), CatalogKey(meta.TenantID, meta.Name), data); err != nil {
		t.Fatalf("Put record: %v", err)
	}
}

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

// TestKVCatalog_ClaimStoreFailures drives every store failure of the claim
// paths (create, release, reap, index, list, delete) through the public
// catalog API: each fails with the typed error naming the failed write or
// read, never with a partial success.
func TestKVCatalog_ClaimStoreFailures(t *testing.T) {
	boom := errors.New("store down")
	app := DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory}
	isWrite := func(err error) bool { var e *CatalogWriteError; return errors.As(err, &e) }
	isRead := func(err error) bool { var e *CatalogReadError; return errors.As(err, &e) }
	isStore := func(err error) bool { var e *CatalogStoreError; return errors.As(err, &e) }
	isBoom := func(err error) bool { return errors.Is(err, boom) }
	isAny := func(err error) bool { return err != nil }
	create := func(c *KVCatalog) error { return c.Create(context.Background(), app) }
	release := func(c *KVCatalog) error { return c.ReleaseCreateClaim(context.Background(), app) }
	listAll := func(c *KVCatalog) error { _, err := c.ListAll(context.Background()); return err }
	listRoots := func(c *KVCatalog) error { _, err := c.ListRoots(context.Background()); return err }
	drop := func(c *KVCatalog) error { return c.Delete(context.Background(), "iso", "app", "i1", "") }
	update := func(c *KVCatalog) error {
		return c.Update(context.Background(), "iso", "app", "i1", func(*DatabaseMeta) error { return nil })
	}
	claimPrefix := RootClaimKeyPrefix + "iso/"
	tests := []struct {
		name  string
		setup func(t *testing.T, c *KVCatalog, system, home *faultStore, homes *faultHomes)
		op    func(*KVCatalog) error
		want  func(error) bool
	}{
		{"release: claim delete fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Delete", claimPrefix, boom)
		}, release, isWrite},
		{"release: home lookup fails", func(_ *testing.T, _ *KVCatalog, _, _ *faultStore, homes *faultHomes) {
			homes.inHomeErr = map[string]error{"iso": boom}
		}, release, isStore},
		{"release: record read fails", func(_ *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			home.failOn("Get", CatalogKey("iso", "app"), boom)
		}, release, isBoom},
		{"release: record does not decode", func(t *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			if err := home.Put(context.Background(), CatalogKey("iso", "app"), []byte("{")); err != nil {
				t.Fatal(err)
			}
		}, release, isAny},
		{"create: index marker read fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Exists", rootClaimIndexedKey("iso"), boom)
		}, create, isRead},
		{"create: existing records scan fails", func(_ *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			home.failOn("Scan", CatalogKeyPrefix, boom)
		}, create, isRead},
		{"create: claim put fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Put", claimPrefix, boom)
		}, create, isWrite},
		{"create: record and claim release both fail", func(_ *testing.T, _ *KVCatalog, system, home *faultStore, _ *faultHomes) {
			home.failOn("PutIfAbsent", CatalogKey("iso", "app"), boom)
			system.failOn("Delete", claimPrefix, errors.New("release down"))
		}, create, func(err error) bool { return isBoom(err) && strings.Contains(err.Error(), "release down") }},
		{"create: duplicate and claim release fails", func(t *testing.T, c *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			if err := c.Create(context.Background(), app); err != nil {
				t.Fatal(err)
			}
			system.failOn("Delete", claimPrefix, boom)
		}, create, isWrite},
		{"list: claim does not decode", func(t *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			if err := system.Put(context.Background(), rootClaimKey("iso", "g1", "ghost"), []byte("{")); err != nil {
				t.Fatal(err)
			}
		}, listAll, func(err error) bool { return isWrite(err) && isRead(err) }},
		{"list: claim scan fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Scan", claimPrefix, boom)
		}, listAll, func(err error) bool { return isWrite(err) && isBoom(err) }},
		{"list: stale claim delete fails", func(t *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			putClaim(t, system, DatabaseMeta{TenantID: "iso", Name: "ghost", Incarnation: "g1", Backend: BackendMemory})
			system.failOn("CompareAndDelete", claimPrefix, boom)
		}, listAll, isWrite},
		{"list: index marker read fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Exists", rootClaimIndexedKey("iso"), boom)
		}, listAll, isRead},
		{"list: indexing claim put fails", func(t *testing.T, _ *KVCatalog, system, home *faultStore, _ *faultHomes) {
			putRecord(t, home, app)
			system.failOn("PutIfAbsent", claimPrefix, boom)
		}, listAll, isWrite},
		{"list: index marker write fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Put", rootClaimIndexedKey("iso"), boom)
		}, listAll, isWrite},
		{"list: home scan fails", func(_ *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			home.failOn("Scan", CatalogKeyPrefix, boom)
		}, listAll, isRead},
		{"list: system records scan fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Scan", CatalogKeyPrefix, boom)
		}, listAll, isRead},
		{"roots: system records scan fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			system.failOn("Scan", CatalogKeyPrefix, boom)
		}, listRoots, isRead},
		{"list: tenant ids fail", func(_ *testing.T, _ *KVCatalog, _, _ *faultStore, homes *faultHomes) {
			homes.idsErr = boom
		}, listAll, isStore},
		{"list: tenant home lookup fails", func(_ *testing.T, _ *KVCatalog, _, _ *faultStore, homes *faultHomes) {
			homes.inHomeErr = map[string]error{"iso": boom}
		}, listAll, isStore},
		{"roots: sealed tenant marker read fails", func(_ *testing.T, _ *KVCatalog, system, _ *faultStore, homes *faultHomes) {
			homes.extra = []string{"sealed"}
			homes.inHomeErr = map[string]error{"sealed": tenant.ErrTenantSealed}
			system.failOn("Exists", rootClaimIndexedKey("sealed"), boom)
		}, listRoots, isRead},
		{"roots: sealed tenant claim scan fails", func(t *testing.T, _ *KVCatalog, system, _ *faultStore, homes *faultHomes) {
			homes.extra = []string{"sealed"}
			homes.inHomeErr = map[string]error{"sealed": tenant.ErrTenantSealed}
			if err := system.Put(context.Background(), rootClaimIndexedKey("sealed"), []byte("1")); err != nil {
				t.Fatal(err)
			}
			system.failOn("Scan", RootClaimKeyPrefix+"sealed/", boom)
		}, listRoots, isRead},
		{"update: record does not decode", func(t *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			if err := home.Put(context.Background(), CatalogKey("iso", "app"), []byte("{")); err != nil {
				t.Fatal(err)
			}
		}, update, isRead},
		{"delete: record does not decode", func(t *testing.T, _ *KVCatalog, _, home *faultStore, _ *faultHomes) {
			if err := home.Put(context.Background(), CatalogKey("iso", "app"), []byte("{")); err != nil {
				t.Fatal(err)
			}
		}, drop, isRead},
		{"delete: record delete fails", func(t *testing.T, c *KVCatalog, _, home *faultStore, _ *faultHomes) {
			if err := c.Create(context.Background(), app); err != nil {
				t.Fatal(err)
			}
			home.failOn("CompareAndDelete", CatalogKey("iso", "app"), boom)
		}, drop, isWrite},
		{"delete: claim release fails", func(t *testing.T, c *KVCatalog, system, _ *faultStore, _ *faultHomes) {
			if err := c.Create(context.Background(), app); err != nil {
				t.Fatal(err)
			}
			system.failOn("Delete", claimPrefix, boom)
		}, drop, isWrite},
		{"delete: reap scan fails", func(t *testing.T, c *KVCatalog, _, home *faultStore, _ *faultHomes) {
			if err := c.Create(context.Background(), app); err != nil {
				t.Fatal(err)
			}
			home.failOn("Scan", CatalogKeyPrefix, boom)
		}, drop, isRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, system, home, homes := newFaultCatalog(t)
			tt.setup(t, cat, system, home, homes)
			if err := tt.op(cat); !tt.want(err) {
				t.Fatalf("err = %v (%T), not the expected failure", err, err)
			}
		})
	}
}

// TestKVCatalog_ListAllSkipsAndNamesTenants pins listAll's tenant walk: the
// system tenant and a shared tenant are not read from a home, and every
// sealed tenant is named in the *TenantsSkippedError.
func TestKVCatalog_ListAllSkipsAndNamesTenants(t *testing.T) {
	ctx := context.Background()
	cat, _, _, homes := newFaultCatalog(t)
	homes.extra = []string{tenantid.System, "shared", "s1", "s2"}
	homes.inHomeErr = map[string]error{"s1": tenant.ErrTenantSealed, "s2": tenant.ErrTenantSealed}
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create app: %v", err)
	}
	metas, err := cat.ListAll(ctx)
	var skipped *TenantsSkippedError
	if !errors.As(err, &skipped) || len(skipped.Tenants) != 2 {
		t.Fatalf("ListAll err = %v, want a *TenantsSkippedError naming s1 and s2", err)
	}
	if got, want := err.Error(), "database catalog: listing skipped sealed tenants: s1, s2"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatal("the skipped-tenants error does not match tenant.ErrTenantSealed")
	}
	if len(metas) != 1 || metas[0].Name != "app" {
		t.Fatalf("ListAll metas = %v, want app", metas)
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

func TestKVCatalog_CreateTimeoutAfterCommitKeepsTheClaim(t *testing.T) {
	ctx := context.Background()
	home := &indeterminateStore{MemoryKVStore: kvstore.NewMemoryKVStore(), armed: true}
	cat, system := newTestCatalog(t, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}})
	meta := DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory}

	err := cat.Create(ctx, meta)
	if !createOutcomeUnknown(err) {
		t.Fatalf("Create err = %v, want an outcome-unknown catalog write error", err)
	}
	if !claimExists(t, system, "iso", "i1", "app") {
		t.Fatal("a create whose record may have committed released its root claim")
	}
	if err := cat.ReleaseCreateClaim(ctx, meta); err != nil {
		t.Fatalf("ReleaseCreateClaim over the surviving record: %v", err)
	}
	if !claimExists(t, system, "iso", "i1", "app") {
		t.Fatal("ReleaseCreateClaim released the claim of a record that survives")
	}
	if err := home.Delete(ctx, CatalogKey("iso", "app")); err != nil {
		t.Fatalf("Delete record: %v", err)
	}
	if err := cat.ReleaseCreateClaim(ctx, meta); err != nil {
		t.Fatalf("ReleaseCreateClaim: %v", err)
	}
	if claimExists(t, system, "iso", "i1", "app") {
		t.Fatal("ReleaseCreateClaim kept the claim of an absent record")
	}
}

func TestManager_CreateCommitThenTimeoutFailedSettleThenSealKeepsRootChecked(t *testing.T) {
	ctx := context.Background()
	m, _, dir := newRootedManager(t)
	home := &indeterminateStore{MemoryKVStore: kvstore.NewMemoryKVStore(), armed: true}
	homes := &sealableHomes{
		isolatedHomes: isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}},
		sealed:        map[string]bool{},
	}
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), homes)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	getErr := errors.New("catalog unreachable")
	m.SetCatalog(&settleFaultCatalog{Catalog: cat, getErr: getErr})
	isoRoot := filepath.Join(dir, "iso-root", "app")

	_, err = m.Create(DatabaseConfig{TenantID: "iso", Name: "app", Backend: BackendMemory, DataDir: isoRoot})
	var orphan *CatalogOrphanError
	if !errors.As(err, &orphan) {
		t.Fatalf("Create err = %v, want *CatalogOrphanError", err)
	}
	m.SetCatalog(cat)
	homes.sealed["iso"] = true

	roots, err := cat.ListRoots(ctx)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	found := false
	for _, r := range roots {
		if r.TenantID == "iso" && r.Name == "app" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListRoots = %v, want the unsettled create's root", roots)
	}
	if _, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")}); err == nil {
		t.Fatal("a create over the unsettled record's root was not refused")
	}
}

func TestManager_CreateCommitThenTimeoutSettledReleasesTheClaim(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(&spyReclaimer{}))
	t.Cleanup(func() { _ = m.Close() })
	home := &indeterminateStore{MemoryKVStore: kvstore.NewMemoryKVStore(), armed: true}
	system := kvstore.NewMemoryKVStore()
	cat, err := NewKVCatalog(system, &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}})
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	m.SetCatalog(cat)

	if _, err := m.Create(DatabaseConfig{TenantID: "iso", Name: "app", Backend: BackendMemory}); err == nil {
		t.Fatal("Create over an indeterminate write succeeded")
	}
	var leftover []string
	if err := system.Scan(ctx, RootClaimKeyPrefix, func(key string, _ []byte) error {
		leftover = append(leftover, key)
		return nil
	}); err != nil {
		t.Fatalf("Scan claims: %v", err)
	}
	if len(leftover) != 0 {
		t.Fatalf("root claims after the settled create = %v, want none", leftover)
	}
}
