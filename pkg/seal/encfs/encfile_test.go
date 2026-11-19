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
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// newMemFS returns a fresh in-memory base FS and a corresponding
// encfs.FS wrapper using a unique fakeBarrier.
func newMemFS(t *testing.T) (filesys.LockFS, *fakeBarrier, filesys.FS) {
	t.Helper()
	mem := newBaseFS()
	b := newFakeBarrier(t)
	return mem, b, New(mem, b)
}

func TestEncFile_WriteThenReadSequential(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, err := fs.Create("a.dat")
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
	f, err := fs.Create("secret.dat")
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
	f, _ := fs.Create("blob.dat")
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
	f, _ := fs.Create("e.dat")
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
	f, _ := fs.Create("s.dat")
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
	f, _ := fs.Create("x.dat")
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
	f, _ := fs.Create("y.dat")
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
	f, err := mem.Create("plain.txt")
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
	f, _ := mem.Create("p.txt")
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
	if _, err := fs.Create("z.dat"); err == nil {
		t.Error("Create on sealed barrier must fail")
	}
}

func TestEncFile_BarrierSealedRejectsOpen(t *testing.T) {
	_, b, fs := newMemFS(t)
	f, _ := fs.Create("z.dat")
	_, _ = f.Write([]byte("x"))
	_ = f.Close()
	b.seal()
	if _, err := fs.Open("z.dat"); err == nil {
		t.Error("Open on sealed barrier must fail")
	}
}

func TestEncFile_UnsealRestoresOps(t *testing.T) {
	_, b, fs := newMemFS(t)
	f, _ := fs.Create("u.dat")
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
	f, _ := fs.Create("p.dat")
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
	f, _ := fs.Create("big.dat")
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
	f, _ := fs.Create("zero.dat")
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
	f, _ := fs.Create("ro.dat")
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
	f, _ := fs.Create("at.dat")
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
	f, _ := fs.Create("mw.dat")
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
	f, _ := fs.Create("c.dat")
	if err := f.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close 2: %v", err)
	}
}

func TestEncFile_OperationsAfterCloseFail(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("dead.dat")
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

func TestEncFile_NegativeOffsetRejected(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("n.dat")
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
	f, err := fs.OpenReadWrite("rw.dat")
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
	f, _ := fs.Create("rw2.dat")
	_, _ = f.Write([]byte("alpha"))
	_ = f.Close()
	g, err := fs.OpenReadWrite("rw2.dat")
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
	f, _ := mem.Create("plain.dat")
	_, _ = f.Write([]byte("plain"))
	_ = f.Close()
	if _, err := fs.OpenReadWrite("plain.dat"); !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("err = %v, want ErrInvalidHeader", err)
	}
}

func TestEncFile_RenameAndRemove(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("a.dat")
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
	f, _ := fs.Create(filepath.Join("d", "e", "x.dat"))
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
}

// diskUsageFS is a base FS that reports a fixed disk usage.
type diskUsageFS struct {
	filesys.FS
	usage filesys.DiskUsage
}

func (d diskUsageFS) DiskUsage(string) (filesys.DiskUsage, error) { return d.usage, nil }

func TestFS_DiskUsage(t *testing.T) {
	want := filesys.DiskUsage{AvailBytes: 1, TotalBytes: 3, UsedBytes: 2}
	fs := New(diskUsageFS{FS: newBaseFS(), usage: want}, newFakeBarrier(t))
	got, err := fs.(filesys.DiskUsageFS).DiskUsage("d")
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if got != want {
		t.Errorf("DiskUsage = %+v, want the base's %+v", got, want)
	}
}

func TestFS_DiskUsageUnsupportedBase(t *testing.T) {
	fs := New(struct{ filesys.FS }{newBaseFS()}, newFakeBarrier(t))
	_, err := fs.(filesys.DiskUsageFS).DiskUsage("d")
	var ue *filesys.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("DiskUsage on a base without disk usage: err = %v, want UnsupportedError", err)
	}
}

func TestEncFile_NewWithNilBaseUsesDefault(t *testing.T) {
	if New(nil, newFakeBarrier(t)) == nil {
		t.Error("New(nil, barrier) returned nil")
	}
}

func TestEncFile_NewWithNilBarrierReturnsNil(t *testing.T) {
	if got := New(newBaseFS(), nil); got != nil {
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
	f, _ := fs.Create("orig.dat")
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
	f, _ := fs.Create("rm/sub/x.dat")
	_ = f.Close()
	if err := fs.RemoveAll("rm"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := fs.Stat("rm/sub/x.dat"); err == nil {
		t.Error("Stat after RemoveAll must fail")
	}
}

func TestFS_Lock(t *testing.T) {
	_, _, fs := newMemFS(t)
	c, err := fs.(filesys.LockFS).Lock("LOCK")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Lock Close: %v", err)
	}
}

func TestFS_LockUnsupportedBase(t *testing.T) {
	fs := New(struct{ filesys.FS }{newBaseFS()}, newFakeBarrier(t))
	_, err := fs.(filesys.LockFS).Lock("LOCK")
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Lock on a base without locking: err = %v, want ErrUnsupported", err)
	}
}

func TestFS_OnHostOverHostBase(t *testing.T) {
	fs := New(filesys.Default, newFakeBarrier(t))
	if !fs.(filesys.HostFS).OnHost() {
		t.Error("OnHost over filesys.Default = false, want true: its ciphertext is at host paths")
	}
}

func TestFS_OnHostOverNonHostBase(t *testing.T) {
	_, _, fs := newMemFS(t)
	if fs.(filesys.HostFS).OnHost() {
		t.Error("OnHost over an in-memory base = true, want false")
	}
}

func TestEncFile_PreallocateAndPrefetch(t *testing.T) {
	_, _, fs := newMemFS(t)
	f, _ := fs.Create("hint.dat")
	defer func() { _ = f.Close() }()
	// Both hints forward to the base file, which accepts them.
	if err := f.(filesys.Preallocator).Preallocate(0, 4096); err != nil {
		t.Errorf("Preallocate: %v", err)
	}
	if err := f.(filesys.Prefetcher).Prefetch(0, 4096); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
}

func TestFS_PassthroughForFileSmallerThanHeader(t *testing.T) {
	mem, _, fs := newMemFS(t)
	f, _ := mem.Create("tiny")
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

// tornFile is a base file whose WriteAt, once tear is set, writes the first
// half of the buffer and fails with ENOSPC: a disk filling up in the middle
// of flushTailLocked's in-place rewrite of the trailing block.
type tornFile struct {
	filesys.File
	tear bool
}

func (f *tornFile) WriteAt(p []byte, off int64) (int, error) {
	if !f.tear {
		return f.File.WriteAt(p, off)
	}
	n, err := f.File.WriteAt(p[:len(p)/2], off)
	if err != nil {
		return n, err
	}
	return n, syscall.ENOSPC
}

// tornFS hands out tornFile from Create and keeps the last one.
type tornFS struct {
	filesys.FS
	file *tornFile
}

func (fs *tornFS) Create(name string) (filesys.File, error) {
	f, err := fs.FS.Create(name)
	if err != nil {
		return nil, err
	}
	fs.file = &tornFile{File: f}
	return fs.file, nil
}

// writeTornFile writes three full blocks plus a synced partial block, then
// extends the partial block and tears its rewrite with ENOSPC. It returns
// the plaintext of the three full blocks.
func writeTornFile(t *testing.T, mem filesys.FS, b *fakeBarrier, name string) []byte {
	t.Helper()
	tfs := &tornFS{FS: mem}
	f, err := New(tfs, b).Create(name)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	full := make([]byte, 3*BlockSize)
	for i := range full {
		full[i] = byte(i % 251)
	}
	if _, err := f.Write(append(bytes.Clone(full), bytes.Repeat([]byte("t"), 100)...)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	tfs.file.tear = true
	if _, err := f.Write(bytes.Repeat([]byte("u"), 200)); err != nil {
		t.Fatalf("Write extension: %v", err)
	}
	if err := f.Sync(); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("torn Sync err = %v, want ENOSPC", err)
	}
	_ = f.Close()
	return full
}

// TestEncFile_TornFinalBlockIsEndOfFile: a trailing block torn by ENOSPC
// mid-rewrite reads as end of file; every earlier block still reads back.
func TestEncFile_TornFinalBlockIsEndOfFile(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	full := writeTornFile(t, mem, b, "000007.log")

	r, err := New(mem, b).Open("000007.log")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	got := make([]byte, len(full))
	if n, err := r.ReadAt(got, 0); err != nil || n != len(full) || !bytes.Equal(got, full) {
		t.Fatalf("ReadAt full blocks: n=%d err=%v equal=%v", n, err, bytes.Equal(got, full))
	}
	if _, err := r.ReadAt(make([]byte, 1), int64(len(full))); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt torn final block: err = %v, want io.EOF", err)
	}
}

// TestEncFile_TornTailShorterThanOverheadIsEndOfFile covers an append torn
// before even the nonce and tag of the new block landed: the read-only open
// ends the file at the last full block instead of failing.
func TestEncFile_TornTailShorterThanOverheadIsEndOfFile(t *testing.T) {
	for _, short := range []int{1, BlockOverhead / 2, BlockOverhead - 1} {
		t.Run(fmt.Sprintf("short=%d", short), func(t *testing.T) {
			mem := newBaseFS()
			b := newFakeBarrier(t)
			full := make([]byte, 2*BlockSize)
			for i := range full {
				full[i] = byte(i % 251)
			}
			w, err := New(mem, b).Create("000008.log")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, err := w.Write(full); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			raw, err := mem.OpenReadWrite("000008.log")
			if err != nil {
				t.Fatalf("raw open: %v", err)
			}
			fi, err := raw.Stat()
			if err != nil {
				t.Fatalf("raw Stat: %v", err)
			}
			if _, err := raw.WriteAt(bytes.Repeat([]byte{0xA5}, short), fi.Size()); err != nil {
				t.Fatalf("raw WriteAt: %v", err)
			}
			if err := raw.Close(); err != nil {
				t.Fatalf("raw Close: %v", err)
			}

			r, err := New(mem, b).Open("000008.log")
			if err != nil {
				t.Fatalf("Open with %d-byte torn tail: %v", short, err)
			}
			defer func() { _ = r.Close() }()
			got := make([]byte, len(full))
			if n, err := r.ReadAt(got, 0); err != nil || n != len(full) || !bytes.Equal(got, full) {
				t.Fatalf("ReadAt full blocks: n=%d err=%v equal=%v", n, err, bytes.Equal(got, full))
			}
			if _, err := r.ReadAt(make([]byte, 1), int64(len(full))); !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt past full blocks: err = %v, want io.EOF", err)
			}
		})
	}
}

// TestEncFile_NonWALFinalBlockCorruptionFails: torn-tail recovery is for
// the WAL only. Any other file whose final block fails authentication
// keeps failing with ErrAuthFailed instead of reading back shortened.
func TestEncFile_NonWALFinalBlockCorruptionFails(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	full := writeTornFile(t, mem, b, "record.dat")

	r, err := New(mem, b).Open("record.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if _, err := io.ReadAll(r); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("ReadAll of a non-WAL file with a corrupt final block: err = %v, want ErrAuthFailed", err)
	}
	fi, err := r.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() <= int64(len(full)) {
		t.Fatalf("logical size = %d, want the full on-disk size past the %d authenticated bytes", fi.Size(), len(full))
	}
}

// TestEncFile_NonWALShortTrailingBlockFails: trailing bytes shorter than a
// block's nonce and tag stay a TrailingBlockTooShortError for a non-WAL file.
func TestEncFile_NonWALShortTrailingBlockFails(t *testing.T) {
	mem := newBaseFS()
	b := newFakeBarrier(t)
	w, err := New(mem, b).Create("record.dat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write(make([]byte, 2*BlockSize)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := mem.OpenReadWrite("record.dat")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	fi, err := raw.Stat()
	if err != nil {
		t.Fatalf("raw Stat: %v", err)
	}
	if _, err := raw.WriteAt([]byte{0xA5}, fi.Size()); err != nil {
		t.Fatalf("raw WriteAt: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw Close: %v", err)
	}
	var short *TrailingBlockTooShortError
	if _, err := New(mem, b).Open("record.dat"); !errors.As(err, &short) {
		t.Fatalf("Open of a non-WAL file with a 1-byte trailing block: err = %v, want *TrailingBlockTooShortError", err)
	}
}

func TestIsWALName(t *testing.T) {
	cases := map[string]bool{
		"000001.log":          true,
		"dir/000123.log":      true,
		"000001-001.log":      true,
		"torn.log":            false,
		".log":                false,
		"000001-.log":         false,
		"000001.sst":          false,
		"MANIFEST-000001":     false,
		"000001.log.tmp":      false,
		"customer/000001.dat": false,
		"000001-abc.log":      false,
	}
	for name, want := range cases {
		if got := isWALName(name); got != want {
			t.Errorf("isWALName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestEncFile_TamperedMiddleBlockStaysFatal: only the final block may be
// dropped as torn; a non-final block that fails authentication is still
// ErrAuthFailed.
func TestEncFile_TamperedMiddleBlockStaysFatal(t *testing.T) {
	mem, b, fs := newMemFS(t)
	f, err := fs.Create("mid.dat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write(make([]byte, 3*BlockSize+100)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := mem.OpenReadWrite("mid.dat")
	if err != nil {
		t.Fatalf("raw OpenReadWrite: %v", err)
	}
	var hs [2]byte
	if _, err := raw.ReadAt(hs[:], offHeaderSize); err != nil {
		t.Fatalf("raw ReadAt header size: %v", err)
	}
	headerSize := int64(hs[0]) | int64(hs[1])<<8
	off := headerSize + int64(CiphertextBlockSize) + int64(inlineNonceSize) + 5
	var one [1]byte
	if _, err := raw.ReadAt(one[:], off); err != nil {
		t.Fatalf("raw ReadAt: %v", err)
	}
	one[0] ^= 0xFF
	if _, err := raw.WriteAt(one[:], off); err != nil {
		t.Fatalf("raw WriteAt: %v", err)
	}
	_ = raw.Close()

	r, err := New(mem, b).Open("mid.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.ReadAt(make([]byte, BlockSize), 0); err != nil {
		t.Fatalf("ReadAt block 0: %v", err)
	}
	if _, err := r.ReadAt(make([]byte, 1), int64(BlockSize)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("ReadAt tampered block 1: err = %v, want ErrAuthFailed", err)
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
