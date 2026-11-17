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
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestPebbleStorage_SyncSurvivesCrash: a write made with SyncWrites off is
// durable once Sync returns: it survives a crash that drops every unsynced
// write, with the WAL on and off (#1618).
func TestPebbleStorage_SyncSurvivesCrash(t *testing.T) {
	for _, disableWAL := range []bool{false, true} {
		fs := vfs.NewCrashableMem()
		s := openOnFS(t, fs, disableWAL)
		if err := s.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := s.Sync(); err != nil {
			t.Fatalf("Sync (disableWAL=%v): %v", disableWAL, err)
		}
		s = crashReopen(t, fs, s, disableWAL)
		if got := mustExists(t, s, "k"); !got {
			t.Fatalf("synced key absent after crash (disableWAL=%v)", disableWAL)
		}
	}
}

// TestPebbleStorage_UnsyncedWriteLostInCrash proves the double: a write with
// SyncWrites off and no Sync is gone after a crash, so the crash tests above
// and below see only what Sync made durable.
func TestPebbleStorage_UnsyncedWriteLostInCrash(t *testing.T) {
	for _, disableWAL := range []bool{false, true} {
		fs := vfs.NewCrashableMem()
		s := openOnFS(t, fs, disableWAL)
		if err := s.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		s = crashReopen(t, fs, s, disableWAL)
		if got := mustExists(t, s, "k"); got {
			t.Fatalf("unsynced key present after crash (disableWAL=%v)", disableWAL)
		}
	}
}

// TestNewFromDB_SyncSurvivesCrash: a store wrapping a database opened
// elsewhere syncs according to that database's real WAL mode: it must not
// trust its own (absent) Config for DisableWAL (#1618, Codex round 1).
func TestNewFromDB_SyncSurvivesCrash(t *testing.T) {
	for _, disableWAL := range []bool{false, true} {
		fs := vfs.NewCrashableMem()
		db := openRawOnFS(t, fs, disableWAL)
		s := NewFromDB(db, false)
		if err := s.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := s.Sync(); err != nil {
			t.Fatalf("Sync (disableWAL=%v): %v", disableWAL, err)
		}
		// A second Sync takes the same route as the first.
		if err := s.Sync(); err != nil {
			t.Fatalf("second Sync (disableWAL=%v): %v", disableWAL, err)
		}
		crashed := fs.CrashClone(vfs.CrashCloneCfg{})
		_ = db.Close()
		db2 := openRawOnFS(t, crashed, disableWAL)
		got := mustExists(t, NewFromDB(db2, false), "k")
		_ = db2.Close()
		if !got {
			t.Fatalf("synced key absent after crash (NewFromDB, disableWAL=%v)", disableWAL)
		}
	}
}

// TestPebbleStorage_SyncReportsSyncFailure: an fsync failure under the WAL
// is never a successful Sync. Pebble treats a failed WAL sync as fatal
// (Logger.Fatalf, a process exit) rather than returning it, so the test
// routes Fatalf to a panic and accepts either outcome; a Sync that does not
// reach the WAL returns nil and fails it.
func TestPebbleStorage_SyncReportsSyncFailure(t *testing.T) {
	ffs := &failSyncFS{FS: vfs.NewMem()}
	db, err := pebble.Open("/db", &pebble.Options{FS: ffs, Logger: fatalPanicLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	// The database is left open: after a fatal commit error Pebble's commit
	// pipeline is unusable, and the in-memory filesystem goes with the test.
	s := NewFromDB(db, false)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ffs.fail.Store(true)
	syncErr, fatal := syncRecovering(s)
	switch {
	case syncErr != nil:
		if !errors.Is(syncErr, errInjectedSync) {
			t.Fatalf("Sync with failing fsync = %v, want it to wrap %v", syncErr, errInjectedSync)
		}
	case fatal != "":
		if !strings.Contains(fatal, errInjectedSync.Error()) {
			t.Fatalf("Sync fatal = %q, want it to name %v", fatal, errInjectedSync)
		}
	default:
		t.Fatal("Sync with failing fsync returned nil")
	}
}

// syncRecovering runs s.Sync, returning its error or the message of a fatal
// Pebble raised through fatalPanicLogger.
func syncRecovering(s *PebbleStorage) (err error, fatal string) {
	defer func() {
		if r := recover(); r != nil {
			msg, ok := r.(fatalPanic)
			if !ok {
				panic(r)
			}
			fatal = string(msg)
		}
	}()
	return s.Sync(), ""
}

type fatalPanic string

// fatalPanicLogger turns Pebble's Fatalf, which would exit the process, into
// a panic the test recovers.
type fatalPanicLogger struct{}

func (fatalPanicLogger) Infof(string, ...interface{})  {}
func (fatalPanicLogger) Errorf(string, ...interface{}) {}
func (fatalPanicLogger) Fatalf(format string, args ...interface{}) {
	panic(fatalPanic(fmt.Sprintf(format, args...)))
}

// openOnFS opens a store with SyncWrites off over fs; the test closes it.
func openOnFS(t *testing.T, fs vfs.FS, disableWAL bool) *PebbleStorage {
	t.Helper()
	cfg := TestConfig("/db")
	cfg.SyncWrites = false
	cfg.DisableWAL = disableWAL
	cfg.FS = fs
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New (disableWAL=%v): %v", disableWAL, err)
	}
	return s
}

// openRawOnFS opens a bare Pebble database over fs, as a caller of NewFromDB
// would.
func openRawOnFS(t *testing.T, fs vfs.FS, disableWAL bool) *pebble.DB {
	t.Helper()
	db, err := pebble.Open("/db", &pebble.Options{FS: fs, DisableWAL: disableWAL})
	if err != nil {
		t.Fatalf("pebble.Open (disableWAL=%v): %v", disableWAL, err)
	}
	return db
}

// crashReopen simulates a crash of s (only synced data survives), closes s
// and opens a store on the crashed filesystem, closed at test end.
func crashReopen(t *testing.T, fs *vfs.MemFS, s *PebbleStorage, disableWAL bool) *PebbleStorage {
	t.Helper()
	crashed := fs.CrashClone(vfs.CrashCloneCfg{})
	_ = s.Close()
	s2 := openOnFS(t, crashed, disableWAL)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

func mustExists(t *testing.T, s *PebbleStorage, key string) bool {
	t.Helper()
	ok, err := s.Exists([]byte(key))
	if err != nil {
		t.Fatalf("Exists(%q): %v", key, err)
	}
	return ok
}

var errInjectedSync = errors.New("injected fsync failure")

// failSyncFS fails every file sync once fail is set.
type failSyncFS struct {
	vfs.FS
	fail atomic.Bool
}

func (f *failSyncFS) wrap(file vfs.File, err error) (vfs.File, error) {
	if err != nil {
		return nil, err
	}
	return &failSyncFile{File: file, fail: &f.fail}, nil
}

func (f *failSyncFS) Create(name string, c vfs.DiskWriteCategory) (vfs.File, error) {
	return f.wrap(f.FS.Create(name, c))
}

func (f *failSyncFS) OpenReadWrite(name string, c vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	return f.wrap(f.FS.OpenReadWrite(name, c, opts...))
}

func (f *failSyncFS) ReuseForWrite(oldname, newname string, c vfs.DiskWriteCategory) (vfs.File, error) {
	return f.wrap(f.FS.ReuseForWrite(oldname, newname, c))
}

type failSyncFile struct {
	vfs.File
	fail *atomic.Bool
}

func (f *failSyncFile) Sync() error {
	if f.fail.Load() {
		return errInjectedSync
	}
	return f.File.Sync()
}

func (f *failSyncFile) SyncData() error {
	if f.fail.Load() {
		return errInjectedSync
	}
	return f.File.SyncData()
}

func (f *failSyncFile) SyncTo(length int64) (bool, error) {
	if f.fail.Load() {
		return false, errInjectedSync
	}
	return f.File.SyncTo(length)
}

// TestPebbleStorage_SyncClosed: Sync on a closed store fails like every other
// method does.
func TestPebbleStorage_SyncClosed(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = s.Sync()
	var closed *storage.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("Sync on closed store = %v, want *storage.ClosedError", err)
	}
}
