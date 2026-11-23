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

//go:build codec_cbor

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// A CBOR collection accepts no unique index: the engine could not decode its
// documents, so two documents could share a uniquely indexed value.
func TestCollectionService_CreateCollection_IndexesRequireJSONCodec_CBOR(t *testing.T) {
	assertIndexesRequireJSON(t, dao.CodecCBOR, false)
	assertIndexesRequireJSON(t, dao.CodecCBOR, true)
}

// TestCollectionService_InsertDocument_CBORUndecodableNestedMapRefused proves
// a CBOR collection refuses a document its decoder cannot read back (a nested
// map with integer keys) at insert, so nothing unreadable is stored, while a
// string-keyed nested document inserts and reads back.
func TestCollectionService_InsertDocument_CBORUndecodableNestedMapRefused(t *testing.T) {
	ctx := context.Background()
	svc := NewCollectionServiceWithIndex(newResolvedKVSvc(), newMemoryIndexSvc())
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "docs", Database: "mydb", StorageCodec: string(dao.CodecCBOR),
	})
	require.NoError(t, err)

	_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "docs", Database: "mydb",
		Document: map[string]interface{}{"nested": map[int]string{1: "one"}},
	})
	require.Error(t, err)
	var shapeErr dao.CBORDecodeShapeError
	assert.True(t, errors.As(err, &shapeErr), "error is %T: %v", err, err)

	list, err := svc.ListDocuments(ctx, &ListDocumentsRequest{Collection: "docs", Database: "mydb"})
	require.NoError(t, err)
	assert.Empty(t, list.Documents, "a refused document is not stored")

	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "docs", Database: "mydb",
		Document: map[string]interface{}{"nested": map[string]interface{}{"a": "b"}},
	})
	require.NoError(t, err)
	got, err := svc.GetDocument(ctx, &GetDocumentRequest{Collection: "docs", Database: "mydb", ID: ins.ID})
	require.NoError(t, err)
	nested, ok := got.Document["nested"].(map[string]interface{})
	require.True(t, ok, "nested is %T", got.Document["nested"])
	assert.Equal(t, "b", nested["a"])
}
