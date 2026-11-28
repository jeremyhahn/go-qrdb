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

package service

import (
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// stubRebalance implements RebalanceServicer returning a fixed imbalance report.
type stubRebalance struct {
	report *ImbalanceReportResult
	err    error
}

func (s *stubRebalance) GetShardStats(context.Context, uint64) (*ShardStats, error) {
	return nil, nil
}
func (s *stubRebalance) CreateRebalancePlan(context.Context, *RebalancePlanOptions) (*RebalancePlanResult, error) {
	return nil, nil
}
func (s *stubRebalance) ExecuteRebalance(context.Context, *RebalancePlanResult) (*RebalanceExecutionResult, error) {
	return nil, nil
}
func (s *stubRebalance) GetImbalanceReport(context.Context) (*ImbalanceReportResult, error) {
	return s.report, s.err
}

func nodeHostInfo() *quicraft.NodeHostInfo {
	return &quicraft.NodeHostInfo{
		RaftAddress: "n1:1",
		ShardInfoList: []quicraft.ShardInfo{
			{ShardID: 1, Nodes: map[uint64]string{1: "n1:1", 2: "n2:1", 3: "n3:1"}},
			{ShardID: 1000, Nodes: map[uint64]string{1: "n1:1"}},
		},
	}
}

// TestHostTopology_RosterFromHost builds the node roster from the host's shard
// membership when no rebalance report is available.
func TestHostTopology_RosterFromHost(t *testing.T) {
	host := &mockHost{getNodeHostInfoFn: func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		return nodeHostInfo()
	}}
	p := NewHostTopologyProvider(host, nil)
	nodes := p.Nodes()
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (n1,n2,n3)", len(nodes))
	}
	// Sorted by NodeID; all marked healthy; shard counts derived from membership.
	if nodes[0].NodeID != "n1:1" || nodes[0].ShardCount != 2 {
		t.Errorf("n1 = %+v, want shard count 2", nodes[0])
	}
	for _, n := range nodes {
		if !n.Healthy {
			t.Errorf("node %s not marked healthy", n.NodeID)
		}
	}
}

// TestHostTopology_LoadFromRebalance overlays per-node byte/shard load from the
// RebalanceService report.
func TestHostTopology_LoadFromRebalance(t *testing.T) {
	host := &mockHost{getNodeHostInfoFn: func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		return nodeHostInfo()
	}}
	reb := &stubRebalance{report: &ImbalanceReportResult{
		Nodes: []NodeLoadInfo{
			{NodeID: "n1:1", Address: "n1:1", ShardCount: 2, TotalBytes: 500},
			{NodeID: "n2:1", Address: "n2:1", ShardCount: 1, TotalBytes: 10},
			{NodeID: "n3:1", Address: "n3:1", ShardCount: 1, TotalBytes: 50},
		},
	}}
	p := NewHostTopologyProvider(host, reb)
	nodes := p.Nodes()
	byID := map[string]uint64{}
	for _, n := range nodes {
		byID[n.NodeID] = n.Bytes
	}
	if byID["n1:1"] != 500 || byID["n2:1"] != 10 || byID["n3:1"] != 50 {
		t.Fatalf("byte load not applied from report: %v", byID)
	}
}

// TestHostTopology_NilHost returns no nodes for a nil host.
func TestHostTopology_NilHost(t *testing.T) {
	var p *HostTopologyProvider
	if got := p.Nodes(); got != nil {
		t.Fatalf("nil provider Nodes = %v, want nil", got)
	}
	p2 := NewHostTopologyProvider(nil, nil)
	if got := p2.Nodes(); got != nil {
		t.Fatalf("nil-host provider Nodes = %v, want nil", got)
	}
}

// servingSetStore holds the isolated tenant "iso" with its home group on n1
// and n2 (its serving set) and its database "orders" on the same two nodes.
// The shared tenant "shared" has no home group.
func servingSetStore(t *testing.T) (*placement.Store, *placement.Placement) {
	t.Helper()
	store, err := placement.NewStore(newMemPlacementKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	two := []placement.Replica{
		{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1"},
		{ReplicaID: 2, NodeID: "n2", RaftAddr: "n2"},
	}
	home := &placement.Placement{TenantID: "iso", DatabaseID: database.TenantHomeID, Database: database.TenantHomeName, ShardID: 100, RF: 2, LeaderReplicaID: 1, Replicas: two}
	db := &placement.Placement{TenantID: "iso", DatabaseID: "orders-id", Database: "orders", ShardID: 101, RF: 2, LeaderReplicaID: 1, Replicas: two}
	for _, p := range []*placement.Placement{home, db} {
		if err := store.Put(ctx, p); err != nil {
			t.Fatalf("Put %s: %v", p.Database, err)
		}
	}
	return store, db
}

func TestPlacementServingSet_IsolatedTenantIsConfinedToItsHomeGroup(t *testing.T) {
	store, _ := servingSetStore(t)
	nodes, confined, err := PlacementServingSet(store, nil)(context.Background(), "iso", "orders")
	if err != nil || !confined {
		t.Fatalf("serving set of iso = (%v, %v, %v), want confined", nodes, confined, err)
	}
	if len(nodes) != 2 || nodes[0] != "n1" || nodes[1] != "n2" {
		t.Fatalf("serving set of iso = %v, want [n1 n2]", nodes)
	}
}

// strictIsolationInactive is the gate of a cluster that has not activated
// strict-isolation (a mixed-version cluster).
func strictIsolationInactive() error {
	return &feature.NotActiveError{Feature: featuregate.StrictIsolation}
}

// TestPlacementServingSet_InactiveStrictIsolationRefusesIsolatedTenant: until
// strict-isolation is active an isolated tenant's lookup fails closed with
// the gate's refusal, never an unconfined placement; a shared tenant and the
// home group are not gated.
func TestPlacementServingSet_InactiveStrictIsolationRefusesIsolatedTenant(t *testing.T) {
	store, _ := servingSetStore(t)
	serving := PlacementServingSet(store, strictIsolationInactive)

	nodes, confined, err := serving(context.Background(), "iso", "orders")
	var notActive *feature.NotActiveError
	if !errors.As(err, &notActive) || notActive.Feature != featuregate.StrictIsolation {
		t.Fatalf("serving set of iso before activation = (%v, %v, %v), want NotActiveError(%s)", nodes, confined, err, featuregate.StrictIsolation)
	}
	if _, _, err := serving(context.Background(), "iso", database.TenantHomeName); err != nil {
		t.Fatalf("home group lookup before activation = %v, want unconfined", err)
	}
	if _, confined, err := PlacementServingSet(store, func() error { return nil })(context.Background(), "iso", "orders"); err != nil || !confined {
		t.Fatalf("serving set of iso after activation = (%v, %v), want confined", confined, err)
	}
}

func TestPlacementServingSet_SharedTenantAndHomeGroupUnconfined(t *testing.T) {
	store, _ := servingSetStore(t)
	serving := PlacementServingSet(store, nil)
	if _, confined, err := serving(context.Background(), "shared", "orders"); err != nil || confined {
		t.Fatalf("shared tenant: confined=%v err=%v, want unconfined", confined, err)
	}
	if _, confined, err := serving(context.Background(), "iso", database.TenantHomeName); err != nil || confined {
		t.Fatalf("home group itself: confined=%v err=%v, want unconfined", confined, err)
	}
}

// TestAddObserverReplica_ServingSetRefusesNodeOutside covers both paths that
// admit a replica onto a chosen node: scale-out and a rebalance MOVE.
func TestAddObserverReplica_ServingSetRefusesNodeOutside(t *testing.T) {
	store, db := servingSetStore(t)
	ctx := context.Background()
	outside := placement.Replica{ReplicaID: 3, NodeID: "n3", RaftAddr: "n3", State: placement.ReplicaStateObserver}
	_, placementFailed, err := addObserverReplica(ctx, nil, store, db, outside)
	var sse *placement.ServingSetError
	if !errors.As(err, &sse) {
		t.Fatalf("addObserverReplica on n3: err = %v, want *placement.ServingSetError", err)
	}
	if placementFailed || sse.TenantID != "iso" || sse.Database != "orders" || sse.Node != "n3" {
		t.Fatalf("placementFailed=%v ServingSetError=%+v", placementFailed, sse)
	}
	got, err := store.Get(ctx, "iso", "orders-id")
	if err != nil || len(got.Replicas) != 2 {
		t.Fatalf("placement after refusal = %+v, %v; want unchanged", got, err)
	}
}

func TestAddObserverReplica_ServingSetAdmitsSharedTenantAnywhere(t *testing.T) {
	store, _ := servingSetStore(t)
	ctx := context.Background()
	shared := &placement.Placement{TenantID: "shared", DatabaseID: "shared-orders-id", Database: "orders", ShardID: 102, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1"}}}
	if err := store.Put(ctx, shared); err != nil {
		t.Fatalf("Put: %v", err)
	}
	r := placement.Replica{ReplicaID: 2, NodeID: "n3", RaftAddr: "n3", State: placement.ReplicaStateObserver}
	if _, _, err := addObserverReplica(ctx, nil, store, shared, r); err != nil {
		t.Fatalf("shared tenant's replica on n3: %v", err)
	}
}
