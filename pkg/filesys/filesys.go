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

// Package filesys is go-qrdb's file-system interface: the one surface the
// at-rest encryption layer (pkg/seal/encfs, pkg/atrest), the file storage
// backend and the vector checkpoint writer do their file I/O through.
//
// It imports nothing but the standard library, so a node built without
// Pebble carries no Pebble packages for it. A Pebble store reaches the same
// encrypting FS through the adapter in pkg/storage/pebble, which presents an
// FS to Pebble as a vfs.FS and bridges Pebble's own FS into one.
//
// The core FS and File interfaces hold what every caller needs. LockFS,
// HostFS, DiskUsageFS, Preallocator and Prefetcher are optional capabilities, in the
// manner of io/fs: an implementation that has them implements them, and a
// caller that wants them asserts for them.
package filesys

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// File is an open file.
//
// Write and Sync must be called sequentially: at most one Write or Sync may
// run at any given time.
type File interface {
	io.Closer
	io.Reader
	io.ReaderAt
	io.Writer
	// WriteAt is supported only on files opened with FS.OpenReadWrite.
	io.WriterAt

	// Stat describes the file.
	Stat() (fs.FileInfo, error)

	// Sync commits the file's contents and metadata to stable storage.
	Sync() error
}

// FS is a namespace of files. Names are file-system paths in the operating
// system's syntax, joined with path/filepath.
type FS interface {
	// Create creates the named file for reading and writing, replacing any
	// file already at the name.
	Create(name string) (File, error)

	// Open opens the named file for reading.
	Open(name string) (File, error)

	// OpenReadWrite opens the named file for reading and writing, creating
	// it if it does not exist.
	OpenReadWrite(name string) (File, error)

	// OpenDir opens the named directory so that it can be synced.
	OpenDir(name string) (File, error)

	// Link creates newname as a hard link to the oldname file.
	Link(oldname, newname string) error

	// Remove removes the named file or empty directory.
	Remove(name string) error

	// RemoveAll removes the named file or directory and everything it
	// contains, returning the first error it meets.
	RemoveAll(name string) error

	// Rename renames a file, replacing any file already at newname.
	Rename(oldname, newname string) error

	// MkdirAll creates a directory and every missing parent, with the
	// semantics of os.MkdirAll.
	MkdirAll(dir string, perm os.FileMode) error

	// List returns the names of the entries in dir, relative to dir.
	List(dir string) ([]string, error)

	// Stat describes the named file.
	Stat(name string) (fs.FileInfo, error)
}

// LockFS is an FS that can take an exclusive, non-blocking lock on a file.
type LockFS interface {
	FS

	// Lock creates or truncates the named file and locks it exclusively.
	// Closing the returned Closer releases the lock. Locking a file the
	// process already holds fails rather than waiting.
	Lock(name string) (io.Closer, error)
}

// HostFS is an FS whose names are paths on the host's file system, so a
// lock on one of its names is a lock on the host's file. Default is one,
// and an FS layered over a base, such as the encrypting FS, reports what
// its base reports.
//
// A host file's lock belongs to the process (a POSIX record lock is the
// process's, not the descriptor's), so every lock the process takes on a
// host file must have one owner keeping one record of what is held. That
// owner is Pebble's default file system, whose own opens lock that way:
// the adapter in pkg/storage/pebble takes a HostFS's locks through it, and
// this package takes none itself.
type HostFS interface {
	FS

	// OnHost reports whether the FS's names are host file-system paths.
	OnHost() bool
}

// DiskUsage is the space on the file system holding a path.
type DiskUsage struct {
	// AvailBytes is the space available to an unprivileged user.
	AvailBytes uint64
	// TotalBytes is the size of the file system.
	TotalBytes uint64
	// UsedBytes is the space in use.
	UsedBytes uint64
}

// DiskUsageFS is an FS that reports the space on its file system.
type DiskUsageFS interface {
	FS

	// DiskUsage reports the space on the file system holding path.
	DiskUsage(path string) (DiskUsage, error)
}

// Preallocator is a File that can reserve storage ahead of writes.
type Preallocator interface {
	// Preallocate reserves length bytes of storage at offset. It is a hint:
	// an implementation may do nothing.
	Preallocate(offset, length int64) error
}

// Prefetcher is a File that can read ahead into the operating system's
// cache.
type Prefetcher interface {
	// Prefetch asks for length bytes at offset to be read into the cache. It
	// is a hint: an implementation may do nothing.
	Prefetch(offset, length int64) error
}

// UnsupportedError reports an optional capability an FS or File does not
// have. It matches errors.ErrUnsupported.
type UnsupportedError struct {
	// Op is the operation asked for, such as "lock".
	Op string
}

func (e *UnsupportedError) Error() string {
	return "filesys: " + e.Op + " not supported"
}

// Unwrap returns errors.ErrUnsupported.
func (e *UnsupportedError) Unwrap() error { return errors.ErrUnsupported }

// Default is the operating system's file system. Its files are *os.File,
// save a directory opened on Windows, whose Sync is a no-op (see openDir).
// It implements HostFS, and neither LockFS nor DiskUsageFS: a Pebble store
// opened over it takes its lock, and reads its disk usage, through Pebble's
// default file system (see HostFS).
var Default FS = osFS{}

var _ HostFS = osFS{}

// osFS is the FS over package os.
type osFS struct{}

// OnHost reports true: osFS's names are host paths.
func (osFS) OnHost() bool { return true }

// filePerm is the mode new files are created with, before the umask, as
// os.Create does.
const filePerm os.FileMode = 0o666

// Create removes any file already at name before creating it, so the new
// file is a new inode: a hard link to the old one keeps its contents.
func (osFS) Create(name string) (File, error) {
	const flag = os.O_RDWR | os.O_CREATE | os.O_EXCL
	f, err := openOSFile(name, flag)
	for errors.Is(err, fs.ErrExist) {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return nil, rerr
		}
		f, err = openOSFile(name, flag)
	}
	return f, err
}

func (osFS) Open(name string) (File, error) {
	return openOSFile(name, os.O_RDONLY)
}

func (osFS) OpenReadWrite(name string) (File, error) {
	return openOSFile(name, os.O_RDWR|os.O_CREATE)
}

func (osFS) OpenDir(name string) (File, error) {
	return openDir(name, dirSyncSupported)
}

// openDir opens the named directory read-only. Where the platform cannot
// sync a directory handle (syncSupported false), the handle's Sync is a
// no-op, as in Pebble's own Windows directory handle: FlushFileBuffers on a
// directory fails there, and NTFS makes a directory entry durable with the
// metadata of the file it names.
func openDir(name string, syncSupported bool) (File, error) {
	d, err := openOSFile(name, os.O_RDONLY)
	if err != nil || syncSupported {
		return d, err
	}
	return unsyncableDir{File: d}, nil
}

// unsyncableDir is a directory handle whose Sync does nothing, for a
// platform that cannot flush a directory handle.
type unsyncableDir struct {
	File
}

// Sync does nothing: the platform cannot flush a directory handle.
func (unsyncableDir) Sync() error { return nil }

func (osFS) Link(oldname, newname string) error   { return os.Link(oldname, newname) }
func (osFS) Remove(name string) error             { return os.Remove(name) }
func (osFS) RemoveAll(name string) error          { return os.RemoveAll(name) }
func (osFS) Rename(oldname, newname string) error { return os.Rename(oldname, newname) }

func (osFS) MkdirAll(dir string, perm os.FileMode) error { return os.MkdirAll(dir, perm) }

func (osFS) List(dir string) ([]string, error) {
	f, err := os.Open(dir) // #nosec G304 -- the caller names its own directory
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return names, nil
}

func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// openOSFile opens name with flag, returning a nil File (not a nil
// *os.File in a non-nil interface) on error.
func openOSFile(name string, flag int) (File, error) {
	f, err := os.OpenFile(name, flag, filePerm) // #nosec G304 -- the caller names its own file
	if err != nil {
		return nil, err
	}
	return f, nil
}
