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
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
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
