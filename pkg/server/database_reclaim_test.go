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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The collection surface is the one shape of owned data whose physical
// layout moved under this branch: go-qrdb#441 put collection keys behind the
// per-(tenant, database) resolver, so they are now written under the
// composite namespace instead of at the store root.
//
// That makes them worth re-checking rather than assuming, for two reasons.
// A teardown that still looked only at the old root-level prefixes would now
// reclaim nothing; and a teardown that reads through the user-facing List
// would be at the mercy of the internal-key guard, which is what made the
// time-series shape a silent no-op (go-qrdb#412). This drives the CURRENT
// path -- a collection created through restServiceRegistry.CollectionService
// -- and asserts the drop reaches it.

// TestReclaim_CollectionsWrittenThroughTheResolverAreReclaimed creates a
// collection and a document the way a request does, then drops the database.
func TestReclaim_CollectionsWrittenThroughTheResolverAreReclaimed(t *testing.T) {
	ctx := context.Background()
	root := newMockKVService()

	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.SetKeyspaceReclaimer(service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: root}))

	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", &DatabaseServices{Name: "system", KV: root})
	reg := NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID,
		WithDatabaseCatalog(newManagerCatalog(mgr, tenant.SystemTenantID)))

	for _, name := range []string{"reports", "ledger"} {
		if _, err := mgr.Create(database.DatabaseConfig{
			Name: name, Engine: database.EngineKV, Backend: database.BackendMemory,
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		coll, err := reg.CollectionService(tenant.SystemTenantID, name)
		if err != nil {
			t.Fatalf("CollectionService %s: %v", name, err)
		}
		if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{
			Database: name, Name: "users",
		}); err != nil {
			t.Fatalf("CreateCollection %s: %v", name, err)
		}
		if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{
			Database: name, Collection: "users",
			Document: map[string]interface{}{"who": "alice"},
		}); err != nil {
			t.Fatalf("InsertDocument %s: %v", name, err)
		}
	}

	// ListAll, never List: an assertion about residue has to see the internal
	// keyspace, or it silently stops checking the thing it exists to check.
	before, err := root.ListAll(ctx, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("fixture wrote nothing; the rest of this test would pass vacuously")
	}

	if err := mgr.Drop(ctx, "reports"); err != nil {
		t.Fatalf("drop: %v", err)
	}

	after, err := root.ListAll(ctx, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	reportsNS := tenant.ComposeNamespace(tenant.SystemTenantID, "reports")
	ledgerNS := tenant.ComposeNamespace(tenant.SystemTenantID, "ledger")
	var survived, ledgerKeys int
	for _, k := range after {
		switch {
		case len(k) >= len(reportsNS) && k[:len(reportsNS)] == reportsNS:
			survived++
			t.Errorf("a key of the dropped database survived: %q", k)
		case len(k) >= len(ledgerNS) && k[:len(ledgerNS)] == ledgerNS:
			ledgerKeys++
		}
	}
	if survived != 0 {
		t.Errorf("%d keys survived the drop", survived)
	}
	if ledgerKeys == 0 {
		t.Error("the other database's collection was reclaimed too; the drop is not scoped")
	}

	// And the recreated name inherits nothing.
	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "reports", Engine: database.EngineKV, Backend: database.BackendMemory,
	}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	coll, err := reg.CollectionService(tenant.SystemTenantID, "reports")
	if err != nil {
		t.Fatalf("CollectionService: %v", err)
	}
	list, err := coll.ListCollections(ctx, &service.ListCollectionsRequest{Database: "reports"})
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(list.Collections) != 0 {
		t.Errorf("a database recreated under a dropped name inherited %d collections", len(list.Collections))
	}
}

// TestReclaim_CollectionKeysAreInsideTheCompositeNamespace pins WHERE the
// collection surface writes, because that is what decides which prefix
// reclaims it. If a future change moves these keys back to the store root,
// the root-level prefixes in service.CollectionKeyPrefixes become load
// bearing again and this test is where that shows up.
func TestReclaim_CollectionKeysAreInsideTheCompositeNamespace(t *testing.T) {
	ctx := context.Background()
	root := newMockKVService()

	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	dbReg := NewDatabaseServiceRegistry("system")
	dbReg.Register("system", &DatabaseServices{Name: "system", KV: root})
	reg := NewRESTServiceRegistry(dbReg, nil, tenant.SystemTenantID,
		WithDatabaseCatalog(newManagerCatalog(mgr, tenant.SystemTenantID)))

	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "reports", Engine: database.EngineKV, Backend: database.BackendMemory,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	coll, err := reg.CollectionService(tenant.SystemTenantID, "reports")
	if err != nil {
		t.Fatalf("CollectionService: %v", err)
	}
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "reports", Name: "users",
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	ns := tenant.ComposeNamespace(tenant.SystemTenantID, "reports")
	inside, err := root.ListAll(ctx, ns)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(inside) == 0 {
		t.Errorf("no collection key under %q — the collection surface is not namespaced, and the root-level prefixes are what reclaims it", ns)
	}
	for _, prefix := range service.CollectionKeyPrefixes("reports") {
		atRoot, lerr := root.ListAll(ctx, prefix)
		if lerr != nil {
			t.Fatalf("ListAll: %v", lerr)
		}
		if len(atRoot) != 0 {
			t.Errorf("collection keys still at the store root under %q: %v", prefix, atRoot)
		}
	}
}

// TestNamespacedKV_ListAll_DropsTheFilterNotTheConfinement is the wrapper's
// half of the seam.
//
// A namespaced view exists to confine a caller to one (tenant, database).
// ListAll drops the internal-key discovery guard, and the thing that must NOT
// come off with it is the namespace: an unfiltered enumeration that also
// escaped its prefix would be a cross-tenant read primitive, which is a worse
// defect than the leak this seam was added to fix.
func TestNamespacedKV_ListAll_DropsTheFilterNotTheConfinement(t *testing.T) {
	ctx := context.Background()
	root := newMockKVService()

	mine := "acme/reports/"
	theirs := "other/reports/"
	for _, k := range []string{
		mine + "greeting",
		mine + "__sys:tables/reports/orders",
		theirs + "greeting",
		"__sys:roles/admin",
	} {
		if err := root.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}

	view := &namespacedKVService{inner: root, prefix: mine}

	all, err := view.ListAll(ctx, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	got := map[string]bool{}
	for _, k := range all {
		got[k] = true
	}
	if !got["greeting"] || !got["__sys:tables/reports/orders"] {
		t.Errorf("ListAll = %v, want both this database's keys with the namespace stripped", all)
	}
	if len(all) != 2 {
		t.Errorf("ListAll = %v, want exactly this database's keys — the namespace must still confine it", all)
	}
}

// TestNamespacedKV_ListAll_StillFiltersThroughList is the control: the
// user-facing verb on the same view keeps the guard, so adding ListAll did not
// quietly widen List.
func TestNamespacedKV_ListAll_StillFiltersThroughList(t *testing.T) {
	ctx := context.Background()
	root := newMockKVService()
	mine := "acme/reports/"
	if err := root.Put(ctx, mine+"__sys:tables/reports/orders", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := root.Put(ctx, mine+"greeting", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	view := &namespacedKVService{inner: root, prefix: mine}

	// The namespace is stripped before the guard runs, so "__sys:tables/..."
	// reaches it as an internal key and is removed — which is exactly the
	// behaviour a request handler must keep.
	filtered, err := view.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(filtered) != 1 || filtered[0] != "greeting" {
		t.Errorf("List = %v, want only the user key", filtered)
	}
}

// TestQuotaKV_ListAll_PassesThrough pins the other wrapper: a storage-byte
// quota bounds writes and has nothing to say about which keys a read sees, so
// a reclamation must not be silently narrowed by passing through one.
func TestQuotaKV_ListAll_PassesThrough(t *testing.T) {
	ctx := context.Background()
	root := newMockKVService()
	if err := root.Put(ctx, "ts:acme/reports/cpu:1", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	view := &quotaKVService{inner: root}

	all, err := view.ListAll(ctx, "ts:acme/reports/")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll = %v, want the one key", all)
	}
}
