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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// handleIndexCreate unmarshals an IndexCreateRequest, creates a new
// secondary index with the specified name, field, and configuration
// via the index service, and returns an empty response payload.
func (s *Server) handleIndexCreate(ctx context.Context, payload []byte) ([]byte, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.IndexCreateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	cfg := index.IndexConfig{
		Unique: req.GetUnique(),
		Sparse: req.GetSparse(),
	}
	if err := indexSvc.CreateIndex(ctx, req.GetName(), req.GetFieldName(), cfg); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleIndexDrop unmarshals an IndexDropRequest, removes the specified
// secondary index, and returns an empty response payload.
func (s *Server) handleIndexDrop(ctx context.Context, payload []byte) ([]byte, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.IndexDropRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := indexSvc.DropIndex(ctx, req.GetName()); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleIndexQuery unmarshals an IndexQueryRequest, queries the specified
// index for entries matching the exact value, and returns a serialized
// IndexQueryResponse containing the matching entry IDs.
func (s *Server) handleIndexQuery(ctx context.Context, payload []byte) ([]byte, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.IndexQueryRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	keys, err := indexSvc.Query(ctx, req.GetIndexName(), req.GetValue())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.IndexQueryResponse{Keys: keys})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleIndexScan unmarshals an IndexScanRequest, scans the specified
// index for entries within the value range [start, end], and returns a
// serialized IndexScanResponse with grouped entries.
func (s *Server) handleIndexScan(ctx context.Context, payload []byte) ([]byte, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.IndexScanRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := indexSvc.Scan(ctx, req.GetIndexName(), req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, err
	}

	entries := make([]*pb.IndexScanEntry, 0, len(result))
	for value, keys := range result {
		entries = append(entries, &pb.IndexScanEntry{
			Value: value,
			Keys:  keys,
		})
	}

	resp, err := proto.Marshal(&pb.IndexScanResponse{Entries: entries})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleIndexList lists all registered indexes and returns a serialized
// IndexListResponse containing the index names.
func (s *Server) handleIndexList(ctx context.Context, _ []byte) ([]byte, error) {
	indexSvc, svcErr := s.indexSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	names, err := indexSvc.ListIndexes(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.IndexListResponse{Names: names})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
