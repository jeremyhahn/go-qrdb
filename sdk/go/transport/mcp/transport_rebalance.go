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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RebalanceClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// RebalanceClient implementation (pending MCP tool definitions)
// ---------------------------------------------------------------------------

// GetShardStats returns statistics for a single shard including key count,
// byte count, leader, and replica count. This method is not yet implemented
// for the MCP transport and requires corresponding MCP tool definitions.
func (t *Transport) GetShardStats(ctx context.Context, shardID uint64) (*transport.ShardStatsResponse, error) {
	return nil, &transport.NotImplementedError{Method: "GetShardStats"}
}

// CreateRebalancePlan analyzes shard distribution across nodes and generates
// an optimal move plan to balance the cluster. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) CreateRebalancePlan(ctx context.Context, req *transport.RebalancePlanRequest) (*transport.RebalancePlan, error) {
	return nil, &transport.NotImplementedError{Method: "CreateRebalancePlan"}
}

// ExecuteRebalance executes the given rebalance plan by moving shards between
// nodes. This method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) ExecuteRebalance(ctx context.Context, plan *transport.RebalancePlan) (*transport.RebalanceResult, error) {
	return nil, &transport.NotImplementedError{Method: "ExecuteRebalance"}
}

// GetImbalanceReport returns the current cluster balance report including
// per-node load distribution and hotspot identification. This method is not
// yet implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) GetImbalanceReport(ctx context.Context) (*transport.ImbalanceReport, error) {
	return nil, &transport.NotImplementedError{Method: "GetImbalanceReport"}
}
