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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/crypto/aesgcm"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const tenantDEKPrefix = "__sys:tenant_keys/"

// Compile-time interface check.
var _ TenantKeyManager = (*tenantKeyManager)(nil)

// tenantKeyManager manages per-tenant data encryption keys using envelope
// encryption. Each tenant's DEK is wrapped by the system EncryptionBarrier
// and persisted in a KVStore. Plaintext DEKs are cached in *secure.Secret
// enclaves (encrypted at rest in memory) within a sync.Map for fast access.
type tenantKeyManager struct {
	barrier EncryptionBarrier
	store   kvstore.KVStore
	cache   sync.Map // map[string]*secure.Secret
}

// NewTenantKeyManager creates a new TenantKeyManager that uses the given
// EncryptionBarrier for envelope encryption and the KVStore for persisting
// wrapped DEKs.
func NewTenantKeyManager(barrier EncryptionBarrier, store kvstore.KVStore) (TenantKeyManager, error) {
	if barrier == nil {
		return nil, &InvalidConfigError{Field: "barrier", Reason: "must not be nil"}
	}
	if store == nil {
		return nil, &InvalidConfigError{Field: "store", Reason: "must not be nil"}
	}
	return &tenantKeyManager{
		barrier: barrier,
		store:   store,
	}, nil
}

// GetTenantDEK returns the plaintext DEK for the given tenant. If no DEK
// exists, one is generated, wrapped with the barrier, persisted to the
// KVStore, and cached.
func (t *tenantKeyManager) GetTenantDEK(ctx context.Context, tenantID string) ([]byte, error) {
	if tenantID == "" {
		return nil, &TenantDEKNotFoundError{TenantID: ""}
	}
	if t.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}

	// Check cache first.
	if cached, ok := t.cache.Load(tenantID); ok {
		sec := cached.(*secure.Secret)
		buf, err := sec.Open()
		if err != nil {
			return nil, &DEKDerivationError{TenantID: tenantID, Err: err}
		}
		out := make([]byte, buf.Size())
		copy(out, buf.Bytes())
		buf.Destroy()
		return out, nil
	}

	// Try to load wrapped DEK from the store.
	key := tenantDEKStoreKey(tenantID)
	wrapped, err := t.store.Get(ctx, key)
	if err != nil {
		// Key not found: generate a new DEK.
		return t.generateAndStoreDEK(ctx, tenantID)
	}
	if wrapped == nil {
		return t.generateAndStoreDEK(ctx, tenantID)
	}

	// Unwrap the DEK.
	dek, err := t.barrier.Decrypt(wrapped)
	if err != nil {
		return nil, &DEKDerivationError{TenantID: tenantID, Err: err}
	}

	// Cache the plaintext DEK and return a copy.
	out := make([]byte, len(dek))
	copy(out, dek)
	t.cacheDEK(tenantID, dek)
	return out, nil
}

// RotateTenantDEK generates a new DEK for the tenant, wraps and persists
// it, and updates the cache.
func (t *tenantKeyManager) RotateTenantDEK(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return &TenantDEKNotFoundError{TenantID: ""}
	}
	if t.barrier.IsSealed() {
		return &BarrierSealedError{}
	}

	// Generate a new DEK.
	dek, err := aesgcm.GenerateKey()
	if err != nil {
		return &DEKDerivationError{TenantID: tenantID, Err: err}
	}

	// Wrap and persist.
	if err := t.wrapAndStore(ctx, tenantID, dek); err != nil {
		secure.Wipe(dek)
		return err
	}

	// Update cache (cacheDEK wipes dek via NewSecret).
	t.clearCachedDEK(tenantID)
	t.cacheDEK(tenantID, dek)
	return nil
}

// DestroyTenantDEK deletes the tenant's wrapped DEK from the store and
// clears the cached plaintext DEK. This implements cryptographic shredding:
// once the wrapped DEK is gone, the tenant's data is unrecoverable.
func (t *tenantKeyManager) DestroyTenantDEK(ctx context.Context, tenantID string) error {
	if tenantID == "" {
		return &TenantDEKNotFoundError{TenantID: ""}
	}

	// Delete the wrapped DEK from the store.
	key := tenantDEKStoreKey(tenantID)
	if err := t.store.Delete(ctx, key); err != nil {
		return &DEKDerivationError{TenantID: tenantID, Err: err}
	}

	// Clear the cached plaintext DEK.
	t.clearCachedDEK(tenantID)
	return nil
}

// ImportTenantKey imports externally-provided key material as the tenant's
// DEK. The key material is wrapped by the barrier, persisted, and cached.
func (t *tenantKeyManager) ImportTenantKey(ctx context.Context, tenantID string, keyMaterial []byte) error {
	if tenantID == "" {
		return &TenantDEKNotFoundError{TenantID: ""}
	}
	if t.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	if len(keyMaterial) != 32 {
		return &InvalidShareError{Reason: "key material must be exactly 32 bytes"}
	}

	// Wrap and persist.
	if err := t.wrapAndStore(ctx, tenantID, keyMaterial); err != nil {
		return err
	}

	// Update cache. Make a copy so cacheDEK doesn't wipe caller's slice.
	keyCopy := make([]byte, len(keyMaterial))
	copy(keyCopy, keyMaterial)
	t.clearCachedDEK(tenantID)
	t.cacheDEK(tenantID, keyCopy)
	return nil
}

// ClearCache removes all cached plaintext DEKs from memory, destroying
// each Secret so its enclave is zeroed immediately rather than waiting
// for GC finalizers.
func (t *tenantKeyManager) ClearCache() {
	t.cache.Range(func(key, val any) bool {
		if sec, ok := val.(*secure.Secret); ok {
			sec.Destroy()
		}
		t.cache.Delete(key)
		return true
	})
}

// generateAndStoreDEK creates a new random DEK, wraps it, persists it,
// caches it, and returns a copy.
func (t *tenantKeyManager) generateAndStoreDEK(ctx context.Context, tenantID string) ([]byte, error) {
	dek, err := aesgcm.GenerateKey()
	if err != nil {
		return nil, &DEKDerivationError{TenantID: tenantID, Err: err}
	}

	if err := t.wrapAndStore(ctx, tenantID, dek); err != nil {
		secure.Wipe(dek)
		return nil, err
	}

	// Copy before cacheDEK wipes dek via NewSecret.
	out := make([]byte, len(dek))
	copy(out, dek)
	t.cacheDEK(tenantID, dek)
	return out, nil
}

// wrapAndStore wraps a DEK with the barrier and persists it in the KVStore.
func (t *tenantKeyManager) wrapAndStore(ctx context.Context, tenantID string, dek []byte) error {
	wrapped, err := t.barrier.Encrypt(dek)
	if err != nil {
		return &DEKDerivationError{TenantID: tenantID, Err: err}
	}

	key := tenantDEKStoreKey(tenantID)
	if err := t.store.Put(ctx, key, wrapped); err != nil {
		return &DEKDerivationError{TenantID: tenantID, Err: err}
	}
	return nil
}

// cacheDEK wraps the DEK in a *secure.Secret and stores it in the cache.
// The input dek slice is wiped by secure.NewSecret. Any pre-existing
// cached Secret for the same tenant is destroyed first to prevent
// enclave leaks.
func (t *tenantKeyManager) cacheDEK(tenantID string, dek []byte) {
	sec, err := secure.NewSecret(dek)
	if err != nil {
		// NewSecret only fails on empty input, which aesgcm.GenerateKey
		// never produces. Defensive wipe.
		secure.Wipe(dek)
		return
	}
	if prev, loaded := t.cache.Swap(tenantID, sec); loaded {
		if old, ok := prev.(*secure.Secret); ok {
			old.Destroy()
		}
	}
}

// clearCachedDEK removes a cached DEK and destroys its Secret.
func (t *tenantKeyManager) clearCachedDEK(tenantID string) {
	if prev, loaded := t.cache.LoadAndDelete(tenantID); loaded {
		if sec, ok := prev.(*secure.Secret); ok {
			sec.Destroy()
		}
	}
}

// tenantDEKStoreKey returns the KVStore key for a tenant's wrapped DEK.
func tenantDEKStoreKey(tenantID string) string {
	return tenantDEKPrefix + tenantID
}
