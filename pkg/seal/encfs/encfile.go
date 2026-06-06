// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// ErrWriteAtUnsupported is returned by WriteAt when the offset does
// not match the file's current logical end. The encfs V1 supports
// only append-style writes; Pebble does not perform mid-file rewrites
// in its standard I/O path. Should this restriction prove too tight
// the package can be extended with read-modify-write block handling.
var ErrWriteAtUnsupported = errors.New("encfs: WriteAt at non-append offset is not supported")

// ErrFileClosed is returned by file operations after Close.
var ErrFileClosed = errors.New("encfs: file is closed")

// ErrReadOnly is returned by write methods on a read-only file.
var ErrReadOnly = errors.New("encfs: file is read-only")

// encryptedFile implements vfs.File on top of an underlying base file
// holding encfs-formatted ciphertext. One instance corresponds to one
// open Pebble file handle. The struct is small enough to embed in the
// returned vfs.File interface value without additional indirection.
type encryptedFile struct {
	base       vfs.File
	crypto     *blockCrypto
	headerSize int64
	readOnly   bool

	mu          sync.Mutex
	closed      bool
	logicalSize int64  // total logical bytes (header excluded)
	flushedSize int64  // logical bytes whose content is on disk; <= logicalSize
	seqPos      int64  // logical position for sequential Read
	tail        []byte // bytes after flushedSize that may still grow before next full-block flush
}

// newWriteFile wraps a freshly-created base file in an encryptedFile
// configured for sequential writes. The base file MUST be empty; the
// header is written immediately.
func newWriteFile(base vfs.File, h *header) (*encryptedFile, error) {
	if _, err := base.Write(h.marshal()); err != nil {
		return nil, err
	}
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	return &encryptedFile{
		base:       base,
		crypto:     crypto,
		headerSize: int64(h.totalSize()),
	}, nil
}

// newReadFile wraps an existing base file in an encryptedFile
// configured for read-only access. The header has already been parsed
// and the base read cursor sits immediately past the header.
func newReadFile(base vfs.File, h *header, physicalSize int64) (*encryptedFile, error) {
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	logicalSize, err := physicalToLogicalSize(physicalSize - int64(h.totalSize()))
	if err != nil {
		return nil, err
	}
	return &encryptedFile{
		base:        base,
		crypto:      crypto,
		headerSize:  int64(h.totalSize()),
		readOnly:    true,
		logicalSize: logicalSize,
		flushedSize: logicalSize,
	}, nil
}

// physicalToLogicalSize maps the byte length of an encfs payload (the
// region after the header) to its plaintext length. Returns an error
// when the payload length is not a valid encfs framing.
func physicalToLogicalSize(payloadBytes int64) (int64, error) {
	if payloadBytes < 0 {
		return 0, fmt.Errorf("encfs: payload length is negative (%d)", payloadBytes)
	}
	if payloadBytes == 0 {
		return 0, nil
	}
	fullBlocks := payloadBytes / int64(CiphertextBlockSize)
	rem := payloadBytes % int64(CiphertextBlockSize)
	if rem == 0 {
		return fullBlocks * int64(BlockSize), nil
	}
	if rem < int64(BlockOverhead) {
		return 0, fmt.Errorf("encfs: trailing block too short (%d bytes)", rem)
	}
	return fullBlocks*int64(BlockSize) + (rem - int64(BlockOverhead)), nil
}

// physicalOffsetForBlock returns the byte offset within the underlying
// base file at which block `blockIndex` begins.
func (f *encryptedFile) physicalOffsetForBlock(blockIndex int64) int64 {
	return f.headerSize + blockIndex*int64(CiphertextBlockSize)
}

// Close releases the underlying file. If the file holds an
// in-memory tail with unflushed bytes the tail is encrypted and
// written before close. Close is idempotent.
func (f *encryptedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if !f.readOnly {
		if err := f.flushTailLocked(); err != nil {
			_ = f.base.Close()
			return err
		}
	}
	return f.base.Close()
}

// Read implements io.Reader against the file's logical sequential
// read cursor.
func (f *encryptedFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	pos := f.seqPos
	f.mu.Unlock()
	n, err := f.ReadAt(p, pos)
	if n > 0 {
		f.mu.Lock()
		f.seqPos += int64(n)
		f.mu.Unlock()
	}
	return n, err
}

// ReadAt implements io.ReaderAt by decrypting the encfs blocks that
// cover the requested byte range and returning the requested slice.
func (f *encryptedFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("encfs: negative offset")
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, ErrFileClosed
	}
	logicalSize := f.logicalSize
	flushedSize := f.flushedSize
	headerSize := f.headerSize
	tail := append([]byte(nil), f.tail...)
	f.mu.Unlock()

	if off >= logicalSize {
		return 0, io.EOF
	}
	want := len(p)
	if int64(want) > logicalSize-off {
		want = int(logicalSize - off)
	}
	end := off + int64(want)

	written := 0
	for cursor := off; cursor < end; {
		blockIndex := cursor / int64(BlockSize)
		blockStart := blockIndex * int64(BlockSize)
		intra := cursor - blockStart
		var plaintext []byte

		// Identify whether this block is on disk or in the in-memory
		// tail. The tail covers logical range [flushedSize, logicalSize).
		if blockStart >= flushedSize {
			tailIntra := blockStart - flushedSize
			if tailIntra >= int64(len(tail)) {
				return written, io.EOF
			}
			tailEnd := tailIntra + int64(BlockSize)
			if tailEnd > int64(len(tail)) {
				tailEnd = int64(len(tail))
			}
			plaintext = tail[tailIntra:tailEnd]
		} else {
			cipherLen := int64(CiphertextBlockSize)
			if (blockIndex+1)*int64(BlockSize) > flushedSize {
				// Trailing partial block already flushed once; its
				// on-disk size is the partial size + overhead.
				cipherLen = (flushedSize - blockStart) + int64(BlockOverhead)
			}
			cipherBuf := make([]byte, cipherLen)
			if _, err := f.base.ReadAt(cipherBuf, headerSize+blockIndex*int64(CiphertextBlockSize)); err != nil {
				return written, err
			}
			pt, err := f.crypto.decrypt(cipherBuf)
			if err != nil {
				return written, err
			}
			plaintext = pt
		}

		copyLen := int64(len(plaintext)) - intra
		if copyLen > end-cursor {
			copyLen = end - cursor
		}
		if copyLen <= 0 {
			break
		}
		copy(p[written:written+int(copyLen)], plaintext[intra:intra+copyLen])
		written += int(copyLen)
		cursor += copyLen
	}
	if written < len(p) {
		return written, io.EOF
	}
	return written, nil
}

// Write implements io.Writer by appending to the file. Plaintext is
// buffered in memory until full blocks accumulate; full blocks are
// encrypted and written to disk immediately. The trailing partial
// block remains in memory until Sync or Close.
func (f *encryptedFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrFileClosed
	}
	if f.readOnly {
		return 0, ErrReadOnly
	}
	return f.appendLocked(p)
}

// WriteAt is supported only at the file's current logical end; it
// then behaves as Write. Mid-file rewrites return ErrWriteAtUnsupported.
func (f *encryptedFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrFileClosed
	}
	if f.readOnly {
		return 0, ErrReadOnly
	}
	if off != f.logicalSize {
		return 0, ErrWriteAtUnsupported
	}
	return f.appendLocked(p)
}

// appendLocked appends p to the file. Caller must hold f.mu.
func (f *encryptedFile) appendLocked(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		room := BlockSize - len(f.tail)
		if room == 0 {
			if err := f.flushFullBlockLocked(); err != nil {
				return written, err
			}
			room = BlockSize - len(f.tail)
		}
		take := len(p)
		if take > room {
			take = room
		}
		f.tail = append(f.tail, p[:take]...)
		f.logicalSize += int64(take)
		written += take
		p = p[take:]
	}
	return written, nil
}

// flushFullBlockLocked writes f.tail (which is exactly BlockSize bytes
// long) as a full encrypted block to disk and resets f.tail. Caller
// must hold f.mu.
func (f *encryptedFile) flushFullBlockLocked() error {
	if len(f.tail) != BlockSize {
		return fmt.Errorf("encfs: flushFullBlock expected %d bytes, got %d", BlockSize, len(f.tail))
	}
	ct, err := f.crypto.encrypt(f.tail)
	if err != nil {
		return err
	}
	blockIndex := f.flushedSize / int64(BlockSize)
	if _, err := f.base.WriteAt(ct, f.physicalOffsetForBlock(blockIndex)); err != nil {
		return err
	}
	f.flushedSize += int64(BlockSize)
	f.tail = f.tail[:0]
	return nil
}

// flushTailLocked writes the in-memory tail as a (possibly partial)
// encrypted block to disk. Called from Sync and Close. Caller must
// hold f.mu.
func (f *encryptedFile) flushTailLocked() error {
	if len(f.tail) == 0 {
		return nil
	}
	ct, err := f.crypto.encrypt(f.tail)
	if err != nil {
		return err
	}
	blockIndex := f.flushedSize / int64(BlockSize)
	if _, err := f.base.WriteAt(ct, f.physicalOffsetForBlock(blockIndex)); err != nil {
		return err
	}
	// flushedSize is intentionally NOT advanced here: a subsequent
	// Write may extend the same trailing block, in which case we will
	// re-encrypt with a fresh inline nonce and overwrite the same disk
	// region.
	return nil
}

// Sync persists buffered data to non-volatile storage by encrypting
// and writing the trailing partial block (if any) and then forwarding
// to the underlying file's Sync.
func (f *encryptedFile) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrFileClosed
	}
	if f.readOnly {
		return f.base.Sync()
	}
	if err := f.flushTailLocked(); err != nil {
		return err
	}
	return f.base.Sync()
}

// SyncData behaves like Sync.
func (f *encryptedFile) SyncData() error { return f.Sync() }

// SyncTo persists at least the prefix of the file up to length
// bytes. The simplification used here syncs the entire file; that is
// always correct and matches the contract's allowance to upgrade to a
// full sync.
func (f *encryptedFile) SyncTo(length int64) (bool, error) {
	if err := f.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

// Stat returns a FileInfo whose Size reports the logical (plaintext)
// length of the file, hiding the encryption overhead from Pebble.
func (f *encryptedFile) Stat() (vfs.FileInfo, error) {
	fi, err := f.base.Stat()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	logicalSize := f.logicalSize
	f.mu.Unlock()
	return &encFileInfo{FileInfo: fi, logicalSize: logicalSize}, nil
}

// Preallocate is forwarded to the base file. The encryption overhead
// is small enough that Pebble's preallocation hint remains useful even
// without a translated offset.
func (f *encryptedFile) Preallocate(offset, length int64) error {
	return f.base.Preallocate(offset, length)
}

// Prefetch is forwarded to the base file with the encryption-overhead
// adjusted offset. Prefetch is a hint; over-fetching is harmless.
func (f *encryptedFile) Prefetch(offset, length int64) error {
	return f.base.Prefetch(offset+f.headerSize, length)
}

// Fd returns InvalidFd: encrypted files do not expose a raw OS file
// descriptor because direct OS access would bypass encryption.
func (f *encryptedFile) Fd() uintptr { return vfs.InvalidFd }

// encFileInfo wraps an underlying vfs.FileInfo and overrides Size to
// report the logical plaintext length. All other methods (Name,
// Mode, ModTime, IsDir, Sys, DeviceID) are promoted from the embedded
// base FileInfo via Go's method promotion rules.
type encFileInfo struct {
	vfs.FileInfo
	logicalSize int64
}

func (i *encFileInfo) Size() int64 { return i.logicalSize }
