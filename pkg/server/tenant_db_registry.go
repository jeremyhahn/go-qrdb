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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

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
}

// tenantInfoSource is the minimal tenant-metadata lookup the registry needs to
// resolve a tenant's DataRoot. It is satisfied by lookupDataRootSource.
type tenantInfoSource interface {
	dataRootOf(ctx context.Context, tenantID string) (string, error)
}

// NewTenantHostRegistry creates a registry that builds per-tenant volume Hosts
// using the given barrier registry (for the per-tenant encryptor) and tenant
// metadata source (for the DataRoot). Both must be non-nil for the registry to
// resolve any tenant.
func NewTenantHostRegistry(barrierReg *seal.BarrierRegistry, tenants tenantInfoSource, logger *slog.Logger) *TenantHostRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &TenantHostRegistry{
		barrierReg: barrierReg,
		tenants:    tenants,
		logger:     logger,
		hosts:      make(map[string]*tenantHost),
	}
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
func NewTenantHostRegistryFromLookup(barrierReg *seal.BarrierRegistry, lookup TenantLookup, logger *slog.Logger) *TenantHostRegistry {
	var source tenantInfoSource
	if lookup != nil {
		source = &lookupDataRootSource{lookup: lookup}
	}
	return NewTenantHostRegistry(barrierReg, source, logger)
}

// lookupDataRootSource adapts a TenantLookup to the tenantInfoSource
// interface, resolving a tenant's DataRoot from its persisted metadata at the
// moment it is asked.
type lookupDataRootSource struct {
	lookup TenantLookup
}

// dataRootOf returns the DataRoot recorded for the tenant, or an error.
func (s *lookupDataRootSource) dataRootOf(ctx context.Context, tenantID string) (string, error) {
	info, err := s.lookup.GetTenant(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return info.DataRoot, nil
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

	th, err := newTenantHost(tenantID, dataRoot, tb, r.logger)
	if err != nil {
		return nil, err
	}
	r.hosts[tenantID] = th
	return th.services, nil
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
