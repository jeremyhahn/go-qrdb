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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
}

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
}

func (f *fakeMaterializingFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	if cfg.TenantID == "" || cfg.TenantID == "system" {
		return database.NewEmbeddedBackendFactory().Create(cfg, dataDir)
	}
	hub := watch.NewHub()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCWatchHub(hub))
	if err != nil {
		return nil, err
	}
	fm := &fakeMaterialized{
		StandaloneBackend: database.NewStandaloneBackend(memory.NewStorage()),
		host:              backend.NewLocalBackend(eng),
		hub:               hub,
		binding:           &database.Binding{TenantID: cfg.TenantID, Database: cfg.Name, Kind: database.BindingLocal, Backend: cfg.Backend},
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
