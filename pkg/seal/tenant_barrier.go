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
	"strings"
)

// TenantBarrier wraps a *Barrier with tenant-specific isolation.
// Each tenant gets its own DEK, root key path, and storage namespace.
// The underlying storage backend is prefixed with "{tenantID}/" so all
// keys are isolated per tenant.
type TenantBarrier struct {
	tenantID string
	barrier  *Barrier
}

// NewTenantBarrier creates a new tenant-specific barrier with namespace isolation.
// The base storage backend is wrapped with a prefix of "{tenantID}/" so all
// keys are isolated per tenant.
func NewTenantBarrier(
	tenantID string,
	logger *slog.Logger,
	base StorageBackend,
	config BarrierConfig,
	strategies ...SealingStrategy,
) (*TenantBarrier, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if base == nil {
		return nil, ErrNilStorageBackend
	}

	// Namespace-isolate storage for this tenant.
	nsBackend := NewNamespacedBackend(tenantID+"/", base)

	// Override root key path for tenant isolation.
	if config.RootKeyPath == "" {
		config.RootKeyPath = "tenant/" + tenantID + "/root-key"
	}

	barrier, err := NewBarrier(logger.With(slog.String("tenant", tenantID)), nsBackend, config, strategies...)
	if err != nil {
		return nil, err
	}

	return &TenantBarrier{
		tenantID: tenantID,
		barrier:  barrier,
	}, nil
}

// TenantID returns the identifier of this tenant barrier.
func (t *TenantBarrier) TenantID() string { return t.tenantID }

// Barrier returns the underlying Barrier instance for advanced operations
// such as Shamir quorum management or master key export.
func (t *TenantBarrier) Barrier() *Barrier { return t.barrier }

// IsSealed reports whether the tenant barrier is in sealed state.
func (t *TenantBarrier) IsSealed() bool { return t.barrier.IsSealed() }

// Status returns the current tenant barrier status.
func (t *TenantBarrier) Status() *BarrierStatus { return t.barrier.Status() }

// Initialize generates a new root key for this tenant, seals it with the best
// available strategy, and transitions the tenant barrier to the unsealed state.
func (t *TenantBarrier) Initialize(ctx context.Context, creds Credentials) error {
	return t.barrier.Initialize(ctx, creds)
}

// Unseal loads the tenant's sealed root key blob, unseals it with the matching
// strategy, and transitions the tenant barrier to the unsealed state.
func (t *TenantBarrier) Unseal(ctx context.Context, creds Credentials) error {
	return t.barrier.Unseal(ctx, creds)
}

// Seal transitions the tenant barrier to sealed state and releases the
// encryptor. All subsequent storage operations will return ErrSealed until
// Unseal is called again.
func (t *TenantBarrier) Seal() error {
	return t.barrier.Seal()
}

// Get retrieves and decrypts the value for the given key from the tenant's
// encrypted storage.
func (t *TenantBarrier) Get(key string) ([]byte, error) {
	return t.barrier.Get(key)
}

// Put encrypts the value and stores it under the given key in the tenant's
// encrypted storage.
func (t *TenantBarrier) Put(key string, value []byte) error {
	return t.barrier.Put(key, value)
}

// Delete removes the key from the tenant's storage.
func (t *TenantBarrier) Delete(key string) error {
	return t.barrier.Delete(key)
}

// List returns all keys matching the given prefix from the tenant's storage.
func (t *TenantBarrier) List(prefix string) ([]string, error) {
	return t.barrier.List(prefix)
}

// Exists checks if a key exists in the tenant's storage.
func (t *TenantBarrier) Exists(key string) (bool, error) {
	return t.barrier.Exists(key)
}

// Close seals the tenant barrier and closes the underlying storage backend.
func (t *TenantBarrier) Close() error {
	return t.barrier.Close()
}

// NamespacedBackend wraps a StorageBackend and prefixes all keys with a namespace.
// This provides storage isolation between tenants sharing the same underlying
// storage backend.
type NamespacedBackend struct {
	prefix string
	base   StorageBackend
}

// Compile-time interface check.
var _ StorageBackend = (*NamespacedBackend)(nil)

// NewNamespacedBackend creates a new NamespacedBackend that prefixes all keys
// with the given prefix string.
func NewNamespacedBackend(prefix string, base StorageBackend) *NamespacedBackend {
	return &NamespacedBackend{prefix: prefix, base: base}
}

// Get retrieves the value for the namespaced key.
func (n *NamespacedBackend) Get(key string) ([]byte, error) {
	return n.base.Get(n.prefix + key)
}

// Put stores the value under the namespaced key.
func (n *NamespacedBackend) Put(key string, data []byte) error {
	return n.base.Put(n.prefix+key, data)
}

// Delete removes the namespaced key from storage.
func (n *NamespacedBackend) Delete(key string) error {
	return n.base.Delete(n.prefix + key)
}

// List returns all keys matching the given prefix, with the namespace prefix
// stripped from returned keys.
func (n *NamespacedBackend) List(prefix string) ([]string, error) {
	keys, err := n.base.List(n.prefix + prefix)
	if err != nil {
		return nil, err
	}
	// Strip namespace prefix from returned keys.
	stripped := make([]string, len(keys))
	for i, k := range keys {
		stripped[i] = strings.TrimPrefix(k, n.prefix)
	}
	return stripped, nil
}

// Exists reports whether the namespaced key exists in storage.
func (n *NamespacedBackend) Exists(key string) (bool, error) {
	return n.base.Exists(n.prefix + key)
}

// Close is a no-op. Closing the base backend is the caller's responsibility.
func (n *NamespacedBackend) Close() error {
	return nil
}
