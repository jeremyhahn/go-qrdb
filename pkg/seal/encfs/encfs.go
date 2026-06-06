// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package encfs provides an encrypting Pebble vfs.FS that transparently
// protects all on-disk file contents using a seal.StorageBarrier as the
// key-encryption-key. Every file written through the FS is structured
// as a small plaintext header carrying a barrier-wrapped per-file data
// encryption key (DEK), followed by a sequence of independently
// encrypted variable-size blocks.
//
// File format (all multi-byte integers are little-endian):
//
//	Offset  Size  Field
//	0       4     magic         = "QENC"
//	4       1     version       = 0x01
//	5       1     flags         (reserved, MUST be 0)
//	6       2     header_size   = 64 + len(wrapped_dek)
//	8       4     block_size    = 4096
//	12      4     wrapped_dek_size
//	16      48    reserved      (zero)
//	64      N     wrapped_dek   (sealed by barrier)
//	64+N    *     payload       (sequence of encrypted blocks)
//
// Each payload block is at most BlockSize plaintext bytes encrypted
// with AES-256-GCM. On disk a block is laid out as:
//
//	[ nonce (12B) | ciphertext (1..BlockSize bytes) | tag (16B) ]
//
// For interior blocks the plaintext is exactly BlockSize bytes. The
// trailing block may be shorter when the logical file size is not a
// multiple of BlockSize. Carrying the nonce inline (rather than
// deriving it from a per-file seed plus block index) lets a Sync
// flush a partial trailing block and a subsequent Write extend that
// same block on disk without reusing a nonce — the rewrite samples a
// fresh random nonce.
//
// When the barrier is sealed, file open and creation operations return
// errors wrapping seal.ErrSealed. Files that are already open at the
// time of sealing continue to operate against their cached unwrapped
// DEK; sealing is not retroactive on open file handles. Pebble's
// sstable cache typically holds open handles for hot files, which
// matches this behavior.
package encfs

const (
	// BlockSize is the per-block plaintext size in bytes. AES-256-GCM
	// adds a 12-byte nonce and 16-byte authentication tag inline,
	// making each on-disk block CiphertextBlockSize bytes long when
	// fully populated. 4096 was chosen to match Pebble's default
	// sstable block size, minimising read amplification.
	BlockSize = 4096

	// inlineNonceSize is the AES-GCM nonce length, written inline
	// before each block's ciphertext.
	inlineNonceSize = 12

	// tagSize is the AES-GCM authentication tag length in bytes.
	tagSize = 16

	// BlockOverhead is the fixed per-block on-disk overhead (inline
	// nonce + auth tag) in bytes.
	BlockOverhead = inlineNonceSize + tagSize

	// CiphertextBlockSize is the on-disk size of one fully-populated
	// encrypted block.
	CiphertextBlockSize = BlockSize + BlockOverhead

	// dekSize is the per-file data encryption key length in bytes.
	// AES-256 requires a 32-byte key.
	dekSize = 32
)

// barrierKEK is the subset of seal.StorageBarrier the encfs package
// depends on. Defined as an interface so unit tests can substitute a
// deterministic in-memory fake; production code passes a concrete
// *seal.StorageBarrier through the public New constructor.
type barrierKEK interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}
