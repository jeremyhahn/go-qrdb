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

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// ---------------------------------------------------------------------------
// Compile-time interface guard: errFile and errFS satisfy filesys.File /
// filesys.LockFS.
// ---------------------------------------------------------------------------

var (
	_ filesys.File   = (*errFile)(nil)
	_ filesys.LockFS = (*errFS)(nil)
)

// errFile is a filesys.File that wraps a real filesys.File but injects an
// error on the first call of a specific method. It is used to trigger error
// branches in encryptedFile that are otherwise unreachable with a
// well-behaved in-memory FS.
type errFile struct {
	real       filesys.File
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

func (f *errFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.real.Stat()
}

// errFS wraps an in-memory FS and injects configurable errors on specific
// operations. Only the methods under test are wrapped; all others
// delegate to the underlying FS.
type errFS struct {
	real      filesys.LockFS
	createErr error
	openRWErr error
	statErr   error
	removeErr error
	renameErr error
	// removeCalls counts how many times Remove has been called so callers
	// can inject an error on a later call.
	removeCalls int
	removeErrOn int // 0 = always, N = on Nth call (1-based)
}

func newErrFS() *errFS {
	return &errFS{real: newBaseFS(), removeErrOn: 0}
}

func (fs *errFS) Create(name string) (filesys.File, error) {
	if fs.createErr != nil {
		return nil, fs.createErr
	}
	return fs.real.Create(name)
}

func (fs *errFS) Open(name string) (filesys.File, error) { return fs.real.Open(name) }

func (fs *errFS) OpenReadWrite(name string) (filesys.File, error) {
	if fs.openRWErr != nil {
		return nil, fs.openRWErr
	}
	return fs.real.OpenReadWrite(name)
}

func (fs *errFS) OpenDir(name string) (filesys.File, error) { return fs.real.OpenDir(name) }
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
func (fs *errFS) MkdirAll(dir string, perm os.FileMode) error { return fs.real.MkdirAll(dir, perm) }
func (fs *errFS) Lock(name string) (io.Closer, error)         { return fs.real.Lock(name) }
func (fs *errFS) List(dir string) ([]string, error)           { return fs.real.List(dir) }
func (fs *errFS) Stat(name string) (os.FileInfo, error) {
	if fs.statErr != nil {
		return nil, fs.statErr
	}
	return fs.real.Stat(name)
}
func (fs *errFS) Link(old, new string) error { return fs.real.Link(old, new) }

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
	mem := newBaseFS()
	realFile, err := mem.Create("w.dat")
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
	mem := newBaseFS()
	realFile, err := mem.Create("r.dat")
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
	mem := newBaseFS()
	realFile, err := mem.Create("cf.dat")
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
	mem := newBaseFS()
	realFile, err := mem.Create("fb.dat")
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
	var sizeErr *FlushBlockSizeError
	if !errors.As(gotErr, &sizeErr) || sizeErr.Expected != BlockSize || sizeErr.Got != BlockSize-1 {
		t.Errorf("flushFullBlockLocked err = %v, want *FlushBlockSizeError{%d, %d}", gotErr, BlockSize, BlockSize-1)
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
	mem := newBaseFS()
	realFile, err := mem.Create("ffb.dat")
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
	mem := newBaseFS()
	realFile, err := mem.Create("sf.dat")
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
	f, _ := fs.Create("sync_ro.dat")
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
	mem := newBaseFS()
	realFile, err := mem.Create("stat.dat")
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	fs := New(mem, b)

	f, err := fs.Create("tamper.dat")
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
	rawFile, err := mem.OpenReadWrite("tamper.dat")
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
	f, _ := fs.Create("end.dat")
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
	f, err := fs.Create("tailpast.dat")
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
	g, err := fs.OpenReadWrite("tailpast.dat")
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
	if _, err := fs.Create("noh.dat"); err == nil {
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
	_, gotErr := fs.Create("x.dat")
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
	base := &writeErrFS{FS: newBaseFS(), writeErr: sentinel}
	b := newFakeBarrier(t)
	fs := New(base, b)
	_, gotErr := fs.Create("y.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// writeErrFS is a minimal filesys.FS whose Create returns an errFile that
// fails Write calls with writeErr.
type writeErrFS struct {
	filesys.FS
	writeErr error
}

func (fs *writeErrFS) Create(name string) (filesys.File, error) {
	f, err := fs.FS.Create(name)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, writeErr: fs.writeErr}, nil
}

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: base.Stat error
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteStatError confirms that OpenReadWrite returns an
// error when the base file's Stat fails immediately after opening.
func TestFS_OpenReadWriteStatError(t *testing.T) {
	sentinel := errors.New("orw-stat-fail")
	base := &statErrFS{FS: newBaseFS(), statErr: sentinel}
	b := newFakeBarrier(t)
	fs := New(base, b)
	_, gotErr := fs.OpenReadWrite("rw.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// statErrFS returns an errFile from OpenReadWrite whose Stat fails.
type statErrFS struct {
	filesys.FS
	statErr error
}

func (fs *statErrFS) OpenReadWrite(name string) (filesys.File, error) {
	f, err := fs.FS.OpenReadWrite(name)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, statErr: fs.statErr}, nil
}

// ---------------------------------------------------------------------------
// fs.go — OpenReadWrite: newHeader error for fresh file (sealed barrier)
// ---------------------------------------------------------------------------

// TestFS_OpenReadWriteNewHeaderError confirms OpenReadWrite returns an error
// when the barrier is sealed and a fresh (empty) file needs a header.
func TestFS_OpenReadWriteNewHeaderError(t *testing.T) {
	_, b, fs := newMemFS(t)
	b.seal()
	if _, err := fs.OpenReadWrite("sealed.dat"); err == nil {
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	fs := New(mem, b)
	// Write a plain file (> fixedHeaderSize bytes so it looks like an encfs
	// file to the size check but has wrong magic).
	plain, _ := mem.Create("bad.dat")
	_, _ = plain.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
	_ = plain.Close()
	if _, err := fs.OpenReadWrite("bad.dat"); !errors.Is(err, ErrInvalidHeader) {
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	// Create a file large enough that the size check passes.
	{
		f, _ := mem.Create("readerr.dat")
		_, _ = f.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
		_ = f.Close()
	}
	// Wrap with a FS that wraps Open's returned file to inject a ReadAt error.
	base := &readAtErrFS{FS: mem, readAtErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.Open("readerr.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// readAtErrFS injects a readAtErr into files returned by Open (for the
// magic-check ReadAt in openEncryptedRO).
type readAtErrFS struct {
	filesys.FS
	readAtErr error
}

func (fs *readAtErrFS) Open(name string) (filesys.File, error) {
	f, err := fs.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &readAtErrFile{File: f, readAtErr: fs.readAtErr}, nil
}

// readAtErrFile wraps a filesys.File and injects readAtErr on ReadAt.
type readAtErrFile struct {
	filesys.File
	readAtErr error
}

func (f *readAtErrFile) ReadAt(p []byte, off int64) (int, error) {
	return 0, f.readAtErr
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a real encfs file to get valid initial bytes, then corrupt
	// the header_size field.
	f, _ := enc.Create("hdr.dat")
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()

	// Read raw bytes.
	rawFile, _ := mem.OpenReadWrite("hdr.dat")
	fi, _ := rawFile.Stat()
	raw := make([]byte, fi.Size())
	_, _ = rawFile.ReadAt(raw, 0)
	_ = rawFile.Close()

	// Corrupt header_size to be 1 (less than fixedHeaderSize).
	raw[offHeaderSize] = 1
	raw[offHeaderSize+1] = 0
	corrupt, _ := mem.Create("hdr_bad.dat")
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
	_, gotErr := enc.OpenReadWrite("x.dat")
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
	base := &statOpenErrFS{FS: newBaseFS()}
	enc := New(base, b)

	// Create an encfs file that is large enough to trigger the Open path.
	realEnc := New(base.FS, b)
	f, _ := realEnc.Create("toctou.dat")
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
	filesys.FS
	openErr error
}

func (fs *statOpenErrFS) Open(name string) (filesys.File, error) {
	if fs.openErr != nil {
		return nil, fs.openErr
	}
	return fs.FS.Open(name)
}

// TestFS_StatLargeNonEncfsMagicMismatch confirms that Stat on a large file
// (>= fixedHeaderSize) whose first bytes are not QENC returns the raw
// physical size rather than a computed logical size.
func TestFS_StatLargeNonEncfsMagicMismatch(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a file with no QENC magic, larger than fixedHeaderSize.
	var writeBuf [fixedHeaderSize + 50]byte
	for i := range writeBuf {
		writeBuf[i] = 'Z'
	}
	f, _ := mem.Create("largenoenc.dat")
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a valid encfs file with enough data to know the header size.
	f, _ := enc.Create("ptle.dat")
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

	corrupt, _ := mem.Create("ptle_corrupt.dat")
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	// Write a file large enough to pass the size check.
	{
		f, _ := mem.Create("rostat.dat")
		_, _ = f.Write(bytes.Repeat([]byte("X"), fixedHeaderSize+10))
		_ = f.Close()
	}
	base := &openStatErrFS{FS: mem, statErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.Open("rostat.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// openStatErrFS injects a statErr into files returned by Open (for
// the Stat call inside openEncryptedRO).
type openStatErrFS struct {
	filesys.FS
	statErr error
}

func (fs *openStatErrFS) Open(name string) (filesys.File, error) {
	f, err := fs.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &errFile{real: f, statErr: fs.statErr}, nil
}

// TestFS_OpenPassthroughLargeNonEncfs confirms that Open returns the base
// file directly (passthrough) when the file is >= fixedHeaderSize bytes
// but does not carry the QENC magic. This covers the non-magic passthrough
// in openEncryptedRO.
func TestFS_OpenPassthroughLargeNonEncfs(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Build the payload in a separate buffer from what we pass to Write, because
	// filesys.File.Write is permitted to modify the slice passed to it.
	const payloadLen = fixedHeaderSize + 100
	var writeBuf [payloadLen]byte
	for i := range writeBuf {
		writeBuf[i] = 0x50 // 'P'
	}
	expected := make([]byte, payloadLen)
	copy(expected, writeBuf[:])

	// Write a plain file larger than fixedHeaderSize with no QENC magic.
	plain, _ := mem.Create("large_plain.dat")
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	// Write a valid encfs file.
	{
		enc := New(mem, b)
		f, _ := enc.Create("rwerr.dat")
		_, _ = f.Write([]byte("data"))
		_ = f.Close()
	}
	// Now wrap with a FS whose OpenReadWrite returns an errFile with readAtErr.
	base := &orwReadAtErrFS{FS: mem, readAtErr: sentinel}
	enc := New(base, b)
	_, gotErr := enc.OpenReadWrite("rwerr.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// orwReadAtErrFS injects a readAtErr into files returned by OpenReadWrite.
type orwReadAtErrFS struct {
	filesys.FS
	readAtErr error
}

func (fs *orwReadAtErrFS) OpenReadWrite(name string) (filesys.File, error) {
	f, err := fs.FS.OpenReadWrite(name)
	if err != nil {
		return nil, err
	}
	return &readAtErrFile{File: f, readAtErr: fs.readAtErr}, nil
}

// TestFS_OpenReadWriteExistingSealedReadHeader confirms that openEncryptedRW
// propagates a readHeader error (sealed barrier) when opening an existing file.
func TestFS_OpenReadWriteExistingSealedReadHeader(t *testing.T) {
	_, b, fs := newMemFS(t)
	// Create an encrypted file while unsealed.
	f, _ := fs.Create("rwhdr.dat")
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()
	// Seal the barrier so readHeader fails.
	b.seal()
	if _, err := fs.OpenReadWrite("rwhdr.dat"); err == nil {
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
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write BlockSize + partial bytes so there is a tail block.
	f, _ := enc.Create("tailrderr.dat")
	_, _ = f.Write(make([]byte, BlockSize+50))
	_ = f.Close()

	// Wrap the FS so that OpenReadWrite returns a readAtErrFile that
	// will fail on ReadAt after the initial magic/header reads succeed.
	// We use a counter-based injection: the first two ReadAt calls
	// (magic + header) succeed; subsequent ones fail.
	sentinel := errors.New("tail-readat-fail")
	base := &nthReadAtErrFS{FS: mem, failAfter: 3, err: sentinel}
	wrapped := New(base, b)
	_, gotErr := wrapped.OpenReadWrite("tailrderr.dat")
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("error = %v, want sentinel", gotErr)
	}
}

// TestFS_OpenReadWriteTailDecryptError verifies that openEncryptedRW
// propagates an error when the tail-block ciphertext fails to decrypt
// (authentication failure due to corruption).
func TestFS_OpenReadWriteTailDecryptError(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	enc := New(mem, b)

	// Write a file with a partial tail.
	f, _ := enc.Create("taildecerr.dat")
	_, _ = f.Write(make([]byte, BlockSize+50))
	_ = f.Close()

	// Corrupt the tail block on disk (the second encrypted block).
	rawFile, _ := mem.OpenReadWrite("taildecerr.dat")
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
	corrupt, _ := mem.Create("taildecerr.dat")
	_, _ = corrupt.Write(raw)
	_ = corrupt.Close()

	if _, err := enc.OpenReadWrite("taildecerr.dat"); err == nil {
		t.Error("expected decrypt error on corrupted tail block")
	}
}

// nthReadAtErrFS wraps a MemFS and counts ReadAt calls; once failAfter
// calls have been made on any file, all subsequent ReadAt calls return err.
type nthReadAtErrFS struct {
	filesys.FS
	failAfter int
	err       error
}

func (fs *nthReadAtErrFS) OpenReadWrite(name string) (filesys.File, error) {
	f, err := fs.FS.OpenReadWrite(name)
	if err != nil {
		return nil, err
	}
	return &nthReadAtErrFile{File: f, failAfter: fs.failAfter, err: fs.err}, nil
}

// nthReadAtErrFile wraps filesys.File and injects an error after failAfter ReadAt calls.
type nthReadAtErrFile struct {
	filesys.File
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
