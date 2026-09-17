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
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// errNotDedicatedDatabaseBarrier is a sentinel returned by
// resolveDedicatedDatabaseBarrier when the (tenant, db) pair is not a
// database with its own dedicated-store encryption barrier, so resolve falls
// through to the shared-store resolution path unchanged.
var errNotDedicatedDatabaseBarrier = errors.New("server: database has no dedicated barrier store")

// restServiceRegistry is the concrete implementation of rest.ServiceRegistry.
// It bridges the per-database DatabaseServiceRegistry with the per-tenant
// TenantServicer and BarrierRegistry to enforce (tenant, db) request scoping
// with per-tenant keyspace isolation.
//
// Tenant resolution order:
//  1. Look up the tenant via tenantSvc.GetTenant. Unknown tenant →
//     TenantNotFoundError (404). When no tenantSvc is configured, the lookup
//     is skipped and any tenant name is accepted (backward-compatible).
//  2. When a BarrierRegistry is configured and the tenant uses IsolationBarrier,
//     validate that the tenant's barrier is not sealed.
//  3. The database is resolved from the DatabaseServiceRegistry. When the
//     database is not found, DatabaseNotFoundError is returned.
//  4. Every (tenant, db) pair — including the bootstrap (system, "system")
//     pair — receives a namespace-prefixed service set so that each database's
//     keys are physically separated under the shared Raft state machine.
//
// Both IsolationBarrier and IsolationPrefix modes use namespace prefixing for
// keyspace separation. The difference is that IsolationBarrier additionally
// validates the per-tenant cryptographic barrier (when the seal subsystem is
// configured). The IsolationType name reflects what the admin is configuring
// from their perspective.
//
// restServiceRegistry is safe for concurrent use.
type restServiceRegistry struct {
	dbReg        *DatabaseServiceRegistry
	barrierReg   *seal.BarrierRegistry
	tenantSvc    tenant.TenantServicer
	catalog      DatabaseCatalog
	tenantHosts  *TenantHostRegistry
	raftBackend  *RaftBackendFactory
	accountant   *quota.Accountant
	systemTenant string

	// sqlDefaultDB is the configured default SQL database name (the dbname
	// every MySQL/PostgreSQL wire client connects with unless it names
	// another). Like the registry's own DefaultName, it EXISTS for every
	// provisioned tenant without a per-tenant create: the composite
	// "<tenant>/<db>/" namespace applied by resolve keeps each tenant's view
	// of it fully disjoint, so treating it as universal grants no
	// cross-tenant access -- it only spares every tenant an explicit
	// CREATE DATABASE for the name the wire protocols already advertise.
	// Empty when the server has no SQL surface configured.
	sqlDefaultDB string

	// dbManager resolves a database's own configuration (barrier, dedicated
	// store, backend type) so resolve can route a database that carries its
	// OWN encryption barrier -- independent of the owning tenant's isolation
	// mode -- to its dedicated encrypting store. nil when the server was not
	// configured with a database manager, in which case no database can have
	// a dedicated barrier store and resolve behaves exactly as before.
	dbManager *database.Manager

	// dedicatedStore caches the per-database service bundles built by
	// resolveDedicatedDatabaseBarrier for the non-raft (direct encrypting
	// Backend()) case. Always non-nil; unused when dbManager is nil.
	dedicatedStore *dedicatedStoreServiceCache

	// collections caches the (tenant, db)-namespaced collection services
	// applyComposite builds over the wrapped engines. Always non-nil. Like
	// dedicatedStore, its entries carry process-local increment ID
	// generators, so its lifetime is observable -- see
	// compositeCollectionCache and Server.buildServiceRegistry.
	collections *compositeCollectionCache
}

// NewRESTServiceRegistry creates a rest.ServiceRegistry that resolves
// per-(tenant, db) services from the given database registry, using the
// optional barrier registry for sealed-state enforcement and the optional
// tenant service for IsolationType lookup and unknown-tenant rejection.
//
// When barrierReg is nil, barrier checks are skipped.
// When tenantSvc is nil, tenant lookup and unknown-tenant rejection are skipped
// (backward-compatible with deployments that do not use the tenant service).
// systemTenant is the canonical system tenant name (typically
// tenant.SystemTenantID), used as the tenant-segment fallback for unscoped
// requests. It is prefixed like any other tenant — it does NOT bypass
// namespace prefixing.
func NewRESTServiceRegistry(
	dbReg *DatabaseServiceRegistry,
	barrierReg *seal.BarrierRegistry,
	systemTenant string,
	opts ...RESTRegistryOption,
) rest.ServiceRegistry {
	r := &restServiceRegistry{
		dbReg:          dbReg,
		barrierReg:     barrierReg,
		systemTenant:   systemTenant,
		dedicatedStore: newDedicatedStoreServiceCache(),
		collections:    newCompositeCollectionCache(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RESTRegistryOption configures optional restServiceRegistry dependencies.
type RESTRegistryOption func(*restServiceRegistry)

// WithTenantServicer sets the tenant service used to look up tenant metadata
// (including IsolationType) and reject requests for unknown tenants.
func WithTenantServicer(svc tenant.TenantServicer) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.tenantSvc = svc
	}
}

// WithSharedSQLDefaultDatabase marks the configured default SQL database
// name as existing for every tenant; see restServiceRegistry.sqlDefaultDB.
func WithSharedSQLDefaultDatabase(name string) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.sqlDefaultDB = name
	}
}

// withTenantHostRegistry sets the per-tenant volume Host registry. When
// configured, IsolationVolume tenants are routed to their dedicated per-tenant
// Raft Host (full engines on the tenant's own DataRoot) instead of the shared
// system service set. Prefix/barrier/system tenants are unaffected. It is the
// REST-registry-level counterpart of the server-level WithTenantHostRegistry.
func withTenantHostRegistry(reg *TenantHostRegistry) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.tenantHosts = reg
	}
}

// withRaftBackendFactory sets the dedicated per-database raft shard factory.
// When configured, IsolationRaft tenants are routed to their database's
// dedicated Raft shard on the shared system Host (full engines on the tenant's
// own DataRoot) instead of the shared system service set. Other isolation modes
// are unaffected.
func withRaftBackendFactory(f *RaftBackendFactory) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.raftBackend = f
	}
}

// withQuotaAccountant sets the storage-byte quota accountant. When configured,
// the resolved KV and MVCC servicers for any non-system tenant with a positive
// MaxStorageBytes limit are wrapped so writes are enforced and accounted before
// commit. Tenants with no limit (or the system tenant) are left unwrapped, so
// the hot path is unaffected for unlimited workloads.
func withQuotaAccountant(acct *quota.Accountant) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.accountant = acct
	}
}

// withDatabaseManager sets the database.Manager used to resolve a database's
// own barrier/dedicated-store configuration. When configured, a database that
// carries its own encryption barrier over its own dedicated store (an
// explicit --data-dir, or Backend == database.BackendRaft) is routed to that
// dedicated encrypting store by resolve, independent of the owning tenant's
// isolation mode. See resolveDedicatedDatabaseBarrier.
func withDatabaseManager(mgr *database.Manager) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.dbManager = mgr
	}
}

// WithDatabaseCatalog sets the catalog used to check whether a database exists
// for a tenant. When configured, any database that exists resolves to a
// namespaced view of the shared service set, even when it is not pre-registered
// in the DatabaseServiceRegistry. When nil, only databases pre-registered in
// the DatabaseServiceRegistry (plus the default database) are resolvable.
func WithDatabaseCatalog(catalog DatabaseCatalog) RESTRegistryOption {
	return func(r *restServiceRegistry) {
		r.catalog = catalog
	}
}

// resolve validates the (tenant, db) pair and returns the DatabaseServices
// wrapped with composite (tenant, db) namespace isolation. Every engine is
// wrapped with a uniform "<tenant>/<db>/" prefix so that each database's keys
// are logically separated under the shared Raft state machine.
func (r *restServiceRegistry) resolve(tenantID, db string) (*DatabaseServices, error) {
	isolation, info, err := r.resolveTenant(tenantID)
	if err != nil {
		return nil, err
	}

	// IsolationVolume tenants are backed by a dedicated per-tenant Raft Host on
	// their own DataRoot (full engines, encrypted under the tenant DEK). The
	// per-tenant Host exposes one shard shared by all the tenant's databases;
	// the composite (tenant, db) namespace prefix below keeps each database's
	// keyspace disjoint, exactly as on the shared system shard.
	if isolation == tenant.IsolationVolume {
		// Fail closed: a volume-isolated tenant MUST be served from its dedicated
		// per-tenant Host. When the Host registry is not wired into this server,
		// falling through to the shared-store path below would silently downgrade
		// isolation and strand the tenant's data on the wrong backend.
		if r.tenantHosts == nil {
			return nil, &rest.IsolationBackendUnavailableError{TenantID: tenantID, Isolation: string(tenant.IsolationVolume)}
		}
		if !r.databaseExists(tenantID, db) {
			return nil, &rest.DatabaseNotFoundError{TenantID: tenantID, Database: db}
		}
		base, err := r.tenantHosts.Services(context.Background(), tenantID)
		if err != nil {
			return nil, err
		}
		prefix := tenant.ComposeNamespace(tenantID, db)
		return r.applyQuota(r.applyComposite(base, prefix), tenantID, info), nil
	}

	// IsolationRaft tenants back each database with its OWN dedicated Raft group
	// (shard) on the shared system Host, rooted under the tenant's DataRoot and
	// encrypted under the tenant DEK. Because every database has a dedicated
	// shard, its keyspace is already physically isolated — no composite
	// namespace prefix is applied (unlike the single-shard volume/system case).
	if isolation == tenant.IsolationRaft {
		// Fail closed: a raft-isolated tenant's databases MUST be served from
		// their dedicated Raft shards. When the raft backend factory is not wired
		// into this server, falling through to the shared-store path below would
		// silently downgrade isolation and strand the tenant's data.
		if r.raftBackend == nil {
			return nil, &rest.IsolationBackendUnavailableError{TenantID: tenantID, Isolation: string(tenant.IsolationRaft)}
		}
		// A dedicated-raft database's existence and ownership are authoritative in
		// the REPLICATED placement table, not the node-local database catalog. The
		// reconciler attaches the shard on every assigned node from the placement
		// entry, but only the creating node holds a local catalog (meta.json)
		// record. Gating on the local catalog (databaseExists) here would 404 the
		// database on every other node after a leader change or restart even
		// though its shard is attached and holds the data. The RaftBackendFactory
		// resolves (and lazily attaches) the shard from the placement entry keyed
		// by (tenantID, db) — which preserves tenant ownership — so it is the
		// correct existence authority. A genuinely unknown database surfaces as
		// ErrRaftDatabaseUnknown, mapped to a 404 below.
		base, err := r.raftBackend.Services(context.Background(), tenantID, db)
		if err != nil {
			if errors.Is(err, ErrRaftDatabaseUnknown) {
				return nil, &rest.DatabaseNotFoundError{TenantID: tenantID, Database: db}
			}
			return nil, err
		}
		return r.applyQuota(base, tenantID, info), nil
	}

	// A specific database may carry its OWN encryption barrier over its OWN
	// dedicated store (an explicit --data-dir, or Backend == BackendRaft --
	// see database.DatabaseConfig.HasDedicatedStore / DatabaseBarrierManager),
	// independent of the owning tenant's isolation mode. When that applies,
	// the database's own encrypting store is the data-plane authority: its
	// keys physically live in its own encrypted store, never the shared
	// engine, and a sealed database barrier must fail closed rather than
	// falling through to dbReg.Default() below (which would silently serve a
	// different, unencrypted keyspace under the same database name).
	if svcs, err := r.resolveDedicatedDatabaseBarrier(tenantID, db, info); !errors.Is(err, errNotDedicatedDatabaseBarrier) {
		return svcs, err
	}

	// A database is a key-namespace layered over the shared service set, so
	// there is only one underlying *DatabaseServices. A pre-registered set for
	// the named database is used when present (e.g. the default database);
	// otherwise any database that exists for the tenant resolves to a
	// namespaced view of the default service set.
	svcs := r.dbReg.GetOrDefault(db)
	if svcs == nil {
		if !r.databaseExists(tenantID, db) {
			return nil, &rest.DatabaseNotFoundError{TenantID: tenantID, Database: db}
		}
		svcs = r.dbReg.Default()
		if svcs == nil {
			return nil, &rest.DatabaseNotFoundError{TenantID: tenantID, Database: db}
		}
	}

	// The composite prefix is uniform and non-empty for every (tenant, db)
	// pair — including the bootstrap (system tenant, "system" database) pair,
	// which yields "system/system/". Every engine is wrapped so that keys are
	// transparently scoped, which keeps each database's keyspace disjoint from
	// every other database and every other tenant.
	prefix := tenant.ComposeNamespace(tenantID, db)
	return r.applyQuota(r.applyComposite(svcs, prefix), tenantID, info), nil
}

// resolveDedicatedDatabaseBarrier resolves a database that carries its OWN
// encryption barrier over its OWN dedicated (non-raft) store, independent of
// the owning tenant's isolation mode (see database.DatabaseConfig.HasDedicatedStore
// and database.DatabaseBarrierManager). It returns errNotDedicatedDatabaseBarrier
// -- checked with errors.Is -- when the (tenant, db) pair is not such a
// database (no manager configured, no such database, not owned by tenantID,
// no barrier-enabled dedicated store, or a raft-backed database -- raft-backed
// isolation is served exclusively through an IsolationRaft tenant's dedicated
// shard, never through a per-database barrier bolted onto a shared/prefix
// tenant), in which case the caller falls through to the normal
// not-found/shared-store resolution path unchanged. Any other returned error
// is a real, final resolution outcome (including a fail-closed sealed
// backend error) that the caller returns as-is.
//
// Only the non-raft dedicated-store shape (an explicit --data-dir) is served
// here, directly over the database's own encrypting Backend()
// (buildDedicatedStoreServices); it fails closed when the database's barrier
// is sealed rather than falling back to the shared (unencrypted) store.
func (r *restServiceRegistry) resolveDedicatedDatabaseBarrier(tenantID, db string, info *tenant.TenantInfo) (*DatabaseServices, error) {
	if r.dbManager == nil {
		return nil, errNotDedicatedDatabaseBarrier
	}
	dbase, err := r.dbManager.Get(db)
	if err != nil {
		return nil, errNotDedicatedDatabaseBarrier
	}

	// Ownership check mirrors managerCatalog.DatabaseExists and
	// managerRaftInfoSource.databaseInfo: the system tenant sees databases
	// with no owner or the system owner; every other tenant sees only
	// databases it owns. A mismatch is treated as "not this database" so the
	// caller falls through to the normal not-found/shared-store handling
	// instead of granting cross-tenant access to a dedicated encrypting store.
	owner := dbase.TenantID()
	if tenantID == r.systemTenant {
		if owner != "" && owner != r.systemTenant {
			return nil, errNotDedicatedDatabaseBarrier
		}
	} else if owner != tenantID {
		return nil, errNotDedicatedDatabaseBarrier
	}

	cfg := dbase.Config()
	if cfg.Barrier == nil || !cfg.Barrier.Enabled || !cfg.HasDedicatedStore() {
		return nil, errNotDedicatedDatabaseBarrier
	}

	// Raft-backed isolation is never resolved here: it comes exclusively from
	// an IsolationRaft tenant's dedicated shard (handled above by the
	// tenant.IsolationRaft branch in resolve), never from a per-database
	// barrier layered onto a shared/prefix tenant's database.
	if cfg.Backend == database.BackendRaft {
		return nil, errNotDedicatedDatabaseBarrier
	}

	return r.applyQuota(r.dedicatedStore.getOrBuild(dbase), tenantID, info), nil
}

// applyQuota wraps the resolved KV and MVCC servicers with storage-byte quota
// enforcement when an accountant is configured, the tenant is known, it is not
// the system tenant, and it has a positive MaxStorageBytes limit. Otherwise it
// returns svcs unchanged so unlimited and system-tenant workloads keep a plain
// passthrough on the data-plane hot path.
func (r *restServiceRegistry) applyQuota(svcs *DatabaseServices, tenantID string, info *tenant.TenantInfo) *DatabaseServices {
	if r.accountant == nil || info == nil || info.Quotas == nil || tenantID == r.systemTenant {
		return svcs
	}
	limit := info.Quotas.MaxStorageBytes
	if limit <= 0 {
		return svcs
	}
	out := *svcs
	if out.KV != nil {
		out.KV = newQuotaKVService(out.KV, r.accountant, tenantID, limit)
	}
	if out.MVCC != nil {
		out.MVCC = newQuotaMVCCService(out.MVCC, r.accountant, tenantID, limit)
	}
	// Batch and Transaction engines commit KV/MVCC Puts directly to the host,
	// bypassing the wrapped KV/MVCC servicers above, so they are wrapped here
	// too — otherwise a tenant could exceed MaxStorageBytes through batched or
	// transactional writes.
	if out.Batch != nil {
		out.Batch = newQuotaBatchService(out.Batch, r.accountant, tenantID, limit)
	}
	if out.Transaction != nil {
		out.Transaction = newQuotaTransactionService(out.Transaction, r.accountant, tenantID, limit)
	}
	// TimeSeries and Vector engines write independent tenant payload (data
	// points, embeddings + metadata) outside the KV/MVCC servicers, so they are
	// wrapped here too. See the package note in quota_batch.go for the byte-size
	// measures and the rationale for leaving the Index engine unwrapped (N/A).
	if out.TimeSeries != nil {
		out.TimeSeries = newQuotaTimeSeriesService(out.TimeSeries, r.accountant, tenantID, limit)
	}
	if out.Vector != nil {
		out.Vector = newQuotaVectorService(out.Vector, r.accountant, tenantID, limit)
	}
	return &out
}

// databaseExists reports whether the (tenant, db) database is resolvable. The
// default database always exists. Named databases are resolved through the
// catalog when configured (which enforces per-tenant ownership); without a
// catalog, only databases pre-registered in the DatabaseServiceRegistry exist.
func (r *restServiceRegistry) databaseExists(tenantID, db string) bool {
	if db == "" || db == r.dbReg.DefaultName() {
		return true
	}
	// The configured default SQL database exists for every tenant; the
	// composite namespace keeps each tenant's view disjoint. See
	// sqlDefaultDB.
	if r.sqlDefaultDB != "" && db == r.sqlDefaultDB {
		return true
	}
	if r.catalog != nil {
		return r.catalog.DatabaseExists(tenantID, db)
	}
	return r.dbReg.Get(db) != nil
}

// resolveTenant looks up the tenant metadata (when a tenant service is
// configured) and validates the barrier state. It returns the effective
// IsolationType for the tenant and, when available, the tenant's metadata
// record (used to derive storage-quota enforcement). The info is nil on the
// fallback paths (no tenant service, or system-tenant lookup miss), in which
// case no quota wrapper is installed.
func (r *restServiceRegistry) resolveTenant(tenantID string) (tenant.IsolationType, *tenant.TenantInfo, error) {
	if r.tenantSvc == nil {
		// No tenant service: skip lookup, behave as before. There is no
		// creation-time validator (pkg/tenant.CreateTenant) standing between
		// an untrusted request header and tenant.ComposeNamespace on this
		// path, so the charset check that normally runs at tenant creation
		// must run here instead — otherwise a header tenant like "a/b"
		// builds the prefix "a/b/<db>/", colliding with tenant "a" + key
		// "b/...".
		if err := validateTenantScopeCharset(tenantID); err != nil {
			return "", nil, err
		}
		return tenant.IsolationBarrier, nil, r.checkTenantBarrier(tenantID)
	}

	// Bound the lookup so a stalled Raft ReadIndex during restart cannot
	// hang every REST request indefinitely. The system-tenant fallback
	// below still lets requests proceed against the barrier check when
	// the lookup fails (including timeouts).
	lookupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	info, err := r.tenantSvc.GetTenant(lookupCtx, tenantID)
	cancel()
	if err != nil {
		// System tenant is auto-provisioned; treat a lookup miss or a sealed
		// cluster barrier as "not yet materialized" rather than a hard 404 so
		// fresh deployments keep working before the operator initializes the
		// barrier via /v1/barrier/initialize. System tenant always uses
		// barrier isolation per server defaults.
		if tenantID == r.systemTenant {
			return tenant.IsolationBarrier, nil, r.checkTenantBarrier(tenantID)
		}
		if errors.Is(err, tenant.ErrTenantNotFound) {
			return "", nil, &rest.TenantNotFoundError{TenantID: tenantID}
		}
		return "", nil, &rest.TenantNotFoundError{TenantID: tenantID}
	}

	isolation := info.EffectiveIsolationType()

	// For barrier, volume, and raft modes, also validate the per-tenant barrier
	// state. A sealed barrier blocks data-plane access (volume and raft tenants
	// store their data encrypted under that barrier's DEK).
	if isolation == tenant.IsolationBarrier || isolation == tenant.IsolationVolume || isolation == tenant.IsolationRaft {
		if err := r.checkTenantBarrier(tenantID); err != nil {
			return "", nil, err
		}
	}

	return isolation, info, nil
}

// validateTenantScopeCharset rejects a request-time tenant scope containing a
// byte that would corrupt the composite "<tenant>/<db>/" namespace prefix
// built by tenant.ComposeNamespace: a control character, or a path separator
// ('/' or '\'). It mirrors — and returns the same *tenant.InvalidTenantIDCharError
// as — the charset check pkg/tenant.validateReservedTenantID applies at tenant
// CREATION time, which a request whose tenant scope is validated only via the
// no-tenant-service fallback (resolveTenant, when r.tenantSvc is nil) never
// passes through. Without this, a header tenant "a/b" builds the prefix
// "a/b/<db>/", colliding with tenant "a" + key "b/...".
func validateTenantScopeCharset(tenantID string) error {
	for i := 0; i < len(tenantID); i++ {
		b := tenantID[i]
		if b < 0x20 || b == '/' || b == '\\' {
			return &tenant.InvalidTenantIDCharError{TenantID: tenantID, Byte: b}
		}
	}
	return nil
}

// checkTenantBarrier validates the tenant against the barrier registry when
// applicable. It is a no-op for the system tenant or when no registry is
// configured.
func (r *restServiceRegistry) checkTenantBarrier(tenantID string) error {
	if r.barrierReg == nil || tenantID == r.systemTenant {
		return nil
	}
	tb, err := r.barrierReg.GetTenant(tenantID)
	if err != nil {
		if errors.Is(err, seal.ErrTenantNotFound) || errors.Is(err, seal.ErrEmptyTenantID) {
			return &rest.TenantNotFoundError{TenantID: tenantID}
		}
		return &rest.TenantNotFoundError{TenantID: tenantID}
	}
	if tb.IsSealed() {
		return &rest.BarrierSealedError{TenantID: tenantID}
	}
	return nil
}

// applyComposite returns a copy of svcs with every non-nil engine wrapped so
// that keys are transparently prefixed with the composite (tenant, db)
// namespace and stripped on the way out. The prefix is guaranteed non-empty by
// tenant.ComposeNamespace, so every (tenant, db) pair — including the
// bootstrap (system, "system") pair — is wrapped and isolated.
//
// Collection is REBUILT over the wrapped engines rather than copied. This
// comment previously claimed the opposite — that Collection "is intentionally
// not wrapped: it composes the namespaced KV and MVCC engines below it rather
// than keying into the shared store directly, so it inherits their isolation"
// — and that claim is what hid go-qrdb#441 from review. A struct copy carries
// the collection service the BUNDLE was built with, and every bundle is built
// over the raw, pre-wrap engines (buildDataPlaneServices,
// lifecycle.BuildServices), so the copy keyed straight into the shared store:
// under prefix isolation two tenants naming the same database and collection
// wrote the same "__col:{database}:{collection}/{id}" keys. A service inherits
// isolation from the engines it HOLDS, never from the engines its neighbours
// in the struct hold. See buildCompositeCollectionService.
func (r *restServiceRegistry) applyComposite(svcs *DatabaseServices, prefix string) *DatabaseServices {
	out := *svcs
	if svcs.KV != nil {
		out.KV = &namespacedKVService{inner: svcs.KV, prefix: prefix}
	}
	if svcs.MVCC != nil {
		out.MVCC = newNamespacedMVCCService(svcs.MVCC, prefix)
	}
	if svcs.Batch != nil {
		out.Batch = newNamespacedBatchService(svcs.Batch, prefix)
	}
	if svcs.Transaction != nil {
		out.Transaction = newNamespacedTransactionService(svcs.Transaction, prefix)
	}
	if svcs.Lease != nil {
		out.Lease = newNamespacedLeaseService(svcs.Lease, prefix)
	}
	if svcs.TimeSeries != nil {
		out.TimeSeries = newNamespacedTimeSeriesService(svcs.TimeSeries, prefix)
	}
	if svcs.Index != nil {
		out.Index = newNamespacedIndexService(svcs.Index, prefix)
	}
	if svcs.Watch != nil {
		out.Watch = newNamespacedWatchService(svcs.Watch, prefix)
	}
	if svcs.Vector != nil {
		out.Vector = newNamespacedVectorService(svcs.Vector, prefix)
	}
	out.Collection = r.collections.getOrBuild(svcs, &out, prefix)
	return &out
}

// KVService returns the KVServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.KV == nil {
		return nil, notImplemented("kv", "")
	}
	return svcs.KV, nil
}

// MVCCService returns the MVCCKVServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) MVCCService(tenantID, db string) (service.MVCCKVServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.MVCC == nil {
		return nil, notImplemented("mvcc", "")
	}
	return svcs.MVCC, nil
}

// WatchService returns the WatchServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) WatchService(tenantID, db string) (service.WatchServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Watch == nil {
		return nil, notImplemented("watch", "")
	}
	return svcs.Watch, nil
}

// LeaseService returns the LeaseServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) LeaseService(tenantID, db string) (service.LeaseServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Lease == nil {
		return nil, notImplemented("lease", "")
	}
	return svcs.Lease, nil
}

// IndexService returns the IndexServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) IndexService(tenantID, db string) (service.IndexServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Index == nil {
		return nil, notImplemented("index", "")
	}
	return svcs.Index, nil
}

// TimeSeriesService returns the TimeSeriesServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) TimeSeriesService(tenantID, db string) (service.TimeSeriesServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.TimeSeries == nil {
		return nil, notImplemented("timeseries", "")
	}
	return svcs.TimeSeries, nil
}

// VectorService returns the VectorServicer for the given (tenant, db) scope.
// VectorService returns the VectorServicer for the given (tenant, db) scope.
// Returns ServiceNotImplementedError when the resolved database has no
// Vector service attached (vector support typically requires the
// qrdb_vector build tag to register a servicer during startup).
func (r *restServiceRegistry) VectorService(tenantID, db string) (service.VectorServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Vector == nil {
		return nil, notImplemented("vector", "")
	}
	return svcs.Vector, nil
}

// TransactionService returns the TransactionServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) TransactionService(tenantID, db string) (service.TransactionServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Transaction == nil {
		return nil, notImplemented("transaction", "")
	}
	return svcs.Transaction, nil
}

// BatchService returns the BatchServicer for the given (tenant, db) scope.
func (r *restServiceRegistry) BatchService(tenantID, db string) (service.BatchServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Batch == nil {
		return nil, notImplemented("batch", "")
	}
	return svcs.Batch, nil
}

// TableService returns the TableServicer for the given (tenant, db) scope.
// It is built fresh on every call from the resolved (tenant, db)-scoped
// KV/MVCC/Transaction servicers -- mirroring how TransactionService and
// MVCCService are resolved -- rather than cached, since resolve already
// applies the per-tenant barrier/quota/isolation wrapping every other
// data-plane path relies on; TableService itself holds no additional state
// beyond those three servicers plus a SequenceServicer, so constructing it
// per call costs nothing that isn't already paid by resolve.
func (r *restServiceRegistry) TableService(tenantID, db string) (service.TableServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.KV == nil || svcs.MVCC == nil || svcs.Transaction == nil {
		return nil, notImplemented("table", "")
	}
	seq := service.NewSequenceService(svcs.MVCC, svcs.Transaction)
	return service.NewTableService(svcs.KV, svcs.MVCC, svcs.Transaction, seq), nil
}

// SequenceService returns the SequenceServicer for the given (tenant, db)
// scope, built the same way TableService builds its own internal one.
func (r *restServiceRegistry) SequenceService(tenantID, db string) (service.SequenceServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.MVCC == nil || svcs.Transaction == nil {
		return nil, notImplemented("sequence", "")
	}
	return service.NewSequenceService(svcs.MVCC, svcs.Transaction), nil
}

// PrivilegeService returns the PrivilegeServicer for the given (tenant, db)
// scope -- the SQL table-privilege catalog. It is built fresh on every call
// from the resolved (tenant, db)-scoped KV/MVCC/Transaction servicers, for
// the same reason TableService is: resolve already applies the per-tenant
// barrier/quota/isolation wrapping, and PrivilegeService holds no state
// beyond those three servicers.
func (r *restServiceRegistry) PrivilegeService(tenantID, db string) (service.PrivilegeServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.KV == nil || svcs.MVCC == nil || svcs.Transaction == nil {
		return nil, notImplemented("privilege", "")
	}
	return service.NewPrivilegeService(svcs.KV, svcs.MVCC, svcs.Transaction), nil
}

// LockService returns the LockServicer for the given (tenant, db) scope --
// the distributed-lock surface. It is built fresh on every call from the
// resolved (tenant, db)-scoped Transaction/Lease/Watch/MVCC servicers, the
// same way TableService, SequenceService and PrivilegeService are built from
// theirs, because resolve has already applied the per-tenant
// barrier/quota/isolation wrapping those four carry.
//
// That wrapping is the whole of go-qrdb#455. A lock record is keyed
// "__sys:lock/{key}" with the key supplied by the caller and no tenant
// segment, and before this accessor existed every transport held ONE
// process-wide servicer built by lifecycle.BuildServices over the raw,
// pre-namespace engines. Under prefix isolation two tenants locking the same
// name therefore took the same lock -- denial of the name for the second
// tenant, and disclosure of the first tenant's lock names and HolderIDs
// through List, Status and LockHeldError. Confinement is by construction
// here: a lock lands wherever the engines the servicer HOLDS put it, which
// is the caller's own "<tenant>/<database>/" namespace. Key-namespacing
// inside the singleton was the rejected alternative -- an exemption keyed on
// a string prefix is a SECOND tenancy mechanism beside the namespace, and
// docs/multi-tenancy/architecture.md rejects that by name.
//
// It is NOT a field on DatabaseServices, for the reason
// sdk/go/driver/node.databaseServices gives for leaving Table and Sequence
// off that bundle: every bundle is built over raw, pre-wrap engines, so a
// composer filling a Lock field would produce a second, unscoped servicer
// that a struct copy then carries straight through the namespacing wrapper
// -- which is exactly how go-qrdb#441 happened to Collection.
//
// Per-call construction holds no resource: lock.NewLocker stores the four
// servicers and a logger and subscribes to nothing. A blocking Lock opens
// its watcher inside the call and closes it on return, and a lease is
// granted per acquisition, so nothing here outlives the operation that
// created it.
//
// ServiceNotImplementedError is returned when any of the four is absent --
// a database on a dedicated non-raft store has no lease, watch or MVCC
// engine, so it cannot serve a lock and must refuse rather than serve a
// lock that nothing can expire.
func (r *restServiceRegistry) LockService(tenantID, db string) (service.LockServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Transaction == nil || svcs.Lease == nil || svcs.Watch == nil || svcs.MVCC == nil {
		return nil, notImplemented("lock", "")
	}
	return service.NewLockService(svcs.Transaction, svcs.Lease, svcs.Watch, svcs.MVCC, nil)
}

// CollectionService returns the CollectionServicer for the given (tenant, db)
// scope. It resolves exactly like KVService and MVCCService -- the resolved
// bundle's Collection is the one applyComposite built over that scope's
// namespaced engines, so a document written through it lands under the
// caller's "<tenant>/<database>/" prefix rather than at the bare
// "__col:{database}:{collection}/{id}" key every tenant would otherwise share
// (go-qrdb#441).
//
// The dedicated-store and dedicated-raft resolution paths return their own
// bundle's Collection unchanged, because those bundles are already built over
// a store that belongs to exactly one (tenant, database) pair.
func (r *restServiceRegistry) CollectionService(tenantID, db string) (service.CollectionServicer, error) {
	svcs, err := r.resolve(tenantID, db)
	if err != nil {
		return nil, err
	}
	if svcs.Collection == nil {
		return nil, notImplemented("collection", "")
	}
	return svcs.Collection, nil
}
