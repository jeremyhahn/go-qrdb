// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package aesgcm provides AES-256-GCM authenticated encryption using the Go
// standard library. Ciphertext is formatted as [nonce (12 bytes)][ciphertext][tag (16 bytes)].
package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
)

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
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, &CipherError{Op: "create GCM", Err: err}
	}
	return gcm, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// The key must be exactly 32 bytes (AES-256).
// Returns: [nonce (12 bytes)][ciphertext][tag (16 bytes)]
func Encrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, &InvalidKeyLengthError{Expected: 32, Actual: len(key)}
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(randReader, nonce); err != nil {
		return nil, &CipherError{Op: "generate nonce", Err: err}
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
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
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, &CipherError{
			Op:  "decrypt",
			Err: &DataTooShortError{MinSize: nonceSize, ActualSize: len(ciphertext)},
		}
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
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
