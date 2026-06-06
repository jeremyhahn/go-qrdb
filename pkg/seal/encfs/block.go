// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// ErrAuthFailed is returned when AES-GCM authentication fails on a
// block read, indicating the block was modified or the wrong key was
// used.
var ErrAuthFailed = errors.New("encfs: block authentication failed")

// ErrBlockTooLarge is returned when an encrypt request supplies more
// plaintext than BlockSize bytes.
var ErrBlockTooLarge = errors.New("encfs: plaintext block exceeds BlockSize")

// ErrBlockTooShort is returned when a decrypt request supplies fewer
// bytes than BlockOverhead, or more bytes than a full ciphertext
// block.
var ErrBlockTooShort = errors.New("encfs: ciphertext block has invalid length")

// ErrDEKWrongLength is returned by newBlockCrypto when the supplied
// DEK is not exactly dekSize bytes.
var ErrDEKWrongLength = errors.New("encfs: dek must be 32 bytes")

// blockCrypto provides per-block AES-256-GCM encrypt/decrypt for one
// open file. Each instance is bound to a single DEK. Nonces are
// sampled fresh per encrypt call and serialized inline before the
// ciphertext so that rewriting a block (e.g. extending a partial
// trailing block on Sync) does not reuse a (key, nonce) pair.
type blockCrypto struct {
	aead cipher.AEAD
}

// newBlockCrypto constructs a blockCrypto from a DEK. The DEK must be
// exactly dekSize (32) bytes.
func newBlockCrypto(dek []byte) (*blockCrypto, error) {
	if len(dek) != dekSize {
		return nil, ErrDEKWrongLength
	}
	blk, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &blockCrypto{aead: aead}, nil
}

// encrypt encrypts plaintext into an inline-nonce ciphertext block:
//
//	[ nonce (12B) | ciphertext (len(plaintext)) | tag (16B) ]
//
// The nonce is sampled from crypto/rand. The output length is
// len(plaintext) + BlockOverhead.
func (c *blockCrypto) encrypt(plaintext []byte) ([]byte, error) {
	if len(plaintext) > BlockSize {
		return nil, ErrBlockTooLarge
	}
	out := make([]byte, inlineNonceSize, inlineNonceSize+len(plaintext)+tagSize)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return c.aead.Seal(out, out[:inlineNonceSize], plaintext, nil), nil
}

// decrypt decrypts an inline-nonce ciphertext block. Returns
// ErrAuthFailed if authentication fails (tampering or wrong key).
func (c *blockCrypto) decrypt(block []byte) ([]byte, error) {
	if len(block) < BlockOverhead || len(block) > CiphertextBlockSize {
		return nil, ErrBlockTooShort
	}
	nonce := block[:inlineNonceSize]
	body := block[inlineNonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return plaintext, nil
}
