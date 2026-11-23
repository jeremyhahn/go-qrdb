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

// A value yaml.v3 cannot encode panics inside the encoder; the collection
// returns its encoding error on insert and update instead.
func TestCollectionService_InsertUpdate_YAMLEncodePanic(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()
	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name: "docs", Database: "mydb", StorageCodec: string(dao.CodecYAML),
	})
	require.NoError(t, err)

	bad := map[string]interface{}{"value": func() {}}

	require.NotPanics(t, func() {
		_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{
			Collection: "docs", Database: "mydb", Document: bad,
		})
	})
	var cme *CollectionMetadataError
	require.Truef(t, errors.As(err, &cme), "insert: want *CollectionMetadataError, got %v", err)
	require.Equal(t, "marshal-document", cme.Op)

	ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "docs", Database: "mydb", Document: map[string]interface{}{"value": "ok"},
	})
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{
			Collection: "docs", Database: "mydb", ID: ins.ID, Document: bad,
		})
	})
	cme = nil
	require.Truef(t, errors.As(err, &cme), "update: want *CollectionMetadataError, got %v", err)
	require.Equal(t, "marshal-document", cme.Op)
}
