// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs

// coverage_gaps_test.go covers the branches in block.go, encfile.go,
// and fs.go that were below the 91% target. All tests use real
// encrypt/decrypt round-trips, AEAD authentication failures, or
// error-injecting FS wrappers — no coverage padding.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// ---------------------------------------------------------------------------
// Compile-time interface guard: errFile and errFS satisfy vfs.File / vfs.FS.
// ---------------------------------------------------------------------------

var _ vfs.File = (*errFile)(nil)

// errFile is a vfs.File that wraps a real vfs.File but injects an error
// on the first call of a specific method. It is used to trigger error
// branches in encryptedFile that are otherwise unreachable with a
// well-behaved MemFS.
type errFile struct {
	real       vfs.File
	writeAtErr error
	syncErr    error
	statErr    error
	writeErr   error
}

func (f *errFile) Close() error               { return f.real.Close() }
func (f *errFile) Read(p []byte) (int, error) { return f.real.Read(p) }
func (f *errFile) ReadAt(p []byte, off int64) (int, error) {
	return f.real.ReadAt(p, off)
}
func (f *errFile) Fd() uintptr { return vfs.InvalidFd }
func (f *errFile) Preallocate(offset, length int64) error {
	return f.real.Preallocate(offset, length)
}
func (f *errFile) Prefetch(offset, length int64) error {
	return f.real.Prefetch(offset, length)
}
func (f *errFile) SyncData() error { return f.Sync() }
func (f *errFile) SyncTo(length int64) (bool, error) {
	if err := f.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func (f *errFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.real.Write(p)
}

func (f *errFile) WriteAt(p []byte, off int64) (int, error) {
	if f.writeAtErr != nil {
		return 0, f.writeAtErr
	}
	return f.real.WriteAt(p, off)
}

func (f *errFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.real.Sync()
}

func (f *errFile) Stat() (vfs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.real.Stat()
}

// errFS wraps vfs.NewMem() and injects configurable errors on specific
// operations. Only the methods under test are wrapped; all others
// delegate to the underlying MemFS.
type errFS struct {
	real      vfs.FS
	createErr error
	openRWErr error
	statErr   error
	removeErr error
	renameErr error
	// removeCount counts how many times Remove has been called so callers
	// can inject an error on the second call (ReuseForWrite path).
	removeCalls int
	removeErrOn int // 0 = always, N = on Nth call (1-based)
}

func newErrFS() *errFS {
	return &errFS{real: vfs.NewMem(), removeErrOn: 0}
}

func (fs *errFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	if fs.createErr != nil {
		return nil, fs.createErr
	}
	return fs.real.Create(name, cat)
}

func (fs *errFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.Open(name, opts...)
}

func (fs *errFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	if fs.openRWErr != nil {
		return nil, fs.openRWErr
	}
	return fs.real.OpenReadWrite(name, cat, opts...)
}

func (fs *errFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *errFS) Remove(name string) error {
	fs.removeCalls++
	if fs.removeErr != nil {
		if fs.removeErrOn == 0 || fs.removeCalls == fs.removeErrOn {
			return fs.removeErr
		}
	}
	return fs.real.Remove(name)
}
func (fs *errFS) RemoveAll(name string) error { return fs.real.RemoveAll(name) }
func (fs *errFS) Rename(oldname, newname string) error {
	if fs.renameErr != nil {
		return fs.renameErr
	}
	return fs.real.Rename(oldname, newname)
}
func (fs *errFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *errFS) MkdirAll(dir string, perm os.FileMode) error { return fs.real.MkdirAll(dir, perm) }
func (fs *errFS) Lock(name string) (io.Closer, error)         { return fs.real.Lock(name) }
func (fs *errFS) List(dir string) ([]string, error)           { return fs.real.List(dir) }
func (fs *errFS) Stat(name string) (vfs.FileInfo, error) {
	if fs.statErr != nil {
		return nil, fs.statErr
	}
	return fs.real.Stat(name)
}
func (fs *errFS) PathBase(p string) string       { return fs.real.PathBase(p) }
func (fs *errFS) PathJoin(elem ...string) string { return fs.real.PathJoin(elem...) }
func (fs *errFS) PathDir(p string) string        { return fs.real.PathDir(p) }
func (fs *errFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *errFS) Link(old, new string) error { return fs.real.Link(old, new) }
func (fs *errFS) Unwrap() vfs.FS             { return nil }

// ---------------------------------------------------------------------------
// block.go — newBlockCrypto error branches
// ---------------------------------------------------------------------------

// TestBlockCrypto_ExactlyFullCiphertextBlock ensures decrypt rejects a
// block one byte larger than the maximum ciphertext block size.
func TestBlockCrypto_MaxCiphertextDecrypt(t *testing.T) {
	c := freshBlockCrypto(t)
	// CiphertextBlockSize is the largest valid ciphertext; CiphertextBlockSize+1 must fail.
	if _, err := c.decrypt(make([]byte, CiphertextBlockSize+1)); !errors.Is(err, ErrBlockTooShort) {
		t.Errorf("error = %v, want ErrBlockTooShort", err)
	}
}

// TestBlockCrypto_ExactOverheadBlock confirms that a block of exactly
// BlockOverhead bytes (empty plaintext) decrypts successfully when
// produced by encrypt(nil).
func TestBlockCrypto_ExactOverheadOnlyBlock(t *testing.T) {
	c := freshBlockCrypto(t)
	ct, err := c.encrypt([]byte{})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if len(ct) != BlockOverhead {
		t.Errorf("len(ct) = %d, want %d", len(ct), BlockOverhead)
	}
	pt, err := c.decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("plaintext len = %d, want 0", len(pt))
	}
}

// TestBlockCrypto_AuthFailOnTag confirms that flipping the last byte
// of the GCM auth tag causes ErrAuthFailed.
func TestBlockCrypto_AuthFailOnTag(t *testing.T) {
	c := freshBlockCrypto(t)
	pt := bytes.Repeat([]byte("B"), 128)
	ct, err := c.encrypt(pt)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ct[len(ct)-1] ^= 0xFF
	if _, err := c.decrypt(ct); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("error = %v, want ErrAuthFailed", err)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — newWriteFile error: base.Write fails
// ---------------------------------------------------------------------------

// TestNewWriteFile_BaseWriteError ensures that newWriteFile returns an
// error (and does not panic) when writing the header to the base file fails.
func TestNewWriteFile_BaseWriteError(t *testing.T) {
	sentinel := errors.New("write-header-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	// Use a base errFile whose Write always fails.
	mem := vfs.NewMem()
	realFile, err := mem.Create("w.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = realFile.Close() }()
	ef := &errFile{real: realFile, writeErr: sentinel}
	_, gotErr := newWriteFile(ef, h)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — newReadFile error: physicalSize smaller than header
// ---------------------------------------------------------------------------

// TestNewReadFile_PhysicalSizeSmallerThanHeader exercises the negative
// payload branch in physicalToLogicalSize (payload < 0 after subtracting
// header size). We build the header and pass a physicalSize equal to the
// header size minus one so payload is negative.
func TestNewReadFile_NegativePayload(t *testing.T) {
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	crypto, err := newBlockCrypto(h.DEK[:])
	if err != nil {
		t.Fatalf("newBlockCrypto: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("r.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = realFile.Close() }()
	_ = crypto // silence unused warning; we need the header for its totalSize

	// physicalSize < h.totalSize() => payload = physicalSize - headerSize < 0
	_, gotErr := newReadFile(realFile, h, int64(h.totalSize()-1))
	if gotErr == nil {
		t.Error("expected error for negative payload size")
	}
}

// ---------------------------------------------------------------------------
// encfile.go — Close: flushTailLocked write error
// ---------------------------------------------------------------------------

// TestEncFile_CloseFlushTailWriteError verifies that if the base file's
// WriteAt fails during Close's implicit tail flush, Close returns that error.
func TestEncFile_CloseFlushTailWriteError(t *testing.T) {
	sentinel := errors.New("close-flush-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("cf.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Write the header first using the real file, then wrap with errFile
	// that will fail on WriteAt.
	ef := &errFile{real: realFile}
	enc, err := newWriteFile(ef, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	// Write a few bytes so the tail is non-empty.
	if _, err := enc.Write([]byte("partial")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Now inject the WriteAt error so the tail flush on Close fails.
	ef.writeAtErr = sentinel
	gotErr := enc.Close()
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("Close error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — flushFullBlockLocked: wrong tail length guard
// ---------------------------------------------------------------------------

// TestEncFile_FlushFullBlockWrongLength exercises the guard in
// flushFullBlockLocked that rejects a tail that is not exactly BlockSize
// bytes. This is an internal invariant check; we call it directly.
func TestEncFile_FlushFullBlockWrongLength(t *testing.T) {
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("fb.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	enc, err := newWriteFile(realFile, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	defer func() { _ = enc.base.Close() }()
	// Manually set tail to a wrong length.
	enc.tail = make([]byte, BlockSize-1)
	enc.logicalSize = int64(BlockSize - 1)
	gotErr := enc.flushFullBlockLocked()
	if gotErr == nil {
		t.Error("expected error for mis-sized tail")
	}
}

// TestEncFile_FlushFullBlockWriteAtError verifies that a WriteAt error
// during a full-block flush is propagated out of appendLocked.
func TestEncFile_FlushFullBlockWriteAtError(t *testing.T) {
	sentinel := errors.New("full-block-writeat-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("ffb.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ef := &errFile{real: realFile}
	enc, err := newWriteFile(ef, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	// Fill the tail to exactly BlockSize so the next Write triggers a flush.
	if _, err := enc.Write(make([]byte, BlockSize)); err != nil {
		t.Fatalf("Write first block: %v", err)
	}
	// At this point the first block was flushed. Now inject the error and
	// attempt to write a second block so flush is triggered again.
	ef.writeAtErr = sentinel
	_, gotErr := enc.Write(make([]byte, BlockSize+1))
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("Write error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — flushTailLocked: WriteAt error path
// ---------------------------------------------------------------------------

// TestEncFile_SyncFlushTailWriteAtError verifies that a WriteAt error
// during Sync's tail flush is surfaced by Sync.
func TestEncFile_SyncFlushTailWriteAtError(t *testing.T) {
	sentinel := errors.New("sync-tail-writeat-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("sf.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ef := &errFile{real: realFile}
	enc, err := newWriteFile(ef, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	if _, err := enc.Write([]byte("partial")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ef.writeAtErr = sentinel
	gotErr := enc.Sync()
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("Sync error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — Sync: read-only file path
// ---------------------------------------------------------------------------

// TestEncFile_SyncReadOnly confirms that Sync on a read-only encryptedFile
// delegates to the base file's Sync without attempting to flush a tail.
func TestEncFile_SyncReadOnly(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("sync_ro.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("some data"))
	_ = f.Close()
	r, err := fs.Open("sync_ro.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	// Sync on a read-only file must succeed.
	if err := r.Sync(); err != nil {
		t.Errorf("Sync on RO file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — SyncTo: Sync error path
// ---------------------------------------------------------------------------

// TestEncFile_SyncToSyncError ensures that SyncTo propagates a Sync failure.
func TestEncFile_SyncToSyncError(t *testing.T) {
	sentinel := errors.New("syncto-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("sto.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ef := &errFile{real: realFile}
	enc, err := newWriteFile(ef, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	if _, err := enc.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ef.writeAtErr = sentinel
	_, gotErr := enc.SyncTo(4)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("SyncTo error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — Stat: base.Stat error
// ---------------------------------------------------------------------------

// TestEncFile_StatBaseError confirms that Stat returns the underlying
// error when the base file's Stat call fails.
func TestEncFile_StatBaseError(t *testing.T) {
	sentinel := errors.New("stat-fail")
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	mem := vfs.NewMem()
	realFile, err := mem.Create("stat.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ef := &errFile{real: realFile}
	enc, err := newWriteFile(ef, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	defer func() { _ = enc.base.Close() }()
	ef.statErr = sentinel
	_, gotErr := enc.Stat()
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("Stat error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — ReadAt: tampered ciphertext from disk triggers ErrAuthFailed
// ---------------------------------------------------------------------------

// TestEncFile_ReadAtTamperedCiphertext writes a valid encrypted file,
// corrupts one byte of the on-disk ciphertext payload, then verifies
// that ReadAt returns an error (ErrAuthFailed propagated from decrypt).
func TestEncFile_ReadAtTamperedCiphertext(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	fs := New(mem, b)

	f, err := fs.Create("tamper.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pt := bytes.Repeat([]byte("Z"), BlockSize+100) // two blocks
	if _, err := f.Write(pt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Corrupt the raw file: flip a byte inside the first block's ciphertext
	// (after the header and nonce, within the GCM body).
	rawFile, err := mem.OpenReadWrite("tamper.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("raw OpenReadWrite: %v", err)
	}
	fi, _ := rawFile.Stat()
	rawBuf := make([]byte, fi.Size())
	_, _ = rawFile.ReadAt(rawBuf, 0)
	// The first block starts at fixedHeaderSize + len(wrappedDEK).
	// The nonce occupies the first inlineNonceSize bytes of the block.
	// We need the actual header size; read it from the on-disk bytes.
	headerSize := int(uint16(rawBuf[offHeaderSize]) | uint16(rawBuf[offHeaderSize+1])<<8)
	corruptOffset := int64(headerSize + inlineNonceSize + 5) // inside GCM body
	rawBuf[corruptOffset] ^= 0xFF
	if _, err := rawFile.WriteAt(rawBuf, 0); err != nil {
		t.Fatalf("corrupt WriteAt: %v", err)
	}
	_ = rawFile.Close()

	// Open through encfs and attempt to read. Should fail with auth error.
	r, err := fs.Open("tamper.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	buf := make([]byte, BlockSize)
	_, gotErr := r.ReadAt(buf, 0)
	if gotErr == nil {
		t.Error("expected error reading tampered ciphertext, got nil")
	}
	if !errors.Is(gotErr, ErrAuthFailed) {
		t.Errorf("error = %v, want ErrAuthFailed", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — ReadAt: in-memory tail boundary (copyLen <= 0 guard)
// ---------------------------------------------------------------------------

// TestEncFile_ReadAtExactLogicalEnd exercises the case where ReadAt is
// called at exactly off == logicalSize, which must return io.EOF immediately.
func TestEncFile_ReadAtExactLogicalEnd(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("end.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("abc"))
	_ = f.Close()
	r, err := fs.Open("end.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	_, gotErr := r.ReadAt(make([]byte, 1), 3)
	if !errors.Is(gotErr, io.EOF) {
		t.Errorf("ReadAt at end: err = %v, want io.EOF", gotErr)
	}
}

// ---------------------------------------------------------------------------
// encfile.go — ReadAt: tail EOF when tailIntra >= len(tail)
// ---------------------------------------------------------------------------

// TestEncFile_ReadAtTailBoundaryEOF exercises the branch inside the
// tail-read path where tailIntra >= int64(len(tail)), triggering a
// premature io.EOF. This is reached by reading from a file while it has
// data in the in-memory tail (unflushed write), requesting at a logical
// offset inside the tail block but past the current tail bytes.
//
// We write a partial block (tail exists), then re-open via OpenReadWrite
// so the file has a loaded tail and is in RW mode, then read at an
// offset that maps to a tailIntra past the tail data.
func TestEncFile_ReadAtTailPastEnd(t *testing.T) {
	_, _, fs := newMemFS(t)
	// Write more than one full block so tail starts at a known position.
	// After Close the tail is flushed. Re-open with OpenReadWrite so that
	// the trailing block is reloaded into the tail.
	f, err := fs.Create("tailpast.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	payload := make([]byte, BlockSize+50)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Re-open via OpenReadWrite: this loads the 50-byte tail back into
	// encryptedFile.tail and sets flushedSize = BlockSize.
	g, err := fs.OpenReadWrite("tailpast.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	defer func() { _ = g.Close() }()
	// Reading at offset BlockSize+50 is past logicalSize, so io.EOF.
	_, gotErr := g.ReadAt(make([]byte, 1), int64(BlockSize+50))
	if !errors.Is(gotErr, io.EOF) {
		t.Errorf("err = %v, want io.EOF", gotErr)
	}
}

// ---------------------------------------------------------------------------
// fs.go — Create: newHeader error (sealed barrier)
// ---------------------------------------------------------------------------

// TestFS_CreateNewHeaderError confirms that Create returns an error when
// newHeader fails (barrier sealed).
func TestFS_CreateNewHeaderError(t *testing.T) {
	_, b, fs := newMemFS(t)
	b.seal()
	if _, err := fs.Create("noh.dat", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("expected Create to fail with sealed barrier")
	}
}

// TestFS_CreateBaseCreateError confirms that Create returns an error when
// the underlying FS Create fails.
func TestFS_CreateBaseCreateError(t *testing.T) {
	sentinel := errors.New("base-create-fail")
	errBase := newErrFS()
	errBase.createErr = sentinel
	b := newFakeBarrier(t)
	fs := New(errBase, b)
	_, gotErr := fs.Create("x.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// TestFS_CreateNewWriteFileError confirms that Create cleans up the base
// file and returns an error when writing the header to the base file fails.
// We use a base FS whose Create returns an errFile with writeErr set so
// that newWriteFile's first Write call fails.
func TestFS_CreateNewWriteFileError(t *testing.T) {
	sentinel := errors.New("base-write-fail")
	// Build a custom FS that returns an errFile from Create.
	base := &writeErrFS{real: vfs.NewMem(), writeErr: sentinel}
	b := newFakeBarrier(t)
	fs := New(base, b)
	_, gotErr := fs.Create("y.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// writeErrFS is a minimal vfs.FS whose Create returns an errFile that
// fails Write calls with writeErr.
type writeErrFS struct {
	real     vfs.FS
	writeErr error
}

func (fs *writeErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	f, err := fs.real.Create(name, cat)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, writeErr: fs.writeErr}, nil
}

func (fs *writeErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.Open(name, opts...)
}
func (fs *writeErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.OpenReadWrite(name, cat, opts...)
}
func (fs *writeErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *writeErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *writeErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *writeErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *writeErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *writeErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *writeErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *writeErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *writeErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *writeErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *writeErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *writeErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *writeErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *writeErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *writeErrFS) Unwrap() vfs.FS { return nil }

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: base.Stat error
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteStatError confirms that OpenReadWrite returns an
// error when the base file's Stat fails immediately after opening.
func TestFS_OpenReadWriteStatError(t *testing.T) {
	sentinel := errors.New("orw-stat-fail")
	base := &statErrFS{real: vfs.NewMem(), statErr: sentinel}
	b := newFakeBarrier(t)
	fs := New(base, b)
	_, gotErr := fs.OpenReadWrite("rw.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// statErrFS returns an errFile from OpenReadWrite whose Stat fails.
type statErrFS struct {
	real    vfs.FS
	statErr error
}

func (fs *statErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *statErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.Open(name, opts...)
}
func (fs *statErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.real.OpenReadWrite(name, cat, opts...)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, statErr: fs.statErr}, nil
}
func (fs *statErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *statErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *statErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *statErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *statErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *statErrFS) MkdirAll(dir string, perm os.FileMode) error { return fs.real.MkdirAll(dir, perm) }
func (fs *statErrFS) Lock(name string) (io.Closer, error)         { return fs.real.Lock(name) }
func (fs *statErrFS) List(dir string) ([]string, error)           { return fs.real.List(dir) }
func (fs *statErrFS) Stat(name string) (vfs.FileInfo, error)      { return fs.real.Stat(name) }
func (fs *statErrFS) Link(old, new string) error                  { return fs.real.Link(old, new) }
func (fs *statErrFS) PathBase(p string) string                    { return fs.real.PathBase(p) }
func (fs *statErrFS) PathJoin(elem ...string) string              { return fs.real.PathJoin(elem...) }
func (fs *statErrFS) PathDir(p string) string                     { return fs.real.PathDir(p) }
func (fs *statErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *statErrFS) Unwrap() vfs.FS { return nil }

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: newHeader error for fresh file (sealed barrier)
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteNewHeaderError confirms OpenReadWrite returns an error
// when the barrier is sealed and a fresh (empty) file needs a header.
func TestFS_OpenReadWriteNewHeaderError(t *testing.T) {
	_, b, fs := newMemFS(t)
	b.seal()
	if _, err := fs.OpenReadWrite("sealed.dat", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("expected error with sealed barrier on fresh OpenReadWrite")
	}
}

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: openEncryptedRW error (bad magic in existing file)
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteExistingBadMagic confirms that OpenReadWrite on an
// existing non-encfs file returns ErrInvalidHeader. This exercises the
// openEncryptedRW code path (file size > 0) hitting the magic mismatch.
func TestFS_OpenReadWriteExistingBadMagic(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	fs := New(mem, b)
	// Write a plain file (> fixedHeaderSize bytes so it looks like an encfs
	// file to the size check but has wrong magic).
	plain, _ := mem.Create("bad.dat", vfs.WriteCategoryUnspecified)
	_, _ = plain.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
	_ = plain.Close()
	if _, err := fs.OpenReadWrite("bad.dat", vfs.WriteCategoryUnspecified); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

// ---------------------------------------------------------------------------
// fs.go — openEncryptedRO: base.ReadAt error (not io.EOF)
// ---------------------------------------------------------------------------

// TestFS_OpenEncryptedROReadAtError exercises the branch in openEncryptedRO
// where ReadAt of the magic bytes returns a non-EOF error. We use a
// readAtErrFS that returns a sentinel error on ReadAt.
func TestFS_OpenEncryptedROReadAtError(t *testing.T) {
	sentinel := errors.New("readat-magic-fail")
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	// Create a file large enough that the size check passes.
	{
		f, _ := mem.Create("readerr.dat", vfs.WriteCategoryUnspecified)
		_, _ = f.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
		_ = f.Close()
	}
	// Wrap with a FS that wraps Open's returned file to inject a ReadAt error.
	base := &readAtErrFS{real: mem, readAtErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.Open("readerr.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// readAtErrFS injects a readAtErr into files returned by Open (for the
// magic-check ReadAt in openEncryptedRO).
type readAtErrFS struct {
	real      vfs.FS
	readAtErr error
}

func (fs *readAtErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *readAtErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.real.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	return &readAtErrFile{File: f, readAtErr: fs.readAtErr}, nil
}
func (fs *readAtErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.OpenReadWrite(name, cat, opts...)
}
func (fs *readAtErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *readAtErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *readAtErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *readAtErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *readAtErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *readAtErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *readAtErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *readAtErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *readAtErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *readAtErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *readAtErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *readAtErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *readAtErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *readAtErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *readAtErrFS) Unwrap() vfs.FS { return nil }

// readAtErrFile wraps a vfs.File and injects readAtErr on ReadAt.
type readAtErrFile struct {
	vfs.File
	readAtErr error
}

func (f *readAtErrFile) ReadAt(p []byte, off int64) (int, error) {
	return 0, f.readAtErr
}

// ---------------------------------------------------------------------------
// fs.go — ReuseForWrite: Remove(newname) error that is not ErrNotExist
// ---------------------------------------------------------------------------

// TestFS_ReuseForWriteRemoveNewNameError verifies that ReuseForWrite
// propagates a non-ErrNotExist error returned by Remove(newname).
func TestFS_ReuseForWriteRemoveNewNameError(t *testing.T) {
	sentinel := errors.New("remove-newname-fail")
	errBase := newErrFS()
	errBase.removeErr = sentinel
	errBase.removeErrOn = 1 // first Remove call fails
	b := newFakeBarrier(t)
	encFS := New(errBase, b)
	// Create old file.
	f, err := encFS.Create("old.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _ = f.Write([]byte("data"))
	_ = f.Close()
	// Create new file too so Remove(newname) is called on something that exists.
	g, err := encFS.Create("new.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create new: %v", err)
	}
	_ = g.Close()
	_, gotErr := encFS.ReuseForWrite("old.dat", "new.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// TestFS_ReuseForWriteRenameError verifies that ReuseForWrite propagates
// an error from Rename(oldname, newname).
func TestFS_ReuseForWriteRenameError(t *testing.T) {
	sentinel := errors.New("rename-fail")
	errBase := newErrFS()
	errBase.renameErr = sentinel
	b := newFakeBarrier(t)
	encFS := New(errBase, b)
	f, err := encFS.Create("r_old.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _ = f.Write([]byte("data"))
	_ = f.Close()
	_, gotErr := encFS.ReuseForWrite("r_old.dat", "r_new.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// TestFS_ReuseForWriteSecondRemoveError verifies that ReuseForWrite
// propagates an error from the second Remove(newname) call (after Rename).
func TestFS_ReuseForWriteSecondRemoveError(t *testing.T) {
	sentinel := errors.New("second-remove-fail")
	errBase := newErrFS()
	errBase.removeErr = sentinel
	errBase.removeErrOn = 2 // second Remove call fails
	b := newFakeBarrier(t)
	encFS := New(errBase, b)
	f, err := encFS.Create("sr_old.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _ = f.Write([]byte("data"))
	_ = f.Close()
	_, gotErr := encFS.ReuseForWrite("sr_old.dat", "sr_new.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// fs.go — Stat: directory passthrough (IsDir == true)
// ---------------------------------------------------------------------------

// TestFS_StatDirectory confirms that Stat on a directory returns the
// raw FileInfo without attempting to read a header.
func TestFS_StatDirectory(t *testing.T) {
	_, _, fs := newMemFS(t)
	if err := fs.MkdirAll("adir", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	fi, err := fs.Stat("adir")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if !fi.IsDir() {
		t.Error("expected IsDir() == true for directory")
	}
}

// ---------------------------------------------------------------------------
// fs.go — Stat: file with valid magic but out-of-range header size field
// ---------------------------------------------------------------------------

// TestFS_StatBadHeaderSizeField creates a raw file whose first bytes look
// like an encfs header (correct magic) but whose header_size field is below
// fixedHeaderSize. Stat must fall back to the raw physical size.
func TestFS_StatBadHeaderSizeField(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a real encfs file to get valid initial bytes, then corrupt
	// the header_size field.
	f, _ := enc.Create("hdr.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()

	// Read raw bytes.
	rawFile, _ := mem.OpenReadWrite("hdr.dat", vfs.WriteCategoryUnspecified)
	fi, _ := rawFile.Stat()
	raw := make([]byte, fi.Size())
	_, _ = rawFile.ReadAt(raw, 0)
	_ = rawFile.Close()

	// Corrupt header_size to be 1 (less than fixedHeaderSize).
	raw[offHeaderSize] = 1
	raw[offHeaderSize+1] = 0
	corrupt, _ := mem.Create("hdr_bad.dat", vfs.WriteCategoryUnspecified)
	_, _ = corrupt.Write(raw)
	_ = corrupt.Close()

	fi2, err := enc.Stat("hdr_bad.dat")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Should fall back to physical size (header_size was invalid, so no
	// logical size adjustment applied).
	if fi2.Size() != int64(len(raw)) {
		t.Errorf("size = %d, want raw physical = %d", fi2.Size(), len(raw))
	}
}

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: base.OpenReadWrite error
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteBaseError verifies that OpenReadWrite returns an
// error when the underlying FS's OpenReadWrite call fails.
func TestFS_OpenReadWriteBaseError(t *testing.T) {
	sentinel := errors.New("orw-base-fail")
	errBase := newErrFS()
	errBase.openRWErr = sentinel
	b := newFakeBarrier(t)
	enc := New(errBase, b)
	_, gotErr := enc.OpenReadWrite("x.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// ---------------------------------------------------------------------------
// fs.go — Stat: open-after-stat TOCTOU (Open fails), ReadAt error passthrough,
// magic-mismatch in large file, physicalToLogicalSize error
// ---------------------------------------------------------------------------

// TestFS_StatOpenAfterStatError confirms that Stat returns the raw FileInfo
// (no panic, no error) when the base Open fails after a successful Stat.
// This simulates the TOCTOU window where a file is removed between Stat and Open.
func TestFS_StatOpenAfterStatError(t *testing.T) {
	b := newFakeBarrier(t)
	base := &statOpenErrFS{real: vfs.NewMem()}
	enc := New(base, b)

	// Create an encfs file that is large enough to trigger the Open path.
	realEnc := New(base.real, b)
	f, _ := realEnc.Create("toctou.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write(make([]byte, 100))
	_ = f.Close()

	// Now tell statOpenErrFS to fail on Open calls.
	base.openErr = errors.New("toctou-open-fail")
	fi, err := enc.Stat("toctou.dat")
	if err != nil {
		t.Fatalf("Stat should not error on TOCTOU open fail, got: %v", err)
	}
	// The returned FileInfo should be the raw physical one (not encFileInfo).
	if fi == nil {
		t.Error("expected non-nil FileInfo")
	}
}

// statOpenErrFS delegates all operations but can inject an error on Open.
type statOpenErrFS struct {
	real    vfs.FS
	openErr error
}

func (fs *statOpenErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *statOpenErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	if fs.openErr != nil {
		return nil, fs.openErr
	}
	return fs.real.Open(name, opts...)
}
func (fs *statOpenErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.OpenReadWrite(name, cat, opts...)
}
func (fs *statOpenErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *statOpenErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *statOpenErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *statOpenErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *statOpenErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *statOpenErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *statOpenErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *statOpenErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *statOpenErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *statOpenErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *statOpenErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *statOpenErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *statOpenErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *statOpenErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *statOpenErrFS) Unwrap() vfs.FS { return nil }

// TestFS_StatLargeNonEncfsMagicMismatch confirms that Stat on a large file
// (>= fixedHeaderSize) whose first bytes are not QENC returns the raw
// physical size rather than a computed logical size.
func TestFS_StatLargeNonEncfsMagicMismatch(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a file with no QENC magic, larger than fixedHeaderSize.
	var writeBuf [fixedHeaderSize + 50]byte
	for i := range writeBuf {
		writeBuf[i] = 'Z'
	}
	f, _ := mem.Create("largenoenc.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write(writeBuf[:])
	_ = f.Close()

	fi, err := enc.Stat("largenoenc.dat")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Should report raw physical size (no logical adjustment).
	if fi.Size() != int64(len(writeBuf)) {
		t.Errorf("size = %d, want %d (physical passthrough)", fi.Size(), len(writeBuf))
	}
}

// TestFS_StatPhysicalToLogicalError confirms that Stat handles a file whose
// physical size implies a corrupt block framing by falling back to raw size.
// We craft a file whose payload (physicalSize - headerSize) has a remainder
// modulo CiphertextBlockSize that is in [1, BlockOverhead-1], which is the
// range physicalToLogicalSize rejects as "trailing block too short".
func TestFS_StatPhysicalToLogicalError(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a valid encfs file with enough data to know the header size.
	f, _ := enc.Create("ptle.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()

	// Read the raw bytes to find the actual header size.
	rawFile, _ := mem.Open("ptle.dat")
	fi, _ := rawFile.Stat()
	raw := make([]byte, fi.Size())
	_, _ = rawFile.ReadAt(raw, 0)
	_ = rawFile.Close()

	// Determine how many bytes to truncate so that
	// payload % CiphertextBlockSize is in [1, BlockOverhead-1].
	// Current payload = len(raw) - headerSize = BlockOverhead + 5 = 33 bytes.
	// We need payload such that payload % CiphertextBlockSize is in [1, 27].
	// Truncating to headerSize+1 leaves payload=1, which satisfies [1,27).
	headerSize := int(uint16(raw[offHeaderSize]) | uint16(raw[offHeaderSize+1])<<8)
	truncated := raw[:headerSize+1] // payload = 1 byte < BlockOverhead

	corrupt, _ := mem.Create("ptle_corrupt.dat", vfs.WriteCategoryUnspecified)
	_, _ = corrupt.Write(truncated)
	_ = corrupt.Close()

	fiStat, err := enc.Stat("ptle_corrupt.dat")
	if err != nil {
		t.Fatalf("Stat should not return error on corrupt framing, got: %v", err)
	}
	// Falls back to raw physical size.
	if fiStat.Size() != int64(len(truncated)) {
		t.Errorf("size = %d, want raw physical = %d", fiStat.Size(), len(truncated))
	}
}

// ---------------------------------------------------------------------------
// header.go — newHeader: wrappedDEK oversized/empty (already covered in
// header_test.go); ensure readHeader rejects wrappedLen > maxWrappedDEKSize
// ---------------------------------------------------------------------------

// TestHeader_ReadHeaderOversizedWrappedLen crafts a raw header where the
// wrappedDEKLen field claims a size beyond maxWrappedDEKSize (but
// header_size is set to match). readHeader must return ErrInvalidHeader.
func TestHeader_ReadHeaderOversizedWrappedLen(t *testing.T) {
	b := newFakeBarrier(t)
	h, _ := newHeader(b)
	raw := h.marshal()

	// Set wrappedLen to maxWrappedDEKSize+1 and adjust headerSize.
	oversized := uint32(maxWrappedDEKSize + 1)
	raw[offWrappedDEKLen] = byte(oversized)
	raw[offWrappedDEKLen+1] = byte(oversized >> 8)
	raw[offWrappedDEKLen+2] = byte(oversized >> 16)
	raw[offWrappedDEKLen+3] = byte(oversized >> 24)

	if _, err := readHeader(bytes.NewReader(raw), b); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("error = %v, want ErrInvalidHeader", err)
	}
}

// ---------------------------------------------------------------------------
// fs.go — openEncryptedRO: Stat error and large non-encfs passthrough
// ---------------------------------------------------------------------------

// TestFS_OpenEncryptedROStatError confirms that Open returns an error when
// the base file's Stat call fails inside openEncryptedRO.
func TestFS_OpenEncryptedROStatError(t *testing.T) {
	sentinel := errors.New("ro-stat-fail")
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	// Write a file large enough to pass the size check.
	{
		f, _ := mem.Create("rostat.dat", vfs.WriteCategoryUnspecified)
		_, _ = f.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
		_ = f.Close()
	}
	base := &openStatErrFS{real: mem, statErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.Open("rostat.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// openStatErrFS injects a statErr into files returned by Open (for
// the Stat call inside openEncryptedRO).
type openStatErrFS struct {
	real    vfs.FS
	statErr error
}

func (fs *openStatErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *openStatErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.real.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, statErr: fs.statErr}, nil
}
func (fs *openStatErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.OpenReadWrite(name, cat, opts...)
}
func (fs *openStatErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *openStatErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *openStatErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *openStatErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *openStatErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *openStatErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *openStatErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *openStatErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *openStatErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *openStatErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *openStatErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *openStatErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *openStatErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *openStatErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *openStatErrFS) Unwrap() vfs.FS { return nil }

// TestFS_OpenPassthroughLargeNonEncfs confirms that Open returns the base
// file directly (passthrough) when the file is >= fixedHeaderSize bytes
// but does not carry the QENC magic. This covers the non-magic passthrough
// in openEncryptedRO.
func TestFS_OpenPassthroughLargeNonEncfs(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Build the payload in a separate buffer from what we pass to Write, because
	// vfs.File.Write is permitted to modify the slice passed to it.
	const payloadLen = fixedHeaderSize + 100
	var writeBuf [payloadLen]byte
	for i := range writeBuf {
		writeBuf[i] = 0x50 // 'P'
	}
	expected := make([]byte, payloadLen)
	copy(expected, writeBuf[:])

	// Write a plain file larger than fixedHeaderSize with no QENC magic.
	plain, _ := mem.Create("large_plain.dat", vfs.WriteCategoryUnspecified)
	_, _ = plain.Write(writeBuf[:])
	_ = plain.Close()

	// Open through encfs should pass through because magic does not match.
	r, err := enc.Open("large_plain.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	buf := make([]byte, payloadLen)
	n, err := r.ReadAt(buf, 0)
	// Some ReadAt implementations return io.EOF on the last read.
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != payloadLen || !bytes.Equal(buf[:n], expected) {
		t.Errorf("passthrough content mismatch: got %d bytes, want %d", n, payloadLen)
	}
}

// ---------------------------------------------------------------------------
// fs.go — openEncryptedRW: ReadAt magic error and readHeader error
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteExistingReadAtError exercises the base.ReadAt error
// branch in openEncryptedRW by injecting a ReadAt failure on the file
// returned by the base FS's OpenReadWrite.
func TestFS_OpenReadWriteExistingReadAtError(t *testing.T) {
	sentinel := errors.New("orw-readat-fail")
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	// Write a valid encfs file.
	{
		enc := New(mem, b)
		f, _ := enc.Create("rwerr.dat", vfs.WriteCategoryUnspecified)
		_, _ = f.Write([]byte("data"))
		_ = f.Close()
	}
	// Now wrap with a FS whose OpenReadWrite returns an errFile with readAtErr.
	base := &orwReadAtErrFS{real: mem, readAtErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.OpenReadWrite("rwerr.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// orwReadAtErrFS injects a readAtErr into files returned by OpenReadWrite.
type orwReadAtErrFS struct {
	real      vfs.FS
	readAtErr error
}

func (fs *orwReadAtErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *orwReadAtErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.Open(name, opts...)
}
func (fs *orwReadAtErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.real.OpenReadWrite(name, cat, opts...)
	if err != nil {
		return nil, err
	}
	return &readAtErrFile{File: f, readAtErr: fs.readAtErr}, nil
}
func (fs *orwReadAtErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *orwReadAtErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *orwReadAtErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *orwReadAtErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *orwReadAtErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *orwReadAtErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *orwReadAtErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *orwReadAtErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *orwReadAtErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *orwReadAtErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *orwReadAtErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *orwReadAtErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *orwReadAtErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *orwReadAtErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *orwReadAtErrFS) Unwrap() vfs.FS { return nil }

// TestFS_OpenReadWriteExistingSealedReadHeader confirms that openEncryptedRW
// propagates a readHeader error (sealed barrier) when opening an existing file.
func TestFS_OpenReadWriteExistingSealedReadHeader(t *testing.T) {
	_, b, fs := newMemFS(t)
	// Create an encrypted file while unsealed.
	f, _ := fs.Create("rwhdr.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()
	// Seal the barrier so readHeader fails.
	b.seal()
	if _, err := fs.OpenReadWrite("rwhdr.dat", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("expected error opening existing RW file with sealed barrier")
	}
}

// ---------------------------------------------------------------------------
// fs.go — openEncryptedRW: tail ReadAt error and decrypt error on reload
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteTailReadAtError verifies that openEncryptedRW
// propagates an error when the tail-block ReadAt fails during reload.
// We create a file with a partial trailing block, then corrupt the
// underlying file so the tail ReadAt returns an error.
func TestFS_OpenReadWriteTailReadAtError(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write BlockSize + partial bytes so there is a tail block.
	f, _ := enc.Create("tailrderr.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write(make([]byte, BlockSize+50))
	_ = f.Close()

	// Wrap the FS so that OpenReadWrite returns a readAtErrFile that
	// will fail on ReadAt after the initial magic/header reads succeed.
	// We use a counter-based injection: the first two ReadAt calls
	// (magic + header) succeed; subsequent ones fail.
	sentinel := errors.New("tail-readat-fail")
	base := &nthReadAtErrFS{real: mem, failAfter: 3, err: sentinel}
	wrapped := New(base, b)
	_, gotErr := wrapped.OpenReadWrite("tailrderr.dat", vfs.WriteCategoryUnspecified)
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// TestFS_OpenReadWriteTailDecryptError verifies that openEncryptedRW
// propagates an error when the tail-block ciphertext fails to decrypt
// (authentication failure due to corruption).
func TestFS_OpenReadWriteTailDecryptError(t *testing.T) {
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a file with a partial tail.
	f, _ := enc.Create("taildecerr.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write(make([]byte, BlockSize+50))
	_ = f.Close()

	// Corrupt the tail block on disk (the second encrypted block).
	rawFile, _ := mem.OpenReadWrite("taildecerr.dat", vfs.WriteCategoryUnspecified)
	fi, _ := rawFile.Stat()
	raw := make([]byte, fi.Size())
	_, _ = rawFile.ReadAt(raw, 0)
	_ = rawFile.Close()

	// Find where the second block starts and flip a byte in its GCM body.
	headerSize := int(uint16(raw[offHeaderSize]) | uint16(raw[offHeaderSize+1])<<8)
	secondBlockOffset := headerSize + CiphertextBlockSize + inlineNonceSize + 5
	if secondBlockOffset < len(raw) {
		raw[secondBlockOffset] ^= 0xFF
	}
	corrupt, _ := mem.Create("taildecerr.dat", vfs.WriteCategoryUnspecified)
	_, _ = corrupt.Write(raw)
	_ = corrupt.Close()

	if _, err := enc.OpenReadWrite("taildecerr.dat", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("expected decrypt error on corrupted tail block")
	}
}

// nthReadAtErrFS wraps a MemFS and counts ReadAt calls; once failAfter
// calls have been made on any file, all subsequent ReadAt calls return err.
type nthReadAtErrFS struct {
	real      vfs.FS
	failAfter int
	err       error
}

func (fs *nthReadAtErrFS) Create(name string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.Create(name, cat)
}
func (fs *nthReadAtErrFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	return fs.real.Open(name, opts...)
}
func (fs *nthReadAtErrFS) OpenReadWrite(name string, cat vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := fs.real.OpenReadWrite(name, cat, opts...)
	if err != nil {
		return nil, err
	}
	return &nthReadAtErrFile{File: f, failAfter: fs.failAfter, err: fs.err}, nil
}
func (fs *nthReadAtErrFS) OpenDir(name string) (vfs.File, error) { return fs.real.OpenDir(name) }
func (fs *nthReadAtErrFS) Remove(name string) error              { return fs.real.Remove(name) }
func (fs *nthReadAtErrFS) RemoveAll(name string) error           { return fs.real.RemoveAll(name) }
func (fs *nthReadAtErrFS) Rename(old, new string) error          { return fs.real.Rename(old, new) }
func (fs *nthReadAtErrFS) ReuseForWrite(old, new string, cat vfs.DiskWriteCategory) (vfs.File, error) {
	return fs.real.ReuseForWrite(old, new, cat)
}
func (fs *nthReadAtErrFS) MkdirAll(dir string, perm os.FileMode) error {
	return fs.real.MkdirAll(dir, perm)
}
func (fs *nthReadAtErrFS) Lock(name string) (io.Closer, error)    { return fs.real.Lock(name) }
func (fs *nthReadAtErrFS) List(dir string) ([]string, error)      { return fs.real.List(dir) }
func (fs *nthReadAtErrFS) Stat(name string) (vfs.FileInfo, error) { return fs.real.Stat(name) }
func (fs *nthReadAtErrFS) Link(old, new string) error             { return fs.real.Link(old, new) }
func (fs *nthReadAtErrFS) PathBase(p string) string               { return fs.real.PathBase(p) }
func (fs *nthReadAtErrFS) PathJoin(elem ...string) string         { return fs.real.PathJoin(elem...) }
func (fs *nthReadAtErrFS) PathDir(p string) string                { return fs.real.PathDir(p) }
func (fs *nthReadAtErrFS) GetDiskUsage(p string) (vfs.DiskUsage, error) {
	return fs.real.GetDiskUsage(p)
}
func (fs *nthReadAtErrFS) Unwrap() vfs.FS { return nil }

// nthReadAtErrFile wraps vfs.File and injects an error after failAfter ReadAt calls.
type nthReadAtErrFile struct {
	vfs.File
	failAfter int
	calls     int
	err       error
}

func (f *nthReadAtErrFile) ReadAt(p []byte, off int64) (int, error) {
	f.calls++
	if f.calls > f.failAfter {
		return 0, f.err
	}
	return f.File.ReadAt(p, off)
}
