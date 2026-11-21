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
