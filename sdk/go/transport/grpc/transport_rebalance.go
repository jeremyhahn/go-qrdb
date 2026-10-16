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
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/rebalanceconv"
)

// Compile-time interface assertion.
var _ transport.RebalanceClient = (*Transport)(nil)

// GetShardStats returns statistics for a single shard via the gRPC
// RebalanceGetShardStats RPC.
func (t *Transport) GetShardStats(ctx context.Context, shardID uint64) (*transport.ShardStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RebalanceGetShardStats(ctx, &pb.RebalanceShardStatsRequest{ShardId: shardID})
	if err != nil {
		return nil, mapGRPCError("GetShardStats", err)
	}
	return &transport.ShardStatsResponse{
		ShardID:      resp.GetShardId(),
		KeyCount:     resp.GetKeyCount(),
		ByteCount:    resp.GetByteCount(),
		LeaderID:     resp.GetLeaderId(),
		ReplicaCount: int(resp.GetReplicaCount()),
	}, nil
}

// CreateRebalancePlan generates a rebalance plan via the gRPC
// RebalanceCreatePlan RPC.
func (t *Transport) CreateRebalancePlan(ctx context.Context, req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RebalanceCreatePlan(ctx, &pb.RebalancePlanRequest{
		MaxMoves:      int32(req.MaxMoves),
		DryRun:        req.DryRun,
		TargetBalance: req.TargetBalance,
	})
	if err != nil {
		return nil, mapGRPCError("CreateRebalancePlan", err)
	}
	return protoToRebalancePlan(resp), nil
}

// ExecuteRebalance executes a rebalance plan via the gRPC RebalanceExecute RPC.
func (t *Transport) ExecuteRebalance(ctx context.Context, plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RebalanceExecute(ctx, rebalancePlanToProto(plan))
	if err != nil {
		return nil, mapGRPCError("ExecuteRebalance", err)
	}
	return rebalanceconv.ResultFromProto(resp), nil
}

// GetImbalanceReport returns the cluster balance report via the gRPC
// RebalanceGetImbalanceReport RPC.
func (t *Transport) GetImbalanceReport(ctx context.Context) (*transport.ImbalanceReport, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RebalanceGetImbalanceReport(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("GetImbalanceReport", err)
	}
	nodes := make([]transport.NodeLoad, len(resp.GetNodes()))
	for i, n := range resp.GetNodes() {
		nodes[i] = transport.NodeLoad{
			NodeID:      n.GetNodeId(),
			Address:     n.GetAddress(),
			ShardCount:  int(n.GetShardCount()),
			TotalKeys:   n.GetTotalKeys(),
			TotalBytes:  n.GetTotalBytes(),
			LeaderCount: int(n.GetLeaderCount()),
		}
	}
	return &transport.ImbalanceReport{
		Nodes:          nodes,
		ImbalanceScore: resp.GetImbalanceScore(),
		Hotspots:       resp.GetHotspots(),
	}, nil
}

// protoToRebalancePlan converts a proto RebalancePlan to its transport form.
func protoToRebalancePlan(p *pb.RebalancePlan) *transport.RebalancePlan {
	if p == nil {
		return nil
	}
	moves := make([]transport.RebalanceMove, len(p.GetMoves()))
	for i, m := range p.GetMoves() {
		moves[i] = transport.RebalanceMove{
			ShardID:        m.GetShardId(),
			FromNode:       m.GetFromNode(),
			ToNode:         m.GetToNode(),
			EstimatedBytes: m.GetEstimatedBytes(),
		}
	}
	return &transport.RebalancePlan{
		Moves:             moves,
		EstimatedDuration: time.Duration(p.GetEstimatedDurationNs()),
		CurrentImbalance:  p.GetCurrentImbalance(),
	}
}

// rebalancePlanToProto converts a transport RebalancePlan to its proto form.
func rebalancePlanToProto(p *transport.RebalancePlan) *pb.RebalancePlan {
	if p == nil {
		return &pb.RebalancePlan{}
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
