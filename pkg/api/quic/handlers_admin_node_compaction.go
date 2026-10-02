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
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/compactionpb"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

// handleGetNodeCompaction returns the platform's node-keyspace compaction
// policy as a NodeCompactionResponse. The method requires
// admin:node_compaction (ScopeCross: system tenant only).
func (s *Server) handleGetNodeCompaction(ctx context.Context, _ []byte) ([]byte, error) {
	policy, err := s.adminSvc.NodeCompaction(ctx)
	if err != nil {
		return nil, err
	}
	return marshalNodeCompaction(policy)
}

// handleSetNodeCompaction unmarshals a SetNodeCompactionRequest, sets the
// platform's node-keyspace compaction policy -- an unset compaction turns it
// off -- and answers with the policy now in force. The method requires
// admin:node_compaction (ScopeCross: system tenant only).
func (s *Server) handleSetNodeCompaction(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.SetNodeCompactionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	policy := compactionpb.FromProto(req.GetCompaction())
	if err := s.adminSvc.SetNodeCompaction(ctx, policy); err != nil {
		return nil, err
	}
	return marshalNodeCompaction(policy.Normalized())
}

func marshalNodeCompaction(policy *compaction.Policy) ([]byte, error) {
	resp, err := proto.Marshal(&pb.NodeCompactionResponse{Compaction: compactionpb.ToProto(policy)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
