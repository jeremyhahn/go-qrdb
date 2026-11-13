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

// mockTopology is a static TopologyProvider for scheduler tests.
type mockTopology struct {
	nodes []NodeLoad
}

func (m *mockTopology) Nodes() []NodeLoad { return m.nodes }

// newScheduler builds a scheduler over the given nodes, failing the test on a
// construction error.
func newScheduler(t *testing.T, nodes []NodeLoad) *Scheduler {
	t.Helper()
	s, err := NewScheduler(&mockTopology{nodes: nodes})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	return s
}

// baseRequest returns a valid PlaceRequest with the given RF.
func baseRequest(rf int) PlaceRequest {
	return PlaceRequest{
		TenantID:   "acme",
		DatabaseID: "orders",
		Database:   "orders",
		ShardID:    1000,
		RF:         rf,
	}
}

func TestNewScheduler_NilTopology(t *testing.T) {
	if _, err := NewScheduler(nil); !errors.Is(err, ErrNilTopology) {
		t.Fatalf("NewScheduler(nil) error = %v, want ErrNilTopology", err)
	}
}

// TestPlace_PicksDistinctLeastLoaded verifies the scheduler selects the RF
// least-loaded distinct nodes and assigns one replica per node.
func TestPlace_PicksDistinctLeastLoaded(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 100, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 10, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 50, Healthy: true},
	})
	p, err := s.Place(context.Background(), baseRequest(2))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.RF != 2 || len(p.Replicas) != 2 {
		t.Fatalf("RF=%d replicas=%d, want 2/2", p.RF, len(p.Replicas))
	}
	// The two least-loaded are n2 (10) then n3 (50).
	if p.Replicas[0].NodeID != "n2" || p.Replicas[1].NodeID != "n3" {
		t.Fatalf("selected %s,%s, want n2,n3", p.Replicas[0].NodeID, p.Replicas[1].NodeID)
	}
	// Replica IDs are 1..RF, and no replica names a path: each node resolves
	// its own directory.
	if p.Replicas[0].ReplicaID != 1 || p.Replicas[1].ReplicaID != 2 {
		t.Errorf("replica IDs = %d,%d, want 1,2", p.Replicas[0].ReplicaID, p.Replicas[1].ReplicaID)
	}
	for _, r := range p.Replicas {
		if r.DataRoot != "" {
			t.Errorf("replica %d names the path %q; placement records none", r.ReplicaID, r.DataRoot)
		}
	}
}

// TestPlace_AntiAffinity verifies no two replicas land on the same node even
// when a node is duplicated in the topology snapshot.
func TestPlace_AntiAffinity(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 0, Healthy: true},
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 0, Healthy: true}, // duplicate
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 5, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 5, Healthy: true},
	})
	p, err := s.Place(context.Background(), baseRequest(3))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range p.Replicas {
		if seen[r.NodeID] {
			t.Fatalf("anti-affinity violated: node %s used twice", r.NodeID)
		}
		seen[r.NodeID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct nodes, want 3", len(seen))
	}
}

// TestPlace_BoundedLoadAvoidsHotNode verifies a hot node is never chosen when a
// colder distinct node remains available.
func TestPlace_BoundedLoadAvoidsHotNode(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "cold1", RaftAddr: "c1:1", Bytes: 1, Healthy: true},
		{NodeID: "cold2", RaftAddr: "c2:1", Bytes: 1, Healthy: true},
		{NodeID: "hot", RaftAddr: "h:1", Bytes: 1_000_000, Healthy: true},
	})
	p, err := s.Place(context.Background(), baseRequest(2))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	for _, r := range p.Replicas {
		if r.NodeID == "hot" {
			t.Fatalf("hot node selected despite colder distinct nodes: %+v", p.Replicas)
		}
	}
}

// TestPlace_DeterministicTieBreak verifies equal-load nodes break ties on
// NodeID, yielding a stable, repeatable placement.
func TestPlace_DeterministicTieBreak(t *testing.T) {
	nodes := []NodeLoad{
		{NodeID: "c", RaftAddr: "c:1", Bytes: 0, Healthy: true},
		{NodeID: "a", RaftAddr: "a:1", Bytes: 0, Healthy: true},
		{NodeID: "b", RaftAddr: "b:1", Bytes: 0, Healthy: true},
	}
	first := newScheduler(t, nodes)
	p1, err := first.Place(context.Background(), baseRequest(2))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	// All equal load => ascending NodeID: a, b.
	if p1.Replicas[0].NodeID != "a" || p1.Replicas[1].NodeID != "b" {
		t.Fatalf("tie-break selected %s,%s, want a,b", p1.Replicas[0].NodeID, p1.Replicas[1].NodeID)
	}
	// Re-running over the same topology yields the identical placement.
	p2, err := first.Place(context.Background(), baseRequest(2))
	if err != nil {
		t.Fatalf("Place (2): %v", err)
	}
	if p2.Replicas[0].NodeID != p1.Replicas[0].NodeID || p2.Replicas[1].NodeID != p1.Replicas[1].NodeID {
		t.Fatalf("non-deterministic placement: %+v vs %+v", p1.Replicas, p2.Replicas)
	}
}

// TestPlace_LeaderIsLeastLoaded verifies the leader is the least-loaded of the
// chosen replicas (replica ID 1).
func TestPlace_LeaderIsLeastLoaded(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 30, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 5, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 20, Healthy: true},
	})
	p, err := s.Place(context.Background(), baseRequest(3))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	leader, ok := p.Leader()
	if !ok {
		t.Fatalf("no leader resolved from placement")
	}
	if leader.NodeID != "n2" {
		t.Fatalf("leader node = %s, want n2 (least loaded)", leader.NodeID)
	}
	if p.LeaderReplicaID != 1 {
		t.Errorf("leader replica ID = %d, want 1", p.LeaderReplicaID)
	}
}

// TestPlace_InsufficientNodes verifies the typed error when healthy distinct
// nodes are fewer than RF (unhealthy nodes are excluded).
func TestPlace_InsufficientNodes(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 0, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 0, Healthy: false}, // unhealthy
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 0, Healthy: false}, // unhealthy
	})
	_, err := s.Place(context.Background(), baseRequest(2))
	if !errors.Is(err, ErrInsufficientNodes) {
		t.Fatalf("error = %v, want ErrInsufficientNodes", err)
	}
	var ie *InsufficientNodesError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %v, want *InsufficientNodesError", err)
	}
	if ie.Have != 1 || ie.Need != 2 {
		t.Errorf("InsufficientNodesError = have %d need %d, want have 1 need 2", ie.Have, ie.Need)
	}
}

// TestPlace_Validation covers the input-validation guards.
func TestPlace_Validation(t *testing.T) {
	s := newScheduler(t, []NodeLoad{{NodeID: "n1", RaftAddr: "n1:1", Healthy: true}})
	cases := []struct {
		name string
		mut  func(*PlaceRequest)
		want error
	}{
		{"empty tenant", func(r *PlaceRequest) { r.TenantID = "" }, ErrEmptyTenant},
		{"empty database id", func(r *PlaceRequest) { r.DatabaseID = "" }, ErrEmptyDatabaseID},
		{"empty database", func(r *PlaceRequest) { r.Database = "" }, ErrEmptyDatabase},
		{"zero shard", func(r *PlaceRequest) { r.ShardID = 0 }, ErrZeroShard},
		{"bad rf", func(r *PlaceRequest) { r.RF = 0 }, ErrInvalidRF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest(1)
			tc.mut(&req)
			if _, err := s.Place(context.Background(), req); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestClusterSize reports the total node count regardless of health.
func TestClusterSize(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", Healthy: true},
		{NodeID: "n2", Healthy: false},
	})
	if got := s.ClusterSize(); got != 2 {
		t.Fatalf("ClusterSize = %d, want 2", got)
	}
}

// TestPlaceAdditional_ExcludesExistingNodesAndAssignsIDs verifies the new
// replicas land on nodes not already hosting a replica of the group, that
// ReplicaID starts at MaxReplicaID(existing)+1, and that new replicas are
// marked ReplicaStateObserver.
func TestPlaceAdditional_ExcludesExistingNodesAndAssignsIDs(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 0, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 1, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 2, Healthy: true},
	})
	existing := &Placement{
		TenantID:        "acme",
		DatabaseID:      "orders",
		Database:        "orders",
		ShardID:         1000,
		RF:              1,
		LeaderReplicaID: 1,
		Replicas: []Replica{
			{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1", DataRoot: "/d/n1"},
		},
	}

	updated, err := s.PlaceAdditional(context.Background(), existing, 2)
	if err != nil {
		t.Fatalf("PlaceAdditional: %v", err)
	}
	if updated.RF != 3 || len(updated.Replicas) != 3 {
		t.Fatalf("RF=%d replicas=%d, want 3/3", updated.RF, len(updated.Replicas))
	}
	// Existing replica is untouched, byte for byte.
	if updated.Replicas[0] != existing.Replicas[0] {
		t.Fatalf("existing replica mutated: got %+v, want %+v", updated.Replicas[0], existing.Replicas[0])
	}
	if updated.LeaderReplicaID != existing.LeaderReplicaID {
		t.Fatalf("LeaderReplicaID changed: got %d, want %d", updated.LeaderReplicaID, existing.LeaderReplicaID)
	}

	seen := map[string]bool{}
	for _, r := range updated.Replicas {
		if seen[r.NodeID] {
			t.Fatalf("anti-affinity violated: node %s used twice", r.NodeID)
		}
		seen[r.NodeID] = true
	}
	if seen["n1"] != true {
		t.Fatalf("existing node n1 missing from result")
	}

	// New replicas (n2, n3) get IDs 2 and 3, in ascending load order, and are
	// marked Observer (non-voting until caught up and promoted).
	for _, r := range updated.Replicas[1:] {
		if r.NodeID == "n1" {
			t.Fatalf("PlaceAdditional re-selected the existing node n1")
		}
		if r.State != ReplicaStateObserver {
			t.Fatalf("new replica %+v State = %v, want ReplicaStateObserver", r, r.State)
		}
	}
	if updated.Replicas[1].ReplicaID != 2 || updated.Replicas[2].ReplicaID != 3 {
		t.Fatalf("new replica IDs = %d,%d, want 2,3", updated.Replicas[1].ReplicaID, updated.Replicas[2].ReplicaID)
	}
}

// TestPlaceAdditional_ReplicaIDStartsAtMaxPlusOne verifies ID assignment
// continues from the highest existing ReplicaID even when replicas were
// removed and re-added out of order (a gap in the ID sequence).
func TestPlaceAdditional_ReplicaIDStartsAtMaxPlusOne(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Healthy: true},
		{NodeID: "n5", RaftAddr: "n5:1", Healthy: true},
	})
	existing := &Placement{
		TenantID:   "acme",
		DatabaseID: "orders",
		Database:   "orders",
		ShardID:    1000,
		RF:         1,
		Replicas: []Replica{
			{ReplicaID: 4, NodeID: "n1", RaftAddr: "n1:1"},
		},
	}
	updated, err := s.PlaceAdditional(context.Background(), existing, 1)
	if err != nil {
		t.Fatalf("PlaceAdditional: %v", err)
	}
	if updated.Replicas[1].ReplicaID != 5 {
		t.Fatalf("new replica ID = %d, want 5 (max existing + 1)", updated.Replicas[1].ReplicaID)
	}
}

// TestPlaceAdditional_InsufficientNodes verifies the typed error when fewer
// than addCount eligible nodes remain after excluding the group's existing
// nodes.
func TestPlaceAdditional_InsufficientNodes(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Healthy: true},
	})
	existing := &Placement{
		TenantID:   "acme",
		DatabaseID: "orders",
		Database:   "orders",
		ShardID:    1000,
		RF:         1,
		Replicas: []Replica{
			{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1"},
		},
	}
	// Only n2 remains eligible; requesting 2 additional replicas must fail.
	_, err := s.PlaceAdditional(context.Background(), existing, 2)
	if !errors.Is(err, ErrInsufficientNodes) {
		t.Fatalf("error = %v, want ErrInsufficientNodes", err)
	}
	var ie *InsufficientNodesError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %v, want *InsufficientNodesError", err)
	}
	if ie.Have != 1 || ie.Need != 2 {
		t.Errorf("InsufficientNodesError = have %d need %d, want have 1 need 2", ie.Have, ie.Need)
	}
}

// TestPlaceAdditional_Validation covers input-validation guards.
func TestPlaceAdditional_Validation(t *testing.T) {
	s := newScheduler(t, []NodeLoad{{NodeID: "n1", RaftAddr: "n1:1", Healthy: true}})
	existing := &Placement{
		TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 1000, RF: 1,
		Replicas: []Replica{{ReplicaID: 1, NodeID: "n2", RaftAddr: "n2:1"}},
	}
	if _, err := s.PlaceAdditional(context.Background(), nil, 1); err == nil {
		t.Fatal("PlaceAdditional(nil existing) should error")
	}
	if _, err := s.PlaceAdditional(context.Background(), existing, 0); !errors.Is(err, ErrInvalidRF) {
		t.Fatalf("PlaceAdditional(addCount=0) error = %v, want ErrInvalidRF", err)
	}
	if _, err := s.PlaceAdditional(context.Background(), existing, -1); !errors.Is(err, ErrInvalidRF) {
		t.Fatalf("PlaceAdditional(addCount=-1) error = %v, want ErrInvalidRF", err)
	}
}

// TestPlace_AllNodesSelectedWhenRFEqualsHealthy verifies that when RF equals the
// healthy node count, every healthy node is used exactly once even if some are
// over the bounded-load ceiling.
func TestPlace_AllNodesSelectedWhenRFEqualsHealthy(t *testing.T) {
	s := newScheduler(t, []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 1, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 1, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 9_999, Healthy: true}, // over ceiling
	})
	p, err := s.Place(context.Background(), baseRequest(3))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if len(p.Replicas) != 3 {
		t.Fatalf("got %d replicas, want 3", len(p.Replicas))
	}
	if _, ok := p.ReplicaForNode("n3"); !ok {
		t.Fatalf("over-ceiling node n3 must still be selected when RF == healthy count")
	}
}
