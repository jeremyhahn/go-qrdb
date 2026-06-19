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

import "testing"

// TestPlacement_Leader resolves the leader replica and reports absence.
func TestPlacement_Leader(t *testing.T) {
	p := samplePlacement("acme", "orders", 1000)
	leader, ok := p.Leader()
	if !ok || leader.NodeID != "n1" {
		t.Fatalf("Leader = %+v ok=%v, want n1", leader, ok)
	}

	p.LeaderReplicaID = 99
	if _, ok := p.Leader(); ok {
		t.Fatalf("Leader should be absent for unknown replica ID")
	}
}

// TestPlacement_ReplicaForNode finds a node's replica and reports absence.
func TestPlacement_ReplicaForNode(t *testing.T) {
	p := samplePlacement("acme", "orders", 1000)
	r, ok := p.ReplicaForNode("n2")
	if !ok || r.ReplicaID != 2 || r.DataRoot != "/d/n2" {
		t.Fatalf("ReplicaForNode(n2) = %+v ok=%v, want replica 2", r, ok)
	}
	if _, ok := p.ReplicaForNode("ghost"); ok {
		t.Fatalf("ReplicaForNode(ghost) should report absence")
	}
}

// TestPlacement_Members builds the replica-ID -> address map for StartShard.
func TestPlacement_Members(t *testing.T) {
	p := samplePlacement("acme", "orders", 1000)
	members := p.Members()
	if len(members) != 2 {
		t.Fatalf("Members len = %d, want 2", len(members))
	}
	if members[1] != "n1:1" || members[2] != "n2:1" {
		t.Fatalf("Members = %v, want {1:n1:1, 2:n2:1}", members)
	}
}
