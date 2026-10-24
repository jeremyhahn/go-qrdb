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
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// probedFS is a filesystem that reports its barrier's state the way
// pkg/seal/encfs does and, like encfs, refuses every Create while sealed: the
// WAL a flush rotates to included, which Pebble cannot survive.
type probedFS struct {
	vfs.FS
	sealed atomic.Bool
}

func (fs *probedFS) BarrierSealed() bool { return fs.sealed.Load() }

func (fs *probedFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if fs.sealed.Load() {
		return nil, errTestSealed
	}
	return fs.FS.Create(name, category)
}

func TestSealWatch_CheckAndFlushFollowTheBarrierProbe(t *testing.T) {
	fs := &probedFS{FS: vfs.NewMem()}
	db, w, err := Open("db", &pebble.Options{FS: fs}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := w.Check(); err != nil {
		t.Fatalf("Check under an unsealed barrier = %v; want nil", err)
	}

	fs.sealed.Store(true)
	var sealed *SealedError
	if err := w.Check(); !errors.As(err, &sealed) || !errors.Is(err, ErrBarrierSealed) {
		t.Fatalf("Check under a sealed barrier = %v; want a *SealedError wrapping ErrBarrierSealed", err)
	}
	// Reaching Pebble here would rotate the WAL onto a refused file.
	if err := flushWithin(t, w, db); !errors.As(err, &sealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want a *SealedError", err)
	}

	fs.sealed.Store(false)
	if err := w.Check(); err != nil {
		t.Fatalf("Check after unseal = %v; want nil", err)
	}
	if err := flushWithin(t, w, db); err != nil {
		t.Fatalf("Flush after unseal = %v; want nil", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSealWatch_ClassifiesByBarrierStateWithoutAClassifier(t *testing.T) {
	logger := &recordingLogger{}
	w := NewSealWatch(logger, nil)
	if w.observe(errTestSealed) {
		t.Fatal("observe claimed an error with neither a classifier nor a probe")
	}
	var barrierSealed atomic.Bool
	w.barrierSealed = barrierSealed.Load
	if w.observe(errTestSealed) {
		t.Fatal("observe claimed an error raised under an unsealed barrier")
	}
	barrierSealed.Store(true)
	if !w.observe(errTestSealed) || !w.observe(errTestSealed) {
		t.Fatal("observe did not claim errors raised under a sealed barrier")
	}
	if n := logger.sealedErrors(); n != 1 {
		t.Fatalf("logged %d sealed background errors; want 1 per episode", n)
	}
	if !errors.Is(w.Err(), errTestSealed) {
		t.Fatalf("Err() = %v; want the episode's first error", w.Err())
	}
}

func TestOpen_DoesNotModifyCallerOptions(t *testing.T) {
	opts := &pebble.Options{FS: vfs.NewMem()}
	db, w, err := Open("db", opts, isTestSealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if w == nil {
		t.Fatal("Open returned no SealWatch")
	}
	if opts.EventListener != nil || opts.Logger != nil {
		t.Fatal("Open modified the caller's options")
	}
	if w.barrierSealed != nil {
		t.Fatal("a filesystem that cannot report its barrier installed a probe")
	}
}

func TestOpen_ReturnsTheOpenError(t *testing.T) {
	db, w, err := Open("missing", &pebble.Options{FS: vfs.NewMem(), ErrorIfNotExists: true}, nil)
	if err == nil || db != nil || w != nil {
		t.Fatalf("Open of a missing database = %v, %v, %v; want only an error", db, w, err)
	}
}

func TestPebbleStorage_FlushOverADatabaseOpenedElsewhere(t *testing.T) {
	db, err := pebble.Open("db", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	s := NewFromDB(db, false)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Compact([]byte("a"), []byte("z")); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPebbleStorage_SealedBarrierRefusesWrites(t *testing.T) {
	fs := &probedFS{FS: vfs.NewMem()}
	cfg := TestConfig("db")
	cfg.FS = fs
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()
	fs.sealed.Store(true)
	var sealed *SealedError
	if err := s.Set([]byte("k"), []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Set under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := s.DeleteRange([]byte("a"), []byte("z")); !errors.As(err, &sealed) {
		t.Fatalf("DeleteRange under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := s.Flush(); !errors.As(err, &sealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := s.Compact(nil, nil); !errors.As(err, &sealed) {
		t.Fatalf("Compact under a sealed barrier = %v; want a *SealedError", err)
	}
	fs.sealed.Store(false)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set after unseal: %v", err)
	}
}
