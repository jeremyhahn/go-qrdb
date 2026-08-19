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
)

// aesGCMNonceSize is the length of the nonce that NewGCMWithRandomNonce
// prepends to every ciphertext. The AEAD reports NonceSize() == 0 because
// callers never supply a nonce, so the framing length is named here instead.
const aesGCMNonceSize = 12

// AESGCMOverhead is the total overhead added by AES-GCM encryption:
// 12-byte nonce prepended + 16-byte authentication tag appended.
const AESGCMOverhead = aesGCMNonceSize + 16

// AESGCMEncrypt encrypts plaintext with the given key using AES-256-GCM.
// The returned ciphertext is formatted as: nonce || ciphertext || tag.
//
// The AEAD is built with cipher.NewGCMWithRandomNonce because SP 800-38D
// requires the module to own nonce generation; cipher.NewGCM (arbitrary IVs)
// is rejected under GODEBUG=fips140=only. The construction prepends the
// 12-byte nonce it samples, which is the layout this function already
// produced, so previously wrapped DEKs stay unwrappable.
func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMEncrypt", Detail: "create cipher", Err: err}
	}

	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMEncrypt", Detail: "create GCM", Err: err}
	}

	// The nonce argument must be empty: the AEAD samples the nonce itself and
	// prepends it to the returned ciphertext.
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

// AESGCMDecrypt decrypts ciphertext that was encrypted with AESGCMEncrypt.
// Expects the format: nonce || ciphertext || tag.
func AESGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMDecrypt", Detail: "create cipher", Err: err}
	}

	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, &CryptoError{Op: "AESGCMDecrypt", Detail: "create GCM", Err: err}
	}

	if len(ciphertext) < aesGCMNonceSize {
		return nil, &CiphertextTooShortError{
			Op:     "AESGCMDecrypt",
			Detail: "ciphertext shorter than nonce",
		}
	}

	// The nonce argument must be empty: the AEAD reads the nonce back off the
	// front of ciphertext.
	plaintext, err := gcm.Open(nil, nil, ciphertext, nil)
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
