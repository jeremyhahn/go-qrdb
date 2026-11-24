// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sealretire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openTestSealStore opens a seal store in a fresh directory and closes it
// at cleanup unless the test closed it first.
func openTestSealStore(t *testing.T) (*pebblestore.PebbleStorage, *Retirer) {
	t.Helper()
	store, retirer, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, retirer
}

func TestCompactor_NilStoreIsNoop(t *testing.T) {
	if err := Compactor(nil)(); err != nil {
		t.Fatalf("Compactor(nil)() = %v, want nil", err)
	}
}

func TestCompactor_EmptyStoreIsNoop(t *testing.T) {
	store, _ := openTestSealStore(t)
	if err := Compactor(store)(); err != nil {
		t.Fatalf("compact an empty store: %v", err)
	}
}

func TestCompactor_ClosedStoreErrors(t *testing.T) {
	store, _ := openTestSealStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := Compactor(store)(); err == nil {
		t.Fatal("compact a closed store = nil, want the Flush error")
	}
}

func TestFS_ReuseForWriteCreatesFresh(t *testing.T) {
	mem := vfs.NewMem()
	f, err := mem.Create("old", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create old: %v", err)
	}
	if _, err := f.Write([]byte("plaintext history")); err != nil {
		t.Fatalf("Write old: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close old: %v", err)
	}
	fs := newFS(mem)

	nf, err := fs.ReuseForWrite("old", "new", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("ReuseForWrite: %v", err)
	}
	if err := nf.Close(); err != nil {
		t.Fatalf("Close new: %v", err)
	}
	if _, err := mem.Stat("old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat old = %v, want it removed", err)
	}
	info, err := mem.Stat("new")
	if err != nil {
		t.Fatalf("Stat new: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("new size = %d, want a fresh empty file", info.Size())
	}

	// A missing oldname still creates newname.
	mf, err := fs.ReuseForWrite("absent", "fresh", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("ReuseForWrite with a missing oldname: %v", err)
	}
	if err := mf.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}
	if _, err := mem.Stat("fresh"); err != nil {
		t.Fatalf("Stat fresh: %v", err)
	}
}

var errFSRemove = errors.New("injected remove failure")

// newFS returns the seal store's no-recycle file system over base.
func newFS(base vfs.FS) *pebblestore.NoRecycleFS {
	return pebblestore.NewNoRecycleFS(pebblestore.FromVFS(base))
}

// failingRemoveFS is a vfs.FS whose Remove fails with errFSRemove.
type failingRemoveFS struct {
	vfs.FS
}

func (failingRemoveFS) Remove(string) error { return errFSRemove }

// errStat is the failure statFaultFS injects.
var errStat = errors.New("injected stat failure")

// statFaultFS fails Stat with the hook's error when the hook returns one.
type statFaultFS struct {
	vfs.FS
	stat func(name string) error
}

func (f statFaultFS) Stat(name string) (vfs.FileInfo, error) {
	if f.stat != nil {
		if err := f.stat(name); err != nil {
			return nil, err
		}
	}
	return f.FS.Stat(name)
}

// keepFilesFS reports every Remove as done without removing the file, so
// the files written before a retirement stay on disk.
type keepFilesFS struct {
	vfs.FS
}

func (keepFilesFS) Remove(string) error { return nil }

func TestFS_ReuseForWriteRemoveError(t *testing.T) {
	mem := vfs.NewMem()
	fs := newFS(failingRemoveFS{FS: mem})

	f, err := fs.ReuseForWrite("old", "new", vfs.WriteCategoryUnspecified)
	if !errors.Is(err, errFSRemove) {
		t.Fatalf("ReuseForWrite err = %v, want %v", err, errFSRemove)
	}
	if f != nil {
		t.Fatal("ReuseForWrite returned a file with its error")
	}
	if _, err := mem.Stat("new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat new = %v, want nothing created", err)
	}
}

func TestFS_RemoveWakesWaiters(t *testing.T) {
	mem := vfs.NewMem()
	f, err := mem.Create("victim", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fs := newFS(mem)

	ch := fs.Removal()
	if err := fs.Remove("victim"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("removal channel still open after Remove")
	}
	select {
	case <-fs.Removal():
		t.Fatal("a later removal() channel is already closed")
	default:
	}
}

func TestRetirer_PlaintextFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001.log", "000002.sst", "OPTIONS-000003", "MANIFEST-000004"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "x.sst"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	r := &Retirer{fs: newFS(vfs.Default), dir: dir}
	got, err := r.plaintextFiles()
	if err != nil {
		t.Fatalf("plaintextFiles: %v", err)
	}
	slices.Sort(got)
	if want := []string{"000001.log", "000002.sst"}; !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}

	missing := &Retirer{fs: newFS(vfs.Default), dir: filepath.Join(dir, "missing")}
	if _, err := missing.plaintextFiles(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir err = %v, want %v", err, os.ErrNotExist)
	}

	faulty := func(file string, fault error) *Retirer {
		target := filepath.Join(dir, file)
		return &Retirer{fs: newFS(statFaultFS{FS: vfs.Default, stat: func(name string) error {
			if name == target {
				return fault
			}
			return nil
		}}), dir: dir}
	}

	// A file removed between List and Stat is skipped.
	got, err = faulty("000001.log", os.ErrNotExist).plaintextFiles()
	if err != nil {
		t.Fatalf("plaintextFiles with a vanished file: %v", err)
	}
	if want := []string{"000002.sst"}; !slices.Equal(got, want) {
		t.Fatalf("files with a vanished file = %v, want %v", got, want)
	}

	// Any other Stat failure is returned.
	if _, err := faulty("000002.sst", errStat).plaintextFiles(); !errors.Is(err, errStat) {
		t.Fatalf("stat failure err = %v, want %v", err, errStat)
	}
}

func TestOpen_DataDirIsAFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seal")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, r, err := Open(path, nil)
	if err == nil {
		_ = store.Close()
		t.Fatal("Open on a regular file: want an error")
	}
	if store != nil || r != nil {
		t.Fatalf("Open on a regular file returned store %v, retirer %v; want nil", store, r)
	}
}

func TestRetirer_NilIsNoop(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]*Retirer{"nil": nil, "no store": {}} {
		t.Run(name, func(t *testing.T) {
			r.SetFault(func() error { return errors.New("never run") })
			if err := r.Retire(ctx); err != nil {
				t.Fatalf("Retire: %v", err)
			}
			if err := r.Mark(); err != nil {
				t.Fatalf("Mark: %v", err)
			}
			if err := r.RetirePending(ctx); err != nil {
				t.Fatalf("RetirePending: %v", err)
			}
			if owed, err := r.Marked(); owed || err != nil {
				t.Fatalf("Marked = %v, %v, want false, nil", owed, err)
			}
		})
	}
}

func TestRetirer_RetirePendingWithoutMarker(t *testing.T) {
	_, r := openTestSealStore(t)
	if err := r.RetirePending(context.Background()); err != nil {
		t.Fatalf("RetirePending: %v", err)
	}
	if owed, err := r.Marked(); owed || err != nil {
		t.Fatalf("Marked = %v, %v, want false, nil", owed, err)
	}
}

func TestRetirer_RetirePendingRunsAndClearsMarker(t *testing.T) {
	_, r := openTestSealStore(t)
	if err := r.Mark(); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if owed, err := r.Marked(); !owed || err != nil {
		t.Fatalf("Marked after Mark = %v, %v, want true, nil", owed, err)
	}
	if err := r.RetirePending(context.Background()); err != nil {
		t.Fatalf("RetirePending: %v", err)
	}
	if owed, err := r.Marked(); owed || err != nil {
		t.Fatalf("Marked after RetirePending = %v, %v, want false, nil", owed, err)
	}
}

func TestRetirer_FaultKeepsMarker(t *testing.T) {
	_, r := openTestSealStore(t)
	errFault := errors.New("injected retirement failure")
	r.SetFault(func() error { return errFault })
	if err := r.Retire(context.Background()); !errors.Is(err, errFault) {
		t.Fatalf("Retire with a fault = %v, want %v", err, errFault)
	}
	if owed, err := r.Marked(); !owed || err != nil {
		t.Fatalf("Marked after the failed Retire = %v, %v, want the marker kept", owed, err)
	}
	r.SetFault(nil)
	if err := r.RetirePending(context.Background()); err != nil {
		t.Fatalf("RetirePending: %v", err)
	}
	if owed, err := r.Marked(); owed || err != nil {
		t.Fatalf("Marked after RetirePending = %v, %v, want false, nil", owed, err)
	}
}

func TestRetirer_ClosedStoreErrors(t *testing.T) {
	store, r := openTestSealStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.Mark(); err == nil || !strings.Contains(err.Error(), "write the plaintext-retirement marker") {
		t.Fatalf("Mark on a closed store = %v, want the marker write error", err)
	}
	if _, err := r.Marked(); err == nil {
		t.Fatal("Marked on a closed store = nil error, want an error")
	}
	if err := r.RetirePending(context.Background()); err == nil {
		t.Fatal("RetirePending on a closed store = nil, want an error")
	}
}

func TestRetirer_RetireMissingDirErrors(t *testing.T) {
	store, r := openTestSealStore(t)
	missing := &Retirer{store: store, fs: r.fs, dir: filepath.Join(t.TempDir(), "missing")}
	if err := missing.Retire(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Retire over a missing dir = %v, want %v", err, os.ErrNotExist)
	}
	if owed, err := missing.Marked(); !owed || err != nil {
		t.Fatalf("Marked after the failed Retire = %v, %v, want the marker kept", owed, err)
	}
}

// TestRetirer_InMemoryStoreRetiresPlaintext runs the retirement over a seal
// store on an in-memory vfs (the embedded node's dataDir=="" case): the
// overwritten plaintext is gone from every file afterwards.
func TestRetirer_InMemoryStoreRetiresPlaintext(t *testing.T) {
	mem := vfs.NewMem()
	const dir = "seal"
	if err := mem.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, r, err := Open(dir, pebblestore.FromVFS(mem))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	secret := []byte("in-memory plaintext secret")
	if err := store.Set([]byte("k"), secret); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := store.Set([]byte("k"), []byte("replacement")); err != nil {
		t.Fatalf("Set replacement: %v", err)
	}
	if err := r.Retire(context.Background()); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	names, err := mem.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, name := range names {
		f, oerr := mem.Open(mem.PathJoin(dir, name))
		if oerr != nil {
			continue
		}
		data, rerr := io.ReadAll(f)
		_ = f.Close()
		if rerr != nil {
			continue
		}
		if bytes.Contains(data, secret) {
			t.Fatalf("%s still holds the retired plaintext", name)
		}
	}
}

// writeOverwritten writes k, flushes it and overwrites it, so the seal
// store holds files written before a retirement.
func writeOverwritten(t *testing.T, store *pebblestore.PebbleStorage) {
	t.Helper()
	if err := store.Set([]byte("k"), []byte("secret")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := store.Set([]byte("k"), []byte("replacement")); err != nil {
		t.Fatalf("Set replacement: %v", err)
	}
}

func assertMarked(t *testing.T, r *Retirer) {
	t.Helper()
	owed, err := r.Marked()
	if err != nil || !owed {
		t.Fatalf("Marked = %v, %v; want true, nil", owed, err)
	}
}

func TestRetirer_CompactFailureKeepsMarker(t *testing.T) {
	dir := t.TempDir()
	store, r, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r.SetFault(func() error { return store.Close() })
	if err := r.Retire(context.Background()); err == nil || !strings.Contains(err.Error(), "compact:") {
		t.Fatalf("Retire on a closed store err = %v, want a compact error", err)
	}
	store2, r2, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	assertMarked(t, r2)
}

func TestRetirer_FilesNotDeletedHonoursContext(t *testing.T) {
	mem := vfs.NewMem()
	const dir = "seal"
	if err := mem.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, r, err := Open(dir, pebblestore.FromVFS(keepFilesFS{FS: mem}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writeOverwritten(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = r.Retire(ctx)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "not deleted") {
		t.Fatalf("Retire err = %v, want a not-deleted %v", err, context.Canceled)
	}
	assertMarked(t, r)
}

func TestRetirer_WaitStatFailureErrors(t *testing.T) {
	mem := vfs.NewMem()
	const dir = "seal"
	if err := mem.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var (
		armed   atomic.Bool
		mu      sync.Mutex
		targets = map[string]bool{}
		stats   = map[string]int{}
	)
	fs := statFaultFS{FS: mem, stat: func(name string) error {
		if !armed.Load() {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if !targets[name] {
			return nil
		}
		stats[name]++
		if stats[name] >= 2 {
			return errStat
		}
		return nil
	}}
	store, r, err := Open(dir, pebblestore.FromVFS(fs))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writeOverwritten(t, store)
	r.SetFault(func() error {
		names, lerr := mem.List(dir)
		if lerr != nil {
			return lerr
		}
		mu.Lock()
		for _, name := range names {
			if strings.HasSuffix(name, ".log") {
				targets[mem.PathJoin(dir, name)] = true
			}
		}
		n := len(targets)
		mu.Unlock()
		if n == 0 {
			return errors.New("no pre-retirement write-ahead log")
		}
		armed.Store(true)
		return nil
	})
	if err := r.Retire(context.Background()); !errors.Is(err, errStat) {
		t.Fatalf("Retire err = %v, want %v", err, errStat)
	}
	armed.Store(false)
	assertMarked(t, r)
}

func TestStagedRecords(t *testing.T) {
	rec := StagedRecords()
	if rec.Prefix != seal.SealStoreDefaultPrefix || rec.StagingPrefix != StagingPrefix {
		t.Fatalf("StagedRecords = %+v", rec)
	}
	if strings.HasPrefix(StagingPrefix, seal.SealStoreDefaultPrefix) {
		t.Fatalf("StagingPrefix %q lies inside the live keyspace %q", StagingPrefix, seal.SealStoreDefaultPrefix)
	}
}

func TestJournalNamesSealStore(t *testing.T) {
	cases := map[string]struct {
		j    *seal.MigrationJournal
		want bool
	}{
		"nil":          {nil, false},
		"no records":   {&seal.MigrationJournal{}, false},
		"other prefix": {&seal.MigrationJournal{StagedRecords: []seal.StagedRecords{{Prefix: "other/"}}}, false},
		"seal store":   {&seal.MigrationJournal{StagedRecords: []seal.StagedRecords{{Prefix: "other/"}, {Prefix: seal.SealStoreDefaultPrefix}}}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := JournalNamesSealStore(tc.j); got != tc.want {
				t.Fatalf("JournalNamesSealStore = %v, want %v", got, tc.want)
			}
		})
	}
}
