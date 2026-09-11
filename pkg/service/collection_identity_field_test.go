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

// "_id" names a document; it is not one of its fields (go-qrdb#467).
//
// Storing it as one made a document's shape depend on how its ID was decided
// -- an explicitly identified document carried a field a generated one did
// not -- and, because updateSchema records every key it is given, it put
// "_id" into the collection's schema. The SQL surface builds its collection
// table from that schema and already declares "_id" as column 0, so
// `SELECT *` returned two columns with the same name.

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDocumentFields_StripsTheIdentity covers the strip.
func TestDocumentFields_StripsTheIdentity(t *testing.T) {
	got := documentFields(map[string]interface{}{"_id": uint64(42), "kind": "deploy"})
	assert.Equal(t, map[string]interface{}{"kind": "deploy"}, got)
}

// TestDocumentFields_StripsANonNumericIdentity pins that the key is removed
// for naming the identity, not for carrying a usable value: a string "_id" is
// not an ID the service can honour, but it is still the identity key and must
// not become a column shadowing the real one.
func TestDocumentFields_StripsANonNumericIdentity(t *testing.T) {
	got := documentFields(map[string]interface{}{"_id": "abc", "kind": "deploy"})
	assert.Equal(t, map[string]interface{}{"kind": "deploy"}, got)
}

// TestDocumentFields_PassesThroughADocumentWithoutAnIdentity covers the
// common path, which must not allocate a copy.
func TestDocumentFields_PassesThroughADocumentWithoutAnIdentity(t *testing.T) {
	doc := map[string]interface{}{"kind": "deploy"}
	got := documentFields(doc)
	assert.Equal(t, doc, got)
	doc["added"] = true
	assert.Contains(t, got, "added", "a document without _id is returned as-is, not copied")
}

// TestDocumentFields_DoesNotMutateTheCaller is load-bearing: on several
// transports the map handed to InsertDocument was decoded straight from the
// request body, and InsertDocument reads the identity out of it AFTER the
// strip would have run.
func TestDocumentFields_DoesNotMutateTheCaller(t *testing.T) {
	doc := map[string]interface{}{"_id": uint64(42), "kind": "deploy"}
	_ = documentFields(doc)
	assert.Equal(t, uint64(42), doc["_id"], "the caller's map must be untouched")
	assert.Equal(t, uint64(42), extractExplicitID(doc), "the claim must still be readable")
}

// TestDocumentFields_EmptyDocument covers the degenerate input.
func TestDocumentFields_EmptyDocument(t *testing.T) {
	assert.Empty(t, documentFields(map[string]interface{}{}))
	assert.Empty(t, documentFields(map[string]interface{}{"_id": uint64(1)}))
}

// TestCollectionService_ExplicitIDIsNotStoredAsAField is the property through
// the verb: the same document inserted with and without an explicit _id is
// the same document.
func TestCollectionService_ExplicitIDIsNotStoredAsAField(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	explicit := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 7, "who": "explicit"})
	generated := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "generated"})

	withID, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: explicit})
	require.NoError(t, err)
	withoutID, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: generated})
	require.NoError(t, err)

	assert.NotContains(t, withID.Document, "_id", "the identity is the document's ID, not one of its fields")
	assert.Len(t, withID.Document, len(withoutID.Document),
		"how a document's ID was decided must not change its shape")
	assert.Equal(t, uint64(7), withID.ID, "the identity is still honoured")
}

// TestCollectionService_ExplicitIDDoesNotEnterTheSchema is the half the SQL
// surface depends on: CollectionInfo.Schema is what
// pkg/sql/provider.buildCollectionSchema builds a collection table's columns
// from.
func TestCollectionService_ExplicitIDDoesNotEnterTheSchema(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")

	insertDoc(t, svc, "mydb", "users", map[string]interface{}{"_id": 7, "who": "explicit"})

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"who": "VARCHAR"}, info.Schema)
}

// TestCollectionService_UpdateDocumentDropsTheIdentityKey pins the same rule
// on the other write verb. An updated document must have the same shape as an
// inserted one, or a read-modify-write through any transport that echoes the
// ID back reintroduces the field.
func TestCollectionService_UpdateDocumentDropsTheIdentityKey(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	id := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "before"})

	_, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Database: "mydb", Collection: "users", ID: id,
		Document: map[string]interface{}{"_id": id, "who": "after"},
	})
	require.NoError(t, err)

	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: id})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"who": "after"}, got.Document)

	info, err := svc.GetCollectionInfo(ctx, "mydb", "users")
	require.NoError(t, err)
	assert.NotContains(t, info.Schema, "_id")
}

// TestCollectionService_UpdateDocumentTakesItsIDFromTheRequest pins which of
// the two spellings wins when they disagree. UpdateDocument addresses a
// document that already exists; the body carries its fields, never its
// identity.
func TestCollectionService_UpdateDocumentTakesItsIDFromTheRequest(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newIncrementCollectionService()
	createCollection(t, svc, "mydb", "users", IDStrategyIncrement, "")
	first := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "first"})
	second := insertDoc(t, svc, "mydb", "users", map[string]interface{}{"who": "second"})

	_, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Database: "mydb", Collection: "users", ID: second,
		Document: map[string]interface{}{"_id": first, "who": "updated"},
	})
	require.NoError(t, err)

	untouched, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: first})
	require.NoError(t, err)
	assert.Equal(t, "first", untouched.Document["who"], "the body's _id must not redirect the update")

	updated, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "mydb", Collection: "users", ID: second})
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Document["who"])
}
