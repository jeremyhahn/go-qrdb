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

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

const bindingTenant = "acme"

func prefixTenants(id string) (database.BindingTenant, error) {
	return database.BindingTenant{ID: id, Isolation: tenant.IsolationPrefix}, nil
}

func newBindingManager(t *testing.T, dir string, enc *atrest.Encryptor) *database.Manager {
	t.Helper()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, enc, qrdbconfig.StorageConfig{}, prefixTenants)
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	return mgr
}

func materialized(t *testing.T, db *database.Database) *lifecycle.MaterializedBinding {
	t.Helper()
	mb, ok := db.Backend().(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("db.Backend() = %T, want *lifecycle.MaterializedBinding", db.Backend())
	}
	return mb
}

// readBack reopens a binding root with the standalone composition and
// returns the value of key, or an error.
func readBack(t *testing.T, b database.BackendType, root string, enc *atrest.Encryptor, key string) ([]byte, error) {
	t.Helper()
	stg, pdb, err := lifecycle.NewLocalStorage(b, root, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = stg.Close() }()
	eng, err := lifecycle.NewLocalEngine(b, stg, pdb, watch.NewHub(), lease.NewLessor())
	if err != nil {
		return nil, err
	}
	local := backend.NewLocalBackend(eng)
	defer func() { _ = local.Close() }()
	return service.NewKVService(local, shard.ShardKV).Get(context.Background(), key)
}

func TestLocalBindingFactory_OwnStorePerBackend(t *testing.T) {
	ctx := context.Background()
	for _, b := range []database.BackendType{database.BackendMemory, database.BackendFile, database.BackendPebble, database.BackendObjstore} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			mgr := newBindingManager(t, dir, nil)
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "orders", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			if mb.Binding().Backend != b {
				t.Fatalf("Binding().Backend = %q, want %q", mb.Binding().Backend, b)
			}
			wantRoot := filepath.Join(dir, "tenants", bindingTenant, "databases", "orders")
			if b == database.BackendMemory {
				wantRoot = ""
			}
			if mb.DataRoot() != wantRoot {
				t.Fatalf("DataRoot() = %q, want %q", mb.DataRoot(), wantRoot)
			}
			if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k1", []byte("v1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := mgr.Close(); err != nil {
				t.Fatalf("Manager.Close: %v", err)
			}
			if b == database.BackendMemory {
				return
			}
			got, err := readBack(t, b, wantRoot, nil, "k1")
			if err != nil || !bytes.Equal(got, []byte("v1")) {
				t.Fatalf("read back = %q, %v; want v1", got, err)
			}
			entries, err := os.ReadDir(wantRoot)
			if err != nil || len(entries) == 0 {
				t.Fatalf("binding root %s is empty (%v)", wantRoot, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "databases", "orders")); !os.IsNotExist(err) {
				t.Fatalf("shared databases/orders exists (%v)", err)
			}
		})
	}
}

func TestLocalBindingFactory_EncryptedUnderNodeEncryptor(t *testing.T) {
	ctx := context.Background()
	enc := atrest.New(newUnsealedBarrier(t))
	other := atrest.New(newUnsealedBarrier(t))
	for _, b := range []database.BackendType{database.BackendPebble, database.BackendFile} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			mgr := newBindingManager(t, dir, enc)
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "secret", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			root := mb.DataRoot()
			value := []byte("binding-at-rest-plaintext-value")
			if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", value); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := mgr.Close(); err != nil {
				t.Fatalf("Manager.Close: %v", err)
			}
			if got, err := readBack(t, b, root, other, "k"); err == nil && bytes.Equal(got, value) {
				t.Fatal("a different key read the plaintext back")
			}
			got, err := readBack(t, b, root, enc, "k")
			if err != nil || !bytes.Equal(got, value) {
				t.Fatalf("read under the node key = %q, %v; want %q", got, err, value)
			}
		})
	}
}

func TestLocalBindingFactory_NonQualifyingDelegates(t *testing.T) {
	barrierTenants := func(id string) (database.BindingTenant, error) {
		return database.BindingTenant{ID: id, Isolation: tenant.IsolationBarrier}, nil
	}
	cases := []struct {
		name    string
		node    database.BindingNode
		tenants func(string) (database.BindingTenant, error)
		cfg     database.DatabaseConfig
	}{
		{"barrier tenant", database.BindingNode{}, barrierTenants,
			database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble}},
		{"database barrier", database.BindingNode{}, prefixTenants,
			database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble,
				Barrier: &database.DatabaseBarrierConfig{Enabled: true}}},
		{"system database", database.BindingNode{}, prefixTenants,
			database.DatabaseConfig{TenantID: "", Name: lifecycle.SystemDatabase, Backend: database.BackendPebble}},
		{"system tenant home", database.BindingNode{}, prefixTenants,
			database.DatabaseConfig{TenantID: "", Name: database.TenantHomeName, Backend: database.BackendPebble}},
		{"explicit data_dir with database barrier", database.BindingNode{}, prefixTenants,
			database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble, DataDir: "explicit",
				Barrier: &database.DatabaseBarrierConfig{Enabled: true}}},
		{"raft mode node", database.BindingNode{RaftMode: true}, prefixTenants,
			database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			node := tc.node
			node.DataDir = dir
			f := lifecycle.NewLocalBindingFactory(node, nil, qrdbconfig.StorageConfig{}, tc.tenants)
			cfg := tc.cfg
			if cfg.DataDir != "" {
				cfg.DataDir = filepath.Join(dir, cfg.DataDir)
			}
			be, err := f.Create(cfg, filepath.Join(dir, "shared"))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			defer func() { _ = be.Close() }()
			if _, ok := be.(*lifecycle.MaterializedBinding); ok {
				t.Fatal("a non-qualifying database was materialized")
			}
		})
	}
}

func TestLocalBindingFactory_ErrorsTyped(t *testing.T) {
	t.Run("resolve binding", func(t *testing.T) {
		f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
		_, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendMemory,
			Barrier: &database.DatabaseBarrierConfig{Enabled: true}}, t.TempDir())
		var want *database.BarrierRequiresPersistentBackendError
		if !errors.As(err, &want) {
			t.Fatalf("err = %v (%T), want *database.BarrierRequiresPersistentBackendError", err, err)
		}
	})
	t.Run("tenant lookup", func(t *testing.T) {
		lookupErr := errors.New("tenant store down")
		f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{},
			func(string) (database.BindingTenant, error) { return database.BindingTenant{}, lookupErr })
		_, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble}, t.TempDir())
		var want *lifecycle.BindingOpenError
		if !errors.As(err, &want) || !errors.Is(err, lookupErr) {
			t.Fatalf("err = %v (%T), want *lifecycle.BindingOpenError wrapping the lookup error", err, err)
		}
	})
	t.Run("unopenable root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "tenants"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		mgr := newBindingManager(t, dir, nil)
		defer func() { _ = mgr.Close() }()
		_, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
		var want *lifecycle.BindingOpenError
		if !errors.As(err, &want) {
			t.Fatalf("err = %v (%T), want *lifecycle.BindingOpenError", err, err)
		}
		if _, ok := mgr.Local("d", bindingTenant); ok {
			t.Fatal("the Manager recorded a database whose binding failed to open")
		}
	})
}

func TestLocalBindingFactory_DropRemovesRoot(t *testing.T) {
	dir := t.TempDir()
	mgr := newBindingManager(t, dir, nil)
	defer func() { _ = mgr.Close() }()
	ctx := context.Background()
	cfg := database.DatabaseConfig{TenantID: bindingTenant, Name: "gone", Backend: database.BackendPebble}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mb := materialized(t, db)
	root := mb.DataRoot()
	if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := mgr.DropForTenant(tenant.WithTenantID(ctx, bindingTenant), "gone"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("binding root %s survives the drop (%v)", root, err)
	}
	db, err = mgr.Create(cfg)
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	mb = materialized(t, db)
	if got, err := service.NewKVService(mb.Host(), mb.ShardID()).Get(ctx, "k"); err == nil && got != nil {
		t.Fatalf("recreated database holds the dropped key: %q", got)
	}
}

// isolationTenantService answers GetTenant with one isolation for every
// tenant; every other TenantServicer method is unused here.
type isolationTenantService struct {
	tenant.TenantServicer
	isolation tenant.IsolationType
}

func (s *isolationTenantService) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationType: s.isolation}, nil
}

func newLookupManager(t *testing.T, get func() tenant.TenantServicer) *database.Manager {
	t.Helper()
	dir := t.TempDir()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{},
		lifecycle.TenantServiceBindingLookup(get))
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// Before the tenant service exists the lookup fails closed: the create is
// refused with a typed error and nothing is recorded, never opened as if the
// tenant were prefix.
func TestTenantServiceBindingLookup_FailsClosedWithoutService(t *testing.T) {
	mgr := newLookupManager(t, func() tenant.TenantServicer { return nil })
	_, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
	var open *lifecycle.BindingOpenError
	var unavailable *lifecycle.BindingTenantUnavailableError
	if !errors.As(err, &open) || !errors.As(err, &unavailable) {
		t.Fatalf("err = %v (%T), want *lifecycle.BindingOpenError wrapping *lifecycle.BindingTenantUnavailableError", err, err)
	}
	if unavailable.TenantID != bindingTenant {
		t.Fatalf("TenantID = %q, want %q", unavailable.TenantID, bindingTenant)
	}
	if _, ok := mgr.Local("d", bindingTenant); ok {
		t.Fatal("the Manager recorded a database whose tenant could not be read")
	}
}

// A barrier tenant's database is delegated to the shared factory; a prefix
// tenant's is materialized in its own store.
func TestTenantServiceBindingLookup_BarrierTenantDelegates(t *testing.T) {
	barrier := newLookupManager(t, func() tenant.TenantServicer {
		return &isolationTenantService{isolation: tenant.IsolationBarrier}
	})
	db, err := barrier.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
	if err != nil {
		t.Fatalf("barrier Create: %v", err)
	}
	if _, ok := db.Backend().(*lifecycle.MaterializedBinding); ok {
		t.Fatal("a barrier tenant's database was materialized in a node-encrypted binding store")
	}

	prefix := newLookupManager(t, func() tenant.TenantServicer {
		return &isolationTenantService{isolation: tenant.IsolationPrefix}
	})
	db, err = prefix.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
	if err != nil {
		t.Fatalf("prefix Create: %v", err)
	}
	materialized(t, db)
}

// newBarrierTenantManager builds a Manager whose factory reads every tenant
// as barrier-isolated and takes each tenant's encryptor from tenantEnc.
func newBarrierTenantManager(t *testing.T, dir string, nodeEnc *atrest.Encryptor, tenantEnc func(string) (*atrest.Encryptor, error)) *database.Manager {
	t.Helper()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nodeEnc, qrdbconfig.StorageConfig{},
		func(id string) (database.BindingTenant, error) {
			return database.BindingTenant{ID: id, Isolation: tenant.IsolationBarrier}, nil
		}).WithTenantEncryptors(tenantEnc)
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	return mgr
}

// A barrier tenant's database is materialized under its binding root and
// written through the tenant's own barrier: the node encryptor cannot read
// it back, the tenant's can.
func TestLocalBindingFactory_BarrierTenantEncryptedUnderTenantBarrier(t *testing.T) {
	ctx := context.Background()
	nodeEnc := atrest.New(newUnsealedBarrier(t))
	tenantEnc := atrest.New(newUnsealedBarrier(t))
	for _, b := range []database.BackendType{database.BackendPebble, database.BackendFile} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			var asked []string
			mgr := newBarrierTenantManager(t, dir, nodeEnc, func(id string) (*atrest.Encryptor, error) {
				asked = append(asked, id)
				return tenantEnc, nil
			})
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			root := mb.DataRoot()
			if want := filepath.Join(dir, "tenants", bindingTenant, "databases", "vault"); root != want {
				t.Fatalf("DataRoot = %q, want %q", root, want)
			}
			if len(asked) != 1 || asked[0] != bindingTenant {
				t.Fatalf("tenant encryptor lookups = %v, want [%q]", asked, bindingTenant)
			}
			value := []byte("barrier-tenant-plaintext-value")
			if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", value); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := mgr.Close(); err != nil {
				t.Fatalf("Manager.Close: %v", err)
			}
			if got, err := readBack(t, b, root, nodeEnc, "k"); err == nil && bytes.Equal(got, value) {
				t.Fatal("the node encryptor read a barrier tenant's value back")
			}
			got, err := readBack(t, b, root, tenantEnc, "k")
			if err != nil || !bytes.Equal(got, value) {
				t.Fatalf("read under the tenant barrier = %q, %v; want %q", got, err, value)
			}
		})
	}
}

// A barrier tenant whose encryptor cannot be had (sealed barrier) fails
// closed: the create is refused with a typed error and nothing is recorded.
// Without WithTenantEncryptors a barrier tenant stays delegated.
func TestLocalBindingFactory_BarrierTenantFailsClosed(t *testing.T) {
	t.Run("sealed", func(t *testing.T) {
		dir := t.TempDir()
		mgr := newBarrierTenantManager(t, dir, atrest.New(newUnsealedBarrier(t)), func(id string) (*atrest.Encryptor, error) {
			return nil, &lifecycle.BindingBarrierSealedError{TenantID: id}
		})
		defer func() { _ = mgr.Close() }()
		_, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
		var open *lifecycle.BindingOpenError
		var sealed *lifecycle.BindingBarrierSealedError
		if !errors.As(err, &open) || !errors.As(err, &sealed) {
			t.Fatalf("err = %v (%T), want *lifecycle.BindingOpenError wrapping *lifecycle.BindingBarrierSealedError", err, err)
		}
		if _, ok := mgr.Local("d", bindingTenant); ok {
			t.Fatal("the Manager recorded a database whose tenant barrier is sealed")
		}
	})
	t.Run("no tenant encryptors", func(t *testing.T) {
		mgr := newLookupManager(t, func() tenant.TenantServicer {
			return &isolationTenantService{isolation: tenant.IsolationBarrier}
		})
		db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "d", Backend: database.BackendPebble})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, ok := db.Backend().(*lifecycle.MaterializedBinding); ok {
			t.Fatal("a barrier tenant's database was materialized with no tenant encryptor lookup")
		}
	})
}

func TestTenantBarrierEncryptorLookup_Typed(t *testing.T) {
	const id = bindingTenant
	t.Run("nil registry", func(t *testing.T) {
		for name, get := range map[string]func() *seal.BarrierRegistry{
			"nil get":      nil,
			"nil registry": func() *seal.BarrierRegistry { return nil },
		} {
			_, err := lifecycle.TenantBarrierEncryptorLookup(get)(id)
			var want *lifecycle.BindingBarrierUnavailableError
			if !errors.As(err, &want) || want.TenantID != id {
				t.Fatalf("%s: err = %v (%T), want *lifecycle.BindingBarrierUnavailableError for %q", name, err, err, id)
			}
		}
	})
	reg, err := seal.NewBarrierRegistry(slog.Default(), newUnsealedBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	lookup := lifecycle.TenantBarrierEncryptorLookup(func() *seal.BarrierRegistry { return reg })
	if _, err := lookup(id); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("unknown tenant: err = %v, want seal.ErrTenantNotFound", err)
	}
	if _, err := reg.RegisterTenantDefault(id); err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	tb := seal.WrapTenantBarrier(id, newUnsealedBarrier(t))
	if err := reg.ReplaceTenantBarrier(id, tb); err != nil {
		t.Fatalf("ReplaceTenantBarrier: %v", err)
	}
	enc, err := lookup(id)
	if err != nil || enc == nil || !enc.Enabled() {
		t.Fatalf("registered tenant: enc = %v, err = %v; want an enabled encryptor", enc, err)
	}
	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, err = lookup(id)
	var sealed *lifecycle.BindingBarrierSealedError
	if !errors.As(err, &sealed) || sealed.TenantID != id {
		t.Fatalf("sealed tenant: err = %v (%T), want *lifecycle.BindingBarrierSealedError for %q", err, err, id)
	}
}

// newManagerWithTenants is newBindingManager with a caller's tenant lookup.
func newManagerWithTenants(t *testing.T, dir string, enc *atrest.Encryptor, tenants func(string) (database.BindingTenant, error)) *database.Manager {
	t.Helper()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, enc, qrdbconfig.StorageConfig{}, tenants)
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	return mgr
}

func TestLocalBindingFactory_SystemTenantDatabaseOwnStore(t *testing.T) {
	ctx := context.Background()
	nodeEnc := atrest.New(newUnsealedBarrier(t))
	for _, tenantID := range []string{"", "system"} {
		for _, b := range []database.BackendType{database.BackendPebble, database.BackendFile} {
			t.Run("tenant="+tenantID+"/"+string(b), func(t *testing.T) {
				dir := t.TempDir()
				mgr := newManagerWithTenants(t, dir, nodeEnc, func(id string) (database.BindingTenant, error) {
					t.Fatalf("the system tenant was looked up (%q)", id)
					return database.BindingTenant{}, nil
				})
				db, err := mgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: "ledger", Backend: b})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				mb := materialized(t, db)
				root := mb.DataRoot()
				if want := filepath.Join(dir, "tenants", "system", "databases", "ledger"); root != want {
					t.Fatalf("DataRoot = %q, want %q", root, want)
				}
				if got := mb.Binding(); got.TenantID != "system" || got.Encryptor != database.EncryptorNode {
					t.Fatalf("binding tenant = %q, encryptor = %q; want system, %q", got.TenantID, got.Encryptor, database.EncryptorNode)
				}
				value := []byte("system-tenant-value")
				if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", value); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if err := mgr.Close(); err != nil {
					t.Fatalf("Manager.Close: %v", err)
				}
				got, err := readBack(t, b, root, nodeEnc, "k")
				if err != nil || !bytes.Equal(got, value) {
					t.Fatalf("read under the node key = %q, %v; want %q", got, err, value)
				}
			})
		}
	}
}

func TestLocalBindingFactory_ExplicitDataDirOwnStore(t *testing.T) {
	ctx := context.Background()
	for _, tenantID := range []string{"", bindingTenant} {
		t.Run("tenant="+tenantID, func(t *testing.T) {
			mgr := newBindingManager(t, t.TempDir(), nil)
			explicit := filepath.Join(t.TempDir(), "x")
			cfg := database.DatabaseConfig{TenantID: tenantID, Name: "placed", Backend: database.BackendPebble, DataDir: explicit}
			db, err := mgr.Create(cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			if root := mb.DataRoot(); root != explicit {
				t.Fatalf("DataRoot = %q, want the explicit data_dir %q", root, explicit)
			}
			value := []byte("explicit-dir-value")
			if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", value); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := mgr.Close(); err != nil {
				t.Fatalf("Manager.Close: %v", err)
			}
			got, err := readBack(t, database.BackendPebble, explicit, nil, "k")
			if err != nil || !bytes.Equal(got, value) {
				t.Fatalf("read back from the explicit data_dir = %q, %v; want %q", got, err, value)
			}

			mgr = newBindingManager(t, t.TempDir(), nil)
			defer func() { _ = mgr.Close() }()
			gone := filepath.Join(t.TempDir(), "x")
			cfg.Name, cfg.DataDir = "gone", gone
			db, err = mgr.Create(cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb = materialized(t, db)
			if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", value); err != nil {
				t.Fatalf("Put: %v", err)
			}
			dropCtx := ctx
			if tenantID != "" {
				dropCtx = tenant.WithTenantID(ctx, tenantID)
			}
			if err := mgr.DropForTenant(dropCtx, "gone"); err != nil {
				t.Fatalf("DropForTenant: %v", err)
			}
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Fatalf("explicit data_dir %s survives the drop (%v)", gone, err)
			}
		})
	}
}
