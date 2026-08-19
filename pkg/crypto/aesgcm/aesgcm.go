// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package aesgcm provides AES-256-GCM authenticated encryption using the Go
// standard library. Ciphertext is formatted as [nonce (12 bytes)][ciphertext][tag (16 bytes)].
//
// The AEAD is constructed with cipher.NewGCMWithRandomNonce, which is the only
// GCM construction approved under GODEBUG=fips140=only: SP 800-38D requires the
// module itself to own nonce generation, so cipher.NewGCM (arbitrary IVs) is
// rejected. NewGCMWithRandomNonce samples a 12-byte nonce internally and
// prepends it to the ciphertext, which is byte-for-byte the layout this package
// has always produced -- existing ciphertext stays readable.
package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
)

// nonceSize is the length of the nonce that NewGCMWithRandomNonce prepends to
// every ciphertext. The AEAD reports NonceSize() == 0 because callers never
// supply a nonce, so the framing length is named here instead.
const nonceSize = 12

// randReader is the source of cryptographic randomness. It defaults to
// crypto/rand.Reader and may be replaced in tests to simulate read failures.
var randReader io.Reader = rand.Reader

// newGCM constructs a cipher.AEAD from an AES key. It defaults to the standard
// library implementation and may be replaced in tests to inject errors.
var newGCM = defaultNewGCM

func defaultNewGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &CipherError{Op: "create cipher", Err: err}
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, &CipherError{Op: "create GCM", Err: err}
	}
	return gcm, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// The key must be exactly 32 bytes (AES-256).
// Returns: [nonce (12 bytes)][ciphertext][tag (16 bytes)].
func Encrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, &InvalidKeyLengthError{Expected: 32, Actual: len(key)}
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	// The nonce argument must be empty: the AEAD samples the nonce itself and
	// prepends it to the returned ciphertext.
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
// The key must be exactly 32 bytes (AES-256).
func Decrypt(key, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, &InvalidKeyLengthError{Expected: 32, Actual: len(key)}
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < nonceSize {
		return nil, &CipherError{
			Op:  "decrypt",
			Err: &DataTooShortError{MinSize: nonceSize, ActualSize: len(ciphertext)},
		}
	}
	// The nonce argument must be empty: the AEAD reads the nonce back off the
	// front of ciphertext.
	plaintext, err := gcm.Open(nil, nil, ciphertext, nil)
	if err != nil {
		return nil, &DecryptionError{Err: err}
	}
	return plaintext, nil
}

// GenerateKey generates a cryptographically random 32-byte AES-256 key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(randReader, key); err != nil {
		return nil, &CipherError{Op: "generate key", Err: err}
	}
	return key, nil
}
