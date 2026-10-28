// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebble

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// plainFS is a filesys.FS with none of the optional interfaces: it hides
// LockFS and DiskUsageFS, and its files hide Preallocator, Prefetcher and
// every vfs.File method filesys does not carry.
type plainFS struct {
	filesys.FS
	statErr error
}

// plainFile is a filesys.File with no optional methods.
type plainFile struct {
	filesys.File
	statErr error
}

func (f plainFile) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (p plainFS) wrap(f filesys.File, err error) (filesys.File, error) {
	if err != nil {
		return nil, err
	}
	return plainFile{File: f, statErr: p.statErr}, nil
}

func (p plainFS) Create(name string) (filesys.File, error) { return p.wrap(p.FS.Create(name)) }
func (p plainFS) Open(name string) (filesys.File, error)   { return p.wrap(p.FS.Open(name)) }

func (p plainFS) OpenReadWrite(name string) (filesys.File, error) {
	return p.wrap(p.FS.OpenReadWrite(name))
}

// diskUsageFS is a filesys.FS that reports a fixed disk usage or error.
type diskUsageFS struct {
	filesys.FS
	usage filesys.DiskUsage
	err   error
}

func (d diskUsageFS) DiskUsage(string) (filesys.DiskUsage, error) { return d.usage, d.err }

// countingOption is a vfs.OpenOption that counts the files it is applied to.
type countingOption struct{ n *int }

func (o countingOption) Apply(vfs.File) { *o.n++ }

func newMemPlain() (vfs.FS, filesys.FS) {
	base := plainFS{FS: FromVFS(vfs.NewMem())}
	return VFS(base), base
}

func mustWrite(t *testing.T, v vfs.FS, name, contents string) {
	t.Helper()
	f, err := v.Create(name, vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		t.Fatalf("Write(%s): %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%s): %v", name, err)
	}
}

func mustRead(t *testing.T, v vfs.FS, name string) string {
	t.Helper()
	f, err := v.Open(name)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", name, err)
	}
	return string(b)
}

func TestVFS_WrapsFilesysFS(t *testing.T) {
	v, _ := newMemPlain()
	if _, ok := v.(*vfsFS); !ok {
		t.Fatalf("VFS(plain) = %T, want *vfsFS", v)
	}
	mustWrite(t, v, "a", "data")
	if got := mustRead(t, v, "a"); got != "data" {
		t.Errorf("round trip = %q, want %q", got, "data")
	}
}

func TestVFS_UnwrapsFromVFS(t *testing.T) {
	mem := vfs.NewMem()
	if got := VFS(FromVFS(mem)); got != mem {
		t.Errorf("VFS(FromVFS(mem)) = %v, want the original vfs.FS", got)
	}
}

func TestFromVFS_NilIsDefault(t *testing.T) {
	f, ok := FromVFS(nil).(*filesysFS)
	if !ok || f.fs != vfs.Default {
		t.Errorf("FromVFS(nil) = %v, want vfs.Default bridged", f)
	}
}

func TestFromVFS_UnwrapsVFS(t *testing.T) {
	base := plainFS{FS: filesys.Default}
	if got := FromVFS(VFS(base)); got != filesys.FS(base) {
		t.Errorf("FromVFS(VFS(base)) = %v, want the original filesys.FS", got)
	}
}

func TestWrapVFS_AppliesWrap(t *testing.T) {
	mem := vfs.NewMem()
	var seen filesys.FS
	got := WrapVFS(mem, func(b filesys.FS) filesys.FS {
		seen = b
		return plainFS{FS: b}
	})
	if FromVFS(mem) == nil || VFS(seen) != mem {
		t.Errorf("wrap was handed %v, want mem bridged", seen)
	}
	mustWrite(t, got, "a", "x")
	if mustRead(t, mem, "a") != "x" {
		t.Error("a write through the wrapped FS did not reach the base")
	}
}

func TestWrapVFS_NilBaseIsDefault(t *testing.T) {
	var seen filesys.FS
	WrapVFS(nil, func(b filesys.FS) filesys.FS {
		seen = b
		return b
	})
	if VFS(seen) != vfs.Default {
		t.Errorf("wrap was handed %v, want vfs.Default bridged", seen)
	}
}

func TestVFSFS_CreateAndOpen(t *testing.T) {
	v, _ := newMemPlain()
	n := 0
	mustWrite(t, v, "a", "hello")
	f, err := v.Open("a", countingOption{&n})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = f.Close()
	if n != 1 {
		t.Errorf("open option applied %d times, want 1", n)
	}
	if _, err := v.Open("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open missing: err = %v, want ErrNotExist", err)
	}
}

func TestVFSFS_CreateMissingDirFails(t *testing.T) {
	v := VFS(plainFS{FS: filesys.Default})
	if _, err := v.Create(filepath.Join(t.TempDir(), "x", "a"), vfs.WriteCategoryUnspecified); err == nil {
		t.Error("Create in a missing dir succeeded")
	}
}

func TestVFSFS_OpenReadWrite(t *testing.T) {
	v, _ := newMemPlain()
	n := 0
	f, err := v.OpenReadWrite("a", vfs.WriteCategoryUnspecified, countingOption{&n})
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	if _, err := f.WriteAt([]byte("rw"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	_ = f.Close()
	if n != 1 || mustRead(t, v, "a") != "rw" {
		t.Errorf("option applied %d times, contents %q", n, mustRead(t, v, "a"))
	}
}

func TestVFSFS_OpenReadWriteFails(t *testing.T) {
	v := VFS(plainFS{FS: filesys.Default})
	if _, err := v.OpenReadWrite(filepath.Join(t.TempDir(), "x", "a"), vfs.WriteCategoryUnspecified); err == nil {
		t.Error("OpenReadWrite in a missing dir succeeded")
	}
}

func TestVFSFS_OpenDir(t *testing.T) {
	v := VFS(plainFS{FS: filesys.Default})
	d, err := v.OpenDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	if err := d.Sync(); err != nil {
		t.Errorf("Sync: %v", err)
	}
	_ = d.Close()
	if _, err := v.OpenDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("OpenDir of a missing dir succeeded")
	}
}

func TestVFSFS_LinkRemoveRename(t *testing.T) {
	v, _ := newMemPlain()
	mustWrite(t, v, "a", "x")
	if err := v.Link("a", "b"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if err := v.Rename("b", "c"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if mustRead(t, v, "c") != "x" {
		t.Error("renamed link lost its contents")
	}
	if err := v.Remove("c"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := v.Remove("c"); err == nil {
		t.Error("second Remove succeeded")
	}
}

func TestVFSFS_MkdirAllListRemoveAll(t *testing.T) {
	v, _ := newMemPlain()
	if err := v.MkdirAll("d/e", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mustWrite(t, v, "d/e/a", "x")
	names, err := v.List("d/e")
	if err != nil || !slices.Equal(names, []string{"a"}) {
		t.Fatalf("List = %v, %v; want [a]", names, err)
	}
	if err := v.RemoveAll("d"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := v.List("d/e"); err == nil {
		t.Error("List after RemoveAll succeeded")
	}
}

func TestVFSFS_ReuseForWrite(t *testing.T) {
	v, _ := newMemPlain()
	mustWrite(t, v, "old", "stale")
	f, err := v.ReuseForWrite("old", "new", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("ReuseForWrite: %v", err)
	}
	_ = f.Close()
	if _, err := v.Stat("old"); err == nil {
		t.Error("ReuseForWrite left the old file")
	}
	if fi, err := v.Stat("new"); err != nil || fi.Size() != 0 {
		t.Errorf("new file = %v, %v; want an empty file", fi, err)
	}
}

func TestVFSFS_ReuseForWriteMissingOldFails(t *testing.T) {
	v, _ := newMemPlain()
	if _, err := v.ReuseForWrite("missing", "new", vfs.WriteCategoryUnspecified); err == nil {
		t.Error("ReuseForWrite of a missing file succeeded")
	}
}

func TestVFSFS_Lock(t *testing.T) {
	v := &vfsFS{fs: FromVFS(vfs.NewMem())}
	c, err := v.Lock("LOCK")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("unlock: %v", err)
	}
}

func TestVFSFS_LockUnsupported(t *testing.T) {
	v, _ := newMemPlain()
	if _, err := v.Lock("LOCK"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Lock on a plain FS: err = %v, want ErrUnsupported", err)
	}
}

// TestVFSFS_LockOnHostSharesPebbleDefault takes one host file's lock through
// VFS(filesys.Default) and through Pebble's vfs.Default, in both orders: the
// second must fail while the first is held, and must succeed once it is
// released. A POSIX record lock belongs to the process, so only a shared
// record of what is held makes the two exclude each other.
func TestVFSFS_LockOnHostSharesPebbleDefault(t *testing.T) {
	adapted := VFS(filesys.Default)
	orders := map[string][2]vfs.FS{
		"adapter then pebble": {adapted, vfs.Default},
		"pebble then adapter": {vfs.Default, adapted},
	}
	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "LOCK")
			first, err := order[0].Lock(lock)
			if err != nil {
				t.Fatalf("first Lock: %v", err)
			}
			if c, err := order[1].Lock(lock); err == nil {
				_ = c.Close()
				t.Fatal("second Lock of a held host file succeeded")
			}
			if c, err := order[0].Lock(lock); err == nil {
				_ = c.Close()
				t.Fatal("the failed second Lock released the first holder's lock")
			}
			if err := first.Close(); err != nil {
				t.Fatalf("unlock: %v", err)
			}
			second, err := order[1].Lock(lock)
			if err != nil {
				t.Fatalf("Lock after release: %v", err)
			}
			_ = second.Close()
		})
	}
}

// offHostFS is a LockFS that reports its names are not host paths.
type offHostFS struct {
	filesys.LockFS
}

func (offHostFS) OnHost() bool { return false }

func TestVFSFS_LockOffHostUsesItsOwnLock(t *testing.T) {
	mem := vfs.NewMem()
	v := VFS(offHostFS{LockFS: FromVFS(mem).(filesys.LockFS)})
	c, err := v.Lock("LOCK")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c2, err := mem.Lock("LOCK"); err == nil {
		_ = c2.Close()
		t.Error("the off-host FS's own lock was not taken: its base locked LOCK again")
	}
}

func TestVFSFS_Stat(t *testing.T) {
	v, _ := newMemPlain()
	mustWrite(t, v, "a", "four")
	fi, err := v.Stat("a")
	if err != nil || fi.Size() != 4 {
		t.Fatalf("Stat = %v, %v; want size 4", fi, err)
	}
	if _, err := v.Stat("missing"); err == nil {
		t.Error("Stat of a missing file succeeded")
	}
}

func TestVFSFS_PathHelpers(t *testing.T) {
	v, _ := newMemPlain()
	if got := v.PathJoin("a", "b"); got != filepath.Join("a", "b") {
		t.Errorf("PathJoin = %q", got)
	}
	if got := v.PathBase(filepath.Join("a", "b")); got != "b" {
		t.Errorf("PathBase = %q", got)
	}
	if got := v.PathDir(filepath.Join("a", "b")); got != "a" {
		t.Errorf("PathDir = %q", got)
	}
}

func TestVFSFS_GetDiskUsage(t *testing.T) {
	want := filesys.DiskUsage{AvailBytes: 1, TotalBytes: 3, UsedBytes: 2}
	v := VFS(diskUsageFS{FS: filesys.Default, usage: want})
	got, err := v.GetDiskUsage("/")
	if err != nil {
		t.Fatalf("GetDiskUsage: %v", err)
	}
	if got.AvailBytes != 1 || got.TotalBytes != 3 || got.UsedBytes != 2 {
		t.Errorf("GetDiskUsage = %+v, want %+v", got, want)
	}
}

func TestVFSFS_GetDiskUsageErrors(t *testing.T) {
	sentinel := errors.New("usage failed")
	if _, err := VFS(diskUsageFS{FS: filesys.Default, err: sentinel}).GetDiskUsage("/"); !errors.Is(err, sentinel) {
		t.Errorf("GetDiskUsage error = %v, want sentinel", err)
	}
	v, _ := newMemPlain()
	if _, err := v.GetDiskUsage("/"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("GetDiskUsage on a plain FS: err = %v, want ErrUnsupported", err)
	}
}

// TestVFSFS_GetDiskUsageOnHost pins that an FS over host paths reports the
// host's disk usage, as Pebble's own default file system does, rather than
// an UnsupportedError Pebble logs as a background error on every check.
func TestVFSFS_GetDiskUsageOnHost(t *testing.T) {
	u, err := VFS(filesys.Default).GetDiskUsage(t.TempDir())
	if err != nil {
		t.Fatalf("GetDiskUsage: %v", err)
	}
	if u.TotalBytes == 0 {
		t.Errorf("GetDiskUsage = %+v, want a non-zero total", u)
	}
}

func TestVFSFS_Unwrap(t *testing.T) {
	v, _ := newMemPlain()
	if got := v.Unwrap(); got != nil {
		t.Errorf("Unwrap = %v, want nil", got)
	}
	if got := (&vfsFS{fs: filesys.Default}).Unwrap(); got != nil {
		t.Errorf("Unwrap = %v, want nil", got)
	}
}

func TestToVFSFile_UnwrapsFilesysFile(t *testing.T) {
	mf, err := vfs.NewMem().Create("a", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := toVFSFile(&filesysFile{File: mf}, nil)
	if err != nil || got != mf {
		t.Errorf("toVFSFile = %v, %v; want the original vfs.File", got, err)
	}
}

func TestToVFSFile_PropagatesError(t *testing.T) {
	sentinel := errors.New("open failed")
	if f, err := toVFSFile(nil, sentinel); f != nil || !errors.Is(err, sentinel) {
		t.Errorf("toVFSFile = %v, %v; want nil, sentinel", f, err)
	}
}

func TestVFSFile_Stat(t *testing.T) {
	v, _ := newMemPlain()
	mustWrite(t, v, "a", "abc")
	f, err := v.Open("a")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || fi.Size() != 3 {
		t.Errorf("Stat = %v, %v; want size 3", fi, err)
	}
}

func TestVFSFile_StatError(t *testing.T) {
	sentinel := errors.New("stat failed")
	base := plainFS{FS: FromVFS(vfs.NewMem()), statErr: sentinel}
	v := VFS(base)
	mustWrite(t, v, "a", "x")
	f, err := v.Open("a")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Stat(); !errors.Is(err, sentinel) {
		t.Errorf("Stat error = %v, want sentinel", err)
	}
}

func TestVFSFile_HintsForwardWhenSupported(t *testing.T) {
	mf, err := vfs.NewMem().Create("a", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f := &vfsFile{File: &filesysFile{File: mf}}
	if err := f.Preallocate(0, 4096); err != nil {
		t.Errorf("Preallocate: %v", err)
	}
	if err := f.Prefetch(0, 4096); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
	if err := f.SyncData(); err != nil {
		t.Errorf("SyncData: %v", err)
	}
	if _, err := f.SyncTo(0); err != nil {
		t.Errorf("SyncTo: %v", err)
	}
	if f.Fd() != mf.Fd() {
		t.Errorf("Fd = %v, want the base file's %v", f.Fd(), mf.Fd())
	}
}

func TestVFSFile_HintsFallBackWhenUnsupported(t *testing.T) {
	v, _ := newMemPlain()
	f, err := v.Create("a", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Preallocate(0, 4096); err != nil {
		t.Errorf("Preallocate: %v", err)
	}
	if err := f.Prefetch(0, 4096); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
	if err := f.SyncData(); err != nil {
		t.Errorf("SyncData: %v", err)
	}
	if full, err := f.SyncTo(1); err != nil || !full {
		t.Errorf("SyncTo = %v, %v; want a full sync", full, err)
	}
	if f.Fd() != vfs.InvalidFd {
		t.Errorf("Fd = %v, want InvalidFd", f.Fd())
	}
}

func TestVFSFile_SyncToPropagatesSyncError(t *testing.T) {
	v := VFS(plainFS{FS: filesys.Default})
	name := filepath.Join(t.TempDir(), "a")
	f, err := v.Create(name, vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = f.Close()
	if _, err := f.SyncTo(1); err == nil {
		t.Error("SyncTo on a closed file succeeded")
	}
}

func TestToVFSFileInfo(t *testing.T) {
	mem := vfs.NewMem()
	mustWrite(t, mem, "a", "x")
	vfi, err := mem.Stat("a")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := toVFSFileInfo(vfi); got != vfi {
		t.Errorf("toVFSFileInfo(vfs.FileInfo) = %v, want it unchanged", got)
	}
	name := filepath.Join(t.TempDir(), "b")
	if err := os.WriteFile(name, []byte("yy"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ofi, err := os.Stat(name)
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}
	got := toVFSFileInfo(ofi)
	if got.Size() != 2 || got.DeviceID() != (vfs.DeviceID{}) {
		t.Errorf("toVFSFileInfo(os.FileInfo) = size %d device %v", got.Size(), got.DeviceID())
	}
}

func TestVFSFileInfo_DeviceIDIsZero(t *testing.T) {
	if got := (vfsFileInfo{}).DeviceID(); got != (vfs.DeviceID{}) {
		t.Errorf("DeviceID = %v, want zero", got)
	}
}

func TestFilesysFS_Files(t *testing.T) {
	b := FromVFS(vfs.NewMem())
	f, err := b.Create("a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != 3 {
		t.Errorf("file Stat = %v, %v; want size 3", fi, err)
	}
	_ = f.Close()
	if _, ok := f.(filesys.Preallocator); !ok {
		t.Error("a bridged file does not keep Preallocate")
	}
	if _, ok := f.(filesys.Prefetcher); !ok {
		t.Error("a bridged file does not keep Prefetch")
	}
	rw, err := b.OpenReadWrite("a")
	if err != nil {
		t.Fatalf("OpenReadWrite: %v", err)
	}
	_ = rw.Close()
	r, err := b.Open("a")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = r.Close()
	if fi, err := b.Stat("a"); err != nil || fi.Size() != 3 {
		t.Errorf("Stat = %v, %v; want size 3", fi, err)
	}
}

func TestFilesysFS_FileErrors(t *testing.T) {
	b := FromVFS(vfs.NewMem())
	if _, err := b.Open("missing"); err == nil {
		t.Error("Open of a missing file succeeded")
	}
	if _, err := b.Create("missing/a"); err == nil {
		t.Error("Create in a missing dir succeeded")
	}
	if _, err := b.OpenReadWrite("missing/a"); err == nil {
		t.Error("OpenReadWrite in a missing dir succeeded")
	}
	if _, err := b.Stat("missing"); err == nil {
		t.Error("Stat of a missing file succeeded")
	}
}

func TestFilesysFS_Namespace(t *testing.T) {
	b := FromVFS(vfs.NewMem())
	if err := b.MkdirAll("d", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	d, err := b.OpenDir("d")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	_ = d.Close()
	f, _ := b.Create("d/a")
	_ = f.Close()
	if err := b.Link("d/a", "d/b"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if err := b.Rename("d/b", "d/c"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := b.Remove("d/c"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	names, err := b.List("d")
	if err != nil || !slices.Equal(names, []string{"a"}) {
		t.Errorf("List = %v, %v; want [a]", names, err)
	}
	if err := b.RemoveAll("d"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := b.OpenDir("d"); err == nil {
		t.Error("OpenDir after RemoveAll succeeded")
	}
}

func TestFilesysFS_Lock(t *testing.T) {
	l, ok := FromVFS(vfs.NewMem()).(filesys.LockFS)
	if !ok {
		t.Fatal("a bridged FS does not keep Lock")
	}
	c, err := l.Lock("LOCK")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if _, err := l.Lock("LOCK"); err == nil {
		t.Error("a second Lock of a held lock succeeded")
	}
	_ = c.Close()
}

func TestFilesysFS_DiskUsage(t *testing.T) {
	d := FromVFS(vfs.Default).(filesys.DiskUsageFS)
	u, err := d.DiskUsage(t.TempDir())
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if u.TotalBytes == 0 {
		t.Errorf("DiskUsage = %+v, want a non-zero total", u)
	}
	if _, err := FromVFS(vfs.NewMem()).(filesys.DiskUsageFS).DiskUsage("/"); err == nil {
		t.Error("DiskUsage on MemFS succeeded")
	}
}
