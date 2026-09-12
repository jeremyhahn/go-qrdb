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

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// This is the end-to-end statement of go-qrdb#412: a database dropped and
// recreated under the same name must inherit nothing.
//
// The stack here is the production one minus the transport: a real
// database.Manager over a temp directory, a real KeyspaceReclaimService over
// a shared store, and the real DatabaseService in front of both -- wired the
// way pkg/server/lifecycle.BuildServices wires them.
//
// The collection service is built over a NAMESPACED view of that store,
// because that is how the server builds it: go-qrdb#441 moved the collection
// surface behind the per-(tenant, database) resolver, so its keys land under
// "<tenant>/<db>/" like every other engine's. This double used to hand it the
// bare store, which made it a model of the PRE-#441 server -- and a reclaim
// test written against that model measured the wrong layout. It is the double
// that was wrong, not the layout.

// namespacedKV is the composite-namespace view every engine is resolved
// through in production: it prepends "<tenant>/<db>/" to every key and strips
// it off every enumeration.
//
// It is the whole of what pkg/server's registry-resolved wrapper does that
// matters to a teardown, which is why the teardown can be measured here rather
// than only one package up.
type namespacedKV struct {
	inner *memoryKVSvc
	ns    string
}

func newNamespacedKV(inner *memoryKVSvc, tenantID, db string) *namespacedKV {
	return &namespacedKV{inner: inner, ns: tenant.ComposeNamespace(tenantID, db)}
}

// Compile-time check: it really is what CollectionService takes.
var _ kvService = (*namespacedKV)(nil)

func (n *namespacedKV) Put(ctx context.Context, key string, value []byte) error {
	return n.inner.Put(ctx, n.ns+key, value)
}

// PutIfAbsent namespaces the key and delegates, so the conditional is decided
// against the SAME key the collection service will later read. Stubbing this
// to a constant would make the double narrower than production in exactly the
// way go-qrdb#489 is about: the guarded insert #466 added exists to report
// which of two concurrent callers won, and a double that always reports "won"
// cannot fail the test that checks it.
func (n *namespacedKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return n.inner.PutIfAbsent(ctx, n.ns+key, value)
}

func (n *namespacedKV) Get(ctx context.Context, key string) ([]byte, error) {
	return n.inner.Get(ctx, n.ns+key)
}

func (n *namespacedKV) Delete(ctx context.Context, key string) error {
	return n.inner.Delete(ctx, n.ns+key)
}

func (n *namespacedKV) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := n.inner.List(ctx, n.ns+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, strings.TrimPrefix(k, n.ns))
	}
	return out, nil
}

func (n *namespacedKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	pairs, err := n.inner.Scan(ctx, n.ns+prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(pairs))
	for k, v := range pairs {
		out[strings.TrimPrefix(k, n.ns)] = v
	}
	return out, nil
}

func (n *namespacedKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	return n.inner.PutWithMode(ctx, n.ns+key, value, mode)
}

func (n *namespacedKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	return n.inner.DeleteWithMode(ctx, n.ns+key, mode)
}

func (n *namespacedKV) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	return n.inner.GetWithConsistency(ctx, n.ns+key, opts)
}

func (n *namespacedKV) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	pairs, err := n.inner.ScanWithConsistency(ctx, n.ns+prefix, opts)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(pairs))
	for k, v := range pairs {
		out[strings.TrimPrefix(k, n.ns)] = v
	}
	return out, nil
}

// dropTeardownStack is the whole stack a DROP DATABASE traverses.
type dropTeardownStack struct {
	db    DatabaseServicer
	store *memoryKVSvc
	// colls holds one collection service per database, each over that
	// database's namespaced view -- the per-(tenant, database) resolution the
	// server performs on every request.
	colls map[string]*CollectionService
	// kv writes through the composite namespace exactly as a registry-
	// resolved KVServicer does, so a test writes what production writes
	// without standing up pkg/server's namespaced wrappers.
	kv func(ctx context.Context, tenantID, db, key string, value []byte) error
}

func newDropTeardownStack(t *testing.T) *dropTeardownStack {
	t.Helper()
	store := newMemoryKVSvc()
	mgr := database.NewManager(nil, t.TempDir())
	mgr.SetKeyspaceReclaimer(NewKeyspaceReclaimService(ReclaimTarget{KV: store}))
	t.Cleanup(func() { _ = mgr.Close() })

	return &dropTeardownStack{
		db:    NewDatabaseService(mgr),
		store: store,
		colls: make(map[string]*CollectionService),
		kv: func(ctx context.Context, tenantID, db, key string, value []byte) error {
			return store.Put(ctx, tenant.ComposeNamespace(tenantID, db)+key, value)
		},
	}
}

// coll resolves the collection service serving name, building it over that
// database's namespaced view on first use.
func (s *dropTeardownStack) coll(name string) *CollectionService {
	if c, ok := s.colls[name]; ok {
		return c
	}
	c := NewCollectionService(newNamespacedKV(s.store, tenant.SystemTenantID, name))
	s.colls[name] = c
	return c
}

// populate creates the named database and fills it the way a user would: a
// plain key and a collection holding one document.
func (s *dropTeardownStack) populate(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	_, err := s.db.Create(database.DatabaseConfig{
		Name: name, Engine: database.EngineKV, Backend: database.BackendMemory,
	})
	require.NoError(t, err)
	require.NoError(t, s.kv(ctx, tenant.SystemTenantID, name, "greeting", []byte("hello")))
	_, err = s.coll(name).CreateCollection(ctx, &CreateCollectionRequest{Database: name, Name: "users"})
	require.NoError(t, err)
	_, err = s.coll(name).InsertDocument(ctx, &InsertDocumentRequest{
		Database: name, Collection: "users", Document: map[string]interface{}{"who": "alice"},
	})
	require.NoError(t, err)
}

// TestDatabaseService_Drop_RecreatedNameInheritsNothing is the measurement
// go-qrdb#412 asked for, and the answer it gave before the fix was total
// inheritance -- not the partial inheritance go-qrdb#411 found one level
// down. Every key, every collection and every document survived the drop,
// and a database recreated under the same name re-attached to all of it:
//
//	create mydb, put greeting=hello, create collection users, insert 1 doc
//	drop mydb        -> store still holds all three keys
//	create mydb      -> Get(greeting) = "hello"
//	                    ListCollections(mydb) = [users]
//	                    ListDocuments(mydb, users) = 1 document
//
// The issue read that from the code and asked for it to be measured before
// anything was changed. It is, and it was accurate.
func TestDatabaseService_Drop_RecreatedNameInheritsNothing(t *testing.T) {
	ctx := context.Background()
	s := newDropTeardownStack(t)

	s.populate(t, ctx, "mydb")
	require.NoError(t, s.db.Drop(ctx, "mydb"))

	// Recreate under the same name and ask it what it holds.
	_, err := s.db.Create(database.DatabaseConfig{
		Name: "mydb", Engine: database.EngineKV, Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	_, err = s.store.Get(ctx, tenant.ComposeNamespace(tenant.SystemTenantID, "mydb")+"greeting")
	var notFound *KeyNotFoundError
	assert.ErrorAs(t, err, &notFound, "a recreated database must not inherit its predecessor's keys")

	list, err := s.coll("mydb").ListCollections(ctx, &ListCollectionsRequest{Database: "mydb"})
	require.NoError(t, err)
	assert.Empty(t, list.Collections, "a recreated database must not inherit its predecessor's collections")
}

// TestDatabaseService_Drop_LeavesEveryOtherDatabaseAlone is the control that
// makes the test above mean something: a teardown that emptied the whole
// store would also pass it.
func TestDatabaseService_Drop_LeavesEveryOtherDatabaseAlone(t *testing.T) {
	ctx := context.Background()
	s := newDropTeardownStack(t)

	s.populate(t, ctx, "mydb")
	s.populate(t, ctx, "otherdb")

	require.NoError(t, s.db.Drop(ctx, "mydb"))

	v, err := s.store.Get(ctx, tenant.ComposeNamespace(tenant.SystemTenantID, "otherdb")+"greeting")
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), v)

	list, err := s.coll("otherdb").ListCollections(ctx, &ListCollectionsRequest{Database: "otherdb"})
	require.NoError(t, err)
	assert.Len(t, list.Collections, 1)

	docs, err := s.coll("otherdb").ListDocuments(ctx, &ListDocumentsRequest{Database: "otherdb", Collection: "users"})
	require.NoError(t, err)
	assert.Len(t, docs.Documents, 1)
}

// TestDatabaseService_Drop_ReclaimsTheDocumentsAndTheCatalog names the two
// halves separately, because they are reclaimed by different prefixes and a
// teardown that got only one of them would leave either orphaned documents or
// a collection that lists but cannot be read.
func TestDatabaseService_Drop_ReclaimsTheDocumentsAndTheCatalog(t *testing.T) {
	ctx := context.Background()
	s := newDropTeardownStack(t)
	s.populate(t, ctx, "mydb")

	// Both halves are INSIDE the composite namespace, which is where the
	// collection surface has written since it moved behind the
	// per-(tenant, database) resolver. Asserting the root-keyed prefixes here
	// would be asserting a layout nothing writes any more.
	ns := tenant.ComposeNamespace(tenant.SystemTenantID, "mydb")
	meta, err := s.store.List(ctx, ns+collectionMetadataPrefix+"mydb/")
	require.NoError(t, err)
	require.Len(t, meta, 1)
	docs, err := s.store.List(ctx, ns+collectionDataPrefix+"mydb:")
	require.NoError(t, err)
	require.Len(t, docs, 1)

	require.NoError(t, s.db.Drop(ctx, "mydb"))

	meta, err = s.store.List(ctx, ns+collectionMetadataPrefix+"mydb/")
	require.NoError(t, err)
	assert.Empty(t, meta, "the collection catalog entry must be reclaimed")
	docs, err = s.store.List(ctx, ns+collectionDataPrefix+"mydb:")
	require.NoError(t, err)
	assert.Empty(t, docs, "the documents must be reclaimed")
}

// TestDatabaseService_Drop_LeavesPreNamespaceResidueInPlace is go-qrdb#481 at
// the end of the same stack the reclamation test uses.
//
// Records written before the collection surface was tenant-scoped name a
// database and no tenant, so a drop cannot tell whose they are. They survive
// the drop -- and they survive it even when the dropping tenant is the only
// tenant in the deployment, because "the only tenant today" is not a fact
// about who wrote a key yesterday.
func TestDatabaseService_Drop_LeavesPreNamespaceResidueInPlace(t *testing.T) {
	ctx := context.Background()
	s := newDropTeardownStack(t)
	s.populate(t, ctx, "mydb")

	// Residue in the pre-namespace layout, written straight at the store root
	// the way the old server-wide collection service wrote it.
	for _, k := range []string{
		collectionMetadataPrefix + "mydb/legacy",
		collectionDataPrefix + "mydb:legacy/1",
	} {
		require.NoError(t, s.store.Put(ctx, k, []byte("v")))
	}

	require.NoError(t, s.db.Drop(ctx, "mydb"))

	for _, prefix := range LegacyCollectionKeyPrefixes("mydb") {
		keys, err := s.store.ListAll(ctx, prefix)
		require.NoError(t, err)
		assert.NotEmpty(t, keys,
			"the drop deleted unattributable residue under %q; it is the only thing left to export", prefix)
	}
}

// TestDatabaseService_Drop_UnknownDatabaseIsStillNotFound pins that the fix
// did not change the not-found contract: a drop names a database, and naming
// one that does not exist is an error, not a silent success.
func TestDatabaseService_Drop_UnknownDatabaseIsStillNotFound(t *testing.T) {
	s := newDropTeardownStack(t)

	err := s.db.Drop(context.Background(), "phantom")

	var want *database.NotFoundError
	assert.ErrorAs(t, err, &want)
}
