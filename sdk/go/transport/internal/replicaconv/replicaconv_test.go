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

package replicaconv

import (
	"reflect"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The generated messages must keep satisfying the getter interfaces the
// converter reads them through.
var (
	_ ReplicaStateMessage                         = (*pb.ReplicaStateProto)(nil)
	_ ReplicaStatesMessage[*pb.ReplicaStateProto] = (*pb.ReplicaStatesResponse)(nil)
)

func TestFromProto_EveryField(t *testing.T) {
	got := FromProto(&pb.ReplicaStatesResponse{NodeId: "node-2", Replicas: []*pb.ReplicaStateProto{
		{ShardId: 1, ReplicaId: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
		{ShardId: 7, ReplicaId: 3, AppliedIndex: 9, Hash: 22},
	}})
	want := &transport.ReplicaStatesResponse{NodeID: "node-2", Replicas: []transport.ReplicaState{
		{ShardID: 1, ReplicaID: 2, AppliedIndex: 48211, Hash: 1<<63 + 5},
		{ShardID: 7, ReplicaID: 3, AppliedIndex: 9, Hash: 22},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromProto = %+v, want %+v", got, want)
	}
}

func TestFromProto_NoReplicasIsEmptyNotNil(t *testing.T) {
	for name, resp := range map[string]*pb.ReplicaStatesResponse{
		"empty message": {NodeId: "node-1"},
		"nil message":   nil,
	} {
		got := FromProto(resp)
		if got.Replicas == nil || len(got.Replicas) != 0 {
			t.Fatalf("%s: Replicas = %#v, want empty and non-nil", name, got.Replicas)
		}
	}
}
