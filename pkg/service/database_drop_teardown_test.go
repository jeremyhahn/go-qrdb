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
)

// This is the end-to-end statement of go-qrdb#412: a database dropped and
// recreated under the same name must inherit nothing.
//
// The stack here is the production one minus the transport: a real
// database.Manager over a temp directory, a real KeyspaceReclaimService over
// a shared store, and the real DatabaseService in front of both -- wired the
// way pkg/server/lifecycle.BuildServices wires them. The collection service
// is built over the SAME store with no namespacing, because that is how the
// server builds it: one server-wide CollectionServicer over the system
// shard's root KV.

// dropTeardownStack is the whole stack a DROP DATABASE traverses.
type dropTeardownStack struct {
	db    DatabaseServicer
	store *memoryKVSvc
	coll  *CollectionService
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
		coll:  NewCollectionService(store),
		kv: func(ctx context.Context, tenantID, db, key string, value []byte) error {
			return store.Put(ctx, tenant.ComposeNamespace(tenantID, db)+key, value)
		},
	}
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
	_, err = s.coll.CreateCollection(ctx, &CreateCollectionRequest{Database: name, Name: "users"})
	require.NoError(t, err)
	_, err = s.coll.InsertDocument(ctx, &InsertDocumentRequest{
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

	list, err := s.coll.ListCollections(ctx, &ListCollectionsRequest{Database: "mydb"})
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

	list, err := s.coll.ListCollections(ctx, &ListCollectionsRequest{Database: "otherdb"})
	require.NoError(t, err)
	assert.Len(t, list.Collections, 1)

	docs, err := s.coll.ListDocuments(ctx, &ListDocumentsRequest{Database: "otherdb", Collection: "users"})
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

	meta, err := s.store.List(ctx, collectionMetadataPrefix+"mydb/")
	require.NoError(t, err)
	require.Len(t, meta, 1)
	docs, err := s.store.List(ctx, collectionDataPrefix+"mydb:")
	require.NoError(t, err)
	require.Len(t, docs, 1)

	require.NoError(t, s.db.Drop(ctx, "mydb"))

	meta, err = s.store.List(ctx, collectionMetadataPrefix+"mydb/")
	require.NoError(t, err)
	assert.Empty(t, meta, "the collection catalog entry must be reclaimed")
	docs, err = s.store.List(ctx, collectionDataPrefix+"mydb:")
	require.NoError(t, err)
	assert.Empty(t, docs, "the documents must be reclaimed")
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
