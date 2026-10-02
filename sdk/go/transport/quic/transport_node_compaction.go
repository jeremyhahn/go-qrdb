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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/compactionconv"
)

// NodeCompaction returns the platform's node-keyspace compaction policy by
// calling admin.GetNodeCompaction.
func (t *Transport) NodeCompaction(ctx context.Context) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	respPayload, err := t.call(ctx, "admin.GetNodeCompaction", nil)
	if err != nil {
		return nil, err
	}
	return decodeNodeCompaction("NodeCompaction", respPayload)
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy by
// calling admin.SetNodeCompaction.
func (t *Transport) SetNodeCompaction(ctx context.Context, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseCompactionConfig, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.SetNodeCompactionRequest{Compaction: compactionconv.ToProto(policy)})
	if err != nil {
		return nil, &transport.RequestError{Method: "SetNodeCompaction", Err: err}
	}
	respPayload, err := t.call(ctx, "admin.SetNodeCompaction", payload)
	if err != nil {
		return nil, err
	}
	return decodeNodeCompaction("SetNodeCompaction", respPayload)
}

func decodeNodeCompaction(method string, payload []byte) (*transport.DatabaseCompactionConfig, error) {
	var resp pb.NodeCompactionResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		return nil, &transport.RequestError{Method: method, Err: err}
	}
	return compactionconv.FromProto(resp.GetCompaction()), nil
}
