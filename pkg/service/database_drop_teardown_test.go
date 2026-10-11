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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
			return store.view(tenantID, db).Put(ctx, key, value)
		},
	}
}

// coll resolves the collection service serving name, building it over that
// database's namespaced view on first use.
func (s *dropTeardownStack) coll(name string) *CollectionService {
	if c, ok := s.colls[name]; ok {
		return c
	}
	c := NewCollectionService(s.store.view(tenantid.System, name))
	s.colls[name] = c
	return c
}

// populate creates the named database and fills it the way a user would: a
// plain key and a collection holding one document.
func (s *dropTeardownStack) populate(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	_, err := s.db.Create(database.DatabaseConfig{
		Name: name, Backend: database.BackendMemory,
	})
	require.NoError(t, err)
	require.NoError(t, s.kv(ctx, tenantid.System, name, "greeting", []byte("hello")))
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
		Name: "mydb", Backend: database.BackendMemory,
	})
	require.NoError(t, err)

	_, err = s.store.Get(ctx, tenant.ComposeNamespace(tenantid.System, "mydb")+"greeting")
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

	v, err := s.store.Get(ctx, tenant.ComposeNamespace(tenantid.System, "otherdb")+"greeting")
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
	ns := tenant.ComposeNamespace(tenantid.System, "mydb")
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
