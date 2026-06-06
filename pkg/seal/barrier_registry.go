// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// BarrierRegistry manages the system barrier and per-tenant barriers.
// It provides a centralized registry for creating, initializing, and
// looking up tenant-specific barriers.
//
// When configured with WithVolumePool and WithPerTenantRegistry, the
// registry can satisfy RegisterTenant calls that pass a nil backend
// by allocating a path from the configured volume pool, opening a
// per-tenant Pebble at that path, and wrapping it as the per-tenant
// StorageBackend. This is the integration point that gives barrier-mode
// tenants their own independent Pebble instances.
type BarrierRegistry struct {
	logger  *slog.Logger
	system  *StorageBarrier
	tenants sync.Map // map[string]*TenantBarrier

	// poolMu guards pool/perTenantReg/defaultPoolName which may be
	// attached either at construction (Options) or post-hoc via Attach.
	poolMu          sync.RWMutex
	pool            objpool.Manager
	poolStateStore  objpool.StateStore // retained for diagnostics; also pins lifetime
	defaultPoolName string
	perTenantReg    pebblestore.PerTenantRegistry
}

// Option configures a BarrierRegistry at construction time.
//
// Options are applied in order; later options override earlier ones
// for fields they touch. Adding new options is non-breaking.
type Option func(*BarrierRegistry)

// WithVolumePool attaches a volume pool manager and its state store
// to the registry. After this option is applied, RegisterTenant calls
// that pass a nil backend will pull a per-tenant path from the named
// pool. The state store is retained as a lifetime pin and for
// diagnostics — strategies that need to read/write cursors do so via
// the manager.
//
// Passing nil for any argument is a no-op for that argument; both
// must be set before RegisterTenant can satisfy a nil-backend call.
func WithVolumePool(mgr objpool.Manager, state objpool.StateStore, defaultPoolName string) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if mgr != nil {
			r.pool = mgr
		}
		if state != nil {
			r.poolStateStore = state
		}
		if defaultPoolName != "" {
			r.defaultPoolName = defaultPoolName
		}
	}
}

// WithPerTenantRegistry attaches the per-tenant Pebble registry. It
// is the second half of the pool-driven RegisterTenant flow: once a
// path is picked from the volume pool, the registry opens (or returns
// an existing) Pebble at that path and the result is wrapped as the
// per-tenant StorageBackend.
func WithPerTenantRegistry(reg pebblestore.PerTenantRegistry) Option {
	return func(r *BarrierRegistry) {
		r.poolMu.Lock()
		defer r.poolMu.Unlock()
		if reg != nil {
			r.perTenantReg = reg
		}
	}
}

// NewBarrierRegistry creates a new BarrierRegistry with the given
// system barrier and applies any supplied options. The system barrier
// must not be nil. Options are applied after the registry is
// constructed so they can mutate registry-private state.
func NewBarrierRegistry(logger *slog.Logger, system *StorageBarrier, opts ...Option) (*BarrierRegistry, error) {
	if system == nil {
		return nil, ErrNilSystemBarrier
	}
	r := &BarrierRegistry{
		logger: logger,
		system: system,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r, nil
}

// Attach applies one or more Options post-construction. It is the
// idiomatic seam for daemon startup wiring where the volume pool and
// per-tenant Pebble registry are not built until after the seal
// subsystem (which constructs the BarrierRegistry) has come up.
//
// Attach is safe to call concurrently with reads but is intended to
// run during boot, not under steady-state load.
func (r *BarrierRegistry) Attach(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
}

// SystemBarrier returns the system-level barrier.
func (r *BarrierRegistry) SystemBarrier() *StorageBarrier { return r.system }

// PerTenantPebbleRegistry returns the attached per-tenant Pebble
// registry, or nil when no registry has been configured. It is
// exposed so daemon shutdown handlers can iterate the registry and
// close its instances cleanly.
func (r *BarrierRegistry) PerTenantPebbleRegistry() pebblestore.PerTenantRegistry {
	r.poolMu.RLock()
	defer r.poolMu.RUnlock()
	return r.perTenantReg
}

// RegisterTenant creates and registers a new tenant barrier with the
// given storage backend, config, and strategies.
//
// When base is non-nil, the supplied backend is used directly — this
// is the keyspace-mode flow and the legacy direct-construction path
// used by every existing caller.
//
// When base is nil, the registry pulls a path from its configured
// volume pool (see WithVolumePool), opens or returns an existing
// per-tenant Pebble at that path via the configured PerTenantRegistry
// (see WithPerTenantRegistry), and wraps the resulting Pebble as the
// per-tenant StorageBackend. This is the barrier-mode flow.
//
// Returns ErrTenantAlreadyExists if the tenant id is already
// registered, ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured
// when the pooled flow is requested but the registry isn't wired for
// it, and ErrPoolBackendNotLocal when the pool returns a backend that
// can't host a Pebble.
func (r *BarrierRegistry) RegisterTenant(
	ctx context.Context,
	tenantID string,
	base StorageBackend,
	config BarrierConfig,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if _, loaded := r.tenants.Load(tenantID); loaded {
		return nil, ErrTenantAlreadyExists
	}

	if base == nil {
		var err error
		base, err = r.allocatePooledBackend(ctx, tenantID)
		if err != nil {
			return nil, err
		}
	}

	tb, err := NewTenantBarrier(tenantID, r.logger, base, config, strategies...)
	if err != nil {
		return nil, err
	}

	if _, loaded := r.tenants.LoadOrStore(tenantID, tb); loaded {
		_ = tb.Close()
		return nil, ErrTenantAlreadyExists
	}

	return tb, nil
}

// allocatePooledBackend picks a backend from the configured volume
// pool, opens a per-tenant Pebble at the resolved local path, and
// wraps it as a StorageBackend.
func (r *BarrierRegistry) allocatePooledBackend(ctx context.Context, tenantID string) (StorageBackend, error) {
	r.poolMu.RLock()
	pool := r.pool
	defaultPoolName := r.defaultPoolName
	perTenantReg := r.perTenantReg
	r.poolMu.RUnlock()

	if pool == nil {
		return nil, ErrPoolNotConfigured
	}
	if perTenantReg == nil {
		return nil, ErrPerTenantRegistryNotConfigured
	}

	picked, err := pool.Pick(ctx, defaultPoolName, objpool.Hint{TenantID: tenantID})
	if err != nil {
		return nil, err
	}

	accessor, ok := picked.(common.PathAccessor)
	if !ok {
		return nil, ErrPoolBackendNotLocal
	}

	tenantPath := filepath.Join(accessor.LocalPath(), tenantID)
	storage, err := perTenantReg.Open(ctx, tenantID, tenantPath)
	if err != nil {
		return nil, err
	}
	if r.logger != nil {
		r.logger.Info("barrier-mode tenant assigned to volume pool path",
			slog.String("tenant", tenantID),
			slog.String("path", tenantPath),
			slog.String("pool", defaultPoolName))
	}

	return NewStorageAdapter(storage), nil
}

// RegisterTenantDefault creates and registers a tenant barrier with
// default in-memory storage and the system barrier's strategies. This
// is a convenience shorthand for RegisterTenant with sensible
// defaults; ctx is taken from the background and the keyspace-mode
// path is used.
func (r *BarrierRegistry) RegisterTenantDefault(tenantID string) (*TenantBarrier, error) {
	base := NewMemoryBackend()
	config := tenantBarrierConfig(tenantID)
	return r.RegisterTenant(context.Background(), tenantID, base, config, r.systemStrategies()...)
}

// RegisterTenantPooled creates and registers a durable, barrier-isolated
// tenant. It mirrors RegisterTenantDefault but passes a nil base backend
// so RegisterTenant takes the pool-driven flow: a per-tenant path is
// pulled from the configured volume pool and a dedicated per-tenant Pebble
// is opened at that path (see WithVolumePool / WithPerTenantRegistry). The
// tenant inherits the system barrier's sealing strategies so its DEK is
// wrapped with the same posture as the system barrier.
//
// This is the registration path for IsolationBarrier tenants that do not
// specify their own strategy: their IAM data (and any other per-tenant
// state) is encrypted with the tenant's own DEK and stored under the
// tenant's "{tenantID}/" namespace in an independent backend, never in the
// shared system store. When the volume pool / per-tenant registry is not
// configured, RegisterTenant returns ErrPoolNotConfigured /
// ErrPerTenantRegistryNotConfigured.
//
// Inheritance of the system strategy is the DEFAULT. A tenant that needs
// its own sealing posture (e.g. TPM2 or Shamir) registers via
// RegisterTenantPooledWithStrategy instead.
func (r *BarrierRegistry) RegisterTenantPooled(ctx context.Context, tenantID string) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, tenantBarrierConfig(tenantID), r.systemStrategies()...)
}

// RegisterTenantPooledWithStrategy creates and registers a durable,
// barrier-isolated tenant using the SAME durable Pebble-pool backend as
// RegisterTenantPooled, but seals the tenant's DEK with the GIVEN strategies
// instead of inheriting the system barrier's posture.
//
// This is the registration path for IsolationBarrier tenants that supply
// their own sealing strategy (e.g. TPM2 or Shamir): the per-tenant path is
// pulled from the configured volume pool and a dedicated per-tenant Pebble
// is opened at that path (see WithVolumePool / WithPerTenantRegistry). When
// the volume pool / per-tenant registry is not configured, RegisterTenant
// returns ErrPoolNotConfigured / ErrPerTenantRegistryNotConfigured.
//
// At least one strategy must be supplied; callers that want to inherit the
// system posture use RegisterTenantPooled instead.
func (r *BarrierRegistry) RegisterTenantPooledWithStrategy(
	ctx context.Context,
	tenantID string,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	return r.RegisterTenant(ctx, tenantID, nil, tenantBarrierConfig(tenantID), strategies...)
}

// tenantBarrierConfig returns the canonical BarrierConfig for a per-tenant
// barrier. It is the single source of truth for the per-tenant root-key
// path so every registration variant derives it identically.
func tenantBarrierConfig(tenantID string) BarrierConfig {
	return BarrierConfig{
		RootKeyPath: "tenant/" + tenantID + "/root-key",
	}
}

// systemStrategies returns the system barrier's sealing strategies as a
// slice suitable for passing to RegisterTenant. It is the inheritance seam
// shared by RegisterTenantDefault and RegisterTenantPooled.
func (r *BarrierRegistry) systemStrategies() []SealingStrategy {
	strategies := r.system.Strategies()
	strategySlice := make([]SealingStrategy, 0, len(strategies))
	for _, st := range strategies {
		strategySlice = append(strategySlice, st)
	}
	return strategySlice
}

// GetTenant looks up a registered tenant barrier by ID. Returns
// ErrEmptyTenantID if tenantID is empty, or ErrTenantNotFound if the
// tenant is not registered.
func (r *BarrierRegistry) GetTenant(tenantID string) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	val, ok := r.tenants.Load(tenantID)
	if !ok {
		return nil, ErrTenantNotFound
	}
	return val.(*TenantBarrier), nil
}

// Tenant is a convenience alias for GetTenant.
func (r *BarrierRegistry) Tenant(tenantID string) (*TenantBarrier, error) {
	return r.GetTenant(tenantID)
}

// RemoveTenant seals and removes the tenant barrier for the given ID.
// Returns ErrTenantNotFound if the tenant is not registered.
func (r *BarrierRegistry) RemoveTenant(tenantID string) error {
	val, ok := r.tenants.LoadAndDelete(tenantID)
	if !ok {
		return ErrTenantNotFound
	}
	return val.(*TenantBarrier).Close()
}

// UnregisterTenant is a convenience alias for RemoveTenant.
func (r *BarrierRegistry) UnregisterTenant(tenantID string) error {
	return r.RemoveTenant(tenantID)
}

// InitializeTenant initializes the tenant barrier identified by tenantID.
// The tenant must already be registered via RegisterTenant.
func (r *BarrierRegistry) InitializeTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Initialize(ctx, creds)
}

// UnsealTenant unseals the tenant barrier identified by tenantID using the
// supplied credentials. The tenant must already be registered and
// initialized.
func (r *BarrierRegistry) UnsealTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Unseal(ctx, creds)
}

// SealTenant seals the tenant barrier identified by tenantID, releasing its
// in-memory key material. The tenant must already be registered.
func (r *BarrierRegistry) SealTenant(tenantID string) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Seal()
}

// TenantStatus returns the current status of the tenant barrier identified
// by tenantID. The tenant must already be registered.
func (r *BarrierRegistry) TenantStatus(tenantID string) (*BarrierStatus, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.Status(), nil
}

// ListTenants returns the IDs of all registered tenant barriers. The order
// is non-deterministic because the backing store is a sync.Map.
func (r *BarrierRegistry) ListTenants() []string {
	ids := make([]string, 0)
	r.tenants.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// SealAll seals every registered tenant barrier and then the system
// barrier. It accumulates errors so a single failing tenant does not
// prevent the remaining barriers from being sealed; the first error
// encountered is returned.
func (r *BarrierRegistry) SealAll() error {
	var errs []error
	r.tenants.Range(func(_, val any) bool {
		if err := val.(*TenantBarrier).Seal(); err != nil {
			errs = append(errs, err)
		}
		return true
	})
	if err := r.system.Seal(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Close seals and closes every registered tenant barrier and then closes
// the system barrier, releasing all underlying storage handles. It
// accumulates errors so a single failing barrier does not prevent the
// remaining barriers from being closed.
func (r *BarrierRegistry) Close() error {
	var errs []error
	r.tenants.Range(func(_, val any) bool {
		if err := val.(*TenantBarrier).Close(); err != nil {
			errs = append(errs, err)
		}
		return true
	})
	if err := r.system.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
