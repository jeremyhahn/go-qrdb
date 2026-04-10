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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Collection handlers
// ---------------------------------------------------------------------------

// CreateCollection creates a new document collection in a database.
func (s *qrdbServiceServer) CreateCollection(ctx context.Context, req *pb.CreateCollectionRequest) (*pb.CollectionInfoResponse, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	info, err := s.collectionSvc.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database:   req.GetDatabase(),
		Name:       req.GetName(),
		IDStrategy: req.GetIdStrategy(),
		FieldHash:  req.GetFieldHash(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return collectionInfoToProto(info), nil
}

// GetCollectionInfo returns metadata about a specific collection.
func (s *qrdbServiceServer) GetCollectionInfo(ctx context.Context, req *pb.GetCollectionInfoRequest) (*pb.CollectionInfoResponse, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	info, err := s.collectionSvc.GetCollectionInfo(ctx, req.GetDatabase(), req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return collectionInfoToProto(info), nil
}

// ListCollections returns all collections within a database.
func (s *qrdbServiceServer) ListCollections(ctx context.Context, req *pb.ListCollectionsRequest) (*pb.ListCollectionsResponse, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	resp, err := s.collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
		Database: req.GetDatabase(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	pbCollections := make([]*pb.CollectionInfoResponse, len(resp.Collections))
	for i, c := range resp.Collections {
		ci := c
		pbCollections[i] = collectionInfoToProto(&ci)
	}

	return &pb.ListCollectionsResponse{Collections: pbCollections}, nil
}

// DropCollection removes a collection and all its documents.
func (s *qrdbServiceServer) DropCollection(ctx context.Context, req *pb.DropCollectionRequest) (*emptypb.Empty, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	if err := s.collectionSvc.DropCollection(ctx, &service.DropCollectionRequest{
		Database: req.GetDatabase(),
		Name:     req.GetName(),
	}); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// InsertDocument inserts a document into a collection.
func (s *qrdbServiceServer) InsertDocument(ctx context.Context, req *pb.InsertDocumentRequest) (*pb.InsertDocumentResponse, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, status.Error(codes.InvalidArgument, (&DocumentUnmarshalError{Err: err}).Error())
	}

	resp, err := s.collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
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
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	resp, err := s.collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	docJSON, err := json.Marshal(resp.Document)
	if err != nil {
		return nil, status.Error(codes.Internal, (&DocumentMarshalError{Err: err}).Error())
	}

	return &pb.DocumentResponse{
		Id:       resp.ID,
		Document: string(docJSON),
	}, nil
}

// UpdateDocument replaces an existing document in a collection.
func (s *qrdbServiceServer) UpdateDocument(ctx context.Context, req *pb.UpdateDocumentRequest) (*emptypb.Empty, error) {
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, status.Error(codes.InvalidArgument, (&DocumentUnmarshalError{Err: err}).Error())
	}

	if _, err := s.collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
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
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	if _, err := s.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
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
	if s.collectionSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "collection"}).Error())
	}

	resp, err := s.collectionSvc.ListDocuments(ctx, &service.ListDocumentsRequest{
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
			return nil, status.Error(codes.Internal, (&DocumentMarshalError{Err: marshalErr}).Error())
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

// collectionInfoToProto converts a service CollectionInfo to its protobuf
// representation.
func collectionInfoToProto(info *service.CollectionInfo) *pb.CollectionInfoResponse {
	return &pb.CollectionInfoResponse{
		Name:          info.Name,
		Database:      info.Database,
		IdStrategy:    info.IDStrategy,
		FieldHash:     info.FieldHash,
		Schema:        info.Schema,
		DocumentCount: info.DocumentCount,
		CreatedAt:     info.CreatedAt.Format(time.RFC3339Nano),
	}
}

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
