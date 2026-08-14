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
	"bytes"
	"context"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleBatch executes multiple operations atomically within a single MVCC
// revision. All operations succeed or fail together. The per-operation
// results are returned in the same order as the input.
func (s *Server) handleBatch(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.BatchRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	ops := make([]service.Op, len(req.GetOps()))
	for i, op := range req.GetOps() {
		ops[i] = protoToOp(op)
	}

	results, err := batchSvc.Execute(ctx, ops)
	if err != nil {
		return nil, err
	}

	responses := make([]*pb.OpResponse, len(results))
	for i, r := range results {
		responses[i] = opResponseToProto(r)
	}

	resp, err := proto.Marshal(&pb.BatchResponse{Responses: responses})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBatchImport parses and imports key-value pairs from the file carried
// in the request. It resolves the batch service through the SAME
// per-(tenant, database) scope as handleBatch, so the namespaced wrapper's
// KeyPrefix is applied to every imported key exactly as it is for every
// other write to the database. The file must fit within the wire protocol's
// MaxPayloadSize (see wire.MaxPayloadSize), which ReadRequest enforces
// before this handler ever runs.
func (s *Server) handleBatchImport(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.BatchImportRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	opts := service.ImportOptions{
		Format:        req.GetFormat(),
		Filename:      req.GetFilename(),
		KeyField:      req.GetKeyField(),
		ValueMode:     req.GetValueMode(),
		ValueField:    req.GetValueField(),
		SkipHeader:    req.GetSkipHeader(),
		Transactional: req.GetTransactional(),
	}

	result, err := batchSvc.Import(ctx, bytes.NewReader(req.GetFile()), opts)
	if err != nil {
		return nil, err
	}

	errs := make([]*pb.BatchImportError, len(result.Errors))
	for i, e := range result.Errors {
		errs[i] = &pb.BatchImportError{
			Row:     int32(e.Row),
			Key:     e.Key,
			Message: e.Message,
		}
	}

	resp, err := proto.Marshal(&pb.BatchImportResponse{
		Total:     int32(result.Total),
		Succeeded: int32(result.Succeeded),
		Failed:    int32(result.Failed),
		Errors:    errs,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
