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

package unix

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MovePrefix moves every live key under req.Src to req.Dst in one revision
// with req.Ops, once req.Compares hold and the live count under Src equals
// req.ExpectedKeys, and returns that revision.
func (t *Transport) MovePrefix(ctx context.Context, req *transport.MovePrefixRequest) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}
	if req == nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &transport.NilRequestError{}}
	}
	resp, err := t.client.MovePrefix(ctx, movePrefixToProto(req))
	if err != nil {
		return 0, mapGRPCError("MovePrefix", err)
	}
	return resp.GetRevision(), nil
}

// movePrefixToProto converts with the transaction converters, so an op or
// compare means what it means in a transaction.
func movePrefixToProto(req *transport.MovePrefixRequest) *pb.MovePrefixRequest {
	ops := make([]*pb.Op, len(req.Ops))
	for i, op := range req.Ops {
		ops[i] = opToProto(op)
	}
	compares := make([]*pb.Compare, len(req.Compares))
	for i, c := range req.Compares {
		compares[i] = compareToProto(c)
	}
	return &pb.MovePrefixRequest{
		Src:          req.Src,
		Dst:          req.Dst,
		ExpectedKeys: req.ExpectedKeys,
		Ops:          ops,
		Compares:     compares,
	}
}
