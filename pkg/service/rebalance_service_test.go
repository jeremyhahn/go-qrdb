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
)

// ---------------------------------------------------------------------------
// NewRebalanceService
// ---------------------------------------------------------------------------

func TestRebalanceService_NewRebalanceService_ReturnsNonNil(t *testing.T) {
	svc := NewRebalanceService(&mockHost{})
	if svc == nil {
		t.Fatal("expected non-nil RebalanceService")
	}
}

func TestRebalanceService_NewRebalanceService_ImplementsInterface(t *testing.T) {
	_ = RebalanceServicer(NewRebalanceService(&mockHost{}))
}

// ---------------------------------------------------------------------------
// GetShardStats: success
// ---------------------------------------------------------------------------

func TestRebalanceService_GetShardStats_ReturnsStats(t *testing.T) {
	host := &mockHost{
		getLeaderIDFn: func(shardID uint64) (uint64, bool, error) {
			return 2, true, nil
		},
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{
						ShardID:   1,
						ReplicaID: 1,
						Nodes: map[uint64]string{
							1: "127.0.0.1:9100",
							2: "127.0.0.1:9101",
							3: "127.0.0.1:9102",
						},
					},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{
				"key1": []byte("value1"),
				"key2": []byte("value2"),
			}, nil
		},
	}

	svc := NewRebalanceService(host)
	stats, err := svc.GetShardStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", stats.ShardID)
	}
	if stats.KeyCount != 2 {
		t.Fatalf("expected KeyCount 2, got %d", stats.KeyCount)
	}
	if stats.ByteCount != 20 { // key1(4)+value1(6) + key2(4)+value2(6) = 20
		t.Fatalf("expected ByteCount 20, got %d", stats.ByteCount)
	}
	if stats.LeaderID != 2 {
		t.Fatalf("expected LeaderID 2, got %d", stats.LeaderID)
	}
	if stats.ReplicaCount != 3 {
		t.Fatalf("expected ReplicaCount 3, got %d", stats.ReplicaCount)
	}
}

// ---------------------------------------------------------------------------
// GetShardStats: error cases
// ---------------------------------------------------------------------------

func TestRebalanceService_GetShardStats_GetLeaderIDError(t *testing.T) {
	host := &mockHost{
		getLeaderIDFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, errors.New("leader lookup failed")
		},
	}

	svc := NewRebalanceService(host)
	_, err := svc.GetShardStats(context.Background(), 99)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var sse *ShardStatsError
	if !errors.As(err, &sse) {
		t.Fatalf("expected ShardStatsError, got %T", err)
	}
	if sse.ShardID != 99 {
		t.Fatalf("expected ShardID 99 in error, got %d", sse.ShardID)
	}
}

func TestRebalanceService_GetShardStats_ShardNotLoaded(t *testing.T) {
	host := &mockHost{
		getLeaderIDFn: func(_ uint64) (uint64, bool, error) {
			return 1, true, nil
		},
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 999, Nodes: map[uint64]string{1: "addr"}},
				},
			}
		},
	}

	svc := NewRebalanceService(host)
	_, err := svc.GetShardStats(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error for shard not loaded")
	}
	var sse *ShardStatsError
	if !errors.As(err, &sse) {
		t.Fatalf("expected ShardStatsError, got %T", err)
	}
}

func TestRebalanceService_GetShardStats_StaleReadError(t *testing.T) {
	host := &mockHost{
		getLeaderIDFn: func(_ uint64) (uint64, bool, error) {
			return 1, true, nil
		},
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "addr"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return nil, errors.New("read error")
		},
	}

	svc := NewRebalanceService(host)
	_, err := svc.GetShardStats(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error from stale read failure")
	}
	var sse *ShardStatsError
	if !errors.As(err, &sse) {
		t.Fatalf("expected ShardStatsError, got %T", err)
	}
}

func TestRebalanceService_GetShardStats_EmptyData(t *testing.T) {
	host := &mockHost{
		getLeaderIDFn: func(_ uint64) (uint64, bool, error) {
			return 1, true, nil
		},
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "addr"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	stats, err := svc.GetShardStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.KeyCount != 0 {
		t.Fatalf("expected KeyCount 0, got %d", stats.KeyCount)
	}
	if stats.ByteCount != 0 {
		t.Fatalf("expected ByteCount 0, got %d", stats.ByteCount)
	}
}

// ---------------------------------------------------------------------------
// CreateRebalancePlan: success
// ---------------------------------------------------------------------------

func TestRebalanceService_CreateRebalancePlan_SingleNodeNoop(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "node1:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{"k": []byte("v")}, nil
		},
	}

	svc := NewRebalanceService(host)
	plan, err := svc.CreateRebalancePlan(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Moves) != 0 {
		t.Fatalf("expected 0 moves for single node, got %d", len(plan.Moves))
	}
}

func TestRebalanceService_CreateRebalancePlan_NilOpts(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	plan, err := svc.CreateRebalancePlan(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
}

func TestRebalanceService_CreateRebalancePlan_MaxMoves(t *testing.T) {
	host := newMultiNodeHost()

	svc := NewRebalanceService(host)
	plan, err := svc.CreateRebalancePlan(context.Background(), &RebalancePlanOptions{
		MaxMoves: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Moves) > 1 {
		t.Fatalf("expected at most 1 move with MaxMoves=1, got %d", len(plan.Moves))
	}
}

// ---------------------------------------------------------------------------
// CreateRebalancePlan: error cases
// ---------------------------------------------------------------------------

func TestRebalanceService_CreateRebalancePlan_SkipsAShardWhoseStaleReadIsNotAMap(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "node1"}},
					{ShardID: 2, Nodes: map[uint64]string{2: "node2"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, shardID uint64, _ interface{}) (interface{}, error) {
			// Return non-map type to exercise the type assertion failure path.
			return "not a map", nil
		},
	}

	svc := NewRebalanceService(host)
	plan, err := svc.CreateRebalancePlan(context.Background(), &RebalancePlanOptions{})
	// This should succeed since the stale read failure is gracefully handled
	// in GetImbalanceReport (it continues on error).
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan even with read errors")
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance: success
// ---------------------------------------------------------------------------

func TestRebalanceService_ExecuteRebalance_EmptyPlan(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	result, err := svc.ExecuteRebalance(context.Background(), &RebalancePlanResult{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesCompleted != 0 {
		t.Fatalf("expected 0 completed moves, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed moves, got %d", result.MovesFailed)
	}
}

func TestRebalanceService_ExecuteRebalance_SuccessfulMove(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{
						ShardID: 1,
						Nodes: map[uint64]string{
							1: "node1:9100",
						},
					},
				},
			}
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return newCompletedRequestState(quicraft.RequestResult{}), nil
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	plan := &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{
				ShardID:        1,
				FromNode:       "node1:9100",
				ToNode:         "node2:9100",
				EstimatedBytes: 1024,
			},
		},
		CurrentImbalance: 0.5,
	}

	result, err := svc.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Fatalf("expected 1 completed move, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed moves, got %d", result.MovesFailed)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance: error cases
// ---------------------------------------------------------------------------

func TestRebalanceService_ExecuteRebalance_NilPlan(t *testing.T) {
	svc := NewRebalanceService(&mockHost{})
	_, err := svc.ExecuteRebalance(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil plan")
	}
	var ree *RebalanceExecuteError
	if !errors.As(err, &ree) {
		t.Fatalf("expected RebalanceExecuteError, got %T", err)
	}
}

func TestRebalanceService_ExecuteRebalance_ShardNotFound(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 999, Nodes: map[uint64]string{1: "addr"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	plan := &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{ShardID: 1, FromNode: "a", ToNode: "b"},
		},
	}

	result, err := svc.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesFailed != 1 {
		t.Fatalf("expected 1 failed move for missing shard, got %d", result.MovesFailed)
	}
}

func TestRebalanceService_ExecuteRebalance_AddNodeError(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "addr"}},
				},
			}
		},
		requestAddNodeFn: func(_ context.Context, _ uint64, _ uint64, _ string) (*quicraft.RequestState, error) {
			return nil, errors.New("add node failed")
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	plan := &RebalancePlanResult{
		Moves: []RebalanceMovePlan{
			{ShardID: 1, FromNode: "a", ToNode: "b"},
		},
	}

	result, err := svc.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MovesFailed != 1 {
		t.Fatalf("expected 1 failed move, got %d", result.MovesFailed)
	}
	if result.MovesCompleted != 0 {
		t.Fatalf("expected 0 completed moves, got %d", result.MovesCompleted)
	}
}

// ---------------------------------------------------------------------------
// GetImbalanceReport: success
// ---------------------------------------------------------------------------

func TestRebalanceService_GetImbalanceReport_EmptyCluster(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{}
		},
	}

	svc := NewRebalanceService(host)
	report, err := svc.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(report.Nodes))
	}
	if report.ImbalanceScore != 0 {
		t.Fatalf("expected 0 imbalance score, got %f", report.ImbalanceScore)
	}
}

func TestRebalanceService_GetImbalanceReport_BalancedCluster(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, IsLeader: true, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 2, IsLeader: true, Nodes: map[uint64]string{2: "node2:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			// Same data size for both shards.
			return map[string][]byte{
				"key": []byte("value"),
			}, nil
		},
	}

	svc := NewRebalanceService(host)
	report, err := svc.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(report.Nodes))
	}
	if report.ImbalanceScore != 0 {
		t.Fatalf("expected 0 imbalance for balanced cluster, got %f", report.ImbalanceScore)
	}
	if len(report.Hotspots) != 0 {
		t.Fatalf("expected 0 hotspots for balanced cluster, got %d", len(report.Hotspots))
	}
}

func TestRebalanceService_GetImbalanceReport_ImbalancedCluster(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, IsLeader: true, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 2, IsLeader: false, Nodes: map[uint64]string{2: "node2:9100"}},
					{ShardID: 3, IsLeader: false, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 4, IsLeader: false, Nodes: map[uint64]string{1: "node1:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, shardID uint64, _ interface{}) (interface{}, error) {
			if shardID == 2 {
				return map[string][]byte{"small": []byte("s")}, nil
			}
			return map[string][]byte{
				"key1": []byte("value-that-is-much-longer"),
				"key2": []byte("another-long-value-here"),
				"key3": []byte("yet-another-value-data"),
			}, nil
		},
	}

	svc := NewRebalanceService(host)
	report, err := svc.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ImbalanceScore <= 0 {
		t.Fatalf("expected positive imbalance score for imbalanced cluster, got %f", report.ImbalanceScore)
	}
}

func TestRebalanceService_GetImbalanceReport_NodesSortedByAddress(t *testing.T) {
	host := &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, Nodes: map[uint64]string{1: "c-node:9100"}},
					{ShardID: 2, Nodes: map[uint64]string{2: "a-node:9100"}},
					{ShardID: 3, Nodes: map[uint64]string{3: "b-node:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
			return map[string][]byte{}, nil
		},
	}

	svc := NewRebalanceService(host)
	report, err := svc.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(report.Nodes))
	}
	if report.Nodes[0].Address != "a-node:9100" {
		t.Fatalf("expected first node a-node:9100, got %s", report.Nodes[0].Address)
	}
	if report.Nodes[1].Address != "b-node:9100" {
		t.Fatalf("expected second node b-node:9100, got %s", report.Nodes[1].Address)
	}
	if report.Nodes[2].Address != "c-node:9100" {
		t.Fatalf("expected third node c-node:9100, got %s", report.Nodes[2].Address)
	}
}

// ---------------------------------------------------------------------------
// calculateImbalanceScore
// ---------------------------------------------------------------------------

func TestCalculateImbalanceScore_SingleNode(t *testing.T) {
	nodes := []NodeLoadInfo{{TotalBytes: 1000}}
	score := calculateImbalanceScore(nodes)
	if score != 0.0 {
		t.Fatalf("expected 0.0 for single node, got %f", score)
	}
}

func TestCalculateImbalanceScore_PerfectlyBalanced(t *testing.T) {
	nodes := []NodeLoadInfo{
		{TotalBytes: 1000},
		{TotalBytes: 1000},
		{TotalBytes: 1000},
	}
	score := calculateImbalanceScore(nodes)
	if score != 0.0 {
		t.Fatalf("expected 0.0 for perfectly balanced, got %f", score)
	}
}

func TestCalculateImbalanceScore_ZeroBytes(t *testing.T) {
	nodes := []NodeLoadInfo{
		{TotalBytes: 0},
		{TotalBytes: 0},
	}
	score := calculateImbalanceScore(nodes)
	if score != 0.0 {
		t.Fatalf("expected 0.0 for zero bytes, got %f", score)
	}
}

func TestCalculateImbalanceScore_Imbalanced(t *testing.T) {
	nodes := []NodeLoadInfo{
		{TotalBytes: 10000},
		{TotalBytes: 100},
	}
	score := calculateImbalanceScore(nodes)
	if score <= 0 {
		t.Fatalf("expected positive score for imbalanced nodes, got %f", score)
	}
}

// ---------------------------------------------------------------------------
// identifyHotspots
// ---------------------------------------------------------------------------

func TestIdentifyHotspots_SingleNode(t *testing.T) {
	nodes := []NodeLoadInfo{{NodeID: "n1", TotalBytes: 5000}}
	hotspots := identifyHotspots(nodes)
	if len(hotspots) != 0 {
		t.Fatalf("expected no hotspots for single node, got %d", len(hotspots))
	}
}

func TestIdentifyHotspots_NoHotspotsWhenBalanced(t *testing.T) {
	nodes := []NodeLoadInfo{
		{NodeID: "n1", TotalBytes: 1000},
		{NodeID: "n2", TotalBytes: 1000},
		{NodeID: "n3", TotalBytes: 1000},
	}
	hotspots := identifyHotspots(nodes)
	if len(hotspots) != 0 {
		t.Fatalf("expected no hotspots when balanced, got %v", hotspots)
	}
}

func TestIdentifyHotspots_DetectsHotspot(t *testing.T) {
	nodes := []NodeLoadInfo{
		{NodeID: "n1", TotalBytes: 100000},
		{NodeID: "n2", TotalBytes: 100},
		{NodeID: "n3", TotalBytes: 100},
		{NodeID: "n4", TotalBytes: 100},
		{NodeID: "n5", TotalBytes: 100},
	}
	hotspots := identifyHotspots(nodes)
	if len(hotspots) != 1 {
		t.Fatalf("expected 1 hotspot, got %d: %v", len(hotspots), hotspots)
	}
	if hotspots[0] != "n1" {
		t.Fatalf("expected hotspot n1, got %s", hotspots[0])
	}
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

func TestRebalanceErrors_ShardStatsError(t *testing.T) {
	err := &ShardStatsError{ShardID: 42, Err: errors.New("test")}
	if err.Error() != "service: get shard stats 42 failed: test" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
	if err.Unwrap().Error() != "test" {
		t.Fatal("Unwrap did not return underlying error")
	}
}

func TestRebalanceErrors_RebalancePlanError(t *testing.T) {
	err := &RebalancePlanError{Err: errors.New("plan failed")}
	if err.Error() != "service: create rebalance plan failed: plan failed" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
	if err.Unwrap().Error() != "plan failed" {
		t.Fatal("Unwrap did not return underlying error")
	}
}

func TestRebalanceErrors_RebalanceExecuteError(t *testing.T) {
	err := &RebalanceExecuteError{Err: errors.New("exec failed")}
	if err.Error() != "service: execute rebalance failed: exec failed" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
	if err.Unwrap().Error() != "exec failed" {
		t.Fatal("Unwrap did not return underlying error")
	}
}

// ---------------------------------------------------------------------------
// formatUint64 helper
// ---------------------------------------------------------------------------

func TestFormatUint64_Zero(t *testing.T) {
	if formatUint64(0) != "0" {
		t.Fatalf("expected '0', got '%s'", formatUint64(0))
	}
}

func TestFormatUint64_Positive(t *testing.T) {
	if formatUint64(12345) != "12345" {
		t.Fatalf("expected '12345', got '%s'", formatUint64(12345))
	}
}

func TestFormatUint64_Large(t *testing.T) {
	if formatUint64(18446744073709551615) != "18446744073709551615" {
		t.Fatalf("expected max uint64, got '%s'", formatUint64(18446744073709551615))
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newMultiNodeHost creates a mockHost simulating a multi-node cluster
// with an imbalanced shard distribution.
func newMultiNodeHost() *mockHost {
	return &mockHost{
		getNodeHostInfoFn: func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			return &quicraft.NodeHostInfo{
				ShardInfoList: []quicraft.ShardInfo{
					{ShardID: 1, IsLeader: true, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 2, IsLeader: false, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 3, IsLeader: false, Nodes: map[uint64]string{1: "node1:9100"}},
					{ShardID: 4, IsLeader: false, Nodes: map[uint64]string{2: "node2:9100"}},
				},
			}
		},
		staleReadFn: func(_ context.Context, shardID uint64, _ interface{}) (interface{}, error) {
			if shardID == 4 {
				return map[string][]byte{"k": []byte("v")}, nil
			}
			return map[string][]byte{
				"key1": []byte("value-data-that-is-large"),
				"key2": []byte("another-value-data-here"),
			}, nil
		},
	}
}
