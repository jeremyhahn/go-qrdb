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

package objstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// cacheLockSuffix names the lock file a CachedRemote holds for as long as
// it owns its directory: the directory's own path plus this suffix, a
// sibling, so the directory itself holds only cached objects.
const cacheLockSuffix = ".lock"

// cacheTempSuffix ends the name of a cache file still being written.
const cacheTempSuffix = ".tmp"

// CachedRemote is the OS-6 A interim local object cache
// (docs/decisions/object-storage-lsm.md): go-objstore has no ranged read, so
// every Remote.ReadObject is a whole-object GET. CachedRemote keeps one local
// file per object under its directory and serves ReadObject and Size from it,
// so each object is fetched from the store at most once per cache dir.
//
// Operations on one object name are serialized by a per-name lock: concurrent
// first reads of an uncached object share one fetch, and a fetch racing Delete
// cannot store the cache file again after Delete removed it. Names that do
// not map to exactly one file directly in the cache dir (empty, ".", "..")
// are refused with an *InvalidObjectNameError before the inner storage is
// called.
//
// Pebble never rewrites an object under the same name, so a cached file stays
// valid until Delete removes it. CreateObject writes through: the bytes land in
// the cache once the inner writer's Close succeeds. Each file is written to a
// temporary name, fsynced, renamed into place and the cache dir fsynced, so a
// crash never leaves a partial object under a real name; leftover temporaries
// are removed by NewCachedRemote.
//
// Every cache file operation (open, stat, create, rename, remove, listing and
// the dir fsync) resolves inside the cache dir's *os.Root, so a symlink placed
// in the dir cannot lead a read or write outside it.
//
// The cache holds exactly the bytes the store holds, so it sits below
// EncryptedRemote: with the barrier on it holds ciphertext and nothing
// plaintext lands on local disk. It is unbounded and shrinks only by Delete
// until go-objstore gains a ranged read.
type CachedRemote struct {
	inner remote.Storage
	root  *os.Root
	// dirLock is the OS lock on dir+cacheLockSuffix, held from before the
	// temp-file sweep until Close, so a second CachedRemote on the same
	// directory fails instead of unlinking a live writer's temp file.
	dirLock io.Closer

	mu     sync.Mutex
	locks  map[string]*objLock
	closed bool
}

// objLock is the per-object-name lock of a CachedRemote; refs counts its
// holders and waiters, and the entry is dropped when it reaches zero.
type objLock struct {
	mu   sync.Mutex
	refs int
}

// lock takes the lock of objName and returns the func that releases it.
func (c *CachedRemote) lock(objName string) func() {
	c.mu.Lock()
	l, ok := c.locks[objName]
	if !ok {
		l = &objLock{}
		c.locks[objName] = l
	}
	l.refs++
	c.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		c.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(c.locks, objName)
		}
		c.mu.Unlock()
	}
}

// checkName refuses an object name whose cache file would not be exactly one
// file directly in the cache dir.
func (c *CachedRemote) checkName(objName string) error {
	esc := url.PathEscape(objName)
	switch {
	case objName == "":
		return &InvalidObjectNameError{Name: objName, Reason: "empty name"}
	case esc == "." || esc == "..":
		return &InvalidObjectNameError{Name: objName, Reason: "name is a directory reference"}
	case strings.ContainsRune(esc, '/') || strings.ContainsRune(esc, filepath.Separator):
		return &InvalidObjectNameError{Name: objName, Reason: "escaped name holds a path separator"}
	}
	return nil
}

var _ remote.Storage = (*CachedRemote)(nil)

// NewCachedRemote returns a CachedRemote over inner keeping its files in dir,
// which is created (0700) when missing. Temporaries a crash left in dir are
// removed. A nil inner or an empty dir is refused with an
// *InvalidCachedRemoteError.
func NewCachedRemote(inner remote.Storage, dir string) (*CachedRemote, error) {
	switch {
	case inner == nil:
		return nil, &InvalidCachedRemoteError{Dir: dir, Reason: "nil inner storage"}
	case dir == "":
		return nil, &InvalidCachedRemoteError{Dir: dir, Reason: "empty dir"}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// One directory has one lock, whatever its spelling: a relative path, an
	// absolute one or a symlink to it all resolve to the same canonical path,
	// so a second open of a live cache fails on the lock instead of sweeping
	// the live instance's temp files.
	canon, err := canonicalCacheDir(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canon)
	if err != nil {
		return nil, err
	}
	// Own the directory before touching its temp files: another live
	// CachedRemote on it may be mid-write.
	dirLock, err := vfs.Default.Lock(canon + cacheLockSuffix)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	fail := func(err error) (*CachedRemote, error) {
		return nil, errors.Join(err, dirLock.Close(), root.Close())
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fail(err)
	}
	for _, e := range entries {
		// A cached object whose escaped name ends in the suffix is removed
		// too; that costs one refetch, never a wrong read.
		if strings.HasSuffix(e.Name(), cacheTempSuffix) {
			if err := root.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fail(err)
			}
		}
	}
	return &CachedRemote{inner: inner, root: root, dirLock: dirLock, locks: make(map[string]*objLock)}, nil
}

// canonicalCacheDir returns the absolute, symlink-free path of the existing
// directory dir.
func canonicalCacheDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// name returns the name, in the cache dir's root, of the cache file of the
// object objName.
func (c *CachedRemote) name(objName string) string {
	return url.PathEscape(objName)
}

// ReadObject serves the object from its cache file, or fetches it whole from
// the inner storage, stores it in the cache and serves the fetched bytes. A
// miss takes the object's lock and re-checks the cache file, so concurrent
// first reads fetch once and a fetch never lands after a concurrent Delete. A
// cache write failure is returned; an unmappable name is refused with an
// *InvalidObjectNameError.
func (c *CachedRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	if err := c.checkName(objName); err != nil {
		return nil, 0, err
	}
	if rd, size, ok, err := c.openCached(objName); ok || err != nil {
		return rd, size, err
	}
	unlock := c.lock(objName)
	defer unlock()
	if rd, size, ok, err := c.openCached(objName); ok || err != nil {
		return rd, size, err
	}
	rd, size, err := c.inner.ReadObject(ctx, objName)
	if err != nil {
		return nil, 0, err
	}
	buf := make([]byte, size)
	err = rd.ReadAt(ctx, buf, 0)
	closeErr := rd.Close()
	if err != nil {
		return nil, 0, err
	}
	if closeErr != nil {
		return nil, 0, closeErr
	}
	if err := c.store(objName, buf); err != nil {
		return nil, 0, err
	}
	return &remoteReader{data: buf}, size, nil
}

// openCached opens the cache file of objName; ok is false when it is absent.
func (c *CachedRemote) openCached(objName string) (rd remote.ObjectReader, size int64, ok bool, err error) {
	f, err := c.root.Open(c.name(objName))
	switch {
	case err == nil:
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, false, err
		}
		return &cacheFileReader{f: f, size: fi.Size()}, fi.Size(), true, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, false, nil
	default:
		return nil, 0, false, err
	}
}

// CreateObject returns a writer that forwards to the inner storage's writer.
// Its Close runs the remote commit and, once that succeeds, the cache
// publication, both under the object's lock, so a Delete either precedes both
// or follows both. An unmappable name is refused with an
// *InvalidObjectNameError.
func (c *CachedRemote) CreateObject(objName string) (io.WriteCloser, error) {
	if err := c.checkName(objName); err != nil {
		return nil, err
	}
	w, err := c.inner.CreateObject(objName)
	if err != nil {
		return nil, err
	}
	return &cacheWriter{c: c, objName: objName, inner: w}, nil
}

// List delegates to the inner storage.
func (c *CachedRemote) List(prefix, delimiter string) ([]string, error) {
	return c.inner.List(prefix, delimiter)
}

// Delete removes the object from the inner storage and its cache file, both
// under the object's lock, so neither a concurrent ReadObject nor a writer's
// Close can store the file again afterwards. The inner error is returned first; a missing cache file is
// not an error. An unmappable name is refused with an *InvalidObjectNameError.
func (c *CachedRemote) Delete(objName string) error {
	if err := c.checkName(objName); err != nil {
		return err
	}
	unlock := c.lock(objName)
	err := c.inner.Delete(objName)
	rmErr := c.root.Remove(c.name(objName))
	unlock()
	if err != nil {
		return err
	}
	if rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return rmErr
	}
	return nil
}

// Size returns the size of the cache file when present, else the inner
// storage's size of the object. An unmappable name is refused with an
// *InvalidObjectNameError.
func (c *CachedRemote) Size(objName string) (int64, error) {
	if err := c.checkName(objName); err != nil {
		return 0, err
	}
	fi, err := c.root.Stat(c.name(objName))
	switch {
	case err == nil:
		return fi.Size(), nil
	case !errors.Is(err, fs.ErrNotExist):
		return 0, err
	}
	return c.inner.Size(objName)
}

// IsNotExistError delegates to the inner storage.
func (c *CachedRemote) IsNotExistError(err error) bool {
	return c.inner.IsNotExistError(err)
}

// Close closes the cache dir's root and returns its error; cache file
// operations after Close fail. A second Close is a no-op returning nil. The
// inner storage is shared and is not closed.
func (c *CachedRemote) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return errors.Join(c.dirLock.Close(), c.root.Close())
}

// cacheTempAttempts bounds the names createTemp tries before giving up.
const cacheTempAttempts = 10

// createTemp creates a new temporary file in the cache dir's root, named by 8
// random bytes in hex and cacheTempSuffix, and returns it with its name. A
// name that already exists is retried, at most cacheTempAttempts times; the
// last error is returned.
func (c *CachedRemote) createTemp() (*os.File, string, error) {
	var lastErr error
	for range cacheTempAttempts {
		var b [8]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
		name := hex.EncodeToString(b[:]) + cacheTempSuffix
		f, err := c.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
		lastErr = err
	}
	return nil, "", lastErr
}

// writeSyncClose writes data to f, fsyncs and closes it. On a Write or Sync
// error f is closed, that close error ignored, and the first error returned.
func writeSyncClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// store writes data as the cache file of objName: a temporary file in the
// cache dir's root, fsynced, closed and renamed into place, then the cache dir
// fsynced so the rename survives a crash. The temporary is removed on any
// failure before or at the rename. The caller holds the object's lock.
func (c *CachedRemote) store(objName string, data []byte) error {
	f, tmp, err := c.createTemp()
	if err != nil {
		return err
	}
	if err := writeSyncClose(f, data); err != nil {
		_ = c.root.Remove(tmp)
		return err
	}
	if err := c.root.Rename(tmp, c.name(objName)); err != nil {
		_ = c.root.Remove(tmp)
		return err
	}
	return c.syncDir()
}

// syncDir fsyncs the cache dir through its root, returning the first error of
// open, sync and close.
func (c *CachedRemote) syncDir() error {
	if !cacheDirSyncSupported {
		return nil
	}
	d, err := c.root.Open(".")
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// cacheFileReader serves ReadAt from a cache file, with the semantics of
// remoteReader.
type cacheFileReader struct {
	mu   sync.RWMutex
	f    *os.File
	size int64
}

// ReadAt copies len(p) bytes from offset off. A read that would pass the end
// of the object returns io.EOF and no partial result, as remoteReader does.
func (r *cacheFileReader) ReadAt(_ context.Context, p []byte, off int64) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.f == nil {
		return errReaderClosed
	}
	if off < 0 {
		return errNegativeOffset
	}
	// Compare against the bytes left after off, never off+len(p): that sum
	// overflows for an offset near MaxInt64.
	if off > r.size || int64(len(p)) > r.size-off {
		return io.EOF
	}
	// os.File.ReadAt returns a non-nil error whenever it reads short.
	if n, err := r.f.ReadAt(p, off); n < len(p) {
		return err
	}
	return nil
}

// Close closes the cache file. A second Close is a no-op returning nil.
func (r *cacheFileReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// cacheWriter forwards an object to the inner writer, buffering it for the
// cache.
type cacheWriter struct {
	c       *CachedRemote
	objName string
	inner   io.WriteCloser
	buf     bytes.Buffer
	closed  bool
}

// Write forwards p to the inner writer and buffers what it accepted.
func (w *cacheWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errWriterClosed
	}
	n, err := w.inner.Write(p)
	w.buf.Write(p[:n])
	return n, err
}

// Close takes the object's lock, closes the inner writer (the remote commit)
// and, only when that succeeds, stores the object in the cache, both under the
// lock: a concurrent Delete either precedes both, so the commit recreates the
// object, or follows both, so it removes the object and its cache file. A
// second Close is a no-op returning nil.
func (w *cacheWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	data := w.buf.Bytes()
	w.buf = bytes.Buffer{}
	unlock := w.c.lock(w.objName)
	defer unlock()
	if err := w.inner.Close(); err != nil {
		return err
	}
	return w.c.store(w.objName, data)
}
