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
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/statehashpb"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleGetStateHashes unmarshals a GetStateHashesRequest and answers with
// the applied index and state digest of every selected replica this node
// runs, as a GetStateHashesResponse. The method requires admin:state_hash
// (ScopeCross: system tenant only).
func (s *Server) handleGetStateHashes(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.GetStateHashesRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	hasher, ok := s.adminSvc.(service.ReplicaStateHasher)
	if !ok {
		return nil, &service.ReplicaStateHashesUnsupportedError{}
	}
	hashes, err := hasher.StateHashes(ctx, req.GetShardIds())
	if err != nil {
		return nil, err
	}
	resp, err := proto.Marshal(statehashpb.ToProto(hashes))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
