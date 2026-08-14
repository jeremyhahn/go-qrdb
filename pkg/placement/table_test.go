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
	"encoding/json"
	"strings"
	"testing"
)

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

// TestMaxReplicaID_EmptyPlacement verifies a zero result for a placement with
// no replicas, so the first replica assigned gets ID 1.
func TestMaxReplicaID_EmptyPlacement(t *testing.T) {
	p := &Placement{}
	if got := MaxReplicaID(p); got != 0 {
		t.Fatalf("MaxReplicaID(empty) = %d, want 0", got)
	}
}

// TestMaxReplicaID_ReturnsLargest verifies the largest ReplicaID is returned
// regardless of slice order.
func TestMaxReplicaID_ReturnsLargest(t *testing.T) {
	p := &Placement{Replicas: []Replica{
		{ReplicaID: 3},
		{ReplicaID: 1},
		{ReplicaID: 7},
		{ReplicaID: 2},
	}}
	if got := MaxReplicaID(p); got != 7 {
		t.Fatalf("MaxReplicaID = %d, want 7", got)
	}
}

// TestReplicaState_ZeroValueIsActive verifies the Go zero-value-useful
// convention: a Replica built without an explicit State is Active, so every
// placement entry persisted before the State field existed keeps its
// (correct) join=false semantics after upgrade.
func TestReplicaState_ZeroValueIsActive(t *testing.T) {
	var r Replica
	if r.State != ReplicaStateActive {
		t.Fatalf("zero-value Replica.State = %v, want ReplicaStateActive", r.State)
	}
}

// TestReplica_JSON_BackwardCompatNoStateKey verifies that a placement entry
// persisted before the State field existed (no "state" key in the JSON)
// decodes to ReplicaStateActive, not some other zero-adjacent value.
func TestReplica_JSON_BackwardCompatNoStateKey(t *testing.T) {
	const legacyJSON = `{"replica_id":1,"node_id":"n1","raft_addr":"n1:1","data_root":"/d/n1"}`
	var r Replica
	if err := json.Unmarshal([]byte(legacyJSON), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.State != ReplicaStateActive {
		t.Fatalf("legacy-JSON decoded State = %v, want ReplicaStateActive", r.State)
	}
}

// TestReplica_JSON_RoundTripState verifies State round-trips through JSON and
// that ReplicaStateActive is omitted (omitempty) while ReplicaStateObserver
// and ReplicaStateObserverReady are explicit.
func TestReplica_JSON_RoundTripState(t *testing.T) {
	active := Replica{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1", State: ReplicaStateActive}
	buf, err := json.Marshal(active)
	if err != nil {
		t.Fatalf("Marshal(active): %v", err)
	}
	if strings.Contains(string(buf), `"state"`) {
		t.Fatalf("Marshal(active) = %s, want no \"state\" key (omitempty)", buf)
	}

	observer := Replica{ReplicaID: 2, NodeID: "n2", RaftAddr: "n2:1", State: ReplicaStateObserver}
	buf, err = json.Marshal(observer)
	if err != nil {
		t.Fatalf("Marshal(observer): %v", err)
	}
	var decoded Replica
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("Unmarshal(observer): %v", err)
	}
	if decoded.State != ReplicaStateObserver {
		t.Fatalf("round-tripped State = %v, want ReplicaStateObserver", decoded.State)
	}

	observerReady := Replica{ReplicaID: 3, NodeID: "n3", RaftAddr: "n3:1", State: ReplicaStateObserverReady}
	buf, err = json.Marshal(observerReady)
	if err != nil {
		t.Fatalf("Marshal(observerReady): %v", err)
	}
	decoded = Replica{}
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("Unmarshal(observerReady): %v", err)
	}
	if decoded.State != ReplicaStateObserverReady {
		t.Fatalf("round-tripped State = %v, want ReplicaStateObserverReady", decoded.State)
	}
}
