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

package quic

import (
	"context"
	"encoding/json"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
	payload, err := proto.Marshal(&pb.CreateCollectionRequest{
		Database:   req.Database,
		Name:       req.Name,
		IdStrategy: req.IDStrategy,
		FieldHash:  req.FieldHash,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.Create", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.CollectionInfoResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return protoToCollectionInfo(&resp), nil
}

// GetCollectionInfo returns information about a specific collection.
func (t *Transport) GetCollectionInfo(ctx context.Context, database, name string) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.GetCollectionInfoRequest{
		Database: database,
		Name:     name,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.GetInfo", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.CollectionInfoResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return protoToCollectionInfo(&resp), nil
}

// ListCollections returns all collections within a database.
func (t *Transport) ListCollections(ctx context.Context, database string) (*transport.ListCollectionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.ListCollectionsRequest{
		Database: database,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.List", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.ListCollectionsResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	collections := make([]transport.CollectionInfo, len(resp.GetCollections()))
	for i, c := range resp.GetCollections() {
		collections[i] = *protoToCollectionInfo(c)
	}
	return &transport.ListCollectionsResponse{Collections: collections}, nil
}

// DropCollection removes a collection from a database by name.
func (t *Transport) DropCollection(ctx context.Context, database, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.DropCollectionRequest{
		Database: database,
		Name:     name,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "collection.Drop", payload)
	return err
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
	payload, err := proto.Marshal(&pb.InsertDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Document:   string(docJSON),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.InsertDocument", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.InsertDocumentResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.InsertDocumentResponse{ID: resp.GetId()}, nil
}

// GetDocument returns a single document by its ID.
func (t *Transport) GetDocument(ctx context.Context, database, collection string, id uint64) (*transport.DocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.GetDocumentRequest{
		Database:   database,
		Collection: collection,
		Id:         id,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.GetDocument", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.DocumentResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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
	payload, err := proto.Marshal(&pb.UpdateDocumentRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Id:         req.ID,
		Document:   string(docJSON),
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "collection.UpdateDocument", payload)
	return err
}

// DeleteDocument removes a document from a collection by its ID.
func (t *Transport) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.DeleteDocumentRequest{
		Database:   database,
		Collection: collection,
		Id:         id,
	})
	if err != nil {
		return &marshalError{Err: err}
	}
	_, err = t.call(ctx, "collection.DeleteDocument", payload)
	return err
}

// ListDocuments returns a paginated list of documents in a collection.
func (t *Transport) ListDocuments(ctx context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.ListDocumentsRequest{
		Database:   req.Database,
		Collection: req.Collection,
		PageSize:   req.PageSize,
		PageToken:  req.PageToken,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "collection.ListDocuments", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.ListDocumentsResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
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

// protoToCollectionInfo converts a proto CollectionInfoResponse to a
// transport CollectionInfo.
func protoToCollectionInfo(p *pb.CollectionInfoResponse) *transport.CollectionInfo {
	return &transport.CollectionInfo{
		Name:          p.GetName(),
		Database:      p.GetDatabase(),
		IDStrategy:    p.GetIdStrategy(),
		FieldHash:     p.GetFieldHash(),
		Schema:        p.GetSchema(),
		DocumentCount: p.GetDocumentCount(),
		CreatedAt:     p.GetCreatedAt(),
	}
}
