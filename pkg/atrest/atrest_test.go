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

package atrest_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// newTestBarrier returns an unsealed *seal.StorageBarrier backed by an
// in-memory backend. It is initialized and unsealed so Apply can wrap a FS.
func newTestBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	memBackend := seal.NewMemoryBackend()
	const passphrase = "atrest-test-passphrase"
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte(passphrase))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(log, memBackend, seal.BarrierConfig{
		RootKeyPath:  "atrest-test-root-key",
		DeploymentID: 1,
	}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(context.Background(), []byte(passphrase))
	if err := barrier.Initialize(ctx, creds); err != nil {
		t.Fatalf("barrier.Initialize: %v", err)
	}
	if err := barrier.Unseal(ctx, creds); err != nil {
		if !strings.Contains(err.Error(), "already unsealed") {
			t.Fatalf("barrier.Unseal: %v", err)
		}
	}
	return barrier
}

// TestNew_NilBarrier confirms New with a nil barrier creates a valid
// Encryptor that reports Enabled() == false.
func TestNew_NilBarrier(t *testing.T) {
	e := atrest.New(nil)
	if e == nil {
		t.Fatal("New(nil) returned nil Encryptor")
	}
	if e.Enabled() {
		t.Error("Enabled() = true, want false for nil barrier")
	}
}

// TestNew_WithBarrier confirms New with a real barrier creates an Encryptor
// that reports Enabled() == true.
func TestNew_WithBarrier(t *testing.T) {
	barrier := newTestBarrier(t)
	e := atrest.New(barrier)
	if e == nil {
		t.Fatal("New(barrier) returned nil Encryptor")
	}
	if !e.Enabled() {
		t.Error("Enabled() = false, want true for real barrier")
	}
}

// TestNilEncryptor_EnabledFalse confirms that a nil *Encryptor pointer is
// safe to call Enabled() on and returns false.
func TestNilEncryptor_EnabledFalse(t *testing.T) {
	var e *atrest.Encryptor
	if e.Enabled() {
		t.Error("nil Encryptor.Enabled() = true, want false")
	}
}

// TestApply_NilBarrierIsNoOp confirms that Apply on a nil-barrier Encryptor
// leaves pebble.Options.FS unchanged.
func TestApply_NilBarrierIsNoOp(t *testing.T) {
	e := atrest.New(nil)
	var opts pebble.Options
	e.Apply(&opts)
	if opts.FS != nil {
		t.Errorf("Apply with nil barrier set FS = %v, want nil (untouched)", opts.FS)
	}
}

// TestApply_NilEncryptorIsNoOp confirms that a nil *Encryptor Apply is safe
// and does not modify pebble.Options.FS.
func TestApply_NilEncryptorIsNoOp(t *testing.T) {
	var e *atrest.Encryptor
	var opts pebble.Options
	e.Apply(&opts)
	if opts.FS != nil {
		t.Errorf("nil Encryptor Apply set FS = %v, want nil", opts.FS)
	}
}

// TestApply_WithBarrierWrapsFS confirms that Apply with a real barrier
// installs a non-nil, non-default encrypting FS on pebble.Options.
func TestApply_WithBarrierWrapsFS(t *testing.T) {
	barrier := newTestBarrier(t)
	e := atrest.New(barrier)
	var opts pebble.Options
	e.Apply(&opts)
	if opts.FS == nil {
		t.Fatal("Apply with real barrier left FS = nil, want encrypting wrapper")
	}
	if opts.FS == vfs.Default {
		t.Error("Apply with real barrier did not replace FS (still vfs.Default)")
	}
}

// TestApply_WrapsEncfsType confirms the FS installed by Apply is the encfs
// type (not just any non-nil value).
func TestApply_WrapsEncfsType(t *testing.T) {
	barrier := newTestBarrier(t)
	e := atrest.New(barrier)
	var opts pebble.Options
	e.Apply(&opts)
	if _, ok := opts.FS.(*encfs.FS); !ok {
		t.Errorf("Apply installed FS of type %T, want *encfs.FS", opts.FS)
	}
}

// TestApply_PreservesExistingBase confirms that Apply wraps a pre-existing
// opts.FS rather than discarding it in favour of vfs.Default.
func TestApply_PreservesExistingBase(t *testing.T) {
	barrier := newTestBarrier(t)
	e := atrest.New(barrier)
	opts := pebble.Options{FS: vfs.NewMem()}
	e.Apply(&opts)
	if _, ok := opts.FS.(*encfs.FS); !ok {
		t.Errorf("Apply with existing FS installed %T, want *encfs.FS", opts.FS)
	}
}

// TestFS_NilBarrierReturnsBase confirms that FS() on a nil-barrier Encryptor
// returns the base FS (vfs.Default) rather than nil.
func TestFS_NilBarrierReturnsBase(t *testing.T) {
	e := atrest.New(nil)
	fs := e.FS()
	if fs == nil {
		t.Error("FS() with nil barrier returned nil, want vfs.Default")
	}
	if fs != vfs.Default {
		t.Errorf("FS() = %T, want vfs.Default", fs)
	}
}

// TestFS_WithBarrierReturnsEncfsType confirms that FS() on a real-barrier
// Encryptor returns an *encfs.FS.
func TestFS_WithBarrierReturnsEncfsType(t *testing.T) {
	barrier := newTestBarrier(t)
	e := atrest.New(barrier)
	fs := e.FS()
	if fs == nil {
		t.Fatal("FS() with real barrier returned nil")
	}
	if _, ok := fs.(*encfs.FS); !ok {
		t.Errorf("FS() = %T, want *encfs.FS", fs)
	}
}
