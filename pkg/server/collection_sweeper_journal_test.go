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
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestCollectionSweeper_LeaderWithoutTheCatalogEntryFinishesTheDrop: a named
// database of a prefix tenant lives in the shared, replicated store, but only
// the node that created it has it in its node-local catalog. Once another
// node leads the system shard, that node must still find and finish a drop
// interrupted in that database -- from the replicated store, not from a
// catalog it does not have.
func TestCollectionSweeper_LeaderWithoutTheCatalogEntryFinishesTheDrop(t *testing.T) {
	ctx := context.Background()
	kv, svcs := sharedCollectionStore()

	// Node A: its catalog has the database; it creates a collection and a
	// drop of it crashes after marking it DROPPING.
	regA := NewDatabaseServiceRegistry("system")
	regA.Register("system", svcs)
	nodeA := NewRESTServiceRegistry(regA, nil, tenantid.System,
		WithDatabaseCatalog(staticCatalog{}))
	coll, err := nodeA.CollectionService(tenantid.System, "appdb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "appdb", Name: "events"}); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "appdb", Collection: "events", Document: map[string]interface{}{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	markCollectionDropping(t, kv)

	// Node B: the same replicated store, an empty catalog, and it leads.
	regB := NewDatabaseServiceRegistry("system")
	regB.Register("system", svcs)
	nodeB := New(testConfig(t, false, false), kv, &mockAdminService{},
		WithDatabaseServiceRegistry(regB),
		WithCollectionSweepLeader(func() bool { return true }))
	sweeper := &collectionSweeper{
		registry:   nodeB.buildServiceRegistry(),
		targets:    nodeB.collectionSweepTargets,
		staleAfter: time.Minute,
		logger:     slog.Default(),
	}
	if got := sweeper.sweepOnce(ctx); got != 1 {
		t.Fatalf("sweepOnce finished %d collections, want 1", got)
	}
	for key := range kv.store {
		if strings.Contains(key, "__sys:collections/") || strings.Contains(key, "__col:") {
			t.Fatalf("the interrupted drop was not finished: %s survives", key)
		}
	}
	for key := range kv.store {
		if strings.HasPrefix(key, "__sys:collection-work/") {
			t.Fatalf("the finished collection's work entry survives: %s", key)
		}
	}
}

// TestCollectionSweeper_WarnsOnARepeatedlyUnresolvableJournaledDatabase: a
// database holding journaled collection work that cannot be resolved is
// logged at Debug for its first failures and at Warn from the
// collectionSweepWarnAfter-th consecutive pass on; a successful resolution
// resets the count.
func TestCollectionSweeper_WarnsOnARepeatedlyUnresolvableJournaledDatabase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	kv := newMockKVService()
	target := collectionSweepTarget{Tenant: "t1", Database: "db", workKV: kv, work: []collectionWorkItem{{key: "k"}}}
	sweeper := &collectionSweeper{
		// No journaled resolver: every journaled target is unresolvable.
		registry: &slowRegistry{svc: &slowSweeper{}},
		targets:  func(context.Context) ([]collectionSweepTarget, error) { return []collectionSweepTarget{target}, nil },
		logger:   logger,
	}
	for pass := 1; pass <= collectionSweepWarnAfter; pass++ {
		buf.Reset()
		sweeper.sweepOnce(context.Background())
		warned := strings.Contains(buf.String(), "level=WARN")
		if want := pass >= collectionSweepWarnAfter; warned != want {
			t.Fatalf("pass %d: warned=%v, want %v: %s", pass, warned, want, buf.String())
		}
	}
	// A target not found through the journal holds no known stuck work: a
	// failure to resolve it stays at Debug however often it happens.
	plain := collectionSweepTarget{Tenant: "t1", Database: "other"}
	failing := &collectionSweeper{
		registry: failingCollectionRegistry{},
		targets:  func(context.Context) ([]collectionSweepTarget, error) { return []collectionSweepTarget{plain}, nil },
		logger:   logger,
	}
	for pass := 0; pass < collectionSweepWarnAfter+1; pass++ {
		buf.Reset()
		failing.sweepOnce(context.Background())
		if strings.Contains(buf.String(), "level=WARN") {
			t.Fatalf("an unjournaled target is not warned about: %s", buf.String())
		}
	}
}

// failingCollectionRegistry resolves no collection service.
type failingCollectionRegistry struct {
	service.Registry
}

func (failingCollectionRegistry) CollectionService(tenantID, db string) (service.CollectionServicer, error) {
	return nil, &CollectionWorkNotServedError{TenantID: tenantID, Database: db}
}

// TestCollectionSweeper_SettlesOnlyFinishedWork: after a sweep, a journal
// entry is removed when its collection is active or gone, and kept while the
// collection is still transitional -- a create in progress, not yet stale --
// or when a later transition re-recorded it.
func TestCollectionSweeper_SettlesOnlyFinishedWork(t *testing.T) {
	ctx := context.Background()
	kv, svcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", svcs)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System).(*restServiceRegistry)
	coll, err := reg.journaledCollectionService(tenantid.System, "appdb")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"live", "fresh"} {
		if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "appdb", Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	// "fresh" is mid-drop, recently: not yet the sweeper's to finish.
	for key, raw := range kv.store {
		if !strings.HasSuffix(key, "__sys:collections/appdb/fresh") {
			continue
		}
		var info service.CollectionInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			t.Fatal(err)
		}
		info.State, info.StateSince = service.CollectionStateDropping, time.Now()
		data, _ := json.Marshal(&info)
		kv.store[key] = data
	}
	// "gone" never existed; its entry is settled too.
	j := &collectionWorkJournal{kv: kv, tenant: tenantid.System}
	if err := j.Record(ctx, "appdb", "gone"); err != nil {
		t.Fatal(err)
	}

	srv := New(testConfig(t, false, false), kv, &mockAdminService{}, WithDatabaseServiceRegistry(dbReg))
	sweeper := &collectionSweeper{registry: reg, targets: srv.collectionSweepTargets, staleAfter: time.Minute, logger: slog.Default()}
	sweeper.sweepOnce(ctx)

	for name, want := range map[string]bool{"live": false, "gone": false, "fresh": true} {
		if _, ok := kv.store[collectionWorkKey(tenantid.System, "appdb", name)]; ok != want {
			t.Errorf("work entry for %q present=%v, want %v", name, ok, want)
		}
	}
}

// TestServesFromNodeLocalStore covers the predicate the registry and the
// sweeper share.
func TestServesFromNodeLocalStore(t *testing.T) {
	enabled := &database.DatabaseBarrierConfig{Enabled: true}
	cases := map[string]struct {
		cfg  database.DatabaseConfig
		want bool
	}{
		"barrier over its own data dir": {database.DatabaseConfig{DataDir: "/d", Barrier: enabled}, true},
		"no barrier":                    {database.DatabaseConfig{DataDir: "/d"}, false},
		"barrier disabled":              {database.DatabaseConfig{DataDir: "/d", Barrier: &database.DatabaseBarrierConfig{}}, false},
		"shared store":                  {database.DatabaseConfig{Barrier: enabled}, false},
		"raft backed":                   {database.DatabaseConfig{Backend: database.BackendRaft, Barrier: enabled}, false},
	}
	for name, tc := range cases {
		if got := servesFromNodeLocalStore(&tc.cfg); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// TestJournaledCollectionService_NotServed covers the stores a journal entry
// cannot be resolved against on this node.
func TestJournaledCollectionService_NotServed(t *testing.T) {
	reg := NewRESTServiceRegistry(NewDatabaseServiceRegistry("system"), nil, tenantid.System).(*restServiceRegistry)
	var notServed *CollectionWorkNotServedError
	if _, err := reg.journaledCollectionService(tenantid.System, "appdb"); !errors.As(err, &notServed) {
		t.Fatalf("a registry with no bundle serves nothing: %v", err)
	}
	if !strings.Contains(notServed.Error(), "not served by this node") {
		t.Fatal(notServed.Error())
	}
	if _, err := reg.journaledCollectionService("bad/tenant", "appdb"); err == nil {
		t.Fatal("tenant resolution still applies")
	}
}

// A database the Manager opened in its own store is journaled against its
// own host: a collection the journaled service creates is in that store and
// never in the shared one.
func TestJournaledCollectionService_MaterializedDatabaseUsesItsOwnHost(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := context.Background()
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	shared := newFilledRegistry()
	reg := NewRESTServiceRegistry(shared, nil, tenantid.System, WithRegistryDatabaseManager(dbMgr)).(*restServiceRegistry)

	coll, err := reg.journaledCollectionService(tenantID, name)
	if err != nil {
		t.Fatalf("journaledCollectionService: %v", err)
	}
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: name, Name: "events"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	fake := factory.last(t)
	own := service.NewCollectionService(service.NewKVService(fake.Host(), fake.ShardID()))
	listed, err := own.ListCollections(ctx, &service.ListCollectionsRequest{Database: name})
	if err != nil || len(listed.Collections) != 1 || listed.Collections[0].Name != "events" {
		t.Fatalf("own host collections = %+v, %v; want events", listed, err)
	}
	if sharedColl := shared.Default().Collection; sharedColl != nil {
		if got, err := sharedColl.ListCollections(ctx, &service.ListCollectionsRequest{Database: name}); err == nil && len(got.Collections) > 0 {
			t.Fatalf("shared store holds collections %+v", got.Collections)
		}
	}
}

// TestCollectionSweeper_FinishesAnInterruptedCreateInAMaterializedDatabase
// proves a collection create a crash left CREATING in a database the
// Manager opened in its own store is swept to a final state by one pass,
// and its work entry in that store is settled.
func TestCollectionSweeper_FinishesAnInterruptedCreateInAMaterializedDatabase(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	factory := &fakeMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(newFilledRegistry()), WithDatabaseManager(dbMgr))
	registry := srv.buildServiceRegistry()
	coll, err := registry.CollectionService(tenantID, name)
	if err != nil {
		t.Fatalf("CollectionService: %v", err)
	}
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: name, Name: "events"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	// The crash: the catalog record is left CREATING, an hour old, and its
	// work entry is still journaled in the database's own store.
	fake := factory.last(t)
	own := service.NewKVService(fake.Host(), fake.ShardID())
	key := "__sys:collections/" + name + "/events"
	raw, err := own.Get(ctx, key)
	if err != nil || raw == nil {
		t.Fatalf("own store catalog record = %q, %v", raw, err)
	}
	var info service.CollectionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	info.State = service.CollectionStateCreating
	info.StateSince = time.Now().Add(-time.Hour)
	data, err := json.Marshal(&info)
	if err != nil {
		t.Fatal(err)
	}
	if err := own.Put(ctx, key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := (&collectionWorkJournal{kv: own, tenant: tenantID}).Record(ctx, name, "events"); err != nil {
		t.Fatalf("journal Record: %v", err)
	}

	sweeper := &collectionSweeper{
		registry:   registry,
		targets:    srv.collectionSweepTargets,
		staleAfter: time.Minute,
		logger:     slog.Default(),
	}
	if got := sweeper.sweepOnce(ctx); got < 1 {
		t.Fatalf("sweepOnce finished %d collections, want the interrupted create", got)
	}
	got, err := coll.GetCollectionInfo(ctx, name, "events")
	var notFound *service.CollectionNotFoundError
	switch {
	case errors.As(err, &notFound):
	case err != nil:
		t.Fatalf("GetCollectionInfo: %v", err)
	case got.State != service.CollectionStateActive:
		t.Fatalf("collection state after the sweep = %q, want a final state", got.State)
	}
	work, err := scanCollectionWork(ctx, own)
	if err != nil {
		t.Fatalf("scanCollectionWork: %v", err)
	}
	if len(work) != 0 {
		t.Fatalf("work entries survive the sweep: %+v", work)
	}
}
