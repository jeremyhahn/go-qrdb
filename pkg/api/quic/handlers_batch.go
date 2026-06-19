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
