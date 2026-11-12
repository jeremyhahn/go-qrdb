// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// ErrEmptyOwnerSet is returned when a migration scope resolves to a placement
// entry that lists no valid replica node IDs. It is deliberately NOT a
// fall-back-to-local case: silently narrowing a placed multi-replica scope to the
// local node would migrate only this node and let the coordinator declare the
// cluster migration complete while peer replicas remain unencrypted at rest.
var ErrEmptyOwnerSet = errors.New("server: migration scope has a placement entry but no valid owner node IDs")

// OwnerResolveError wraps an owner-resolution failure with its scope.
type OwnerResolveError struct {
	TenantID string
	Database string
	Err      error
}

func (e *OwnerResolveError) Error() string {
	return "server: resolve migration owners for " + e.TenantID + "/" + e.Database + ": " + e.Err.Error()
}
func (e *OwnerResolveError) Unwrap() error { return e.Err }

// Compile-time check: the resolver satisfies the coordinator's owner seam.
var _ clustermig.OwnerResolver = (*clusterOwnerResolver)(nil)

// NewClusterOwnerResolver builds the cluster-migration owner resolver for the
// daemon: system scope → live roster (topology) + system shard; tenant scope →
// placement replicas; single-node / no-placement → the local node. placements
// and topology may be nil (fall back to the local node). localNodeID is this
// node's placement identity (its Raft address).
func NewClusterOwnerResolver(placements *placement.Store, topology placement.TopologyProvider, systemShardID uint64, localNodeID string) clustermig.OwnerResolver {
	return newClusterOwnerResolver(placements, topology, systemShardID, localNodeID)
}

// clusterOwnerResolver resolves the node owners of a migration scope for the
// cluster enable-encryption coordinator (Phase D). It implements
// clustermig.OwnerResolver without importing clustermig (structural typing): the
// daemon passes it to clustermig.NewCoordinator.
//
//   - System scope (empty tenant + empty database): every node in the live
//     cluster roster owns the system barrier; the shard is the system KV shard.
//   - Tenant scope: the placed replicas of the tenant's dedicated-raft database
//     own it; the shard is the placement-allocated shard. A database with no
//     placement entry (a meta-managed single-node database) resolves to the local
//     node alone, so single-node tenant migrations flow through the same path.
type clusterOwnerResolver struct {
	placements    *placement.Store
	topology      placement.TopologyProvider
	systemShardID uint64
	localNodeID   string
}

// newClusterOwnerResolver builds the resolver. placements and topology may be
// nil on deployments without dedicated-raft placement or a live roster; the
// resolver then falls back to the local node for tenant scope and to the local
// node alone for system scope.
func newClusterOwnerResolver(placements *placement.Store, topology placement.TopologyProvider, systemShardID uint64, localNodeID string) *clusterOwnerResolver {
	return &clusterOwnerResolver{
		placements:    placements,
		topology:      topology,
		systemShardID: systemShardID,
		localNodeID:   localNodeID,
	}
}

// Owners implements clustermig.OwnerResolver.
func (r *clusterOwnerResolver) Owners(ctx context.Context, tenantID, database string) (uint64, []string, error) {
	if tenantID == "" && database == "" {
		return r.systemShardID, r.systemOwners(), nil
	}
	if r.placements == nil {
		return 0, r.localOnly(), nil
	}
	// Whole-tenant scope (empty database): a tenant-barrier migration covers ALL
	// the tenant's dedicated-raft databases, so the owner set is the union of
	// every placement's replicas. ShardID is 0 — there is no single shard.
	if database == "" {
		owners, oerr := r.tenantOwners(ctx, tenantID)
		return 0, owners, oerr
	}
	p, err := r.placements.GetByName(ctx, tenantID, database)
	if err != nil {
		// No placement entry → meta-managed single-node database. Migrate locally.
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return 0, r.localOnly(), nil
		}
		return 0, nil, err
	}
	owners := dedupeNonEmpty(replicaNodeIDs(p.Replicas))
	if len(owners) == 0 {
		// A placement entry EXISTS but lists no valid replica node IDs — do NOT
		// silently fall back to local-only, which would migrate only this node and
		// let the coordinator declare the cluster "done" while peer replicas stay
		// plaintext at rest. Surface it instead.
		return 0, nil, &OwnerResolveError{TenantID: tenantID, Database: database, Err: ErrEmptyOwnerSet}
	}
	return p.ShardID, owners, nil
}

// tenantOwners unions the replica node IDs across all of a tenant's placements.
// A tenant with NO placement entries is a meta-managed single-node tenant →
// local-only. But entries that exist yet yield no valid node IDs are a corrupt
// owner set, returned as an error rather than a silent narrow-to-local (which
// would leave peer replicas unencrypted while the migration reports complete).
func (r *clusterOwnerResolver) tenantOwners(ctx context.Context, tenantID string) ([]string, error) {
	placements, err := r.placements.ListForTenant(ctx, tenantID)
	if err != nil {
		// A real lookup failure (transient KV error, corrupt/undecodable entry)
		// must NOT be conflated with "no placements" — silently narrowing to
		// local-only here would let the coordinator declare a whole-tenant
		// migration complete while peer replicas stay plaintext at rest.
		return nil, &OwnerResolveError{TenantID: tenantID, Err: err}
	}
	if len(placements) == 0 {
		// Genuinely no placement entries → meta-managed single-node tenant.
		return r.localOnly(), nil
	}
	var all []string
	for _, p := range placements {
		all = append(all, replicaNodeIDs(p.Replicas)...)
	}
	owners := dedupeNonEmpty(all)
	if len(owners) == 0 {
		return nil, &OwnerResolveError{TenantID: tenantID, Err: ErrEmptyOwnerSet}
	}
	return owners, nil
}

// replicaNodeIDs extracts the NodeIDs from a replica slice.
func replicaNodeIDs(reps []placement.Replica) []string {
	out := make([]string, 0, len(reps))
	for _, rep := range reps {
		out = append(out, rep.NodeID)
	}
	return out
}

// dedupeNonEmpty returns the input with empty strings dropped and duplicates
// removed, preserving first-seen order.
func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// systemOwners returns the live cluster roster as system-barrier owners, falling
// back to the local node when no topology is wired or the roster is momentarily
// empty (so a system migration is never lost to an empty owner set).
func (r *clusterOwnerResolver) systemOwners() []string {
	if r.topology == nil {
		return r.localOnly()
	}
	nodes := r.topology.Nodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	owners := dedupeNonEmpty(ids)
	if len(owners) == 0 {
		return r.localOnly()
	}
	return owners
}

// localOnly returns the local node as the sole owner, or an empty set when the
// local node id is unknown (the coordinator rejects an empty owner set).
func (r *clusterOwnerResolver) localOnly() []string {
	if r.localNodeID == "" {
		return nil
	}
	return []string{r.localNodeID}
}
