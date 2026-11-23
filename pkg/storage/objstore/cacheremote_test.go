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
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/require"
)

// countingRemote counts the ReadObject calls that reach the wrapped storage.
type countingRemote struct {
	remote.Storage
	reads atomic.Int64
}

func (c *countingRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	c.reads.Add(1)
	return c.Storage.ReadObject(ctx, objName)
}

// errFailingClose is the error failingCloseRemote's writers return on Close.
var errFailingClose = errors.New("test: inner writer close failed")

// failingCloseRemote hands out writers whose Close fails without storing.
type failingCloseRemote struct {
	remote.Storage
}

func (f *failingCloseRemote) CreateObject(string) (io.WriteCloser, error) {
	return failingCloseWriter{}, nil
}

type failingCloseWriter struct{}

func (failingCloseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (failingCloseWriter) Close() error                { return errFailingClose }

func newTestCachedRemote(t *testing.T, inner remote.Storage, dir string) *CachedRemote {
	t.Helper()
	c, err := NewCachedRemote(inner, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// cacheDirBytes returns the concatenated bytes of every file in dir.
func cacheDirBytes(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []byte
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out = append(out, b...)
	}
	return out
}

func TestNewCachedRemote_Refusals(t *testing.T) {
	_, err := NewCachedRemote(nil, t.TempDir())
	var ie *InvalidCachedRemoteError
	require.ErrorAs(t, err, &ie)
	require.Contains(t, err.Error(), "nil inner storage")

	_, err = NewCachedRemote(newTestRemote(t, memory.New(), "db1"), "")
	require.ErrorAs(t, err, &ie)
	require.Contains(t, err.Error(), "empty dir")
}

func TestNewCachedRemote_RemovesTemporaries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x.tmp"), []byte("partial"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001.sst"), []byte("kept"), 0o600))
	newTestCachedRemote(t, newTestRemote(t, memory.New(), "db1"), dir)
	_, err := os.Stat(filepath.Join(dir, "x.tmp"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "000001.sst"))
	require.NoError(t, err)
}

func TestNewCachedRemote_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "cache")
	newTestCachedRemote(t, newTestRemote(t, memory.New(), "db1"), dir)
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, fi.IsDir())
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

func TestCachedRemote_ReadFetchesOnce(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	data := []byte("0123456789abcdef")
	putObject(t, rs, "000001.sst", data)
	counting := &countingRemote{Storage: rs}
	c := newTestCachedRemote(t, counting, t.TempDir())

	for i := range 2 {
		rd, size, err := c.ReadObject(context.Background(), "000001.sst")
		require.NoError(t, err, "read %d", i)
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
			p := make([]byte, tc.n)
			err := rd.ReadAt(context.Background(), p, tc.off)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err, "read %d %s", i, tc.name)
				continue
			}
			require.NoError(t, err, "read %d %s", i, tc.name)
			require.Equal(t, tc.want, string(p), "read %d %s", i, tc.name)
		}
		require.NoError(t, rd.Close())
		require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), errReaderClosed)
		require.NoError(t, rd.Close())
	}
	require.Equal(t, int64(1), counting.reads.Load())

	got, err := c.Size("000001.sst")
	require.NoError(t, err)
	want, err := rs.Size("000001.sst")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestCachedRemote_EmptyObject(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "empty.sst", nil)
	counting := &countingRemote{Storage: rs}
	c := newTestCachedRemote(t, counting, t.TempDir())
	for range 2 {
		rd, size, err := c.ReadObject(context.Background(), "empty.sst")
		require.NoError(t, err)
		require.Zero(t, size)
		require.NoError(t, rd.ReadAt(context.Background(), nil, 0))
		require.ErrorIs(t, rd.ReadAt(context.Background(), make([]byte, 1), 0), io.EOF)
		require.NoError(t, rd.Close())
	}
	require.Equal(t, int64(1), counting.reads.Load())
}

func TestCachedRemote_CreateWritesThrough(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	counting := &countingRemote{Storage: rs}
	c := newTestCachedRemote(t, counting, t.TempDir())
	w, err := c.CreateObject("000002.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("hello "))
	require.NoError(t, err)
	_, err = w.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	_, err = w.Write([]byte("x"))
	require.ErrorIs(t, err, errWriterClosed)

	rd, size, err := c.ReadObject(context.Background(), "000002.sst")
	require.NoError(t, err)
	require.Equal(t, int64(11), size)
	p := make([]byte, 11)
	require.NoError(t, rd.ReadAt(context.Background(), p, 0))
	require.Equal(t, "hello world", string(p))
	require.NoError(t, rd.Close())
	require.Zero(t, counting.reads.Load())

	// The store holds the object too.
	srd, _, err := rs.ReadObject(context.Background(), "000002.sst")
	require.NoError(t, err)
	require.NoError(t, srd.ReadAt(context.Background(), p, 0))
	require.Equal(t, "hello world", string(p))
	require.NoError(t, srd.Close())
}

func TestCachedRemote_InnerCloseFailureCachesNothing(t *testing.T) {
	dir := t.TempDir()
	c := newTestCachedRemote(t, &failingCloseRemote{Storage: newTestRemote(t, memory.New(), "db1")}, dir)
	w, err := c.CreateObject("000003.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("lost"))
	require.NoError(t, err)
	require.ErrorIs(t, w.Close(), errFailingClose)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCachedRemote_Delete(t *testing.T) {
	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	c := newTestCachedRemote(t, rs, dir)
	w, err := c.CreateObject("000004.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("doomed"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	_, err = c.root.Stat(c.name("000004.sst"))
	require.NoError(t, err)

	require.NoError(t, c.Delete("000004.sst"))
	_, err = c.root.Stat(c.name("000004.sst"))
	require.True(t, os.IsNotExist(err))
	_, err = rs.Size("000004.sst")
	require.True(t, rs.IsNotExistError(err))

	_, _, err = c.ReadObject(context.Background(), "000004.sst")
	require.Error(t, err)
	require.True(t, c.IsNotExistError(err))
	_, err = c.Size("000004.sst")
	require.True(t, c.IsNotExistError(err))
}

func TestCachedRemote_ListDelegates(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "a/1.sst", []byte("1"))
	putObject(t, rs, "a/2.sst", []byte("2"))
	c := newTestCachedRemote(t, rs, t.TempDir())
	want, err := rs.List("a/", "")
	require.NoError(t, err)
	got, err := c.List("a/", "")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.NoError(t, c.Close())
}

func TestCachedRemote_SlashedNameStaysInDir(t *testing.T) {
	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "sub/dir/000005.sst", []byte("nested"))
	c := newTestCachedRemote(t, rs, dir)
	rd, size, err := c.ReadObject(context.Background(), "sub/dir/000005.sst")
	require.NoError(t, err)
	require.Equal(t, int64(6), size)
	require.NoError(t, rd.Close())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].Type().IsRegular())
	require.Equal(t, url.PathEscape("sub/dir/000005.sst"), entries[0].Name())

	rd, _, err = c.ReadObject(context.Background(), "sub/dir/000005.sst")
	require.NoError(t, err)
	p := make([]byte, 6)
	require.NoError(t, rd.ReadAt(context.Background(), p, 0))
	require.Equal(t, "nested", string(p))
	require.NoError(t, rd.Close())
}

func TestCachedRemote_UnderEncryptedRemote(t *testing.T) {
	barrier, _ := newUnsealedBarrier(t)
	store := memory.New()
	rs := newTestRemote(t, store, "tenant/db1")
	plaintext := []byte("plaintext-sstable-bytes")

	// Put the object sealed, bypassing the cache.
	direct, err := NewEncryptedRemote(rs, barrier)
	require.NoError(t, err)
	w, err := direct.CreateObject("000006.sst")
	require.NoError(t, err)
	_, err = w.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	dir := t.TempDir()
	counting := &countingRemote{Storage: rs}
	enc, err := NewEncryptedRemote(newTestCachedRemote(t, counting, dir), barrier)
	require.NoError(t, err)
	for range 2 {
		size, err := enc.Size("000006.sst")
		require.NoError(t, err)
		require.Equal(t, int64(len(plaintext)), size)
	}
	require.Equal(t, int64(1), counting.reads.Load())

	rd, _, err := enc.ReadObject(context.Background(), "000006.sst")
	require.NoError(t, err)
	p := make([]byte, len(plaintext))
	require.NoError(t, rd.ReadAt(context.Background(), p, 0))
	require.Equal(t, plaintext, p)
	require.NoError(t, rd.Close())
	require.Equal(t, int64(1), counting.reads.Load())

	// A write-through object lands sealed in the cache too.
	w, err = enc.CreateObject("000007.sst")
	require.NoError(t, err)
	_, err = w.Write(plaintext)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	cached := cacheDirBytes(t, dir)
	require.NotEmpty(t, cached)
	require.False(t, bytes.Contains(cached, plaintext))
}

func TestPebbleRoundTrip_CachedRemote(t *testing.T) {
	dir := t.TempDir()
	c := newTestCachedRemote(t, newTestRemote(t, memory.New(), "db1"), dir)
	pebbleRoundTrip(t, c, func([]string) {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.NotEmpty(t, entries)
	})
}

// blockingRemote holds every ReadObject that reaches the wrapped storage
// until release is closed: it signals entry on entered first, and counts the
// calls.
type blockingRemote struct {
	remote.Storage
	entered chan struct{}
	release chan struct{}
	reads   atomic.Int64
}

func newBlockingRemote(inner remote.Storage) *blockingRemote {
	return &blockingRemote{Storage: inner, entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (b *blockingRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	b.reads.Add(1)
	b.entered <- struct{}{}
	<-b.release
	return b.Storage.ReadObject(ctx, objName)
}

// allCallsRemote counts every call of the methods a CachedRemote forwards
// per object name.
type allCallsRemote struct {
	remote.Storage
	calls atomic.Int64
}

func (a *allCallsRemote) ReadObject(ctx context.Context, objName string) (remote.ObjectReader, int64, error) {
	a.calls.Add(1)
	return a.Storage.ReadObject(ctx, objName)
}

func (a *allCallsRemote) CreateObject(objName string) (io.WriteCloser, error) {
	a.calls.Add(1)
	return a.Storage.CreateObject(objName)
}

func (a *allCallsRemote) Delete(objName string) error {
	a.calls.Add(1)
	return a.Storage.Delete(objName)
}

func (a *allCallsRemote) Size(objName string) (int64, error) {
	a.calls.Add(1)
	return a.Storage.Size(objName)
}

// requireLocksReleased asserts no per-object lock entry is left.
func requireLocksReleased(t *testing.T, c *CachedRemote) {
	t.Helper()
	c.mu.Lock()
	n := len(c.locks)
	c.mu.Unlock()
	require.Zero(t, n)
}

// readAll reads the whole object objName through c.
func readAll(c *CachedRemote, objName string) ([]byte, error) {
	rd, size, err := c.ReadObject(context.Background(), objName)
	if err != nil {
		return nil, err
	}
	p := make([]byte, size)
	err = rd.ReadAt(context.Background(), p, 0)
	if cerr := rd.Close(); err == nil {
		err = cerr
	}
	return p, err
}

func TestCachedRemote_ConcurrentFirstReadsFetchOnce(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	data := []byte("concurrent-object")
	putObject(t, rs, "000010.sst", data)
	b := newBlockingRemote(rs)
	c := newTestCachedRemote(t, b, t.TempDir())

	const n = 8
	var wg sync.WaitGroup
	got := make([][]byte, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = readAll(c, "000010.sst")
		}()
	}
	<-b.entered
	close(b.release)
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i], "reader %d", i)
		require.Equal(t, data, got[i], "reader %d", i)
	}
	require.Equal(t, int64(1), b.reads.Load())
	requireLocksReleased(t, c)
}

func TestCachedRemote_DeleteDuringFetchLeavesNoCacheFile(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	data := []byte("raced")
	putObject(t, rs, "000011.sst", data)
	b := newBlockingRemote(rs)
	c := newTestCachedRemote(t, b, t.TempDir())

	readDone := make(chan struct{})
	var readGot []byte
	var readErr error
	go func() {
		defer close(readDone)
		readGot, readErr = readAll(c, "000011.sst")
	}()
	<-b.entered

	delDone := make(chan struct{})
	var delErr error
	go func() {
		defer close(delDone)
		delErr = c.Delete("000011.sst")
	}()
	select {
	case <-delDone:
		t.Fatal("Delete returned while a fetch of the object held its lock")
	default:
	}
	close(b.release)
	<-readDone
	<-delDone

	require.NoError(t, readErr)
	require.Equal(t, data, readGot)
	require.NoError(t, delErr)
	_, err := c.root.Stat(c.name("000011.sst"))
	require.True(t, os.IsNotExist(err))
	_, err = c.Size("000011.sst")
	require.True(t, c.IsNotExistError(err))
	_, _, err = c.ReadObject(context.Background(), "000011.sst")
	require.True(t, c.IsNotExistError(err))
	requireLocksReleased(t, c)
}

// commitBlockingRemote hands out writers whose Close commits to the wrapped
// storage, signals committed and then holds until release is closed.
type commitBlockingRemote struct {
	remote.Storage
	committed chan struct{}
	release   chan struct{}
}

func (b *commitBlockingRemote) CreateObject(objName string) (io.WriteCloser, error) {
	w, err := b.Storage.CreateObject(objName)
	if err != nil {
		return nil, err
	}
	return &commitBlockingWriter{WriteCloser: w, b: b}, nil
}

type commitBlockingWriter struct {
	io.WriteCloser
	b *commitBlockingRemote
}

func (w *commitBlockingWriter) Close() error {
	err := w.WriteCloser.Close()
	w.b.committed <- struct{}{}
	<-w.b.release
	return err
}

// lockWaiters reports whether objName's lock has at least n references: its
// holder and the callers queued on it.
func lockWaiters(c *CachedRemote, objName string, n int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[objName]
	return ok && l.refs >= n
}

func TestCachedRemote_DeleteDuringWriterCloseLeavesNoStaleCache(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	b := &commitBlockingRemote{Storage: rs, committed: make(chan struct{}, 1), release: make(chan struct{})}
	c := newTestCachedRemote(t, b, t.TempDir())

	w, err := c.CreateObject("000012.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("written"))
	require.NoError(t, err)

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		defer close(closeDone)
		closeErr = w.Close()
	}()
	<-b.committed

	delDone := make(chan struct{})
	var delErr error
	go func() {
		defer close(delDone)
		delErr = c.Delete("000012.sst")
	}()
	// Delete either returns, which is the defect, or queues on the object's
	// lock the writer's Close holds (a second reference on it).
	for !lockWaiters(c, "000012.sst", 2) {
		select {
		case <-delDone:
			t.Fatal("Delete returned while a writer's Close of the object held its lock")
		default:
		}
		runtime.Gosched()
	}
	close(b.release)
	<-closeDone
	<-delDone

	require.NoError(t, closeErr)
	require.NoError(t, delErr)
	_, err = c.root.Stat(c.name("000012.sst"))
	require.True(t, os.IsNotExist(err))
	_, err = c.Size("000012.sst")
	require.True(t, c.IsNotExistError(err))
	_, _, err = c.ReadObject(context.Background(), "000012.sst")
	require.True(t, c.IsNotExistError(err))
	requireLocksReleased(t, c)
}

func TestCachedRemote_RefusesUnmappableNames(t *testing.T) {
	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	inner := &allCallsRemote{Storage: rs}
	c := newTestCachedRemote(t, inner, dir)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)

	ops := []struct {
		name string
		call func(string) error
	}{
		{"ReadObject", func(n string) error { _, _, err := c.ReadObject(context.Background(), n); return err }},
		{"CreateObject", func(n string) error { _, err := c.CreateObject(n); return err }},
		{"Delete", c.Delete},
		{"Size", func(n string) error { _, err := c.Size(n); return err }},
	}
	for _, name := range []string{"", ".", ".."} {
		for _, op := range ops {
			err := op.call(name)
			var ie *InvalidObjectNameError
			require.ErrorAs(t, err, &ie, "%s %q", op.name, name)
			require.Equal(t, name, ie.Name)
		}
	}
	require.Zero(t, inner.calls.Load())
	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, before, after)
	requireLocksReleased(t, c)

	putObject(t, rs, "sub/dir/x.sst", []byte("ok"))
	got, err := readAll(c, "sub/dir/x.sst")
	require.NoError(t, err)
	require.Equal(t, "ok", string(got))
}

func TestCachedRemote_LocksReleased(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "000012.sst", []byte("a"))
	c := newTestCachedRemote(t, rs, t.TempDir())

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = readAll(c, "000012.sst")
			w, err := c.CreateObject("000013.sst")
			if err == nil {
				_, _ = w.Write([]byte("b"))
				_ = w.Close()
			}
			_, _ = c.Size("000013.sst")
		}()
	}
	wg.Wait()
	require.NoError(t, c.Delete("000012.sst"))
	require.NoError(t, c.Delete("000013.sst"))
	requireLocksReleased(t, c)
}

// A symlink planted in the cache dir under a valid object's cache name never
// serves the bytes it points at: the root refuses to follow a link that leads
// outside the cache dir, so the read fails, and the outside file is untouched.
func TestCachedRemote_SymlinkOutsideDirRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	secret := []byte("outside-the-cache-dir")
	require.NoError(t, os.WriteFile(outside, secret, 0o600))

	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "000020.sst", []byte("inner-object"))
	c := newTestCachedRemote(t, rs, dir)
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, url.PathEscape("000020.sst"))))

	got, err := readAll(c, "000020.sst")
	require.Error(t, err)
	require.NotContains(t, string(got), string(secret))
	_, err = c.Size("000020.sst")
	require.Error(t, err)

	after, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, secret, after)
	requireLocksReleased(t, c)
}

func TestCachedRemote_CloseReleasesRoot(t *testing.T) {
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "000021.sst", []byte("cached"))
	c := newTestCachedRemote(t, rs, t.TempDir())
	got, err := readAll(c, "000021.sst")
	require.NoError(t, err)
	require.Equal(t, "cached", string(got))

	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
	_, _, err = c.ReadObject(context.Background(), "000021.sst")
	require.Error(t, err)
}

// TestCachedRemote_SecondOpenLeavesALiveWritersTempFile pins that a
// CachedRemote owns its directory before it sweeps temporaries: a second
// open on a directory a live instance holds fails and unlinks nothing, so
// the live instance's in-flight temp file and its next publication survive.
func TestCachedRemote_SecondOpenLeavesALiveWritersTempFile(t *testing.T) {
	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	counting := &countingRemote{Storage: rs}
	live := newTestCachedRemote(t, counting, dir)

	f, tmp, err := live.createTemp()
	require.NoError(t, err)
	_, err = f.Write([]byte("in flight"))
	require.NoError(t, err)

	second, err := NewCachedRemote(rs, dir)
	require.Error(t, err)
	require.Nil(t, second)
	_, err = os.Stat(filepath.Join(dir, tmp))
	require.NoError(t, err, "the live instance's temp file survives a second open")
	require.NoError(t, f.Close())
	require.NoError(t, live.root.Remove(tmp))

	w, err := live.CreateObject("000023.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("published"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	got, err := readAll(live, "000023.sst")
	require.NoError(t, err)
	require.Equal(t, "published", string(got))
	require.Zero(t, counting.reads.Load(), "the write was published to the cache")

	require.NoError(t, live.Close())
	reopened, err := NewCachedRemote(rs, dir)
	require.NoError(t, err, "Close releases the directory")
	require.NoError(t, reopened.Close())
}

func TestCachedRemote_StoreLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	rs := newTestRemote(t, memory.New(), "db1")
	putObject(t, rs, "sub/000022.sst", []byte("fetched"))
	c := newTestCachedRemote(t, rs, dir)

	w, err := c.CreateObject("000023.sst")
	require.NoError(t, err)
	_, err = w.Write([]byte("written"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	got, err := readAll(c, "sub/000022.sst")
	require.NoError(t, err)
	require.Equal(t, "fetched", string(got))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), cacheTempSuffix), e.Name())
		names = append(names, e.Name())
	}
	require.ElementsMatch(t, []string{url.PathEscape("000023.sst"), url.PathEscape("sub/000022.sst")}, names)
}
