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
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// fakeMaterialized is a faithful stand-in for lifecycle.MaterializedBinding:
// a database.Backend for the Manager over one store and a service host over
// a store of its own, which is where the database's data plane lives.
type fakeMaterialized struct {
	*database.StandaloneBackend
	host    *backend.LocalBackend
	hub     *watch.Hub
	binding *database.Binding
	vector  service.VectorServicer
}

// Vector returns the fake database's own vector service.
func (f *fakeMaterialized) Vector() service.VectorServicer { return f.vector }

func (f *fakeMaterialized) Host() service.Host         { return f.host }
func (f *fakeMaterialized) ShardID() uint64            { return shard.ShardKV }
func (f *fakeMaterialized) WatchHub() *watch.Hub       { return f.hub }
func (f *fakeMaterialized) Binding() *database.Binding { return f.binding }
func (f *fakeMaterialized) Close() error {
	return errors.Join(f.host.Close(), f.StandaloneBackend.Close())
}

// fakeMaterializingFactory materializes every tenant database and delegates
// the rest to the embedded factory.
type fakeMaterializingFactory struct {
	mu      sync.Mutex
	created []*fakeMaterialized
	// system materializes the system tenant's databases too (except its
	// system database), as lifecycle.LocalBindingFactory does.
	system bool
}

func (f *fakeMaterializingFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	if (cfg.TenantID == "" || cfg.TenantID == "system") && (!f.system || cfg.Name == "system") {
		return database.NewEmbeddedBackendFactory().Create(cfg, dataDir)
	}
	hub := watch.NewHub()
	stg := memory.NewStorage()
	ts, err := timeseries.New(stg)
	if err != nil {
		return nil, err
	}
	eng, err := engine.NewMVCCEngine(stg, engine.WithMVCCWatchHub(hub), engine.WithMVCCTimeSeries(ts))
	if err != nil {
		return nil, err
	}
	log, err := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	if err != nil {
		return nil, err
	}
	vec, err := service.NewVectorService(vectorstorage.NewMemoryVectorStorage(), logger.NewAdapter(log))
	if err != nil {
		return nil, err
	}
	fm := &fakeMaterialized{
		StandaloneBackend: database.NewStandaloneBackend(memory.NewStorage()),
		host:              backend.NewLocalBackend(eng),
		hub:               hub,
		binding:           &database.Binding{TenantID: cfg.TenantID, Database: cfg.Name, Kind: database.BindingLocal, Backend: cfg.Backend},
		vector:            vec,
	}
	f.mu.Lock()
	f.created = append(f.created, fm)
	f.mu.Unlock()
	return fm, nil
}

func (f *fakeMaterializingFactory) Close() error { return nil }

func (f *fakeMaterializingFactory) last(t *testing.T) *fakeMaterialized {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		t.Fatal("the factory materialized nothing")
	}
	return f.created[len(f.created)-1]
}

func TestRESTServiceRegistry_MaterializedDatabaseServedFromItsOwnHost(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	cfg := database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}
	if _, err := dbMgr.Create(cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := factory.last(t)

	shared := newFilledRegistry()
	r := NewRESTServiceRegistry(shared, nil, "system", withDatabaseManager(dbMgr)).(*restServiceRegistry)

	svcs, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svcs.Placement != service.StorePlacementNode {
		t.Fatalf("Placement = %q, want %q", svcs.Placement, service.StorePlacementNode)
	}
	value := []byte("materialized-value")
	if err := svcs.KV.Put(ctx, "k", value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := service.NewKVService(fake.Host(), fake.ShardID()).Get(ctx, "k")
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("own host Get = %q, %v; want %q", got, err, value)
	}
	for _, key := range []string{"k", tenantID + "/" + name + "/k"} {
		if got, err := shared.Default().KV.Get(ctx, key); err == nil && got != nil {
			t.Fatalf("shared store holds %q = %q", key, got)
		}
	}

	again, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	db, err := dbMgr.GetOwned(name, tenantID)
	if err != nil {
		t.Fatalf("GetOwned: %v", err)
	}
	cached := r.materialized.getOrBuild(db, func() *DatabaseServices { t.Fatal("bundle rebuilt for the same instance"); return nil })
	if again.KV == nil || cached == nil {
		t.Fatal("no cached bundle")
	}

	if err := dbMgr.DropForTenant(tenant.WithTenantID(ctx, tenantID), name); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := dbMgr.Create(cfg); err != nil {
		t.Fatalf("re-Create: %v", err)
	}
	fresh := factory.last(t)
	if fresh == fake {
		t.Fatal("re-Create reused the dropped backend")
	}
	svcs, err = r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve after recreate: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err == nil && got != nil {
		t.Fatalf("recreated database serves the dropped key: %q", got)
	}
	if err := svcs.KV.Put(ctx, "k2", value); err != nil {
		t.Fatalf("Put after recreate: %v", err)
	}
	if got, err := service.NewKVService(fresh.Host(), fresh.ShardID()).Get(ctx, "k2"); err != nil || !bytes.Equal(got, value) {
		t.Fatalf("recreated own host Get = %q, %v; want %q", got, err, value)
	}
}

// flakyGetCatalog answers the first `allow` Gets from the wrapped catalog and
// fails every later one, standing in for a catalog read that fails
// transiently in the middle of a request's resolution.
type flakyGetCatalog struct {
	database.Catalog
	allow atomic.Int32
}

var errCatalogUnavailable = errors.New("catalog unavailable")

func (c *flakyGetCatalog) Get(ctx context.Context, tenantID, name string) (database.DatabaseMeta, error) {
	if c.allow.Add(-1) < 0 {
		return database.DatabaseMeta{}, errCatalogUnavailable
	}
	return c.Catalog.Get(ctx, tenantID, name)
}

// A catalog lookup that fails after the database's record was resolved never
// reads as "not materialized": resolve serves the record it already holds
// from the database's own host, and the journaled collection path returns
// the error. Neither writes into the shared (control-plane) store.
func TestRESTServiceRegistry_MaterializedLookupErrorNeverFallsToShared(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	dir := t.TempDir()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, dir, database.WithBackendFactory(factory))
	cfg := database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}
	if _, err := dbMgr.Create(cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := factory.last(t)
	flaky := &flakyGetCatalog{Catalog: databasetest.Catalog(dir)}
	dbMgr.SetCatalog(flaky)

	shared := newFilledRegistry()
	r := NewRESTServiceRegistry(shared, nil, "system", withDatabaseManager(dbMgr)).(*restServiceRegistry)

	flaky.allow.Store(1)
	value := []byte("materialized-value")
	svcs, err := r.resolve(tenantID, name)
	if err == nil {
		if svcs.Placement != service.StorePlacementNode {
			t.Fatalf("Placement = %q, want %q", svcs.Placement, service.StorePlacementNode)
		}
		if err := svcs.KV.Put(ctx, "k", value); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, gerr := service.NewKVService(fake.Host(), fake.ShardID()).Get(ctx, "k")
		if gerr != nil || !bytes.Equal(got, value) {
			t.Fatalf("own host Get = %q, %v; want %q", got, gerr, value)
		}
	}

	flaky.allow.Store(0)
	if _, err := r.resolve(tenantID, name); !errors.Is(err, errCatalogUnavailable) {
		t.Fatalf("resolve with a failing catalog = %v, want %v", err, errCatalogUnavailable)
	}
	if _, err := r.journaledCollectionService(tenantID, name); !errors.Is(err, errCatalogUnavailable) {
		t.Fatalf("journaledCollectionService with a failing catalog = %v, want %v", err, errCatalogUnavailable)
	}
	for _, key := range []string{"k", tenantID + "/" + name + "/k"} {
		if got, err := shared.Default().KV.Get(ctx, key); err == nil && got != nil {
			t.Fatalf("shared store holds %q = %q", key, got)
		}
	}
}

// writeAfterFence is a reclaimer that, once the drop fence has landed and
// before the Manager closes the database, writes through a handle resolved
// before the drop: the race the drop fence exists for.
type writeAfterFence struct {
	*service.KeyspaceReclaimService
	write func() error
	err   error
}

func (w *writeAfterFence) FenceDatabaseKeyspace(ctx context.Context, tenantID, db, incarnation, claim string) error {
	if err := w.KeyspaceReclaimService.FenceDatabaseKeyspace(ctx, tenantID, db, incarnation, claim); err != nil {
		return err
	}
	if w.write != nil {
		w.err = w.write()
	}
	return nil
}

// newFencedMaterializedManager returns a Manager materializing acme's
// databases, whose reclaimer keeps each materialized database's drop fence in
// its own store (storeResolver.materializedFenceTarget).
func newFencedMaterializedManager(t *testing.T) (*database.Manager, *fakeMaterializingFactory, *writeAfterFence) {
	t.Helper()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	reclaim := &writeAfterFence{KeyspaceReclaimService: service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})}
	reclaim.SetStoreResolver(&storeResolver{databases: dbMgr, systemTenant: tenantid.System})
	dbMgr.SetKeyspaceReclaimer(reclaim)
	return dbMgr, factory, reclaim
}

// TestRESTServiceRegistry_MaterializedHandleResolvedBeforeDropIsFenced pins
// F5: a materialized database's views carry the drop-fence guard, checked
// against the marker in its own store, so a handle resolved before Drop is
// refused with *database.DroppingError once the fence lands.
func TestRESTServiceRegistry_MaterializedHandleResolvedBeforeDropIsFenced(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	dbMgr, _, reclaim := newFencedMaterializedManager(t)
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
	svcs, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := svcs.KV.Put(ctx, "before", []byte("v")); err != nil {
		t.Fatalf("Put before the drop: %v", err)
	}
	reclaim.write = func() error { return svcs.KV.Put(ctx, "after", []byte("v")) }
	if err := dbMgr.DropForTenant(tenant.WithTenantID(ctx, tenantID), name); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	assertDroppingError(t, reclaim.err, name)
}

// faultyOwnedSource fails the resolver's catalog lookup while fail is set,
// as a catalog read that does not answer during a DROP does.
type faultyOwnedSource struct {
	ownedDatabaseSource
	fail atomic.Bool
}

func (s *faultyOwnedSource) GetOwnedIgnoringFence(name, tenantID string) (*database.Database, error) {
	if s.fail.Load() {
		return nil, errCatalogUnavailable
	}
	return s.ownedDatabaseSource.GetOwnedIgnoringFence(name, tenantID)
}

// A catalog read that fails while a DROP resolves a materialized database's
// fence fails the drop closed: the shared store is left as it was, not fenced
// in the database's place, and the database's own marker stays live, so a
// handle resolved before the drop still writes and the drop, retried, fences
// the database's own store.
func TestRESTServiceRegistry_MaterializedDropFailsClosedOnALookupFault(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	sharedKV := newMockKVService()
	reclaim := &writeAfterFence{KeyspaceReclaimService: service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: sharedKV})}
	source := &faultyOwnedSource{ownedDatabaseSource: dbMgr}
	reclaim.SetStoreResolver(&storeResolver{databases: source, systemTenant: tenantid.System})
	dbMgr.SetKeyspaceReclaimer(reclaim)
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
	svcs, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	before := maps.Clone(sharedKV.store)
	source.fail.Store(true)
	if err := dbMgr.DropForTenant(tenant.WithTenantID(ctx, tenantID), name); !errors.Is(err, errCatalogUnavailable) {
		t.Fatalf("DropForTenant with a failing lookup = %v, want %v", err, errCatalogUnavailable)
	}
	if !maps.EqualFunc(sharedKV.store, before, bytes.Equal) {
		t.Fatalf("the shared store was fenced in the database's place: %q, want %q", sharedKV.store, before)
	}
	if err := svcs.KV.Put(ctx, "after-failed-drop", []byte("v")); err != nil {
		t.Fatalf("Put after the failed drop: %v (the database's own marker was tombstoned)", err)
	}

	source.fail.Store(false)
	reclaim.write = func() error { return svcs.KV.Put(ctx, "after", []byte("v")) }
	if err := dbMgr.DropForTenant(tenant.WithTenantID(ctx, tenantID), name); err != nil {
		t.Fatalf("DropForTenant retried: %v", err)
	}
	assertDroppingError(t, reclaim.err, name)
}

// A handle resolved before Drop and used after the name is created again is
// refused: it belongs to the dropped incarnation, so its write lands neither
// in the dropped database nor in the new one under the same name.
func TestRESTServiceRegistry_MaterializedHandleFromBeforeDropRefusedAfterRecreate(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	dbMgr, _, _ := newFencedMaterializedManager(t)
	cfg := database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}
	if _, err := dbMgr.Create(cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
	stale, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := dbMgr.DropForTenant(tenant.WithTenantID(ctx, tenantID), name); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if _, err := dbMgr.Create(cfg); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	// The materialized store is the incarnation: the stale handle's views
	// are over the dropped database's own store, which Drop closed, so the
	// refusal is that store's -- the recreated database is another store.
	if err := stale.KV.Put(ctx, "stale", []byte("v")); err == nil {
		t.Fatal("a handle from the dropped incarnation accepted a write after the name was recreated")
	}
	fresh, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve the recreated database: %v", err)
	}
	if got, err := fresh.KV.Get(ctx, "stale"); err == nil && got != nil {
		t.Fatalf("the recreated database holds the stale handle's write %q", got)
	}
	if err := fresh.KV.Put(ctx, "fresh", []byte("v")); err != nil {
		t.Fatalf("Put on the recreated database: %v", err)
	}
}

// TestRESTServiceRegistry_MaterializedScheduledCompactionRuns pins F5's
// second half: the compaction record a materialized database's policy writes
// is guarded on the marker in its own store, which CompactionScheduleGuard
// finds live, so the scheduler compacts it.
func TestRESTServiceRegistry_MaterializedScheduledCompactionRuns(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	dbMgr, _, _ := newFencedMaterializedManager(t)
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
	svcs, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := svcs.KV.Put(ctx, "k", []byte{byte('a' + i)}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	holder, ok := svcs.MVCC.(service.CompactionPolicyHolder)
	if !ok {
		t.Fatalf("MVCC view %T holds no compaction policy", svcs.MVCC)
	}
	if err := holder.SetCompactionPolicy(ctx, &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 1}); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithDatabaseManager(dbMgr))
	s := &compactionScheduler{
		stores:   srv.compactionStores,
		interval: time.Hour,
		now:      time.Now,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if n := s.passOnce(ctx); n != 1 {
		t.Fatalf("compaction pass compacted %d stores, want the materialized database's 1", n)
	}
}

// TestStoreResolver_FenceTargetMaterializedDatabase pins where FenceTarget
// sends a materialized database's drop fence: its own store, per database;
// a stale incarnation is refused as superseded; and a database the Manager
// does not hold keeps the system store.
func TestStoreResolver_FenceTargetMaterializedDatabase(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	dbMgr, _, _ := newFencedMaterializedManager(t)
	db, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := &storeResolver{databases: dbMgr, systemTenant: tenantid.System}

	store, dedicated, err := r.FenceTarget(ctx, tenantID, name, db.Incarnation())
	if err != nil || !dedicated || !store.PerDatabase || store.KV == nil {
		t.Fatalf("FenceTarget = (%+v, %v, %v), want the database's own store, per database", store, dedicated, err)
	}

	_, dedicated, err = r.FenceTarget(ctx, tenantID, name, "a-superseded-incarnation")
	var superseded *database.DropSupersededError
	if !errors.As(err, &superseded) || !dedicated {
		t.Fatalf("FenceTarget(stale incarnation) = (%v, %v), want *database.DropSupersededError", dedicated, err)
	}

	if _, dedicated, err := r.FenceTarget(ctx, tenantID, "missing", ""); err != nil || dedicated {
		t.Fatalf("FenceTarget(unknown database) = (%v, %v), want the system store", dedicated, err)
	}
}

// TestRESTServiceRegistry_MaterializedDatabaseServesItsOwnVectors proves a
// materialized database answers vector calls from its binding's own vector
// service: a put lands there and a search through the bundle finds it.
func TestRESTServiceRegistry_MaterializedDatabaseServesItsOwnVectors(t *testing.T) {
	const tenantID, name = "acme", "embeddings"
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := factory.last(t)
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr)).(*restServiceRegistry)
	svcs, err := r.resolve(tenantID, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svcs.Vector == nil {
		t.Fatal("materialized bundle has no vector service")
	}
	if _, err := svcs.Vector.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		if strings.Contains(err.Error(), "qrdb_vector") {
			t.Skipf("vector not built: %v", err)
		}
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := svcs.Vector.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 7, Vector: []float32{1, 0}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	resp, err := svcs.Vector.SearchSimilar(ctx, &service.SearchSimilarRequest{Namespace: "ns", QueryVector: []float32{1, 0}, K: 1})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].ID != 7 {
		t.Fatalf("SearchSimilar = %+v, want id 7", resp.Results)
	}
	list, err := fake.Vector().ListNamespaces(ctx, &service.ListNamespacesRequest{})
	if err != nil || len(list.Namespaces) == 0 {
		t.Fatalf("own vector service namespaces = %+v, %v; want the created namespace", list, err)
	}
}

func TestRESTServiceRegistry_MaterializedSystemTenantDatabaseServedFromItsOwnHost(t *testing.T) {
	const name = "ledger"
	ctx := context.Background()
	factory := &fakeMaterializingFactory{system: true}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: "", Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake := factory.last(t)
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: "", Name: "system", Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create system: %v", err)
	}

	// The node's shared set is registered under the system database.
	shared := NewDatabaseServiceRegistry("system")
	shared.Register("system", newFilledServices("system"))
	r := NewRESTServiceRegistry(shared, nil, "system", withDatabaseManager(dbMgr)).(*restServiceRegistry)

	svcs, err := r.resolve("system", name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svcs.Placement != service.StorePlacementNode {
		t.Fatalf("Placement = %q, want %q", svcs.Placement, service.StorePlacementNode)
	}
	value := []byte("system-tenant-value")
	if err := svcs.KV.Put(ctx, "k", value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := service.NewKVService(fake.Host(), fake.ShardID()).Get(ctx, "k")
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("own host Get = %q, %v; want %q", got, err, value)
	}
	for _, key := range []string{"k", "system/" + name + "/k"} {
		if got, err := shared.Default().KV.Get(ctx, key); err == nil && got != nil {
			t.Fatalf("shared store holds %q = %q", key, got)
		}
	}

	factory.mu.Lock()
	created := len(factory.created)
	factory.mu.Unlock()
	sys, err := r.resolve("system", "system")
	if err != nil {
		t.Fatalf("resolve system database: %v", err)
	}
	if sys.Placement == service.StorePlacementNode {
		t.Fatalf("the system database is served from its own host (Placement = %q), want the shared set", sys.Placement)
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.created) != created {
		t.Fatal("the factory materialized the system database")
	}
}

// The configuration WithBindingServiceConfig gives the server reaches the
// registry every transport resolves through, so a materialized database's
// services are built with the node's limits rather than package defaults.
func TestServer_RestRegistryOptions_CarryBindingServiceConfig(t *testing.T) {
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(&fakeMaterializingFactory{}))
	t.Cleanup(func() { _ = dbMgr.Close() })
	importSession := qrdbconfig.ImportSessionConfig{MaxSessionBytes: 4096}
	index := qrdbconfig.IndexConfig{MaxIndexes: 3}
	ts := qrdbconfig.TimeSeriesConfig{DefaultRetention: 60}

	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseManager(dbMgr), WithBindingServiceConfig(importSession, index, ts))
	r := newRESTRegistry(NewDatabaseServiceRegistry("system"), nil, tenantid.System, srv.restRegistryOptions()...)
	if r.bindingImportSession != importSession {
		t.Fatalf("import session = %+v, want %+v", r.bindingImportSession, importSession)
	}
	if r.bindingIndex != index {
		t.Fatalf("index = %+v, want %+v", r.bindingIndex, index)
	}
	if r.bindingTimeSeries.DefaultRetention != ts.DefaultRetention {
		t.Fatalf("time series = %+v, want %+v", r.bindingTimeSeries, ts)
	}
}
