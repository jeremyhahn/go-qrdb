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

// Package statehashconv converts replica state hashes from the protobuf
// message to the transport-neutral type. The gRPC, Unix and QUIC transports
// receive the same protobuf message, so the mapping lives here once. It is
// internal: only the transport implementations may import it.
package statehashconv

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// FromProto converts a protobuf response, carrying every field. A nil
// response converts to an empty one rather than nil, so a caller never has
// to tell "no replicas" from "no answer".
func FromProto(r *pb.GetStateHashesResponse) *transport.StateHashesResponse {
	out := &transport.StateHashesResponse{
		NodeID:   r.GetNodeId(),
		Replicas: make([]transport.ReplicaStateHash, 0, len(r.GetReplicas())),
	}
	for _, rep := range r.GetReplicas() {
		out.Replicas = append(out.Replicas, transport.ReplicaStateHash{
			TenantID:     rep.GetTenantId(),
			ShardID:      rep.GetShardId(),
			ReplicaID:    rep.GetReplicaId(),
			AppliedIndex: rep.GetAppliedIndex(),
			Records:      rep.GetRecords(),
			Hash:         rep.GetHash(),
			Error:        rep.GetError(),
		})
	}
	return out
}
