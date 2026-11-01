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
	"log/slog"
	"sync"
	"sync/atomic"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

var _ service.TenantReplicaHosts = (*TenantHostRegistry)(nil)

// TenantHostRegistry lazily builds and caches one per-tenant Raft Host per
// IsolationVolume tenant and exposes its data-plane service set. A single Host
// (one local shard) backs all of a tenant's databases; the resolver applies the
// composite (tenant, db) namespace prefix on top so each database's keyspace is
// disjoint, mirroring how databases share the system shard.
//
// The Host's encrypted state lives under the tenant's DataRoot, keyed by the
// tenant barrier's DEK. The registry resolves the DataRoot from the tenant
// metadata and the barrier from the BarrierRegistry, both of which the daemon
// re-establishes (create or boot re-attach) before the registry is consulted.
//
// TenantHostRegistry is safe for concurrent use.
type TenantHostRegistry struct {
	barrierReg *seal.BarrierRegistry
	tenants    tenantInfoSource
	logger     *slog.Logger

	// importSessionCfg bounds a chunked import session on every tenant Host
	// this registry builds (config.ImportSession, "import_session:" in
	// YAML). The zero value resolves to the package defaults
	// WithImportSessionLimits documents. See WithImportSessionConfig.
	importSessionCfg qrdbconfig.ImportSessionConfig

	// indexCfg caps the secondary indexes of every tenant Host this registry
	// builds (config.Index, "index:" in YAML). The zero value leaves them
	// unlimited. See WithTenantHostIndexConfig.
	indexCfg qrdbconfig.IndexConfig

	// tsCfg caps the metric names of every tenant Host this registry
	// builds (config.TimeSeries.MaxMetrics); the zero value leaves them
	// unlimited. See WithTenantHostTimeSeriesConfig.
	tsCfg qrdbconfig.TimeSeriesConfig

	// watchHubOpts configures every tenant Host's watch hub. See
	// WithTenantHostWatchHubOptions.
	watchHubOpts []watch.HubOption

	mu    sync.Mutex
	hosts map[string]*tenantHost

	// migrating marks tenants whose barrier is mid encryption-migration recycle.
	// While set, Services must NOT lazily (re)build the tenant Host: the barrier
	// is being swapped from the source (none) to a keyed barrier and the at-rest
	// stores are being re-encrypted, so a build started in this window would wrap
	// the host with the stale source barrier and fail to decrypt the already-keyed
	// WAL/SM — and cache that broken host. Set for the duration of
	// MigrateEncryption (up to the barrier replacement); a concurrent Services
	// returns ErrTenantMigrationInProgress (retryable) instead.
	migrating sync.Map // tenantID -> struct{}

	// homeOps counts each tenant's in-flight standalone home-store
	// operations, which AdmitHome admits against migrating; MigrateEncryption
	// drains them before it touches the barrier. See tenant_home_admission.go.
	homeOps sync.Map // tenantID -> *homeOpGate

	// onMigrationPhase, when set, observes each journal checkpoint
	// MigrateEncryption writes, after it is durable; onHomeDrainWait observes
	// a drain that found operations in flight, before it waits. Tests set
	// them to act at an exact point in a migration without sleeping.
	onMigrationPhase func(tenantID string, phase seal.MigrationPhase)
	onHomeDrainWait  func(tenantID string)

	// raftShards is the server's raft backend factory, which runs a tenant's
	// dedicated raft shards -- its home group -- beside its Host under the same
	// tenant barrier, so the tenant's migration re-encrypts them with its Host's
	// stores. It is nil until the server that serves this registry wires it.
	raftShards atomic.Pointer[RaftBackendFactory]

	// startGate admits each Host build against a whole-node seal's pause
	// (ShardPauser.beginStart); ShardPauser.SetTenants installs it. Unset, it
	// admits every build.
	startGate atomic.Pointer[ShardPauser]
}

// tenantInfoSource is the minimal tenant-metadata lookup the registry needs to
// resolve a tenant's DataRoot. It is satisfied by lookupDataRootSource.
type tenantInfoSource interface {
	dataRootOf(ctx context.Context, tenantID string) (string, error)
}

// TenantHostRegistryOption configures optional TenantHostRegistry behavior at
// construction.
type TenantHostRegistryOption func(*TenantHostRegistry)

// WithTenantHostImportSessionConfig bounds a chunked import session on every
// tenant Host this registry builds. Omitting it leaves every tenant Host at
// the package defaults WithImportSessionLimits documents, not unbounded
// staging.
func WithTenantHostImportSessionConfig(cfg qrdbconfig.ImportSessionConfig) TenantHostRegistryOption {
	return func(r *TenantHostRegistry) { r.importSessionCfg = cfg }
}

// WithTenantHostIndexConfig caps the secondary indexes of every tenant Host
// this registry builds at cfg.MaxIndexes, each Host on its own. Omitting it
// leaves them unlimited.
func WithTenantHostIndexConfig(cfg qrdbconfig.IndexConfig) TenantHostRegistryOption {
	return func(r *TenantHostRegistry) { r.indexCfg = cfg }
}

// WithTenantHostTimeSeriesConfig caps the metric names of every tenant Host
// this registry builds at cfg.MaxMetrics, each Host on its own. Omitting it
// leaves them unlimited.
func WithTenantHostTimeSeriesConfig(cfg qrdbconfig.TimeSeriesConfig) TenantHostRegistryOption {
	return func(r *TenantHostRegistry) { r.tsCfg = cfg }
}

// WithTenantHostWatchHubOptions configures the watch hub of every tenant
// Host this registry builds: the node's watch.buffer_size and the
// watch.max_watchers limit its other hubs share. Omitting it leaves each hub
// at the package defaults, unlimited.
func WithTenantHostWatchHubOptions(opts ...watch.HubOption) TenantHostRegistryOption {
	return func(r *TenantHostRegistry) { r.watchHubOpts = opts }
}

// NewTenantHostRegistry creates a registry that builds per-tenant volume Hosts
// using the given barrier registry (for the per-tenant encryptor) and tenant
// metadata source (for the DataRoot). Both must be non-nil for the registry to
// resolve any tenant.
func NewTenantHostRegistry(barrierReg *seal.BarrierRegistry, tenants tenantInfoSource, logger *slog.Logger, opts ...TenantHostRegistryOption) *TenantHostRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	r := &TenantHostRegistry{
		barrierReg: barrierReg,
		tenants:    tenants,
		logger:     logger,
		hosts:      make(map[string]*tenantHost),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NewTenantHostRegistryFromLookup is the composition-facing constructor that
// derives the tenant-metadata source from a TenantLookup.
//
// The registry is long-lived: it outlives any one tenant service in a
// composition that rebuilds its data plane in place (sdk/go/node recycles the
// whole service graph on a system-scope encryption migration, and the
// registry is deliberately kept running through it). lookup is therefore
// consulted on EVERY DataRoot resolution and never unwrapped into a captured
// *tenant.Service here; a composition whose tenant service can be swapped must
// pass a lookup that resolves the current one (go-qrdb#383).
func NewTenantHostRegistryFromLookup(barrierReg *seal.BarrierRegistry, lookup TenantLookup, logger *slog.Logger, opts ...TenantHostRegistryOption) *TenantHostRegistry {
	var source tenantInfoSource
	if lookup != nil {
		source = &lookupDataRootSource{registry: barrierReg}
	}
	return NewTenantHostRegistry(barrierReg, source, logger, opts...)
}

// lookupDataRootSource adapts a *seal.BarrierRegistry to the
// tenantInfoSource interface, resolving a tenant's DataRoot through its
// catalog Assignment at the moment it is asked. TenantInfo no longer
// carries a resolved path itself, only the pool name (VolumePool), so
// resolution goes through the registry rather than the tenant lookup.
type lookupDataRootSource struct {
	registry *seal.BarrierRegistry
}

// dataRootOf returns the local DataRoot resolved for the tenant, or an error.
func (s *lookupDataRootSource) dataRootOf(ctx context.Context, tenantID string) (string, error) {
	return s.registry.LocalTenantDataRoot(ctx, tenantID)
}

// Services returns the (un-namespaced) data-plane service set backing the named
// IsolationVolume tenant, building and caching the per-tenant Host on first
// access. The tenant's barrier must already be registered and unsealed; a
// sealed or missing barrier returns an error so the request fails closed.
func (r *TenantHostRegistry) Services(ctx context.Context, tenantID string) (*DatabaseServices, error) {
	if r == nil || r.barrierReg == nil || r.tenants == nil {
		return nil, &TenantHostError{TenantID: tenantID, Op: "resolve", Err: ErrTenantHostUnavailable}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if th, ok := r.hosts[tenantID]; ok {
		return th.services, nil
	}
	// Refuse to (re)build while an encryption migration is recycling this
	// tenant's barrier: a build here would wrap the host with the stale source
	// barrier and fail to decrypt the already-keyed at-rest stores. Retryable.
	if _, migrating := r.migrating.Load(tenantID); migrating {
		return nil, &TenantHostError{TenantID: tenantID, Op: "resolve", Err: ErrTenantMigrationInProgress}
	}

	tb, err := r.barrierReg.GetTenant(tenantID)
	if err != nil {
		// A tenant absent from the in-process registry is not necessarily a
		// tenant that doesn't exist: under a degraded boot
		// (--allow-degraded-volume-boot, decision D10) a tenant assigned to
		// a pool this node could not open was never registered at all
		// (reattachVolumeTenants/reattachRaftTenants skip it). Resolve
		// through the volume allocator directly -- independent of whether
		// the barrier was ever registered -- so a caller sees the real
		// cause (*volume.PoolUnavailableError, classified Unavailable) in
		// place of the uninformative "tenant not found" the in-memory
		// lookup alone would give.
		if errors.Is(err, seal.ErrTenantNotFound) {
			if _, derr := r.tenants.dataRootOf(ctx, tenantID); derr != nil {
				return nil, &TenantHostError{TenantID: tenantID, Op: "data-root", Err: derr}
			}
		}
		return nil, &TenantHostError{TenantID: tenantID, Op: "barrier-lookup", Err: err}
	}
	if tb.IsSealed() {
		return nil, &TenantHostError{TenantID: tenantID, Op: "resolve", Err: seal.ErrSealed}
	}

	dataRoot, err := r.tenants.dataRootOf(ctx, tenantID)
	if err != nil {
		return nil, &TenantHostError{TenantID: tenantID, Op: "data-root", Err: err}
	}
	if dataRoot == "" {
		return nil, &TenantHostError{TenantID: tenantID, Op: "data-root", Err: ErrTenantDataRootUnset}
	}

	// The Host is not listed for a seal's pause until it is registered
	// below, so the whole build is held against the pause.
	done, err := r.startGate.Load().beginStart()
	if err != nil {
		return nil, &TenantHostError{TenantID: tenantID, Op: "resolve", Err: err}
	}
	defer done()
	th, err := newTenantHost(ctx, tenantID, dataRoot, tb, r.importSessionCfg, r.indexCfg, r.tsCfg, r.watchHubOpts, r.logger)
	if err != nil {
		return nil, err
	}
	r.hosts[tenantID] = th
	return th.services, nil
}

// Started reports whether tenantID's Host is already built on this node,
// without building it. A background task that must not be the reason a
// tenant's Host starts -- the collection sweeper -- asks this first.
func (r *TenantHostRegistry) Started(tenantID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.hosts[tenantID]
	return ok
}

// shardHosts snapshots each started volume tenant's Raft host, by tenant ID,
// for the shard pauser.
func (r *TenantHostRegistry) shardHosts() map[string]shardHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]shardHost, len(r.hosts))
	for tenantID, th := range r.hosts {
		if th.host != nil {
			out[tenantID] = th.host
		}
	}
	return out
}

// startedServices returns the service set of every tenant Host already built
// on this node, keyed by tenant, without building any. The collection sweeper
// reads each started Host's work journal through it.
func (r *TenantHostRegistry) startedServices() map[string]*DatabaseServices {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*DatabaseServices, len(r.hosts))
	for id, th := range r.hosts {
		out[id] = th.services
	}
	return out
}

// StartedHosts implements service.TenantReplicaHosts: the Host of every
// tenant already built on this node, keyed by tenant, without building any.
// The admin service's replica state hashes read each one.
func (r *TenantHostRegistry) StartedHosts() map[string]service.AdminHost {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]service.AdminHost, len(r.hosts))
	for id, th := range r.hosts {
		if th.adapter != nil {
			out[id] = th.adapter
		}
	}
	return out
}

// Attach builds (and caches) the per-tenant Host eagerly. It is used at boot to
// re-attach IsolationVolume tenants before serving so the first request does
// not pay the Host-construction cost. It is idempotent: an already-attached
// tenant is a no-op.
func (r *TenantHostRegistry) Attach(ctx context.Context, tenantID string) error {
	_, err := r.Services(ctx, tenantID)
	return err
}

// Close stops and closes the named tenant's Host, releasing its Pebble lock,
// and evicts it from the cache. It is a no-op for a tenant with no live Host.
func (r *TenantHostRegistry) Close(tenantID string) error {
	r.mu.Lock()
	th, ok := r.hosts[tenantID]
	if ok {
		delete(r.hosts, tenantID)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return th.Close()
}

// CloseAll stops and closes every live per-tenant Host. Errors are joined so a
// single failing Host does not prevent the rest from closing.
func (r *TenantHostRegistry) CloseAll() error {
	r.mu.Lock()
	hosts := r.hosts
	r.hosts = make(map[string]*tenantHost)
	r.mu.Unlock()

	var errs []error
	for _, th := range hosts {
		if err := th.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
