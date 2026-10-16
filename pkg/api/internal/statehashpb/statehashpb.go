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

// Package statehashpb converts a node's replica state hashes from the service
// type to the protobuf message the gRPC, Unix and QUIC transports carry, so
// those transports cannot drift apart on the mapping.
package statehashpb

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ToProto converts a StateHashesResponse to its GetStateHashesResponse; nil
// converts to an empty response.
func ToProto(r *service.StateHashesResponse) *pb.GetStateHashesResponse {
	if r == nil {
		return &pb.GetStateHashesResponse{}
	}
	out := &pb.GetStateHashesResponse{
		NodeId:   r.NodeID,
		Replicas: make([]*pb.ReplicaStateHash, 0, len(r.Replicas)),
	}
	for _, rep := range r.Replicas {
		out.Replicas = append(out.Replicas, &pb.ReplicaStateHash{
			TenantId:     rep.TenantID,
			ShardId:      rep.ShardID,
			ReplicaId:    rep.ReplicaID,
			AppliedIndex: rep.AppliedIndex,
			Records:      rep.Records,
			Hash:         rep.Hash,
			Error:        rep.Error,
		})
	}
	return out
}
