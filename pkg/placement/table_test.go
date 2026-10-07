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

// TestPlacement_IsLocalBinding_ZeroValueIsRaft proves a placement entry
// persisted before Backend existed -- the zero value, no "backend" key --
// decodes as a dedicated Raft group, not a local binding.
func TestPlacement_IsLocalBinding_ZeroValueIsRaft(t *testing.T) {
	var p Placement
	if p.IsLocalBinding() {
		t.Fatalf("zero-value Placement.IsLocalBinding() = true, want false")
	}
}

// TestPlacement_IsLocalBinding_ExplicitRaftIsNotLocal proves a placement
// that names BackendRaft explicitly is treated identically to the zero
// value.
func TestPlacement_IsLocalBinding_ExplicitRaftIsNotLocal(t *testing.T) {
	p := Placement{Backend: BackendRaft}
	if p.IsLocalBinding() {
		t.Fatalf("Placement{Backend: BackendRaft}.IsLocalBinding() = true, want false")
	}
}

// TestPlacement_IsLocalBinding_NonRaftBackends proves every non-Raft backend
// is a local binding.
func TestPlacement_IsLocalBinding_NonRaftBackends(t *testing.T) {
	for _, backend := range []Backend{BackendMemory, BackendFile, BackendPebble, BackendObjstore} {
		p := Placement{Backend: backend}
		if !p.IsLocalBinding() {
			t.Fatalf("Placement{Backend: %v}.IsLocalBinding() = false, want true", backend)
		}
	}
}

// TestPlacement_IsLocalBinding_JSON_BackwardCompatNoBackendKey proves a
// legacy placement entry with no "backend" key decodes as non-local, the
// same guarantee TestReplica_JSON_BackwardCompatNoStateKey gives State.
func TestPlacement_IsLocalBinding_JSON_BackwardCompatNoBackendKey(t *testing.T) {
	const legacyJSON = `{"tenant_id":"acme","database":"orders","shard_id":1000,"rf":1,"leader_replica_id":1,"replicas":[]}`
	var p Placement
	if err := json.Unmarshal([]byte(legacyJSON), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.IsLocalBinding() {
		t.Fatalf("legacy-JSON decoded IsLocalBinding() = true, want false")
	}
}

// TestPlacement_RefusalForNode_AssignedNodeIsNotRefused reports that a node
// holding a replica gets no refusal.
func TestPlacement_RefusalForNode_AssignedNodeIsNotRefused(t *testing.T) {
	p := samplePlacement("acme", "orders", 1000)
	if refusal, refused := p.RefusalForNode("n2"); refused {
		t.Fatalf("RefusalForNode(n2) = %+v, want no refusal", refusal)
	}
}

// TestPlacement_RefusalForNode_UnassignedNodeNamesTheReplicaSet refuses a
// node the placement does not list, naming the replicas and leader hint a
// forward resolves from.
func TestPlacement_RefusalForNode_UnassignedNodeNamesTheReplicaSet(t *testing.T) {
	p := samplePlacement("acme", "orders", 1000)
	refusal, refused := p.RefusalForNode("n3")
	if !refused {
		t.Fatalf("RefusalForNode(n3) should refuse")
	}
	if refusal.TenantID != "acme" || refusal.Database != "orders" || refusal.ShardID != 1000 {
		t.Fatalf("RefusalForNode(n3) = %+v, want tenant/database/shard from p", refusal)
	}
	if refusal.NodeID != "n3" {
		t.Fatalf("RefusalForNode(n3).NodeID = %q, want n3", refusal.NodeID)
	}
	if len(refusal.Replicas) != len(p.Replicas) || refusal.LeaderReplicaID != p.LeaderReplicaID {
		t.Fatalf("RefusalForNode(n3) = %+v, want p's replica set and leader hint", refusal)
	}
}

// TestPlacement_RefusalForNode_LocalBinding refuses a non-owner node of a
// local binding (RF 1, a single Replica) exactly as it would a dedicated
// Raft group's non-replica node: the existing forwarding machinery is keyed
// only on ShardID and Replicas, not on Backend.
func TestPlacement_RefusalForNode_LocalBinding(t *testing.T) {
	p := &Placement{
		TenantID: "acme", Database: "cache", ShardID: 1000, RF: 1,
		LeaderReplicaID: 1, Backend: BackendMemory,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1"}},
	}
	if !p.IsLocalBinding() {
		t.Fatalf("sample local-binding placement should report IsLocalBinding")
	}
	if refusal, refused := p.RefusalForNode("n1"); refused {
		t.Fatalf("RefusalForNode(n1) = %+v, want the owner served", refusal)
	}
	refusal, refused := p.RefusalForNode("n2")
	if !refused {
		t.Fatalf("RefusalForNode(n2) should refuse the non-owner node")
	}
	if refusal.NodeID != "n2" || len(refusal.Replicas) != 1 || refusal.Replicas[0].NodeID != "n1" {
		t.Fatalf("RefusalForNode(n2) = %+v, want the owner n1 named as the sole replica", refusal)
	}
	if refusal.LeaderReplicaID != 1 {
		t.Fatalf("RefusalForNode(n2).LeaderReplicaID = %d, want 1", refusal.LeaderReplicaID)
	}
}
