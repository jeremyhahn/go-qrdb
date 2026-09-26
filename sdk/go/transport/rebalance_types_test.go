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

package transport

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ShardStatsResponse
// ---------------------------------------------------------------------------

func TestShardStatsResponse_Fields(t *testing.T) {
	resp := &ShardStatsResponse{
		ShardID:      1,
		KeyCount:     100,
		ByteCount:    2048,
		LeaderID:     3,
		ReplicaCount: 3,
	}
	if resp.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", resp.ShardID)
	}
	if resp.KeyCount != 100 {
		t.Fatalf("expected KeyCount 100, got %d", resp.KeyCount)
	}
	if resp.ByteCount != 2048 {
		t.Fatalf("expected ByteCount 2048, got %d", resp.ByteCount)
	}
	if resp.LeaderID != 3 {
		t.Fatalf("expected LeaderID 3, got %d", resp.LeaderID)
	}
	if resp.ReplicaCount != 3 {
		t.Fatalf("expected ReplicaCount 3, got %d", resp.ReplicaCount)
	}
}

func TestShardStatsResponse_ZeroValues(t *testing.T) {
	resp := &ShardStatsResponse{}
	if resp.ShardID != 0 || resp.KeyCount != 0 || resp.ByteCount != 0 {
		t.Fatal("expected zero values for uninitialized struct")
	}
}

// ---------------------------------------------------------------------------
// RebalancePlanRequest
// ---------------------------------------------------------------------------

func TestRebalancePlanRequest_Fields(t *testing.T) {
	req := &RebalancePlanRequest{
		MaxMoves:      10,
		DryRun:        true,
		TargetBalance: 5.0,
	}
	if req.MaxMoves != 10 {
		t.Fatalf("expected MaxMoves 10, got %d", req.MaxMoves)
	}
	if !req.DryRun {
		t.Fatal("expected DryRun true")
	}
	if req.TargetBalance != 5.0 {
		t.Fatalf("expected TargetBalance 5.0, got %f", req.TargetBalance)
	}
}

func TestRebalancePlanRequest_Defaults(t *testing.T) {
	req := &RebalancePlanRequest{}
	if req.MaxMoves != 0 {
		t.Fatal("expected default MaxMoves 0")
	}
	if req.DryRun {
		t.Fatal("expected default DryRun false")
	}
	if req.TargetBalance != 0 {
		t.Fatal("expected default TargetBalance 0")
	}
}

// ---------------------------------------------------------------------------
// RebalanceMove
// ---------------------------------------------------------------------------

func TestRebalanceMove_Fields(t *testing.T) {
	move := RebalanceMove{
		ShardID:        5,
		FromNode:       "node1:9100",
		ToNode:         "node2:9100",
		EstimatedBytes: 1024 * 1024,
	}
	if move.ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", move.ShardID)
	}
	if move.FromNode != "node1:9100" {
		t.Fatalf("expected FromNode node1:9100, got %s", move.FromNode)
	}
	if move.ToNode != "node2:9100" {
		t.Fatalf("expected ToNode node2:9100, got %s", move.ToNode)
	}
	if move.EstimatedBytes != 1024*1024 {
		t.Fatalf("expected EstimatedBytes 1048576, got %d", move.EstimatedBytes)
	}
}

func TestRebalanceMove_ZeroValues(t *testing.T) {
	move := RebalanceMove{}
	if move.ShardID != 0 || move.FromNode != "" || move.ToNode != "" || move.EstimatedBytes != 0 {
		t.Fatal("expected zero values for uninitialized RebalanceMove")
	}
}

// ---------------------------------------------------------------------------
// RebalancePlan
// ---------------------------------------------------------------------------

func TestRebalancePlan_Fields(t *testing.T) {
	plan := &RebalancePlan{
		Moves: []RebalanceMove{
			{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 512},
		},
		EstimatedDuration: 30 * time.Second,
		CurrentImbalance:  0.75,
	}
	if len(plan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(plan.Moves))
	}
	if plan.EstimatedDuration != 30*time.Second {
		t.Fatalf("expected 30s duration, got %v", plan.EstimatedDuration)
	}
	if plan.CurrentImbalance != 0.75 {
		t.Fatalf("expected imbalance 0.75, got %f", plan.CurrentImbalance)
	}
}

func TestRebalancePlan_EmptyMoves(t *testing.T) {
	plan := &RebalancePlan{}
	if plan.Moves != nil {
		t.Fatal("expected nil moves for uninitialized plan")
	}
	if plan.CurrentImbalance != 0 {
		t.Fatal("expected zero imbalance for uninitialized plan")
	}
}

// ---------------------------------------------------------------------------
// RebalanceResult
// ---------------------------------------------------------------------------

func TestRebalanceResult_Fields(t *testing.T) {
	result := &RebalanceResult{
		MovesCompleted: 5,
		MovesFailed:    2,
		Duration:       10 * time.Second,
		FinalImbalance: 0.1,
	}
	if result.MovesCompleted != 5 {
		t.Fatalf("expected MovesCompleted 5, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 2 {
		t.Fatalf("expected MovesFailed 2, got %d", result.MovesFailed)
	}
	if result.Duration != 10*time.Second {
		t.Fatalf("expected Duration 10s, got %v", result.Duration)
	}
	if result.FinalImbalance != 0.1 {
		t.Fatalf("expected FinalImbalance 0.1, got %f", result.FinalImbalance)
	}
}

func TestRebalanceResult_ZeroValues(t *testing.T) {
	result := &RebalanceResult{}
	if result.MovesCompleted != 0 || result.MovesFailed != 0 {
		t.Fatal("expected zero values for uninitialized result")
	}
}

// ---------------------------------------------------------------------------
// NodeLoad
// ---------------------------------------------------------------------------

func TestNodeLoad_Fields(t *testing.T) {
	load := NodeLoad{
		NodeID:      "node-1",
		Address:     "10.0.0.1:9100",
		ShardCount:  4,
		TotalKeys:   1000,
		TotalBytes:  1024 * 1024,
		LeaderCount: 2,
	}
	if load.NodeID != "node-1" {
		t.Fatalf("expected NodeID node-1, got %s", load.NodeID)
	}
	if load.Address != "10.0.0.1:9100" {
		t.Fatalf("expected Address 10.0.0.1:9100, got %s", load.Address)
	}
	if load.ShardCount != 4 {
		t.Fatalf("expected ShardCount 4, got %d", load.ShardCount)
	}
	if load.TotalKeys != 1000 {
		t.Fatalf("expected TotalKeys 1000, got %d", load.TotalKeys)
	}
	if load.TotalBytes != 1024*1024 {
		t.Fatalf("expected TotalBytes 1048576, got %d", load.TotalBytes)
	}
	if load.LeaderCount != 2 {
		t.Fatalf("expected LeaderCount 2, got %d", load.LeaderCount)
	}
}

func TestNodeLoad_ZeroValues(t *testing.T) {
	load := NodeLoad{}
	if load.NodeID != "" || load.Address != "" || load.ShardCount != 0 {
		t.Fatal("expected zero values for uninitialized NodeLoad")
	}
}

// ---------------------------------------------------------------------------
// ImbalanceReport
// ---------------------------------------------------------------------------

func TestImbalanceReport_Fields(t *testing.T) {
	report := &ImbalanceReport{
		Nodes: []NodeLoad{
			{NodeID: "n1", TotalBytes: 1000},
			{NodeID: "n2", TotalBytes: 2000},
		},
		ImbalanceScore: 0.5,
		Hotspots:       []string{"n2"},
	}
	if len(report.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(report.Nodes))
	}
	if report.ImbalanceScore != 0.5 {
		t.Fatalf("expected ImbalanceScore 0.5, got %f", report.ImbalanceScore)
	}
	if len(report.Hotspots) != 1 || report.Hotspots[0] != "n2" {
		t.Fatalf("expected hotspot n2, got %v", report.Hotspots)
	}
}

func TestImbalanceReport_EmptyReport(t *testing.T) {
	report := &ImbalanceReport{}
	if report.Nodes != nil {
		t.Fatal("expected nil nodes for uninitialized report")
	}
	if report.ImbalanceScore != 0 {
		t.Fatal("expected zero imbalance for uninitialized report")
	}
}

// ---------------------------------------------------------------------------
// Rebalance error types
// ---------------------------------------------------------------------------

func TestShardStatsError_Message(t *testing.T) {
	err := &ShardStatsError{ShardID: 42, Err: &RequestError{Method: "test"}}
	msg := err.Error()
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
	if err.Unwrap() == nil {
		t.Fatal("expected non-nil Unwrap")
	}
}

func TestRebalancePlanError_Message(t *testing.T) {
	inner := &RequestError{Method: "plan"}
	err := &RebalancePlanError{Err: inner}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	if err.Unwrap() != inner {
		t.Fatal("expected Unwrap to return inner error")
	}
}

func TestRebalanceExecuteError_Message(t *testing.T) {
	inner := &RequestError{Method: "exec"}
	err := &RebalanceExecuteError{Err: inner}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	if err.Unwrap() != inner {
		t.Fatal("expected Unwrap to return inner error")
	}
}

func TestNilRebalancePlanError_Message(t *testing.T) {
	err := &NilRebalancePlanError{}
	expected := "transport: rebalance plan must not be nil"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}
