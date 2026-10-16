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

package statehashpb

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestToProto_CarriesEveryField(t *testing.T) {
	got := ToProto(&service.StateHashesResponse{
		NodeID: "node-a",
		Replicas: []service.ReplicaStateHash{
			{TenantID: "acme", ShardID: 3, ReplicaID: 2, AppliedIndex: 40, Records: 7, Hash: "ab"},
			{ShardID: 4, ReplicaID: 1, Error: "replica not loaded"},
		},
	})
	if got.GetNodeId() != "node-a" || len(got.GetReplicas()) != 2 {
		t.Fatalf("response = %v", got)
	}
	r := got.GetReplicas()[0]
	if r.GetTenantId() != "acme" || r.GetShardId() != 3 || r.GetReplicaId() != 2 ||
		r.GetAppliedIndex() != 40 || r.GetRecords() != 7 || r.GetHash() != "ab" || r.GetError() != "" {
		t.Fatalf("replica 0 = %v", r)
	}
	if e := got.GetReplicas()[1]; e.GetError() != "replica not loaded" || e.GetHash() != "" || e.GetTenantId() != "" {
		t.Fatalf("replica 1 = %v", e)
	}
}

func TestToProto_NilAndEmpty(t *testing.T) {
	if got := ToProto(nil); got == nil || got.GetNodeId() != "" || len(got.GetReplicas()) != 0 {
		t.Fatalf("ToProto(nil) = %v, want an empty response", got)
	}
	got := ToProto(&service.StateHashesResponse{NodeID: "n"})
	if got.GetNodeId() != "n" || len(got.GetReplicas()) != 0 {
		t.Fatalf("ToProto(no replicas) = %v", got)
	}
}
