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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/collectionpb"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Collection handlers
// ---------------------------------------------------------------------------

// CreateCollection creates a new document collection in a database.
func (s *qrdbServiceServer) CreateCollection(ctx context.Context, req *pb.CreateCollectionRequest) (*pb.CollectionInfoResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	info, err := collectionSvc.CreateCollection(ctx, collectionpb.CreateRequestFromProto(req))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return collectionpb.InfoToProto(info), nil
}

// GetCollectionInfo returns metadata about a specific collection.
func (s *qrdbServiceServer) GetCollectionInfo(ctx context.Context, req *pb.GetCollectionInfoRequest) (*pb.CollectionInfoResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	info, err := collectionSvc.GetCollectionInfo(ctx, req.GetDatabase(), req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return collectionpb.InfoToProto(info), nil
}

// ListCollections returns all collections within a database.
func (s *qrdbServiceServer) ListCollections(ctx context.Context, req *pb.ListCollectionsRequest) (*pb.ListCollectionsResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	resp, err := collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
		Database: req.GetDatabase(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbCollections := make([]*pb.CollectionInfoResponse, len(resp.Collections))
	for i, c := range resp.Collections {
		ci := c
		pbCollections[i] = collectionpb.InfoToProto(&ci)
	}

	return &pb.ListCollectionsResponse{Collections: pbCollections}, nil
}

// DropCollection removes a collection and all its documents.
func (s *qrdbServiceServer) DropCollection(ctx context.Context, req *pb.DropCollectionRequest) (*emptypb.Empty, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	if err := collectionSvc.DropCollection(ctx, &service.DropCollectionRequest{
		Database: req.GetDatabase(),
		Name:     req.GetName(),
	}); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// InsertDocument inserts a document into a collection.
func (s *qrdbServiceServer) InsertDocument(ctx context.Context, req *pb.InsertDocumentRequest) (*pb.InsertDocumentResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, mapServiceError(&DocumentUnmarshalError{Err: err})
	}

	resp, err := collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		Document:   doc,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.InsertDocumentResponse{Id: resp.ID}, nil
}

// GetDocument retrieves a single document by ID.
func (s *qrdbServiceServer) GetDocument(ctx context.Context, req *pb.GetDocumentRequest) (*pb.DocumentResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	resp, err := collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	docJSON, err := json.Marshal(resp.Document)
	if err != nil {
		return nil, mapServiceError(&DocumentMarshalError{Err: err})
	}

	return &pb.DocumentResponse{
		Id:       resp.ID,
		Document: string(docJSON),
	}, nil
}

// UpdateDocument replaces an existing document in a collection.
func (s *qrdbServiceServer) UpdateDocument(ctx context.Context, req *pb.UpdateDocumentRequest) (*emptypb.Empty, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, mapServiceError(&DocumentUnmarshalError{Err: err})
	}

	if _, err := collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
		Document:   doc,
	}); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// DeleteDocument removes a document from a collection by ID.
func (s *qrdbServiceServer) DeleteDocument(ctx context.Context, req *pb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	if _, err := collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
	}); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListDocuments returns a paginated list of documents in a collection.
func (s *qrdbServiceServer) ListDocuments(ctx context.Context, req *pb.ListDocumentsRequest) (*pb.ListDocumentsResponse, error) {
	collectionSvc, svcErr := s.collectionSvcFor(ctx, req.GetDatabase())
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}

	resp, err := collectionSvc.ListDocuments(ctx, &service.ListDocumentsRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbDocs := make([]*pb.DocumentResponse, len(resp.Documents))
	for i, d := range resp.Documents {
		docJSON, marshalErr := json.Marshal(d.Document)
		if marshalErr != nil {
			return nil, mapServiceError(&DocumentMarshalError{Err: marshalErr})
		}
		pbDocs[i] = &pb.DocumentResponse{
			Id:       d.ID,
			Document: string(docJSON),
		}
	}

	return &pb.ListDocumentsResponse{
		Documents:     pbDocs,
		NextPageToken: resp.NextPageToken,
		TotalCount:    resp.TotalCount,
	}, nil
}

// ---------------------------------------------------------------------------
// Collection type conversion helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Collection error types
// ---------------------------------------------------------------------------

// DocumentUnmarshalError is returned when a JSON document from a gRPC
// request cannot be deserialized.
type DocumentUnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DocumentUnmarshalError) Error() string {
	return "grpc: failed to unmarshal document: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DocumentUnmarshalError) Unwrap() error {
	return e.Err
}

// DocumentMarshalError is returned when a document cannot be serialized
// to JSON for a gRPC response.
type DocumentMarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DocumentMarshalError) Error() string {
	return "grpc: failed to marshal document: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DocumentMarshalError) Unwrap() error {
	return e.Err
}
