// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

const (
	headerMagic     = "QENC"
	headerVersion   = 1
	fixedHeaderSize = 64

	offMagic         = 0
	offVersion       = 4
	offFlags         = 5
	offHeaderSize    = 6
	offBlockSize     = 8
	offWrappedDEKLen = 12
	offReserved      = 16

	lenMagic    = 4
	lenReserved = 48

	maxWrappedDEKSize = 4096
	maxHeaderSize     = fixedHeaderSize + maxWrappedDEKSize
)

var (
	// ErrInvalidHeader is returned when a file does not begin with a
	// valid encrypted-file header (wrong magic, malformed length
	// fields, or inconsistent metadata).
	ErrInvalidHeader = errors.New("encfs: invalid header")

	// ErrTruncatedHeader is returned when the file ends before the
	// header has been fully read.
	ErrTruncatedHeader = errors.New("encfs: truncated header")

	// ErrUnsupportedVersion is returned when the header carries a
	// version number this build does not understand.
	ErrUnsupportedVersion = errors.New("encfs: unsupported header version")

	// ErrNilBarrier is returned by header construction or parsing when
	// the caller passes a nil barrier reference.
	ErrNilBarrier = errors.New("encfs: barrier is nil")

	// ErrCorruptDEK is returned when the unwrapped DEK is not exactly
	// dekSize bytes, indicating tampering or wrong-barrier mismatch.
	ErrCorruptDEK = errors.New("encfs: unwrapped DEK has unexpected length")
)

// header captures the parsed contents of an encrypted-file header,
// including the unwrapped DEK after a successful read. The marshaled
// representation matches the layout documented in encfs.go.
type header struct {
	Version    uint8
	Flags      uint8
	BlockSize  uint32
	WrappedDEK []byte
	DEK        [dekSize]byte
}

// newHeader generates a fresh per-file header by sampling a random DEK
// and wrapping it with the barrier. The returned header has DEK
// populated for immediate use by the caller.
func newHeader(barrier barrierKEK) (*header, error) {
	if barrier == nil {
		return nil, ErrNilBarrier
	}
	h := &header{
		Version:   headerVersion,
		Flags:     0,
		BlockSize: BlockSize,
	}
	if _, err := rand.Read(h.DEK[:]); err != nil {
		return nil, err
	}
	wrapped, err := barrier.Encrypt(h.DEK[:])
	if err != nil {
		return nil, err
	}
	if len(wrapped) == 0 || len(wrapped) > maxWrappedDEKSize {
		return nil, ErrInvalidHeader
	}
	h.WrappedDEK = wrapped
	return h, nil
}

// totalSize returns the on-disk size of the marshaled header in bytes.
func (h *header) totalSize() int {
	return fixedHeaderSize + len(h.WrappedDEK)
}

// marshal returns the on-disk representation of the header.
func (h *header) marshal() []byte {
	buf := make([]byte, h.totalSize())
	copy(buf[offMagic:offMagic+lenMagic], headerMagic)
	buf[offVersion] = h.Version
	buf[offFlags] = h.Flags
	binary.LittleEndian.PutUint16(buf[offHeaderSize:], uint16(h.totalSize()))
	binary.LittleEndian.PutUint32(buf[offBlockSize:], h.BlockSize)
	binary.LittleEndian.PutUint32(buf[offWrappedDEKLen:], uint32(len(h.WrappedDEK)))
	// reserved bytes [offReserved : offReserved+lenReserved] are
	// already zero from make.
	copy(buf[fixedHeaderSize:], h.WrappedDEK)
	return buf
}

// readHeader reads and unwraps a header from r. The barrier must be
// unsealed; if sealed, the wrapping seal.ErrSealed is returned via the
// barrier's Decrypt call.
func readHeader(r io.Reader, barrier barrierKEK) (*header, error) {
	if barrier == nil {
		return nil, ErrNilBarrier
	}
	var fixed [fixedHeaderSize]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrTruncatedHeader
		}
		return nil, err
	}
	if string(fixed[offMagic:offMagic+lenMagic]) != headerMagic {
		return nil, ErrInvalidHeader
	}
	if fixed[offVersion] != headerVersion {
		return nil, ErrUnsupportedVersion
	}
	headerSize := int(binary.LittleEndian.Uint16(fixed[offHeaderSize:]))
	if headerSize < fixedHeaderSize || headerSize > maxHeaderSize {
		return nil, ErrInvalidHeader
	}
	wrappedLen := int(binary.LittleEndian.Uint32(fixed[offWrappedDEKLen:]))
	if wrappedLen <= 0 || wrappedLen > maxWrappedDEKSize {
		return nil, ErrInvalidHeader
	}
	if fixedHeaderSize+wrappedLen != headerSize {
		return nil, ErrInvalidHeader
	}
	blockSize := binary.LittleEndian.Uint32(fixed[offBlockSize:])
	if blockSize != BlockSize {
		// Future versions may carry a different block size on disk;
		// sizing arithmetic in this build assumes the constant.
		return nil, ErrInvalidHeader
	}
	h := &header{
		Version:    fixed[offVersion],
		Flags:      fixed[offFlags],
		BlockSize:  blockSize,
		WrappedDEK: make([]byte, wrappedLen),
	}
	if _, err := io.ReadFull(r, h.WrappedDEK); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrTruncatedHeader
		}
		return nil, err
	}
	plaintext, err := barrier.Decrypt(h.WrappedDEK)
	if err != nil {
		return nil, err
	}
	if len(plaintext) != dekSize {
		return nil, ErrCorruptDEK
	}
	copy(h.DEK[:], plaintext)
	return h, nil
}
