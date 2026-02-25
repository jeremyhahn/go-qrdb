// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"

	"github.com/awnumar/memguard"
	"golang.org/x/crypto/hkdf"
)

const (
	// aesKeySize is the size of the AES-256 key in bytes.
	aesKeySize = 32

	// gcmNonceSize is the size of the AES-GCM nonce in bytes.
	gcmNonceSize = 12

	// hkdfInfo is the HKDF info string for DEK derivation.
	hkdfInfo = "qrdb/barrier/v1"

	// sealedRootKeyVersion is the format version for SealedRootKey blobs.
	sealedRootKeyVersion = 1

	// barrierVersion is the version byte prepended to encrypted values.
	// Wire format: [0x01][nonce:12][ciphertext+tag]
	barrierVersion byte = 0x01
)

// Compile-time interface check.
var _ SymmetricEncrypter = (*softwareBarrierEncryptor)(nil)

// softwareBarrierEncryptor implements SymmetricEncrypter using a
// software-derived DEK for barrier data operations. Used as fallback when
// no hardware encryptor is available.
type softwareBarrierEncryptor struct {
	dek []byte
}

// newSoftwareBarrierEncryptor derives a DEK from the root key using
// HKDF-SHA256 and returns a software encryptor that uses AES-256-GCM.
func newSoftwareBarrierEncryptor(rootKey []byte) (*softwareBarrierEncryptor, error) {
	dek, err := deriveDEK(rootKey)
	if err != nil {
		return nil, err
	}
	return &softwareBarrierEncryptor{dek: dek}, nil
}

// newSoftwareBarrierEncryptorFromDEK creates a software encryptor from a
// pre-derived DEK. The DEK is copied to prevent aliasing. Used by recovery
// operations where the DEK itself was split into Shamir shares.
func newSoftwareBarrierEncryptorFromDEK(dek []byte) *softwareBarrierEncryptor {
	copied := make([]byte, len(dek))
	copy(copied, dek)
	return &softwareBarrierEncryptor{dek: copied}
}

// Encrypt encrypts plaintext using AES-256-GCM with the derived DEK.
// Returns wire format: [nonce:12][ciphertext+tag].
func (e *softwareBarrierEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.dek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// GCM Seal appends ciphertext+tag after the nonce.
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return sealed, nil
}

// Decrypt decrypts ciphertext using AES-256-GCM with the derived DEK.
// Expects wire format: [nonce:12][ciphertext+tag].
func (e *softwareBarrierEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.dek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize+gcm.Overhead() {
		return nil, errors.New("qrdb/seal: ciphertext too short")
	}

	nonce := ciphertext[:nonceSize]
	ct := ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	return plaintext, nil
}

// DEK returns a copy of the derived data encryption key.
func (e *softwareBarrierEncryptor) DEK() []byte {
	out := make([]byte, len(e.dek))
	copy(out, e.dek)
	return out
}

// Zero zeros the DEK memory using memguard.
func (e *softwareBarrierEncryptor) Zero() {
	memguard.WipeBytes(e.dek)
}

// deriveDEK derives a 32-byte data encryption key from the root key
// using HKDF-SHA256.
func deriveDEK(rootKey []byte) ([]byte, error) {
	hkdfReader := hkdf.New(sha256.New, rootKey, nil, []byte(hkdfInfo))
	dek := make([]byte, aesKeySize)
	if _, err := io.ReadFull(hkdfReader, dek); err != nil {
		return nil, err
	}
	return dek, nil
}
