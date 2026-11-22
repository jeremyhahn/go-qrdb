// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Object form. SealObject and OpenObject seal a whole object (for
// example an SST in an object store) in the encfs layout: the QENC
// header (version 1, flags 0, block size 4096, the barrier-wrapped
// per-object 32-byte DEK) followed by BlockSize plaintext chunks, each
// stored as [nonce 12 | ciphertext | tag 16]. Unlike an encfs file,
// every block's AEAD additional data binds it to the object:
//
//	"QOBJ" || uint32 LE len(objName) || objName || uint64 LE block index || final
//
// where final is one byte, 1 for the last block and 0 otherwise. Block
// reorder, truncation at a block boundary, appended blocks and a whole
// object moved to another name all fail authentication. An empty object
// is one zero-length final block, never zero blocks. A sealed object and
// an encfs file are distinct domains: neither opens as the other.

package encfs

import (
	"bytes"
	"encoding/binary"
)

const objectAADMagic = "QOBJ"

// objectAAD returns the additional data binding block index of objName.
func objectAAD(objName string, index uint64, final bool) []byte {
	aad := make([]byte, 0, len(objectAADMagic)+4+len(objName)+8+1)
	aad = append(aad, objectAADMagic...)
	aad = binary.LittleEndian.AppendUint32(aad, uint32(len(objName)))
	aad = append(aad, objName...)
	aad = binary.LittleEndian.AppendUint64(aad, index)
	if final {
		aad = append(aad, 1)
	} else {
		aad = append(aad, 0)
	}
	return aad
}

// SealObject seals plaintext as the object objName: a fresh DEK wrapped
// by barrier, then the chunked, name-bound blocks. Barrier errors (for
// example a wrapped seal.ErrSealed) are returned unchanged.
func SealObject(barrier barrierKEK, objName string, plaintext []byte) ([]byte, error) {
	h, err := newHeader(barrier)
	if err != nil {
		return nil, err
	}
	c, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	blocks := (len(plaintext) + BlockSize - 1) / BlockSize
	if blocks == 0 {
		blocks = 1
	}
	hdr := h.marshal()
	out := make([]byte, 0, len(hdr)+len(plaintext)+blocks*BlockOverhead)
	out = append(out, hdr...)
	for i := 0; i < blocks; i++ {
		start := i * BlockSize
		end := min(start+BlockSize, len(plaintext))
		aad := objectAAD(objName, uint64(i), i == blocks-1)
		out = c.aead.Seal(out, nil, plaintext[start:end], aad)
	}
	return out, nil
}

// OpenObject authenticates and decrypts sealed as the object objName.
// Input that is not a QENC header yields ErrInvalidHeader (or
// ErrTruncatedHeader when short); any block that fails authentication,
// a missing final block or data after it yields ErrAuthFailed; a
// payload length that cannot be a block sequence yields the typed
// error physicalToLogicalSize returns.
func OpenObject(barrier barrierKEK, objName string, sealed []byte) ([]byte, error) {
	if barrier == nil {
		return nil, ErrNilBarrier
	}
	h, err := readHeader(bytes.NewReader(sealed), barrier)
	if err != nil {
		return nil, err
	}
	if h.Flags != 0 {
		return nil, ErrInvalidHeader
	}
	payload := sealed[h.totalSize():]
	if len(payload) == 0 {
		return nil, ErrAuthFailed
	}
	logical, err := physicalToLogicalSize(int64(len(payload)))
	if err != nil {
		return nil, err
	}
	c, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	blocks := (len(payload) + CiphertextBlockSize - 1) / CiphertextBlockSize
	plain := make([]byte, 0, logical)
	for i := 0; i < blocks; i++ {
		start := i * CiphertextBlockSize
		end := min(start+CiphertextBlockSize, len(payload))
		aad := objectAAD(objName, uint64(i), i == blocks-1)
		plain, err = c.aead.Open(plain, nil, payload[start:end], aad)
		if err != nil {
			return nil, ErrAuthFailed
		}
	}
	return plain, nil
}

// ObjectLogicalSize returns the plaintext size of a sealed object from
// its first fixedHeaderSize (64) bytes and its stored size, without the
// barrier. The header is checked as FS.Stat checks it; the size is not
// authenticated (OpenObject does that).
func ObjectLogicalSize(fixedHeader []byte, physicalSize int64) (int64, error) {
	if len(fixedHeader) < fixedHeaderSize {
		return 0, ErrTruncatedHeader
	}
	if string(fixedHeader[offMagic:offMagic+lenMagic]) != headerMagic {
		return 0, ErrInvalidHeader
	}
	if fixedHeader[offVersion] != headerVersion {
		return 0, ErrUnsupportedVersion
	}
	headerSize := int64(binary.LittleEndian.Uint16(fixedHeader[offHeaderSize:]))
	if headerSize < fixedHeaderSize || headerSize > maxHeaderSize {
		return 0, ErrInvalidHeader
	}
	payload := physicalSize - headerSize
	if payload == 0 {
		// An object always carries a final block.
		return 0, ErrAuthFailed
	}
	return physicalToLogicalSize(payload)
}
