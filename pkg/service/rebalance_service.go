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
	"errors"
	"log/slog"
	"math"
	"sort"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
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
// fields enable Phase 4c-2 dedicated-tenant Raft group MOVE operations. The
// optional raftHost field enables the observer-first membership change and
// live-leader routing moveTenantGroupShard performs for that MOVE -- see
// SetRaftHost. When placementStore is nil, ExecuteRebalance falls back to
// the generic path (moveGenericShard), leaving existing
// non-IsolationRaft behavior intact.
type RebalanceService struct {
	host           Host
	placementStore *placement.Store
	keyDeliverer   RebalanceKeyDeliverer
	barrierLookup  RebalanceBarrierLookup
	selfNodeID     string
	raftHost       RaftHost
	logger         *slog.Logger
}

// NewRebalanceService creates a new RebalanceServicer backed by the given host.
// Functional opts configure Phase 4c-2 dedicated-tenant move capabilities;
// an empty opts list produces the same generic-shard-only behaviour as before.
func NewRebalanceService(host Host, opts ...RebalanceServiceOption) RebalanceServicer {
	s := &RebalanceService{host: host, logger: slog.Default()}
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

// SetRaftHost injects the Raft membership-change handle used by
// moveTenantGroupShard to admit a MOVE's destination replica as a
// non-voting observer (RaftHost.AddObserver) and evict its source replica
// (RaftHost.RemoveFollowerNode), and by resolveMoveLeader to route a MOVE to
// the dedicated group's current Raft leader. Mirrors
// DatabaseService.SetRaftHost -- the daemon wires the SAME adapter instance
// into both services wherever IsolationRaft is configured (see start.go).
// When nil, moveTenantGroupShard updates placement only and relies on
// TenantShardReconciler to converge the Raft membership change
// asynchronously, exactly like DatabaseService.scaleUp/scaleDown when their
// RaftHost is unset.
func (s *RebalanceService) SetRaftHost(h RaftHost) { s.raftHost = h }

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
//   - Dedicated-tenant shard: routes through moveTenantGroupShard, which
//     routes to the group's current Raft leader, admits the destination
//     observer-first, delivers the barrier key, updates the placement, and
//     evicts the source. The placement update drives orphan teardown on the
//     source node via TenantShardReconciler. See moveTenantGroupShard's doc
//     comment for the full step sequence.
//
//   - Generic (non-dedicated) shard: moveGenericShard adds a replica on the
//     destination with RequestAddNodeWithIdentity, committing the identity the
//     system shard committed for the destination host. Generic shards share
//     the cluster-wide barrier key and need no key delivery. The system shard
//     itself, a destination already hosting the shard, and a destination with
//     no committed identity are refused with a typed error, which is logged
//     and counted as a failed move.
//
// When placementStore is nil all moves are treated as generic shards, so
// existing deployments without Phase 4c-2 wiring are unaffected.
//
// A *NotLeaderError from moveTenantGroupShard (the local node is not the
// dedicated group's current Raft leader) aborts the ENTIRE call immediately
// and is returned as-is (not wrapped, not counted as a failed move) rather
// than continuing to the plan's remaining moves: moveTenantGroupShard
// guarantees no mutation occurred before returning it, so the whole request
// is safe to retry in full, and returning it unwrapped lets the REST layer's
// tryForwardWrite (the same mechanism DatabaseService.ScaleForTenant relies
// on) proxy the ENTIRE rebalance-execute request to the leader via
// errors.As. Any moves already completed earlier in this same call are NOT
// rolled back -- they already committed -- so a forwarded retry that
// re-executes the full plan on the leader is idempotent for a plan whose
// moves are independent of one another (the common case; a plan mixing
// multiple dedicated groups with DIFFERENT leaders can still require more
// than one forwarding hop, which the loop-detection guard in
// forwardToLeader bounds to a single hop).
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
				var nle *NotLeaderError
				if errors.As(err, &nle) {
					return nil, err
				}
				s.logger.Warn("rebalance: move failed",
					slog.Uint64("shard_id", move.ShardID),
					slog.String("tenant", p.TenantID),
					slog.String("database", p.Database),
					slog.String("from_node", move.FromNode),
					slog.String("to_node", move.ToNode),
					slog.Any("error", err))
				failed++
				continue
			}
			completed++
			continue
		}

		// Generic (non-dedicated) path: RequestAddNodeWithIdentity only.
		if err := s.moveGenericShard(ctx, info, move); err != nil {
			s.logger.Warn("rebalance: move refused",
				slog.Uint64("shard_id", move.ShardID),
				slog.String("from_node", move.FromNode),
				slog.String("to_node", move.ToNode),
				slog.Any("error", err))
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

// moveGenericShard adds a replica of a non-dedicated shard on move.ToNode.
// The system shard is refused (*RebalanceSystemShardMoveError), as is a
// destination the shard's committed membership already names as a voter,
// observer or witness (*RebalanceTargetHostsReplicaError). The new replica ID
// is past every ID the membership has used, removed ones included. Its
// identity is the one the system shard committed for the destination host,
// found by its address; a destination no committed membership names is
// refused with the *MemberIdentityError ResolveMemberIdentity returns, since a
// rebalance has no operator to supply one.
func (s *RebalanceService) moveGenericShard(ctx context.Context, info *quicraft.NodeHostInfo, move RebalanceMovePlan) error {
	if move.ShardID == shard.ShardKV {
		return &RebalanceSystemShardMoveError{ShardID: move.ShardID, ToNode: move.ToNode}
	}
	var shardInfo *quicraft.ShardInfo
	for i, si := range info.ShardInfoList {
		if si.ShardID == move.ShardID {
			shardInfo = &info.ShardInfoList[i]
			break
		}
	}
	if shardInfo == nil {
		return quicraft.ErrShardNotFound
	}

	// The committed membership, not this host's view of its voters, is what
	// says whether the destination already holds a replica — as a voter, an
	// observer or a witness — and which replica IDs are spent: a removed ID
	// is never reused.
	m, err := s.host.SyncGetShardMembership(ctx, move.ShardID)
	if err != nil {
		return &cluster.MembershipReadError{ShardID: move.ShardID, Err: err}
	}
	var maxID uint64
	for _, roster := range []map[uint64]string{m.Nodes, m.Observers, m.Witnesses} {
		for rid, addr := range roster {
			if addr == move.ToNode {
				return &RebalanceTargetHostsReplicaError{ShardID: move.ShardID, ToNode: move.ToNode}
			}
			maxID = max(maxID, rid)
		}
	}
	for rid := range m.Removed {
		maxID = max(maxID, rid)
	}
	newReplicaID := maxID + 1

	identity, err := ResolveMemberIdentity(ctx, s.host, move.ShardID, newReplicaID, move.ToNode, "")
	if err != nil {
		return err
	}
	rs, err := s.host.RequestAddNodeWithIdentity(ctx, move.ShardID, newReplicaID, move.ToNode, identity, 0)
	if err != nil {
		return err
	}
	_, err = rs.Result()
	return err
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

// moveTenantGroupShard executes a single MOVE for a dedicated-tenant Raft
// group by composing the SAME observer-first primitives
// DatabaseService.ScaleForTenant uses (addObserverReplica /
// removeReplicaFromGroup / resolveGroupLeader, all defined in
// database_service.go) rather than duplicating scale-up/scale-down's
// ordering, quorum-safety, and leader-routing logic: a MOVE is exactly a
// scale-up of the destination followed by a scale-down of the source.
//
//  0. Resolve the dedicated group's CURRENT Raft leader (resolveMoveLeader)
//     and, when the local node is not it, fail with *NotLeaderError so the
//     REST layer's tryForwardWrite can proxy the whole rebalance-execute
//     request there -- go-quicraft hard-rejects AddObserver/
//     RemoveFollowerNode on any non-leader member.
//  1. Wait for the shard to report a known Raft leader (mirrors
//     DatabaseService.scaleUp/scaleDown's identical wait; AddObserver/
//     RemoveFollowerNode are hard-rejected until it is known).
//  2. Locate the source replica in the placement entry.
//  3. Assign the destination a new replica ID (max existing + 1) and build
//     it as a NON-VOTING OBSERVER (placement.ReplicaStateObserver): admitting
//     it directly as a voter can transiently break the group's existing
//     quorum before it has caught up -- see addObserverReplica's doc comment.
//  4. addObserverReplica: persist placement (dst added) -> best-effort key
//     delivery -> RaftHost.AddObserver. Placement MUST be persisted before
//     key delivery -- see addObserverReplica.
//  5. removeReplicaFromGroup: persist placement (src removed) ->
//     RaftHost.RemoveFollowerNode.
//
// The destination is promoted from observer to a full voting member
// asynchronously, off this request path, by TenantShardReconciler's
// Observer -> ObserverReady -> Active state machine -- exactly as for a
// scale-up replica.
//
// Failure semantics:
//   - Step 0 (leader-routing): no mutation has occurred; safe to forward
//     and retry in full.
//   - Steps 1–3 fail: no placement change has been made; the move is
//     counted as failed and the cluster is left in its pre-move state.
//   - Step 4 (add-placement/add-observer) fails: see addObserverReplica.
//     An add-observer failure leaves the just-persisted placement in place
//     (dst added) for a retry to pick up; the source is never touched.
//   - Step 5 (remove-placement/remove-node) fails: the placement already
//     reflects the intended post-move state (dst in, src out).
//     TenantShardReconciler will eventually stop the src shard when it
//     reconciles the updated placement. The move is counted as failed so
//     the caller can alert or retry.
func (s *RebalanceService) moveTenantGroupShard(ctx context.Context, move RebalanceMovePlan, p *placement.Placement) error {
	// Step -1: a database mid-drop is never rebalanced. p is the caller's
	// snapshot of the placement entry the whole MOVE plan was computed
	// against; if it already shows Dropping=true, admitting or removing a
	// replica here would mutate quota and Raft membership while the
	// database is being torn down (go-qrdb#482 round 3, finding 5).
	if p.Dropping {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "dropping",
			Err:      &database.DroppingError{Name: p.Database, TenantID: p.TenantID},
		}
	}

	// Step 0: route to the dedicated group's current Raft leader. When the
	// source is ALSO the local node, resolveMoveLeader additionally
	// transfers leadership away before reporting itself as not-the-leader
	// -- see its doc comment.
	if isLeader, leaderAddr := s.resolveMoveLeader(ctx, move, p); !isLeader {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "leader-routing",
			Err:      &NotLeaderError{ShardID: p.ShardID, LeaderAddress: leaderAddr},
		}
	}

	// Step 1: fail closed before any mutation rather than racing an
	// election that has not yet settled.
	if s.raftHost != nil {
		if err := waitForShardLeaderKnown(ctx, s.raftHost, p.ShardID, defaultScaleLeaderWaitTimeout, defaultScaleLeaderWaitInterval); err != nil {
			return &RebalanceMoveError{
				ShardID:  move.ShardID,
				TenantID: p.TenantID,
				Step:     "wait-leader",
				Err:      err,
			}
		}
	}

	// Step 2: locate the source replica.
	srcReplica, ok := findReplicaByAddr(p, move.FromNode)
	if !ok {
		return &RebalanceMoveError{
			ShardID:  move.ShardID,
			TenantID: p.TenantID,
			Step:     "find-source",
			Err:      &sourceReplicaNotFoundError{ShardID: move.ShardID, FromNode: move.FromNode},
		}
	}

	// Step 3: assign the destination replica ID and build it as an
	// observer. NodeID == RaftAddr by cluster convention. DataRoot is left
	// empty (never copied from an existing replica) so the destination
	// resolves its OWN local volume-pool root via LocalRootResolver when the
	// reconciler materializes it -- exactly like PlaceAdditional's scale-up
	// replicas (placement/scheduler.go). A Placement's replicas do NOT share
	// a single DataRoot: persistPlacement records a real absolute path only
	// for the replica local to the node that created the database, and ""
	// for every other replica (BUG 1 fix; see
	// placementRaftInfoSource.databaseAndPlacementInfo's doc comment).
	// Reusing another replica's DataRoot here (the bug this comment
	// replaces) planted the destination's on-disk shard under that OTHER
	// replica's node-local path instead of the destination's own volume
	// pool.
	newReplicaID := placementMaxReplicaID(p) + 1
	dstReplica := placement.Replica{
		ReplicaID: newReplicaID,
		NodeID:    move.ToNode,
		RaftAddr:  move.ToNode,
		DataRoot:  "",
		State:     placement.ReplicaStateObserver,
	}

	// Step 4: admit the destination, observer-first.
	updated, kerr, placementFailed, err := addObserverReplica(
		ctx, s.raftHost, s.placementStore, s.keyDeliverer, s.barrierLookup, p, dstReplica, s.selfNodeID)
	if kerr != nil {
		s.logger.Warn("clusterkeys: key distribution failed for rebalance MOVE destination, follower will reconcile",
			slog.String("tenant", p.TenantID),
			slog.String("database", p.Database),
			slog.Uint64("shard_id", move.ShardID),
			slog.String("dst_node", move.ToNode),
			slog.Any("error", kerr))
	}
	if err != nil {
		step := "add-observer"
		if placementFailed {
			step = "add-placement"
		}
		return &RebalanceMoveError{ShardID: move.ShardID, TenantID: p.TenantID, Step: step, Err: err}
	}

	// Step 5: evict the source.
	if _, placementFailed, err := removeReplicaFromGroup(ctx, s.raftHost, s.placementStore, updated, srcReplica.ReplicaID); err != nil {
		step := "remove-node"
		if placementFailed {
			step = "remove-placement"
		}
		return &RebalanceMoveError{ShardID: move.ShardID, TenantID: p.TenantID, Step: step, Err: err}
	}

	return nil
}

// resolveMoveLeader determines whether the local node can safely execute
// this MOVE locally, so moveTenantGroupShard can either proceed or report
// the address of a node the caller should forward the whole
// rebalance-execute request to instead.
//
// The ordinary case delegates to resolveGroupLeader (database_service.go),
// the same routing logic DatabaseService.resolveScaleLeader uses, so a
// scale and a MOVE membership change are routed identically; see
// resolveScaleLeader's doc comment for the full return-value rationale,
// which applies unchanged here.
//
// A MOVE has one additional case resolveScaleLeader never needs to handle:
// the source replica (move.FromNode) may be the LOCAL node itself, which
// may ALSO be the group's current leader. Removing the current leader via
// RemoveFollowerNode without first transferring leadership away is unsafe
// (see transferLeadershipAwayFromRemoved's doc comment) -- but RaftHost only
// ever exposes the LOCAL node's Raft state, so once this node transfers
// leadership away from itself it can no longer complete the removal
// itself; go-quicraft would reject a RemoveFollowerNode call from a node
// that is no longer the leader. So when the source IS this node AND this
// node IS the leader, resolveMoveLeader proactively transfers leadership to
// another voting replica right here -- BEFORE any placement mutation, so
// it is always safe to retry the whole request -- and reports itself as
// NOT the leader, with the address of the NEW leader. The REST layer's
// tryForwardWrite then retries the ENTIRE MOVE there, where the source is
// a different (non-executing) replica and this case can never recur for
// the same request.
//
// KNOWN LIMITATION: the REST layer's forwardToLeader tolerates at most ONE
// forwarding hop per client request (a deliberate, deployment-wide loop
// guard shared by every forwarded write -- KV PUT/DELETE, database scale,
// and this MOVE alike). A MOVE request that originates from a node that is
// NOT a member of the dedicated group at all first takes resolveGroupLeader's
// "guess any member" hop; if that guessed member happens to ALSO be both the
// live leader AND the MOVE's source, resolveMoveLeader's own transfer-and-
// forward above would need a SECOND hop, which forwardToLeader rejects with
// *LeaderForwardLoopError even though this is not a genuine loop. This was
// observed directly in practice (not merely theorized) while developing this
// fix. Until resolved, callers SHOULD target the MOVE's source or another
// existing member of the dedicated group directly (never an arbitrary node)
// to stay within resolveMoveLeader's own worst case of exactly one hop --
// see the integration test isolation_raft_rebalance_test.go for the
// concrete pattern. Fixing this generally would mean either teaching
// forwardToLeader to tolerate a small BOUNDED number of hops (a
// deployment-wide behavior change affecting every forwarded write, not
// merely rebalance) or giving the "guess any member" fallback a way to
// guess the actual current leader instead of an arbitrary member -- both
// are non-trivial, deployment-wide changes intentionally left out of this
// fix's scope pending an explicit decision.
func (s *RebalanceService) resolveMoveLeader(ctx context.Context, move RebalanceMovePlan, p *placement.Placement) (isLocalLeader bool, leaderAddr string) {
	isLocalLeader, leaderAddr, _ = resolveGroupLeader(ctx, s.raftHost, s.selfNodeID, p)
	if !isLocalLeader || s.raftHost == nil {
		return isLocalLeader, leaderAddr
	}

	srcReplica, ok := findReplicaByAddr(p, move.FromNode)
	if !ok || (srcReplica.NodeID != s.selfNodeID && srcReplica.RaftAddr != s.selfNodeID) {
		// The source is not this node -- the ordinary case.
		return true, ""
	}

	if terr := transferLeadershipAwayFromRemoved(ctx, s.raftHost, p, p.ShardID, srcReplica.ReplicaID); terr != nil {
		// Transfer failed or timed out -- degrade to executing locally so
		// the ordinary AddObserver/RemoveFollowerNode error paths surface
		// the definitive failure, mirroring resolveGroupLeader's own
		// degrade-to-local behavior when the leader cannot be resolved.
		return true, ""
	}

	newIsLocalLeader, newLeaderAddr, _ := resolveGroupLeader(ctx, s.raftHost, s.selfNodeID, p)
	if newIsLocalLeader {
		// Should not happen (the transfer reported success), but degrade to
		// local execution rather than forward to an address that is
		// actually still this node.
		return true, ""
	}
	return false, newLeaderAddr
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

// placementMaxReplicaID returns the largest ReplicaID across all replicas in
// p. Returns 0 for an empty placement (the first replica will get ID 1). It
// delegates to placement.MaxReplicaID, which also backs
// DatabaseService.ScaleForTenant's replica-ID assignment.
func placementMaxReplicaID(p *placement.Placement) uint64 {
	return placement.MaxReplicaID(p)
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
