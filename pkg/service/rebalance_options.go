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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// RebalanceServiceOption configures a RebalanceService at construction time.
type RebalanceServiceOption func(*RebalanceService)

// WithRebalancePlacementStore injects the replicated placement table. When set,
// ExecuteRebalance consults the table to identify dedicated-tenant Raft groups
// and applies the full tenant-aware MOVE sequence (placement update, Raft
// membership change) for those shards. Generic (system-shard)
// moves are unchanged when the store is absent.
func WithRebalancePlacementStore(s *placement.Store) RebalanceServiceOption {
	return func(r *RebalanceService) { r.placementStore = s }
}

// WithRebalanceSelfNodeID sets the local Raft node ID, the identity this node
// has in placements: a MOVE runs only on the group's leader and source.
func WithRebalanceSelfNodeID(nodeID string) RebalanceServiceOption {
	return func(r *RebalanceService) { r.selfNodeID = nodeID }
}

// WithRebalanceRaftHost injects the Raft membership-change handle
// moveTenantGroupShard uses to admit a MOVE's destination replica
// observer-first and evict its source replica, and resolveMoveLeader uses to
// route a MOVE to the dedicated group's current Raft leader. See
// RebalanceService.SetRaftHost for the post-construction equivalent (used in
// production, since the daemon builds the RaftHost adapter after
// RebalanceService is constructed).
func WithRebalanceRaftHost(h RaftHost) RebalanceServiceOption {
	return func(r *RebalanceService) { r.raftHost = h }
}
