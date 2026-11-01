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
	"bytes"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
)

// countingFS is a thin instrumentation wrapper around vfs.Default that
// records how many file Create / Open invocations pass through it.
// Used by the unit tests below to confirm that Config.FS is actually
// the FS PebbleDB writes through, rather than the OS default.
type countingFS struct {
	vfs.FS
	creates atomic.Int64
	opens   atomic.Int64
}

func newCountingFS() *countingFS {
	return &countingFS{FS: vfs.Default}
}

func (f *countingFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	f.creates.Add(1)
	return f.FS.Create(name, category)
}

func (f *countingFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	f.opens.Add(1)
	return f.FS.Open(name, opts...)
}

// TestConfig_ToPebbleOptions_PropagatesFS asserts that a non-nil
// Config.FS lands in pebble.Options.FS so PebbleDB will route every
// open/create through the supplied FS.
func TestConfig_ToPebbleOptions_PropagatesFS(t *testing.T) {
	fs := newCountingFS()
	cfg := TestConfig(t.TempDir())
	cfg.FS = fs

	opts := cfg.ToPebbleOptions()
	if opts.FS == nil {
		t.Fatal("ToPebbleOptions: opts.FS is nil, expected propagation")
	}
	if opts.FS != fs {
		t.Fatalf("ToPebbleOptions: opts.FS = %p, want %p", opts.FS, fs)
	}
}

// TestConfig_ToPebbleOptions_NilFSDefaultsToPebble confirms that a nil
// Config.FS leaves opts.FS nil so PebbleDB falls back to its own
// vfs.Default. Earlier behavior must be unchanged for callers that do
// not set the FS field.
func TestConfig_ToPebbleOptions_NilFSDefaultsToPebble(t *testing.T) {
	cfg := TestConfig(t.TempDir())

	opts := cfg.ToPebbleOptions()
	if opts.FS != nil {
		t.Fatalf("ToPebbleOptions: opts.FS = %v with nil Config.FS, want nil", opts.FS)
	}
}

// TestNewWithCustomFS exercises the end-to-end path: open
// PebbleStorage with Config.FS = countingFS, perform a Set + Flush,
// and confirm the FS observed at least one Create call. This also
// proves Config.FS survives Validate() and WithDefaults().
func TestNewWithCustomFS(t *testing.T) {
	fs := newCountingFS()
	cfg := TestConfig(t.TempDir())
	cfg.FS = fs

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if fs.creates.Load() == 0 {
		t.Fatalf("expected non-zero Create calls through custom FS, got %d", fs.creates.Load())
	}
}

// TestNewWithCustomFS_PreservedAcrossWithDefaults asserts that the FS
// field of Config survives the WithDefaults copy. The defaulting path
// must not zero out user-supplied references.
func TestNewWithCustomFS_PreservedAcrossWithDefaults(t *testing.T) {
	fs := newCountingFS()
	original := TestConfig(t.TempDir())
	original.FS = fs

	defaulted := original.WithDefaults()
	if defaulted.FS != fs {
		t.Fatalf("WithDefaults dropped Config.FS: got %p, want %p", defaulted.FS, fs)
	}
}

// TestPebbleIterator_ValueFetchErrorIsReported proves that a value whose read
// fails at Value() time is reported through Error rather than by a panic.
// Value separation stores a large value out of line in a blob file, which
// pebble reads lazily when the value is asked for; read faults are armed only
// once the iterator is positioned, so the failing read is the value fetch.
func TestPebbleIterator_ValueFetchErrorIsReported(t *testing.T) {
	var armed atomic.Bool
	fs := errorfs.Wrap(vfs.NewMem(), errorfs.InjectorFunc(func(op errorfs.Op) error {
		if armed.Load() && (op.Kind == errorfs.OpFileRead ||
			op.Kind == errorfs.OpFileReadAt || op.Kind == errorfs.OpOpen) {
			return errorfs.ErrInjected
		}
		return nil
	}))
	opts := &pebble.Options{
		FS:                 fs,
		Cache:              pebble.NewCache(1),
		FormatMajorVersion: pebble.FormatNewest,
	}
	opts.Experimental.ValueSeparationPolicy = func() pebble.ValueSeparationPolicy {
		return pebble.ValueSeparationPolicy{
			Enabled:               true,
			MinimumSize:           64,
			MaxBlobReferenceDepth: 10,
			TargetGarbageRatio:    1.0,
		}
	}
	db, err := pebble.Open("db", opts)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() {
		armed.Store(false)
		_ = db.Close()
	})

	if err := db.Set([]byte("k"), bytes.Repeat([]byte("v"), 4096), pebble.Sync); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	it := newIterator(db, nil)
	defer func() { _ = it.Close() }()
	if !it.SeekToFirst() || !it.Valid() {
		t.Fatalf("iterator not positioned: %v", it.Error())
	}

	armed.Store(true)
	if v := it.Value(); v != nil {
		t.Fatalf("Value after a failed fetch = %d bytes, want nil", len(v))
	}
	if it.Valid() {
		t.Fatal("Valid() = true after a failed value fetch")
	}
	if err := it.Error(); !errors.Is(err, errorfs.ErrInjected) {
		t.Fatalf("Error() = %v, want the injected read error", err)
	}
	if it.Next() || it.Valid() || it.Key() != nil {
		t.Fatal("iterator advanced past a recorded value-fetch error")
	}
}
