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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// MovePrefix moves every live key under req.Src to the same suffix under
// req.Dst in one revision, with req.Ops in the same revision, once
// req.Compares hold and the live count under Src equals req.ExpectedKeys
// (go-qrdb#926: the SQLite page store publishes a staged commit with it).
// A scoped MVCC service that cannot move a prefix answers
// *service.PrefixMoveUnsupportedError, Unimplemented on the wire.
func (s *qrdbServiceServer) MovePrefix(ctx context.Context, req *pb.MovePrefixRequest) (*pb.MovePrefixResponse, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, mapServiceError(svcErr)
	}
	mover, ok := mvccSvc.(service.PrefixMover)
	if !ok {
		return nil, mapServiceError(&service.PrefixMoveUnsupportedError{})
	}
	rev, err := mover.MovePrefix(ctx, protoToMovePrefixRequest(req))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.MovePrefixResponse{Revision: rev}, nil
}

// protoToMovePrefixRequest converts the wire request with the transaction
// converters, so an op or compare means what it means in a transaction.
func protoToMovePrefixRequest(req *pb.MovePrefixRequest) *service.MovePrefixRequest {
	ops := make([]service.Op, len(req.GetOps()))
	for i, op := range req.GetOps() {
		ops[i] = protoToOp(op)
	}
	compares := make([]service.Compare, len(req.GetCompares()))
	for i, c := range req.GetCompares() {
		compares[i] = protoToCompare(c)
	}
	return &service.MovePrefixRequest{
		Src:          string(req.GetSrc()),
		Dst:          string(req.GetDst()),
		ExpectedKeys: req.GetExpectedKeys(),
		Ops:          ops,
		Compares:     compares,
	}
}
