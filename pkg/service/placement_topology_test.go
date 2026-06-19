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
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
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
