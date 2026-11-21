// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// FS is a filesys.FS that transparently encrypts file contents using a
// seal.StorageBarrier as the key-encryption-key. It wraps a base FS
// (typically filesys.Default, or Pebble's FS bridged by pkg/storage/pebble).
type FS struct {
	base    filesys.FS
	barrier barrierKEK
}

// FS implements the optional capabilities by forwarding them to its base.
var (
	_ filesys.LockFS      = (*FS)(nil)
	_ filesys.HostFS      = (*FS)(nil)
	_ filesys.DiskUsageFS = (*FS)(nil)
)

// New returns an encrypting FS wrapping base. If base is nil,
// filesys.Default is used. The barrier provides the master key that wraps
// each file's per-file DEK; if barrier is nil New returns nil.
func New(base filesys.FS, barrier barrierKEK) filesys.FS {
	if base == nil {
		base = filesys.Default
	}
	if barrier == nil {
		return nil
	}
	return &FS{base: base, barrier: barrier}
}

// Create creates a new encrypted file at the given path. If a file
// already exists at the path it is replaced.
func (fs *FS) Create(name string) (filesys.File, error) {
	h, err := fs.newFileHeader(name)
	if err != nil {
		return nil, err
	}
	base, err := fs.base.Create(name)
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

// newFileHeader returns the header for a new file at name: the framed,
// seal-on-sync layout for an append-and-sync file (isAppendLogName), the
// fixed-size block layout, which keeps ReadAt arithmetic, for every other.
func (fs *FS) newFileHeader(name string) (*header, error) {
	if isAppendLogName(name) {
		return newFramedHeader(fs.barrier)
	}
	return newHeader(fs.barrier)
}

// isAppendLogName reports whether name is an append-and-sync file: a Pebble
// write-ahead log (*.log) or MANIFEST (MANIFEST-*). Such a file is Synced
// after every append, so a power cut can fall during any write to it.
func isAppendLogName(name string) bool {
	base := filepath.Base(name)
	return strings.HasSuffix(base, ".log") || strings.HasPrefix(base, "MANIFEST-")
}

// Open opens an existing encrypted file for reading. If the file is
// not encrypted (no QENC magic), Open transparently returns the base
// file for passthrough access. This handles Pebble's LOCK and other
// non-content files that may not have been written through the
// encrypting FS.
func (fs *FS) Open(name string) (filesys.File, error) {
	base, err := fs.base.Open(name)
	if err != nil {
		return nil, err
	}
	enc, err := fs.openEncryptedRO(base, isAppendLogName(name))
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
func (fs *FS) OpenReadWrite(name string) (filesys.File, error) {
	base, err := fs.base.OpenReadWrite(name)
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
		h, err := fs.newFileHeader(name)
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
	enc, err := fs.openEncryptedRW(base, fi.Size(), isAppendLogName(name))
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	return enc, nil
}

// openEncryptedRO reads the header from a base file and returns a
// read-only encryptedFile. If the magic does not match, the base file
// is returned directly (passthrough path).
func (fs *FS) openEncryptedRO(base filesys.File, appendLog bool) (filesys.File, error) {
	fi, err := base.Stat()
	if err != nil {
		return nil, err
	}
	physicalSize := fi.Size()
	if appendLog {
		fresh, err := fs.tornFramedHeader(base, physicalSize)
		if err != nil {
			return nil, err
		}
		if fresh != nil {
			return fs.emptyFramedRO(base, fresh)
		}
		if err := refuseDamagedFramedStart(base, physicalSize); err != nil {
			return nil, err
		}
	}
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
	return newReadFile(base, h, physicalSize, appendLog)
}

// tornFramedHeader returns the fresh framed header an append log torn inside
// its header opens with, or nil when the file's first physicalSize bytes are
// not such a tear. A tear leaves a prefix of the real header: shorter than a
// fresh header for this barrier, the magic, and, wherever the file reaches
// them, a header size and wrapped-DEK length equal to that fresh header's. A
// size field of any other in-bounds value is corruption, which refuses (its
// file may hold synced records). When two fresh headers differ in size the
// barrier's wrapped-DEK length is not deterministic, so the size a tear cut
// from cannot be known: that refuses too, rather than guess.
func (fs *FS) tornFramedHeader(base filesys.File, physicalSize int64) (*header, error) {
	// Inspect the on-disk marker before asking the barrier for anything:
	// only an empty file or one under the pending marker can be an
	// unfinished Create, so an intact (stamped) log opens without
	// barrier.Encrypt, which an exhausted or superseded barrier refuses
	// while it still decrypts.
	if physicalSize > 0 {
		_, pending, err := framedMagicState(base, physicalSize)
		if err != nil {
			return nil, err
		}
		if !pending {
			return nil, nil
		}
	}
	h, err := fs.freshFramedHeader()
	if err != nil {
		return nil, err
	}
	again, err := fs.freshFramedHeader()
	if err != nil {
		return nil, err
	}
	want := h.totalSize()
	if again.totalSize() != want || physicalSize > framedDataStart(h) {
		return nil, nil
	}
	if physicalSize == 0 {
		return h, nil
	}
	buf := make([]byte, physicalSize)
	if _, err := base.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// Only the pending marker is evidence that Create never finished: a
	// stamped magic means the header and commit slots were synced, so a
	// short stamped file is a committed log cut short, never a fresh one.
	n := min(len(buf), lenMagic)
	if buf[0] != pendingMagic || string(buf[1:n]) != headerMagic[1:n] {
		return nil, nil
	}
	if len(buf) >= offHeaderSize+2 && int(binary.LittleEndian.Uint16(buf[offHeaderSize:])) != want {
		return nil, nil
	}
	if len(buf) >= offWrappedDEKLen+4 && int(binary.LittleEndian.Uint32(buf[offWrappedDEKLen:])) != want-fixedHeaderSize {
		return nil, nil
	}
	return h, nil
}

// pendingMagic replaces the first magic byte of a framed log until Create
// has synced its whole header and commit slots; only then is the real magic
// stamped (and synced). A pending file is therefore an unfinished Create,
// and a stamped one cut into its header is a truncated committed log.
const pendingMagic byte = 'q'

// writeFramedStart writes a framed log's header and commit slots (img, at
// offset 0) under the pending marker, syncs them, then stamps the real
// magic and syncs again.
func writeFramedStart(base filesys.File, img []byte) error {
	pending := bytes.Clone(img)
	pending[offMagic] = pendingMagic
	if _, err := base.WriteAt(pending, 0); err != nil {
		return err
	}
	if err := base.Sync(); err != nil {
		return err
	}
	if _, err := base.WriteAt([]byte{headerMagic[0]}, offMagic); err != nil {
		return err
	}
	return base.Sync()
}

// framedMagicState reports whether a framed log's first bytes are a prefix
// of the stamped magic or of the pending one.
func framedMagicState(base filesys.File, physicalSize int64) (stamped, pending bool, err error) {
	n := min(physicalSize, int64(lenMagic))
	if n == 0 {
		return false, false, nil
	}
	buf := make([]byte, n)
	if _, err := base.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return false, false, err
	}
	if string(buf[1:]) != headerMagic[1:n] {
		return false, false, nil
	}
	return buf[0] == headerMagic[0], buf[0] == pendingMagic, nil
}

// refuseDamagedFramedStart refuses a framed log that tornFramedHeader did not
// classify as an unfinished Create: a pending marker on it, or a stamped
// magic on a file shorter than the fixed header, is corruption.
func refuseDamagedFramedStart(base filesys.File, physicalSize int64) error {
	stamped, pending, err := framedMagicState(base, physicalSize)
	if err != nil {
		return err
	}
	if pending {
		return ErrInvalidHeader
	}
	if stamped && physicalSize < int64(fixedHeaderSize) {
		return ErrTruncatedHeader
	}
	return nil
}

// freshFramedHeader is the header of a new append log (WAL or MANIFEST).
func (fs *FS) freshFramedHeader() (*header, error) {
	return newFramedHeader(fs.barrier)
}

// emptyFramedRO opens, read-only and empty, an append log torn inside its
// header. The header is written before any record, and no record is
// acknowledged before the file's first Sync, so such a file holds no
// acknowledged record: it reads as a log of logical size 0.
func (fs *FS) emptyFramedRO(base filesys.File, h *header) (*encryptedFile, error) {
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	start := framedDataStart(h)
	return &encryptedFile{
		base:       base,
		crypto:     crypto,
		headerSize: start,
		readOnly:   true,
		framed:     true,
		physEnd:    start,
		commitSeq:  commitSlots - 1,
	}, nil
}

// freshOverTornHeader reopens for writing an append log torn inside its
// header (see tornFramedHeader): it writes the fresh header h over the torn
// bytes, which are all shorter than h, so no byte past h exists to zero;
// the result is an empty log that takes appends.
func (fs *FS) freshOverTornHeader(base filesys.File, h *header, physicalSize int64) (*encryptedFile, error) {
	start := framedDataStart(h)
	if physicalSize > start {
		// tornFramedHeader passed only a file no longer than the header
		// and commit slots; never write past the bytes Create wrote.
		return nil, ErrTruncatedHeader
	}
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		return nil, err
	}
	if err := writeFramedStart(base, append(h.marshal(), crypto.freshCommitSlots(start)...)); err != nil {
		return nil, err
	}
	return &encryptedFile{
		base:       base,
		crypto:     crypto,
		headerSize: start,
		framed:     true,
		physEnd:    start,
		commitSeq:  commitSlots - 1,
	}, nil
}

// openEncryptedRW reads the header for a non-empty file opened with
// OpenReadWrite. Mid-file writes are blocked at the encryptedFile
// layer; the file is positioned for append-only writes from the
// current logical end. If the file ends with a partial trailing
// block, that block is decrypted and reloaded into the in-memory
// tail so a subsequent Write extends it rather than replacing it.
func (fs *FS) openEncryptedRW(base filesys.File, physicalSize int64, appendLog bool) (*encryptedFile, error) {
	if appendLog {
		fresh, err := fs.tornFramedHeader(base, physicalSize)
		if err != nil {
			return nil, err
		}
		if fresh != nil {
			return fs.freshOverTornHeader(base, fresh, physicalSize)
		}
		if err := refuseDamagedFramedStart(base, physicalSize); err != nil {
			return nil, err
		}
	}
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
	rf, err := newReadFile(base, h, physicalSize, appendLog)
	if err != nil {
		return nil, err
	}
	rf.readOnly = false
	if rf.framed {
		// Frames are sealed: appends start a new frame at the end of the
		// last authentic one. Zero a torn final frame first, so the next
		// scan reads it as zero fill rather than a frame with bytes after.
		// Only bytes past the commit boundary reach here (scanFrames
		// refuses a log whose synced frames do not authenticate).
		if torn := physicalSize - rf.physEnd; torn > 0 {
			if _, err := base.WriteAt(make([]byte, torn), rf.physEnd); err != nil {
				return nil, err
			}
		}
		return rf, nil
	}
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

// offsetReader adapts a filesys.File's ReadAt into io.Reader for use with
// readHeader.
type offsetReader struct {
	base filesys.File
	off  int64
}

func (r *offsetReader) Read(p []byte) (int, error) {
	n, err := r.base.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

// OpenDir, Link, Remove, RemoveAll, Rename, MkdirAll, List, Lock and
// DiskUsage are simple passthroughs: they operate on filenames or
// directory entries, not file contents.

func (fs *FS) OpenDir(name string) (filesys.File, error) { return fs.base.OpenDir(name) }
func (fs *FS) Link(oldname, newname string) error        { return fs.base.Link(oldname, newname) }
func (fs *FS) Remove(name string) error                  { return fs.base.Remove(name) }
func (fs *FS) RemoveAll(name string) error               { return fs.base.RemoveAll(name) }
func (fs *FS) Rename(oldname, newname string) error {
	return fs.base.Rename(oldname, newname)
}
func (fs *FS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.base.MkdirAll(dir, perm)
}
func (fs *FS) List(dir string) ([]string, error) { return fs.base.List(dir) }

// Lock locks name through the base FS, or reports an UnsupportedError
// when the base cannot lock.
func (fs *FS) Lock(name string) (io.Closer, error) {
	l, ok := fs.base.(filesys.LockFS)
	if !ok {
		return nil, &filesys.UnsupportedError{Op: "lock"}
	}
	return l.Lock(name)
}

// OnHost reports whether the base FS's names are host paths. Ciphertext
// lives at the name the caller gives, so they are this FS's too.
func (fs *FS) OnHost() bool {
	h, ok := fs.base.(filesys.HostFS)
	return ok && h.OnHost()
}

// DiskUsage reports the base FS's disk usage, or an UnsupportedError
// when the base cannot.
func (fs *FS) DiskUsage(path string) (filesys.DiskUsage, error) {
	d, ok := fs.base.(filesys.DiskUsageFS)
	if !ok {
		return filesys.DiskUsage{}, &filesys.UnsupportedError{Op: "disk usage"}
	}
	return d.DiskUsage(path)
}

// Stat returns a FileInfo whose Size reports the logical (plaintext)
// length when the file is encfs-formatted, and the raw physical size
// otherwise.
func (fs *FS) Stat(name string) (os.FileInfo, error) {
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
	if fixed[offFlags]&headerFlagFramed != 0 {
		// A framed file's size is the sum of its authentic frames:
		// open it as a read does, so Stat agrees with File.Stat and
		// with the bytes a read returns, torn tail excluded.
		ef, err := fs.Open(name)
		if err != nil {
			return nil, err
		}
		defer func() { _ = ef.Close() }()
		return ef.Stat()
	}
	logicalSize, err := physicalToLogicalSize(physicalSize - int64(headerSize))
	if err != nil {
		return fi, nil
	}
	return &encFileInfo{FileInfo: fi, logicalSize: logicalSize}, nil
}

// BarrierSealed reports whether the barrier keying this file system is
// sealed now, when the barrier can say (both go-qrdb's StorageBarrier and
// go-quicraft's EpochBarrier can). A sealed barrier refuses every Create,
// and Pebble panics when it cannot create its next WAL, so a store asks
// before any call that rotates one.
func (fs *FS) BarrierSealed() bool {
	b, ok := fs.barrier.(interface{ IsSealed() bool })
	return ok && b.IsSealed()
}

// Quiesce returns the quiesce gate of the barrier keying this file system,
// through which a store over it holds its in-flight work while the barrier
// seals; nil when the barrier has none.
func (fs *FS) Quiesce() *quiesce.Gate {
	b, ok := fs.barrier.(interface{ Quiesce() *quiesce.Gate })
	if !ok {
		return nil
	}
	return b.Quiesce()
}
