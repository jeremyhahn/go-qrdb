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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock rebalance service for embedded transport tests
// ---------------------------------------------------------------------------

type mockRebalanceService struct {
	getShardStatsFn       func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	createRebalancePlanFn func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeRebalanceFn    func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	getImbalanceReportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *mockRebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.getShardStatsFn != nil {
		return m.getShardStatsFn(ctx, shardID)
	}
	return &service.ShardStats{
		ShardID:      shardID,
		KeyCount:     100,
		ByteCount:    2048,
		LeaderID:     1,
		ReplicaCount: 3,
	}, nil
}

func (m *mockRebalanceService) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.createRebalancePlanFn != nil {
		return m.createRebalancePlanFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 1024},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}, nil
}

func (m *mockRebalanceService) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeRebalanceFn != nil {
		return m.executeRebalanceFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{
		MovesCompleted: len(plan.Moves),
		MovesFailed:    0,
		Duration:       2 * time.Second,
		FinalImbalance: 0.05,
	}, nil
}

func (m *mockRebalanceService) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.getImbalanceReportFn != nil {
		return m.getImbalanceReportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes: []service.NodeLoadInfo{
			{NodeID: "node-1", Address: "10.0.0.1:9100", ShardCount: 3, TotalKeys: 500, TotalBytes: 4096, LeaderCount: 2},
			{NodeID: "node-2", Address: "10.0.0.2:9100", ShardCount: 2, TotalKeys: 300, TotalBytes: 2048, LeaderCount: 1},
		},
		ImbalanceScore: 0.3,
		Hotspots:       []string{"node-1"},
	}, nil
}

// newRebalanceTransport creates a Transport with a RebalanceServicer for testing.
func newRebalanceTransport(t *testing.T) *Transport {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	rebalanceSvc := &mockRebalanceService{}

	tr, err := New(kvSvc, adminSvc, WithRebalanceService(rebalanceSvc))
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}
	return tr
}

// newRebalanceTransportWith creates a Transport with a custom RebalanceServicer.
func newRebalanceTransportWith(t *testing.T, svc service.RebalanceServicer) *Transport {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}

	tr, err := New(kvSvc, adminSvc, WithRebalanceService(svc))
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}
	return tr
}

// ---------------------------------------------------------------------------
// GetShardStats tests
// ---------------------------------------------------------------------------

func TestTransportRebalance_GetShardStats_Success(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	stats, err := tr.GetShardStats(ctx, 1)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", stats.ShardID)
	}
	if stats.KeyCount != 100 {
		t.Fatalf("expected KeyCount 100, got %d", stats.KeyCount)
	}
	if stats.ByteCount != 2048 {
		t.Fatalf("expected ByteCount 2048, got %d", stats.ByteCount)
	}
	if stats.LeaderID != 1 {
		t.Fatalf("expected LeaderID 1, got %d", stats.LeaderID)
	}
	if stats.ReplicaCount != 3 {
		t.Fatalf("expected ReplicaCount 3, got %d", stats.ReplicaCount)
	}
}

func TestTransportRebalance_GetShardStats_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.GetShardStats(ctx, 1)
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_GetShardStats_ServiceError(t *testing.T) {
	svc := &mockRebalanceService{
		getShardStatsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
			return nil, &service.ShardStatsError{ShardID: 42, Err: errors.New("lookup failed")}
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.GetShardStats(ctx, 42)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestTransportRebalance_GetShardStats_Closed(t *testing.T) {
	tr := newRebalanceTransport(t)
	_ = tr.Close()

	_, err := tr.GetShardStats(context.Background(), 1)
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// CreateRebalancePlan tests
// ---------------------------------------------------------------------------

func TestTransportRebalance_CreateRebalancePlan_Success(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	plan, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
		MaxMoves:      5,
		DryRun:        true,
		TargetBalance: 10.0,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(plan.Moves))
	}
	if plan.Moves[0].ShardID != 1 {
		t.Fatalf("expected move ShardID 1, got %d", plan.Moves[0].ShardID)
	}
	if plan.Moves[0].FromNode != "node1:9100" {
		t.Fatalf("expected FromNode node1:9100, got %s", plan.Moves[0].FromNode)
	}
	if plan.Moves[0].ToNode != "node2:9100" {
		t.Fatalf("expected ToNode node2:9100, got %s", plan.Moves[0].ToNode)
	}
	if plan.Moves[0].EstimatedBytes != 1024 {
		t.Fatalf("expected EstimatedBytes 1024, got %d", plan.Moves[0].EstimatedBytes)
	}
	if plan.EstimatedDuration != 5*time.Second {
		t.Fatalf("expected 5s duration, got %v", plan.EstimatedDuration)
	}
	if plan.CurrentImbalance != 0.3 {
		t.Fatalf("expected CurrentImbalance 0.3, got %f", plan.CurrentImbalance)
	}
}

func TestTransportRebalance_CreateRebalancePlan_VerifiesOptsMapping(t *testing.T) {
	var capturedOpts *service.RebalancePlanOptions
	svc := &mockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			capturedOpts = opts
			return &service.RebalancePlanResult{}, nil
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
		MaxMoves:      7,
		DryRun:        true,
		TargetBalance: 3.5,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if capturedOpts == nil {
		t.Fatal("expected service to receive options")
	}
	if capturedOpts.MaxMoves != 7 {
		t.Fatalf("expected MaxMoves 7, got %d", capturedOpts.MaxMoves)
	}
	if !capturedOpts.DryRun {
		t.Fatal("expected DryRun true")
	}
	if capturedOpts.TargetBalance != 3.5 {
		t.Fatalf("expected TargetBalance 3.5, got %f", capturedOpts.TargetBalance)
	}
}

func TestTransportRebalance_CreateRebalancePlan_NilRequest(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc, WithRebalanceService(&mockRebalanceService{}))
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.CreateRebalancePlan(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_CreateRebalancePlan_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_CreateRebalancePlan_ServiceError(t *testing.T) {
	svc := &mockRebalanceService{
		createRebalancePlanFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, &service.RebalancePlanError{Err: errors.New("plan generation failed")}
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestTransportRebalance_CreateRebalancePlan_Closed(t *testing.T) {
	tr := newRebalanceTransport(t)
	_ = tr.Close()

	_, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance tests
// ---------------------------------------------------------------------------

func TestTransportRebalance_ExecuteRebalance_Success(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	plan := &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 1, FromNode: "node1:9100", ToNode: "node2:9100", EstimatedBytes: 512},
			{ShardID: 2, FromNode: "node1:9100", ToNode: "node3:9100", EstimatedBytes: 256},
		},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.5,
	}

	result, err := tr.ExecuteRebalance(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 2 {
		t.Fatalf("expected 2 completed moves, got %d", result.MovesCompleted)
	}
	if result.MovesFailed != 0 {
		t.Fatalf("expected 0 failed moves, got %d", result.MovesFailed)
	}
	if result.Duration != 2*time.Second {
		t.Fatalf("expected 2s duration, got %v", result.Duration)
	}
	if result.FinalImbalance != 0.05 {
		t.Fatalf("expected FinalImbalance 0.05, got %f", result.FinalImbalance)
	}
}

func TestTransportRebalance_ExecuteRebalance_VerifiesPlanMapping(t *testing.T) {
	var capturedPlan *service.RebalancePlanResult
	svc := &mockRebalanceService{
		executeRebalanceFn: func(_ context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			capturedPlan = plan
			return &service.RebalanceExecutionResult{MovesCompleted: 1}, nil
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.ExecuteRebalance(ctx, &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{
			{ShardID: 5, FromNode: "a", ToNode: "b", EstimatedBytes: 999},
		},
		EstimatedDuration: 10 * time.Second,
		CurrentImbalance:  0.7,
	})
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if capturedPlan == nil {
		t.Fatal("expected service to receive plan")
	}
	if len(capturedPlan.Moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(capturedPlan.Moves))
	}
	if capturedPlan.Moves[0].ShardID != 5 {
		t.Fatalf("expected ShardID 5, got %d", capturedPlan.Moves[0].ShardID)
	}
	if capturedPlan.Moves[0].FromNode != "a" {
		t.Fatalf("expected FromNode 'a', got %s", capturedPlan.Moves[0].FromNode)
	}
	if capturedPlan.Moves[0].ToNode != "b" {
		t.Fatalf("expected ToNode 'b', got %s", capturedPlan.Moves[0].ToNode)
	}
	if capturedPlan.Moves[0].EstimatedBytes != 999 {
		t.Fatalf("expected EstimatedBytes 999, got %d", capturedPlan.Moves[0].EstimatedBytes)
	}
	if capturedPlan.EstimatedDuration != 10*time.Second {
		t.Fatalf("expected EstimatedDuration 10s, got %v", capturedPlan.EstimatedDuration)
	}
	if capturedPlan.CurrentImbalance != 0.7 {
		t.Fatalf("expected CurrentImbalance 0.7, got %f", capturedPlan.CurrentImbalance)
	}
}

func TestTransportRebalance_ExecuteRebalance_NilPlan(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	_, err := tr.ExecuteRebalance(ctx, nil)
	if err == nil {
		t.Fatal("expected NilRebalancePlanError")
	}
	var nilPlan *transport.NilRebalancePlanError
	if !errors.As(err, &nilPlan) {
		t.Fatalf("expected NilRebalancePlanError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_ExecuteRebalance_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.ExecuteRebalance(ctx, &transport.RebalancePlan{})
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_ExecuteRebalance_ServiceError(t *testing.T) {
	svc := &mockRebalanceService{
		executeRebalanceFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, &service.RebalanceExecuteError{Err: errors.New("execution failed")}
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.ExecuteRebalance(ctx, &transport.RebalancePlan{})
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestTransportRebalance_ExecuteRebalance_Closed(t *testing.T) {
	tr := newRebalanceTransport(t)
	_ = tr.Close()

	_, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetImbalanceReport tests
// ---------------------------------------------------------------------------

func TestTransportRebalance_GetImbalanceReport_Success(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	report, err := tr.GetImbalanceReport(ctx)
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(report.Nodes))
	}
	if report.Nodes[0].NodeID != "node-1" {
		t.Fatalf("expected first node ID node-1, got %s", report.Nodes[0].NodeID)
	}
	if report.Nodes[0].Address != "10.0.0.1:9100" {
		t.Fatalf("expected first node address 10.0.0.1:9100, got %s", report.Nodes[0].Address)
	}
	if report.Nodes[0].ShardCount != 3 {
		t.Fatalf("expected ShardCount 3, got %d", report.Nodes[0].ShardCount)
	}
	if report.Nodes[0].TotalKeys != 500 {
		t.Fatalf("expected TotalKeys 500, got %d", report.Nodes[0].TotalKeys)
	}
	if report.Nodes[0].TotalBytes != 4096 {
		t.Fatalf("expected TotalBytes 4096, got %d", report.Nodes[0].TotalBytes)
	}
	if report.Nodes[0].LeaderCount != 2 {
		t.Fatalf("expected LeaderCount 2, got %d", report.Nodes[0].LeaderCount)
	}
	if report.ImbalanceScore != 0.3 {
		t.Fatalf("expected ImbalanceScore 0.3, got %f", report.ImbalanceScore)
	}
	if len(report.Hotspots) != 1 || report.Hotspots[0] != "node-1" {
		t.Fatalf("expected hotspot node-1, got %v", report.Hotspots)
	}
}

func TestTransportRebalance_GetImbalanceReport_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.GetImbalanceReport(ctx)
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

func TestTransportRebalance_GetImbalanceReport_ServiceError(t *testing.T) {
	svc := &mockRebalanceService{
		getImbalanceReportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return nil, errors.New("report failed")
		},
	}
	tr := newRebalanceTransportWith(t, svc)
	ctx := context.Background()

	_, err := tr.GetImbalanceReport(ctx)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestTransportRebalance_GetImbalanceReport_Closed(t *testing.T) {
	tr := newRebalanceTransport(t)
	_ = tr.Close()

	_, err := tr.GetImbalanceReport(context.Background())
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle test
// ---------------------------------------------------------------------------

func TestTransportRebalance_FullLifecycle(t *testing.T) {
	tr := newRebalanceTransport(t)
	ctx := context.Background()

	// Get shard stats.
	stats, err := tr.GetShardStats(ctx, 1)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 1 {
		t.Fatalf("expected ShardID 1, got %d", stats.ShardID)
	}

	// Get imbalance report.
	report, err := tr.GetImbalanceReport(ctx)
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if report.ImbalanceScore <= 0 {
		t.Fatalf("expected positive imbalance score, got %f", report.ImbalanceScore)
	}

	// Create rebalance plan.
	plan, err := tr.CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
		MaxMoves: 10,
		DryRun:   false,
	})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) == 0 {
		t.Fatal("expected at least one move in plan")
	}

	// Execute rebalance.
	result, err := tr.ExecuteRebalance(ctx, plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != len(plan.Moves) {
		t.Fatalf("expected %d completed moves, got %d", len(plan.Moves), result.MovesCompleted)
	}
}
