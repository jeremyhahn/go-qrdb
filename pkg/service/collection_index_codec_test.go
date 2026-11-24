// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// assertIndexesRequireJSON creates a collection with a unique index under
// codec (set on the collection, or as the database default when viaDatabase)
// and asserts the definition is refused: the engine decodes indexed values as
// JSON, so under any other codec a unique index would pass every document.
func assertIndexesRequireJSON(t *testing.T, codec dao.CodecType, viaDatabase bool) {
	t.Helper()
	svc := NewCollectionServiceWithIndex(newResolvedKVSvc(), newMemoryIndexSvc())
	req := &CreateCollectionRequest{
		Name: "users", Database: "mydb",
		Indexes: []IndexDefinition{{FieldName: "email", Unique: true}},
	}
	if viaDatabase {
		svc.WithDatabaseCodecLookup(func(string) string { return string(codec) })
	} else {
		req.StorageCodec = string(codec)
	}
	_, err := svc.CreateCollection(context.Background(), req)
	var iie *CollectionInvalidIndexError
	require.Truef(t, errors.As(err, &iie), "codec %s: want *CollectionInvalidIndexError, got %v", codec, err)
}

func TestCollectionService_CreateCollection_IndexesRequireJSONCodec(t *testing.T) {
	cases := []struct {
		name        string
		codec       dao.CodecType
		viaDatabase bool
	}{
		{"msgpack", dao.CodecMsgPack, false},
		{"yaml", dao.CodecYAML, false},
		{"bson", dao.CodecBSON, false},
		{"database default msgpack", dao.CodecMsgPack, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertIndexesRequireJSON(t, tc.codec, tc.viaDatabase)
		})
	}

	t.Run("json accepted", func(t *testing.T) {
		svc := NewCollectionServiceWithIndex(newResolvedKVSvc(), newMemoryIndexSvc())
		_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
			Name: "users", Database: "mydb", StorageCodec: string(dao.CodecJSON),
			Indexes: []IndexDefinition{{FieldName: "email", Unique: true}},
		})
		require.NoError(t, err)
	})

	t.Run("non-json codec without indexes accepted", func(t *testing.T) {
		svc := NewCollectionServiceWithIndex(newResolvedKVSvc(), newMemoryIndexSvc())
		_, err := svc.CreateCollection(context.Background(), &CreateCollectionRequest{
			Name: "users", Database: "mydb", StorageCodec: string(dao.CodecMsgPack),
		})
		require.NoError(t, err)
	})
}

// assertWritesRefusedBeforePropose runs Insert, Update and Upsert against
// mydb.users and checks each returns *CollectionInvalidIndexError with the
// store byte-for-byte unchanged: no ID reserved, nothing proposed.
func assertWritesRefusedBeforePropose(t *testing.T, svc *CollectionService, kv *memoryKVSvc) {
	t.Helper()
	ctx := context.Background()
	before, err := kv.Scan(ctx, "")
	require.NoError(t, err)
	doc := map[string]interface{}{"email": "a@example.com"}

	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "mydb", Collection: "users", Document: doc})
	var iie *CollectionInvalidIndexError
	require.Truef(t, errors.As(err, &iie), "insert: want *CollectionInvalidIndexError, got %v", err)

	_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{Database: "mydb", Collection: "users", ID: 1, Document: doc})
	require.Truef(t, errors.As(err, &iie), "update: want *CollectionInvalidIndexError, got %v", err)

	_, err = svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "mydb", Collection: "users", ID: 1, Document: doc})
	require.Truef(t, errors.As(err, &iie), "upsert: want *CollectionInvalidIndexError, got %v", err)

	after, err := kv.Scan(ctx, "")
	require.NoError(t, err)
	require.Equal(t, before, after, "a refused write changed the store")
}

// TestCollectionService_WritesToLegacyIndexedNonJSONCollectionRefused pins
// the propose-side refusal of document writes to an indexed collection whose
// effective codec is not JSON: one created before CreateCollection refused
// the combination (seeded here by writing its metadata record directly), or
// one whose database default codec changed after creation. The engine's
// apply path keeps storing such values unindexed so old logs replay.
func TestCollectionService_WritesToLegacyIndexedNonJSONCollectionRefused(t *testing.T) {
	ctx := context.Background()
	newIndexed := func(t *testing.T) (*CollectionService, *memoryKVSvc) {
		t.Helper()
		kv := newResolvedKVSvc()
		svc := NewCollectionServiceWithIndex(kv, newMemoryIndexSvc())
		_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
			Name: "users", Database: "mydb",
			Indexes: []IndexDefinition{{FieldName: "email", Unique: true}},
		})
		require.NoError(t, err)
		return svc, kv
	}

	t.Run("legacy msgpack collection", func(t *testing.T) {
		svc, kv := newIndexed(t)
		info, _, err := svc.loadCollectionRecord(ctx, "mydb", "users")
		require.NoError(t, err)
		require.NotEmpty(t, info.Indexes)
		info.StorageCodec = string(dao.CodecMsgPack)
		data, err := json.Marshal(info)
		require.NoError(t, err)
		require.NoError(t, kv.Put(ctx, collectionMetadataKey("mydb", "users"), data))
		assertWritesRefusedBeforePropose(t, svc, kv)
	})

	t.Run("database default changed to msgpack", func(t *testing.T) {
		svc, kv := newIndexed(t)
		svc.WithDatabaseCodecLookup(func(string) string { return string(dao.CodecMsgPack) })
		assertWritesRefusedBeforePropose(t, svc, kv)
	})

	t.Run("json collection still writes", func(t *testing.T) {
		svc, _ := newIndexed(t)
		_, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
			Database: "mydb", Collection: "users",
			Document: map[string]interface{}{"email": "a@example.com"},
		})
		require.NoError(t, err)
	})
}
