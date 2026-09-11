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

package server

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// buildDedicatedStoreServices builds a *DatabaseServices bundle directly over
// a barrier-enabled, non-raft dedicated-store database's own encrypting
// Backend() -- database.BarrierBackend, wrapping the database's own storage
// with AES-256-GCM keyed by its own barrier (see database.DatabaseBarrierManager
// and database.Manager.Create/Open). No Raft group is involved: this is the
// "lighter, single-node store" path for a database whose dedicated store is a
// plain --data-dir directory rather than Backend == database.BackendRaft.
//
// Raft-backed isolation is deliberately NOT served here: a database with
// Backend == database.BackendRaft never gets a per-database barrier bolted
// onto a shared/prefix tenant. Raft-backed encryption at rest comes ONLY from
// an IsolationRaft tenant's dedicated shard (see RaftBackendFactory.Services,
// keyed by the OWNING TENANT's barrier), never from a database-level barrier
// independent of tenant isolation.
//
// The Collection service built here is the one the registry accessor returns
// for this path: it is NOT rebuilt over a namespace wrapper, because this
// database's own encrypting store belongs to exactly one (tenant, database)
// pair already -- there is no shared keyspace to separate from. Contrast
// applyComposite, which must rebuild for exactly the opposite reason.
//
// It provides KV and a KV-backed Collection service (no secondary indexes,
// since IndexServicer has no direct-storage implementation over a plain
// database.Backend today). Every other engine -- MVCC, Batch, Transaction,
// Lease, TimeSeries, Index, Watch, Vector -- has no direct-storage
// implementation over a plain database.Backend and is left nil. The REST/
// gRPC/QUIC/MCP service accessors already treat a nil engine as
// "not implemented for this database" (service.ServiceNotImplementedError --
// see restServiceRegistry.MVCCService et al.) rather than panicking or
// silently falling back to the shared, unencrypted store, so this is a safe,
// fail-closed partial bundle rather than a silent capability downgrade.
//
// The backend already fails closed while its barrier is sealed
// (database.BarrierBackend returns database.SealedError from every
// operation); NewNamedBackendKVService translates that into a
// service.BarrierSealedError so transports map it to their standard
// sealed-barrier status instead of a generic internal error.
// Collection here is NOT wired with a SequenceServicer
// (service.CollectionService.WithSequenceService): SequenceService requires
// an MVCC read (for a key's ModRevision) and a TransactionServicer (for the
// compare-and-swap that advances it), and this path's plain
// database.Backend has neither -- it is not a Raft state machine. A
// collection on this path using IDStrategyIncrement therefore still uses
// the legacy process-local generator (see
// CollectionService.getOrCreateIDGen) and inherits its known duplicate-ID
// risk across a process restart. Closing that gap requires either an MVCC
// layer over database.Backend or a different sequence-allocation primitive
// for non-Raft backends; that is an architecture decision outside the scope
// of this change.
func buildDedicatedStoreServices(db *database.Database) *DatabaseServices {
	kv := service.NewNamedBackendKVService(db.Name(), db.Backend())
	return &DatabaseServices{
		Name:       db.Name(),
		KV:         kv,
		Collection: service.NewCollectionService(kv),
	}
}

// dedicatedStoreServiceCache caches per-database dedicated-store service
// bundles (buildDedicatedStoreServices) so restServiceRegistry.resolve does
// not rebuild one on every request. It is safe for concurrent use.
type dedicatedStoreServiceCache struct {
	mu      sync.RWMutex
	entries map[string]*dedicatedStoreServiceEntry
}

// dedicatedStoreServiceEntry pairs a cached bundle with the *database.Database
// instance it was built from, so a subsequent database.Manager.Get returning a
// DIFFERENT instance for the same name (e.g. a drop followed by a recreate)
// invalidates the cache instead of serving a stale, closed backend.
type dedicatedStoreServiceEntry struct {
	db   *database.Database
	svcs *DatabaseServices
}

// newDedicatedStoreServiceCache creates an empty cache.
func newDedicatedStoreServiceCache() *dedicatedStoreServiceCache {
	return &dedicatedStoreServiceCache{entries: make(map[string]*dedicatedStoreServiceEntry)}
}

// getOrBuild returns the cached bundle for db, building and caching one with
// buildDedicatedStoreServices when absent or when db is a different
// *database.Database instance than the one currently cached under its name.
func (c *dedicatedStoreServiceCache) getOrBuild(db *database.Database) *DatabaseServices {
	name := db.Name()

	c.mu.RLock()
	if e, ok := c.entries[name]; ok && e.db == db {
		c.mu.RUnlock()
		return e.svcs
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[name]; ok && e.db == db {
		return e.svcs
	}
	svcs := buildDedicatedStoreServices(db)
	c.entries[name] = &dedicatedStoreServiceEntry{db: db, svcs: svcs}
	return svcs
}
