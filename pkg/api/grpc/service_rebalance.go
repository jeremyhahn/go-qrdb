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

package grpc

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Rebalance conversion helpers
// ---------------------------------------------------------------------------

// rebalancePlanToProto converts a service-layer RebalancePlanResult to its
// protobuf representation.
func rebalancePlanToProto(p *service.RebalancePlanResult) *pb.RebalancePlan {
	if p == nil {
		return nil
	}
	moves := make([]*pb.RebalanceMove, len(p.Moves))
	for i := range p.Moves {
		moves[i] = &pb.RebalanceMove{
			ShardId:        p.Moves[i].ShardID,
			FromNode:       p.Moves[i].FromNode,
			ToNode:         p.Moves[i].ToNode,
			EstimatedBytes: p.Moves[i].EstimatedBytes,
		}
	}
	return &pb.RebalancePlan{
		Moves:               moves,
		EstimatedDurationNs: int64(p.EstimatedDuration),
		CurrentImbalance:    p.CurrentImbalance,
	}
}

// protoToRebalancePlan converts a proto RebalancePlan to a service-layer
// RebalancePlanResult.
func protoToRebalancePlan(p *pb.RebalancePlan) *service.RebalancePlanResult {
	if p == nil {
		return nil
	}
	moves := make([]service.RebalanceMovePlan, len(p.GetMoves()))
	for i, m := range p.GetMoves() {
		moves[i] = service.RebalanceMovePlan{
			ShardID:        m.GetShardId(),
			FromNode:       m.GetFromNode(),
			ToNode:         m.GetToNode(),
			EstimatedBytes: m.GetEstimatedBytes(),
		}
	}
	return &service.RebalancePlanResult{
		Moves:             moves,
		EstimatedDuration: time.Duration(p.GetEstimatedDurationNs()),
		CurrentImbalance:  p.GetCurrentImbalance(),
	}
}

// ---------------------------------------------------------------------------
// Rebalance handlers
// ---------------------------------------------------------------------------

// RebalanceGetShardStats returns statistics for a single shard.
func (s *qrdbServiceServer) RebalanceGetShardStats(ctx context.Context, req *pb.RebalanceShardStatsRequest) (*pb.ShardStats, error) {
	if s.rebalanceSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "rebalance"})
	}
	stats, err := s.rebalanceSvc.GetShardStats(ctx, req.GetShardId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.ShardStats{
		ShardId:      stats.ShardID,
		KeyCount:     stats.KeyCount,
		ByteCount:    stats.ByteCount,
		LeaderId:     stats.LeaderID,
		ReplicaCount: int32(stats.ReplicaCount),
	}, nil
}

// RebalanceCreatePlan generates a shard move plan to balance the cluster.
func (s *qrdbServiceServer) RebalanceCreatePlan(ctx context.Context, req *pb.RebalancePlanRequest) (*pb.RebalancePlan, error) {
	if s.rebalanceSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "rebalance"})
	}
	plan, err := s.rebalanceSvc.CreateRebalancePlan(ctx, &service.RebalancePlanOptions{
		MaxMoves:      int(req.GetMaxMoves()),
		DryRun:        req.GetDryRun(),
		TargetBalance: req.GetTargetBalance(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return rebalancePlanToProto(plan), nil
}

// RebalanceExecute executes a previously generated rebalance plan.
func (s *qrdbServiceServer) RebalanceExecute(ctx context.Context, req *pb.RebalancePlan) (*pb.RebalanceExecutionResponse, error) {
	if s.rebalanceSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "rebalance"})
	}
	result, err := s.rebalanceSvc.ExecuteRebalance(ctx, protoToRebalancePlan(req))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.RebalanceExecutionResponse{
		MovesCompleted: int32(result.MovesCompleted),
		MovesFailed:    int32(result.MovesFailed),
		DurationNs:     int64(result.Duration),
		FinalImbalance: result.FinalImbalance,
	}, nil
}

// RebalanceGetImbalanceReport returns the cluster's current load distribution.
func (s *qrdbServiceServer) RebalanceGetImbalanceReport(ctx context.Context, _ *emptypb.Empty) (*pb.ImbalanceReport, error) {
	if s.rebalanceSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "rebalance"})
	}
	report, err := s.rebalanceSvc.GetImbalanceReport(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	nodes := make([]*pb.NodeLoad, len(report.Nodes))
	for i := range report.Nodes {
		nodes[i] = &pb.NodeLoad{
			NodeId:      report.Nodes[i].NodeID,
			Address:     report.Nodes[i].Address,
			ShardCount:  int32(report.Nodes[i].ShardCount),
			TotalKeys:   report.Nodes[i].TotalKeys,
			TotalBytes:  report.Nodes[i].TotalBytes,
			LeaderCount: int32(report.Nodes[i].LeaderCount),
		}
	}
	return &pb.ImbalanceReport{
		Nodes:          nodes,
		ImbalanceScore: report.ImbalanceScore,
		Hotspots:       report.Hotspots,
	}, nil
}
