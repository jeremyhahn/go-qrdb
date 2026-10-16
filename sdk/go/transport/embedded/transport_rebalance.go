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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RebalanceClient = (*Transport)(nil)

// WithRebalanceService injects a RebalanceServicer into the transport.
func WithRebalanceService(svc service.RebalanceServicer) Option {
	return func(t *Transport) {
		t.rebalanceSvc = svc
	}
}

// ---------------------------------------------------------------------------
// RebalanceClient implementation
// ---------------------------------------------------------------------------

// GetShardStats returns statistics for a single shard by delegating to
// the injected RebalanceServicer. Returns NilServiceError when no
// rebalance service has been configured.
func (t *Transport) GetShardStats(ctx context.Context, shardID uint64) (*transport.ShardStatsResponse, error) {
	if err := t.gate("GetShardStats"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetShardStats"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.rebalanceSvc == nil {
		return nil, &NilServiceError{Service: "RebalanceServicer"}
	}
	stats, err := t.rebalanceSvc.GetShardStats(ctx, shardID)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.ShardStatsResponse{
		ShardID:      stats.ShardID,
		KeyCount:     stats.KeyCount,
		ByteCount:    stats.ByteCount,
		LeaderID:     stats.LeaderID,
		ReplicaCount: stats.ReplicaCount,
	}, nil
}

// CreateRebalancePlan generates a rebalance plan by delegating to the
// injected RebalanceServicer. Returns NilServiceError when no rebalance
// service has been configured.
func (t *Transport) CreateRebalancePlan(ctx context.Context, req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	if err := t.gate("CreateRebalancePlan"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CreateRebalancePlan"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.rebalanceSvc == nil {
		return nil, &NilServiceError{Service: "RebalanceServicer"}
	}
	if req == nil {
		return nil, &NilServiceError{Service: "RebalancePlanRequest"}
	}
	opts := &service.RebalancePlanOptions{
		MaxMoves:      req.MaxMoves,
		DryRun:        req.DryRun,
		TargetBalance: req.TargetBalance,
	}
	plan, err := t.rebalanceSvc.CreateRebalancePlan(ctx, opts)
	if err != nil {
		return nil, translateError(err)
	}
	moves := make([]transport.RebalanceMove, len(plan.Moves))
	for i, m := range plan.Moves {
		moves[i] = transport.RebalanceMove{
			ShardID:        m.ShardID,
			FromNode:       m.FromNode,
			ToNode:         m.ToNode,
			EstimatedBytes: m.EstimatedBytes,
		}
	}
	return &transport.RebalancePlan{
		Moves:             moves,
		EstimatedDuration: plan.EstimatedDuration,
		CurrentImbalance:  plan.CurrentImbalance,
	}, nil
}

// ExecuteRebalance executes the given rebalance plan by delegating to
// the injected RebalanceServicer. Returns NilServiceError when no
// rebalance service has been configured.
func (t *Transport) ExecuteRebalance(ctx context.Context, plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	if err := t.gate("ExecuteRebalance"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ExecuteRebalance"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.rebalanceSvc == nil {
		return nil, &NilServiceError{Service: "RebalanceServicer"}
	}
	if plan == nil {
		return nil, &transport.NilRebalancePlanError{}
	}
	svcMoves := make([]service.RebalanceMovePlan, len(plan.Moves))
	for i, m := range plan.Moves {
		svcMoves[i] = service.RebalanceMovePlan{
			ShardID:        m.ShardID,
			FromNode:       m.FromNode,
			ToNode:         m.ToNode,
			EstimatedBytes: m.EstimatedBytes,
		}
	}
	svcPlan := &service.RebalancePlanResult{
		Moves:             svcMoves,
		EstimatedDuration: plan.EstimatedDuration,
		CurrentImbalance:  plan.CurrentImbalance,
	}
	result, err := t.rebalanceSvc.ExecuteRebalance(ctx, svcPlan)
	if err != nil {
		return nil, translateError(err)
	}
	out := &transport.RebalanceResult{
		MovesCompleted: result.MovesCompleted,
		MovesFailed:    result.MovesFailed,
		Duration:       result.Duration,
		FinalImbalance: result.FinalImbalance,
	}
	for _, f := range result.Failures {
		out.Failures = append(out.Failures, transport.RebalanceMoveFailure(f))
	}
	return out, nil
}

// GetImbalanceReport returns the current cluster balance report by
// delegating to the injected RebalanceServicer. Returns NilServiceError
// when no rebalance service has been configured.
func (t *Transport) GetImbalanceReport(ctx context.Context) (*transport.ImbalanceReport, error) {
	if err := t.gate("GetImbalanceReport"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetImbalanceReport"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.rebalanceSvc == nil {
		return nil, &NilServiceError{Service: "RebalanceServicer"}
	}
	report, err := t.rebalanceSvc.GetImbalanceReport(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	nodes := make([]transport.NodeLoad, len(report.Nodes))
	for i, n := range report.Nodes {
		nodes[i] = transport.NodeLoad{
			NodeID:      n.NodeID,
			Address:     n.Address,
			ShardCount:  n.ShardCount,
			TotalKeys:   n.TotalKeys,
			TotalBytes:  n.TotalBytes,
			LeaderCount: n.LeaderCount,
		}
	}
	return &transport.ImbalanceReport{
		Nodes:          nodes,
		ImbalanceScore: report.ImbalanceScore,
		Hotspots:       report.Hotspots,
	}, nil
}
