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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
	// dropCheck is run by every mutating BackendKVService method before it
	// delegates to db.Backend(), so a handle resolved before a
	// Manager.Drop began answers database.DroppingError instead of reaching
	// a store the drop is about to close and remove (see the dropCheck field
	// of service.BackendKVService for why a check suffices for a dedicated
	// local store).
	dropCheck := func() error {
		if db.Dropping() {
			return &database.DroppingError{Name: db.Name()}
		}
		return nil
	}
	kv := service.NewNamedBackendKVService(db.Name(), db.Backend(), dropCheck)
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
	entries map[dedicatedStoreKey]*dedicatedStoreServiceEntry

	// onEvict, when set, runs under mu once watchDrop has evicted a dropped
	// database's entry. Tests use it to wait for the eviction; it is nil in
	// production.
	onEvict func(dedicatedStoreKey)
}

// dedicatedStoreKey names one database: a name is unique only within its
// tenant, so two tenants' same-named databases are separate entries.
type dedicatedStoreKey struct {
	tenant string
	name   string
}

// dedicatedStoreServiceEntry pairs a cached bundle with the *database.Database
// instance it was built from, so a subsequent database.Manager.Get returning a
// DIFFERENT instance for the same name (e.g. a drop followed by a recreate)
// invalidates the cache instead of serving a stale, closed backend.
type dedicatedStoreServiceEntry struct {
	db   *database.Database
	svcs *DatabaseServices

	// watched is set while a goroutine waits for db to stop serving, to
	// evict the entry if a drop stopped it (watchDrop).
	watched atomic.Bool
}

// newDedicatedStoreServiceCache creates an empty cache.
func newDedicatedStoreServiceCache() *dedicatedStoreServiceCache {
	return &dedicatedStoreServiceCache{entries: make(map[dedicatedStoreKey]*dedicatedStoreServiceEntry)}
}

// getOrBuild returns the cached bundle for db, building and caching one with
// buildDedicatedStoreServices when absent or when db is a different
// *database.Database instance than the one currently cached under its tenant
// and name. A cached entry is evicted when a drop stops its database
// (watchDrop), so the cache does not keep a dropped database, and the
// backend and object store it carries, for the life of the process.
func (c *dedicatedStoreServiceCache) getOrBuild(db *database.Database) *DatabaseServices {
	key := dedicatedStoreKey{tenant: tenantid.Normalize(db.TenantID()), name: db.Name()}

	c.mu.RLock()
	if e, ok := c.entries[key]; ok && e.db == db {
		c.mu.RUnlock()
		c.watchDrop(key, e)
		return e.svcs
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.db == db {
		c.watchDrop(key, e)
		return e.svcs
	}
	svcs := buildDedicatedStoreServices(db)
	if db.Dropping() {
		// Manager.Drop has fenced it: it is never served again.
		delete(c.entries, key)
		return svcs
	}
	e := &dedicatedStoreServiceEntry{db: db, svcs: svcs}
	c.entries[key] = e
	c.watchDrop(key, e)
	return svcs
}

// watchDrop starts, unless one is already waiting, a goroutine that waits
// for e's database to stop serving and evicts e if a drop stopped it.
// Manager.Drop fences the database (Dropping) before it closes it, so the
// fence is visible once Unserved is closed. A database that stopped for
// another reason (a seal) is left cached: the goroutine ends, and the next
// request that resolves the database, once it serves again, waits anew.
func (c *dedicatedStoreServiceCache) watchDrop(key dedicatedStoreKey, e *dedicatedStoreServiceEntry) {
	if !e.watched.CompareAndSwap(false, true) {
		return
	}
	go func() {
		<-e.db.Unserved()
		if !e.db.Dropping() {
			e.watched.Store(false)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		if c.onEvict != nil {
			c.onEvict(key)
		}
	}()
}
