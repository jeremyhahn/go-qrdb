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
	"encoding/binary"
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
//
// The AEAD is cipher.NewGCMWithRandomNonce: SP 800-38D requires the
// module to own nonce generation, so cipher.NewGCM (arbitrary IVs) is
// rejected under GODEBUG=fips140=only. It samples the 12-byte nonce
// itself and prepends it, which is exactly the inline-nonce layout
// this type already wrote -- existing encrypted files stay readable.
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
	aead, err := cipher.NewGCMWithRandomNonce(blk)
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
// len(plaintext) + BlockOverhead. It is the encfs file form: encryptAAD
// with no additional data.
func (c *blockCrypto) encrypt(plaintext []byte) ([]byte, error) {
	return c.encryptAAD(plaintext, nil)
}

// encryptAAD encrypts plaintext into an inline-nonce ciphertext block as
// encrypt does, binding aad as the GCM additional data; a nil aad is the
// encfs file form. It is the one Seal call site of blockCrypto.
func (c *blockCrypto) encryptAAD(plaintext, aad []byte) ([]byte, error) {
	if len(plaintext) > BlockSize {
		return nil, ErrBlockTooLarge
	}
	// The nonce argument must be empty: the AEAD samples the nonce
	// itself and prepends it to the returned block.
	out := make([]byte, 0, inlineNonceSize+len(plaintext)+tagSize)
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return c.aead.Seal(out, nil, plaintext, aad), nil
}

// decrypt decrypts an inline-nonce ciphertext block. Returns
// ErrAuthFailed if authentication fails (tampering or wrong key). It is
// the encfs file form: decryptAAD with no additional data.
func (c *blockCrypto) decrypt(block []byte) ([]byte, error) {
	return c.decryptAAD(block, nil)
}

// decryptAAD decrypts an inline-nonce ciphertext block as decrypt does,
// authenticating aad as the GCM additional data; a nil aad is the encfs
// file form. It is the one Open call site of blockCrypto.
func (c *blockCrypto) decryptAAD(block, aad []byte) ([]byte, error) {
	if len(block) < BlockOverhead || len(block) > CiphertextBlockSize {
		return nil, ErrBlockTooShort
	}
	// The nonce argument must be empty: the AEAD reads the inline
	// nonce back off the front of the block.
	plaintext, err := c.aead.Open(nil, nil, block, aad)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return plaintext, nil
}

// frameLenSize is the length of the clear plaintext-length prefix of a
// framed block (see the framed layout in encfile.go).
const frameLenSize = 4

// frameAAD is the additional data a framed block is sealed with: its index
// in the file and its plaintext length, so frames cannot be reordered,
// spliced or have their length prefix rewritten.
func frameAAD(index int64, n int) []byte {
	var aad [12]byte
	binary.LittleEndian.PutUint64(aad[:8], uint64(index)) // #nosec G115 -- index is a non-negative frame count
	binary.LittleEndian.PutUint32(aad[8:], uint32(n))     // #nosec G115 -- n is at most BlockSize
	return aad[:]
}

// sealFrame returns the framed block for plaintext at position index: a
// little-endian uint32 plaintext length followed by the AEAD output
// (inline nonce, ciphertext, tag) over plaintext with frameAAD as
// additional data.
func (c *blockCrypto) sealFrame(plaintext []byte, index int64) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > BlockSize {
		return nil, ErrBlockTooLarge
	}
	out := make([]byte, frameLenSize, frameLenSize+inlineNonceSize+len(plaintext)+tagSize)
	binary.LittleEndian.PutUint32(out, uint32(len(plaintext))) // #nosec G115 -- at most BlockSize
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return c.aead.Seal(out, nil, plaintext, frameAAD(index, len(plaintext))), nil
}

// openFrame authenticates and decrypts the AEAD part of the framed block
// at position index whose length prefix read n.
func (c *blockCrypto) openFrame(sealed []byte, index int64, n int) ([]byte, error) {
	if n <= 0 || n > BlockSize || len(sealed) != n+BlockOverhead {
		return nil, ErrBlockTooShort
	}
	plaintext, err := c.aead.Open(nil, nil, sealed, frameAAD(index, n))
	if err != nil {
		return nil, ErrAuthFailed
	}
	return plaintext, nil
}
