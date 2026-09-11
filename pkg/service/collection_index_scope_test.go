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
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// A collection's index is scoped to the collection's documents (go-qrdb#410)
// ---------------------------------------------------------------------------

// TestCollectionService_CreateCollection_DeclaresDocumentKeyScope states that
// CreateCollection tells the index subsystem which keys the index it asks for
// covers. The index NAME, "{database}.{collection}.{field}", is a routing
// label the apply path cannot turn into a key predicate: without the declared
// scope the index took every JSON value written anywhere in the state machine.
func TestCollectionService_CreateCollection_DeclaresDocumentKeyScope(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()

	_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes: []IndexDefinition{
			{FieldName: "email", Unique: true},
			{FieldName: "city"},
		},
	})
	require.NoError(t, err)

	idxSvc.mu.Lock()
	defer idxSvc.mu.Unlock()

	for _, name := range []string{"mydb.users.email", "mydb.users.city"} {
		cfg, ok := idxSvc.indexes[name]
		require.Truef(t, ok, "index %q was not created", name)
		assert.Equal(t, "__col:mydb:users/", cfg.KeyPrefix,
			"index %q must be scoped to the collection's document keyspace", name)
	}
}

// TestCollectionService_CreateCollection_KeyScopeMatchesDocumentKeys pins the
// declared scope against the key the service actually writes a document
// under, so the two cannot drift apart: a scope that does not prefix the
// document key produces an index that is created, never populated, and
// answers every query empty with a nil error.
func TestCollectionService_CreateCollection_KeyScopeMatchesDocumentKeys(t *testing.T) {
	svc, idxSvc := newTestCollectionServiceWithIndex()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "users",
		Database: "mydb",
		Indexes:  []IndexDefinition{{FieldName: "email"}},
	})
	require.NoError(t, err)

	resp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Database:   "mydb",
		Collection: "users",
		Document:   map[string]interface{}{"email": "a@example.com"},
	})
	require.NoError(t, err)

	idxSvc.mu.Lock()
	cfg := idxSvc.indexes["mydb.users.email"]
	idxSvc.mu.Unlock()

	docKey := collectionDataEntityType("mydb", "users") + "/" + strconv.FormatUint(resp.ID, 10)
	assert.Truef(t, len(cfg.KeyPrefix) > 0 && len(docKey) >= len(cfg.KeyPrefix) &&
		docKey[:len(cfg.KeyPrefix)] == cfg.KeyPrefix,
		"document key %q is not inside the declared index scope %q", docKey, cfg.KeyPrefix)
}
