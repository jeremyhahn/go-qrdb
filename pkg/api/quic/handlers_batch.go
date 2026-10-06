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

// handleImportBegin starts a chunked import session. See
// docs/transactions/architecture.md ("Import session").
func (s *Server) handleImportBegin(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.ImportBeginRequest
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

	sessionID, err := batchSvc.ImportBegin(ctx, opts)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ImportBeginResponse{SessionId: sessionID})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleImportAppend stages a chunk of a chunked import session.
func (s *Server) handleImportAppend(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.ImportAppendRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := batchSvc.ImportAppend(ctx, req.GetSessionId(), req.GetSeq(), req.GetChunk()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ImportAppendResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleImportCommit applies every staged chunk of a chunked import
// session atomically.
func (s *Server) handleImportCommit(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.ImportCommitRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := batchSvc.ImportCommit(ctx, req.GetSessionId())
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

	resp, err := proto.Marshal(&pb.ImportCommitResponse{
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

// handleImportAbort discards a chunked import session without applying
// anything.
func (s *Server) handleImportAbort(ctx context.Context, payload []byte) ([]byte, error) {
	batchSvc, svcErr := s.batchSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.ImportAbortRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := batchSvc.ImportAbort(ctx, req.GetSessionId()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ImportAbortResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
