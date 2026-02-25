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
	"log/slog"
	"sync"
)

// BarrierRegistry manages the system barrier and per-tenant barriers.
// It provides a centralized registry for creating, initializing, and
// looking up tenant-specific barriers.
type BarrierRegistry struct {
	logger  *slog.Logger
	system  *Barrier
	tenants sync.Map // map[string]*TenantBarrier
}

// NewBarrierRegistry creates a new BarrierRegistry with the given system
// barrier. The system barrier must not be nil.
func NewBarrierRegistry(logger *slog.Logger, system *Barrier) (*BarrierRegistry, error) {
	if system == nil {
		return nil, ErrNilSystemBarrier
	}
	return &BarrierRegistry{
		logger: logger,
		system: system,
	}, nil
}

// SystemBarrier returns the system-level barrier.
func (r *BarrierRegistry) SystemBarrier() *Barrier { return r.system }

// RegisterTenant creates and registers a new tenant barrier with the given
// storage backend, config, and strategies. Returns ErrTenantAlreadyExists
// if the tenant ID is already registered.
func (r *BarrierRegistry) RegisterTenant(
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

// RemoveTenant seals and removes the tenant barrier for the given ID.
// Returns ErrTenantNotFound if the tenant is not registered.
func (r *BarrierRegistry) RemoveTenant(tenantID string) error {
	val, ok := r.tenants.LoadAndDelete(tenantID)
	if !ok {
		return ErrTenantNotFound
	}
	return val.(*TenantBarrier).Close()
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

// UnsealTenant unseals the tenant barrier identified by tenantID.
func (r *BarrierRegistry) UnsealTenant(ctx context.Context, tenantID string, creds Credentials) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Unseal(ctx, creds)
}

// SealTenant seals the tenant barrier identified by tenantID.
func (r *BarrierRegistry) SealTenant(tenantID string) error {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return err
	}
	return tb.Seal()
}

// TenantStatus returns the current status of the tenant barrier identified
// by tenantID.
func (r *BarrierRegistry) TenantStatus(tenantID string) (*BarrierStatus, error) {
	tb, err := r.GetTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return tb.Status(), nil
}

// ListTenants returns all registered tenant IDs.
func (r *BarrierRegistry) ListTenants() []string {
	var ids []string
	r.tenants.Range(func(key, value any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// SealAll seals the system barrier and all tenant barriers.
func (r *BarrierRegistry) SealAll() error {
	r.tenants.Range(func(key, value any) bool {
		_ = value.(*TenantBarrier).Seal()
		return true
	})
	return r.system.Seal()
}

// Close closes the system barrier and all tenant barriers.
func (r *BarrierRegistry) Close() error {
	r.tenants.Range(func(key, value any) bool {
		_ = value.(*TenantBarrier).Close()
		return true
	})
	return r.system.Close()
}
