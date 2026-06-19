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
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
)

// Unseal-key indirection (Vault-style).
//
// In Shamir direct mode the secret split to the Security Officers is NOT the
// barrier root key itself. Instead a random 256-bit unseal key (UK) is
// generated, the root key is AEAD-wrapped under the UK, and the wrapped blob
// is persisted as a sibling of the sealed root key envelope. The UK — not the
// root key — is what gets split into M-of-N shares and sealed to the
// officers' ceremony enclaves.
//
// This indirection exists so that Rekey can cryptographically revoke every
// previously distributed share, including shares already claimed by
// officers: rekeying generates a fresh UK and re-wraps the root key under
// it, after which shares from any prior round reconstruct a UK that no
// longer decrypts the wrapped blob. The root key itself never rotates (the
// EpochBarrier derives per-epoch DEKs from it via a KDF, so rotating it
// would orphan every epoch), and no stored data is re-encrypted.
//
// The authenticated decrypt of the wrapped blob also provides the
// out-of-band secret validation that shamir.CombineUnverified requires
// when shares carry no integrity digest: garbage or stale shares produce a UK whose
// AEAD open fails, surfacing ErrUnsealKeyMismatch instead of silently
// unsealing with a wrong key.
//
// Barriers initialized before unseal-key indirection have no wrapped blob;
// for those the reconstructed secret IS the root key (legacy direct mode)
// and unsealing behaves exactly as before. The first Rekey upgrades a
// legacy barrier in place by writing its first wrapped blob.

const (
	// wrappedRootKeyPathSuffix is appended to the configured RootKeyPath to
	// form the storage key for the AEAD-wrapped root key blob. Like
	// metadataPathSuffix it must be a SIBLING of the root key (a "." suffix,
	// never a "/" child) so filesystem-backed KV stores can represent it.
	wrappedRootKeyPathSuffix = ".wrapped"

	// wrappedRootKeyVersion is the current wrapped-blob format version.
	wrappedRootKeyVersion = 1

	// wrappedRootKeyAADPrefix is the associated-data domain separator for the
	// root-key wrap. The barrier's RootKeyPath is appended so a wrapped blob
	// cannot be transplanted between barriers (e.g. across tenant paths).
	wrappedRootKeyAADPrefix = "go-qrdb/seal/wrapped-root-key/v1|"

	// unsealKeySize is the size in bytes of a generated unseal key
	// (AES-256-GCM key size).
	unsealKeySize = 32
)

// wrappedRootKey is the persisted JSON envelope holding the barrier root key
// AEAD-encrypted under the current unseal key. It contains no plaintext key
// material.
type wrappedRootKey struct {
	Version    int    `json:"version"`
	Algorithm  byte   `json:"algorithm"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// wrappedRootKeyPath returns the storage key for the wrapped root key blob.
func (b *StorageBarrier) wrappedRootKeyPath() string {
	return b.config.RootKeyPath + wrappedRootKeyPathSuffix
}

// wrappedRootKeyAAD returns the associated data binding a wrapped blob to
// this barrier's root key path.
func (b *StorageBarrier) wrappedRootKeyAAD() []byte {
	return []byte(wrappedRootKeyAADPrefix + b.config.RootKeyPath)
}

// unsealKeyAEAD constructs the AEAD used to wrap and unwrap the root key
// under an unseal key, honoring an injected AEADProvider when configured.
func (b *StorageBarrier) unsealKeyAEAD(unsealKey []byte) (cipher.AEAD, error) {
	provider := b.config.AEADProvider
	if provider == nil {
		provider = NewSoftwareAEADProvider()
	}
	return provider.NewAEAD(AlgAES256GCM, unsealKey)
}

// generateUnsealKey returns a fresh random 256-bit unseal key.
func generateUnsealKey() ([]byte, error) {
	key := make([]byte, unsealKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// wrapRootKey AEAD-encrypts rootKey under unsealKey and returns the
// serialized wrapped-blob bytes ready for persistence. Neither input slice
// is wiped; the caller owns both.
func (b *StorageBarrier) wrapRootKey(unsealKey, rootKey []byte) ([]byte, error) {
	aead, err := b.unsealKeyAEAD(unsealKey)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	wrapped := wrappedRootKey{
		Version:    wrappedRootKeyVersion,
		Algorithm:  byte(AlgAES256GCM),
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, rootKey, b.wrappedRootKeyAAD()),
	}
	return json.Marshal(&wrapped)
}

// resolveDirectSecret converts the secret reconstructed from a Shamir share
// quorum into the barrier root key.
//
// When a wrapped root key blob exists, the secret is the current unseal key:
// the blob is loaded and the root key is recovered via authenticated
// decrypt. A failed decrypt means the shares are wrong or were revoked by a
// prior rekey (ErrUnsealKeyMismatch). When no blob exists (legacy direct
// mode), the secret IS the root key and a copy is returned unchanged.
//
// The returned slice is a fresh allocation in both modes; the caller must
// wipe it after use.
func (b *StorageBarrier) resolveDirectSecret(ctx context.Context, secret []byte) ([]byte, error) {
	exists, err := b.base.Exists(ctx, b.wrappedRootKeyPath())
	if err != nil {
		return nil, err
	}
	if !exists {
		// Legacy direct mode: the reconstructed secret is the root key.
		rootKey := make([]byte, len(secret))
		copy(rootKey, secret)
		return rootKey, nil
	}

	blob, err := b.base.Get(ctx, b.wrappedRootKeyPath())
	if err != nil {
		return nil, err
	}

	var wrapped wrappedRootKey
	if err := json.Unmarshal(blob, &wrapped); err != nil {
		return nil, ErrWrappedRootKeyCorrupt
	}
	if wrapped.Version != wrappedRootKeyVersion ||
		SymmetricAlgorithm(wrapped.Algorithm) != AlgAES256GCM {
		return nil, ErrWrappedRootKeyCorrupt
	}

	aead, err := b.unsealKeyAEAD(secret)
	if err != nil {
		// Wrong-length secret (e.g. a legacy root-key share set submitted
		// against an upgraded barrier) cannot be a valid unseal key.
		return nil, ErrUnsealKeyMismatch
	}
	if len(wrapped.Nonce) != aead.NonceSize() {
		return nil, ErrWrappedRootKeyCorrupt
	}

	rootKey, err := aead.Open(nil, wrapped.Nonce, wrapped.Ciphertext, b.wrappedRootKeyAAD())
	if err != nil {
		return nil, ErrUnsealKeyMismatch
	}
	return rootKey, nil
}
