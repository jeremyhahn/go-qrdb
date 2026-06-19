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

// handleTransaction executes an atomic compare-and-swap transaction. It
// evaluates a set of preconditions (compares) and atomically applies the
// success operations when all pass, or the failure operations when any fail.
func (s *Server) handleTransaction(ctx context.Context, payload []byte) ([]byte, error) {
	txSvc, svcErr := s.txSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.TransactionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	compares := make([]service.Compare, len(req.GetCompares()))
	for i, c := range req.GetCompares() {
		compares[i] = protoToCompare(c)
	}

	successOps := make([]service.Op, len(req.GetSuccessOps()))
	for i, op := range req.GetSuccessOps() {
		successOps[i] = protoToOp(op)
	}

	failureOps := make([]service.Op, len(req.GetFailureOps()))
	for i, op := range req.GetFailureOps() {
		failureOps[i] = protoToOp(op)
	}

	result, err := txSvc.Execute(ctx, compares, successOps, failureOps)
	if err != nil {
		return nil, err
	}

	responses := make([]*pb.OpResponse, len(result.Responses))
	for i, r := range result.Responses {
		responses[i] = opResponseToProto(r)
	}

	resp, err := proto.Marshal(&pb.TransactionResponse{
		Succeeded: result.Succeeded,
		Revision:  result.Revision,
		Responses: responses,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
