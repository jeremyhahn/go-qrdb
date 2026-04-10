// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"
)

// AESGCMOverhead is the total overhead added by AES-GCM encryption:
// 12-byte nonce prepended + 16-byte authentication tag appended.
const AESGCMOverhead = 12 + 16

// AESGCMEncrypt encrypts plaintext with the given key using AES-256-GCM.
// The returned ciphertext is formatted as: nonce || ciphertext || tag.
func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMEncrypt", Detail: "create cipher", Err: err}
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMEncrypt", Detail: "create GCM", Err: err}
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, &CryptoError{Op: "AESGCMEncrypt", Detail: "generate nonce", Err: err}
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// AESGCMDecrypt decrypts ciphertext that was encrypted with AESGCMEncrypt.
// Expects the format: nonce || ciphertext || tag.
func AESGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMDecrypt", Detail: "create cipher", Err: err}
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMDecrypt", Detail: "create GCM", Err: err}
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, &CiphertextTooShortError{
			Op:     "AESGCMDecrypt",
			Detail: "ciphertext shorter than nonce",
		}
	}

	plaintext, err := gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMDecrypt", Detail: "decrypt", Err: err}
	}

	return plaintext, nil
}

// SecureMemZero zeroes a byte slice to minimize key material exposure in memory.
func SecureMemZero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
