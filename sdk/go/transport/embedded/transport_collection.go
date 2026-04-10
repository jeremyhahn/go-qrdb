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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CollectionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CollectionClient implementation
// ---------------------------------------------------------------------------

// CreateCollection creates a new collection by delegating to the injected
// CollectionServicer. Returns NilServiceError when no collection service
// has been configured.
func (t *Transport) CreateCollection(ctx context.Context, req transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	info, err := t.collectionSvc.CreateCollection(ctx, &service.CreateCollectionRequest{
		Name:         req.Name,
		Database:     req.Database,
		IDStrategy:   req.IDStrategy,
		FieldHash:    req.FieldHash,
		StorageCodec: req.StorageCodec,
	})
	if err != nil {
		return nil, err
	}
	return toTransportCollectionInfo(info), nil
}

// GetCollectionInfo returns information about a specific collection by
// delegating to the injected CollectionServicer. Returns NilServiceError
// when no collection service has been configured.
func (t *Transport) GetCollectionInfo(ctx context.Context, database, name string) (*transport.CollectionInfo, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	info, err := t.collectionSvc.GetCollectionInfo(ctx, database, name)
	if err != nil {
		return nil, err
	}
	return toTransportCollectionInfo(info), nil
}

// ListCollections returns all collections within a database by delegating
// to the injected CollectionServicer. Returns NilServiceError when no
// collection service has been configured.
func (t *Transport) ListCollections(ctx context.Context, database string) (*transport.ListCollectionsResponse, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	resp, err := t.collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
		Database: database,
	})
	if err != nil {
		return nil, err
	}
	collections := make([]transport.CollectionInfo, len(resp.Collections))
	for i, info := range resp.Collections {
		collections[i] = *toTransportCollectionInfo(&info)
	}
	return &transport.ListCollectionsResponse{
		Collections: collections,
	}, nil
}

// DropCollection removes a collection from a database by delegating to
// the injected CollectionServicer. Returns NilServiceError when no
// collection service has been configured.
func (t *Transport) DropCollection(ctx context.Context, database, name string) error {
	if t.collectionSvc == nil {
		return &NilServiceError{Service: "CollectionServicer"}
	}
	return t.collectionSvc.DropCollection(ctx, &service.DropCollectionRequest{
		Database: database,
		Name:     name,
	})
}

// InsertDocument inserts a document into a collection and returns the
// assigned document ID by delegating to the injected CollectionServicer.
// Returns NilServiceError when no collection service has been configured.
func (t *Transport) InsertDocument(ctx context.Context, req transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	resp, err := t.collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Document:   req.Document,
	})
	if err != nil {
		return nil, err
	}
	return &transport.InsertDocumentResponse{
		ID: resp.ID,
	}, nil
}

// GetDocument returns a single document by its ID by delegating to the
// injected CollectionServicer. Returns NilServiceError when no collection
// service has been configured.
func (t *Transport) GetDocument(ctx context.Context, database, collection string, id uint64) (*transport.DocumentResponse, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	resp, err := t.collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
		Database:   database,
		Collection: collection,
		ID:         id,
	})
	if err != nil {
		return nil, err
	}
	return toTransportDocumentResponse(resp), nil
}

// UpdateDocument replaces an existing document in a collection by
// delegating to the injected CollectionServicer. Returns NilServiceError
// when no collection service has been configured.
func (t *Transport) UpdateDocument(ctx context.Context, req transport.UpdateDocumentRequest) error {
	if t.collectionSvc == nil {
		return &NilServiceError{Service: "CollectionServicer"}
	}
	_, err := t.collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		ID:         req.ID,
		Document:   req.Document,
	})
	return err
}

// DeleteDocument removes a document from a collection by its ID by
// delegating to the injected CollectionServicer. Returns NilServiceError
// when no collection service has been configured.
func (t *Transport) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	if t.collectionSvc == nil {
		return &NilServiceError{Service: "CollectionServicer"}
	}
	_, err := t.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
		Database:   database,
		Collection: collection,
		ID:         id,
	})
	return err
}

// ListDocuments returns a paginated list of documents in a collection by
// delegating to the injected CollectionServicer. Returns NilServiceError
// when no collection service has been configured.
func (t *Transport) ListDocuments(ctx context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if t.collectionSvc == nil {
		return nil, &NilServiceError{Service: "CollectionServicer"}
	}
	resp, err := t.collectionSvc.ListDocuments(ctx, &service.ListDocumentsRequest{
		Database:   req.Database,
		Collection: req.Collection,
		PageSize:   req.PageSize,
		PageToken:  req.PageToken,
	})
	if err != nil {
		return nil, err
	}
	docs := make([]transport.DocumentResponse, len(resp.Documents))
	for i, doc := range resp.Documents {
		docs[i] = *toTransportDocumentResponse(&doc)
	}
	return &transport.ListDocumentsResponse{
		Documents:     docs,
		NextPageToken: resp.NextPageToken,
		TotalCount:    resp.TotalCount,
	}, nil
}

// ---------------------------------------------------------------------------
// Type conversion helpers
// ---------------------------------------------------------------------------

// toTransportCollectionInfo converts a service.CollectionInfo to a
// transport.CollectionInfo, formatting CreatedAt as an RFC3339 string.
func toTransportCollectionInfo(info *service.CollectionInfo) *transport.CollectionInfo {
	return &transport.CollectionInfo{
		Name:          info.Name,
		Database:      info.Database,
		IDStrategy:    info.IDStrategy,
		FieldHash:     info.FieldHash,
		Schema:        info.Schema,
		DocumentCount: info.DocumentCount,
		StorageCodec:  info.StorageCodec,
		CreatedAt:     info.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// toTransportDocumentResponse converts a service.GetDocumentResponse to a
// transport.DocumentResponse.
func toTransportDocumentResponse(resp *service.GetDocumentResponse) *transport.DocumentResponse {
	return &transport.DocumentResponse{
		ID:       resp.ID,
		Document: resp.Document,
	}
}
