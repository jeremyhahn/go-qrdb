package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// fakeCatalogHomes routes the tenants in homes to their own store; every
// other tenant is not isolated.
type fakeCatalogHomes struct {
	homes     map[string]kvstore.KVStore
	locateErr error
	resolveEr error
}

var _ CatalogHomes = (*fakeCatalogHomes)(nil)

func (f *fakeCatalogHomes) InHome(_ context.Context, tenantID string) (bool, error) {
	if f.locateErr != nil {
		return false, f.locateErr
	}
	_, ok := f.homes[tenantID]
	return ok, nil
}

func (f *fakeCatalogHomes) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if f.resolveEr != nil {
		return nil, f.resolveEr
	}
	return f.homes[tenantID], nil
}

// plainKVStore hides a store's atomic methods.
type plainKVStore struct{ kvstore.KVStore }

func newTestCatalog(t *testing.T, homes CatalogHomes) (*KVCatalog, *kvstore.MemoryKVStore) {
	t.Helper()
	system := kvstore.NewMemoryKVStore()
	cat, err := NewKVCatalog(system, homes)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	return cat, system
}

func TestCatalogKey_SystemSpellingsShareAKey(t *testing.T) {
	if CatalogKey("", "db") != CatalogKey("system", "db") {
		t.Fatalf("CatalogKey(\"\") = %q, CatalogKey(system) = %q", CatalogKey("", "db"), CatalogKey("system", "db"))
	}
	if got, want := CatalogKey("acme", "orders"), "__sys:dbcatalog/acme/orders"; got != want {
		t.Fatalf("CatalogKey = %q, want %q", got, want)
	}
}

func TestNewKVCatalog_RequiresSystemStore(t *testing.T) {
	_, err := NewKVCatalog(nil, nil)
	var want *CatalogStoreRequiredError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *CatalogStoreRequiredError", err)
	}
}

func TestKVCatalog_RoundTrip(t *testing.T) {
	ctx := context.Background()
	cat, _ := newTestCatalog(t, nil)
	meta := DatabaseMeta{TenantID: "acme", Name: "orders", Backend: BackendMemory, Incarnation: "i1"}
	if err := cat.Create(ctx, meta); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := cat.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "orders" || got.Incarnation != "i1" || got.Backend != BackendMemory {
		t.Fatalf("Get = %+v", got)
	}
	meta.Dropping = true
	if err := cat.Update(ctx, meta.TenantID, meta.Name, meta.Incarnation, replaceRecord(meta)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ = cat.Get(ctx, "acme", "orders"); !got.Dropping {
		t.Fatal("Update did not replace the record")
	}
	if err := cat.Delete(ctx, "acme", "orders", "i1", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var nf *NotFoundError
	if _, err := cat.Get(ctx, "acme", "orders"); !errors.As(err, &nf) {
		t.Fatalf("Get after Delete: err = %v, want *NotFoundError", err)
	}
	if err := cat.Delete(ctx, "acme", "orders", "i1", ""); err != nil {
		t.Fatalf("Delete of an absent record: %v", err)
	}
}

func TestKVCatalog_CreateRefusesAnExistingRecord(t *testing.T) {
	ctx := context.Background()
	cat, _ := newTestCatalog(t, nil)
	meta := DatabaseMeta{Name: "db", Backend: BackendMemory}
	if err := cat.Create(ctx, meta); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var exists *AlreadyExistsError
	if err := cat.Create(ctx, meta); !errors.As(err, &exists) {
		t.Fatalf("second Create: err = %v, want *AlreadyExistsError", err)
	}
}

func TestKVCatalog_ConcurrentCreatesHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	cat, _ := newTestCatalog(t, nil)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cat.Create(ctx, DatabaseMeta{Name: "db", Backend: BackendMemory}) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d creates succeeded, want 1", wins.Load())
	}
}

func TestKVCatalog_RefusesUnknownFields(t *testing.T) {
	ctx := context.Background()
	cat, system := newTestCatalog(t, nil)
	key := CatalogKey("", "db")
	if err := system.Put(ctx, key, []byte(`{"name":"db","backend":"memory","future":1}`)); err != nil {
		t.Fatal(err)
	}
	var readErr *CatalogReadError
	if _, err := cat.Get(ctx, "", "db"); !errors.As(err, &readErr) {
		t.Fatalf("Get: err = %v, want *CatalogReadError", err)
	}
	if _, err := cat.List(ctx); !errors.As(err, &readErr) {
		t.Fatalf("List: err = %v, want *CatalogReadError", err)
	}
}

func TestKVCatalog_IsolatedTenantLivesInItsHomeStore(t *testing.T) {
	ctx := context.Background()
	home := kvstore.NewMemoryKVStore()
	cat, system := newTestCatalog(t, &fakeCatalogHomes{homes: map[string]kvstore.KVStore{"iso": home}})
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "a", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create isolated: %v", err)
	}
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "shared", Name: "b", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create shared: %v", err)
	}
	if v, _ := system.Get(ctx, CatalogKey("iso", "a")); v != nil {
		t.Fatal("an isolated tenant's record is in the system store")
	}
	if v, _ := home.Get(ctx, CatalogKey("iso", "a")); v == nil {
		t.Fatal("an isolated tenant's record is not in its home store")
	}
	all, err := cat.List(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "b" {
		t.Fatalf("List = %+v, %v; want only the shared tenant's record", all, err)
	}
	iso, err := cat.ListTenant(ctx, "iso")
	if err != nil || len(iso) != 1 || iso[0].Name != "a" {
		t.Fatalf("ListTenant(iso) = %+v, %v", iso, err)
	}
}

func TestKVCatalog_ListTenantDoesNotMatchAPrefixTenant(t *testing.T) {
	ctx := context.Background()
	cat, _ := newTestCatalog(t, nil)
	for _, tid := range []string{"acme", "acme2"} {
		if err := cat.Create(ctx, DatabaseMeta{TenantID: tid, Name: "db", Backend: BackendMemory}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := cat.ListTenant(ctx, "acme")
	if err != nil || len(got) != 1 || got[0].TenantID != "acme" {
		t.Fatalf("ListTenant(acme) = %+v, %v", got, err)
	}
}

func TestKVCatalog_HomeResolutionFailsClosed(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("sealed")
	meta := DatabaseMeta{TenantID: "iso", Name: "a", Backend: BackendMemory}
	cases := map[string]*fakeCatalogHomes{
		"locate":  {homes: map[string]kvstore.KVStore{"iso": nil}, locateErr: boom},
		"resolve": {homes: map[string]kvstore.KVStore{"iso": nil}, resolveEr: boom},
	}
	for name, homes := range cases {
		t.Run(name, func(t *testing.T) {
			cat, system := newTestCatalog(t, homes)
			var storeErr *CatalogStoreError
			if err := cat.Create(ctx, meta); !errors.As(err, &storeErr) || !errors.Is(err, boom) {
				t.Fatalf("Create: err = %v, want *CatalogStoreError wrapping the cause", err)
			}
			if _, err := cat.Get(ctx, "iso", "a"); !errors.As(err, &storeErr) {
				t.Fatalf("Get: err = %v", err)
			}
			if _, err := cat.ListTenant(ctx, "iso"); !errors.As(err, &storeErr) {
				t.Fatalf("ListTenant: err = %v", err)
			}
			if err := cat.Delete(ctx, "iso", "a", "", ""); !errors.As(err, &storeErr) {
				t.Fatalf("Delete: err = %v", err)
			}
			if err := cat.Update(ctx, meta.TenantID, meta.Name, meta.Incarnation, replaceRecord(meta)); !errors.As(err, &storeErr) {
				t.Fatalf("Update: err = %v", err)
			}
			if v, _ := system.Get(ctx, CatalogKey("iso", "a")); v != nil {
				t.Fatal("a failed home resolution fell back to the system store")
			}
		})
	}
}

func TestKVCatalog_HomeStoreWithoutPutIfAbsentIsRefused(t *testing.T) {
	ctx := context.Background()
	homes := &fakeCatalogHomes{homes: map[string]kvstore.KVStore{"iso": plainKVStore{kvstore.NewMemoryKVStore()}}}
	cat, _ := newTestCatalog(t, homes)
	var notAtomic *CatalogStoreNotAtomicError
	if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "a"}); !errors.As(err, &notAtomic) {
		t.Fatalf("Create: err = %v, want *CatalogStoreNotAtomicError", err)
	}
}

func TestManager_SetCatalog(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	var required *CatalogRequiredError
	if _, err := m.catalog("Open"); !errors.As(err, &required) || required.Operation != "Open" {
		t.Fatalf("catalog() with none attached: err = %v, want *CatalogRequiredError", err)
	}
	cat, _ := newTestCatalog(t, nil)
	m.SetCatalog(cat)
	if got, err := m.catalog("Open"); err != nil || got != Catalog(cat) {
		t.Fatalf("catalog() = %v, %v; want the attached catalog", got, err)
	}
	m.SetCatalog(nil)
	if _, err := m.catalog("Open"); !errors.As(err, &required) {
		t.Fatalf("catalog() after detach: err = %v, want *CatalogRequiredError", err)
	}
}

func TestCatalogErrors_Messages(t *testing.T) {
	cause := errors.New("cause")
	errs := []error{
		&CatalogReadError{Key: "k", Err: cause},
		&CatalogWriteError{Key: "k", Err: cause},
		&CatalogStoreError{TenantID: "t", Err: cause},
	}
	for _, err := range errs {
		if !errors.Is(err, cause) || err.Error() == "" {
			t.Fatalf("%T: does not wrap its cause or has no message", err)
		}
	}
	for _, err := range []error{&CatalogStoreNotAtomicError{TenantID: "t"}, &CatalogStoreRequiredError{}, &CatalogRequiredError{Operation: "Open"}} {
		if err.Error() == "" {
			t.Fatalf("%T has no message", err)
		}
	}
}

func TestAmbiguousNameError_NamesTheDatabase(t *testing.T) {
	err := &AmbiguousNameError{Name: "orders"}
	if !strings.Contains(err.Error(), `"orders"`) {
		t.Fatalf("Error() = %q, want the database name", err.Error())
	}
}

// failingCatalogStore is an AtomicKVStore whose every read and write fails
// with err.
type failingCatalogStore struct {
	*kvstore.MemoryKVStore
	err error
}

func (s failingCatalogStore) Put(context.Context, string, []byte) error { return s.err }

func (s failingCatalogStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, s.err
}

func (s failingCatalogStore) Get(context.Context, string) ([]byte, error) { return nil, s.err }

func (s failingCatalogStore) Delete(context.Context, string) error { return s.err }

func (s failingCatalogStore) Scan(context.Context, string, func(string, []byte) error) error {
	return s.err
}

func TestKVCatalog_StoreFailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("store unavailable")
	cat, err := NewKVCatalog(failingCatalogStore{MemoryKVStore: kvstore.NewMemoryKVStore(), err: boom}, nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := DatabaseMeta{Name: "orders", Backend: BackendMemory}
	var writeErr *CatalogWriteError
	var readErr *CatalogReadError
	writes := map[string]error{
		"Create": cat.Create(ctx, meta),
	}
	for op, err := range writes {
		if !errors.As(err, &writeErr) || !errors.Is(err, boom) || writeErr.Key != CatalogKey("", "orders") {
			t.Fatalf("%s: err = %v, want *CatalogWriteError for the record wrapping the cause", op, err)
		}
	}
	if _, err := cat.Get(ctx, "", "orders"); !errors.As(err, &readErr) || !errors.Is(err, boom) {
		t.Fatalf("Get: err = %v, want *CatalogReadError wrapping the cause", err)
	}
	// Update and Delete read the record they guard before they write.
	if err := cat.Update(ctx, meta.TenantID, meta.Name, meta.Incarnation, replaceRecord(meta)); !errors.As(err, &readErr) || !errors.Is(err, boom) {
		t.Fatalf("Update: err = %v, want *CatalogReadError wrapping the cause", err)
	}
	if err := cat.Delete(ctx, meta.TenantID, meta.Name, meta.Incarnation, ""); !errors.As(err, &readErr) || !errors.Is(err, boom) {
		t.Fatalf("Delete: err = %v, want *CatalogReadError wrapping the cause", err)
	}
	if _, err := cat.List(ctx); !errors.As(err, &readErr) || !errors.Is(err, boom) || readErr.Key != CatalogKeyPrefix {
		t.Fatalf("List: err = %v, want *CatalogReadError for the prefix wrapping the cause", err)
	}
}

// TestKVCatalog_DeleteRemovesOnlyTheNamedIncarnationUnderItsClaim checks
// Delete leaves a record of another incarnation (*NotFoundError) or under
// another drop's claim (*DropInProgressError), and removes the one it names.
func TestKVCatalog_DeleteRemovesOnlyTheNamedIncarnationUnderItsClaim(t *testing.T) {
	ctx := context.Background()
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	meta := DatabaseMeta{Name: "orders", TenantID: "acme", Backend: BackendMemory, Incarnation: "i2", Dropping: true, DropClaim: "c2"}
	if err := cat.Create(ctx, meta); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var nf *NotFoundError
	if err := cat.Delete(ctx, "acme", "orders", "i1", "c2"); !errors.As(err, &nf) {
		t.Fatalf("Delete of another incarnation: err = %v, want *NotFoundError", err)
	}
	var taken *DropInProgressError
	if err := cat.Delete(ctx, "acme", "orders", "i2", "c1"); !errors.As(err, &taken) {
		t.Fatalf("Delete under another claim: err = %v, want *DropInProgressError", err)
	}
	if _, err := cat.Get(ctx, "acme", "orders"); err != nil {
		t.Fatalf("a refused Delete removed the record: %v", err)
	}
	if err := cat.Delete(ctx, "acme", "orders", "i2", "c2"); err != nil {
		t.Fatalf("Delete under its own claim: %v", err)
	}
	if _, err := cat.Get(ctx, "acme", "orders"); !errors.As(err, &nf) {
		t.Fatalf("Get after Delete: err = %v, want *NotFoundError", err)
	}
}

func TestManager_OpenAll_RequiresACatalog(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	var required *CatalogRequiredError
	if err := m.OpenAll(context.Background()); !errors.As(err, &required) {
		t.Fatalf("OpenAll with no catalog: err = %v, want *CatalogRequiredError", err)
	}
}

func TestManager_OpenAll_UnreadableCatalogFails(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	cat, system := newTestCatalog(t, nil)
	if err := system.Put(context.Background(), CatalogKey("", "orders"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	m.SetCatalog(cat)
	var readErr *CatalogReadError
	if err := m.OpenAll(context.Background()); !errors.As(err, &readErr) || readErr.Key != CatalogKey("", "orders") {
		t.Fatalf("OpenAll over a corrupt record: err = %v, want *CatalogReadError naming it", err)
	}
	if _, err := m.Get("orders"); err == nil {
		t.Fatal("a database whose record could not be read was opened")
	}
}

func TestManager_OpenAll_KeepsTheOpenDatabase(t *testing.T) {
	m := newCatalogManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	created, err := m.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.OpenAll(context.Background()); err != nil {
		t.Fatalf("OpenAll: %v", err)
	}
	got, err := m.Get("orders")
	if err != nil || got != created {
		t.Fatalf("Get after OpenAll = %p, %v; want the database already open (%p)", got, err, created)
	}
}

func TestManager_UpdateWithoutACatalogIsRefused(t *testing.T) {
	m := newCatalogManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory}); err != nil {
		t.Fatal(err)
	}
	m.SetCatalog(nil)
	var required *CatalogRequiredError
	if err := m.UpdateReplicasForTenant(context.Background(), "orders", 3); !errors.As(err, &required) {
		t.Fatalf("UpdateReplicas with the catalog detached: err = %v, want *CatalogRequiredError", err)
	}
}
