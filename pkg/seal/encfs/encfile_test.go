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
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// newMemFS returns a fresh in-memory base FS and a corresponding
// encfs.FS wrapper using a unique fakeBarrier.
func newMemFS(t *testing.T) (vfs.FS, *fakeBarrier, vfs.FS) {
	t.Helper()
	mem := vfs.NewMem()
	b := newFakeBarrier(t)
	return mem, b, New(mem, b)
}

func TestEncFile_WriteThenReadSequential(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, err := fs.Create("a.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pt := bytes.Repeat([]byte("hello "), 1500) // 9000 bytes -> 2 full blocks + 808 byte tail
	if _, err := f.Write(pt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := fs.Open("a.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	got := make([]byte, len(pt))
	if _, err := r.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Error("plaintext mismatch after write/read round trip")
	}
}

func TestEncFile_OnDiskHasNoPlaintext(t *testing.T) {
	mem, _, fs := newMemFS(t)
	pt := []byte("super secret payload that should never appear in ciphertext")
	f, err := fs.Create("secret.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write(pt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Read the raw bytes from the underlying base FS and confirm the
	// plaintext does not appear.
	raw, err := mem.Open("secret.dat")
	if err != nil {
		t.Fatalf("base Open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	fi, err := raw.Stat()
	if err != nil {
		t.Fatalf("base Stat: %v", err)
	}
	buf := make([]byte, fi.Size())
	if _, err := raw.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("base ReadAt: %v", err)
	}
	if bytes.Contains(buf, pt) {
		t.Error("plaintext leaked into on-disk ciphertext")
	}
	if string(buf[:lenMagic]) != headerMagic {
		t.Errorf("on-disk magic = %q, want %q", string(buf[:lenMagic]), headerMagic)
	}
}

func TestEncFile_RandomReadAtBlockBoundaries(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("blob.dat", vfs.WriteCategoryUnspecified)
	pt := make([]byte, 5*BlockSize+123)
	if _, err := rand.Read(pt); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := f.Write(pt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := fs.Open("blob.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	cases := []struct {
		off int64
		n   int
	}{
		{0, 1},
		{0, BlockSize},
		{1, BlockSize - 1},
		{BlockSize, BlockSize},
		{BlockSize - 5, 10},
		{2*BlockSize + 100, 200},
		{int64(len(pt)) - 50, 50},
	}
	for _, c := range cases {
		buf := make([]byte, c.n)
		if _, err := r.ReadAt(buf, c.off); err != nil {
			t.Fatalf("ReadAt(%d, %d): %v", c.off, c.n, err)
		}
		if !bytes.Equal(buf, pt[c.off:c.off+int64(c.n)]) {
			t.Errorf("ReadAt(%d, %d) returned wrong bytes", c.off, c.n)
		}
	}
}

func TestEncFile_ReadEOF(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("e.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()
	r, _ := fs.Open("e.dat")
	defer func() { _ = r.Close() }()
	got := make([]byte, 50)
	n, err := r.ReadAt(got, 0)
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt past end err = %v, want io.EOF", err)
	}
	if n != 10 {
		t.Errorf("ReadAt past end n = %d, want 10", n)
	}
	if _, err := r.ReadAt(got[:1], 100); !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt beyond size err = %v, want io.EOF", err)
	}
}

func TestEncFile_SequentialReadAdvancesCursor(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("s.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()
	r, _ := fs.Open("s.dat")
	defer func() { _ = r.Close() }()
	a := make([]byte, 4)
	if _, err := r.Read(a); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(a) != "abcd" {
		t.Errorf("first read = %q, want abcd", a)
	}
	b := make([]byte, 4)
	if _, err := r.Read(b); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(b) != "efgh" {
		t.Errorf("second read = %q, want efgh", b)
	}
}

func TestEncFile_StatReportsLogicalSize(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("x.dat", vfs.WriteCategoryUnspecified)
	const N = 12345
	if _, err := f.Write(make([]byte, N)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()
	r, _ := fs.Open("x.dat")
	defer func() { _ = r.Close() }()
	fi, err := r.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != int64(N) {
		t.Errorf("logical size = %d, want %d", fi.Size(), N)
	}
}

func TestEncFile_FSStatReportsLogicalSize(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("y.dat", vfs.WriteCategoryUnspecified)
	const N = 9999
	_, _ = f.Write(make([]byte, N))
	_ = f.Close()
	fi, err := fs.Stat("y.dat")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != int64(N) {
		t.Errorf("FS Stat size = %d, want %d", fi.Size(), N)
	}
}

func TestEncFile_FSStatPassthroughForNonEncrypted(t *testing.T) {
	mem, _, fs := newMemFS(t)
	// Write a plain (non-encrypted) file directly to the base FS.
	f, err := mem.Create("plain.txt", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("base Create: %v", err)
	}
	if _, err := f.Write([]byte("hello plain world")); err != nil {
		t.Fatalf("base Write: %v", err)
	}
	_ = f.Close()
	fi, err := fs.Stat("plain.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != int64(len("hello plain world")) {
		t.Errorf("size = %d, want %d", fi.Size(), len("hello plain world"))
	}
}

func TestEncFile_OpenPassthroughForNonEncrypted(t *testing.T) {
	mem, _, fs := newMemFS(t)
	const payload = "plain bytes, no encryption"
	f, _ := mem.Create("p.txt", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte(payload))
	_ = f.Close()
	r, err := fs.Open("p.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	buf := make([]byte, len(payload))
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != payload {
		t.Errorf("passthrough content = %q, want %q", buf, payload)
	}
}

func TestEncFile_BarrierSealedRejectsCreate(t *testing.T) {
	_, b, fs := newMemFS(t)
	b.seal()
	if _, err := fs.Create("z.dat", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("Create on sealed barrier must fail")
	}
}

func TestEncFile_BarrierSealedRejectsOpen(t *testing.T) {
	_, b, fs := newMemFS(t)
	f, _ := fs.Create("z.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("x"))
	_ = f.Close()
	b.seal()
	if _, err := fs.Open("z.dat"); err == nil {
		t.Error("Open on sealed barrier must fail")
	}
}

func TestEncFile_UnsealRestoresOps(t *testing.T) {
	_, b, fs := newMemFS(t)
	f, _ := fs.Create("u.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("data"))
	_ = f.Close()
	b.seal()
	if _, err := fs.Open("u.dat"); err == nil {
		t.Error("expected sealed open to fail")
	}
	b.unseal()
	r, err := fs.Open("u.dat")
	if err != nil {
		t.Fatalf("Open after unseal: %v", err)
	}
	defer func() { _ = r.Close() }()
	buf := make([]byte, 4)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "data" {
		t.Errorf("payload = %q, want data", buf)
	}
}

func TestEncFile_SyncFlushesPartialThenExtension(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("p.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.Write([]byte("first")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := f.Write([]byte(" second")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, _ := fs.Open("p.dat")
	defer func() { _ = r.Close() }()
	buf := make([]byte, len("first second"))
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "first second" {
		t.Errorf("got %q, want %q", buf, "first second")
	}
}

func TestEncFile_ManyBlocksRoundTrip(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("big.dat", vfs.WriteCategoryUnspecified)
	pt := make([]byte, 8*BlockSize)
	if _, err := rand.Read(pt); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := f.Write(pt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, _ := fs.Open("big.dat")
	defer func() { _ = r.Close() }()
	got := make([]byte, len(pt))
	if _, err := r.ReadAt(got, 0); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Error("multi-block round trip mismatch")
	}
}

func TestEncFile_ZeroLengthFile(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("zero.dat", vfs.WriteCategoryUnspecified)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := fs.Open("zero.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	fi, err := r.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("size = %d, want 0", fi.Size())
	}
	buf := make([]byte, 1)
	if _, err := r.ReadAt(buf, 0); !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt empty err = %v, want io.EOF", err)
	}
}

func TestEncFile_ReadOnlyRejectsWrite(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("ro.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("payload"))
	_ = f.Close()
	r, _ := fs.Open("ro.dat")
	defer func() { _ = r.Close() }()
	if _, err := r.Write([]byte("x")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Write on RO err = %v, want ErrReadOnly", err)
	}
	if _, err := r.WriteAt([]byte("x"), 0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteAt on RO err = %v, want ErrReadOnly", err)
	}
}

func TestEncFile_WriteAtAtEndAppends(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("at.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.WriteAt([]byte("alpha"), 0); err != nil {
		t.Fatalf("WriteAt 0: %v", err)
	}
	if _, err := f.WriteAt([]byte("beta"), 5); err != nil {
		t.Fatalf("WriteAt 5: %v", err)
	}
	_ = f.Close()
	r, _ := fs.Open("at.dat")
	defer func() { _ = r.Close() }()
	buf := make([]byte, 9)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "alphabeta" {
		t.Errorf("got %q, want alphabeta", buf)
	}
}

func TestEncFile_WriteAtMidFileFails(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("mw.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.Write([]byte("0123456789")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := f.WriteAt([]byte("X"), 3); !errors.Is(err, ErrWriteAtUnsupported) {
		t.Errorf("err = %v, want ErrWriteAtUnsupported", err)
	}
	_ = f.Close()
}

func TestEncFile_CloseIsIdempotent(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("c.dat", vfs.WriteCategoryUnspecified)
	if err := f.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close 2: %v", err)
	}
}

func TestEncFile_OperationsAfterCloseFail(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("dead.dat", vfs.WriteCategoryUnspecified)
	_ = f.Close()
	if _, err := f.Write([]byte("x")); !errors.Is(err, ErrFileClosed) {
		t.Errorf("Write after close err = %v, want ErrFileClosed", err)
	}
	if _, err := f.WriteAt([]byte("x"), 0); !errors.Is(err, ErrFileClosed) {
		t.Errorf("WriteAt after close err = %v, want ErrFileClosed", err)
	}
	if _, err := f.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrFileClosed) {
		t.Errorf("ReadAt after close err = %v, want ErrFileClosed", err)
	}
	if err := f.Sync(); !errors.Is(err, ErrFileClosed) {
		t.Errorf("Sync after close err = %v, want ErrFileClosed", err)
	}
}

func TestEncFile_FdReturnsInvalid(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("fd.dat", vfs.WriteCategoryUnspecified)
	defer func() { _ = f.Close() }()
	if f.Fd() != vfs.InvalidFd {
		t.Errorf("Fd = %d, want InvalidFd", f.Fd())
	}
}

func TestEncFile_NegativeOffsetRejected(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("n.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("payload"))
	_ = f.Close()
	r, _ := fs.Open("n.dat")
	defer func() { _ = r.Close() }()
	if _, err := r.ReadAt(make([]byte, 1), -1); err == nil {
		t.Error("expected error on negative offset")
	}
}

func TestEncFile_OpenReadWriteCreatesNew(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, err := fs.OpenReadWrite("rw.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	if _, err := f.Write([]byte("created via RW")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, _ := fs.Open("rw.dat")
	defer func() { _ = r.Close() }()
	buf := make([]byte, len("created via RW"))
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "created via RW" {
		t.Errorf("got %q", buf)
	}
}

func TestEncFile_OpenReadWriteAppendsExisting(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("rw2.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("alpha"))
	_ = f.Close()
	g, err := fs.OpenReadWrite("rw2.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("OpenReadWrite existing: %v", err)
	}
	if _, err := g.Write([]byte("beta")); err != nil {
		t.Fatalf("Write append: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, _ := fs.Open("rw2.dat")
	defer func() { _ = r.Close() }()
	buf := make([]byte, 9)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "alphabeta" {
		t.Errorf("got %q", buf)
	}
}

func TestEncFile_OpenReadWriteRejectsNonEncrypted(t *testing.T) {
	mem, _, fs := newMemFS(t)
	f, _ := mem.Create("plain.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("plain"))
	_ = f.Close()
	if _, err := fs.OpenReadWrite("plain.dat", vfs.WriteCategoryUnspecified); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("err = %v, want ErrInvalidHeader", err)
	}
}

func TestEncFile_RenameAndRemove(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("a.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("x"))
	_ = f.Close()
	if err := fs.Rename("a.dat", "b.dat"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := fs.Stat("a.dat"); err == nil {
		t.Error("Stat on old name must fail after rename")
	}
	if _, err := fs.Stat("b.dat"); err != nil {
		t.Fatalf("Stat new name: %v", err)
	}
	if err := fs.Remove("b.dat"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := fs.Stat("b.dat"); err == nil {
		t.Error("Stat on removed name must fail")
	}
}

func TestEncFile_MkdirAllListGetUsage(t *testing.T) {
	_, _, fs := newMemFS(t)
	if err := fs.MkdirAll(filepath.Join("d", "e"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, _ := fs.Create(filepath.Join("d", "e", "x.dat"), vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("z"))
	_ = f.Close()
	names, err := fs.List(filepath.Join("d", "e"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, n := range names {
		if n == "x.dat" {
			found = true
		}
	}
	if !found {
		t.Errorf("List did not include x.dat: %v", names)
	}
	if _, err := fs.GetDiskUsage("d"); err != nil {
		// vfs.MemFS may not implement disk usage; tolerate either.
		t.Logf("GetDiskUsage on memfs: %v", err)
	}
}

func TestEncFile_PathHelpers(t *testing.T) {
	_, _, fs := newMemFS(t)
	if got := fs.PathBase("/a/b.txt"); got != "b.txt" {
		t.Errorf("PathBase = %q", got)
	}
	if got := fs.PathDir("/a/b.txt"); got != "/a" {
		t.Errorf("PathDir = %q", got)
	}
	if got := fs.PathJoin("a", "b"); got != filepath.Join("a", "b") {
		t.Errorf("PathJoin = %q", got)
	}
}

func TestEncFile_UnwrapReturnsNil(t *testing.T) {
	_, _, fs := newMemFS(t)
	if got := fs.(*FS).Unwrap(); got != nil {
		t.Errorf("Unwrap = %v, want nil", got)
	}
}

func TestEncFile_NewWithNilBaseUsesDefault(t *testing.T) {
	if New(nil, newFakeBarrier(t)) == nil {
		t.Error("New(nil, barrier) returned nil")
	}
}

func TestEncFile_NewWithNilBarrierReturnsNil(t *testing.T) {
	if got := New(vfs.NewMem(), nil); got != nil {
		t.Errorf("New(_, nil) = %v, want nil", got)
	}
}

func TestEncFile_StatNonExistentPassesThroughError(t *testing.T) {
	_, _, fs := newMemFS(t)
	if _, err := fs.Stat("nope.dat"); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat missing err = %v, want os.ErrNotExist", err)
	}
}

func TestEncFile_OpenNonExistentReturnsError(t *testing.T) {
	_, _, fs := newMemFS(t)
	if _, err := fs.Open("ghost.dat"); err == nil {
		t.Error("Open of missing file must fail")
	}
}

func TestFS_Link(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("orig.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("payload"))
	_ = f.Close()
	if err := fs.Link("orig.dat", "linked.dat"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if _, err := fs.Stat("linked.dat"); err != nil {
		t.Errorf("Stat linked: %v", err)
	}
}

func TestFS_OpenDir(t *testing.T) {
	_, _, fs := newMemFS(t)
	if err := fs.MkdirAll("subdir", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := fs.OpenDir("subdir")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = d.Close() }()
}

func TestFS_RemoveAll(t *testing.T) {
	_, _, fs := newMemFS(t)
	if err := fs.MkdirAll("rm/sub", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, _ := fs.Create("rm/sub/x.dat", vfs.WriteCategoryUnspecified)
	_ = f.Close()
	if err := fs.RemoveAll("rm"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := fs.Stat("rm/sub/x.dat"); err == nil {
		t.Error("Stat after RemoveAll must fail")
	}
}

func TestFS_ReuseForWrite(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("old.dat", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte("stale"))
	_ = f.Close()
	g, err := fs.ReuseForWrite("old.dat", "new.dat", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("ReuseForWrite: %v", err)
	}
	if _, err := g.Write([]byte("fresh")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, _ := fs.Open("new.dat")
	defer func() { _ = r.Close() }()
	buf := make([]byte, 5)
	if _, err := r.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "fresh" {
		t.Errorf("got %q, want fresh", buf)
	}
	if _, err := fs.Stat("old.dat"); err == nil {
		t.Error("ReuseForWrite must remove the old name")
	}
}

func TestFS_Lock(t *testing.T) {
	_, _, fs := newMemFS(t)
	c, err := fs.Lock("LOCK")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Lock Close: %v", err)
	}
}

func TestEncFile_SyncDataAndSyncTo(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("sync.dat", vfs.WriteCategoryUnspecified)
	if _, err := f.Write([]byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.SyncData(); err != nil {
		t.Errorf("SyncData: %v", err)
	}
	full, err := f.SyncTo(3)
	if err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	if !full {
		t.Error("SyncTo expected to return fullSync=true")
	}
	_ = f.Close()
}

func TestEncFile_PreallocateAndPrefetch(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("hint.dat", vfs.WriteCategoryUnspecified)
	defer func() { _ = f.Close() }()
	// MemFS does not implement Preallocate/Prefetch meaningfully; just
	// confirm the calls do not panic and surface the underlying error
	// path (or nil) cleanly.
	_ = f.Preallocate(0, 4096)
	_ = f.Prefetch(0, 4096)
}

func TestFS_PassthroughForFileSmallerThanHeader(t *testing.T) {
	mem, _, fs := newMemFS(t)
	f, _ := mem.Create("tiny", vfs.WriteCategoryUnspecified)
	_, _ = f.Write([]byte{0x01})
	_ = f.Close()
	fi, err := fs.Stat("tiny")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != 1 {
		t.Errorf("size = %d, want 1", fi.Size())
	}
}

func TestPhysicalToLogicalSize(t *testing.T) {
	cases := []struct {
		payload int64
		want    int64
		wantErr bool
	}{
		{0, 0, false},
		{int64(BlockOverhead), 0, false}, // empty trailing block
		{int64(BlockOverhead) + 1, 1, false},
		{int64(BlockOverhead) + int64(BlockSize), int64(BlockSize), false},
		{int64(CiphertextBlockSize), int64(BlockSize), false},
		{int64(CiphertextBlockSize) + int64(BlockOverhead) + 100, int64(BlockSize) + 100, false},
		{-1, 0, true},
		{int64(BlockOverhead) - 1, 0, true},
	}
	for _, c := range cases {
		got, err := physicalToLogicalSize(c.payload)
		if (err != nil) != c.wantErr {
			t.Errorf("payload=%d err = %v, wantErr=%v", c.payload, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("payload=%d got=%d want=%d", c.payload, got, c.want)
		}
	}
}

// TestPhysicalToLogicalSize_TypedErrors pins each invalid framing to its
// typed error and a message that names the offending length.
func TestPhysicalToLogicalSize_TypedErrors(t *testing.T) {
	_, err := physicalToLogicalSize(-7)
	var neg *NegativePayloadLengthError
	if !errors.As(err, &neg) || neg.Length != -7 {
		t.Fatalf("payload=-7 err = %v, want *NegativePayloadLengthError{Length: -7}", err)
	}
	if want := "encfs: payload length is negative (-7)"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	short := int64(BlockOverhead) - 1
	_, err = physicalToLogicalSize(int64(CiphertextBlockSize) + short)
	var trailing *TrailingBlockTooShortError
	if !errors.As(err, &trailing) || trailing.Bytes != short {
		t.Fatalf("short trailing block err = %v, want *TrailingBlockTooShortError{Bytes: %d}", err, short)
	}
	if want := fmt.Sprintf("encfs: trailing block too short (%d bytes)", short); err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestFlushBlockSizeError_Message checks the refusal flushFullBlockLocked
// returns for a mis-sized tail names both sizes.
func TestFlushBlockSizeError_Message(t *testing.T) {
	err := error(&FlushBlockSizeError{Expected: BlockSize, Got: BlockSize - 1})
	want := fmt.Sprintf("encfs: flushFullBlock expected %d bytes, got %d", BlockSize, BlockSize-1)
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
