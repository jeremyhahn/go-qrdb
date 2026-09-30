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

package placement

import (
	"context"
	"errors"
	"testing"
)

// refuseAddr is a replica filter refusing one address.
func refuseAddr(addr string) ReplicaFilter {
	return func(_ uint64, raftAddr string) error {
		if raftAddr == addr {
			return errors.New("does not advertise the group's features")
		}
		return nil
	}
}

// A node the filter refuses is never chosen, even when it is the least
// loaded, for a new group or an additional replica of an existing one; and
// when too few nodes remain the request fails rather than using it.
func TestScheduler_ReplicaFilterExcludesRefusedNodes(t *testing.T) {
	nodes := []NodeLoad{
		{NodeID: "old", RaftAddr: "old:1", Bytes: 1, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 50, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 60, Healthy: true},
	}
	s, err := NewScheduler(&mockTopology{nodes: nodes}, WithReplicaFilter(refuseAddr("old:1")))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	p, err := s.Place(context.Background(), baseRequest(2))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	for _, r := range p.Replicas {
		if r.NodeID == "old" {
			t.Fatalf("Place chose the refused node: %+v", p.Replicas)
		}
	}

	var short *InsufficientNodesError
	if _, err := s.Place(context.Background(), baseRequest(3)); !errors.As(err, &short) {
		t.Fatalf("Place(RF=3) = %v, want *InsufficientNodesError with the refused node excluded", err)
	}

	one := &Placement{TenantID: "acme", Database: "orders", ShardID: 1000, RF: 1,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n2", RaftAddr: "n2:1"}}}
	more, err := s.PlaceAdditional(context.Background(), one, 1)
	if err != nil {
		t.Fatalf("PlaceAdditional: %v", err)
	}
	if got := more.Replicas[len(more.Replicas)-1].NodeID; got != "n3" {
		t.Fatalf("PlaceAdditional chose %q, want n3 (old is refused)", got)
	}
	if _, err := s.PlaceAdditional(context.Background(), more, 1); !errors.As(err, &short) {
		t.Fatalf("PlaceAdditional = %v, want *InsufficientNodesError: only the refused node remains", err)
	}
}
