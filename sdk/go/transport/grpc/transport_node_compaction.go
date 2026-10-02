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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/compactionconv"
)

// NodeCompaction returns the platform's node-keyspace compaction policy by
// calling the GetNodeCompaction gRPC method.
func (t *Transport) NodeCompaction(ctx context.Context) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetNodeCompaction(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("NodeCompaction", err)
	}
	return compactionconv.FromProto(resp.GetCompaction()), nil
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy by
// calling the SetNodeCompaction gRPC method.
func (t *Transport) SetNodeCompaction(ctx context.Context, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.SetNodeCompaction(ctx, &pb.SetNodeCompactionRequest{Compaction: compactionconv.ToProto(policy)})
	if err != nil {
		return nil, mapGRPCError("SetNodeCompaction", err)
	}
	return compactionconv.FromProto(resp.GetCompaction()), nil
}
