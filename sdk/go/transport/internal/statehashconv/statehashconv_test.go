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

package statehashconv

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestFromProto_CarriesEveryField(t *testing.T) {
	got := FromProto(&pb.GetStateHashesResponse{
		NodeId: "node-a",
		Replicas: []*pb.ReplicaStateHash{
			{TenantId: "acme", ShardId: 3, ReplicaId: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
			{ShardId: 5, ReplicaId: 2, Error: "not started"},
		},
	})
	want := transport.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []transport.ReplicaStateHash{
			{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 9, Records: 4, Hash: "ab"},
			{ShardID: 5, ReplicaID: 2, Error: "not started"},
		},
	}
	if got.NodeID != want.NodeID || len(got.Replicas) != len(want.Replicas) {
		t.Fatalf("FromProto = %+v, want %+v", got, want)
	}
	for i := range want.Replicas {
		if got.Replicas[i] != want.Replicas[i] {
			t.Fatalf("replica %d = %+v, want %+v", i, got.Replicas[i], want.Replicas[i])
		}
	}
}

func TestFromProto_NilAndEmpty(t *testing.T) {
	for name, in := range map[string]*pb.GetStateHashesResponse{
		"nil":   nil,
		"empty": {},
	} {
		got := FromProto(in)
		if got == nil || got.NodeID != "" || got.Replicas == nil || len(got.Replicas) != 0 {
			t.Fatalf("%s: FromProto = %+v, want an empty non-nil response", name, got)
		}
	}
}
