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

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
// The optional placementStore, keyDeliverer, barrierLookup, and selfNodeID
// fields enable Phase 4c-2 dedicated-tenant Raft group MOVE operations.
// When nil, ExecuteRebalance falls back to the generic system-shard path
// (RequestAddNode only), leaving existing non-IsolationRaft behavior intact.
type RebalanceService struct {
	host           Host
	placementStore *placement.Store
	keyDeliverer   RebalanceKeyDeliverer
	barrierLookup  RebalanceBarrierLookup
	selfNodeID     string
}

// NewRebalanceService creates a new RebalanceServicer backed by the given host.
// Functional opts configure Phase 4c-2 dedicated-tenant move capabilities;
// an empty opts list produces the same generic-shard-only behaviour as before.
func NewRebalanceService(host Host, opts ...RebalanceServiceOption) RebalanceServicer {
	s := &RebalanceService{host: host}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetRebalanceDeps injects the placement store, key deliverer, barrier lookup,
// and local node ID after construction. This mirrors the SetDistributor pattern
// used by DatabaseService: lifecycle.BuildServices constructs RebalanceService
// before the clusterkeys.Sender (which needs the LeaderAdminResolver built in
// start.go) is available, so post-construction injection is required.
//
// Passing nil for any argument disables the corresponding capability without
// affecting generic-shard rebalancing.
func (s *RebalanceService) SetRebalanceDeps(
	ps *placement.Store,
	d RebalanceKeyDeliverer,
	lookup RebalanceBarrierLookup,
	selfNodeID string,
) {
	s.placementStore = ps
	s.keyDeliverer = d
	s.barrierLookup = lookup
	s.selfNodeID = selfNodeID
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

// ExecuteRebalance executes the given rebalance plan. For each move it first
// consults the placement table (when available) to determine whether the shard
// belongs to a dedicated-tenant Raft group:
//
//   - Dedicated-tenant shard: routes through moveTenantGroupShard which
//     delivers the barrier key, calls RequestAddNode, updates the placement,
//     and calls RequestRemoveNode to evict the source. The placement update
//     drives orphan teardown on the source node via TenantShardReconciler.
//
//   - Generic (system) shard: unchanged path — calls RequestAddNode only, as
//     system shards share the cluster-wide barrier key and need no key delivery.
//
// When placementStore is nil all moves are treated as generic shards, so
// existing deployments without Phase 4c-2 wiring are unaffected.
func (s *RebalanceService) ExecuteRebalance(ctx context.Context, plan *RebalancePlanResult) (*RebalanceExecutionResult, error) {
	if plan == nil {
		return nil, &RebalanceExecuteError{Err: &nilPlanError{}}
	}

	start := time.Now()
	completed := 0
	failed := 0

	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	// Build a shardID→Placement index. A shard present in this index belongs to
	// a dedicated-tenant Raft group and requires the full tenant-aware MOVE.
	placementIndex := s.buildShardPlacementIndex(ctx)

	for _, move := range plan.Moves {
		// Dedicated-tenant path.
		if p, ok := placementIndex[move.ShardID]; ok {
			if err := s.moveTenantGroupShard(ctx, move, p); err != nil {
				failed++
				continue
			}
			completed++
			continue
		}

		// Generic (system-shard) path: RequestAddNode only.
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

		var maxID uint64
		for rid := range shardInfo.Nodes {
			if rid > maxID {
				maxID = rid
			}
		}
		newReplicaID := maxID + 1

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

// buildShardPlacementIndex lists all placements from the store and returns a
// map of ShardID → *Placement. Returns nil when placementStore is nil or when
// List fails (callers treat a nil map as an empty index).
func (s *RebalanceService) buildShardPlacementIndex(ctx context.Context) map[uint64]*placement.Placement {
	if s.placementStore == nil {
		return nil
	}
	placements, err := s.placementStore.List(ctx)
	if err != nil {
		return nil
	}
	idx := make(map[uint64]*placement.Placement, len(placements))
	for _, p := range placements {
		idx[p.ShardID] = p
	}
	return idx
}

// moveTenantGroupShard executes a single MOVE for a dedicated-tenant Raft group:
//
//  a. Locate the source replica in the placement entry.
//  b. Assign a new replica ID (max existing ID + 1).
//  c. Deliver the tenant barrier key to the destination node (when configured).
//  d. Call RequestAddNode to admit the destination into the Raft group.
//  e. Update the placement to include the destination and exclude the source.
//  f. Call RequestRemoveNode to evict the source from Raft consensus.
//
// Failure semantics:
//   - Steps a–d fail: no placement change has been made; the move is counted
//     as failed and the cluster is left in its pre-move state.
//   - Step e (placement-update) fails: RequestAddNode has committed the
//     destination as a Raft member. The function returns a RebalanceMoveError
//     so the caller counts the move as failed, but does NOT attempt to remove
//     the newly added destination — doing so would require another Raft
//     config-change and could leave the group in a worse state on error.
//   - Step f (remove-node) fails: the placement already reflects the intended
//     post-move state (dst in, src out). TenantShardReconciler will eventually
//     stop the src shard when it reconciles the updated placement. The move is
//     counted as failed so the caller can alert or retry.
func (s *RebalanceService) moveTenantGroupShard(ctx context.Context, move RebalanceMovePlan, p *placement.Placement) error {
	// Step a: locate the source replica.
	srcReplica, ok := findReplicaByAddr(p, move.FromNode)
	if !ok {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "find-source",
			Err:      &sourceReplicaNotFoundError{ShardID: move.ShardID, FromNode: move.FromNode},
		}
	}

	// Step b: assign destination replica ID.
	newReplicaID := placementMaxReplicaID(p) + 1

	// Build the destination Replica. NodeID == RaftAddr by cluster convention.
	dstReplica := placement.Replica{
		ReplicaID: newReplicaID,
		NodeID:    move.ToNode,
		RaftAddr:  move.ToNode,
		DataRoot:  placementSharedDataRoot(p),
	}

	// Step c: deliver the tenant barrier key to the destination.
	if s.keyDeliverer != nil && s.barrierLookup != nil {
		if err := s.deliverKeyToDst(ctx, p, dstReplica); err != nil {
			return &RebalanceMoveError{
				ShardID:  move.ShardID,
				TenantID: p.TenantID,
				Step:     "key-delivery",
				Err:      err,
			}
		}
	}

	// Step d: admit the destination into the Raft group.
	rs, err := s.host.RequestAddNode(ctx, move.ShardID, newReplicaID, move.ToNode, 0)
	if err != nil {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "add-node",
			Err:      err,
		}
	}
	if _, err := rs.Result(); err != nil {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "add-node-result",
			Err:      err,
		}
	}

	// Step e: update the placement — add dst, remove src.
	updated := clonePlacementWithReplica(p, dstReplica)
	updated = dropReplicaFromPlacement(updated, srcReplica.ReplicaID)
	if err := s.placementStore.Put(ctx, updated); err != nil {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "placement-update",
			Err:      err,
		}
	}

	// Step f: evict the source from Raft consensus.
	rsRm, err := s.host.RequestRemoveNode(ctx, move.ShardID, srcReplica.ReplicaID, 0)
	if err != nil {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "remove-node",
			Err:      err,
		}
	}
	if _, err := rsRm.Result(); err != nil {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "remove-node-result",
			Err:      err,
		}
	}

	return nil
}

// deliverKeyToDst ships the tenant barrier key to dstReplica. It constructs a
// single-replica Placement containing only the destination so that
// DistributeTenantKey delivers the key to exactly that one recipient (it skips
// the local node, identified by selfNodeID, which already holds the key as the
// current leader). autoUnseal is true when the tenant barrier runs under the
// system barrier strategy (system-managed); false for self-managed strategies.
func (s *RebalanceService) deliverKeyToDst(ctx context.Context, p *placement.Placement, dst placement.Replica) error {
	tb, err := s.barrierLookup.GetTenant(p.TenantID)
	if err != nil {
		return err
	}
	autoUnseal := tb.Barrier().ActiveStrategy() == seal.StrategySystemBarrier

	// A single-replica placement ensures DistributeTenantKey delivers to
	// exactly dst (selfNodeID != dst.NodeID, so the skip check passes).
	singleReplica := &placement.Placement{
		TenantID: p.TenantID,
		Database: p.Database,
		ShardID:  p.ShardID,
		RF:       1,
		Replicas: []placement.Replica{dst},
	}
	return s.keyDeliverer.DistributeTenantKey(ctx, singleReplica, tb.Barrier(), s.selfNodeID, autoUnseal)
}

// findReplicaByAddr returns the Replica whose NodeID or RaftAddr matches addr
// and true, or a zero Replica and false when no match is found.
func findReplicaByAddr(p *placement.Placement, addr string) (placement.Replica, bool) {
	for _, r := range p.Replicas {
		if r.NodeID == addr || r.RaftAddr == addr {
			return r, true
		}
	}
	return placement.Replica{}, false
}

// placementMaxReplicaID returns the largest ReplicaID across all replicas in p.
// Returns 0 for an empty placement (the first replica will get ID 1).
func placementMaxReplicaID(p *placement.Placement) uint64 {
	var max uint64
	for _, r := range p.Replicas {
		if r.ReplicaID > max {
			max = r.ReplicaID
		}
	}
	return max
}

// placementSharedDataRoot returns the DataRoot from the first replica in p.
// By cluster convention all replicas in a Placement share the same DataRoot
// value (set to the node's configured data directory by the scheduler).
func placementSharedDataRoot(p *placement.Placement) string {
	if len(p.Replicas) > 0 {
		return p.Replicas[0].DataRoot
	}
	return ""
}

// clonePlacementWithReplica returns a copy of p with r appended to Replicas.
// RF is updated to reflect the new replica count.
func clonePlacementWithReplica(p *placement.Placement, r placement.Replica) *placement.Placement {
	replicas := make([]placement.Replica, len(p.Replicas)+1)
	copy(replicas, p.Replicas)
	replicas[len(p.Replicas)] = r
	clone := *p
	clone.Replicas = replicas
	clone.RF = len(replicas)
	return &clone
}

// dropReplicaFromPlacement returns a copy of p with the replica identified by
// replicaID removed. RF is updated accordingly.
func dropReplicaFromPlacement(p *placement.Placement, replicaID uint64) *placement.Placement {
	replicas := make([]placement.Replica, 0, len(p.Replicas))
	for _, r := range p.Replicas {
		if r.ReplicaID != replicaID {
			replicas = append(replicas, r)
		}
	}
	clone := *p
	clone.Replicas = replicas
	clone.RF = len(replicas)
	return &clone
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
