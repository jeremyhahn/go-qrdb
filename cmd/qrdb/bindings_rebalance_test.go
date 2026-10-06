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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

func newTestRebalanceBinding(t *testing.T, svc service.RebalanceServicer) *RebalanceBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(), embedded.WithRebalanceService(svc))
	return NewRebalanceBinding(testLogger(t), conn)
}

func TestRebalanceBinding_GetShardStats(t *testing.T) {
	var gotShard uint64
	b := newTestRebalanceBinding(t, &cliMockRebalanceService{
		getShardStatsFn: func(_ context.Context, shardID uint64) (*service.ShardStats, error) {
			gotShard = shardID
			return &service.ShardStats{ShardID: shardID, KeyCount: 42, ByteCount: 4096, LeaderID: 2, ReplicaCount: 3}, nil
		},
	})
	stats, err := b.GetShardStats(7)
	if err != nil {
		t.Fatalf("GetShardStats: %v", err)
	}
	if gotShard != 7 || stats.ShardID != 7 || stats.KeyCount != 42 || stats.ByteCount != 4096 || stats.ReplicaCount != 3 {
		t.Fatalf("shard %d stats = %+v", gotShard, stats)
	}
}

func TestRebalanceBinding_PlanThenExecute(t *testing.T) {
	var executed *service.RebalancePlanResult
	b := newTestRebalanceBinding(t, &cliMockRebalanceService{
		executeRebalanceFn: func(_ context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			executed = plan
			return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves), FinalImbalance: 0.01}, nil
		},
	})

	plan, err := b.CreatePlan(&transport.RebalancePlanRequest{})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].FromNode != "node1:9100" || plan.Moves[0].ToNode != "node2:9100" {
		t.Fatalf("plan = %+v", plan)
	}

	result, err := b.Execute(plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.MovesCompleted != 1 || result.MovesFailed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if executed == nil || len(executed.Moves) != 1 || executed.Moves[0].ShardID != 1 {
		t.Fatalf("service executed %+v, want the plan CreatePlan returned", executed)
	}
}

func TestRebalanceBinding_GetImbalanceReport(t *testing.T) {
	b := newTestRebalanceBinding(t, &cliMockRebalanceService{})
	report, err := b.GetImbalanceReport()
	if err != nil {
		t.Fatalf("GetImbalanceReport: %v", err)
	}
	if report.ImbalanceScore != 0.3 || len(report.Nodes) != 2 || len(report.Hotspots) != 1 || report.Hotspots[0] != "node-1" {
		t.Fatalf("report = %+v", report)
	}
}

// TestRebalanceBinding_ServiceErrorsSurface proves every binding returns the
// service's failure instead of a zero-value success.
func TestRebalanceBinding_ServiceErrorsSurface(t *testing.T) {
	boom := errors.New("raft: no leader")
	b := newTestRebalanceBinding(t, &cliMockRebalanceService{
		getShardStatsFn: func(context.Context, uint64) (*service.ShardStats, error) { return nil, boom },
		createRebalancePlanFn: func(context.Context, *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, boom
		},
		executeRebalanceFn: func(context.Context, *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, boom
		},
		getImbalanceReportFn: func(context.Context) (*service.ImbalanceReportResult, error) { return nil, boom },
	})
	if s, err := b.GetShardStats(1); err == nil || s != nil {
		t.Errorf("GetShardStats = %+v, %v; want error", s, err)
	}
	if p, err := b.CreatePlan(&transport.RebalancePlanRequest{}); err == nil || p != nil {
		t.Errorf("CreatePlan = %+v, %v; want error", p, err)
	}
	if r, err := b.Execute(&transport.RebalancePlan{}); err == nil || r != nil {
		t.Errorf("Execute = %+v, %v; want error", r, err)
	}
	if r, err := b.GetImbalanceReport(); err == nil || r != nil {
		t.Errorf("GetImbalanceReport = %+v, %v; want error", r, err)
	}
}

func TestRebalanceBinding_RefusesWhenDisconnected(t *testing.T) {
	b := NewRebalanceBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if _, err := b.GetShardStats(1); err == nil {
		t.Error("GetShardStats succeeded without a connection")
	}
	if _, err := b.CreatePlan(&transport.RebalancePlanRequest{}); err == nil {
		t.Error("CreatePlan succeeded without a connection")
	}
	if _, err := b.Execute(&transport.RebalancePlan{}); err == nil {
		t.Error("Execute succeeded without a connection")
	}
	if _, err := b.GetImbalanceReport(); err == nil {
		t.Error("GetImbalanceReport succeeded without a connection")
	}
}
