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

package quic

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/internal/rebalancepb"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// rebalancePlanToProto converts a service.RebalancePlanResult to its protobuf
// representation.
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
		Skipped:             rebalanceSkippedToProto(p.Skipped),
	}
}

// rebalanceSkippedToProto carries the planner's refused moves and their
// reasons onto the wire, so an operator sees why a plan is short.
func rebalanceSkippedToProto(skipped []service.RebalanceSkippedMove) []*pb.RebalanceSkippedMove {
	if len(skipped) == 0 {
		return nil
	}
	out := make([]*pb.RebalanceSkippedMove, len(skipped))
	for i, s := range skipped {
		out[i] = &pb.RebalanceSkippedMove{
			ShardId:  s.ShardID,
			FromNode: s.FromNode,
			ToNode:   s.ToNode,
			Reason:   s.Reason,
		}
		if s.Err != nil {
			out[i].Error = s.Err.Error()
		}
	}
	return out
}

// protoToRebalancePlan converts a proto RebalancePlan to its service-layer
// representation.
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

// handleRebalanceGetShardStats returns statistics for a single shard.
func (s *Server) handleRebalanceGetShardStats(ctx context.Context, payload []byte) ([]byte, error) {
	if s.rebalanceSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "rebalance"}
	}

	var req pb.RebalanceShardStatsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	stats, err := s.rebalanceSvc.GetShardStats(ctx, req.GetShardId())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ShardStats{
		ShardId:      stats.ShardID,
		KeyCount:     stats.KeyCount,
		ByteCount:    stats.ByteCount,
		LeaderId:     stats.LeaderID,
		ReplicaCount: int32(stats.ReplicaCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRebalanceCreatePlan generates a shard move plan to balance the cluster.
func (s *Server) handleRebalanceCreatePlan(ctx context.Context, payload []byte) ([]byte, error) {
	if s.rebalanceSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "rebalance"}
	}

	var req pb.RebalancePlanRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	plan, err := s.rebalanceSvc.CreateRebalancePlan(ctx, &service.RebalancePlanOptions{
		MaxMoves:      int(req.GetMaxMoves()),
		DryRun:        req.GetDryRun(),
		TargetBalance: req.GetTargetBalance(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(rebalancePlanToProto(plan))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRebalanceExecute executes a previously generated rebalance plan.
func (s *Server) handleRebalanceExecute(ctx context.Context, payload []byte) ([]byte, error) {
	if s.rebalanceSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "rebalance"}
	}

	var req pb.RebalancePlan
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.rebalanceSvc.ExecuteRebalance(ctx, protoToRebalancePlan(&req))
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(rebalancepb.ExecutionToProto(result))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRebalanceGetImbalanceReport returns the cluster's current load distribution.
func (s *Server) handleRebalanceGetImbalanceReport(ctx context.Context, _ []byte) ([]byte, error) {
	if s.rebalanceSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "rebalance"}
	}

	report, err := s.rebalanceSvc.GetImbalanceReport(ctx)
	if err != nil {
		return nil, err
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
	resp, err := proto.Marshal(&pb.ImbalanceReport{
		Nodes:          nodes,
		ImbalanceScore: report.ImbalanceScore,
		Hotspots:       report.Hotspots,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
