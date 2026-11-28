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
	"io"
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

// materializedServiceCache caches the service bundle built over each
// materialized database's own host (a local binding, see
// lifecycle.LocalBindingFactory), keyed by owning tenant and name. The entry
// remembers the *database.Database instance it was built for: a database
// dropped and created again under the same name is a new instance over a new
// store, so its bundle is rebuilt rather than served over the closed one.
type materializedServiceCache struct {
	mu      sync.RWMutex
	entries map[string]*dedicatedStoreServiceEntry
}

// newMaterializedServiceCache creates an empty cache.
func newMaterializedServiceCache() *materializedServiceCache {
	return &materializedServiceCache{entries: make(map[string]*dedicatedStoreServiceEntry)}
}

// getOrBuild returns the cached bundle for db, building and caching one with
// build when absent or when db is a different instance than the cached one.
func (c *materializedServiceCache) getOrBuild(db *database.Database, build func() *DatabaseServices) *DatabaseServices {
	key := db.TenantID() + "\x00" + db.Name()

	c.mu.RLock()
	if e, ok := c.entries[key]; ok && e.db == db {
		c.mu.RUnlock()
		return e.svcs
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.db == db {
		return e.svcs
	}
	svcs := build()
	c.entries[key] = &dedicatedStoreServiceEntry{db: db, svcs: svcs}
	return svcs
}

// evict drops tenantID/name's cached bundle, closing what it holds, so a
// dropped database's closed store and vector indexes stay reachable through
// no cache. A missing entry is a no-op.
func (c *materializedServiceCache) evict(tenantID, name string) {
	key := tenantID + "\x00" + name
	c.mu.Lock()
	e, ok := c.entries[key]
	delete(c.entries, key)
	c.mu.Unlock()
	if !ok {
		return
	}
	// The binding's own vector service is closed by the binding when the
	// database closes, so a drop that fails after its fence leaves it open.
	if ms, isMS := e.db.Backend().(materializedStore); isMS && e.svcs != nil {
		if own := ms.Vector(); own != nil && e.svcs.Vector == own {
			return
		}
	}
	closeServiceBundle(e.svcs)
}

// evict drops name's cached bundle when it was built for tenantID's
// database, closing what it holds. The cache is keyed by name alone, so a
// drop of another tenant's same-named database leaves the entry in place.
func (c *dedicatedStoreServiceCache) evict(tenantID, name string) {
	c.mu.Lock()
	e, ok := c.entries[name]
	if ok && e.db.TenantID() != tenantID {
		ok = false
	}
	if ok {
		delete(c.entries, name)
	}
	c.mu.Unlock()
	if ok {
		closeServiceBundle(e.svcs)
	}
}

// closeServiceBundle closes the parts of an evicted bundle that hold
// resources of their own: the vector service and its in-memory HNSW
// indexes. Its store is the database's and closes with the drop.
func closeServiceBundle(svcs *DatabaseServices) {
	if svcs == nil {
		return
	}
	if c, ok := svcs.Vector.(io.Closer); ok {
		_ = c.Close()
	}
}

// bundleEvictors collects the evict of every bundle cache the server's
// transports build (each transport has its own registry and caches), so a
// drop's fence evicts the database from all of them. The zero value is
// ready to use.
type bundleEvictors struct {
	mu    sync.Mutex
	evict []func(tenantID, name string)
}

// add registers the caches of one registry.
func (b *bundleEvictors) add(dedicated *dedicatedStoreServiceCache, materialized *materializedServiceCache) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evict = append(b.evict, dedicated.evict, materialized.evict)
}

// evictAll evicts tenantID/name from every registered cache.
func (b *bundleEvictors) evictAll(tenantID, name string) {
	b.mu.Lock()
	fns := append([]func(string, string){}, b.evict...)
	b.mu.Unlock()
	for _, fn := range fns {
		fn(tenantID, name)
	}
}
