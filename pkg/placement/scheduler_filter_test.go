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
	return func(_ string, _ uint64, raftAddr string) error {
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

// TestScheduler_ReplicaFilterReceivesTheRequestsTenantID proves the
// placement request's own tenant id reaches the filter, which the
// volume-pools feature needs to resolve the tenant's pool before it can
// judge a candidate's health for it.
func TestScheduler_ReplicaFilterReceivesTheRequestsTenantID(t *testing.T) {
	nodes := []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 1, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 50, Healthy: true},
	}
	var seen []string
	filter := func(tenantID string, _ uint64, _ string) error {
		seen = append(seen, tenantID)
		return nil
	}
	s, err := NewScheduler(&mockTopology{nodes: nodes}, WithReplicaFilter(filter))
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if _, err := s.Place(context.Background(), baseRequest(2)); err != nil {
		t.Fatalf("Place: %v", err)
	}
	for _, tenantID := range seen {
		if tenantID != "acme" {
			t.Fatalf("filter saw tenant id %q, want acme", tenantID)
		}
	}
	if len(seen) == 0 {
		t.Fatal("filter was never called")
	}
}

// TestWithReplicaFilter_ComposesMultipleFilters proves two filters attached
// by separate WithReplicaFilter calls are ANDed: a candidate must pass
// every one of them, as the feature-gate filter and a pool-health filter
// both need to.
func TestWithReplicaFilter_ComposesMultipleFilters(t *testing.T) {
	nodes := []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 1, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 50, Healthy: true},
		{NodeID: "n3", RaftAddr: "n3:1", Bytes: 60, Healthy: true},
	}
	s, err := NewScheduler(&mockTopology{nodes: nodes},
		WithReplicaFilter(refuseAddr("n1:1")),
		WithReplicaFilter(refuseAddr("n2:1")),
	)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	p, err := s.Place(context.Background(), baseRequest(1))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.Replicas[0].NodeID != "n3" {
		t.Fatalf("Place chose %q, want n3: both n1 and n2 are refused by composed filters", p.Replicas[0].NodeID)
	}
}

// TestWithReplicaFilter_NilFilterIsANoOp proves a nil filter does not
// clobber a previously attached one.
func TestWithReplicaFilter_NilFilterIsANoOp(t *testing.T) {
	nodes := []NodeLoad{
		{NodeID: "n1", RaftAddr: "n1:1", Bytes: 1, Healthy: true},
		{NodeID: "n2", RaftAddr: "n2:1", Bytes: 50, Healthy: true},
	}
	s, err := NewScheduler(&mockTopology{nodes: nodes},
		WithReplicaFilter(refuseAddr("n1:1")),
		WithReplicaFilter(nil),
	)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	p, err := s.Place(context.Background(), baseRequest(1))
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.Replicas[0].NodeID != "n2" {
		t.Fatalf("Place chose %q, want n2: n1 is still refused after WithReplicaFilter(nil)", p.Replicas[0].NodeID)
	}
}
