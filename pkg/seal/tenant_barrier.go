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

// TenantBarrier wraps a *StorageBarrier with tenant-specific isolation.
// Each tenant gets its own DEK, root key path, and storage namespace.
// The underlying storage backend is prefixed with "{tenantID}/" so all
// keys are isolated per tenant.
type TenantBarrier struct {
	tenantID string
	barrier  *StorageBarrier
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

	barrier, err := NewStorageBarrier(logger.With(slog.String("tenant", tenantID)), nsBackend, config, strategies...)
	if err != nil {
		return nil, err
	}

	return &TenantBarrier{
		tenantID: tenantID,
		barrier:  barrier,
	}, nil
}

// WrapTenantBarrier wraps an existing *StorageBarrier as a TenantBarrier for
// the given tenant. It is used by the per-tenant encryption-migration commit:
// after the tenant's data is re-encrypted and the keyed barrier is reopened
// (StorageBarrier.ReopenKeyed), the result is wrapped and swapped into the
// registry via ReplaceTenantBarrier.
func WrapTenantBarrier(tenantID string, barrier *StorageBarrier) *TenantBarrier {
	return &TenantBarrier{tenantID: tenantID, barrier: barrier}
}

// TenantID returns the identifier of this tenant barrier.
func (t *TenantBarrier) TenantID() string { return t.tenantID }

// Barrier returns the underlying StorageBarrier instance for advanced
// operations such as Shamir quorum management or master key export.
func (t *TenantBarrier) Barrier() *StorageBarrier { return t.barrier }

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

// InitializeShamir initializes this tenant barrier using Shamir secret sharing
// and seals the generated M-of-N shares to the enrolled Security Officers via
// the CeremonyDistributor. No share bytes are returned; the ShamirInitParams
// reports only the threshold, total, and officer share count.
// The tenant barrier must have been registered with a ShamirStrategy and a
// populated ShamirConfig (see BarrierRegistry.RegisterTenantPooledShamir).
func (t *TenantBarrier) InitializeShamir(ctx context.Context, creds Credentials) (*ShamirInitParams, error) {
	return t.barrier.InitializeShamir(ctx, creds)
}

// UnsealWithShare submits a single Shamir share toward this tenant barrier's
// quorum threshold, automatically unsealing once enough shares are present. The
// accumulator is owned by the tenant's own StorageBarrier, so quorum state is
// isolated per tenant.
func (t *TenantBarrier) UnsealWithShare(ctx context.Context, shareValue string) (*QuorumProgress, error) {
	return t.barrier.UnsealWithShare(ctx, shareValue)
}

// Seal transitions the tenant barrier to sealed state and releases the
// encryptor. All subsequent storage operations will return ErrSealed until
// Unseal is called again.
func (t *TenantBarrier) Seal() error {
	return t.barrier.Seal()
}

// Get retrieves and decrypts the value for the given key from the tenant's
// encrypted storage.
func (t *TenantBarrier) Get(ctx context.Context, key string) ([]byte, error) {
	return t.barrier.Get(ctx, key)
}

// Put encrypts the value and stores it under the given key in the tenant's
// encrypted storage.
func (t *TenantBarrier) Put(ctx context.Context, key string, value []byte) error {
	return t.barrier.Put(ctx, key, value)
}

// PutIfAbsent encrypts the value and atomically stores it under the given key
// in the tenant's encrypted storage only if the key does not already exist.
func (t *TenantBarrier) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return t.barrier.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete removes key only when its stored value still equals
// expected, delegating to the underlying barrier.
func (t *TenantBarrier) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return t.barrier.CompareAndDelete(ctx, key, expected)
}

// CompareAndSwap replaces the key's value in the tenant's storage only when it
// still equals expected.
func (t *TenantBarrier) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return t.barrier.CompareAndSwap(ctx, key, expected, next)
}

// Delete removes the key from the tenant's storage.
func (t *TenantBarrier) Delete(ctx context.Context, key string) error {
	return t.barrier.Delete(ctx, key)
}

// List returns all keys matching the given prefix from the tenant's storage.
func (t *TenantBarrier) List(ctx context.Context, prefix string) ([]string, error) {
	return t.barrier.List(ctx, prefix)
}

// Scan iterates over all key-value pairs matching the given prefix from the
// tenant's storage, with each value decrypted, invoking fn for each entry.
func (t *TenantBarrier) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return t.barrier.Scan(ctx, prefix, fn)
}

// Exists checks if a key exists in the tenant's storage.
func (t *TenantBarrier) Exists(ctx context.Context, key string) (bool, error) {
	return t.barrier.Exists(ctx, key)
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
// with the given prefix.
func NewNamespacedBackend(prefix string, base StorageBackend) *NamespacedBackend {
	return &NamespacedBackend{prefix: prefix, base: base}
}

// Get retrieves the value for the namespaced key.
func (n *NamespacedBackend) Get(ctx context.Context, key string) ([]byte, error) {
	return n.base.Get(ctx, n.prefix+key)
}

// Put stores the value under the namespaced key.
func (n *NamespacedBackend) Put(ctx context.Context, key string, data []byte) error {
	return n.base.Put(ctx, n.prefix+key, data)
}

// PutIfAbsent atomically stores the value under the namespaced key only if the
// key does not already exist. Atomicity is delegated to the base backend.
func (n *NamespacedBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	return n.base.PutIfAbsent(ctx, n.prefix+key, data)
}

// CompareAndDelete removes the namespaced key only when its stored value
// still equals expected.
func (n *NamespacedBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	// n.base is typed StorageBackend, which guardedDeleteBackend's doc comment
	// (and storageBackendImpliesGuardedDelete in barrier_guarded_writes_test.go)
	// establishes it always satisfies, so !ok is unreachable here. Kept for
	// symmetry with the genuinely-fallible CompareAndSwap probe below
	// (go-qrdb#552).
	guarded, ok := n.base.(guardedDeleteBackend)
	if !ok {
		return false, ErrGuardedDeleteUnsupported
	}
	return guarded.CompareAndDelete(ctx, n.prefix+key, expected)
}

// CompareAndSwap replaces the namespaced key's value only when it still equals
// expected. A base offering no guarded swap is refused rather than degraded.
func (n *NamespacedBackend) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	guarded, ok := n.base.(guardedSwapBackend)
	if !ok {
		return false, ErrGuardedSwapUnsupported
	}
	return guarded.CompareAndSwap(ctx, n.prefix+key, expected, next)
}

// Delete removes the namespaced key from storage.
func (n *NamespacedBackend) Delete(ctx context.Context, key string) error {
	return n.base.Delete(ctx, n.prefix+key)
}

// List returns all keys matching the given prefix, with the namespace prefix
// stripped from returned keys.
func (n *NamespacedBackend) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := n.base.List(ctx, n.prefix+prefix)
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

// Scan iterates over all key-value pairs matching the given prefix, with the
// namespace prefix stripped from returned keys, invoking fn for each entry.
func (n *NamespacedBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return n.base.Scan(ctx, n.prefix+prefix, func(key string, value []byte) error {
		return fn(strings.TrimPrefix(key, n.prefix), value)
	})
}

// Exists reports whether the namespaced key exists in storage.
func (n *NamespacedBackend) Exists(ctx context.Context, key string) (bool, error) {
	return n.base.Exists(ctx, n.prefix+key)
}

// Close is a no-op. Closing the base backend is the caller's responsibility.
func (n *NamespacedBackend) Close() error {
	return nil
}
