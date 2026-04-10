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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleCollectionCreate creates a new document collection.
func (s *Server) handleCollectionCreate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.CreateCollectionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	info, err := s.collectionSvc.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database:   req.GetDatabase(),
		Name:       req.GetName(),
		IDStrategy: req.GetIdStrategy(),
		FieldHash:  req.GetFieldHash(),
	})
	if err != nil {
		return nil, err
	}
	resp, err := proto.Marshal(collectionInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCollectionGetInfo returns metadata about a specific collection.
func (s *Server) handleCollectionGetInfo(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.GetCollectionInfoRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	info, err := s.collectionSvc.GetCollectionInfo(ctx, req.GetDatabase(), req.GetName())
	if err != nil {
		return nil, err
	}
	resp, err := proto.Marshal(collectionInfoToProto(info))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCollectionList returns all collections within a database.
func (s *Server) handleCollectionList(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.ListCollectionsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	svcResp, err := s.collectionSvc.ListCollections(ctx, &service.ListCollectionsRequest{
		Database: req.GetDatabase(),
	})
	if err != nil {
		return nil, err
	}
	pbCollections := make([]*pb.CollectionInfoResponse, len(svcResp.Collections))
	for i, c := range svcResp.Collections {
		ci := c
		pbCollections[i] = collectionInfoToProto(&ci)
	}
	resp, err := proto.Marshal(&pb.ListCollectionsResponse{Collections: pbCollections})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCollectionDrop removes a collection and all its documents.
func (s *Server) handleCollectionDrop(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.DropCollectionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.collectionSvc.DropCollection(ctx, &service.DropCollectionRequest{
		Database: req.GetDatabase(),
		Name:     req.GetName(),
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// handleCollectionInsertDocument inserts a document into a collection.
func (s *Server) handleCollectionInsertDocument(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.InsertDocumentRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, &InvalidArgumentError{Reason: "invalid document JSON: " + err.Error()}
	}
	svcResp, err := s.collectionSvc.InsertDocument(ctx, &service.InsertDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		Document:   doc,
	})
	if err != nil {
		return nil, err
	}
	resp, err := proto.Marshal(&pb.InsertDocumentResponse{Id: svcResp.ID})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCollectionGetDocument retrieves a single document by ID.
func (s *Server) handleCollectionGetDocument(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.GetDocumentRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	svcResp, err := s.collectionSvc.GetDocument(ctx, &service.GetDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
	})
	if err != nil {
		return nil, err
	}
	docJSON, err := json.Marshal(svcResp.Document)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	resp, err := proto.Marshal(&pb.DocumentResponse{
		Id:       svcResp.ID,
		Document: string(docJSON),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCollectionUpdateDocument replaces an existing document.
func (s *Server) handleCollectionUpdateDocument(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.UpdateDocumentRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(req.GetDocument()), &doc); err != nil {
		return nil, &InvalidArgumentError{Reason: "invalid document JSON: " + err.Error()}
	}
	if _, err := s.collectionSvc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
		Document:   doc,
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// handleCollectionDeleteDocument removes a document by ID.
func (s *Server) handleCollectionDeleteDocument(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.DeleteDocumentRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if _, err := s.collectionSvc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		ID:         req.GetId(),
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// handleCollectionListDocuments returns a paginated list of documents.
func (s *Server) handleCollectionListDocuments(ctx context.Context, payload []byte) ([]byte, error) {
	if s.collectionSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "collection"}
	}
	var req pb.ListDocumentsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	svcResp, err := s.collectionSvc.ListDocuments(ctx, &service.ListDocumentsRequest{
		Database:   req.GetDatabase(),
		Collection: req.GetCollection(),
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	pbDocs := make([]*pb.DocumentResponse, len(svcResp.Documents))
	for i, d := range svcResp.Documents {
		docJSON, marshalErr := json.Marshal(d.Document)
		if marshalErr != nil {
			return nil, &MarshalError{Err: marshalErr}
		}
		pbDocs[i] = &pb.DocumentResponse{
			Id:       d.ID,
			Document: string(docJSON),
		}
	}
	resp, err := proto.Marshal(&pb.ListDocumentsResponse{
		Documents:     pbDocs,
		NextPageToken: svcResp.NextPageToken,
		TotalCount:    svcResp.TotalCount,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
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
