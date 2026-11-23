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
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
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
	host, closeHost, err := openBack(t, b, root, enc)
	if err != nil {
		return nil, err
	}
	defer closeHost()
	return service.NewKVService(host, shard.ShardKV).Get(context.Background(), key)
}

// openBack reopens a binding root with the standalone composition under enc
// and returns its host and a func that closes the host and then the storage.
func openBack(t *testing.T, b database.BackendType, root string, enc *atrest.Encryptor) (service.Host, func(), error) {
	t.Helper()
	stg, pdb, err := lifecycle.NewLocalStorage(b, root, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		return nil, nil, err
	}
	eng, err := lifecycle.NewLocalEngine(b, stg, pdb, watch.NewHub(), lease.NewLessor())
	if err != nil {
		_ = stg.Close()
		return nil, nil, err
	}
	local := backend.NewLocalBackend(eng)
	return local, func() {
		_ = local.Close()
		_ = stg.Close()
	}, nil
}

// failingLookup is a shared engine whose every read fails.
type failingLookup struct {
	backend.SMAdapter
	err error
}

func (f failingLookup) Lookup(context.Context, interface{}) (interface{}, error) { return nil, f.err }

func TestSharedStoreKeyspaceProbe_ReportsOnlyItsNamespace(t *testing.T) {
	ctx := context.Background()
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, watch.NewHub(), lease.NewLessor())
	if err != nil {
		_ = stg.Close()
		t.Fatalf("NewLocalEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() {
		_ = host.Close()
		_ = stg.Close()
	})
	probe := lifecycle.SharedStoreKeyspaceProbe(eng)

	if got, err := probe("acme", "orders"); err != nil || got {
		t.Fatalf("empty store: probe = %v, %v; want false, nil", got, err)
	}

	key := tenant.ComposeNamespace("acme", "orders") + "k"
	if err := service.NewKVService(host, shard.ShardKV).Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	if got, err := probe("acme", "orders"); err != nil || !got {
		t.Errorf("acme/orders: probe = %v, %v; want true, nil", got, err)
	}
	// "order" is a prefix of "orders" but a different database: the
	// namespace's trailing separator keeps its probe off orders' keys.
	for _, other := range [][2]string{{"acme", "order"}, {"acme", "orders2"}, {"acm", "orders"}, {"acme2", "orders"}} {
		if got, err := probe(other[0], other[1]); err != nil || got {
			t.Errorf("%s/%s: probe = %v, %v; want false, nil", other[0], other[1], got, err)
		}
	}

	readErr := errors.New("shared store read failed")
	if got, err := lifecycle.SharedStoreKeyspaceProbe(failingLookup{err: readErr})("acme", "orders"); !errors.Is(err, readErr) || got {
		t.Errorf("failing store: probe = %v, %v; want false, %v", got, err, readErr)
	}
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

// The manager checks every catalog record's binding root through the
// factory, opened on this node or not.
var _ database.RecordRootResolver = (*lifecycle.LocalBindingFactory)(nil)

// TestLocalBindingFactory_PreBindingDataKept proves a database recorded
// before the binding change keeps reading the data in the store it was
// created with: reopened through the binding factory, with no binding root
// on disk, it opens that store, not a new empty one. A fresh create of
// another database still gets its materialized binding, and RecordRoot names
// the root Create opens.
func TestLocalBindingFactory_PreBindingDataKept(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy := database.DatabaseConfig{TenantID: bindingTenant, Name: "legacy", Backend: database.BackendPebble}
	legacyDir := filepath.Join(dir, "legacy-data")

	// Written the way the database was served before the binding change.
	before, err := database.NewEmbeddedBackendFactory().Create(legacy, legacyDir)
	if err != nil {
		t.Fatalf("pre-binding Create: %v", err)
	}
	if err := before.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("pre-binding Put: %v", err)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("pre-binding Close: %v", err)
	}

	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
	be, err := f.Create(legacy, legacyDir)
	if err != nil {
		t.Fatalf("reopen legacy: %v", err)
	}
	if _, ok := be.(*lifecycle.MaterializedBinding); ok {
		t.Fatal("a database with pre-binding data was reopened as a new materialized store")
	}
	got, err := be.Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("legacy Get(k) = %q, %v; want v", got, err)
	}
	if err := be.Close(); err != nil {
		t.Fatalf("legacy Close: %v", err)
	}

	fresh := database.DatabaseConfig{TenantID: bindingTenant, Name: "fresh", Backend: database.BackendPebble}
	fb, err := f.Create(fresh, filepath.Join(dir, "fresh-data"))
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	defer func() { _ = fb.Close() }()
	mb, ok := fb.(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("fresh backend = %T, want *lifecycle.MaterializedBinding", fb)
	}
	root, err := f.RecordRoot(fresh)
	if err != nil || root != mb.DataRoot() {
		t.Fatalf("RecordRoot(fresh) = %q, %v; want %q", root, err, mb.DataRoot())
	}
}

// TestLocalBindingFactory_LegacyMemoryDatabaseKeepsSharedRoute proves a
// backend: memory database recorded before the binding change, whose data
// lives under its namespace of the shared store, is not reopened as a new,
// empty materialized memory store: it keeps the shared route, and no binding
// is recorded for it. A probe that cannot read the shared store fails the
// open rather than hide that data.
func TestLocalBindingFactory_LegacyMemoryDatabaseKeepsSharedRoute(t *testing.T) {
	dir := t.TempDir()
	legacy := database.DatabaseConfig{TenantID: bindingTenant, Name: "legacy", Backend: database.BackendMemory}
	var probed []string
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants).
		WithSharedKeyspaceProbe(func(tenantID, db string) (bool, error) {
			probed = append(probed, tenantID+"/"+db)
			return db == "legacy", nil
		})

	be, err := f.Create(legacy, filepath.Join(dir, "legacy-data"))
	if err != nil {
		t.Fatalf("Create legacy: %v", err)
	}
	defer func() { _ = be.Close() }()
	if _, ok := be.(*lifecycle.MaterializedBinding); ok {
		t.Fatal("a memory database with shared data was opened as a new materialized store")
	}
	if want := []string{bindingTenant + "/legacy"}; !slices.Equal(probed, want) {
		t.Fatalf("probed %q, want %q", probed, want)
	}
	if b, err := f.RecordBinding(legacy); err != nil || b != nil {
		t.Fatalf("RecordBinding(legacy) = %v, %v; want no binding", b, err)
	}
	if root, err := f.RecordRoot(legacy); err != nil || root != "" {
		t.Fatalf("RecordRoot(legacy) = %q, %v; want none", root, err)
	}

	boom := errors.New("shared store unreadable")
	failing := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants).
		WithSharedKeyspaceProbe(func(string, string) (bool, error) { return false, boom })
	got, err := failing.Create(legacy, filepath.Join(dir, "legacy-data"))
	var probeErr *lifecycle.LegacySharedKeyspaceError
	if !errors.As(err, &probeErr) || !errors.Is(err, boom) || probeErr.Error() == "" {
		t.Fatalf("Create over an unreadable shared store = %T, %v; want *LegacySharedKeyspaceError", got, err)
	}
}

// TestLocalBindingFactory_FreshMemoryDatabaseIsMaterialized proves a
// backend: memory database with no data under its shared namespace -- a
// fresh create, or one reopened after the binding change -- is materialized
// in its own store, and only backend: memory consults the probe.
func TestLocalBindingFactory_FreshMemoryDatabaseIsMaterialized(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var probed []string
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants).
		WithSharedKeyspaceProbe(func(tenantID, db string) (bool, error) {
			probed = append(probed, tenantID+"/"+db)
			return false, nil
		})

	fresh := database.DatabaseConfig{TenantID: bindingTenant, Name: "fresh", Backend: database.BackendMemory}
	be, err := f.Create(fresh, filepath.Join(dir, "fresh-data"))
	if err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	defer func() { _ = be.Close() }()
	mb, ok := be.(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("fresh backend = %T, want *lifecycle.MaterializedBinding", be)
	}
	if err := mb.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if b, err := f.RecordBinding(fresh); err != nil || b == nil {
		t.Fatalf("RecordBinding(fresh) = %v, %v; want its binding", b, err)
	}

	pebble := database.DatabaseConfig{TenantID: bindingTenant, Name: "disk", Backend: database.BackendPebble}
	pb, err := f.Create(pebble, filepath.Join(dir, "disk-data"))
	if err != nil {
		t.Fatalf("Create pebble: %v", err)
	}
	defer func() { _ = pb.Close() }()
	for _, p := range probed {
		if p != bindingTenant+"/fresh" {
			t.Fatalf("probe consulted for %q; only backend: memory is probed", p)
		}
	}
	if len(probed) == 0 {
		t.Fatal("the probe was never consulted for the memory database")
	}
}

// TestLocalBindingFactory_ExplicitDataDirLegacyKept proves a database with
// an explicit data_dir, whose binding root is that data_dir, keeps the store
// it wrote straight into data_dir before the binding change: reopened
// through the factory, it is served from there, not from a new empty
// <data_dir>/<backend> store. A fresh create under an empty explicit
// data_dir is materialized, and stays materialized when reopened.
func TestLocalBindingFactory_ExplicitDataDirLegacyKept(t *testing.T) {
	ctx := context.Background()
	for _, b := range []database.BackendType{database.BackendPebble, database.BackendFile} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			legacyDir := filepath.Join(dir, "explicit-legacy")
			legacy := database.DatabaseConfig{TenantID: bindingTenant, Name: "legacy", Backend: b, DataDir: legacyDir}

			before, err := database.NewEmbeddedBackendFactory().Create(legacy, legacyDir)
			if err != nil {
				t.Fatalf("pre-binding Create: %v", err)
			}
			if err := before.Put(ctx, []byte("k"), []byte("v")); err != nil {
				t.Fatalf("pre-binding Put: %v", err)
			}
			if err := before.Close(); err != nil {
				t.Fatalf("pre-binding Close: %v", err)
			}

			f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
			be, err := f.Create(legacy, legacyDir)
			if err != nil {
				t.Fatalf("reopen legacy: %v", err)
			}
			if _, ok := be.(*lifecycle.MaterializedBinding); ok {
				t.Fatal("a legacy explicit data_dir database was reopened as a new materialized store")
			}
			got, err := be.Get(ctx, []byte("k"))
			if err != nil || string(got) != "v" {
				t.Fatalf("legacy Get(k) = %q, %v; want v", got, err)
			}
			if err := be.Close(); err != nil {
				t.Fatalf("legacy Close: %v", err)
			}
			if _, err := os.Stat(filepath.Join(legacyDir, string(b))); !os.IsNotExist(err) {
				t.Fatalf("the reopen created a binding store under the legacy data_dir (%v)", err)
			}

			freshDir := filepath.Join(dir, "explicit-fresh")
			fresh := database.DatabaseConfig{TenantID: bindingTenant, Name: "fresh", Backend: b, DataDir: freshDir}
			for i := 0; i < 2; i++ {
				fb, err := f.Create(fresh, freshDir)
				if err != nil {
					t.Fatalf("Create fresh (open %d): %v", i, err)
				}
				_, ok := fb.(*lifecycle.MaterializedBinding)
				if err := fb.Close(); err != nil {
					t.Fatalf("Close fresh (open %d): %v", i, err)
				}
				if !ok {
					t.Fatalf("fresh explicit data_dir backend (open %d) = %T, want *lifecycle.MaterializedBinding", i, fb)
				}
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

// TestLocalBindingFactory_WALDirPerBinding pins that an absolute
// storage.wal_dir gives each Pebble binding its own WAL subdirectory: two
// bindings' logs are never in one directory, neither replays the other's
// writes after a reopen, and a database created again under a dropped one's
// root does not replay the dropped one's log.
func TestLocalBindingFactory_WALDirPerBinding(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(t.TempDir(), "wal")
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	newMgr := func() *database.Manager {
		f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{WALDir: walDir}, prefixTenants)
		mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
		mgr.SetCatalog(cat)
		return mgr
	}
	ctx := context.Background()
	mgr := newMgr()
	roots := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: name, Backend: database.BackendPebble})
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		mb := materialized(t, db)
		roots[name] = mb.DataRoot()
		if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, name+"-key", []byte(name)); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}
	alphaWAL := lifecycle.BindingWALDir(walDir, roots["alpha"])
	betaWAL := lifecycle.BindingWALDir(walDir, roots["beta"])
	if alphaWAL == betaWAL || alphaWAL == walDir {
		t.Fatalf("binding WAL dirs %q and %q, want two distinct subdirectories of %q", alphaWAL, betaWAL, walDir)
	}
	for _, d := range []string{alphaWAL, betaWAL} {
		if got := walFiles(t, d); len(got) == 0 {
			t.Errorf("no WAL file in the binding WAL dir %s", d)
		}
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mgr = newMgr()
	defer func() { _ = mgr.Close() }()
	for name, other := range map[string]string{"alpha": "beta", "beta": "alpha"} {
		db, err := mgr.GetOwned(name, bindingTenant)
		if err != nil {
			t.Fatalf("GetOwned %s after reopen: %v", name, err)
		}
		mb := materialized(t, db)
		kv := service.NewKVService(mb.Host(), mb.ShardID())
		if got, err := kv.Get(ctx, name+"-key"); err != nil || string(got) != name {
			t.Fatalf("%s after reopen Get = %q, %v; want %q", name, got, err, name)
		}
		if got, err := kv.Get(ctx, other+"-key"); err == nil && got != nil {
			t.Fatalf("%s replayed %s's write: %q", name, other, got)
		}
	}

	if err := mgr.DropForTenant(tenant.WithTenantID(ctx, bindingTenant), "alpha"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "alpha", Backend: database.BackendPebble})
	if err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	mb := materialized(t, db)
	if got, err := service.NewKVService(mb.Host(), mb.ShardID()).Get(ctx, "alpha-key"); err == nil && got != nil {
		t.Fatalf("recreated database replayed the dropped one's WAL: %q", got)
	}
}

// TestLocalBindingFactory_VectorsInBindingStore proves a materialized
// database keeps its vectors in its binding store, through the apply host,
// on every backend: a restarted binding reads them back, and nothing is
// written under <root>/vector.
func TestLocalBindingFactory_VectorsInBindingStore(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), bindingTenant)
	open := func(t *testing.T, dir string, b database.BackendType) *lifecycle.MaterializedBinding {
		t.Helper()
		f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
		t.Cleanup(func() { _ = f.Close() })
		be, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vecs", Backend: b}, dir)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		mb, ok := be.(*lifecycle.MaterializedBinding)
		if !ok {
			t.Fatalf("Create returned %T, want *lifecycle.MaterializedBinding", be)
		}
		if mb.Vector() == nil {
			t.Fatal("binding has no vector service")
		}
		return mb
	}
	search := func(t *testing.T, mb *lifecycle.MaterializedBinding) {
		t.Helper()
		resp, err := mb.Vector().SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{0, 1}, K: 1})
		if err != nil {
			t.Fatalf("SearchSimilar: %v", err)
		}
		if len(resp.Results) != 1 || resp.Results[0].ID != 3 {
			t.Fatalf("SearchSimilar = %+v, want id 3", resp.Results)
		}
	}
	cases := []struct {
		backend  database.BackendType
		survives bool
	}{
		{database.BackendMemory, false},
		{database.BackendFile, true},
		{database.BackendPebble, true},
		{database.BackendObjstore, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.backend), func(t *testing.T) {
			dir := t.TempDir()
			mb := open(t, dir, tc.backend)
			root := mb.DataRoot()
			if _, err := mb.Vector().CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
				_ = mb.Close()
				if strings.Contains(err.Error(), "qrdb_vector") {
					t.Skipf("vector not built: %v", err)
				}
				t.Fatalf("CreateNamespace: %v", err)
			}
			if _, err := mb.Vector().UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 3, Vector: []float32{0, 1}}); err != nil {
				t.Fatalf("UpsertVector: %v", err)
			}
			search(t, mb)
			if err := mb.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if root != "" {
				if _, err := os.Stat(filepath.Join(root, "vector")); !os.IsNotExist(err) {
					t.Fatalf("a vector store exists under the binding root (%v)", err)
				}
			}
			if !tc.survives {
				return
			}
			reopened := open(t, dir, tc.backend)
			defer func() { _ = reopened.Close() }()
			search(t, reopened)
			if _, err := os.Stat(filepath.Join(root, "vector")); !os.IsNotExist(err) {
				t.Fatalf("a vector store exists under the binding root after restart (%v)", err)
			}
		})
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

// newSharedObjstoreManager builds a binding Manager whose node storage
// configuration sets the local objstore provider's path, over the catalog
// store kv, so a second Manager on the same dir and kv reopens the catalog.
func newSharedObjstoreManager(t *testing.T, dir, shared string, kv kvstore.AtomicKVStore) *database.Manager {
	t.Helper()
	cfg := qrdbconfig.StorageConfig{Objstore: qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": shared}}}
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, cfg, prefixTenants)
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kv, nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	return mgr
}

// TestLocalBindingFactory_ObjstoreLocalBindingAtItsRoot proves an objstore
// database on the local provider lives in its own store under its binding
// root even when storage.objstore sets a path: the configured path places
// the node's own store only, and a drop removes the database's store with
// its journal while a sibling keeps its value.
func TestLocalBindingFactory_ObjstoreLocalBindingAtItsRoot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared")
	kv := kvstore.NewMemoryKVStore()
	mgr := newSharedObjstoreManager(t, dir, shared, kv)
	dbs := []struct{ tenantID, dirTenant, name, value string }{
		{bindingTenant, bindingTenant, "a", "value-a"},
		{bindingTenant, bindingTenant, "b", "value-b"},
		{"", "system", "c", "value-c"},
	}
	roots := make(map[string]string, len(dbs))
	for _, d := range dbs {
		db, err := mgr.Create(database.DatabaseConfig{TenantID: d.tenantID, Name: d.name, Backend: database.BackendObjstore})
		if err != nil {
			t.Fatalf("Create %s: %v", d.name, err)
		}
		mb := materialized(t, db)
		want := filepath.Join(dir, "tenants", d.dirTenant, "databases", d.name)
		if mb.DataRoot() != want {
			t.Fatalf("%s DataRoot = %q, want %q", d.name, mb.DataRoot(), want)
		}
		roots[d.name] = want
		if err := service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, "k", []byte(d.value)); err != nil {
			t.Fatalf("Put %s: %v", d.name, err)
		}
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Manager.Close: %v", err)
	}
	for _, d := range dbs {
		root := roots[d.name]
		entries, err := os.ReadDir(filepath.Join(root, "objstore"))
		if err != nil || len(entries) == 0 {
			t.Fatalf("%s: %s/objstore is empty (%v)", d.name, root, err)
		}
		got, err := readBack(t, database.BackendObjstore, root, nil, "k")
		if err != nil || !bytes.Equal(got, []byte(d.value)) {
			t.Fatalf("%s read back = %q, %v; want %q", d.name, got, err, d.value)
		}
	}
	if entries, err := os.ReadDir(shared); err == nil && len(entries) != 0 {
		t.Fatalf("the configured objstore path holds %d entries; want none", len(entries))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(shared): %v", err)
	}

	mgr = newSharedObjstoreManager(t, dir, shared, kv)
	defer func() { _ = mgr.Close() }()
	if err := mgr.DropForTenant(tenant.WithTenantID(ctx, bindingTenant), "a"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := os.Stat(roots["a"]); !os.IsNotExist(err) {
		t.Fatalf("binding root %s survives the drop (%v)", roots["a"], err)
	}
	if _, err := os.Stat(filepath.Join(roots["a"], "objstore.journal")); !os.IsNotExist(err) {
		t.Fatalf("objstore journal of a survives the drop (%v)", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Manager.Close: %v", err)
	}
	got, err := readBack(t, database.BackendObjstore, roots["b"], nil, "k")
	if err != nil || !bytes.Equal(got, []byte("value-b")) {
		t.Fatalf("b after dropping a = %q, %v; want value-b", got, err)
	}
}

// TestLocalBindingFactory_RemoteObjstoreRefused proves a materialized objstore
// database on a remote provider is refused for every tenant, so no two
// tenants' databases ever share the provider's unprefixed keyspace.
func TestLocalBindingFactory_RemoteObjstoreRefused(t *testing.T) {
	dir := t.TempDir()
	cfg := qrdbconfig.StorageConfig{Objstore: qrdbconfig.ObjstoreConfig{Provider: "s3", Settings: map[string]string{"bucket": "shared"}}}
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, cfg, prefixTenants)
	defer func() { _ = f.Close() }()
	for _, tenantID := range []string{bindingTenant, "other-tenant"} {
		_, err := f.Create(database.DatabaseConfig{TenantID: tenantID, Name: "app", Backend: database.BackendObjstore}, filepath.Join(dir, tenantID))
		var remote *lifecycle.RemoteObjstoreBindingUnsupportedError
		if !errors.As(err, &remote) {
			t.Fatalf("Create for %s: err = %v, want *RemoteObjstoreBindingUnsupportedError", tenantID, err)
		}
		if remote.TenantID != tenantID || remote.Database != "app" || remote.Provider != "s3" {
			t.Fatalf("Create for %s: error = %+v", tenantID, remote)
		}
	}
}

// TestLocalBindingFactory_EncryptedObjstoreRefused proves an objstore binding
// that would carry an at-rest encryptor is refused instead of being stored
// in plaintext, while an unencrypted prefix-tenant objstore binding opens.
func TestLocalBindingFactory_EncryptedObjstoreRefused(t *testing.T) {
	t.Run("barrier tenant", func(t *testing.T) {
		dir := t.TempDir()
		tenantEnc := atrest.New(newUnsealedBarrier(t))
		mgr := newBarrierTenantManager(t, dir, nil, func(string) (*atrest.Encryptor, error) { return tenantEnc, nil })
		defer func() { _ = mgr.Close() }()
		_, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "app", Backend: database.BackendObjstore})
		var refused *lifecycle.ObjstoreBindingEncryptionUnsupportedError
		if !errors.As(err, &refused) {
			t.Fatalf("Create: err = %v, want *ObjstoreBindingEncryptionUnsupportedError", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "tenants", bindingTenant, "databases", "app", "objstore")); !os.IsNotExist(statErr) {
			t.Fatalf("a refused binding left an objstore root (%v)", statErr)
		}
	})
	t.Run("sealed node", func(t *testing.T) {
		dir := t.TempDir()
		f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, atrest.New(newUnsealedBarrier(t)), qrdbconfig.StorageConfig{}, prefixTenants)
		defer func() { _ = f.Close() }()
		_, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "app", Backend: database.BackendObjstore}, dir)
		var refused *lifecycle.ObjstoreBindingEncryptionUnsupportedError
		if !errors.As(err, &refused) {
			t.Fatalf("Create: err = %v, want *ObjstoreBindingEncryptionUnsupportedError", err)
		}
	})
	t.Run("prefix tenant unencrypted opens", func(t *testing.T) {
		dir := t.TempDir()
		mgr := newSharedObjstoreManager(t, dir, filepath.Join(t.TempDir(), "shared"), kvstore.NewMemoryKVStore())
		defer func() { _ = mgr.Close() }()
		db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "app", Backend: database.BackendObjstore})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		_ = materialized(t, db)
	})
}

// TestLocalBindingFactory_PlaintextBarrierAdmitsObjstore proves the objstore
// refusal keys on whether the barrier encrypts, not on its presence: a node
// barrier under seal strategy none (which writes plaintext) admits an
// objstore binding, while an encrypting strategy refuses it.
func TestLocalBindingFactory_PlaintextBarrierAdmitsObjstore(t *testing.T) {
	cases := []struct {
		name    string
		barrier func(*testing.T) *seal.StorageBarrier
		refused bool
	}{
		{name: "strategy none admits", barrier: newNoneStrategyBarrier, refused: false},
		{name: "passphrase strategy refuses", barrier: newUnsealedBarrier, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, atrest.New(tc.barrier(t)), qrdbconfig.StorageConfig{}, prefixTenants)
			defer func() { _ = f.Close() }()
			bound, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "app", Backend: database.BackendObjstore}, dir)
			var refused *lifecycle.ObjstoreBindingEncryptionUnsupportedError
			if tc.refused {
				if !errors.As(err, &refused) {
					t.Fatalf("Create: err = %v, want *ObjstoreBindingEncryptionUnsupportedError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if bound == nil {
				t.Fatal("Create returned no binding")
			}
		})
	}
}

// TestLocalBindingFactory_ReservedRoots proves the factory reports the node
// stores its storage config places outside the data directory: an absolute
// wal_dir and the local object-store path (a relative one resolved against
// the data directory); a relative wal_dir is under the data root already.
func TestLocalBindingFactory_ReservedRoots(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(t.TempDir(), "wal")
	objects := filepath.Join(t.TempDir(), "objects")
	cfg := qrdbconfig.StorageConfig{WALDir: wal, Objstore: qrdbconfig.ObjstoreConfig{Settings: map[string]string{"path": objects}}}
	got, err := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, cfg, prefixTenants).ReservedRoots()
	if err != nil {
		t.Fatalf("ReservedRoots: %v", err)
	}
	want := []string{wal, objects, objects + ".journal"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ReservedRoots = %v, want %v", got, want)
	}
}

// TestLocalBindingFactory_ReservedRootsFollowTheWorkingDirectory proves a
// relative objstore path and wal_dir are reserved where storage opens them,
// against the process working directory, never under the node data directory.
func TestLocalBindingFactory_ReservedRootsFollowTheWorkingDirectory(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	dir := t.TempDir()
	cfg := qrdbconfig.StorageConfig{WALDir: "wal", Objstore: qrdbconfig.ObjstoreConfig{Settings: map[string]string{"path": "objects"}}}
	got, err := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, cfg, prefixTenants).ReservedRoots()
	if err != nil {
		t.Fatalf("ReservedRoots: %v", err)
	}
	want := []string{filepath.Join(cwd, "wal"), filepath.Join(cwd, "objects"), filepath.Join(cwd, "objects.journal")}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ReservedRoots = %v, want %v", got, want)
	}
	for _, p := range got {
		if p == filepath.Join(dir, "objects") {
			t.Fatalf("ReservedRoots reserves %s under the data directory, not the opened store", p)
		}
	}
}

// TestLocalBindingFactory_ReservedRootsDefaultObjstoreJournal proves the
// default local object store reserves its journal sibling.
func TestLocalBindingFactory_ReservedRootsDefaultObjstoreJournal(t *testing.T) {
	dir := t.TempDir()
	got, err := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants).ReservedRoots()
	if err != nil {
		t.Fatalf("ReservedRoots: %v", err)
	}
	want := []string{filepath.Join(dir, "objstore"), filepath.Join(dir, "objstore.journal")}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ReservedRoots = %v, want %v", got, want)
	}
}

// TestLocalBindingFactory_RelativeObjstoreRootRefusedAsDataDir proves a
// Manager over the factory refuses a data_dir over the cwd-relative object
// store, its journal or the working directory above them, and the store's
// files stay.
func TestLocalBindingFactory_RelativeObjstoreRootRefusedAsDataDir(t *testing.T) {
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	dir := t.TempDir()
	objects := filepath.Join(cwd, "objects")
	journal := objects + ".journal"
	var markers []string
	for _, d := range []string{objects, journal} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(d, "marker")
		if err := os.WriteFile(marker, []byte("node store"), 0o600); err != nil {
			t.Fatal(err)
		}
		markers = append(markers, marker)
	}
	cfg := qrdbconfig.StorageConfig{Objstore: qrdbconfig.ObjstoreConfig{Settings: map[string]string{"path": "objects"}}}
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, cfg, prefixTenants)
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	for i, dataDir := range []string{objects, journal, cwd} {
		_, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: fmt.Sprintf("db%d", i), Backend: database.BackendFile, DataDir: dataDir})
		var reserved *database.DataDirReservedPathError
		if !errors.As(err, &reserved) {
			t.Fatalf("Create with data_dir %s: err = %v, want *database.DataDirReservedPathError", dataDir, err)
		}
	}
	for _, marker := range markers {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("node store file after the refused creates: %v", err)
		}
	}
}

// strictPolicy is a ClusterIsolationPolicyReader pinned to strict.
type strictPolicy struct{}

func (strictPolicy) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyStrict, nil
}

// TestLocalBindingFactory_VectorHonorsIsolationPolicy proves a materialized
// database's own vector service consults the factory's isolation policy: a
// strict policy refuses a system-tenant namespace create in the named
// database, as the shared vector service does, and without a policy it opens.
func TestLocalBindingFactory_VectorHonorsIsolationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy tenant.ClusterIsolationPolicyReader
		refuse bool
	}{
		{name: "strict refuses", policy: strictPolicy{}, refuse: true},
		{name: "no policy admits", policy: nil, refuse: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants).
				WithIsolationPolicy(tc.policy)
			defer func() { _ = f.Close() }()
			be, err := f.Create(database.DatabaseConfig{TenantID: tenantid.System, Name: "app", Backend: database.BackendMemory}, dir)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb, ok := be.(*lifecycle.MaterializedBinding)
			if !ok {
				t.Fatalf("Create = %T, want *lifecycle.MaterializedBinding", be)
			}
			_, err = mb.Vector().CreateNamespace(context.Background(), &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "L2"})
			var required *service.TenantRequiredError
			if tc.refuse != errors.As(err, &required) {
				t.Fatalf("CreateNamespace: err = %v, want TenantRequiredError = %v", err, tc.refuse)
			}
		})
	}
}

// TestNodeBindingFactory_IsolationPolicyFailsClosedBeforeTenantService proves
// the late-bound policy a node factory wires refuses a system-tenant
// namespace create while the tenant service does not exist yet.
func TestNodeBindingFactory_IsolationPolicyFailsClosedBeforeTenantService(t *testing.T) {
	dir := t.TempDir()
	f := lifecycle.NewNodeBindingFactory(dir, database.Defaults{}, nil, qrdbconfig.StorageConfig{},
		func() tenant.TenantServicer { return nil }, func() *seal.BarrierRegistry { return nil })
	defer func() { _ = f.Close() }()
	be, err := f.Create(database.DatabaseConfig{TenantID: tenantid.System, Name: "app", Backend: database.BackendMemory}, dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mb, ok := be.(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("Create = %T, want *lifecycle.MaterializedBinding", be)
	}
	_, err = mb.Vector().CreateNamespace(context.Background(), &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "L2"})
	var unavailable *lifecycle.IsolationPolicyUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("CreateNamespace: err = %v, want *IsolationPolicyUnavailableError", err)
	}
}

// TestMaterializedBinding_CloseClosesWatchHub proves teardown closes the
// database's own watch hub: an open watcher's channel closes, its slot is
// released, and a new watch on the dropped database is refused.
func TestMaterializedBinding_CloseClosesWatchHub(t *testing.T) {
	dir := t.TempDir()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
	defer func() { _ = f.Close() }()
	be, err := f.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "app", Backend: database.BackendMemory}, dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mb, ok := be.(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("Create = %T, want *lifecycle.MaterializedBinding", be)
	}
	hub := mb.WatchHub()
	w, err := hub.Watch(context.Background(), "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if err := mb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for range w.Events() {
	}
	if n := hub.WatchCount(); n != 0 {
		t.Fatalf("WatchCount after Close = %d, want 0", n)
	}
	var closed *watch.HubClosedError
	if _, err := hub.Watch(context.Background(), "k"); !errors.As(err, &closed) {
		t.Fatalf("Watch after Close: err = %v, want *watch.HubClosedError", err)
	}
}

// newNoneStrategyBarrier returns an initialized node barrier whose active
// strategy is seal.StrategyNone.
func newNoneStrategyBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "binding-none-root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := barrier.InitializeWithStrategy(ctx, seal.NewNoneStrategy(), seal.Credentials{}); err != nil {
		t.Fatalf("InitializeWithStrategy: %v", err)
	}
	if barrier.IsSealed() {
		if err := barrier.UnsealWithStrategy(ctx, seal.NewNoneStrategy(), seal.Credentials{}); err != nil {
			t.Fatalf("UnsealWithStrategy: %v", err)
		}
	}
	return barrier
}

// TestLocalBindingFactory_RaftOnStandaloneRefused proves a backend: raft
// record on a standalone node is refused with the binding's
// *RaftBackendUnavailableError, never opened by the embedded factory.
// TestLocalBindingFactory_DescribeBinding: the block database info reports is
// what ResolveBinding decides for the record, including a database the
// factory serves from the fallback store; a raft record on a standalone node
// is refused as Create refuses it.
func TestLocalBindingFactory_DescribeBinding(t *testing.T) {
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
	var _ database.BindingDescriber = f
	for _, cfg := range []database.DatabaseConfig{
		{TenantID: bindingTenant, Name: "orders", Backend: database.BackendFile},
		{TenantID: bindingTenant, Name: database.TenantHomeName, Backend: database.BackendMemory},
	} {
		b, err := f.DescribeBinding(cfg)
		if err != nil || b == nil {
			t.Fatalf("DescribeBinding(%s) = %v, %v; want a binding", cfg.Name, b, err)
		}
		got := b.Describe("")
		if got.Kind != database.BindingLocal || got.Encryption != "node" || got.Nodes != nil {
			t.Fatalf("DescribeBinding(%s).Describe = %+v; want local, node, no nodes", cfg.Name, got)
		}
	}
	_, err := f.DescribeBinding(database.DatabaseConfig{TenantID: bindingTenant, Name: "orders", Backend: database.BackendRaft})
	var unavailable *database.RaftBackendUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("DescribeBinding(raft) err = %v, want *RaftBackendUnavailableError", err)
	}
}

func TestLocalBindingFactory_RaftOnStandaloneRefused(t *testing.T) {
	dir := t.TempDir()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{}, prefixTenants)
	defer func() { _ = f.Close() }()
	for _, tenantID := range []string{"", bindingTenant} {
		be, err := f.Create(database.DatabaseConfig{TenantID: tenantID, Name: "app", Backend: database.BackendRaft}, dir)
		var unavailable *database.RaftBackendUnavailableError
		if !errors.As(err, &unavailable) {
			if be != nil {
				_ = be.Close()
			}
			t.Fatalf("Create raft for %q: err = %v, want *RaftBackendUnavailableError", tenantID, err)
		}
	}
}

// The plaintext markers the seal tests write, one per engine, so a leak names
// the engine it came from.
const (
	sealKVMarker   = "seal-kv-plaintext-marker"
	sealMVCCMarker = "seal-mvcc-plaintext-marker"
	sealDocMarker  = "seal-doc-plaintext-marker"
	sealTSMarker   = "seal-ts-plaintext-marker"
)

var sealMarkers = []string{sealKVMarker, sealMVCCMarker, sealDocMarker, sealTSMarker}

// sealTSAt is the time-series point's timestamp in the seal tests.
var sealTSAt = time.Unix(1_700_000_000, 0)

// newRegisteredTenantBarrier returns a real barrier registry with
// bindingTenant registered under its own unsealed tenant barrier, set up as
// TestTenantBarrierEncryptorLookup_Typed does.
func newRegisteredTenantBarrier(t *testing.T) (*seal.BarrierRegistry, *seal.TenantBarrier) {
	t.Helper()
	reg, err := seal.NewBarrierRegistry(slog.Default(), newUnsealedBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	if _, err := reg.RegisterTenantDefault(bindingTenant); err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	tb := seal.WrapTenantBarrier(bindingTenant, newUnsealedBarrier(t))
	if err := reg.ReplaceTenantBarrier(bindingTenant, tb); err != nil {
		t.Fatalf("ReplaceTenantBarrier: %v", err)
	}
	return reg, tb
}

// sealEngines are a database's data-plane engines over one host, built the
// way pkg/server buildDataPlaneServices builds them.
type sealEngines struct {
	kv   service.KVServicer
	mvcc service.MVCCKVServicer
	idx  service.IndexServicer
	ts   service.TimeSeriesServicer
	coll *service.CollectionService
}

func newSealEngines(host service.Host, shardID uint64) sealEngines {
	kv := service.NewKVService(host, shardID)
	mvcc := service.NewMVCCKVService(host, shardID)
	idx := service.NewIndexService(host, shardID)
	txn := service.NewTransactionService(host, shardID)
	return sealEngines{
		kv:   kv,
		mvcc: mvcc,
		idx:  idx,
		ts:   service.NewTimeSeriesService(host, shardID),
		coll: service.NewCollectionServiceWithIndex(kv, idx).WithSequenceService(service.NewSequenceService(mvcc, txn)),
	}
}

// writeEveryEngine writes one marker per engine (KV, MVCC, a collection
// document under a secondary index on its marker field, a time-series point
// tagged with its marker) and returns the document's ID and whether the
// time-series engine is compiled in (build tag qrdb_ts, which make test sets).
func (e sealEngines) writeEveryEngine(t *testing.T, db string) (uint64, bool) {
	t.Helper()
	ctx := context.Background()
	if err := e.kv.Put(ctx, "seal-kv", []byte(sealKVMarker)); err != nil {
		t.Fatalf("KV Put: %v", err)
	}
	if err := e.mvcc.Put(ctx, "seal-mvcc", []byte(sealMVCCMarker)); err != nil {
		t.Fatalf("MVCC Put: %v", err)
	}
	if _, err := e.coll.CreateCollection(ctx, &service.CreateCollectionRequest{
		Name: "docs", Database: db, Indexes: []service.IndexDefinition{{FieldName: "secret"}},
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	ins, err := e.coll.InsertDocument(ctx, &service.InsertDocumentRequest{
		Collection: "docs", Database: db, Document: map[string]interface{}{"secret": sealDocMarker},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	err = e.ts.Write(ctx, "seal.metric", 42.5, sealTSAt, map[string]string{"secret": sealTSMarker})
	// The state machine encodes the refusal into the proposal's result, so
	// only its text, not its type, reaches the caller.
	if err != nil && strings.Contains(err.Error(), (&timeseries.NotCompiledError{}).Error()) {
		t.Logf("time series not compiled in (%v): its seal cell runs under make test", err)
		return ins.ID, false
	}
	if err != nil {
		t.Fatalf("TimeSeries Write: %v", err)
	}
	return ins.ID, true
}

// engineRead is one engine's read of its own marker: what it returned, as
// text, and its error.
type engineRead struct {
	engine, marker, got string
	err                 error
}

// readEveryEngine reads each engine's value back by its exact key, ID, index
// value or metric, never by listing.
func (e sealEngines) readEveryEngine(db string, docID uint64, withTS bool) []engineRead {
	ctx := context.Background()
	kv, kvErr := e.kv.Get(ctx, "seal-kv")
	mv, mvErr := e.mvcc.Get(ctx, "seal-mvcc")
	doc, docErr := e.coll.GetDocument(ctx, &service.GetDocumentRequest{Collection: "docs", Database: db, ID: docID})
	var docText string
	if doc != nil {
		docText = fmt.Sprint(doc.Document)
	}
	reads := []engineRead{
		{engine: "kv", marker: sealKVMarker, got: string(kv), err: kvErr},
		{engine: "mvcc", marker: sealMVCCMarker, got: string(mv), err: mvErr},
		{engine: "collection", marker: sealDocMarker, got: docText, err: docErr},
	}
	// The index returns the document's key, not its value: the control asks
	// for one hit. A reopened store serves it too: the engine restores the
	// index definitions from its catalog on open.
	hits, idxErr := e.idx.Query(ctx, db+".docs.secret", []byte(sealDocMarker))
	reads = append(reads, engineRead{engine: "index", marker: sealDocMarker, got: fmt.Sprint(len(hits)), err: idxErr})
	if withTS {
		points, tsErr := e.ts.Query(ctx, "seal.metric", sealTSAt.Add(-time.Hour), sealTSAt.Add(time.Hour))
		reads = append(reads, engineRead{engine: "timeseries", marker: sealTSMarker, got: fmt.Sprint(points), err: tsErr})
	}
	return reads
}

// TestLocalBindingFactory_BarrierTenantSealFailsEveryEngineClosed is
// binding-materialization test (3) for the tenant barrier on a standalone
// node (engineering.md "Target: acceptance"): a barrier tenant's database is
// written on every engine except vector (still the node-local store) and
// objstore (values not yet encrypted at rest), read back under the tenant
// barrier, and once that barrier is sealed the store cannot be read, no new
// database opens, and no file under the binding root holds a plaintext value.
func TestLocalBindingFactory_BarrierTenantSealFailsEveryEngineClosed(t *testing.T) {
	for _, b := range []database.BackendType{database.BackendFile, database.BackendPebble} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			reg, tb := newRegisteredTenantBarrier(t)
			lookup := lifecycle.TenantBarrierEncryptorLookup(func() *seal.BarrierRegistry { return reg })
			nodeEnc := atrest.New(newUnsealedBarrier(t))
			mgr := newBarrierTenantManager(t, dir, nodeEnc, lookup)
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			root := mb.DataRoot()
			live := newSealEngines(mb.Host(), mb.ShardID())
			docID, withTS := live.writeEveryEngine(t, "vault")
			// Control: every engine, the index included, reads its marker
			// back on the open store, so the sealed checks below are not
			// vacuous.
			assertControl(t, "open store", live.readEveryEngine("vault", docID, withTS))
			if err := mgr.Close(); err != nil {
				t.Fatalf("Manager.Close: %v", err)
			}

			// Control: the store reopens under the unsealed tenant barrier and
			// every engine, the index included, reads back.
			host, closeHost, err := openBack(t, b, root, atrest.New(tb.Barrier()))
			if err != nil {
				t.Fatalf("reopen under the unsealed tenant barrier: %v", err)
			}
			reads := newSealEngines(host, shard.ShardKV).readEveryEngine("vault", docID, withTS)
			closeHost()
			assertControl(t, "reopened store", reads)

			if err := tb.Seal(); err != nil {
				t.Fatalf("Seal: %v", err)
			}

			// (1) The store does not reopen under the sealed barrier, so no
			// engine is ever reached. The composition refuses the open with
			// the store's own sealed error, not seal.ErrSealed: the file
			// store's quiesce gate is closed (quiesce.ErrSealing), and
			// pebble's seal watch reports pebblestore.ErrBarrierSealed.
			wantSealed := map[database.BackendType]error{
				database.BackendFile:   quiesce.ErrSealing,
				database.BackendPebble: pebblestore.ErrBarrierSealed,
			}[b]
			host, closeHost, err = openBack(t, b, root, atrest.New(tb.Barrier()))
			if err == nil {
				closeHost()
				t.Fatalf("the store reopened under its sealed tenant barrier")
			}
			if !errors.Is(err, wantSealed) {
				t.Fatalf("reopen under the sealed tenant barrier: err = %v (%T), want %v", err, err, wantSealed)
			}

			// (2) A fresh Manager opens no new database for the tenant.
			mgr2 := newBarrierTenantManager(t, dir, nodeEnc, lookup)
			defer func() { _ = mgr2.Close() }()
			_, err = mgr2.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault2", Backend: b})
			var open *lifecycle.BindingOpenError
			var sealed *lifecycle.BindingBarrierSealedError
			if !errors.As(err, &open) || !errors.As(err, &sealed) {
				t.Fatalf("Create under a sealed barrier: err = %v (%T), want *lifecycle.BindingOpenError wrapping *lifecycle.BindingBarrierSealedError", err, err)
			}
			if _, ok := mgr2.Local("vault2", bindingTenant); ok {
				t.Fatal("the Manager recorded a database whose tenant barrier is sealed")
			}

			// (3) No file under the binding root holds a plaintext marker.
			assertNoMarkerUnder(t, root)
		})
	}
}

// assertNoMarkerUnder fails if any file under root contains a seal marker.
// assertControl fails unless every read succeeded with its marker (the index
// with exactly one hit).
func assertControl(t *testing.T, where string, reads []engineRead) {
	t.Helper()
	for _, r := range reads {
		ok := strings.Contains(r.got, r.marker)
		if r.engine == "index" {
			ok = r.got == "1"
		}
		if r.err != nil || !ok {
			t.Fatalf("control on the %s: %s read = %q, %v; want its marker %q", where, r.engine, r.got, r.err, r.marker)
		}
	}
}

func assertNoMarkerUnder(t *testing.T, root string) {
	t.Helper()
	files := 0
	scoped, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("OpenRoot %s: %v", root, err)
	}
	defer func() { _ = scoped.Close() }()
	rootFS := scoped.FS()
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(rootFS, path)
		if err != nil {
			return err
		}
		files++
		for _, m := range sealMarkers {
			if bytes.Contains(data, []byte(m)) {
				t.Errorf("%s holds the plaintext marker %q", path, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir %s: %v", root, err)
	}
	if files == 0 {
		t.Fatalf("no file under the binding root %s", root)
	}
}

// TestLocalBindingFactory_BarrierTenantSealRefusesWritesOnOpenStore seals a
// barrier tenant's barrier while its database's store is open: the next write
// must be refused, not accepted into memory or the log as plaintext.
func TestLocalBindingFactory_BarrierTenantSealRefusesWritesOnOpenStore(t *testing.T) {
	ctx := context.Background()
	for _, b := range []database.BackendType{database.BackendFile, database.BackendPebble} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			reg, tb := newRegisteredTenantBarrier(t)
			mgr := newBarrierTenantManager(t, dir, atrest.New(newUnsealedBarrier(t)),
				lifecycle.TenantBarrierEncryptorLookup(func() *seal.BarrierRegistry { return reg }))
			defer func() { _ = mgr.Close() }()
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			kv := service.NewKVService(mb.Host(), mb.ShardID())
			if err := kv.Put(ctx, "before", []byte(sealKVMarker)); err != nil {
				t.Fatalf("Put before the seal: %v", err)
			}
			if err := tb.Seal(); err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if err := kv.Put(ctx, "after", []byte(sealMVCCMarker)); err == nil {
				t.Fatal("a write to an open store succeeded after its tenant barrier was sealed")
			}
		})
	}
}

// TestLocalBindingFactory_BarrierTenantSealRefusesReadsOnOpenStore seals a
// barrier tenant's barrier while its database's store is open, after every
// engine was written: every engine's next read must be refused, not served
// from the open store's memtables or cache, and no refusal carries a marker.
func TestLocalBindingFactory_BarrierTenantSealRefusesReadsOnOpenStore(t *testing.T) {
	for _, b := range []database.BackendType{database.BackendFile, database.BackendPebble} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			reg, tb := newRegisteredTenantBarrier(t)
			mgr := newBarrierTenantManager(t, dir, atrest.New(newUnsealedBarrier(t)),
				lifecycle.TenantBarrierEncryptorLookup(func() *seal.BarrierRegistry { return reg }))
			defer func() { _ = mgr.Close() }()
			db, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "vault", Backend: b})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			live := newSealEngines(mb.Host(), mb.ShardID())
			docID, withTS := live.writeEveryEngine(t, "vault")
			assertControl(t, "open store", live.readEveryEngine("vault", docID, withTS))

			if err := tb.Seal(); err != nil {
				t.Fatalf("Seal: %v", err)
			}
			for _, r := range live.readEveryEngine("vault", docID, withTS) {
				if r.err == nil {
					t.Errorf("%s: a read on the open store succeeded after its tenant barrier was sealed (got %q)", r.engine, r.got)
				}
				if r.engine != "index" && strings.Contains(r.got, r.marker) {
					t.Errorf("%s: the sealed read returned the marker %q", r.engine, r.marker)
				}
			}
		})
	}
}
