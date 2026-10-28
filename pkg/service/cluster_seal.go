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

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// This file holds the cluster sealing half of the barrier and database
// services: how a node or database seal reaches every node once the cluster
// has activated cluster-seal (docs/barrier-seal/architecture.md, "Sealing in
// a cluster").

// BarrierServiceOption configures a BarrierService.
type BarrierServiceOption func(*BarrierService)

// WithClusterSealer makes the node barrier's seal, seal vote and cluster
// unseal cluster-wide once the cluster has activated cluster-seal. Without
// it every seal is node-local.
func WithClusterSealer(s clusterseal.Sealer) BarrierServiceOption {
	return func(b *BarrierService) { b.sealer = s }
}

// WithBarrierRegistry makes a seal of the node barrier seal the whole node:
// every registered tenant barrier, then the node barrier
// (BarrierRegistry.SealAll, the shutdown seal), wiping every tenant's keys.
// Without it only the node barrier is sealed.
func WithBarrierRegistry(reg *seal.BarrierRegistry) BarrierServiceOption {
	return func(b *BarrierService) { b.registry = reg }
}

// BarrierDatabases is the database half of a whole-node seal: it seals every
// database barrier on the node -- wiping each key -- and marks the databases
// sealed. *database.Manager satisfies it.
type BarrierDatabases interface {
	SealBarrierDatabases() error
}

// ShardPauser is the Raft half of a whole-node seal: PauseShards stops every
// Raft replica the node hosts, the system shard included, without unloading
// it, and returns once none of them has log, apply or snapshot work in flight
// -- so no write reaches a log after the keys protecting it are wiped. A
// paused replica refuses requests with quicraft.ErrShardPaused. The pauser
// resumes the replicas itself once the node barrier is unsealed again, after
// the keys are back, so a seal is undone by an unseal alone. If ctx ends
// first PauseShards returns ctx's error and the replicas stay paused.
// *server.ShardPauser satisfies it.
type ShardPauser interface {
	PauseShards(ctx context.Context) error
}

// SealWholeNode is the one whole-node seal. Every path that seals a node --
// the single-admin seal, a share-holder seal quorum (BarrierService.seal),
// and a node seal record's enforcement (server.SealEnforcer.SealNode) --
// calls it, so they cannot diverge. It first pauses every Raft shard the node
// hosts (shards; see ShardPauser), so no write reaches a log the keys are
// about to stop protecting, then seals every database barrier
// (databases, through the database barrier manager's own registry), then
// every tenant barrier and the system barrier (registry.SealAll), or the
// system barrier alone when there is no registry. Each registry records the
// system-managed barriers it moved from unsealed to sealed, the only ones the
// node unseal brings back. Every part runs even when another fails -- a
// pause that fails still wipes the keys, since a seal must always remove
// them -- and the errors are joined. Ending the node's streams is the
// caller's.
func SealWholeNode(ctx context.Context, shards ShardPauser, registry *seal.BarrierRegistry, system *seal.StorageBarrier, databases BarrierDatabases) error {
	var errs []error
	if shards != nil {
		errs = append(errs, shards.PauseShards(ctx))
	}
	if databases != nil {
		errs = append(errs, databases.SealBarrierDatabases())
	}
	switch {
	case registry != nil:
		errs = append(errs, registry.SealAll())
	case system != nil:
		errs = append(errs, system.Seal())
	}
	return errors.Join(errs...)
}

// WithBarrierDatabases makes a node seal seal every database barrier and mark
// every barrier-enabled database sealed (SealWholeNode): the mark is what
// refuses a database whose store does not refuse by itself.
func WithBarrierDatabases(dbs BarrierDatabases) BarrierServiceOption {
	return func(b *BarrierService) { b.databases = dbs }
}

// WithShardPauser makes a node seal pause every Raft shard the node hosts
// before it wipes a key (SealWholeNode). Without it -- a standalone node has
// no Raft shards -- nothing is paused.
func WithShardPauser(p ShardPauser) BarrierServiceOption {
	return func(b *BarrierService) { b.shards = p }
}

// ClusterMigrations reads a barrier's cluster-wide encryption migration from
// the replicated record: nil when none is in progress. *clustermig.Store
// implements it.
type ClusterMigrations interface {
	Report(ctx context.Context, tenantID, database string) (*clustermig.Report, error)
}

var _ ClusterMigrations = (*clustermig.Store)(nil)

// WithClusterMigrations has the barrier status report the node barrier's
// cluster-wide encryption migration, read through m. Without it the status
// carries none.
func WithClusterMigrations(m ClusterMigrations) BarrierServiceOption {
	return func(b *BarrierService) { b.migrations = m }
}

// seal seals the node barrier: cluster-wide through the sealer when active,
// this node only otherwise. Once active, the sealer records the seal before
// it wipes this node's key, because the key protects this node's Raft log;
// every node's monitor then seals the whole node (SealEnforcer.SealNode).
// Before activation this node is sealed here the same way: every Raft shard
// paused, every tenant barrier and the node barrier, then every open stream ends -- after the
// barriers, so the sealed node barrier is the gate a stream opening between
// the two finds closed.
func (s *BarrierService) seal(ctx context.Context) error {
	if clusterseal.ActiveIn(s.sealer) {
		_, err := s.sealer.Seal(ctx, clusterseal.NodeBarrier(), clusterseal.PrincipalFrom(ctx))
		return err
	}
	err := SealWholeNode(ctx, s.shards, s.registry, s.barrier, s.databases)
	if streams := clusterseal.StreamsOf(s.sealer); streams != nil {
		streams.EndAll(&clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()})
	}
	return err
}

// BarrierClusterUnseal clears a cluster-wide seal of the node barrier. See
// BarrierServicer.
func (s *BarrierService) BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error) {
	if !clusterseal.ActiveIn(s.sealer) {
		return nil, clusterseal.ErrInactive
	}
	if _, err := s.sealer.Clear(ctx, clusterseal.NodeBarrier(), clusterseal.PrincipalFrom(ctx)); err != nil {
		return nil, err
	}
	return clusterseal.ReportOf(s.sealer, clusterseal.NodeBarrier()), nil
}

// DatabaseBarrierStatusInfo is a database barrier's status with its cluster
// seal state.
type DatabaseBarrierStatusInfo struct {
	*seal.BarrierStatus
	// ClusterSeal is the database barrier's cluster seal state. Never nil.
	ClusterSeal *clusterseal.Report `json:"cluster_seal"`
}

// WithDatabaseClusterSealer makes a database seal reach every node once the
// cluster has activated cluster-seal, and makes an unseal clear the
// database's cluster seal record. Without it every seal is node-local.
func WithDatabaseClusterSealer(s clusterseal.Sealer) DatabaseServiceOption {
	return func(d *DatabaseService) { d.sealer = s }
}

// databaseBarrier names the cluster seal barrier of name, owned by the
// caller's tenant (authorizeDatabaseTenant has checked that).
func (s *DatabaseService) databaseBarrier(ctx context.Context, name string) clusterseal.Barrier {
	owner := tenant.FromContext(ctx)
	owner = tenantid.Normalize(owner)
	return clusterseal.DatabaseBarrier(owner, name)
}

// clearPredecessorSeal clears the record a dropped database of the same name
// and owner left sealed, so the database just created starts from a clear
// record. Records are never deleted -- a barrier's epoch keeps increasing
// across re-creation -- and without this the new database would be refused
// from birth and could never be unsealed, its barrier being unsealed already.
// The create itself proved the name was free; another tenant's record is a
// different barrier and is never touched.
func (s *DatabaseService) clearPredecessorSeal(ctx context.Context, db *database.Database) error {
	owner := db.TenantID()
	owner = tenantid.Normalize(owner)
	b := clusterseal.DatabaseBarrier(owner, db.Name())
	if !clusterseal.ActiveIn(s.sealer) {
		return nil
	}
	_, err := s.sealer.Clear(ctx, b, clusterseal.PrincipalFrom(ctx))
	return err
}

// rollBackCreate seals and drops a database whose create cannot complete, so
// it is never served and a failed create leaves nothing behind.
func (s *DatabaseService) rollBackCreate(ctx context.Context, db *database.Database, cause error) {
	name := db.Name()
	if s.barrierMgr != nil {
		if _, err := s.barrierMgr.BarrierStatus(name, db.TenantID()); err == nil {
			if serr := s.sealDatabaseHere(db); serr != nil {
				s.logger.Error("failed to seal database while rolling back its create",
					"database", name, "cause", cause, "error", serr)
			}
		}
	}
	// Dropped by its own tenant and name: another tenant may hold a
	// database of the same name, which a name-only drop refuses.
	if err := s.manager.DropForTenant(tenant.WithTenantID(ctx, db.TenantID()), name); err != nil {
		s.logger.Error("failed to roll back database create",
			"database", name, "cause", cause, "error", err)
	}
}

// clearDatabaseSeal ends a cluster seal of db after this node has unsealed
// it. If the clear record cannot be written the database is sealed again
// here, so this node never serves a database whose record still says sealed.
func (s *DatabaseService) clearDatabaseSeal(ctx context.Context, db *database.Database) error {
	b := s.databaseBarrier(ctx, db.Name())
	if !clusterseal.ActiveIn(s.sealer) {
		return nil
	}
	if _, err := s.sealer.Clear(ctx, b, clusterseal.PrincipalFrom(ctx)); err != nil {
		if serr := s.sealDatabaseHere(db); serr != nil {
			s.logger.Error("database barrier could not be sealed again after a failed cluster unseal",
				"database", db.Name(), "error", serr)
		}
		return err
	}
	return nil
}
