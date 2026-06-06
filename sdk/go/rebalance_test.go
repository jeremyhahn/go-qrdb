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

package sdk_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock rebalance service for SDK client tests
// ---------------------------------------------------------------------------

type mockRebalanceServiceSDK struct {
	getShardStatsFn       func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	createRebalancePlanFn func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeRebalanceFn    func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	getImbalanceReportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *mockRebalanceServiceSDK) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.getShardStatsFn != nil {
		return m.getShardStatsFn(ctx, shardID)
	}
	return &service.ShardStats{
		ShardID:      shardID,
		KeyCount:     50,
		ByteCount:    1024,
		LeaderID:     1,
		ReplicaCount: 3,
	}, nil
}

func (m *mockRebalanceServiceSDK) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.createRebalancePlanFn != nil {
		return m.createRebalancePlanFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 512},
		},
		EstimatedDuration: 3 * time.Second,
		CurrentImbalance:  0.4,
	}, nil
}

func (m *mockRebalanceServiceSDK) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeRebalanceFn != nil {
		return m.executeRebalanceFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{
		MovesCompleted: len(plan.Moves),
		MovesFailed:    0,
		Duration:       1 * time.Second,
		FinalImbalance: 0.02,
	}, nil
}

func (m *mockRebalanceServiceSDK) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.getImbalanceReportFn != nil {
		return m.getImbalanceReportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes: []service.NodeLoadInfo{
			{NodeID: "node-1", Address: "10.0.0.1:9100", ShardCount: 2, TotalKeys: 100, TotalBytes: 2048, LeaderCount: 1},
		},
		ImbalanceScore: 0.0,
		Hotspots:       nil,
	}, nil
}

// newRebalanceClient creates an SDK client with embedded transport and
// rebalance service wired in.
func newRebalanceClient(t *testing.T) *sdk.Client {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	rebalanceSvc := &mockRebalanceServiceSDK{}

	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithRebalanceService(rebalanceSvc))
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// Client.Rebalance() accessor
// ---------------------------------------------------------------------------

func TestClient_Rebalance_ReturnsRebalanceClient(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	rc := client.Rebalance()
	if rc == nil {
		t.Fatal("expected non-nil RebalanceClient")
	}
}

// ---------------------------------------------------------------------------
// GetShardStats through SDK client
// ---------------------------------------------------------------------------

func TestClient_Rebalance_GetShardStats_Success(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	ctx := context.Background()
	stats, err := client.Rebalance().GetShardStats(ctx, 42)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 42 {
		t.Fatalf("expected ShardID 42, got %d", stats.ShardID)
	}
	if stats.KeyCount != 50 {
		t.Fatalf("expected KeyCount 50, got %d", stats.KeyCount)
	}
	if stats.ByteCount != 1024 {
		t.Fatalf("expected ByteCount 1024, got %d", stats.ByteCount)
	}
	if stats.LeaderID != 1 {
		t.Fatalf("expected LeaderID 1, got %d", stats.LeaderID)
	}
	if stats.ReplicaCount != 3 {
		t.Fatalf("expected ReplicaCount 3, got %d", stats.ReplicaCount)
	}
}

func TestClient_Rebalance_GetShardStats_AfterClose(t *testing.T) {
	client := newRebalanceClient(t)
	_ = client.Close()

	_, err := client.Rebalance().GetShardStats(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CreateRebalancePlan through SDK client
// ---------------------------------------------------------------------------

func TestClient_Rebalance_CreateRebalancePlan_Success(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	ctx := context.Background()
	plan, err := client.Rebalance().CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
		MaxMoves: 10,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(plan.Moves))
	}
	if plan.EstimatedDuration != 3*time.Second {
		t.Fatalf("expected 3s duration, got %v", plan.EstimatedDuration)
	}
	if plan.CurrentImbalance != 0.4 {
		t.Fatalf("expected CurrentImbalance 0.4, got %f", plan.CurrentImbalance)
	}
}

func TestClient_Rebalance_CreateRebalancePlan_AfterClose(t *testing.T) {
	client := newRebalanceClient(t)
	_ = client.Close()

	_, err := client.Rebalance().CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance through SDK client
// ---------------------------------------------------------------------------

func TestClient_Rebalance_ExecuteRebalance_Success(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	ctx := context.Background()
	plan := &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 256},
		},
		EstimatedDuration: 2 * time.Second,
		CurrentImbalance:  0.5,
	}

	result, err := client.Rebalance().ExecuteRebalance(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Fatalf("expected 1 completed move, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed moves, got %d", result.MovesFailed)
	}
	if result.FinalImbalance != 0.02 {
		t.Fatalf("expected FinalImbalance 0.02, got %f", result.FinalImbalance)
	}
}

func TestClient_Rebalance_ExecuteRebalance_AfterClose(t *testing.T) {
	client := newRebalanceClient(t)
	_ = client.Close()

	_, err := client.Rebalance().ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetImbalanceReport through SDK client
// ---------------------------------------------------------------------------

func TestClient_Rebalance_GetImbalanceReport_Success(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	ctx := context.Background()
	report, err := client.Rebalance().GetImbalanceReport(ctx)
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(report.Nodes))
	}
	if report.Nodes[0].NodeID != "node-1" {
		t.Fatalf("expected NodeID node-1, got %s", report.Nodes[0].NodeID)
	}
	if report.ImbalanceScore != 0.0 {
		t.Fatalf("expected ImbalanceScore 0.0, got %f", report.ImbalanceScore)
	}
	if report.Hotspots != nil {
		t.Fatalf("expected nil hotspots, got %v", report.Hotspots)
	}
}

func TestClient_Rebalance_GetImbalanceReport_AfterClose(t *testing.T) {
	client := newRebalanceClient(t)
	_ = client.Close()

	_, err := client.Rebalance().GetImbalanceReport(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle through SDK client
// ---------------------------------------------------------------------------

func TestClient_Rebalance_FullLifecycle(t *testing.T) {
	client := newRebalanceClient(t)
	defer client.Close()

	ctx := context.Background()
	rc := client.Rebalance()

	// Get shard stats.
	stats, err := rc.GetShardStats(ctx, 1)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", stats.ShardID)
	}

	// Get imbalance report.
	report, err := rc.GetImbalanceReport(ctx)
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if report.Nodes == nil {
		t.Fatal("expected non-nil nodes in report")
	}

	// Create plan.
	plan, err := rc.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
		MaxMoves: 5,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	// Execute plan.
	result, err := rc.ExecuteRebalance(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != len(plan.Moves) {
		t.Fatalf("expected %d completed, got %d", len(plan.Moves), result.MovesCompleted)
	}
}
