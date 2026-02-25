// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/awnumar/memguard"
	"github.com/jeremyhahn/go-qrdb/pkg/shamir"
)

const (
	rootTokenSize     = 32
	rootTokenHMACInfo = "go-qrdb/root-token/v1"
)

// RootToken represents a generated root token that proves knowledge of
// the master key through Shamir shares. The token is derived from the DEK
// using HMAC-SHA256 with a random nonce.
type RootToken struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// GenerateRootToken creates a new root token by proving knowledge of the
// master key through Shamir shares. The caller must provide enough share
// values to meet the Shamir threshold. The reconstructed secret is verified
// against the barrier's sealed root key, and a new token is derived from
// the DEK using HMAC-SHA256.
func (b *Barrier) GenerateRootToken(ctx context.Context, shareValues []string) (*RootToken, error) {
	if b.config.Shamir == nil {
		return nil, ErrShamirNotConfigured
	}

	threshold := b.config.Shamir.Threshold
	if len(shareValues) < threshold {
		return nil, ErrShamirQuorumIncomplete
	}

	// Build Share objects and combine to reconstruct the secret.
	shares := make([]*shamir.Share, len(shareValues))
	for i, val := range shareValues {
		shares[i] = &shamir.Share{
			Index:     i + 1,
			Threshold: threshold,
			Total:     b.config.Shamir.TotalShares,
			Value:     val,
		}
	}

	secret, err := shamir.Combine(shares)
	if err != nil {
		return nil, ErrShamirCombineFailed
	}
	defer memguard.WipeBytes(secret)

	// Verify the secret and derive the DEK.
	dek, err := b.verifySecretAndDeriveDEK(ctx, secret)
	if err != nil {
		return nil, err
	}
	defer memguard.WipeBytes(dek)

	// Generate a random nonce.
	nonce := make([]byte, rootTokenSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// HMAC-SHA256(DEK, nonce || info) to derive the token.
	mac := hmac.New(sha256.New, dek)
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

// verifySecretAndDeriveDEK verifies the reconstructed Shamir secret against
// the barrier's sealed root key and returns the data encryption key (DEK).
// It supports two modes:
//   - Direct mode: a ShamirStrategy is registered and the secret is the root
//     key itself.
//   - Credential mode: the secret is a password used to unseal the root key
//     via one of the barrier's registered strategies.
func (b *Barrier) verifySecretAndDeriveDEK(ctx context.Context, secret []byte) ([]byte, error) {
	exists, err := b.base.Exists(b.config.RootKeyPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotInitialized
	}

	if b.ShamirStrategy() != nil {
		return b.verifyDirectMode(secret)
	}
	return b.verifyCredentialMode(ctx, secret)
}

// verifyDirectMode verifies the secret when a ShamirStrategy is registered.
// The secret IS the root key. Verification is done by creating a software
// encryptor, performing a canary encrypt/decrypt round-trip, and then
// confirming the sealed root key was sealed with the Shamir strategy.
func (b *Barrier) verifyDirectMode(secret []byte) ([]byte, error) {
	// Create a software encryptor from the secret (as root key).
	enc, err := newSoftwareBarrierEncryptor(secret)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	// Canary round-trip to verify the key works.
	canary := []byte("root-token-canary")
	encrypted, err := enc.Encrypt(canary)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	decrypted, err := enc.Decrypt(encrypted)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	if !bytes.Equal(canary, decrypted) {
		return nil, ErrRootTokenVerificationFailed
	}

	// Verify the sealed root key blob uses the Shamir strategy.
	blob, err := b.base.Get(b.config.RootKeyPath)
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

	// Return a copy of the DEK.
	return enc.DEK(), nil
}

// verifyCredentialMode verifies the secret when no ShamirStrategy is
// registered. The secret is treated as a password credential. We load
// the sealed root key blob, find the matching strategy, unseal the root
// key, derive the DEK from the root key, and return it.
func (b *Barrier) verifyCredentialMode(ctx context.Context, secret []byte) ([]byte, error) {
	blob, err := b.base.Get(b.config.RootKeyPath)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	var sealed SealedRootKey
	if err := json.Unmarshal(blob, &sealed); err != nil {
		return nil, ErrRootTokenVerificationFailed
	}

	strategy, ok := b.strategies[sealed.Strategy]
	if !ok {
		return nil, ErrRootTokenVerificationFailed
	}

	rootKey, err := strategy.UnsealRootKey(ctx, &sealed, Credentials{Secret: string(secret)})
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	defer memguard.WipeBytes(rootKey)

	// Derive DEK from root key.
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		return nil, ErrRootTokenVerificationFailed
	}
	return enc.DEK(), nil
}
