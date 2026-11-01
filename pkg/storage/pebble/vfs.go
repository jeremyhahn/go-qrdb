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
	"io"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// This file is the one place go-qrdb meets Pebble's vfs package. VFS
// presents a filesys.FS to Pebble as a vfs.FS; FromVFS presents a Pebble
// vfs.FS as a filesys.FS, keeping its locking, disk usage, preallocation and
// prefetch. WrapVFS composes them, which is how a Pebble store writes
// through the at-rest encryption layer.

// VFS presents fsys to Pebble as a vfs.FS. A filesys.FS that FromVFS made
// is unwrapped to the vfs.FS it bridges.
//
// Write categories and open options Pebble passes are not carried below the
// adapter: filesys has neither. Pebble records categories in the
// disk-health layer it puts above the FS, and an open option's Apply still
// runs against the returned file. A file whose FileInfo carries no Pebble
// DeviceID reports the zero DeviceID. ReuseForWrite removes oldname and
// creates newname, which the vfs.FS contract permits.
func VFS(fsys filesys.FS) vfs.FS {
	if b, ok := fsys.(*filesysFS); ok {
		return b.fs
	}
	return &vfsFS{fs: fsys}
}

// FromVFS presents fs to go-qrdb as a filesys.FS; a nil fs is vfs.Default.
// The result implements filesys.LockFS and filesys.DiskUsageFS, and its
// files filesys.Preallocator and filesys.Prefetcher. A vfs.FS that VFS
// made is unwrapped to the filesys.FS it adapts.
func FromVFS(fs vfs.FS) filesys.FS {
	if fs == nil {
		fs = vfs.Default
	}
	if a, ok := fs.(*vfsFS); ok {
		return a.fs
	}
	return &filesysFS{fs: fs}
}

// WrapVFS returns base (vfs.Default when nil) with wrap applied through the
// filesys interface, as a vfs.FS Pebble can open. wrap is typically an
// *atrest.Encryptor's Wrap; when it returns its argument unchanged, so does
// WrapVFS.
func WrapVFS(base vfs.FS, wrap func(filesys.FS) filesys.FS) vfs.FS {
	return VFS(wrap(FromVFS(base)))
}

// vfsFS is a filesys.FS presented as a vfs.FS.
type vfsFS struct {
	fs filesys.FS
}

var _ vfs.FS = (*vfsFS)(nil)

func (a *vfsFS) Create(name string, _ vfs.DiskWriteCategory) (vfs.File, error) {
	return toVFSFile(a.fs.Create(name))
}

func (a *vfsFS) Link(oldname, newname string) error { return a.fs.Link(oldname, newname) }

func (a *vfsFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := toVFSFile(a.fs.Open(name))
	if err != nil {
		return nil, err
	}
	applyOpenOptions(f, opts)
	return f, nil
}

func (a *vfsFS) OpenReadWrite(name string, _ vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := toVFSFile(a.fs.OpenReadWrite(name))
	if err != nil {
		return nil, err
	}
	applyOpenOptions(f, opts)
	return f, nil
}

func (a *vfsFS) OpenDir(name string) (vfs.File, error) { return toVFSFile(a.fs.OpenDir(name)) }

func (a *vfsFS) Remove(name string) error    { return a.fs.Remove(name) }
func (a *vfsFS) RemoveAll(name string) error { return a.fs.RemoveAll(name) }

func (a *vfsFS) Rename(oldname, newname string) error { return a.fs.Rename(oldname, newname) }

func (a *vfsFS) ReuseForWrite(oldname, newname string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if err := a.fs.Remove(oldname); err != nil {
		return nil, err
	}
	return a.Create(newname, category)
}

func (a *vfsFS) MkdirAll(dir string, perm os.FileMode) error { return a.fs.MkdirAll(dir, perm) }

// Lock locks name. An FS whose names are host paths is locked through
// vfs.Default, the one owner of host-file locks in the process (see
// filesys.HostFS): a store opened through VFS and one opened through
// Pebble's own default file system then exclude each other, and the lock is
// the platform's own, LockFileEx on Windows. Any other FS locks through its
// own LockFS, or reports an UnsupportedError.
func (a *vfsFS) Lock(name string) (io.Closer, error) {
	if a.onHost() {
		return vfs.Default.Lock(name)
	}
	l, ok := a.fs.(filesys.LockFS)
	if !ok {
		return nil, &filesys.UnsupportedError{Op: "lock"}
	}
	return l.Lock(name)
}

// onHost reports whether the adapted FS's names are host paths.
func (a *vfsFS) onHost() bool {
	h, ok := a.fs.(filesys.HostFS)
	return ok && h.OnHost()
}

func (a *vfsFS) List(dir string) ([]string, error) { return a.fs.List(dir) }

func (a *vfsFS) Stat(name string) (vfs.FileInfo, error) {
	fi, err := a.fs.Stat(name)
	if err != nil {
		return nil, err
	}
	return toVFSFileInfo(fi), nil
}

func (a *vfsFS) PathBase(path string) string    { return filepath.Base(path) }
func (a *vfsFS) PathJoin(elem ...string) string { return filepath.Join(elem...) }
func (a *vfsFS) PathDir(path string) string     { return filepath.Dir(path) }

// GetDiskUsage reports the space on the file system holding path. An FS
// whose names are host paths reports what vfs.Default measures there, the
// figure Pebble's own default file system gives it.
func (a *vfsFS) GetDiskUsage(path string) (vfs.DiskUsage, error) {
	if a.onHost() {
		return vfs.Default.GetDiskUsage(path)
	}
	d, ok := a.fs.(filesys.DiskUsageFS)
	if !ok {
		return vfs.DiskUsage{}, &filesys.UnsupportedError{Op: "disk usage"}
	}
	u, err := d.DiskUsage(path)
	if err != nil {
		return vfs.DiskUsage{}, err
	}
	return vfs.DiskUsage{AvailBytes: u.AvailBytes, TotalBytes: u.TotalBytes, UsedBytes: u.UsedBytes}, nil
}

// Unwrap returns nil: the adapter is the root of any vfs.Root walk, so
// nothing reaches past it to the files beneath an encrypting FS.
func (a *vfsFS) Unwrap() vfs.FS { return nil }

// toVFSFile presents f as a vfs.File, unwrapping a file FromVFS made.
func toVFSFile(f filesys.File, err error) (vfs.File, error) {
	if err != nil {
		return nil, err
	}
	if b, ok := f.(*filesysFile); ok {
		return b.File, nil
	}
	return &vfsFile{File: f}, nil
}

// applyOpenOptions applies Pebble's open options to an opened file.
func applyOpenOptions(f vfs.File, opts []vfs.OpenOption) {
	for _, opt := range opts {
		opt.Apply(f)
	}
}

// vfsFile is a filesys.File presented as a vfs.File. The methods filesys
// leaves optional fall back to what the vfs.File contract allows: no-op
// hints, and a full Sync for SyncData and SyncTo.
type vfsFile struct {
	filesys.File
}

var _ vfs.File = (*vfsFile)(nil)

func (f *vfsFile) Stat() (vfs.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return toVFSFileInfo(fi), nil
}

func (f *vfsFile) Preallocate(offset, length int64) error {
	if p, ok := f.File.(filesys.Preallocator); ok {
		return p.Preallocate(offset, length)
	}
	return nil
}

func (f *vfsFile) Prefetch(offset, length int64) error {
	if p, ok := f.File.(filesys.Prefetcher); ok {
		return p.Prefetch(offset, length)
	}
	return nil
}

func (f *vfsFile) SyncData() error {
	if s, ok := f.File.(interface{ SyncData() error }); ok {
		return s.SyncData()
	}
	return f.File.Sync()
}

func (f *vfsFile) SyncTo(length int64) (bool, error) {
	if s, ok := f.File.(interface {
		SyncTo(int64) (bool, error)
	}); ok {
		return s.SyncTo(length)
	}
	if err := f.File.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func (f *vfsFile) Fd() uintptr {
	if d, ok := f.File.(interface{ Fd() uintptr }); ok {
		return d.Fd()
	}
	return vfs.InvalidFd
}

// toVFSFileInfo presents fi as a vfs.FileInfo, with the zero DeviceID when
// fi carries none.
func toVFSFileInfo(fi os.FileInfo) vfs.FileInfo {
	if v, ok := fi.(vfs.FileInfo); ok {
		return v
	}
	return vfsFileInfo{FileInfo: fi}
}

// vfsFileInfo is an os.FileInfo with no device identity.
type vfsFileInfo struct {
	os.FileInfo
}

func (vfsFileInfo) DeviceID() vfs.DeviceID { return vfs.DeviceID{} }

// filesysFS is a Pebble vfs.FS presented as a filesys.FS.
type filesysFS struct {
	fs vfs.FS
}

var (
	_ filesys.LockFS      = (*filesysFS)(nil)
	_ filesys.DiskUsageFS = (*filesysFS)(nil)
)

func (b *filesysFS) Create(name string) (filesys.File, error) {
	return toFilesysFile(b.fs.Create(name, vfs.WriteCategoryUnspecified))
}

func (b *filesysFS) Open(name string) (filesys.File, error) {
	return toFilesysFile(b.fs.Open(name))
}

func (b *filesysFS) OpenReadWrite(name string) (filesys.File, error) {
	return toFilesysFile(b.fs.OpenReadWrite(name, vfs.WriteCategoryUnspecified))
}

func (b *filesysFS) OpenDir(name string) (filesys.File, error) {
	return toFilesysFile(b.fs.OpenDir(name))
}

func (b *filesysFS) Link(oldname, newname string) error { return b.fs.Link(oldname, newname) }
func (b *filesysFS) Remove(name string) error           { return b.fs.Remove(name) }
func (b *filesysFS) RemoveAll(name string) error        { return b.fs.RemoveAll(name) }

func (b *filesysFS) Rename(oldname, newname string) error { return b.fs.Rename(oldname, newname) }

func (b *filesysFS) MkdirAll(dir string, perm os.FileMode) error { return b.fs.MkdirAll(dir, perm) }

func (b *filesysFS) List(dir string) ([]string, error) { return b.fs.List(dir) }

// Stat returns Pebble's vfs.FileInfo, so its DeviceID survives a round
// trip through VFS.
func (b *filesysFS) Stat(name string) (os.FileInfo, error) {
	fi, err := b.fs.Stat(name)
	if err != nil {
		return nil, err
	}
	return fi, nil
}

func (b *filesysFS) Lock(name string) (io.Closer, error) { return b.fs.Lock(name) }

func (b *filesysFS) DiskUsage(path string) (filesys.DiskUsage, error) {
	u, err := b.fs.GetDiskUsage(path)
	if err != nil {
		return filesys.DiskUsage{}, err
	}
	return filesys.DiskUsage{AvailBytes: u.AvailBytes, TotalBytes: u.TotalBytes, UsedBytes: u.UsedBytes}, nil
}

// toFilesysFile presents f as a filesys.File.
func toFilesysFile(f vfs.File, err error) (filesys.File, error) {
	if err != nil {
		return nil, err
	}
	return &filesysFile{File: f}, nil
}

// filesysFile is a Pebble vfs.File presented as a filesys.File. Preallocate
// and Prefetch are promoted from the vfs.File.
type filesysFile struct {
	vfs.File
}

var (
	_ filesys.File         = (*filesysFile)(nil)
	_ filesys.Preallocator = (*filesysFile)(nil)
	_ filesys.Prefetcher   = (*filesysFile)(nil)
)

func (f *filesysFile) Stat() (os.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return fi, nil
}
