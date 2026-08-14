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

//go:build codec_msgpack

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// ---------------------------------------------------------------------------
// Per-collection storage codec tests
// ---------------------------------------------------------------------------

func TestCollectionService_CreateCollection_WithStorageCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "users",
		Database:     "mydb",
		StorageCodec: codec.TypeMsgPack,
	})
	require.NoError(t, err)
	assert.Equal(t, codec.TypeMsgPack, info.StorageCodec)
}

func TestCollectionService_CreateCollection_InvalidStorageCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "users",
		Database:     "mydb",
		StorageCodec: "nonexistent_codec",
	})
	assert.IsType(t, &CollectionInvalidCodecError{}, err)
}

func TestCollectionService_InsertGet_MsgpackCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "products",
		Database:     "mydb",
		IDStrategy:   IDStrategyIncrement,
		StorageCodec: codec.TypeMsgPack,
	})
	require.NoError(t, err)

	insertResp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "products",
		Database:   "mydb",
		Document:   map[string]interface{}{"name": "widget", "price": float64(9.99)},
	})
	require.NoError(t, err)
	assert.NotZero(t, insertResp.ID)

	getResp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "products",
		Database:   "mydb",
		ID:         insertResp.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "widget", getResp.Document["name"])
}

func TestCollectionService_UpdateDocument_MsgpackCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "items",
		Database:     "mydb",
		IDStrategy:   IDStrategyIncrement,
		StorageCodec: codec.TypeMsgPack,
	})
	require.NoError(t, err)

	insertResp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "items",
		Database:   "mydb",
		Document:   map[string]interface{}{"status": "new"},
	})
	require.NoError(t, err)

	_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "items",
		Database:   "mydb",
		ID:         insertResp.ID,
		Document:   map[string]interface{}{"status": "updated"},
	})
	require.NoError(t, err)

	getResp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "items",
		Database:   "mydb",
		ID:         insertResp.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", getResp.Document["status"])
}

func TestCollectionService_ListDocuments_MsgpackCodec(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "logs",
		Database:     "mydb",
		IDStrategy:   IDStrategyIncrement,
		StorageCodec: codec.TypeMsgPack,
	})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "logs",
			Database:   "mydb",
			Document:   map[string]interface{}{"seq": float64(i)},
		})
		require.NoError(t, err)
	}

	listResp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "logs",
		Database:   "mydb",
	})
	require.NoError(t, err)
	assert.Len(t, listResp.Documents, 3)
	assert.Equal(t, int64(3), listResp.TotalCount)
}

// TestCollectionService_BackwardCompatibility verifies that JSON-encoded
// documents written before a codec was configured can still be read when
// a collection's StorageCodec is set to msgpack.
func TestCollectionService_BackwardCompatibility(t *testing.T) {
	kvSvc := newMemoryKVSvc()
	ctx := context.Background()

	// Manually write a JSON-encoded document into the KV store,
	// simulating a document written before the codec was configured.
	// Use a pointer to ensure MarshalJSON (pointer receiver) is called.
	entity := &dao.DynamicEntity{
		ID:     42,
		Fields: map[string]interface{}{"legacy": "value"},
	}
	jsonBytes, err := json.Marshal(entity)
	require.NoError(t, err)

	docKey := collectionDataEntityType("mydb", "legacy") + "/42"
	require.NoError(t, kvSvc.Put(ctx, docKey, jsonBytes))

	// Create a collection service with the msgpack codec configured on an
	// existing collection (as if the codec was added after initial documents
	// were written).
	svc := NewCollectionService(kvSvc)

	// Store collection metadata with msgpack codec set.
	info := &CollectionInfo{
		Name:         "legacy",
		Database:     "mydb",
		IDStrategy:   IDStrategyUnixNano,
		StorageCodec: codec.TypeMsgPack,
		Schema:       make(map[string]string),
	}
	metaBytes, err := json.Marshal(info)
	require.NoError(t, err)
	require.NoError(t, kvSvc.Put(ctx, collectionMetadataKey("mydb", "legacy"), metaBytes))

	// Reading the legacy JSON document through the msgpack-configured
	// collection should succeed via the JSON fallback path.
	getResp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "legacy",
		Database:   "mydb",
		ID:         42,
	})
	require.NoError(t, err)
	assert.Equal(t, "value", getResp.Document["legacy"])
}

// ---------------------------------------------------------------------------
// Per-database codec lookup tests
// ---------------------------------------------------------------------------

func TestCollectionService_DatabaseCodecLookup(t *testing.T) {
	svc := newTestCollectionService().WithDatabaseCodecLookup(func(db string) string {
		if db == "msgpack_db" {
			return codec.TypeMsgPack
		}
		return ""
	})
	ctx := context.Background()

	// Create a collection with no per-collection codec. The database-level
	// default should be resolved via the lookup callback.
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "events",
		Database: "msgpack_db",
	})
	require.NoError(t, err)

	insertResp, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "events",
		Database:   "msgpack_db",
		Document:   map[string]interface{}{"type": "click"},
	})
	require.NoError(t, err)

	getResp, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "events",
		Database:   "msgpack_db",
		ID:         insertResp.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "click", getResp.Document["type"])
}

func TestCollectionService_DatabaseCodecLookup_CollectionOverrides(t *testing.T) {
	svc := newTestCollectionService().WithDatabaseCodecLookup(func(_ string) string {
		return codec.TypeMsgPack
	})
	ctx := context.Background()

	// Per-collection codec takes priority over the database-level default.
	info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:         "override",
		Database:     "mydb",
		StorageCodec: codec.TypeJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, codec.TypeJSON, info.StorageCodec)

	c := svc.resolveCodec(info)
	assert.Equal(t, codec.TypeJSON, c.Type())
}
