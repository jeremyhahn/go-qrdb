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

// Package replicaconv converts a node's replica states from the protobuf
// message to the transport-neutral type. The gRPC, Unix and QUIC transports
// receive the same protobuf message, so the mapping lives here once. It is
// internal: only the transport implementations may import it.
//
// The package reads the message through its generated getters rather than
// importing the proto package: the QUIC transport is compiled into a lean
// standalone embedder (through the embedded transport's replica forwarding),
// and a converter that names the proto types would add an import edge from
// that graph into pkg/api (test/buildgates/lean_standalone_test.go).
package replicaconv

import (
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReplicaStateMessage is the read side of one replica-state record, the
// getters protoc generates for ReplicaStateProto.
type ReplicaStateMessage interface {
	GetShardId() uint64
	GetReplicaId() uint64
	GetAppliedIndex() uint64
	GetHash() uint64
}

// ReplicaStatesMessage is the read side of a replica-states response, the
// getters protoc generates for ReplicaStatesResponse.
type ReplicaStatesMessage[R ReplicaStateMessage] interface {
	GetNodeId() string
	GetReplicas() []R
}

// FromProto converts a ReplicaStatesResponse. Replicas is never nil, so a
// node holding no replicas reads as an empty list rather than a missing one.
func FromProto[R ReplicaStateMessage](resp ReplicaStatesMessage[R]) *transport.ReplicaStatesResponse {
	replicas := make([]transport.ReplicaState, 0, len(resp.GetReplicas()))
	for _, rs := range resp.GetReplicas() {
		replicas = append(replicas, transport.ReplicaState{
			ShardID:      rs.GetShardId(),
			ReplicaID:    rs.GetReplicaId(),
			AppliedIndex: rs.GetAppliedIndex(),
			Hash:         rs.GetHash(),
		})
	}
	return &transport.ReplicaStatesResponse{NodeID: resp.GetNodeId(), Replicas: replicas}
}
