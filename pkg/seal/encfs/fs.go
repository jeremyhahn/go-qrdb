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
	"io"
	"os"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// FS is a Pebble vfs.FS implementation that transparently encrypts
// file contents using a seal.StorageBarrier as the key-encryption-key.
// Wraps a base FS (typically vfs.Default).
type FS struct {
	base    vfs.FS
	barrier barrierKEK
}

// New returns an encrypting FS wrapping base. If base is nil,
// vfs.Default is used. The barrier provides the master key that wraps
// each file's per-file DEK; if barrier is nil New returns nil.
func New(base vfs.FS, barrier barrierKEK) vfs.FS {
	if base == nil {
		base = vfs.Default
	}
	if barrier == nil {
		return nil
	}
	return &FS{base: base, barrier: barrier}
}

// Create creates a new encrypted file at the given path. If a file
// already exists at the path it is replaced.
func (fs *FS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	h, err := newHeader(fs.barrier)
	if err != nil {
		return nil, err
	}
	base, err := fs.base.Create(name, category)
	if err != nil {
		return nil, err
	}
	enc, err := newWriteFile(base, h)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	return enc, nil
}

// Open opens an existing encrypted file for reading. If the file is
// not encrypted (no QENC magic), Open transparently returns the base
// file for passthrough access. This handles Pebble's LOCK and other
// non-content files that may not have been written through the
// encrypting FS.
func (fs *FS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	base, err := fs.base.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	enc, err := fs.openEncryptedRO(base)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	return enc, nil
}

// OpenReadWrite opens an existing encrypted file for reading and
// writing, or creates a new one if it does not exist. If the existing
// file is not in encfs format the call returns ErrInvalidHeader.
//
// V1 limitation: only append-style writes are supported; mid-file
// rewrites return ErrWriteAtUnsupported. If Pebble's I/O patterns
// require mid-file WriteAt this will be lifted in a follow-up.
func (fs *FS) OpenReadWrite(name string, category vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	base, err := fs.base.OpenReadWrite(name, category, opts...)
	if err != nil {
		return nil, err
	}
	fi, err := base.Stat()
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	if fi.Size() == 0 {
		// Fresh file: write a new header.
		h, err := newHeader(fs.barrier)
		if err != nil {
			_ = base.Close()
			return nil, err
		}
		enc, err := newWriteFile(base, h)
		if err != nil {
			_ = base.Close()
			return nil, err
		}
		return enc, nil
	}
	enc, err := fs.openEncryptedRW(base, fi.Size())
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	return enc, nil
}

// openEncryptedRO reads the header from a base file and returns a
// read-only encryptedFile. If the magic does not match, the base file
// is returned directly (passthrough path).
func (fs *FS) openEncryptedRO(base vfs.File) (vfs.File, error) {
	fi, err := base.Stat()
	if err != nil {
		return nil, err
	}
	physicalSize := fi.Size()
	if physicalSize < int64(fixedHeaderSize) {
		// Too small to be encrypted; pass through.
		return base, nil
	}
	var magic [lenMagic]byte
	if _, err := base.ReadAt(magic[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if string(magic[:]) != headerMagic {
		// Not an encrypted file; passthrough.
		return base, nil
	}
	h, err := readHeader(&offsetReader{base: base, off: 0}, fs.barrier)
	if err != nil {
		return nil, err
	}
	return newReadFile(base, h, physicalSize)
}

// openEncryptedRW reads the header for a non-empty file opened with
// OpenReadWrite. Mid-file writes are blocked at the encryptedFile
// layer; the file is positioned for append-only writes from the
// current logical end. If the file ends with a partial trailing
// block, that block is decrypted and reloaded into the in-memory
// tail so a subsequent Write extends it rather than replacing it.
func (fs *FS) openEncryptedRW(base vfs.File, physicalSize int64) (*encryptedFile, error) {
	var magic [lenMagic]byte
	if _, err := base.ReadAt(magic[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if string(magic[:]) != headerMagic {
		return nil, ErrInvalidHeader
	}
	h, err := readHeader(&offsetReader{base: base, off: 0}, fs.barrier)
	if err != nil {
		return nil, err
	}
	rf, err := newReadFile(base, h, physicalSize)
	if err != nil {
		return nil, err
	}
	rf.readOnly = false
	if rem := rf.logicalSize % int64(BlockSize); rem != 0 {
		blockIndex := rf.logicalSize / int64(BlockSize)
		cipherBuf := make([]byte, rem+int64(BlockOverhead))
		if _, err := base.ReadAt(cipherBuf, rf.physicalOffsetForBlock(blockIndex)); err != nil {
			return nil, err
		}
		pt, err := rf.crypto.decrypt(cipherBuf)
		if err != nil {
			return nil, err
		}
		rf.tail = pt
		rf.flushedSize = blockIndex * int64(BlockSize)
	}
	return rf, nil
}

// offsetReader adapts a vfs.File's ReadAt into io.Reader for use with
// readHeader.
type offsetReader struct {
	base vfs.File
	off  int64
}

func (r *offsetReader) Read(p []byte) (int, error) {
	n, err := r.base.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

// Link, Remove, RemoveAll, Rename, MkdirAll, Lock, List, Path*,
// GetDiskUsage are simple passthroughs: they operate on filenames or
// directory entries, not file contents.

func (fs *FS) Link(oldname, newname string) error { return fs.base.Link(oldname, newname) }
func (fs *FS) OpenDir(name string) (vfs.File, error) {
	return fs.base.OpenDir(name)
}
func (fs *FS) Remove(name string) error    { return fs.base.Remove(name) }
func (fs *FS) RemoveAll(name string) error { return fs.base.RemoveAll(name) }
func (fs *FS) Rename(oldname, newname string) error {
	return fs.base.Rename(oldname, newname)
}
func (fs *FS) ReuseForWrite(oldname, newname string, category vfs.DiskWriteCategory) (vfs.File, error) {
	// Reusing an existing file's data in encfs is non-trivial because
	// the new write needs a fresh header (DEK rotation). Treat
	// ReuseForWrite as a Rename+Create which discards the old contents
	// — Pebble's contract permits this.
	if err := fs.base.Remove(newname); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := fs.base.Rename(oldname, newname); err != nil {
		return nil, err
	}
	if err := fs.base.Remove(newname); err != nil {
		return nil, err
	}
	return fs.Create(newname, category)
}
func (fs *FS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.base.MkdirAll(dir, perm)
}
func (fs *FS) Lock(name string) (io.Closer, error) { return fs.base.Lock(name) }
func (fs *FS) List(dir string) ([]string, error)   { return fs.base.List(dir) }

// Stat returns a FileInfo whose Size reports the logical (plaintext)
// length when the file is encfs-formatted, and the raw physical size
// otherwise.
func (fs *FS) Stat(name string) (vfs.FileInfo, error) {
	fi, err := fs.base.Stat(name)
	if err != nil {
		return nil, err
	}
	physicalSize := fi.Size()
	if physicalSize < int64(fixedHeaderSize) || fi.IsDir() {
		return fi, nil
	}
	// Read just enough bytes to determine whether this is an encrypted
	// file and, if so, the header size.
	f, err := fs.base.Open(name)
	if err != nil {
		// The file may have been removed between Stat and Open.
		return fi, nil
	}
	defer func() { _ = f.Close() }()
	var fixed [fixedHeaderSize]byte
	if _, err := f.ReadAt(fixed[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return fi, nil
	}
	if string(fixed[offMagic:offMagic+lenMagic]) != headerMagic {
		return fi, nil
	}
	headerSize := int(uint16(fixed[offHeaderSize]) | uint16(fixed[offHeaderSize+1])<<8)
	if headerSize < fixedHeaderSize || headerSize > maxHeaderSize {
		return fi, nil
	}
	logicalSize, err := physicalToLogicalSize(physicalSize - int64(headerSize))
	if err != nil {
		return fi, nil
	}
	return &encFileInfo{FileInfo: fi, logicalSize: logicalSize}, nil
}

func (fs *FS) PathBase(path string) string    { return fs.base.PathBase(path) }
func (fs *FS) PathJoin(elem ...string) string { return fs.base.PathJoin(elem...) }
func (fs *FS) PathDir(path string) string     { return fs.base.PathDir(path) }
func (fs *FS) GetDiskUsage(path string) (vfs.DiskUsage, error) {
	return fs.base.GetDiskUsage(path)
}

// Unwrap returns nil to declare this FS as the root of any
// vfs.Root walk; returning the base would let callers bypass
// encryption.
func (fs *FS) Unwrap() vfs.FS { return nil }

// BarrierSealed reports whether the barrier keying this file system is
// sealed now, when the barrier can say (both go-qrdb's StorageBarrier and
// go-quicraft's EpochBarrier can). A sealed barrier refuses every Create,
// and Pebble panics when it cannot create its next WAL, so a store asks
// before any call that rotates one.
func (fs *FS) BarrierSealed() bool {
	b, ok := fs.barrier.(interface{ IsSealed() bool })
	return ok && b.IsSealed()
}
