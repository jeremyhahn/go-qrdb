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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openTestSealStore opens a seal store in a fresh directory and closes it
// at cleanup unless the test closed it first.
func openTestSealStore(t *testing.T) (*pebblestore.PebbleStorage, *sealStoreRetirer) {
	t.Helper()
	store, retirer, err := openSealStore(t.TempDir())
	if err != nil {
		t.Fatalf("openSealStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, retirer
}

func TestSealStoreCompactor_NilStoreIsNoop(t *testing.T) {
	if err := sealStoreCompactor(nil)(); err != nil {
		t.Fatalf("sealStoreCompactor(nil)() = %v, want nil", err)
	}
}

func TestSealStoreCompactor_EmptyStoreIsNoop(t *testing.T) {
	store, _ := openTestSealStore(t)
	if err := sealStoreCompactor(store)(); err != nil {
		t.Fatalf("compact an empty store: %v", err)
	}
}

func TestSealStoreCompactor_ClosedStoreErrors(t *testing.T) {
	store, _ := openTestSealStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sealStoreCompactor(store)(); err == nil {
		t.Fatal("compact a closed store = nil, want the Flush error")
	}
}

func TestSealStoreFS_ReuseForWriteCreatesFresh(t *testing.T) {
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
	fs := newSealStoreFS(mem)

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

var errSealStoreFSRemove = errors.New("injected remove failure")

// failingRemoveFS is a vfs.FS whose Remove fails with errSealStoreFSRemove.
type failingRemoveFS struct {
	vfs.FS
}

func (failingRemoveFS) Remove(string) error { return errSealStoreFSRemove }

func TestSealStoreFS_ReuseForWriteRemoveError(t *testing.T) {
	mem := vfs.NewMem()
	fs := newSealStoreFS(failingRemoveFS{FS: mem})

	f, err := fs.ReuseForWrite("old", "new", vfs.WriteCategoryUnspecified)
	if !errors.Is(err, errSealStoreFSRemove) {
		t.Fatalf("ReuseForWrite err = %v, want %v", err, errSealStoreFSRemove)
	}
	if f != nil {
		t.Fatal("ReuseForWrite returned a file with its error")
	}
	if _, err := mem.Stat("new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat new = %v, want nothing created", err)
	}
}

func TestSealStoreFS_RemoveWakesWaiters(t *testing.T) {
	mem := vfs.NewMem()
	f, err := mem.Create("victim", vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fs := newSealStoreFS(mem)

	ch := fs.removal()
	if err := fs.Remove("victim"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("removal channel still open after Remove")
	}
	select {
	case <-fs.removal():
		t.Fatal("a later removal() channel is already closed")
	default:
	}
}

func TestSealStorePlaintextFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001.log", "000002.sst", "OPTIONS-000003", "MANIFEST-000004"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "x.sst"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	got, err := sealStorePlaintextFiles(dir)
	if err != nil {
		t.Fatalf("sealStorePlaintextFiles: %v", err)
	}
	slices.Sort(got)
	if want := []string{"000001.log", "000002.sst"}; !slices.Equal(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}

	if _, err := sealStorePlaintextFiles(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir err = %v, want %v", err, os.ErrNotExist)
	}
}

func TestSealStoreRetirer_NilIsNoop(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]*sealStoreRetirer{"nil": nil, "no store": {}} {
		t.Run(name, func(t *testing.T) {
			if err := r.Retire(ctx); err != nil {
				t.Fatalf("Retire: %v", err)
			}
			if err := r.mark(); err != nil {
				t.Fatalf("mark: %v", err)
			}
			if err := r.RetirePending(ctx); err != nil {
				t.Fatalf("RetirePending: %v", err)
			}
			if owed, err := r.marked(); owed || err != nil {
				t.Fatalf("marked = %v, %v, want false, nil", owed, err)
			}
		})
	}
}

func TestSealStoreRetirer_RetirePendingWithoutMarker(t *testing.T) {
	_, r := openTestSealStore(t)
	if err := r.RetirePending(context.Background()); err != nil {
		t.Fatalf("RetirePending: %v", err)
	}
	if owed, err := r.marked(); owed || err != nil {
		t.Fatalf("marked = %v, %v, want false, nil", owed, err)
	}
}

func TestSealStoreRetirer_RetirePendingRunsAndClearsMarker(t *testing.T) {
	_, r := openTestSealStore(t)
	if err := r.mark(); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if owed, err := r.marked(); !owed || err != nil {
		t.Fatalf("marked after mark = %v, %v, want true, nil", owed, err)
	}
	if err := r.RetirePending(context.Background()); err != nil {
		t.Fatalf("RetirePending: %v", err)
	}
	if owed, err := r.marked(); owed || err != nil {
		t.Fatalf("marked after RetirePending = %v, %v, want false, nil", owed, err)
	}
}

func TestSealStoreRetirer_ClosedStoreErrors(t *testing.T) {
	store, r := openTestSealStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.mark(); err == nil || !strings.Contains(err.Error(), "write the plaintext-retirement marker") {
		t.Fatalf("mark on a closed store = %v, want the marker write error", err)
	}
	if _, err := r.marked(); err == nil {
		t.Fatal("marked on a closed store = nil error, want an error")
	}
	if err := r.RetirePending(context.Background()); err == nil {
		t.Fatal("RetirePending on a closed store = nil, want an error")
	}
}

func TestSealStoreRetirer_RetireMissingDirErrors(t *testing.T) {
	store, r := openTestSealStore(t)
	missing := &sealStoreRetirer{store: store, fs: r.fs, dir: filepath.Join(t.TempDir(), "missing")}
	if err := missing.Retire(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Retire over a missing dir = %v, want %v", err, os.ErrNotExist)
	}
	if owed, err := missing.marked(); !owed || err != nil {
		t.Fatalf("marked after the failed Retire = %v, %v, want the marker kept", owed, err)
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
			if got := journalNamesSealStore(tc.j); got != tc.want {
				t.Fatalf("journalNamesSealStore = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRefuseCommittedJournalWithoutStagingRoot_Allows(t *testing.T) {
	ctx := context.Background()
	const rootKeyPath = "system/root-key"
	const stagingPath = "system/root-key.staging"
	committed := func() *seal.MigrationJournal {
		return &seal.MigrationJournal{Phase: seal.PhaseCommitting, StagingRootKeyPath: stagingPath}
	}
	cases := map[string]struct {
		j     *seal.MigrationJournal
		setup func(t *testing.T, backend seal.StorageBackend)
	}{
		"nil journal":         {j: nil},
		"uncommitted journal": {j: &seal.MigrationJournal{StagingRootKeyPath: stagingPath}},
		"in-place commit":     {j: &seal.MigrationJournal{Phase: seal.PhaseCommitting}},
		"canonical root keyed": {j: committed(), setup: func(t *testing.T, backend seal.StorageBackend) {
			t.Helper()
			blob := []byte(`{"strategy":"` + string(seal.StrategyPassphrase) + `"}`)
			if err := backend.Put(ctx, rootKeyPath, blob); err != nil {
				t.Fatalf("Put root key: %v", err)
			}
		}},
		"staging root present": {j: committed(), setup: func(t *testing.T, backend seal.StorageBackend) {
			t.Helper()
			if err := backend.Put(ctx, stagingPath, []byte("sealed staging root")); err != nil {
				t.Fatalf("Put staging root: %v", err)
			}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := seal.NewMemoryBackend()
			if tc.setup != nil {
				tc.setup(t, backend)
			}
			if err := refuseCommittedJournalWithoutStagingRoot(ctx, backend, rootKeyPath, tc.j); err != nil {
				t.Fatalf("refuseCommittedJournalWithoutStagingRoot = %v, want nil", err)
			}
		})
	}
}
