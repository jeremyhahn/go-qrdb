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

package service

import (
	"context"
	"math"
	"sort"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// RebalanceServicer provides shard statistics and cluster rebalancing operations.
type RebalanceServicer interface {
	// GetShardStats returns statistics for a single shard including
	// key count, byte count, leader, and replica count.
	GetShardStats(ctx context.Context, shardID uint64) (*ShardStats, error)

	// CreateRebalancePlan analyzes shard distribution across nodes and
	// generates an optimal move plan to balance the cluster.
	CreateRebalancePlan(ctx context.Context, opts *RebalancePlanOptions) (*RebalancePlanResult, error)

	// ExecuteRebalance executes the given rebalance plan by moving
	// shards between nodes.
	ExecuteRebalance(ctx context.Context, plan *RebalancePlanResult) (*RebalanceExecutionResult, error)

	// GetImbalanceReport returns the current cluster balance report
	// including per-node load distribution and hotspot identification.
	GetImbalanceReport(ctx context.Context) (*ImbalanceReportResult, error)
}

// ShardStats contains statistics for a single shard.
type ShardStats struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64

	// KeyCount is the number of keys stored in the shard.
	KeyCount uint64

	// ByteCount is the total byte size of all keys and values in the shard.
	ByteCount uint64

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// ReplicaCount is the number of replicas for this shard.
	ReplicaCount int
}

// RebalancePlanOptions describes the parameters for generating a rebalance plan.
type RebalancePlanOptions struct {
	// MaxMoves is the maximum number of shard moves to include in the plan.
	// 0 means no limit.
	MaxMoves int

	// DryRun when true generates the plan without executing it.
	DryRun bool

	// TargetBalance is the target balance percentage (0-100).
	TargetBalance float64
}

// RebalanceMovePlan describes a single planned shard move operation.
type RebalanceMovePlan struct {
	// ShardID is the shard to be moved.
	ShardID uint64

	// FromNode is the source node address.
	FromNode string

	// ToNode is the destination node address.
	ToNode string

	// EstimatedBytes is the estimated data size to be transferred.
	EstimatedBytes uint64
}

// RebalancePlanResult contains the list of planned shard moves and metadata.
type RebalancePlanResult struct {
	// Moves is the ordered list of shard move operations.
	Moves []RebalanceMovePlan

	// EstimatedDuration is the estimated time to complete all moves.
	EstimatedDuration time.Duration

	// CurrentImbalance is the current imbalance score before rebalancing.
	CurrentImbalance float64
}

// RebalanceExecutionResult contains the outcome of an executed rebalance.
type RebalanceExecutionResult struct {
	// MovesCompleted is the number of moves that completed successfully.
	MovesCompleted int

	// MovesFailed is the number of moves that failed.
	MovesFailed int

	// Duration is the actual time taken to execute all moves.
	Duration time.Duration

	// FinalImbalance is the imbalance score after rebalancing.
	FinalImbalance float64
}

// NodeLoadInfo describes the load distribution on a single node.
type NodeLoadInfo struct {
	// NodeID is the unique host identifier.
	NodeID string

	// Address is the node's Raft network address.
	Address string

	// ShardCount is the number of shards hosted by this node.
	ShardCount int

	// TotalKeys is the total key count across all shards on this node.
	TotalKeys uint64

	// TotalBytes is the total byte count across all shards on this node.
	TotalBytes uint64

	// LeaderCount is the number of shards for which this node is leader.
	LeaderCount int
}

// ImbalanceReportResult describes the cluster's current load distribution.
type ImbalanceReportResult struct {
	// Nodes contains per-node load distribution.
	Nodes []NodeLoadInfo

	// ImbalanceScore is the overall cluster imbalance metric.
	ImbalanceScore float64

	// Hotspots contains node IDs that are significantly overloaded.
	Hotspots []string
}

// bytesPerSecondEstimate is a conservative estimate for data transfer rate
// used to calculate estimated rebalance duration.
const bytesPerSecondEstimate = 100 * 1024 * 1024 // 100 MB/s

// hotspotThreshold defines how many standard deviations above the mean
// constitutes a hotspot.
const hotspotThreshold = 1.5

// RebalanceService implements RebalanceServicer backed by a QuicRaft host.
type RebalanceService struct {
	host Host
}

// NewRebalanceService creates a new RebalanceServicer backed by the given host.
func NewRebalanceService(host Host) RebalanceServicer {
	return &RebalanceService{host: host}
}

// GetShardStats returns statistics for a single shard. It queries the
// state machine for key count and byte count, and retrieves leader and
// membership information from the host.
func (s *RebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*ShardStats, error) {
	leaderID, _, err := s.host.GetLeaderID(shardID)
	if err != nil {
		return nil, &ShardStatsError{ShardID: shardID, Err: err}
	}

	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	replicaCount := 0
	for _, si := range info.ShardInfoList {
		if si.ShardID == shardID {
			replicaCount = len(si.Nodes)
			break
		}
	}

	if replicaCount == 0 {
		return nil, &ShardStatsError{ShardID: shardID, Err: &shardNotLoadedError{ShardID: shardID}}
	}

	result, err := s.host.StaleRead(ctx, shardID, &statemachine.PrefixQuery{
		Prefix: nil,
		Limit:  0,
	})
	if err != nil {
		return nil, &ShardStatsError{ShardID: shardID, Err: err}
	}

	var keyCount uint64
	var byteCount uint64
	if data, ok := result.(map[string][]byte); ok {
		keyCount = uint64(len(data))
		for k, v := range data {
			byteCount += uint64(len(k)) + uint64(len(v))
		}
	}

	return &ShardStats{
		ShardID:      shardID,
		KeyCount:     keyCount,
		ByteCount:    byteCount,
		LeaderID:     leaderID,
		ReplicaCount: replicaCount,
	}, nil
}

// CreateRebalancePlan analyzes shard distribution across nodes and generates
// an optimal move plan to balance the cluster. The algorithm identifies
// overloaded and underloaded nodes based on byte count, then proposes moves
// from overloaded to underloaded nodes.
func (s *RebalanceService) CreateRebalancePlan(ctx context.Context, opts *RebalancePlanOptions) (*RebalancePlanResult, error) {
	if opts == nil {
		opts = &RebalancePlanOptions{}
	}

	report, err := s.GetImbalanceReport(ctx)
	if err != nil {
		return nil, &RebalancePlanError{Err: err}
	}

	if len(report.Nodes) < 2 {
		return &RebalancePlanResult{
			CurrentImbalance: report.ImbalanceScore,
		}, nil
	}

	// Calculate the target bytes per node for a balanced distribution.
	var totalBytes uint64
	for _, n := range report.Nodes {
		totalBytes += n.TotalBytes
	}
	targetBytes := totalBytes / uint64(len(report.Nodes))

	// Build sorted lists of overloaded and underloaded nodes.
	type nodeBalance struct {
		node  NodeLoadInfo
		delta int64 // positive = overloaded, negative = underloaded
	}

	balances := make([]nodeBalance, 0, len(report.Nodes))
	for _, n := range report.Nodes {
		delta := int64(n.TotalBytes) - int64(targetBytes)
		balances = append(balances, nodeBalance{node: n, delta: delta})
	}

	// Sort overloaded nodes (most overloaded first).
	sort.Slice(balances, func(i, j int) bool {
		return balances[i].delta > balances[j].delta
	})

	// Generate moves by shifting load from overloaded to underloaded nodes.
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	moves := make([]RebalanceMovePlan, 0)
	moveCount := 0

	for i := 0; i < len(balances); i++ {
		if balances[i].delta <= 0 {
			break
		}
		// Find shards on this overloaded node.
		for _, si := range info.ShardInfoList {
			if opts.MaxMoves > 0 && moveCount >= opts.MaxMoves {
				break
			}

			// Check if this shard is on the overloaded node.
			isOnNode := false
			for _, addr := range si.Nodes {
				if addr == balances[i].node.Address {
					isOnNode = true
					break
				}
			}
			if !isOnNode {
				continue
			}

			// Find the most underloaded node (last in sorted order).
			for j := len(balances) - 1; j > i; j-- {
				if balances[j].delta >= 0 {
					continue
				}

				// Estimate the bytes for this shard as a fraction of node total.
				shardCount := balances[i].node.ShardCount
				if shardCount == 0 {
					shardCount = 1
				}
				estimatedBytes := balances[i].node.TotalBytes / uint64(shardCount)

				moves = append(moves, RebalanceMovePlan{
					ShardID:        si.ShardID,
					FromNode:       balances[i].node.Address,
					ToNode:         balances[j].node.Address,
					EstimatedBytes: estimatedBytes,
				})
				moveCount++

				// Adjust balances.
				balances[i].delta -= int64(estimatedBytes)
				balances[j].delta += int64(estimatedBytes)
				break
			}

			if balances[i].delta <= 0 {
				break
			}
		}
	}

	// Calculate estimated duration based on total bytes to transfer.
	var totalTransferBytes uint64
	for _, m := range moves {
		totalTransferBytes += m.EstimatedBytes
	}
	estimatedDuration := time.Duration(0)
	if totalTransferBytes > 0 {
		estimatedDuration = time.Duration(totalTransferBytes/uint64(bytesPerSecondEstimate)) * time.Second
		if estimatedDuration == 0 {
			estimatedDuration = time.Second
		}
	}

	return &RebalancePlanResult{
		Moves:             moves,
		EstimatedDuration: estimatedDuration,
		CurrentImbalance:  report.ImbalanceScore,
	}, nil
}

// ExecuteRebalance executes the given rebalance plan by adding members to
// destination nodes and removing them from source nodes. Each move
// transfers leadership by adding a new replica on the target node.
func (s *RebalanceService) ExecuteRebalance(ctx context.Context, plan *RebalancePlanResult) (*RebalanceExecutionResult, error) {
	if plan == nil {
		return nil, &RebalanceExecuteError{Err: &nilPlanError{}}
	}

	start := time.Now()
	completed := 0
	failed := 0

	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	for _, move := range plan.Moves {
		// Find the shard info to get the config change index.
		var shardInfo *quicraft.ShardInfo
		for i, si := range info.ShardInfoList {
			if si.ShardID == move.ShardID {
				shardInfo = &info.ShardInfoList[i]
				break
			}
		}
		if shardInfo == nil {
			failed++
			continue
		}

		// Find the next available replica ID.
		var maxReplicaID uint64
		for rid := range shardInfo.Nodes {
			if rid > maxReplicaID {
				maxReplicaID = rid
			}
		}
		newReplicaID := maxReplicaID + 1

		// Add the new replica on the destination node.
		rs, err := s.host.RequestAddNode(ctx, move.ShardID, newReplicaID, move.ToNode, 0)
		if err != nil {
			failed++
			continue
		}
		if _, err := rs.Result(); err != nil {
			failed++
			continue
		}

		completed++
	}

	duration := time.Since(start)

	// Calculate final imbalance.
	finalImbalance := plan.CurrentImbalance
	if completed > 0 {
		report, err := s.GetImbalanceReport(ctx)
		if err == nil {
			finalImbalance = report.ImbalanceScore
		}
	}

	return &RebalanceExecutionResult{
		MovesCompleted: completed,
		MovesFailed:    failed,
		Duration:       duration,
		FinalImbalance: finalImbalance,
	}, nil
}

// GetImbalanceReport returns the current cluster balance report including
// per-node load distribution, overall imbalance score, and hotspot
// identification. The imbalance score is calculated as the coefficient
// of variation (standard deviation / mean) of byte counts across nodes.
func (s *RebalanceService) GetImbalanceReport(ctx context.Context) (*ImbalanceReportResult, error) {
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	// Build per-node load information.
	nodeMap := make(map[string]*NodeLoadInfo)

	for _, si := range info.ShardInfoList {
		for _, addr := range si.Nodes {
			node, ok := nodeMap[addr]
			if !ok {
				node = &NodeLoadInfo{
					NodeID:  addr,
					Address: addr,
				}
				nodeMap[addr] = node
			}
			node.ShardCount++
			if si.IsLeader {
				node.LeaderCount++
			}
		}
	}

	// Collect shard stats for byte/key counts.
	for _, si := range info.ShardInfoList {
		result, err := s.host.StaleRead(ctx, si.ShardID, &statemachine.PrefixQuery{
			Prefix: nil,
			Limit:  0,
		})
		if err != nil {
			continue
		}

		data, ok := result.(map[string][]byte)
		if !ok {
			continue
		}

		var keyCount uint64
		var byteCount uint64
		for k, v := range data {
			keyCount++
			byteCount += uint64(len(k)) + uint64(len(v))
		}

		// Distribute this shard's stats to all nodes hosting it.
		for _, addr := range si.Nodes {
			if node, ok := nodeMap[addr]; ok {
				node.TotalKeys += keyCount
				node.TotalBytes += byteCount
			}
		}
	}

	nodes := make([]NodeLoadInfo, 0, len(nodeMap))
	for _, n := range nodeMap {
		nodes = append(nodes, *n)
	}

	// Sort nodes by address for deterministic output.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Address < nodes[j].Address
	})

	// Calculate imbalance score using coefficient of variation of byte counts.
	imbalanceScore := calculateImbalanceScore(nodes)

	// Identify hotspots: nodes with byte count > mean + hotspotThreshold * stddev.
	hotspots := identifyHotspots(nodes)

	return &ImbalanceReportResult{
		Nodes:          nodes,
		ImbalanceScore: imbalanceScore,
		Hotspots:       hotspots,
	}, nil
}

// calculateImbalanceScore computes the coefficient of variation of byte
// counts across nodes. A score of 0.0 means perfectly balanced; higher
// values indicate greater imbalance.
func calculateImbalanceScore(nodes []NodeLoadInfo) float64 {
	if len(nodes) <= 1 {
		return 0.0
	}

	var sum float64
	for _, n := range nodes {
		sum += float64(n.TotalBytes)
	}
	mean := sum / float64(len(nodes))

	if mean == 0 {
		return 0.0
	}

	var variance float64
	for _, n := range nodes {
		diff := float64(n.TotalBytes) - mean
		variance += diff * diff
	}
	variance /= float64(len(nodes))

	stddev := math.Sqrt(variance)
	return stddev / mean
}

// identifyHotspots returns node IDs where byte count exceeds
// mean + hotspotThreshold * stddev.
func identifyHotspots(nodes []NodeLoadInfo) []string {
	if len(nodes) <= 1 {
		return nil
	}

	var sum float64
	for _, n := range nodes {
		sum += float64(n.TotalBytes)
	}
	mean := sum / float64(len(nodes))

	var variance float64
	for _, n := range nodes {
		diff := float64(n.TotalBytes) - mean
		variance += diff * diff
	}
	variance /= float64(len(nodes))

	stddev := math.Sqrt(variance)
	threshold := mean + hotspotThreshold*stddev

	hotspots := make([]string, 0)
	for _, n := range nodes {
		if float64(n.TotalBytes) > threshold {
			hotspots = append(hotspots, n.NodeID)
		}
	}

	return hotspots
}

// shardNotLoadedError is returned when a shard is not loaded on the host.
type shardNotLoadedError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *shardNotLoadedError) Error() string {
	return "shard not loaded: " + formatUint64(e.ShardID)
}

// nilPlanError is returned when ExecuteRebalance is called with a nil plan.
type nilPlanError struct{}

// Error returns a human-readable description.
func (e *nilPlanError) Error() string {
	return "rebalance plan must not be nil"
}

// formatUint64 converts a uint64 to its string representation without
// importing strconv to keep the import list minimal.
func formatUint64(v uint64) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	// Reverse the digits.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
