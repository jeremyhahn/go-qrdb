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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// TenantHomeUnplaceableError is returned by ProvisionTenantHome on a node
// with no replicated placement table or scheduler wired: there is nowhere to
// record the home group, so an isolated tenant cannot have one.
type TenantHomeUnplaceableError struct {
	TenantID string
}

// Error implements error.
func (e *TenantHomeUnplaceableError) Error() string {
	return "service: tenant " + e.TenantID + " home group cannot be placed: placement is not configured on this node"
}

// ProvisionTenantHome places tenantID's home group -- the Raft group of the
// tenant's own that holds an isolated tenant's identity records and DEK
// keyring, whatever its policy (docs/multi-tenancy/architecture.md "Strict
// isolation") -- and returns its
// placement. It is idempotent: an existing placement is returned unchanged,
// and concurrent calls for one tenant all return the one placement that
// committed first (placement.Store.ClaimPlacement), whose caller alone invites
// its replicas.
//
// The home group is a placement and nothing else. It is not a catalog
// database, so it counts against no database or raft-group quota and no
// caller can list, drop or rename it; database.TenantHomeName is reserved so
// no database can collide with it. It is replicated on every node (RF is the
// cluster size), which is what lets every node authenticate the tenant's
// callers from a local replica. Deleting the tenant retires it through
// RetireTenantHome.
//
// Starting the group is not done here: the replicas start where the tenant's
// barrier is unsealed, through the raft backend factory and the shard
// reconciler, exactly as a dedicated-raft database's do.
func (s *DatabaseService) ProvisionTenantHome(ctx context.Context, tenantID string) (*placement.Placement, error) {
	if s.placementStore == nil || s.placementScheduler == nil {
		return nil, &TenantHomeUnplaceableError{TenantID: tenantID}
	}
	existing, err := s.placementStore.Get(ctx, tenantID, database.TenantHomeID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, placement.ErrPlacementNotFound) {
		return nil, err
	}
	var autoUnseal bool
	if s.tenantInfo != nil {
		info, err := s.tenantInfo.GetTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		autoUnseal = info.BarrierAutoUnseal
	}
	shardID, err := s.manager.ClaimShardID(ctx)
	if err != nil {
		return nil, err
	}
	rf := max(s.placementScheduler.ClusterSize(), 1)
	plan, err := s.placementScheduler.Place(ctx, placement.PlaceRequest{
		TenantID:   tenantID,
		DatabaseID: database.TenantHomeID,
		Database:   database.TenantHomeName,
		ShardID:    shardID,
		RF:         rf,
	})
	if err != nil {
		return nil, err
	}
	// The home group's Raft log is written under the tenant's own key, never
	// the node's: an isolated tenant has no record in a log the platform can
	// read.
	plan.DedicatedWAL = true
	// Provisioning is not exclusive (bootstrap.ProvisionTenant), so another
	// call may have placed the group since the read above: the first
	// placement to commit is the home group, and a later one returns it.
	claimed, winner, err := s.placementStore.ClaimPlacement(ctx, plan)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return winner, nil
	}
	s.logger.Info("placed tenant home group",
		slog.String("tenant", tenantID),
		slog.Uint64("shard_id", plan.ShardID),
		slog.Int("rf", plan.RF))
	s.inviteReplicas(ctx, tenantID, database.TenantHomeName, plan, autoUnseal)
	return plan, nil
}

// RetireTenantHome implements tenant.HomeRetirer: it deletes tenantID's
// home-group placement, so no node starts a replica of it again and every
// node's shard reconciler closes and removes the replica it holds. A tenant
// with no home group, or a node with no placement table, has nothing to
// retire.
func (s *DatabaseService) RetireTenantHome(ctx context.Context, tenantID string) error {
	if s.placementStore == nil {
		return nil
	}
	return s.placementStore.Delete(ctx, tenantID, database.TenantHomeID)
}
