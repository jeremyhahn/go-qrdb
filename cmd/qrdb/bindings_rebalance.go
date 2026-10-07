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

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// RebalanceBinding exposes shard rebalancing and load-distribution
// operations to the Wails frontend.
type RebalanceBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewRebalanceBinding creates a new RebalanceBinding backed by the given
// connection.
func NewRebalanceBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *RebalanceBinding {
	return &RebalanceBinding{logger: log, conn: conn}
}

// GetShardStats returns load statistics for a single shard.
func (b *RebalanceBinding) GetShardStats(shardID uint64) (*transport.ShardStatsResponse, error) {
	b.logger.Debug("GetShardStats called", "shardID", shardID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetShardStats failed", "error", err)
		return nil, err
	}
	rc, err := rebalanceClient(cl)
	if err != nil {
		b.logger.Error("GetShardStats failed", "error", err)
		return nil, err
	}
	stats, err := rc.GetShardStats(context.Background(), shardID)
	if err != nil {
		b.logger.Error("GetShardStats failed", "shardID", shardID, "error", err)
		return nil, err
	}
	b.logger.Debug("GetShardStats completed", "shardID", shardID)
	return stats, nil
}

// CreatePlan analyzes shard distribution across nodes and generates an
// optimal move plan to balance the cluster.
func (b *RebalanceBinding) CreatePlan(req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	b.logger.Debug("CreatePlan called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("CreatePlan failed", "error", err)
		return nil, err
	}
	rc, err := rebalanceClient(cl)
	if err != nil {
		b.logger.Error("CreatePlan failed", "error", err)
		return nil, err
	}
	plan, err := rc.CreateRebalancePlan(context.Background(), req)
	if err != nil {
		b.logger.Error("CreatePlan failed", "error", err)
		return nil, err
	}
	b.logger.Debug("CreatePlan completed", "moves", len(plan.Moves))
	return plan, nil
}

// Execute executes the given rebalance plan by moving shards between nodes.
func (b *RebalanceBinding) Execute(plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	b.logger.Debug("Execute called", "moves", len(plan.Moves))
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	rc, err := rebalanceClient(cl)
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	result, err := rc.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		b.logger.Error("Execute failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Execute completed", "movesCompleted", result.MovesCompleted, "movesFailed", result.MovesFailed)
	return result, nil
}

// GetImbalanceReport returns the current cluster balance report including
// per-node load distribution and hotspot identification.
func (b *RebalanceBinding) GetImbalanceReport() (*transport.ImbalanceReport, error) {
	b.logger.Debug("GetImbalanceReport called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetImbalanceReport failed", "error", err)
		return nil, err
	}
	rc, err := rebalanceClient(cl)
	if err != nil {
		b.logger.Error("GetImbalanceReport failed", "error", err)
		return nil, err
	}
	report, err := rc.GetImbalanceReport(context.Background())
	if err != nil {
		b.logger.Error("GetImbalanceReport failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetImbalanceReport completed", "imbalanceScore", report.ImbalanceScore)
	return report, nil
}
