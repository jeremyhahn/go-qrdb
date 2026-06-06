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
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
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
