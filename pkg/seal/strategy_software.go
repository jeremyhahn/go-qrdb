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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
	"time"

	"github.com/awnumar/memguard"
	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for password-based key derivation.
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 32
)

// Compile-time interface check.
var _ SealingStrategy = (*SoftwareStrategy)(nil)

// SoftwareStrategy provides password-based root key protection using
// Argon2id for key derivation and AES-256-GCM for encryption. This is
// a software-only fallback when no hardware-backed strategy is available.
type SoftwareStrategy struct{}

// NewSoftwareStrategy returns a new SoftwareStrategy.
func NewSoftwareStrategy() *SoftwareStrategy {
	return &SoftwareStrategy{}
}

// ID returns StrategySoftware.
func (s *SoftwareStrategy) ID() StrategyID {
	return StrategySoftware
}

// Available always returns true. Software-based sealing has no external
// hardware dependency.
func (s *SoftwareStrategy) Available() bool {
	return true
}

// HardwareBacked returns false. This is a software-only strategy.
func (s *SoftwareStrategy) HardwareBacked() bool {
	return false
}

// SealRootKey encrypts the root key using a password-derived key.
//
// Process:
//  1. Generate 32-byte random salt
//  2. Derive key from password via Argon2id (time=3, memory=64KB, threads=4, keyLen=32)
//  3. Generate 12-byte random nonce
//  4. Encrypt root key via AES-256-GCM(derived key, nonce, rootKey)
//  5. Return SealedRootKey with salt, nonce, KDF version, and ciphertext
func (s *SoftwareStrategy) SealRootKey(
	ctx context.Context,
	rootKey []byte,
	creds Credentials,
) (*SealedRootKey, error) {
	// Generate salt.
	salt := make([]byte, argon2SaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	// Derive key from password.
	derivedKey := argon2.IDKey(
		[]byte(creds.Secret), salt,
		argon2Time, argon2Memory, argon2Threads, argon2KeyLen,
	)
	defer memguard.WipeBytes(derivedKey)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Generate nonce.
	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nil, nonce, rootKey, nil)

	return &SealedRootKey{
		Version:    sealedRootKeyVersion,
		Strategy:   StrategySoftware,
		KDFVersion: 1,
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		CreatedAt:  time.Now(),
	}, nil
}

// UnsealRootKey decrypts the root key using the password and stored salt.
// The KDF version stored in the sealed data is used to select the correct
// derivation algorithm, ensuring backward compatibility with older seals.
// KDFVersion 0 or 1 both use Argon2id.
func (s *SoftwareStrategy) UnsealRootKey(
	ctx context.Context,
	sealed *SealedRootKey,
	creds Credentials,
) ([]byte, error) {
	if sealed == nil {
		return nil, ErrNilSealedData
	}

	// Derive key from password + stored salt.
	derivedKey := argon2.IDKey(
		[]byte(creds.Secret), sealed.Salt,
		argon2Time, argon2Memory, argon2Threads, argon2KeyLen,
	)
	defer memguard.WipeBytes(derivedKey)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := gcm.Open(nil, sealed.Nonce, sealed.Ciphertext, nil)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	return plaintext, nil
}

// BarrierEncryptor returns a software SymmetricEncrypter for barrier data
// operations. The DEK is derived from the root key using HKDF-SHA256.
func (s *SoftwareStrategy) BarrierEncryptor(
	ctx context.Context,
	rootKey []byte,
) (SymmetricEncrypter, error) {
	return newSoftwareBarrierEncryptor(rootKey)
}
