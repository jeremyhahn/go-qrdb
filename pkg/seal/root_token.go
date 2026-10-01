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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	rootTokenSize = 32
	// #nosec G101 -- HKDF/HMAC domain-separation label, not a credential.
	rootTokenHMACInfo = "go-qrdb/root-token/v1"
)

// RootToken represents a generated root token that proves knowledge of
// the master key through Shamir shares. The token is derived from the
// root key using HMAC-SHA256 with a random nonce.
type RootToken struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// GenerateRootToken creates a new root token by proving knowledge of the
// master key through Shamir shares. The caller must provide enough share
// values to meet the Shamir threshold. The reconstructed secret is verified
// against the barrier's sealed root key, and a new token is derived from
// the root key using HMAC-SHA256.
func (b *StorageBarrier) GenerateRootToken(ctx context.Context, shareValues []string) (*RootToken, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	threshold := b.config.Shamir.Threshold
	if len(shareValues) < threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	// Build protected shares and combine to reconstruct the secret. Each share
	// is one mlock'd allocation, released however this function unwinds.
	shares := make([]*shamir.ProtectedShare, 0, len(shareValues))
	defer func() {
		for _, share := range shares {
			share.Destroy()
		}
	}()
	for i, val := range shareValues {
		// NewProtectedShare wipes raw as it enters protected memory on success;
		// wipe unconditionally so a validation failure (which leaves it
		// untouched) does not strand a plaintext copy on the heap. The caller's
		// string cannot be wiped (go-quicraft#35).
		raw := []byte(val)
		share, err := shamir.NewProtectedShare(i+1, threshold, b.config.Shamir.TotalShares, raw)
		secure.Wipe(raw)
		if err != nil {
			return nil, ErrShamirCombineFailed
		}
		shares = append(shares, share)
	}

	secretEnclave, err := shamir.CombineProtected(shares)
	if err != nil {
		return nil, ErrShamirCombineFailed
	}
	defer secretEnclave.Destroy()

	// Copy the reconstructed secret into a protected buffer and verify OUTSIDE
	// the enclave borrow. verifySecretAndGetRootKey performs an AEAD unwrap that
	// allocates protected memory, which deadlocks if run inside WithBytes (see
	// copyEnclaveToBuffer in shamir.go). The buffer is destroyed and the returned
	// rootKey wiped as soon as the token is derived; rootKey never aliases the
	// buffer, since resolveDirectSecret copies in legacy mode and unwrapRootKey
	// returns a freshly decrypted buffer.
	secretBuf, err := copyEnclaveToBuffer(secretEnclave)
	if err != nil {
		return nil, ErrShamirCombineFailed
	}
	defer secretBuf.Destroy()

	rootKey, err := b.verifySecretAndGetRootKey(ctx, secretBuf.Bytes())
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(rootKey)

	// Generate a random nonce.
	nonce := make([]byte, rootTokenSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// HMAC-SHA256(rootKey, nonce || info) to derive the token.
	mac := hmac.New(sha256.New, rootKey)
	mac.Write(nonce)
	mac.Write([]byte(rootTokenHMACInfo))
	tokenBytes := mac.Sum(nil)

	token := &RootToken{
		Token:     hex.EncodeToString(tokenBytes),
		CreatedAt: time.Now(),
	}

	emitAudit(b.config.AuditLogger, "generate-root-token", "barrier",
		b.config.RootKeyPath, "allow", map[string]string{
			"shares_provided": itoa(len(shareValues)),
		})

	return token, nil
}

// verifySecretAndGetRootKey verifies the reconstructed Shamir secret against
// the barrier's sealed root key and returns a copy of the root key.
// It supports two modes:
//   - Direct mode: a ShamirStrategy is registered and the secret is the
//     unseal key that decrypts the wrapped root key (or, on legacy barriers
//     without a wrapped blob, the root key itself).
//   - Credential mode: the secret is a password used to unseal the root key
//     via one of the barrier's registered strategies.
func (b *StorageBarrier) verifySecretAndGetRootKey(ctx context.Context, secret []byte) ([]byte, error) {
	exists, err := b.base.Exists(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotInitialized
	}

	if b.ShamirStrategy() != nil {
		return b.verifyDirectMode(ctx, secret)
	}
	return b.verifyCredentialMode(ctx, secret)
}

// verifyDirectMode verifies the secret when a ShamirStrategy is registered.
// The secret is the unseal key (or, on legacy barriers without a wrapped
// root key blob, the root key itself). With a wrapped blob present the
// authenticated decrypt is real cryptographic verification: wrong or
// rekey-revoked shares fail the AEAD open. The sealed root key envelope is
// additionally confirmed to use the Shamir strategy.
func (b *StorageBarrier) verifyDirectMode(ctx context.Context, secret []byte) ([]byte, error) {
	// Verify the sealed root key blob uses the Shamir strategy.
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	if sealed.Strategy != StrategyShamir {
		return nil, ErrRootTokenVerificationFailed
	}

	// Resolve the secret to the root key (AEAD unwrap in unseal-key mode,
	// pass-through copy in legacy direct mode).
	rootKey, err := b.resolveDirectSecret(ctx, secret)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	return rootKey, nil
}

// verifyCredentialMode verifies the secret when no ShamirStrategy is
// registered. The secret is treated as a password credential. We load
// the sealed root key blob, find the matching strategy, unseal the root
// key, and return it.
func (b *StorageBarrier) verifyCredentialMode(ctx context.Context, secret []byte) ([]byte, error) {
	blob, err := b.base.Get(ctx, b.config.RootKeyPath)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	strategy, ok := b.strategyByID(sealed.Strategy)
	if !ok {
		return nil, ErrRootTokenVerificationFailed
	}

	// NewCredentials wipes secret; it is the caller's protected buffer,
	// destroyed on its own return, and nothing reads it after this.
	creds, err := NewCredentials(secret)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	defer creds.Secret.Destroy()

	rootKey, err := strategy.UnsealRootKey(ctx, &sealed, creds)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	return rootKey, nil
}
