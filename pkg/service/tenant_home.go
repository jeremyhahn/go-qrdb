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
		// The group was placed on the cluster as it stood then: nodes that
		// joined since gain their replica now. A failed growth leaves the
		// group serving where it is, so it does not fail the provision.
		grown, gerr := s.growHome(ctx, existing)
		if gerr != nil {
			s.logger.Warn("tenant home group growth failed; it keeps its current nodes",
				slog.String("tenant", tenantID), slog.Any("error", gerr))
		}
		return grown, nil
	}
	if !errors.Is(err, placement.ErrPlacementNotFound) {
		return nil, err
	}
	if s.tenantInfo != nil {
		if _, err := s.tenantInfo.GetTenant(ctx, tenantID); err != nil {
			return nil, err
		}
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
	s.inviteReplicas(ctx, tenantID, database.TenantHomeName, plan)
	return plan, nil
}

// GrowTenantHome reconciles tenantID's home group membership with the
// cluster: every healthy node the scheduler admits that holds no home
// replica -- a node that joined after the tenant was created, or one that
// replaced an original node -- gains one, observer-first (the reconciler
// promotes it once it has caught up), so the group's voting quorum never
// breaks while it grows. An isolated tenant's databases are confined to its
// home group's nodes (PlacementServingSet), so this is what lets them scale
// onto, or move to, such a node (docs/multi-tenancy/engineering.md,
// late-joining nodes). It returns the placement as it stands after the
// replicas it added; a tenant with no home group returns
// placement.ErrPlacementNotFound.
func (s *DatabaseService) GrowTenantHome(ctx context.Context, tenantID string) (*placement.Placement, error) {
	if s.placementStore == nil || s.placementScheduler == nil {
		return nil, &TenantHomeUnplaceableError{TenantID: tenantID}
	}
	home, err := s.placementStore.Get(ctx, tenantID, database.TenantHomeID)
	if err != nil {
		return nil, err
	}
	return s.growHome(ctx, home)
}

// growHome adds an observer replica of home on every eligible node holding
// none. The scheduler sizes the additions: it is asked for one per cluster
// node, and an *InsufficientNodesError reports how many it could place.
func (s *DatabaseService) growHome(ctx context.Context, home *placement.Placement) (*placement.Placement, error) {
	plan, err := s.placementScheduler.PlaceAdditional(ctx, home, max(s.placementScheduler.ClusterSize(), 1))
	var short *placement.InsufficientNodesError
	if errors.As(err, &short) {
		if short.Have < 1 {
			return home, nil
		}
		plan, err = s.placementScheduler.PlaceAdditional(ctx, home, short.Have)
	}
	if err != nil {
		return home, err
	}
	if s.raftHost != nil {
		if err := waitForShardLeaderKnown(ctx, s.raftHost, home.ShardID, s.scaleLeaderWaitTimeout, s.scaleLeaderWaitInterval); err != nil {
			return home, err
		}
	}
	base := placement.MaxReplicaID(home)
	current := home
	for _, r := range plan.Replicas {
		if r.ReplicaID <= base {
			continue
		}
		updated, _, aerr := addObserverReplica(ctx, s.raftHost, s.placementStore, current, r)
		current = updated
		if aerr != nil {
			return current, aerr
		}
		s.logger.Info("grew tenant home group onto a node",
			slog.String("tenant", home.TenantID),
			slog.Uint64("shard_id", home.ShardID),
			slog.Uint64("replica_id", r.ReplicaID),
			slog.String("node", r.NodeID))
	}
	return current, nil
}

// ReconcileTenantHomes grows every tenant's home group onto the healthy
// nodes it does not yet span (GrowTenantHome). It is what a node join or
// replacement runs; one tenant's failure does not stop the others, and the
// errors are returned joined.
func (s *DatabaseService) ReconcileTenantHomes(ctx context.Context) error {
	if s.placementStore == nil || s.placementScheduler == nil {
		return nil
	}
	all, err := s.placementStore.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range all {
		if p == nil || p.DatabaseID != database.TenantHomeID {
			continue
		}
		if _, gerr := s.growHome(ctx, p); gerr != nil {
			errs = append(errs, gerr)
		}
	}
	return errors.Join(errs...)
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
