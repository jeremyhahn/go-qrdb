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
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// markCollectionDropping rewrites every stored collection record in kv into
// the DROPPING state, entered long ago -- the state a drop that crashed after
// its first write leaves behind.
func markCollectionDropping(t *testing.T, kv *mockKVService) {
	t.Helper()
	marked := 0
	for key, raw := range kv.store {
		if !strings.Contains(key, "__sys:collections/") {
			continue
		}
		var info service.CollectionInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
		info.State = service.CollectionStateDropping
		info.StateSince = time.Now().Add(-time.Hour)
		data, err := json.Marshal(&info)
		if err != nil {
			t.Fatal(err)
		}
		kv.store[key] = data
		marked++
	}
	if marked == 0 {
		t.Fatal("no collection record to mark")
	}
}

// TestCollectionSweeper_FinishesAnAbandonedDrop drives the sweeper through the
// same registry the transports use. A drop that crashed after marking its
// collection DROPPING is finished without anybody re-issuing it.
func TestCollectionSweeper_FinishesAnAbandonedDrop(t *testing.T) {
	ctx := context.Background()
	kv, reg := twoTenantCollectionRegistry(t)
	coll, err := reg.CollectionService(tenant.SystemTenantID, "appdb")
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

	sweeper := &collectionSweeper{
		registry: reg,
		targets: func(context.Context) ([]collectionSweepTarget, error) {
			return []collectionSweepTarget{
				{Tenant: tenant.SystemTenantID, Database: "appdb"},
				// An unresolvable target is skipped, not fatal.
				{Tenant: "no-such-tenant/", Database: "appdb"},
			}, nil
		},
		staleAfter: time.Minute,
		logger:     slog.Default(),
	}
	if got := sweeper.sweepOnce(ctx); got != 1 {
		t.Fatalf("sweepOnce finished %d collections, want 1", got)
	}
	if _, err := coll.GetCollectionInfo(ctx, "appdb", "events"); err == nil {
		t.Fatal("the abandoned drop must be finished")
	}
	for key := range kv.store {
		if strings.Contains(key, "__col:") {
			t.Fatalf("a document survived the swept drop: %s", key)
		}
	}
}

func TestCollectionSweeper_PassesDoNotOverlapAndSurviveEnumerationFailure(t *testing.T) {
	sweeper := &collectionSweeper{
		targets: func(context.Context) ([]collectionSweepTarget, error) {
			return nil, errors.New("tenant store unavailable")
		},
		logger: slog.Default(),
	}
	if got := sweeper.sweepOnce(context.Background()); got != 0 {
		t.Fatalf("a failed enumeration sweeps nothing, got %d", got)
	}
	sweeper.running.Store(true)
	if got := sweeper.sweepOnce(context.Background()); got != 0 {
		t.Fatalf("an overlapping pass must be a no-op, got %d", got)
	}
}

// TestServer_CollectionSweepTargets: the shared store's databases are found
// from its collection work journal, one target per (tenant, database)
// carrying its entries, and not at all on a node that does not lead the
// store. A malformed entry, or one whose key its fields do not derive, is not
// acted on.
func TestServer_CollectionSweepTargets(t *testing.T) {
	ctx := context.Background()
	kv := newMockKVService()
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", &DatabaseServices{KV: kv})
	journal := &collectionWorkJournal{kv: kv, tenant: tenant.SystemTenantID}
	for _, w := range [][2]string{{"sqldb", "a"}, {"sqldb", "b/with/slashes"}, {"appdb", "c"}} {
		if err := journal.Record(ctx, w[0], w[1]); err != nil {
			t.Fatal(err)
		}
	}
	kv.store[collectionWorkPrefix+"junk"] = []byte("{not json")
	kv.store[collectionWorkPrefix+"system/x/elsewhere"] = []byte(`{"tenant":"system","database":"x","name":"y"}`)

	leads := true
	srv := New(testConfig(t, false, false), kv, &mockAdminService{}, WithDatabaseServiceRegistry(dbReg),
		WithCollectionSweepLeader(func() bool { return leads }))
	targets, err := srv.collectionSweepTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, tgt := range targets {
		if !tgt.journaled() {
			t.Fatalf("unexpected unjournaled target %+v", tgt)
		}
		got[tgt.key()] = len(tgt.work)
	}
	want := map[string]int{tenant.SystemTenantID + "/sqldb": 2, tenant.SystemTenantID + "/appdb": 1}
	if len(got) != len(want) || got[tenant.SystemTenantID+"/sqldb"] != 2 || got[tenant.SystemTenantID+"/appdb"] != 1 {
		t.Fatalf("targets = %v, want %v", got, want)
	}

	leads = false
	if targets, err := srv.collectionSweepTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("a node that does not lead the store sweeps none of it: %v, %v", targets, err)
	}
}

// TestServer_CollectionSweeperLifecycle starts and stops the sweeper the way
// Start and Stop do, and checks the option overrides the defaults.
func TestServer_CollectionSweeperLifecycle(t *testing.T) {
	cfg := testConfig(t, false, false)
	dbReg := NewDatabaseServiceRegistry("system")
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(dbReg),
		WithCollectionSweep(time.Hour, 2*time.Hour))
	if srv.opts.collectionSweepInterval != time.Hour || srv.opts.collectionStaleAfter != 2*time.Hour {
		t.Fatalf("options not applied: %v %v", srv.opts.collectionSweepInterval, srv.opts.collectionStaleAfter)
	}

	srv.startCollectionSweeper()
	if srv.sweeperCancel == nil {
		t.Fatal("the sweeper did not start")
	}
	srv.Stop()
	if srv.sweeperCancel != nil {
		t.Fatal("Stop must stop the sweeper")
	}

	// A registry-less server has nothing to sweep.
	bare := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{})
	bare.startCollectionSweeper()
	if bare.sweeperCancel != nil {
		t.Fatal("a registry-less server must not start a sweeper")
	}
}
