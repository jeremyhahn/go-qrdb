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

package server

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// A database the manager opens over a file store of its own, with no
// per-database barrier, is still served from the node's store: its file store
// holds no data. That is why the at-rest report (capabilities
// UnencryptedAtRest) judges the node's own store and not every database's
// backend -- a database's data is encrypted exactly when the node's store is,
// unless it carries a per-database barrier over its own store, which
// encrypts it separately.
func TestRESTServiceRegistry_FileBackendDatabaseServedFromNodeStore(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		Name:    "files",
		Backend: database.BackendFile,
		DataDir: filepath.Join(t.TempDir(), "files"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	nodeStore := newFilledRegistry()
	reg := NewRESTServiceRegistry(nodeStore, nil, tenantid.System,
		WithRegistryDatabaseManager(mgr),
	)

	kv, err := reg.KVService(tenantid.System, "files")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := nodeStore.Default().KV.Get(ctx, tenant.ComposeNamespace(tenantid.System, "files")+"k"); err != nil || string(got) != "v" {
		t.Fatalf("the write is not in the node's store: %q, %v", got, err)
	}
	own, err := db.Backend().List(ctx, "")
	if err != nil {
		t.Fatalf("List own store: %v", err)
	}
	if len(own) != 0 {
		t.Fatalf("the database's own file store holds data: %v", own)
	}
}

// A `backend: objstore` database is served from its own store, over its
// provider, with no barrier: a write through the registry lands with the
// provider and is read back from it, and the node's shared store never sees
// it.
func TestRESTServiceRegistry_ObjstoreDatabaseServedFromItsProvider(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		Name:    "blobs",
		Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{
			Provider: "local",
			Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	nodeStore := newFilledRegistry()
	reg := NewRESTServiceRegistry(nodeStore, nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		setRegistryDatabaseManager(mgr),
	)

	kv, err := reg.KVService(tenantid.System, "blobs")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := kv.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("Get through the registry = %q, %v; want \"v\"", got, err)
	}
	if got, err := db.Backend().Get(ctx, []byte("k")); err != nil || string(got) != "v" {
		t.Fatalf("the write is not in the database's own store: %q, %v", got, err)
	}
	if got, err := nodeStore.Default().KV.Get(ctx, tenant.ComposeNamespace(tenantid.System, "blobs")+"k"); err == nil && got != nil {
		t.Fatalf("the write reached the node's shared store: %q, %v", got, err)
	}
}

// newObjstoreRegistry returns a system-tenant registry serving one objstore
// database "blobs" from a local provider, plus that database.
func newObjstoreRegistry(t *testing.T) (*restServiceRegistry, *database.Database) {
	t.Helper()
	mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		Name:    "blobs",
		Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{
			Provider: "local",
			Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		setRegistryDatabaseManager(mgr),
	)
	r, ok := reg.(*restServiceRegistry)
	if !ok {
		t.Fatalf("NewRESTServiceRegistry returned %T, want *restServiceRegistry", reg)
	}
	return r, db
}

// TestRESTServiceRegistry_ObjstoreResolveDoesNotMutateCachedBundle pins that
// resolving an objstore database hands back a copy carrying the node
// placement and leaves the cached dedicated-store bundle untouched.
func TestRESTServiceRegistry_ObjstoreResolveDoesNotMutateCachedBundle(t *testing.T) {
	r, db := newObjstoreRegistry(t)
	got, ok := r.resolveObjstoreDatabase(db, tenantid.System, nil)
	if !ok || got == nil {
		t.Fatalf("resolveObjstoreDatabase = %v, %v; want a bundle", got, ok)
	}
	if got.Placement != service.StorePlacementNode {
		t.Fatalf("Placement = %q, want %q", got.Placement, service.StorePlacementNode)
	}
	cached := r.dedicatedStore.getOrBuild(db)
	if cached == got {
		t.Fatal("resolveObjstoreDatabase returned the cached bundle itself")
	}
	if cached.Placement != "" {
		t.Fatalf("cached bundle Placement = %q, want empty (resolution mutated the shared bundle)", cached.Placement)
	}
}

// TestRESTServiceRegistry_ObjstoreConcurrentResolve resolves the same objstore
// database from many goroutines; under -race it reports any write to the
// shared cached bundle.
func TestRESTServiceRegistry_ObjstoreConcurrentResolve(t *testing.T) {
	r, db := newObjstoreRegistry(t)
	const workers, rounds = 16, 50
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				svcs, ok := r.resolveObjstoreDatabase(db, tenantid.System, nil)
				if !ok || svcs.Placement != service.StorePlacementNode {
					errs <- "resolve did not return a node-placed bundle"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// failAfterFirstGetCatalog answers the first Get from the catalog it wraps
// and every later Get with errCatalogReadFault: a catalog read that fails
// between two lookups of one request. gets counts every Get.
type failAfterFirstGetCatalog struct {
	database.Catalog
	gets atomic.Int64
}

var errCatalogReadFault = errors.New("catalog read fault")

func (c *failAfterFirstGetCatalog) Get(ctx context.Context, tenantID, name string) (database.DatabaseMeta, error) {
	if c.gets.Add(1) > 1 {
		return database.DatabaseMeta{}, errCatalogReadFault
	}
	return c.Catalog.Get(ctx, tenantID, name)
}

// TestRESTServiceRegistry_ObjstoreCatalogFailureAfterLookupNeverFallsBackToShared
// is the #1577 Codex round-3 P1: resolve looks the database up once
// (managedDatabase) and serves it from that record. A catalog read that
// fails after that lookup must not route the objstore database onto the
// node's shared store, where its writes have no guard.
func TestRESTServiceRegistry_ObjstoreCatalogFailureAfterLookupNeverFallsBackToShared(t *testing.T) {
	dataDir := t.TempDir()
	mgr := databasetest.NewManager(nil, dataDir, database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		Name:    "blobs",
		Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{
			Provider: "local",
			Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	nodeStore := newFilledRegistry()
	reg := NewRESTServiceRegistry(nodeStore, nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		setRegistryDatabaseManager(mgr),
	)

	faulty := &failAfterFirstGetCatalog{Catalog: databasetest.Catalog(dataDir)}
	mgr.SetCatalog(faulty)
	kv, err := reg.KVService(tenantid.System, "blobs")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	if got := faulty.gets.Load(); got != 1 {
		t.Fatalf("resolve read the catalog %d times, want exactly one lookup", got)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := db.Backend().Get(ctx, []byte("k")); err != nil || string(got) != "v" {
		t.Fatalf("the write is not in the database's own store: %q, %v", got, err)
	}
	if got, err := nodeStore.Default().KV.Get(ctx, tenant.ComposeNamespace(tenantid.System, "blobs")+"k"); err == nil && got != nil {
		t.Fatalf("the write reached the node's shared store: %q, %v", got, err)
	}
}

// TestRESTServiceRegistry_VolumeTenantObjstoreDatabaseServedFromItsProvider
// pins that a volume-isolated tenant's objstore database is routed to its own
// store, not to the tenant's per-tenant Host: the volume branch of resolve
// would otherwise serve it from the tenant's shard (or, with no Host registry,
// refuse it) while the database reports objstore.
func TestRESTServiceRegistry_VolumeTenantObjstoreDatabaseServedFromItsProvider(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.Create(database.DatabaseConfig{
		TenantID: "acme",
		Name:     "blobs",
		Backend:  database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{
			Provider: "local",
			Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System,
		WithDatabaseCatalog(newManagerCatalog(mgr)),
		setRegistryDatabaseManager(mgr),
		WithTenantServicer(&isolationTenantServicer{isolation: tenant.IsolationVolume}),
	)

	kv, err := reg.KVService("acme", "blobs")
	if err != nil {
		t.Fatalf("KVService for a volume tenant's objstore database: %v", err)
	}
	ctx := context.Background()
	if err := kv.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := db.Backend().Get(ctx, []byte("k")); err != nil || string(got) != "v" {
		t.Fatalf("the write is not in the database's own store: %q, %v", got, err)
	}

	// Another tenant does not reach it, and the tenant's other databases
	// still take the volume branch (which fails closed with no Host registry).
	if _, err := reg.KVService("other", "blobs"); err == nil {
		t.Fatal("another tenant resolved acme's objstore database")
	}
	var ibu *rest.IsolationBackendUnavailableError
	if _, err := reg.KVService("acme", "default"); !errors.As(err, &ibu) {
		t.Fatalf("acme/default = %v, want *rest.IsolationBackendUnavailableError", err)
	}
}

// An objstore database opens the objstore engine from its own settings: one
// that names no provider fails the open, before any store is created.
func TestDatabaseManager_ObjstoreDatabaseWithoutProviderRefused(t *testing.T) {
	mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	var create *database.StorageCreateError
	if !errors.As(err, &create) || create.Backend != string(database.BackendObjstore) {
		t.Fatalf("Create(objstore, no provider) = %v, want *database.StorageCreateError for objstore", err)
	}
}

// setRegistryDatabaseManager attaches mgr as the registry's database manager,
// the half of the server's registry wiring that the WithDatabaseCatalog
// option alongside it does not cover. The test sets the field itself so it
// builds against both this branch and main, where the package-private
// withDatabaseManager option was folded into WithRegistryDatabaseManager.
func setRegistryDatabaseManager(mgr *database.Manager) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.dbManager = mgr
	}
}
