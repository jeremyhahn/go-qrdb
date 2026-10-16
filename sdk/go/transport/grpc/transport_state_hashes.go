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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/statehashconv"
)

var _ transport.ReplicaStateHasher = (*Transport)(nil)

// StateHashes returns the applied index and state digest of every replica
// the connected node runs whose shard is in shardIDs (every shard when
// empty), by calling the GetStateHashes gRPC method.
func (t *Transport) StateHashes(ctx context.Context, shardIDs []uint64) (*transport.StateHashesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.GetStateHashes(ctx, &pb.GetStateHashesRequest{ShardIds: shardIDs})
	if err != nil {
		return nil, mapGRPCError("StateHashes", err)
	}
	return statehashconv.FromProto(resp), nil
}
