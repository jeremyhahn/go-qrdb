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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"
	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

func newTestRemote(t *testing.T, store objcommon.Storage, prefix string) *Remote {
	t.Helper()
	r, err := NewRemote(store, prefix)
	require.NoError(t, err)
	return r
}

func putObject(t *testing.T, r *Remote, name string, data []byte) {
	t.Helper()
	w, err := r.CreateObject(name)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}

func TestNewRemote_Refusals(t *testing.T) {
	store := memory.New()
	tests := []struct {
		name   string
		store  objcommon.Storage
		prefix string
		reason string
	}{
		{"nil store", nil, "db1", "nil store"},
		{"empty prefix", store, "", "empty prefix"},
		{"leading slash", store, "/db1", "leading /"},
		{"NUL", store, "db\x001", "NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRemote(tc.store, tc.prefix)
			require.Nil(t, r)
			var ire *InvalidRemoteError
			require.ErrorAs(t, err, &ire)
			require.Equal(t, tc.prefix, ire.Prefix)
			require.Contains(t, ire.Error(), tc.reason)
		})
	}
	r, err := NewRemote(store, "tenant/db1")
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRemote_CreateReadRoundTrip(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")
	data := []byte("0123456789abcdef")
	putObject(t, r, "000001.sst", data)

	// The object is stored as <prefix>/<objName>.
	ok, err := store.Exists(context.Background(), "db1/000001.sst")
	require.NoError(t, err)
	require.True(t, ok)

	rd, size, err := r.ReadObject(context.Background(), "000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), size)

	tests := []struct {
		name string
		off  int64
		n    int
		want string
		err  error
	}{
		{"offset 0", 0, 4, "0123", nil},
		{"mid", 6, 5, "6789a", nil},
		{"up to end", 12, 4, "cdef", nil},
		{"empty at end", 16, 0, "", nil},
		{"past end", 14, 4, "", io.EOF},
		{"wholly past end", 20, 1, "", io.EOF},
		{"negative offset", -1, 1, "", errNegativeOffset},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := make([]byte, tc.n)
			err := rd.ReadAt(context.Background(), p, tc.off)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, string(p))
		})
	}

	require.NoError(t, rd.Close())
	require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), errReaderClosed)
	require.NoError(t, rd.Close())

	got, err := r.Size("000001.sst")
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), got)
}

func TestRemote_EmptyObject(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "empty", nil)
	rd, size, err := r.ReadObject(context.Background(), "empty")
	require.NoError(t, err)
	require.Zero(t, size)
	require.NoError(t, rd.ReadAt(context.Background(), nil, 0))
	require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), io.EOF)
	require.NoError(t, rd.Close())
}

func TestRemote_CreateObjectOverwrites(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "obj", []byte("first version"))
	putObject(t, r, "obj", []byte("second"))
	rd, size, err := r.ReadObject(context.Background(), "obj")
	require.NoError(t, err)
	require.Equal(t, int64(len("second")), size)
	p := make([]byte, size)
	require.NoError(t, rd.ReadAt(context.Background(), p, 0))
	require.Equal(t, "second", string(p))
	require.NoError(t, rd.Close())
}

func TestRemote_NotExist(t *testing.T) {
	r := newTestRemote(t, memory.New(), "db1")
	putObject(t, r, "gone", []byte("x"))
	require.NoError(t, r.Delete("gone"))

	_, _, err := r.ReadObject(context.Background(), "gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "ReadObject: %v", err)

	_, err = r.Size("gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "Size: %v", err)

	err = r.Delete("gone")
	require.Error(t, err)
	require.True(t, r.IsNotExistError(err), "Delete: %v", err)

	require.False(t, r.IsNotExistError(nil))
	require.False(t, r.IsNotExistError(errors.New("other")))
}

func TestRemote_List(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")
	for _, n := range []string{"a", "b/4", "b/5", "b/6", "c/x/1", "c/y"} {
		putObject(t, r, n, []byte(n))
	}
	// An object outside the prefix that shares its leading characters.
	require.NoError(t, store.PutWithContext(context.Background(), "db10/a", bytes.NewReader([]byte("other"))))

	tests := []struct {
		prefix, delim string
		want          []string
	}{
		{"", "", []string{"a", "b/4", "b/5", "b/6", "c/x/1", "c/y"}},
		{"", "/", []string{"a", "b/", "c/"}},
		{"b", "", []string{"b/4", "b/5", "b/6"}},
		{"b/", "/", []string{"b/4", "b/5", "b/6"}},
		{"c/", "/", []string{"c/x/", "c/y"}},
		{"z", "", []string{}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%q/%q", tc.prefix, tc.delim), func(t *testing.T) {
			got, err := r.List(tc.prefix, tc.delim)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRemote_PrefixIsolation proves OS-3: two Remotes on one store under
// different prefixes see only their own objects.
func TestRemote_PrefixIsolation(t *testing.T) {
	store := memory.New()
	r1 := newTestRemote(t, store, "db1")
	r2 := newTestRemote(t, store, "db2")
	putObject(t, r1, "000001.sst", []byte("one"))
	putObject(t, r2, "000002.sst", []byte("two"))

	got1, err := r1.List("", "")
	require.NoError(t, err)
	require.Equal(t, []string{"000001.sst"}, got1)
	got2, err := r2.List("", "")
	require.NoError(t, err)
	require.Equal(t, []string{"000002.sst"}, got2)

	_, _, err = r1.ReadObject(context.Background(), "000002.sst")
	require.True(t, r1.IsNotExistError(err), "%v", err)
	require.True(t, r2.IsNotExistError(r2.Delete("000001.sst")))
}

func TestRemote_CloseSemantics(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "db1")

	w, err := r.CreateObject("obj")
	require.NoError(t, err)
	_, err = w.Write([]byte("abc"))
	require.NoError(t, err)

	// Nothing is stored before Close.
	ok, err := store.Exists(context.Background(), "db1/obj")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	_, err = w.Write([]byte("d"))
	require.ErrorIs(t, err, errWriterClosed)

	got, err := r.Size("obj")
	require.NoError(t, err)
	require.Equal(t, int64(3), got)

	// Closing the Remote leaves the shared store usable.
	require.NoError(t, r.Close())
	ok, err = store.Exists(context.Background(), "db1/obj")
	require.NoError(t, err)
	require.True(t, ok)
	r2 := newTestRemote(t, store, "db1")
	got, err = r2.Size("obj")
	require.NoError(t, err)
	require.Equal(t, int64(3), got)
}

// pebbleOnRemote opens a Pebble DB on fs whose SSTables are all created on
// the shared storage rs.
func pebbleOnRemote(t *testing.T, fs vfs.FS, rs remote.Storage) *pebble.DB {
	t.Helper()
	opts := &pebble.Options{
		FS:                 fs,
		FormatMajorVersion: pebble.FormatMinForSharedObjects,
	}
	opts.Experimental.RemoteStorage = remote.MakeSimpleFactory(map[remote.Locator]remote.Storage{"bucket": rs})
	opts.Experimental.CreateOnShared = remote.CreateOnSharedAll
	opts.Experimental.CreateOnSharedLocator = "bucket"
	db, err := pebble.Open("db", opts)
	require.NoError(t, err)
	return db
}

// pebbleRoundTrip writes keys through Pebble on rs, flushes, reopens on the
// same vfs and storage and reads every key back. afterFlush runs between the
// flush and the close.
func pebbleRoundTrip(t *testing.T, rs remote.Storage, afterFlush func(keys []string)) {
	t.Helper()
	fs := vfs.NewMem()
	db := pebbleOnRemote(t, fs, rs)
	require.NoError(t, db.SetCreatorID(1))
	keys := make([]string, 0, 50)
	for i := range 50 {
		k := fmt.Sprintf("remote-key-%03d", i)
		keys = append(keys, k)
		require.NoError(t, db.Set([]byte(k), []byte("value-"+k), pebble.Sync))
	}
	require.NoError(t, db.Flush())
	afterFlush(keys)
	require.NoError(t, db.Close())

	db = pebbleOnRemote(t, fs, rs)
	defer func() { require.NoError(t, db.Close()) }()
	for _, k := range keys {
		v, closer, err := db.Get([]byte(k))
		require.NoError(t, err, k)
		require.Equal(t, "value-"+k, string(v))
		require.NoError(t, closer.Close())
	}
}

// TestPebbleRoundTrip_InMemHarness proves the harness on Pebble's own
// in-memory remote storage.
func TestPebbleRoundTrip_InMemHarness(t *testing.T) {
	rs := remote.NewInMem()
	pebbleRoundTrip(t, rs, func([]string) {
		names, err := rs.List("", "")
		require.NoError(t, err)
		require.NotEmpty(t, names)
	})
}

// TestPebbleRoundTrip_Remote runs Pebble with every SSTable on the adapter
// over the go-objstore memory provider.
func TestPebbleRoundTrip_Remote(t *testing.T) {
	store := memory.New()
	r := newTestRemote(t, store, "tenant/db1")
	pebbleRoundTrip(t, r, func(keys []string) {
		names, err := store.ListWithContext(context.Background(), "tenant/db1/")
		require.NoError(t, err)
		ssts := 0
		for _, n := range names {
			if strings.HasSuffix(n, ".sst") {
				ssts++
			}
			for _, k := range keys {
				require.NotContains(t, n, k)
			}
		}
		require.Positive(t, ssts, "objects: %v", names)
		all, err := store.ListWithContext(context.Background(), "")
		require.NoError(t, err)
		require.Equal(t, len(names), len(all), "every object is under the prefix")
	})
}

// errTestFault is the error the fault doubles below inject.
var errTestFault = errors.New("test: injected fault")

// remoteFaultStore is a go-objstore Storage whose Get bodies fail to read or
// close, whose List fails, or whose List also returns keys outside the
// prefix asked for, as a provider with a looser prefix match would.
type remoteFaultStore struct {
	objcommon.Storage
	readErr, closeErr, listErr error
	extraKeys                  []string
}

type faultBody struct {
	r                 io.Reader
	readErr, closeErr error
}

func (b *faultBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.r.Read(p)
}

func (b *faultBody) Close() error { return b.closeErr }

func (s *remoteFaultStore) GetWithContext(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.Storage.GetWithContext(ctx, key)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return nil, err
	}
	return &faultBody{r: bytes.NewReader(data), readErr: s.readErr, closeErr: s.closeErr}, nil
}

func (s *remoteFaultStore) ListWithContext(ctx context.Context, prefix string) ([]string, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	keys, err := s.Storage.ListWithContext(ctx, prefix)
	return append(keys, s.extraKeys...), err
}

func TestRemote_StoreFaults(t *testing.T) {
	ctx := context.Background()
	st := &remoteFaultStore{Storage: memory.New()}
	r := newTestRemote(t, st, "db1")
	putRemoteObject(t, r, "obj", []byte("payload"))

	st.readErr = errTestFault
	_, _, err := r.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	st.readErr = nil

	st.closeErr = errTestFault
	_, _, err = r.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	st.closeErr = nil

	st.listErr = errTestFault
	_, err = r.List("", "")
	require.ErrorIs(t, err, errTestFault)
	st.listErr = nil

	st.extraKeys = []string{"db10/obj", "other/obj"}
	names, err := r.List("", "")
	require.NoError(t, err)
	require.Equal(t, []string{"obj"}, names)
}

// faultRemote is a Pebble remote.Storage whose readers fail ReadAt or Close,
// whose CreateObject fails or returns a writer failing Write, or whose
// Delete fails.
type faultRemote struct {
	remote.Storage
	readAtErr, readerCloseErr, createErr, writeErr, deleteErr error
}

type faultReader struct {
	remote.ObjectReader
	readAtErr, closeErr error
}

func (r *faultReader) ReadAt(ctx context.Context, p []byte, off int64) error {
	if r.readAtErr != nil {
		return r.readAtErr
	}
	return r.ObjectReader.ReadAt(ctx, p, off)
}

func (r *faultReader) Close() error {
	err := r.ObjectReader.Close()
	if r.closeErr != nil {
		return r.closeErr
	}
	return err
}

type faultWriter struct {
	io.WriteCloser
	err error
}

func (w *faultWriter) Write([]byte) (int, error) { return 0, w.err }

func (f *faultRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	rd, size, err := f.Storage.ReadObject(ctx, objName)
	if err != nil {
		return nil, 0, err
	}
	return &faultReader{ObjectReader: rd, readAtErr: f.readAtErr, closeErr: f.readerCloseErr}, size, nil
}

func (f *faultRemote) CreateObject(objName string) (io.WriteCloser, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	w, err := f.Storage.CreateObject(objName)
	if err != nil {
		return nil, err
	}
	if f.writeErr != nil {
		return &faultWriter{WriteCloser: w, err: f.writeErr}, nil
	}
	return w, nil
}

func (f *faultRemote) Delete(objName string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.Storage.Delete(objName)
}

func TestCachedRemote_InnerFaults(t *testing.T) {
	ctx := context.Background()
	inner := newTestRemote(t, memory.New(), "db1")
	putRemoteObject(t, inner, "obj", []byte("payload"))
	fr := &faultRemote{Storage: inner}
	dir := t.TempDir()
	c := newTestCachedRemote(t, fr, dir)

	fr.readAtErr = errTestFault
	_, _, err := c.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	fr.readAtErr = nil

	fr.readerCloseErr = errTestFault
	_, _, err = c.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	fr.readerCloseErr = nil
	require.Empty(t, cacheDirBytes(t, dir), "a failed fetch caches nothing")

	fr.createErr = errTestFault
	_, err = c.CreateObject("new")
	require.ErrorIs(t, err, errTestFault)
	fr.createErr = nil

	fr.deleteErr = errTestFault
	require.ErrorIs(t, c.Delete("obj"), errTestFault)
	fr.deleteErr = nil
	_, err = inner.Size("obj")
	require.NoError(t, err, "a failed inner delete keeps the object")
}

func TestEncryptedRemote_InnerFaults(t *testing.T) {
	ctx := context.Background()
	barrier, _ := newUnsealedBarrier(t)
	fr := &faultRemote{Storage: newTestRemote(t, memory.New(), "db1")}
	e, err := NewEncryptedRemote(fr, barrier)
	require.NoError(t, err)
	putRemoteObject(t, e, "obj", []byte("payload"))

	fr.readAtErr = errTestFault
	_, _, err = e.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	fr.readAtErr = nil

	fr.readerCloseErr = errTestFault
	_, _, err = e.ReadObject(ctx, "obj")
	require.ErrorIs(t, err, errTestFault)
	fr.readerCloseErr = nil

	for _, fault := range []*error{&fr.createErr, &fr.writeErr} {
		*fault = errTestFault
		w, err := e.CreateObject("new")
		require.NoError(t, err)
		_, err = w.Write([]byte("plain"))
		require.NoError(t, err)
		require.ErrorIs(t, w.Close(), errTestFault)
		*fault = nil
	}
}

// restoreMode puts dir's mode back to 0700 at test end, before TempDir's
// removal and the CachedRemote's Close.
func restoreMode(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) }) // #nosec G302 -- restores the directory mode t.TempDir and the CachedRemote's Close need; not a secret file's mode
}

// skipIfRoot skips a test that needs directory permission bits to deny
// access: root ignores them.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits deny nothing")
	}
}

func TestNewCachedRemote_DirFaults(t *testing.T) {
	inner := newTestRemote(t, memory.New(), "db1")

	t.Run("a file where the dir belongs", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		_, err := NewCachedRemote(inner, file)
		require.Error(t, err)
	})

	t.Run("unreadable dir", func(t *testing.T) {
		skipIfRoot(t)
		unreadable := t.TempDir()
		require.NoError(t, os.Chmod(unreadable, 0o000))
		restoreMode(t, unreadable)
		_, err := NewCachedRemote(inner, unreadable)
		require.ErrorIs(t, err, fs.ErrPermission)
	})

	t.Run("a temporary that cannot be removed", func(t *testing.T) {
		stuck := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(stuck, "left"+cacheTempSuffix, "child"), 0o700))
		_, err := NewCachedRemote(inner, stuck)
		require.Error(t, err)
	})
}

func TestCachedRemote_CacheDirFaults(t *testing.T) {
	ctx := context.Background()
	inner := newTestRemote(t, memory.New(), "db1")
	putRemoteObject(t, inner, "obj", []byte("payload"))

	t.Run("cache file name held by a directory", func(t *testing.T) {
		dir := t.TempDir()
		c := newTestCachedRemote(t, inner, dir)
		blocked := filepath.Join(dir, c.name("held"), "child")
		require.NoError(t, os.MkdirAll(blocked, 0o700))

		err := c.store("held", []byte("data"))
		require.Error(t, err)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "the temporary is removed after a failed rename")

		putRemoteObject(t, inner, "held", []byte("x"))
		require.Error(t, c.Delete("held"), "a cache entry that cannot be removed")
		_, err = inner.Size("held")
		require.True(t, inner.IsNotExistError(err), "the inner delete ran first")
	})

	t.Run("read-only cache dir", func(t *testing.T) {
		dir := t.TempDir()
		skipIfRoot(t)
		c := newTestCachedRemote(t, inner, dir)
		require.NoError(t, os.Chmod(dir, 0o500)) // #nosec G302 -- r-x: the cache directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		restoreMode(t, dir)

		_, _, err := c.createTemp()
		require.ErrorIs(t, err, fs.ErrPermission)
		require.ErrorIs(t, c.store("obj", []byte("x")), fs.ErrPermission)
		_, _, err = c.ReadObject(ctx, "obj")
		require.ErrorIs(t, err, fs.ErrPermission, "a cache write failure is returned")
	})

	t.Run("unlistable cache dir", func(t *testing.T) {
		dir := t.TempDir()
		skipIfRoot(t)
		c := newTestCachedRemote(t, inner, dir)
		require.NoError(t, os.Chmod(dir, 0o300)) // #nosec G302 -- -wx: the cache directory's own permission bits are the fault this negative-permission test injects, not a secret file's mode
		restoreMode(t, dir)
		require.ErrorIs(t, c.syncDir(), fs.ErrPermission)
	})
}

func TestWriteSyncClose_Faults(t *testing.T) {
	t.Run("write to a read-only file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		ro, err := os.Open(path)
		require.NoError(t, err)
		require.Error(t, writeSyncClose(ro, []byte("x")))
	})

	t.Run("fsync of a device", func(t *testing.T) {
		// Linux /dev/null takes writes but refuses fsync (EINVAL); other
		// kernels may accept the fsync.
		if runtime.GOOS != "linux" {
			t.Skip("fsync of /dev/null fails only on Linux")
		}
		null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		require.NoError(t, err)
		require.Error(t, writeSyncClose(null, []byte("x")))
	})
}

func TestCacheFileReader_ShortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	// The recorded size passes the file's end, as when the file shrank.
	r := &cacheFileReader{f: f, size: 10}
	t.Cleanup(func() { _ = r.Close() })
	require.ErrorIs(t, r.ReadAt(context.Background(), make([]byte, 8), 0), io.EOF)
}

// readAtBoundsCases are the offsets a corrupt SSTable index can hand ReadAt:
// every one past the object's end, or overflowing off+len(p), must fail with
// an error and never panic.
func readAtBoundsCases(size int64) []struct {
	name string
	off  int64
	want error
} {
	return []struct {
		name string
		off  int64
		want error
	}{
		{"MaxInt64", math.MaxInt64, io.EOF},
		{"MaxInt64-1", math.MaxInt64 - 1, io.EOF},
		{"size", size, io.EOF},
		{"size+1", size + 1, io.EOF},
		{"negative", -1, errNegativeOffset},
	}
}

func TestRemoteReader_ReadAtBounds(t *testing.T) {
	data := []byte("0123456789")
	rr := &remoteReader{data: data}
	for _, tc := range readAtBoundsCases(int64(len(data))) {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, rr.ReadAt(context.Background(), make([]byte, 4), tc.off), tc.want)
		})
	}
	// A zero-length read at the end is in range.
	require.NoError(t, rr.ReadAt(context.Background(), nil, int64(len(data))))
}

func TestCacheFileReader_ReadAtBounds(t *testing.T) {
	data := []byte("0123456789")
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	r := &cacheFileReader{f: f, size: int64(len(data))}
	t.Cleanup(func() { _ = r.Close() })
	for _, tc := range readAtBoundsCases(int64(len(data))) {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, r.ReadAt(context.Background(), make([]byte, 4), tc.off), tc.want)
		})
	}
	require.NoError(t, r.ReadAt(context.Background(), nil, int64(len(data))))
}

func TestObjstoreErrors_Messages(t *testing.T) {
	require.Contains(t, (&InvalidObjectNameError{Name: "a/b", Reason: "why"}).Error(), `"a/b": why`)
	require.Contains(t, (&ArchiveTierUnsupportedError{Provider: "glacier"}).Error(), `"glacier" is an archive tier`)
}
