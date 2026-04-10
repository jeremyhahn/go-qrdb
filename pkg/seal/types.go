// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import "context"

// SealMode identifies the sealing mode.
type SealMode int

const (
	// SealModeAuto selects the best available sealing mode automatically.
	SealModeAuto SealMode = iota

	// SealModeShamir uses Shamir secret sharing for seal/unseal operations.
	SealModeShamir
)

// EncryptionBarrier provides encryption/decryption using the master key.
// Unlike the full StorageBarrier struct, this interface exposes only the
// cryptographic operations needed by consumers.
type EncryptionBarrier interface {
	// Encrypt encrypts plaintext using the master key.
	Encrypt(plaintext []byte) ([]byte, error)

	// Decrypt decrypts ciphertext using the master key.
	Decrypt(ciphertext []byte) ([]byte, error)

	// IsSealed reports whether the barrier is in sealed state.
	IsSealed() bool
}

// TenantKeyManager manages per-tenant data encryption keys (DEKs) using
// envelope encryption. Each tenant's DEK is wrapped by the system barrier
// and cached in memory for fast access.
type TenantKeyManager interface {
	// GetTenantDEK returns the plaintext DEK for the given tenant. If no
	// DEK exists, one is generated, wrapped, and persisted.
	GetTenantDEK(ctx context.Context, tenantID string) ([]byte, error)

	// RotateTenantDEK generates a new DEK for the tenant, replaces the
	// persisted wrapped DEK, and updates the cache.
	RotateTenantDEK(ctx context.Context, tenantID string) error

	// DestroyTenantDEK deletes the tenant's wrapped DEK and clears the
	// cache entry. This implements cryptographic shredding.
	DestroyTenantDEK(ctx context.Context, tenantID string) error

	// ImportTenantKey imports externally-provided key material as the
	// tenant's DEK, wrapping and persisting it.
	ImportTenantKey(ctx context.Context, tenantID string, keyMaterial []byte) error

	// ClearCache removes all cached plaintext DEKs. Called on seal.
	ClearCache()
}
