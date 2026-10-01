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
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ownershipIndexRec is an IndexServicer whose owner-checked drop and create
// refuse with an ownership error naming the NAMESPACED index, as the inner
// service would.
type ownershipIndexRec struct {
	service.IndexServicer
	lastName string
}

func (o *ownershipIndexRec) CreateIndex(_ context.Context, name, _ string, cfg index.IndexConfig) error {
	o.lastName = name
	return &index.OwnershipError{Name: name, Owner: "", Requested: cfg.Owner}
}

func (o *ownershipIndexRec) DropOwnedIndex(_ context.Context, name, owner string) error {
	o.lastName = name
	return &index.OwnershipError{Name: name, Owner: "", Requested: owner}
}

// TestNamespacedIndex_OwnershipIsReportedUnderTheCallersName pins that the
// namespace wrapper forwards the owner-checked drop under the prefixed name
// and reports an ownership refusal under the name the caller used: the
// namespace is the wrapper's detail.
func TestNamespacedIndex_OwnershipIsReportedUnderTheCallersName(t *testing.T) {
	inner := &ownershipIndexRec{}
	ns := newNamespacedIndexService(inner, "acme/app/")

	var own *index.OwnershipError
	err := ns.DropOwnedIndex(context.Background(), "app.users.email", "collection:app/users#1")
	if !errors.As(err, &own) || own.Name != "app.users.email" || own.Requested != "collection:app/users#1" {
		t.Fatalf("drop refusal = %v", err)
	}
	if inner.lastName != "acme/app/app.users.email" {
		t.Fatalf("inner saw %q", inner.lastName)
	}
	err = ns.CreateIndex(context.Background(), "app.users.email", "email", index.IndexConfig{Owner: "collection:app/users#1"})
	if !errors.As(err, &own) || own.Name != "app.users.email" {
		t.Fatalf("create refusal = %v", err)
	}
	// Any other error passes through untouched.
	if got := ns.unscopeOwnership(errBoom, "x"); !errors.Is(got, errBoom) {
		t.Fatalf("passthrough = %v", got)
	}
}

// TestQuotaCollection_Passthroughs pins the verbs the wrapper does not meter.
func TestQuotaCollection_Passthroughs(t *testing.T) {
	ctx := context.Background()
	out, acct := quotaBundle(t, 1<<20)
	coll := out.Collection
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	ins, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"v": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	before := acct.Usage("t1")
	if _, err := coll.GetDocument(ctx, &service.GetDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID}); err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if _, err := coll.ListDocuments(ctx, &service.ListDocumentsRequest{Database: "db", Collection: "docs"}); err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if _, err := coll.GetCollectionInfo(ctx, "db", "docs"); err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if _, err := coll.ListCollections(ctx, &service.ListCollectionsRequest{Database: "db"}); err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if acct.Usage("t1") != before {
		t.Fatal("a read must not move the tenant's usage")
	}
}

// TestQuotaCollection_RefusalsAndFailures covers admission refused on update
// and upsert, and verbs that fail without freeing anything.
func TestQuotaCollection_RefusalsAndFailures(t *testing.T) {
	ctx := context.Background()
	out, acct := quotaBundle(t, 40)
	coll := out.Collection
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	ins, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"v": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	big := map[string]interface{}{"v": "0123456789012345678901234567890123456789"}
	var exceeded *quota.ExceededError
	if _, err := coll.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID, Document: big}); !errors.As(err, &exceeded) {
		t.Fatalf("update over the limit: %v", err)
	}
	if _, err := coll.UpsertDocument(ctx, &service.UpsertDocumentRequest{Database: "db", Collection: "docs", ID: ins.ID, Document: big}); !errors.As(err, &exceeded) {
		t.Fatalf("upsert over the limit: %v", err)
	}

	before := acct.Usage("t1")
	// Failures decided before anything is written change nothing.
	if _, err := coll.UpdateDocument(ctx, &service.UpdateDocumentRequest{Database: "db", Collection: "docs", ID: 999, Document: map[string]interface{}{"v": "b"}}); err == nil {
		t.Fatal("updating a missing document must fail")
	}
	if _, err := coll.DeleteDocument(ctx, &service.DeleteDocumentRequest{Database: "db", Collection: "docs", ID: 999}); err == nil {
		t.Fatal("deleting a missing document must fail")
	}
	if err := coll.DropCollection(ctx, &service.DropCollectionRequest{Database: "db", Name: "absent"}); err == nil {
		t.Fatal("dropping a missing collection must fail")
	}
	if acct.Usage("t1") != before {
		t.Fatalf("usage moved from %d to %d on failed verbs", before, acct.Usage("t1"))
	}
}

// TestQuotaCollection_DropReleasesALargeCollection drops a collection larger
// than several teardown batches: the release is the teardown's own count of
// every document it removed, across every batch.
func TestQuotaCollection_DropReleasesALargeCollection(t *testing.T) {
	ctx := context.Background()
	out, acct := quotaBundle(t, 1<<30)
	coll := out.Collection
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Database: "db", Name: "docs", IDStrategy: service.IDStrategyIncrement}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Database: "db", Collection: "docs", Document: map[string]interface{}{"v": "x"}}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if acct.Usage("t1") == 0 {
		t.Fatal("inserts were not charged")
	}
	if err := coll.DropCollection(ctx, &service.DropCollectionRequest{Database: "db", Name: "docs"}); err != nil {
		t.Fatal(err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("after drop usage = %d, want 0", got)
	}
}

// TestCollectionSweeper_RunStopsOnCancel drives the loop through exactly one
// pass and out. The pass trigger is an injected channel holding one tick: a
// 1ms wall-clock ticker could fire again before run observed the cancellation
// the pass made, and select then picked a second pass at random (about 1 run
// in 340 under -race).
func TestCollectionSweeper_RunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	passes := 0
	sweeper := &collectionSweeper{
		ticks:  ticks,
		logger: slog.Default(),
		targets: func(context.Context) ([]collectionSweepTarget, error) {
			passes++
			cancel()
			return nil, nil
		},
	}
	sweeper.run(ctx)
	if passes != 1 {
		t.Fatalf("passes = %d, want 1", passes)
	}
}

// TestCollectionSweeper_RunUsesIntervalTicker: with no injected trigger, run
// paces passes on its interval ticker and still returns on cancellation. It
// asserts at least one pass, which is all the real ticker can promise.
func TestCollectionSweeper_RunUsesIntervalTicker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	passes := 0
	sweeper := &collectionSweeper{
		interval: time.Millisecond,
		logger:   slog.Default(),
		targets: func(context.Context) ([]collectionSweepTarget, error) {
			passes++
			cancel()
			return nil, nil
		},
	}
	sweeper.run(ctx)
	if passes < 1 {
		t.Fatalf("passes = %d, want at least 1", passes)
	}
}

// TestServer_CollectionSweepTargets_TenantsAndDatabases: a catalogued
// database served from the shared replicated store is not enumerated from the
// node-local catalog -- its work is found through the store's journal -- and a
// tenant enumeration that fails fails the pass.
func TestServer_CollectionSweepTargets_TenantsAndDatabases(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	for _, cfg := range []database.DatabaseConfig{
		{Name: "reports", Engine: database.EngineKV, Backend: database.BackendMemory},
		{Name: "ledger", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory},
	} {
		if _, err := mgr.Create(cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	tenants := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{{ID: tenant.SystemTenantID}, {ID: "acme"}}, nil
	}}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(NewDatabaseServiceRegistry("system")),
		WithTenantService(tenants), WithDatabaseManager(mgr))

	targets, err := srv.collectionSweepTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(targets))
	for i, tgt := range targets {
		got[i] = tgt.Tenant + "/" + tgt.Database
	}
	sort.Strings(got)
	var want []string
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		// #nosec G602 -- the lengths are compared equal above, and i ranges over want.
		if got[i] != want[i] {
			t.Fatalf("targets = %v, want %v", got, want)
		}
	}

	failing := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithDatabaseServiceRegistry(NewDatabaseServiceRegistry("system")),
		WithTenantService(&listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) { return nil, errBoom }}))
	if _, err := failing.collectionSweepTargets(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("a failed tenant listing must be reported, got %v", err)
	}
}
