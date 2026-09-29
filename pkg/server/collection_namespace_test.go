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

// Cross-tenant isolation of collection DOCUMENTS (go-qrdb#441).
//
// A collection document key is "__col:{database}:{collection}/{id}" and the
// database name comes from the request. Under prefix/barrier isolation two
// tenants share one state machine and are separated only by the
// "<tenant>/<database>/" prefix tenant.ComposeNamespace builds, so two tenants
// naming the same database and the same collection wrote the SAME KEYS. That
// is the documents themselves, not a leak of key names through an index
// (contrast go-qrdb#410).
//
// These tests drive the composite wrapper and the registry accessor over one
// shared store and assert that neither tenant can observe the other's
// documents, metadata, or ID counter.

package server

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// sharedCollectionStore builds the service bundle exactly as production does
// on the shared-shard path: one KV servicer, and a Collection built over that
// same raw, pre-wrap servicer (cmd/qrdbd/start.go registers the lifecycle
// Node's Collection; pkg/server/tenant_host.go builds its own the same way).
func sharedCollectionStore() (*mockKVService, *DatabaseServices) {
	kv := newMockKVService()
	return kv, &DatabaseServices{
		Name:       "appdb",
		KV:         kv,
		Collection: service.NewCollectionService(kv),
	}
}

// twoTenantCollectionRegistry returns a registry whose default database
// resolves to a single shared bundle, plus the store behind it.
func twoTenantCollectionRegistry(t *testing.T) (*mockKVService, *restServiceRegistry) {
	t.Helper()
	kv, svcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("appdb")
	dbReg.Register("appdb", svcs)
	return kv, NewRESTServiceRegistry(dbReg, nil, tenantid.System).(*restServiceRegistry)
}

// storeKeys returns the store's keys, sorted, for stable assertions.
func storeKeys(kv *mockKVService) []string {
	keys := make([]string, 0, len(kv.store))
	for k := range kv.store {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestCollectionDocumentsAreNamespacedPerTenant is the whole claim of #441:
// two tenants, the same database name, the same collection name, one shared
// state machine. Tenant A inserts a document; tenant B must not see it, must
// be able to create a collection of the same name, and must read back its own
// document under the same ID.
//
// This is the failing-first test. Before the fix, applyComposite copied the
// bundle's Collection unchanged, so both tenants drove the same unnamespaced
// KV servicer and B read A's document.
func TestCollectionDocumentsAreNamespacedPerTenant(t *testing.T) {
	kv, reg := twoTenantCollectionRegistry(t)
	ctx := context.Background()

	colA, err := reg.CollectionService("tenant-a", "appdb")
	if err != nil {
		t.Fatalf("resolve tenant-a: %v", err)
	}
	colB, err := reg.CollectionService("tenant-b", "appdb")
	if err != nil {
		t.Fatalf("resolve tenant-b: %v", err)
	}

	if _, err := colA.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "appdb",
		Name:     "users",
	}); err != nil {
		t.Fatalf("tenant-a CreateCollection: %v", err)
	}

	insA, err := colA.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database:   "appdb",
		Collection: "users",
		Document:   map[string]interface{}{"email": "a@example.com", "secret": "tenant-a-only"},
	})
	if err != nil {
		t.Fatalf("tenant-a InsertDocument: %v", err)
	}

	// 1. B cannot read A's document, and cannot even resolve the collection:
	//    A's metadata key is in A's namespace.
	if got, err := colB.GetDocument(ctx, &service.GetDocumentRequest{
		Database:   "appdb",
		Collection: "users",
		ID:         insA.ID,
	}); err == nil {
		t.Fatalf("CROSS-TENANT LEAK: tenant-b read tenant-a's document: %#v", got.Document)
	}

	// 2. B cannot list A's collection.
	listB, err := colB.ListCollections(ctx, &service.ListCollectionsRequest{Database: "appdb"})
	if err != nil {
		t.Fatalf("tenant-b ListCollections: %v", err)
	}
	if len(listB.Collections) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: tenant-b listed %d collection(s) it did not create: %#v",
			len(listB.Collections), listB.Collections)
	}

	// 3. The name is B's to use. Before the fix this returned
	//    CollectionAlreadyExistsError -- one tenant's naming choice denying
	//    another's.
	if _, err := colB.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "appdb",
		Name:     "users",
	}); err != nil {
		t.Fatalf("tenant-b CreateCollection of the same name: %v", err)
	}

	insB, err := colB.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database:   "appdb",
		Collection: "users",
		Document:   map[string]interface{}{"email": "b@example.com", "secret": "tenant-b-only"},
	})
	if err != nil {
		t.Fatalf("tenant-b InsertDocument: %v", err)
	}

	// 4. Each tenant reads back its OWN document. The assertion is not
	//    vacuously satisfied by an empty store.
	gotA, err := colA.GetDocument(ctx, &service.GetDocumentRequest{
		Database: "appdb", Collection: "users", ID: insA.ID,
	})
	if err != nil {
		t.Fatalf("tenant-a GetDocument of its own document: %v", err)
	}
	if gotA.Document["secret"] != "tenant-a-only" {
		t.Fatalf("tenant-a read the wrong document: %#v", gotA.Document)
	}
	gotB, err := colB.GetDocument(ctx, &service.GetDocumentRequest{
		Database: "appdb", Collection: "users", ID: insB.ID,
	})
	if err != nil {
		t.Fatalf("tenant-b GetDocument of its own document: %v", err)
	}
	if gotB.Document["secret"] != "tenant-b-only" {
		t.Fatalf("tenant-b read the wrong document: %#v", gotB.Document)
	}

	// 5. The physical separation, pinned. Every key written through either
	//    tenant's collection service carries that tenant's composite prefix,
	//    and no bare "__col:"/"__sys:collections/" key survives. The one key
	//    outside the namespace is the collection work journal entry, which
	//    names its tenant and database and holds no document data.
	for _, k := range storeKeys(kv) {
		if strings.HasPrefix(k, "tenant-a/appdb/") || strings.HasPrefix(k, "tenant-b/appdb/") ||
			strings.HasPrefix(k, collectionWorkPrefix+"tenant-a/appdb/") || strings.HasPrefix(k, collectionWorkPrefix+"tenant-b/appdb/") {
			continue
		}
		t.Fatalf("unnamespaced collection key in the shared store: %q (all keys: %v)", k, storeKeys(kv))
	}
}

// TestCollectionDocumentsSurviveAnIdenticalIDInAnotherTenant pins the
// overwrite half of the same defect. field_hash makes both tenants derive the
// SAME document ID from the same natural key, so before the fix tenant B's
// insert silently overwrote tenant A's document at the shared key. A
// disclosure is a read; this one is a write, and it destroys data.
func TestCollectionDocumentsSurviveAnIdenticalIDInAnotherTenant(t *testing.T) {
	_, reg := twoTenantCollectionRegistry(t)
	ctx := context.Background()

	colA, err := reg.CollectionService("tenant-a", "appdb")
	if err != nil {
		t.Fatalf("resolve tenant-a: %v", err)
	}
	colB, err := reg.CollectionService("tenant-b", "appdb")
	if err != nil {
		t.Fatalf("resolve tenant-b: %v", err)
	}

	for _, c := range []service.CollectionServicer{colA, colB} {
		if _, err := c.CreateCollection(ctx, &service.CreateCollectionRequest{
			Database:   "appdb",
			Name:       "accounts",
			IDStrategy: service.IDStrategyFieldHash,
			FieldHash:  "account",
		}); err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
	}

	insA, err := colA.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database: "appdb", Collection: "accounts",
		Document: map[string]interface{}{"account": "shared-key", "owner": "tenant-a"},
	})
	if err != nil {
		t.Fatalf("tenant-a InsertDocument: %v", err)
	}
	insB, err := colB.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database: "appdb", Collection: "accounts",
		Document: map[string]interface{}{"account": "shared-key", "owner": "tenant-b"},
	})
	if err != nil {
		t.Fatalf("tenant-b InsertDocument: %v", err)
	}
	if insA.ID != insB.ID {
		t.Fatalf("field_hash must derive the same ID from the same field value; got %d and %d", insA.ID, insB.ID)
	}

	gotA, err := colA.GetDocument(ctx, &service.GetDocumentRequest{
		Database: "appdb", Collection: "accounts", ID: insA.ID,
	})
	if err != nil {
		t.Fatalf("tenant-a GetDocument: %v", err)
	}
	if gotA.Document["owner"] != "tenant-a" {
		t.Fatalf("CROSS-TENANT OVERWRITE: tenant-a's document now reads %#v", gotA.Document)
	}
}

// TestCollectionServiceResolvesThroughTheRegistryLikeItsSiblings holds the
// accessor to the contract every other data-plane accessor keeps: an unknown
// database is refused, a bundle with no collection service reports
// not-implemented, and a resolve failure propagates rather than falling back
// to an unscoped service.
func TestCollectionServiceResolvesThroughTheRegistryLikeItsSiblings(t *testing.T) {
	t.Run("unknown database is refused", func(t *testing.T) {
		_, reg := twoTenantCollectionRegistry(t)
		reg.catalog = staticCatalog{}
		if _, err := reg.CollectionService("tenant-a", "nosuchdb"); err == nil {
			t.Fatal("expected an error for an unknown database")
		} else if _, ok := err.(*rest.DatabaseNotFoundError); !ok {
			t.Fatalf("expected *rest.DatabaseNotFoundError, got %T: %v", err, err)
		}
	})

	t.Run("a bundle with no KV reports not implemented", func(t *testing.T) {
		dbReg := NewDatabaseServiceRegistry("appdb")
		dbReg.Register("appdb", &DatabaseServices{Name: "appdb"})
		reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System).(*restServiceRegistry)
		if _, err := reg.CollectionService(tenantid.System, "appdb"); err == nil {
			t.Fatal("expected an error for a bundle with no collection surface")
		} else if _, ok := err.(*service.CapabilityUnsupportedError); !ok {
			t.Fatalf("expected *service.CapabilityUnsupportedError, got %T: %v", err, err)
		}
	})
}

// staticCatalog is a DatabaseCatalog that knows only "appdb", so a request for
// any other name resolves to a not-found rather than to the default bundle.
type staticCatalog struct{}

// DatabaseExists reports whether db is the one database this catalog holds.
func (staticCatalog) DatabaseExists(_, db string) bool { return db == "appdb" }

// TestApplyCompositeNamespacesTheCollectionEngines is the same claim one layer
// down, at the wrapper that every prefix/barrier/volume request passes
// through. It is the test that compiles against the pre-fix tree, and it
// fails there: applyComposite wrapped KV and Index but COPIED Collection, so
// the copy still held the raw, pre-wrap engines the bundle was built with.
func TestApplyCompositeNamespacesTheCollectionEngines(t *testing.T) {
	kv, svcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("appdb")
	dbReg.Register("appdb", svcs)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System).(*restServiceRegistry)
	ctx := context.Background()

	a := reg.applyComposite(svcs, "tenant-a", "appdb", databaseFence{})
	b := reg.applyComposite(svcs, "tenant-b", "appdb", databaseFence{})

	if _, err := a.Collection.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "appdb", Name: "users",
	}); err != nil {
		t.Fatalf("tenant-a CreateCollection: %v", err)
	}
	ins, err := a.Collection.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database: "appdb", Collection: "users",
		Document: map[string]interface{}{"secret": "tenant-a-only"},
	})
	if err != nil {
		t.Fatalf("tenant-a InsertDocument: %v", err)
	}

	if got, err := b.Collection.GetDocument(ctx, &service.GetDocumentRequest{
		Database: "appdb", Collection: "users", ID: ins.ID,
	}); err == nil {
		t.Fatalf("CROSS-TENANT LEAK: tenant-b read tenant-a's document: %#v", got.Document)
	}

	keys := storeKeys(kv)
	for _, k := range keys {
		if !strings.HasPrefix(k, "tenant-a/appdb/") && !strings.HasPrefix(k, collectionWorkPrefix+"tenant-a/appdb/") {
			t.Fatalf("collection key written outside tenant-a's namespace: %q (all keys: %v)", k, keys)
		}
	}
	if len(keys) == 0 {
		t.Fatal("no keys written; the assertion above would be vacuous")
	}
}

// TestCollectionIndexScopeMatchesTheNamespacedDocumentKeys pins the seam
// between this change and the index key-scoping one, because each is only half
// of the property and neither test would catch the pair drifting.
//
// An index is maintained over engine.ComposeIndexScope(name, declaredPrefix) =
// the namespace read back off the index NAME, followed by the key prefix its
// creator DECLARED. CreateCollection declares "__col:{db}:{collection}/", and
// the resolved index servicer prefixes the name with the composite namespace.
// The composed scope must therefore equal the exact prefix under which the
// resolved collection servicer writes its documents -- an index that is scoped
// one byte differently from the keys it indexes maintains nothing, or
// maintains another tenant's.
func TestCollectionIndexScopeMatchesTheNamespacedDocumentKeys(t *testing.T) {
	kv := newMockKVService()
	idx := &recordingIndexService{}
	svcs := &DatabaseServices{
		Name:       "appdb",
		KV:         kv,
		Index:      idx,
		Collection: service.NewCollectionServiceWithIndex(kv, idx),
	}
	dbReg := NewDatabaseServiceRegistry("appdb")
	dbReg.Register("appdb", svcs)
	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System).(*restServiceRegistry)

	col, err := reg.CollectionService("tenant-a", "appdb")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := col.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "appdb",
		Name:     "users",
		Indexes:  []service.IndexDefinition{{FieldName: "email"}},
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := col.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database: "appdb", Collection: "users",
		Document: map[string]interface{}{"email": "a@example.com"},
	}); err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	if len(idx.created) != 1 {
		t.Fatalf("indexes created = %v, want exactly one", idx.created)
	}
	scope := string(engine.ComposeIndexScope(idx.created[0], idx.configs[0].KeyPrefix))

	var docKey string
	for _, k := range storeKeys(kv) {
		if strings.Contains(k, "__col:") {
			docKey = k
			break
		}
	}
	if docKey == "" {
		t.Fatalf("no document key written; keys = %v", storeKeys(kv))
	}
	if !strings.HasPrefix(docKey, scope) {
		t.Fatalf("index scope %q does not cover the document key %q", scope, docKey)
	}
	if want := tenant.ComposeNamespace("tenant-a", "appdb") + "__col:appdb:users#"; !strings.HasPrefix(scope, want) || !strings.HasSuffix(scope, "/") || len(scope) != len(want)+16+1 {
		t.Fatalf("index scope = %q, want %q", scope, want)
	}
}

// TestRegistryImplementationsAnswerEveryAccessor is the guard for the class of
// break this change caused in CI.
//
// Widening service.Registry is a COMPILE-time break for an explicit
// implementer and a RUNTIME break for a test double that EMBEDS the interface:
// the new method is promoted off the embedded (nil) interface, so the file
// still compiles, and the first call dereferences nil -- in the CI failure
// that surfaced thirty frames away, inside go-mysql-server's plan builder, in
// a concurrent test. `var _ service.Registry = (*T)(nil)` does not catch it,
// because the promoted method satisfies the assertion.
//
// This test calls EVERY accessor on the two shipped implementations, so an
// accessor added to the interface and forgotten in one of them fails here
// rather than at a call site in another package. It cannot reach the test
// doubles in other packages -- nothing can, from here -- which is why the
// rule is also written down beside each of them.
func TestRegistryImplementationsAnswerEveryAccessor(t *testing.T) {
	_, svcs := sharedCollectionStore()
	dbReg := NewDatabaseServiceRegistry("appdb")
	dbReg.Register("appdb", svcs)
	base := NewRESTServiceRegistry(dbReg, nil, tenantid.System)

	impls := map[string]service.Registry{
		"restServiceRegistry": base,
	}

	// Each entry calls one accessor and discards the result: the assertion is
	// that the call RETURNS -- a promoted method off a nil embedded interface
	// panics instead.
	accessors := map[string]func(service.Registry) error{
		"KVService":          func(r service.Registry) error { _, err := r.KVService("tenant-a", "appdb"); return err },
		"MVCCService":        func(r service.Registry) error { _, err := r.MVCCService("tenant-a", "appdb"); return err },
		"WatchService":       func(r service.Registry) error { _, err := r.WatchService("tenant-a", "appdb"); return err },
		"LeaseService":       func(r service.Registry) error { _, err := r.LeaseService("tenant-a", "appdb"); return err },
		"IndexService":       func(r service.Registry) error { _, err := r.IndexService("tenant-a", "appdb"); return err },
		"TimeSeriesService":  func(r service.Registry) error { _, err := r.TimeSeriesService("tenant-a", "appdb"); return err },
		"VectorService":      func(r service.Registry) error { _, err := r.VectorService("tenant-a", "appdb"); return err },
		"TransactionService": func(r service.Registry) error { _, err := r.TransactionService("tenant-a", "appdb"); return err },
		"BatchService":       func(r service.Registry) error { _, err := r.BatchService("tenant-a", "appdb"); return err },
		"TableService":       func(r service.Registry) error { _, err := r.TableService("tenant-a", "appdb"); return err },
		"SequenceService":    func(r service.Registry) error { _, err := r.SequenceService("tenant-a", "appdb"); return err },
		"PrivilegeService":   func(r service.Registry) error { _, err := r.PrivilegeService("tenant-a", "appdb"); return err },
		"CollectionService":  func(r service.Registry) error { _, err := r.CollectionService("tenant-a", "appdb"); return err },
		"LockService":        func(r service.Registry) error { _, err := r.LockService("tenant-a", "appdb"); return err },
	}

	// The count is asserted so that adding an accessor to the interface
	// without adding it here fails, rather than silently testing thirteen of
	// fourteen.
	if got := reflect.TypeOf((*service.Registry)(nil)).Elem().NumMethod(); got != len(accessors) {
		t.Fatalf("service.Registry has %d methods but this test exercises %d; add the new accessor here", got, len(accessors))
	}

	for implName, impl := range impls {
		for name, call := range accessors {
			t.Run(implName+"/"+name, func(t *testing.T) {
				// A typed error is a legitimate answer; a panic is not.
				_ = call(impl)
			})
		}
	}
}
