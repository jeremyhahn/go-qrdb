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

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// RebalanceKeyDeliverer ships the tenant barrier key to a destination replica
// during a dedicated-tenant Raft group MOVE. It is satisfied by
// *clusterkeys.Sender.
//
// The move algorithm constructs a single-replica Placement containing only the
// destination replica and delegates to DistributeTenantKey so that HPKE-seal,
// sealed-blob export, and recipient-authentication logic remain inside
// clusterkeys without duplication in this package.
type RebalanceKeyDeliverer interface {
	// DistributeTenantKey ships the barrier key to each replica in p, skipping
	// the replica whose NodeID equals selfNodeID. For a MOVE the caller passes a
	// single-replica Placement containing only the destination replica so exactly
	// one delivery occurs.
	DistributeTenantKey(ctx context.Context, p *placement.Placement, leaderBarrier *seal.StorageBarrier, selfNodeID string) error
}

// RebalanceBarrierLookup resolves the dedicated storage barrier for a tenant.
// It is satisfied by *seal.BarrierRegistry.
type RebalanceBarrierLookup interface {
	// GetTenant returns the TenantBarrier registered for tenantID, or an error
	// when no barrier exists for the tenant.
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
}

// RebalanceServiceOption configures a RebalanceService at construction time.
type RebalanceServiceOption func(*RebalanceService)

// WithRebalancePlacementStore injects the replicated placement table. When set,
// ExecuteRebalance consults the table to identify dedicated-tenant Raft groups
// and applies the full tenant-aware MOVE sequence (key delivery, placement
// update, Raft membership change) for those shards. Generic (system-shard)
// moves are unchanged when the store is absent.
func WithRebalancePlacementStore(s *placement.Store) RebalanceServiceOption {
	return func(r *RebalanceService) { r.placementStore = s }
}

// WithRebalanceKeyDeliverer injects the key deliverer used to ship the tenant
// barrier key to the destination replica during a dedicated-tenant shard MOVE.
// When nil, key delivery is skipped (the destination reconciler may pull the
// key on its next pass via Phase 4c-1d).
func WithRebalanceKeyDeliverer(d RebalanceKeyDeliverer) RebalanceServiceOption {
	return func(r *RebalanceService) { r.keyDeliverer = d }
}

// WithRebalanceBarrierLookup injects the barrier registry used to resolve a
// tenant's dedicated storage barrier for key export during a tenant shard MOVE.
func WithRebalanceBarrierLookup(lookup RebalanceBarrierLookup) RebalanceServiceOption {
	return func(r *RebalanceService) { r.barrierLookup = lookup }
}

// WithRebalanceSelfNodeID sets the local Raft node ID. DistributeTenantKey
// skips any replica whose NodeID matches selfNodeID so the leader never ships
// the key to itself.
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
