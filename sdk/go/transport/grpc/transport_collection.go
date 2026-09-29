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

package grpc

import (
	"context"
	"encoding/json"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/collectionconv"
)

// Compile-time interface assertion.
var _ transport.CollectionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Collection management
// ---------------------------------------------------------------------------

// CreateCollection creates a new collection within a database.
func (t *Transport) CreateCollection(ctx context.Context, req transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CreateCollection(ctx, collectionconv.ToCreateRequest(req))
	if err != nil {
		return nil, mapGRPCError("CreateCollection", err)
	}
	return collectionconv.ToCollectionInfo(resp), nil
}

// GetCollectionInfo returns information about a specific collection.
func (t *Transport) GetCollectionInfo(ctx context.Context, database, name string) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetCollectionInfo(ctx, &pb.GetCollectionInfoRequest{
		Database: database,
		Name:     name,
	})
	if err != nil {
		return nil, mapGRPCError("GetCollectionInfo", err)
	}
	return collectionconv.ToCollectionInfo(resp), nil
}

// ListCollections returns all collections within a database.
func (t *Transport) ListCollections(ctx context.Context, database string) (*transport.ListCollectionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListCollections(ctx, &pb.ListCollectionsRequest{
		Database: database,
	})
	if err != nil {
		return nil, mapGRPCError("ListCollections", err)
	}
	collections := make([]transport.CollectionInfo, len(resp.GetCollections()))
	for i, c := range resp.GetCollections() {
		collections[i] = *collectionconv.ToCollectionInfo(c)
	}
	return &transport.ListCollectionsResponse{Collections: collections}, nil
}

// DropCollection removes a collection from a database by name.
func (t *Transport) DropCollection(ctx context.Context, database, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.DropCollection(ctx, &pb.DropCollectionRequest{
		Database: database,
		Name:     name,
	})
	if err != nil {
		return mapGRPCError("DropCollection", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Document operations
// ---------------------------------------------------------------------------

// InsertDocument inserts a document into a collection.
func (t *Transport) InsertDocument(ctx context.Context, req transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	docJSON, err := json.Marshal(req.Document)
	if err != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: err}
	}
	resp, err := t.client.InsertDocument(ctx, &pb.InsertDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Document:   string(docJSON),
	})
	if err != nil {
		return nil, mapGRPCError("InsertDocument", err)
	}
	return &transport.InsertDocumentResponse{ID: resp.GetId()}, nil
}

// GetDocument returns a single document by its ID.
func (t *Transport) GetDocument(ctx context.Context, database, collection string, id uint64) (*transport.DocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetDocument(ctx, &pb.GetDocumentRequest{
		Database:   database,
		Collection: collection,
		Id:         id,
	})
	if err != nil {
		return nil, mapGRPCError("GetDocument", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(resp.GetDocument()), &doc); err != nil {
		return nil, &transport.RequestError{Method: "GetDocument", Err: err}
	}
	return &transport.DocumentResponse{ID: resp.GetId(), Document: doc}, nil
}

// UpdateDocument replaces an existing document in a collection.
func (t *Transport) UpdateDocument(ctx context.Context, req transport.UpdateDocumentRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	docJSON, err := json.Marshal(req.Document)
	if err != nil {
		return &transport.RequestError{Method: "UpdateDocument", Err: err}
	}
	_, err = t.client.UpdateDocument(ctx, &pb.UpdateDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Id:         req.ID,
		Document:   string(docJSON),
	})
	if err != nil {
		return mapGRPCError("UpdateDocument", err)
	}
	return nil
}

// DeleteDocument removes a document from a collection by its ID.
func (t *Transport) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	_, err := t.client.DeleteDocument(ctx, &pb.DeleteDocumentRequest{
		Database:   database,
		Collection: collection,
		Id:         id,
	})
	if err != nil {
		return mapGRPCError("DeleteDocument", err)
	}
	return nil
}

// ListDocuments returns a paginated list of documents in a collection.
func (t *Transport) ListDocuments(ctx context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListDocuments(ctx, &pb.ListDocumentsRequest{
		Database:   req.Database,
		Collection: req.Collection,
		PageSize:   req.PageSize,
		PageToken:  req.PageToken,
	})
	if err != nil {
		return nil, mapGRPCError("ListDocuments", err)
	}
	docs := make([]transport.DocumentResponse, len(resp.GetDocuments()))
	for i, d := range resp.GetDocuments() {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(d.GetDocument()), &doc); err != nil {
			return nil, &transport.RequestError{Method: "ListDocuments", Err: err}
		}
		docs[i] = transport.DocumentResponse{ID: d.GetId(), Document: doc}
	}
	return &transport.ListDocumentsResponse{
		Documents:     docs,
		NextPageToken: resp.GetNextPageToken(),
		TotalCount:    resp.GetTotalCount(),
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------
