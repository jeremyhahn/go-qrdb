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

// Round-trip tests for the Rebalance surface (transport.RebalanceClient),
// closing the unix-vs-grpc SDK transport parity gap: the CLI's
// "qrdb rebalance" commands previously panicked over the default (unix)
// transport because RebalanceClient was implemented only by the gRPC and
// REST transports.

package unix

import (
	"context"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockRebalanceSvc
// ---------------------------------------------------------------------------

type mockRebalanceSvc struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

var _ service.RebalanceServicer = (*mockRebalanceSvc)(nil)

func (m *mockRebalanceSvc) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx, shardID)
	}
	return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
}

func (m *mockRebalanceSvc) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.planFn != nil {
		return m.planFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves:             []service.RebalanceMovePlan{{ShardID: 1, FromNode: "node-a", ToNode: "node-b", EstimatedBytes: 1024}},
		EstimatedDuration: 5 * time.Second,
		CurrentImbalance:  0.3,
	}, nil
}

func (m *mockRebalanceSvc) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves), Duration: 3 * time.Second, FinalImbalance: 0.05}, nil
}

func (m *mockRebalanceSvc) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.reportFn != nil {
		return m.reportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes:          []service.NodeLoadInfo{{NodeID: "n1", Address: "127.0.0.1:7001", ShardCount: 3, TotalKeys: 300, TotalBytes: 12288, LeaderCount: 2}},
		ImbalanceScore: 0.2,
		Hotspots:       []string{},
	}, nil
}

// ---------------------------------------------------------------------------
// GetShardStats
// ---------------------------------------------------------------------------

func TestUnixRT_GetShardStats(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(&mockRebalanceSvc{}))
	defer cleanup()

	stats, err := tr.GetShardStats(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if stats.ShardID != 7 || stats.KeyCount != 100 {
		t.Errorf("got %+v, want shardID=7 keyCount=100", stats)
	}
}

func TestUnixRT_GetShardStats_Error(t *testing.T) {
	svc := &mockRebalanceSvc{statsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(svc))
	defer cleanup()

	if _, err := tr.GetShardStats(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// CreateRebalancePlan
// ---------------------------------------------------------------------------

func TestUnixRT_CreateRebalancePlan(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(&mockRebalanceSvc{}))
	defer cleanup()

	plan, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{MaxMoves: 5})
	if err != nil {
		t.Fatalf("CreateRebalancePlan: %v", err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].ShardID != 1 {
		t.Errorf("got %+v, want 1 move for shard 1", plan)
	}
}

func TestUnixRT_CreateRebalancePlan_Error(t *testing.T) {
	svc := &mockRebalanceSvc{planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(svc))
	defer cleanup()

	if _, err := tr.CreateRebalancePlan(context.Background(), &transport.RebalancePlanRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ExecuteRebalance
// ---------------------------------------------------------------------------

func TestUnixRT_ExecuteRebalance(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(&mockRebalanceSvc{}))
	defer cleanup()

	result, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{
		Moves: []transport.RebalanceMove{{ShardID: 1, FromNode: "a", ToNode: "b"}},
	})
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if result.MovesCompleted != 1 {
		t.Errorf("got %+v, want movesCompleted=1", result)
	}
}

func TestUnixRT_ExecuteRebalance_Error(t *testing.T) {
	svc := &mockRebalanceSvc{executeFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(svc))
	defer cleanup()

	if _, err := tr.ExecuteRebalance(context.Background(), &transport.RebalancePlan{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetImbalanceReport
// ---------------------------------------------------------------------------

func TestUnixRT_GetImbalanceReport(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(&mockRebalanceSvc{}))
	defer cleanup()

	report, err := tr.GetImbalanceReport(context.Background())
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].NodeID != "n1" {
		t.Errorf("got %+v, want 1 node n1", report)
	}
}

func TestUnixRT_GetImbalanceReport_Error(t *testing.T) {
	svc := &mockRebalanceSvc{reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithRebalanceService(svc))
	defer cleanup()

	if _, err := tr.GetImbalanceReport(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// protoToRebalancePlan / rebalancePlanToProto nil guards
// ---------------------------------------------------------------------------

func TestUnix_ProtoToRebalancePlan_Nil(t *testing.T) {
	if got := protoToRebalancePlan(nil); got != nil {
		t.Errorf("protoToRebalancePlan(nil) = %+v, want nil", got)
	}
}

func TestUnix_RebalancePlanToProto_Nil(t *testing.T) {
	got := rebalancePlanToProto(nil)
	if got == nil || len(got.Moves) != 0 {
		t.Errorf("rebalancePlanToProto(nil) = %+v, want empty non-nil plan", got)
	}
}
