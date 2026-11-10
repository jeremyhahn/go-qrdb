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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockRebalanceServiceQuic implements service.RebalanceServicer.
type mockRebalanceServiceQuic struct {
	statsFn   func(ctx context.Context, shardID uint64) (*service.ShardStats, error)
	planFn    func(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error)
	executeFn func(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error)
	reportFn  func(ctx context.Context) (*service.ImbalanceReportResult, error)
}

func (m *mockRebalanceServiceQuic) GetShardStats(ctx context.Context, shardID uint64) (*service.ShardStats, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx, shardID)
	}
	return &service.ShardStats{ShardID: shardID, KeyCount: 100, ByteCount: 4096, LeaderID: 1, ReplicaCount: 3}, nil
}

func (m *mockRebalanceServiceQuic) CreateRebalancePlan(ctx context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
	if m.planFn != nil {
		return m.planFn(ctx, opts)
	}
	return &service.RebalancePlanResult{
		Moves: []service.RebalanceMovePlan{{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 512}},
	}, nil
}

func (m *mockRebalanceServiceQuic) ExecuteRebalance(ctx context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, plan)
	}
	return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves), MovesFailed: 0}, nil
}

func (m *mockRebalanceServiceQuic) GetImbalanceReport(ctx context.Context) (*service.ImbalanceReportResult, error) {
	if m.reportFn != nil {
		return m.reportFn(ctx)
	}
	return &service.ImbalanceReportResult{
		Nodes:          []service.NodeLoadInfo{{NodeID: "n1", ShardCount: 3}},
		ImbalanceScore: 0.2,
	}, nil
}

func TestRebalancePlanToProtoQUIC_Nil(t *testing.T) {
	if got := rebalancePlanToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestProtoToRebalancePlanQUIC_Nil(t *testing.T) {
	if got := protoToRebalancePlan(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestHandleRebalanceGetShardStats_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 1})
	_, err := srv.handleRebalanceGetShardStats(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleRebalanceGetShardStats_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(&mockRebalanceServiceQuic{}))
	_, err := srv.handleRebalanceGetShardStats(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleRebalanceGetShardStats_Success(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(&mockRebalanceServiceQuic{}))
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 42})
	resp, err := srv.handleRebalanceGetShardStats(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceGetShardStats failed: %v", err)
	}
	var out pb.ShardStats
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetShardId() != 42 || out.GetKeyCount() != 100 || out.GetByteCount() != 4096 ||
		out.GetLeaderId() != 1 || out.GetReplicaCount() != 3 {
		t.Fatalf("stats = %+v", &out)
	}
}

func TestHandleRebalanceGetShardStats_ServiceError(t *testing.T) {
	sentinel := errors.New("stats failed")
	svc := &mockRebalanceServiceQuic{
		statsFn: func(_ context.Context, _ uint64) (*service.ShardStats, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalanceShardStatsRequest{ShardId: 1})
	if _, err := srv.handleRebalanceGetShardStats(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRebalanceCreatePlan_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{})
	_, err := srv.handleRebalanceCreatePlan(context.Background(), payload)
	assertNotConfigured(t, err)
}

// TestHandleRebalanceCreatePlan_Success pins the request mapping and every
// field rebalancePlanToProto carries onto the wire.
func TestHandleRebalanceCreatePlan_Success(t *testing.T) {
	var gotOpts *service.RebalancePlanOptions
	svc := &mockRebalanceServiceQuic{
		planFn: func(_ context.Context, opts *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			gotOpts = opts
			return &service.RebalancePlanResult{
				Moves:             []service.RebalanceMovePlan{{ShardID: 1, FromNode: "a", ToNode: "b", EstimatedBytes: 1024}},
				EstimatedDuration: 5 * time.Second,
				CurrentImbalance:  0.3,
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{MaxMoves: 10, DryRun: true, TargetBalance: 0.1})
	resp, err := srv.handleRebalanceCreatePlan(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceCreatePlan failed: %v", err)
	}
	if gotOpts == nil || gotOpts.MaxMoves != 10 || !gotOpts.DryRun || gotOpts.TargetBalance != 0.1 {
		t.Fatalf("plan options = %+v", gotOpts)
	}
	var out pb.RebalancePlan
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetMoves()) != 1 {
		t.Fatalf("expected 1 move, got %d", len(out.GetMoves()))
	}
	m := out.GetMoves()[0]
	if m.GetShardId() != 1 || m.GetFromNode() != "a" || m.GetToNode() != "b" || m.GetEstimatedBytes() != 1024 {
		t.Fatalf("move = %+v", m)
	}
	if out.GetEstimatedDurationNs() != int64(5*time.Second) || out.GetCurrentImbalance() != 0.3 {
		t.Fatalf("plan = %+v", &out)
	}
}

func TestHandleRebalanceCreatePlan_ServiceError(t *testing.T) {
	sentinel := errors.New("plan failed")
	svc := &mockRebalanceServiceQuic{
		planFn: func(_ context.Context, _ *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlanRequest{})
	if _, err := srv.handleRebalanceCreatePlan(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRebalanceExecute_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.RebalancePlan{})
	_, err := srv.handleRebalanceExecute(context.Background(), payload)
	assertNotConfigured(t, err)
}

// TestHandleRebalanceExecute_Success pins every field protoToRebalancePlan
// hands the service.
func TestHandleRebalanceExecute_Success(t *testing.T) {
	var gotPlan *service.RebalancePlanResult
	svc := &mockRebalanceServiceQuic{
		executeFn: func(_ context.Context, plan *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			gotPlan = plan
			return &service.RebalanceExecutionResult{MovesCompleted: len(plan.Moves)}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlan{
		Moves:               []*pb.RebalanceMove{{ShardId: 3, FromNode: "x", ToNode: "y", EstimatedBytes: 512}},
		EstimatedDurationNs: int64(2 * time.Second),
		CurrentImbalance:    0.25,
	})
	resp, err := srv.handleRebalanceExecute(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRebalanceExecute failed: %v", err)
	}
	want := service.RebalanceMovePlan{ShardID: 3, FromNode: "x", ToNode: "y", EstimatedBytes: 512}
	if gotPlan == nil || len(gotPlan.Moves) != 1 || gotPlan.Moves[0] != want ||
		gotPlan.EstimatedDuration != 2*time.Second || gotPlan.CurrentImbalance != 0.25 {
		t.Fatalf("plan = %+v", gotPlan)
	}
	var out pb.RebalanceExecutionResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetMovesCompleted() != 1 {
		t.Fatalf("expected moves_completed=1, got %d", out.GetMovesCompleted())
	}
}

func TestHandleRebalanceExecute_ServiceError(t *testing.T) {
	sentinel := errors.New("execute failed")
	svc := &mockRebalanceServiceQuic{
		executeFn: func(_ context.Context, _ *service.RebalancePlanResult) (*service.RebalanceExecutionResult, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	payload, _ := proto.Marshal(&pb.RebalancePlan{Moves: []*pb.RebalanceMove{{ShardId: 1}}})
	if _, err := srv.handleRebalanceExecute(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleRebalanceGetImbalanceReport_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	_, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil)
	assertNotConfigured(t, err)
}

func TestHandleRebalanceGetImbalanceReport_Success(t *testing.T) {
	svc := &mockRebalanceServiceQuic{
		reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return &service.ImbalanceReportResult{
				Nodes: []service.NodeLoadInfo{{
					NodeID: "n1", Address: "10.0.0.1:1", ShardCount: 3, TotalKeys: 7, TotalBytes: 9, LeaderCount: 2,
				}},
				ImbalanceScore: 0.2,
				Hotspots:       []string{"n1"},
			}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	resp, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleRebalanceGetImbalanceReport failed: %v", err)
	}
	var out pb.ImbalanceReport
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(out.GetNodes()))
	}
	n := out.GetNodes()[0]
	if n.GetNodeId() != "n1" || n.GetAddress() != "10.0.0.1:1" || n.GetShardCount() != 3 ||
		n.GetTotalKeys() != 7 || n.GetTotalBytes() != 9 || n.GetLeaderCount() != 2 {
		t.Fatalf("node = %+v", n)
	}
	if out.GetImbalanceScore() != 0.2 || len(out.GetHotspots()) != 1 || out.GetHotspots()[0] != "n1" {
		t.Fatalf("report = %+v", &out)
	}
}

func TestHandleRebalanceGetImbalanceReport_ServiceError(t *testing.T) {
	sentinel := errors.New("report failed")
	svc := &mockRebalanceServiceQuic{
		reportFn: func(_ context.Context) (*service.ImbalanceReportResult, error) {
			return nil, sentinel
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithRebalanceService(svc))
	if _, err := srv.handleRebalanceGetImbalanceReport(context.Background(), nil); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}
