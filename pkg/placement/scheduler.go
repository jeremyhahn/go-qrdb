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

package placement

import (
	"context"
	"sort"
)

// DefaultEpsilon is the default bounded-load slack. A node is considered "over
// target" once its load exceeds (1+DefaultEpsilon) times the cluster average;
// the scheduler prefers under-target nodes when selecting replicas.
const DefaultEpsilon = 0.20

// NodeLoad is a TopologyProvider's view of one cluster node and its current
// load. The scheduler ranks candidates by (Bytes, ShardCount, NodeID), so the
// richer the load signal a provider supplies, the better balanced the placement.
type NodeLoad struct {
	// NodeID is the cluster-unique node identifier (stable across calls).
	NodeID string

	// RaftAddr is the node's Raft network address.
	RaftAddr string

	// ShardCount is the number of shards the node currently hosts.
	ShardCount int

	// Bytes is the node's total stored bytes across its shards. It is the
	// primary load signal, mirroring the RebalanceService coefficient-of-
	// variation scoring; ShardCount breaks ties when bytes are equal (e.g. a
	// fresh cluster).
	Bytes uint64

	// Healthy reports whether the node is eligible to host a new replica.
	// Unhealthy nodes are never selected.
	Healthy bool
}

// TopologyProvider supplies the scheduler with the current cluster topology and
// per-node load. It is the single seam between the pure scheduling core and the
// live cluster, which keeps Place fully unit-testable with a mock provider.
type TopologyProvider interface {
	// Nodes returns a snapshot of all known cluster nodes and their load. The
	// returned slice is owned by the caller.
	Nodes() []NodeLoad
}

// PlaceRequest describes a single placement decision.
type PlaceRequest struct {
	// TenantID owns the database. Required.
	TenantID string

	// Database is the database name. Required.
	Database string

	// ShardID is the pre-allocated Raft shard ID for the group. Required
	// (must be non-zero).
	ShardID uint64

	// RF is the desired replication factor (number of voting replicas).
	// Callers clamp it to the cluster size before calling Place; Place fails
	// with ErrInsufficientNodes when fewer than RF healthy distinct nodes
	// exist.
	RF int

	// DataRootForNode maps a selected node's ID to the on-disk root the
	// replica should use on that node. Required.
	DataRootForNode func(nodeID string) string
}

// SchedulerOption customizes a Scheduler.
type SchedulerOption func(*Scheduler)

// WithEpsilon sets the bounded-load slack (must be >= 0). Values <= 0 leave the
// default in place.
func WithEpsilon(epsilon float64) SchedulerOption {
	return func(s *Scheduler) {
		if epsilon > 0 {
			s.epsilon = epsilon
		}
	}
}

// ReplicaFilter reports whether a replica of shardID may be placed on the
// node at raftAddr; a non-nil error excludes the node. The feature gate
// supplies one so a group is never placed on a node that cannot apply the
// commands the group uses.
type ReplicaFilter func(shardID uint64, raftAddr string) error

// WithReplicaFilter excludes every candidate node filter refuses from Place
// and PlaceAdditional.
func WithReplicaFilter(filter ReplicaFilter) SchedulerOption {
	return func(s *Scheduler) { s.filter = filter }
}

// Scheduler computes a Placement from a snapshot of cluster topology using a
// load-aware, bounded-load objective with anti-affinity (one voting replica per
// node). It is safe for concurrent use: it holds only an immutable provider
// reference and a scalar, and reads a fresh topology snapshot per Place call.
type Scheduler struct {
	topo    TopologyProvider
	epsilon float64
	filter  ReplicaFilter
}

// NewScheduler constructs a Scheduler over the given TopologyProvider. The
// provider must be non-nil.
func NewScheduler(topo TopologyProvider, opts ...SchedulerOption) (*Scheduler, error) {
	if topo == nil {
		return nil, ErrNilTopology
	}
	s := &Scheduler{topo: topo, epsilon: DefaultEpsilon}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// ClusterSize returns the number of known cluster nodes (healthy or not). It is
// the upper bound a caller uses to clamp a requested replication factor before
// calling Place.
func (s *Scheduler) ClusterSize() int {
	return len(s.topo.Nodes())
}

// Place computes a placement for req. It selects RF distinct, healthy,
// least-loaded nodes (anti-affinity: at most one voting replica per node),
// assigns replica IDs 1..RF in increasing load order, and chooses the
// least-loaded selected node as the leader.
//
// Selection is bounded-load: candidates are partitioned at the (1+epsilon)*avg
// load ceiling and drained least-loaded-first from the under-target set before
// the over-target set, so a hot node is never chosen while a colder distinct
// node remains. Ranking is deterministic — ties on (Bytes, ShardCount) break on
// the stable NodeID — so the same topology always yields the same placement.
//
// Place returns *InsufficientNodesError (matching ErrInsufficientNodes) when
// fewer than RF healthy distinct nodes are available.
func (s *Scheduler) Place(ctx context.Context, req PlaceRequest) (*Placement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.TenantID == "" {
		return nil, ErrEmptyTenant
	}
	if req.Database == "" {
		return nil, ErrEmptyDatabase
	}
	if req.ShardID == 0 {
		return nil, ErrZeroShard
	}
	if req.RF < 1 {
		return nil, ErrInvalidRF
	}
	if req.DataRootForNode == nil {
		return nil, ErrNilDataRootFunc
	}

	candidates := s.admitted(req.ShardID, healthyDistinct(s.topo.Nodes()))
	if len(candidates) < req.RF {
		return nil, &InsufficientNodesError{Have: len(candidates), Need: req.RF}
	}

	selected := s.selectReplicas(candidates, req.RF)

	replicas := make([]Replica, 0, req.RF)
	for i, n := range selected {
		replicas = append(replicas, Replica{
			ReplicaID: uint64(i + 1),
			NodeID:    n.NodeID,
			RaftAddr:  n.RaftAddr,
			DataRoot:  req.DataRootForNode(n.NodeID),
		})
	}

	return &Placement{
		TenantID:        req.TenantID,
		Database:        req.Database,
		ShardID:         req.ShardID,
		RF:              req.RF,
		LeaderReplicaID: replicas[0].ReplicaID,
		Replicas:        replicas,
	}, nil
}

// PlaceAdditional extends an existing placement by addCount new replicas,
// selecting addCount distinct healthy nodes NOT already hosting a replica of
// this group (extending the anti-affinity set from existing.Replicas), using
// the same bounded-load selection as Place. Existing replicas, their
// ReplicaIDs, NodeIDs, DataRoots, and LeaderReplicaID are never modified —
// only new Replica entries are appended, each with State:
// ReplicaStateObserver and ReplicaID starting at MaxReplicaID(existing)+1.
// A ReplicaStateObserver replica is admitted into the live Raft group as a
// non-voting observer (see DatabaseService.scaleUp and RaftHost.AddObserver)
// so scale-up never transiently breaks the group's existing voting quorum;
// it is promoted to a full voting member only after it catches up (see
// TenantShardReconciler's Observer→ObserverReady→Active state machine).
//
// PlaceAdditional never recomputes the group from scratch (unlike Place): a
// dedicated Raft group's founding-member identity (ReplicaID, NodeID) must
// stay stable across a scale-up, so a fresh Place call is never safe once a
// group is running.
//
// Returns *InsufficientNodesError (matching ErrInsufficientNodes) when fewer
// than addCount eligible distinct healthy nodes remain after excluding nodes
// already hosting a replica of existing.
func (s *Scheduler) PlaceAdditional(ctx context.Context, existing *Placement, addCount int) (*Placement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrEmptyDatabase
	}
	if addCount < 1 {
		return nil, ErrInvalidRF
	}

	occupied := make(map[string]struct{}, len(existing.Replicas))
	for _, r := range existing.Replicas {
		occupied[r.NodeID] = struct{}{}
	}

	candidates := make([]NodeLoad, 0, len(s.topo.Nodes()))
	for _, n := range s.admitted(existing.ShardID, healthyDistinct(s.topo.Nodes())) {
		if _, dup := occupied[n.NodeID]; dup {
			continue
		}
		candidates = append(candidates, n)
	}
	if len(candidates) < addCount {
		return nil, &InsufficientNodesError{Have: len(candidates), Need: addCount}
	}

	selected := s.selectReplicas(candidates, addCount)

	nextID := MaxReplicaID(existing) + 1
	newReplicas := make([]Replica, 0, addCount)
	for _, n := range selected {
		newReplicas = append(newReplicas, Replica{
			ReplicaID: nextID,
			NodeID:    n.NodeID,
			RaftAddr:  n.RaftAddr,
			DataRoot:  "",
			State:     ReplicaStateObserver,
		})
		nextID++
	}

	replicas := make([]Replica, 0, len(existing.Replicas)+len(newReplicas))
	replicas = append(replicas, existing.Replicas...)
	replicas = append(replicas, newReplicas...)

	updated := *existing
	updated.Replicas = replicas
	updated.RF = len(replicas)
	return &updated, nil
}

// admitted returns the candidates the scheduler's replica filter accepts for
// shardID, in their original order; all of them when there is no filter.
func (s *Scheduler) admitted(shardID uint64, candidates []NodeLoad) []NodeLoad {
	if s.filter == nil {
		return candidates
	}
	out := candidates[:0:0]
	for _, n := range candidates {
		if s.filter(shardID, n.RaftAddr) == nil {
			out = append(out, n)
		}
	}
	return out
}

// healthyDistinct returns the healthy nodes, deduplicated by NodeID (first
// occurrence wins), sorted ascending by load so the least-loaded node is first.
func healthyDistinct(nodes []NodeLoad) []NodeLoad {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]NodeLoad, 0, len(nodes))
	for _, n := range nodes {
		if !n.Healthy || n.NodeID == "" {
			continue
		}
		if _, dup := seen[n.NodeID]; dup {
			continue
		}
		seen[n.NodeID] = struct{}{}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return lessLoaded(out[i], out[j]) })
	return out
}

// lessLoaded reports whether node a is less loaded than node b under the stable
// ordering (Bytes, then ShardCount, then NodeID).
func lessLoaded(a, b NodeLoad) bool {
	if a.Bytes != b.Bytes {
		return a.Bytes < b.Bytes
	}
	if a.ShardCount != b.ShardCount {
		return a.ShardCount < b.ShardCount
	}
	return a.NodeID < b.NodeID
}

// selectReplicas chooses rf nodes from the load-ascending candidates using the
// bounded-load objective: under-target nodes (load <= (1+epsilon)*avg) are
// drained least-loaded-first, then over-target nodes if more are still needed.
// Because candidates are already load-ascending, the result is the rf
// least-loaded distinct nodes; partitioning at the ceiling makes the
// bounded-load intent explicit and keeps a hot node out of the selection while
// a colder distinct node remains. The caller guarantees len(candidates) >= rf.
func (s *Scheduler) selectReplicas(candidates []NodeLoad, rf int) []NodeLoad {
	ceiling := s.loadCeiling(candidates)

	under := make([]NodeLoad, 0, len(candidates))
	over := make([]NodeLoad, 0, len(candidates))
	for _, n := range candidates {
		if float64(n.Bytes) <= ceiling {
			under = append(under, n)
		} else {
			over = append(over, n)
		}
	}

	selected := make([]NodeLoad, 0, rf)
	for _, n := range under {
		if len(selected) == rf {
			break
		}
		selected = append(selected, n)
	}
	for _, n := range over {
		if len(selected) == rf {
			break
		}
		selected = append(selected, n)
	}
	return selected
}

// loadCeiling returns (1+epsilon) times the mean Bytes load across candidates.
// With an empty input it returns 0.
func (s *Scheduler) loadCeiling(candidates []NodeLoad) float64 {
	if len(candidates) == 0 {
		return 0
	}
	var sum uint64
	for _, n := range candidates {
		sum += n.Bytes
	}
	avg := float64(sum) / float64(len(candidates))
	return avg * (1 + s.epsilon)
}
