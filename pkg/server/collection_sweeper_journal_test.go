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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	nodeA := NewRESTServiceRegistry(regA, nil, tenant.SystemTenantID,
		WithDatabaseCatalog(staticCatalog{}))
	coll, err := nodeA.CollectionService(tenant.SystemTenantID, "appdb")
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
	reg := NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID).(*restServiceRegistry)
	coll, err := reg.journaledCollectionService(tenant.SystemTenantID, "appdb")
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
	j := &collectionWorkJournal{kv: kv, tenant: tenant.SystemTenantID}
	if err := j.Record(ctx, "appdb", "gone"); err != nil {
		t.Fatal(err)
	}

	srv := New(testConfig(t, false, false), kv, &mockAdminService{}, WithDatabaseServiceRegistry(dbReg))
	sweeper := &collectionSweeper{registry: reg, targets: srv.collectionSweepTargets, staleAfter: time.Minute, logger: slog.Default()}
	sweeper.sweepOnce(ctx)

	for name, want := range map[string]bool{"live": false, "gone": false, "fresh": true} {
		if _, ok := kv.store[collectionWorkKey(tenant.SystemTenantID, "appdb", name)]; ok != want {
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
	reg := NewRESTServiceRegistry(NewDatabaseServiceRegistry("system"), nil, tenant.SystemTenantID).(*restServiceRegistry)
	var notServed *CollectionWorkNotServedError
	if _, err := reg.journaledCollectionService(tenant.SystemTenantID, "appdb"); !errors.As(err, &notServed) {
		t.Fatalf("a registry with no bundle serves nothing: %v", err)
	}
	if !strings.Contains(notServed.Error(), "not served by this node") {
		t.Fatal(notServed.Error())
	}
	if _, err := reg.journaledCollectionService("bad/tenant", "appdb"); err == nil {
		t.Fatal("tenant resolution still applies")
	}
}
