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
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
)

// newTestBarrier returns an unsealed *seal.StorageBarrier backed by an
// in-memory backend. It is initialized and unsealed so Wrap can wrap a FS.
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

// TestFS_NilBarrierReturnsBase confirms that FS() on a nil-barrier Encryptor
// returns the base FS (filesys.Default) rather than nil.
func TestFS_NilBarrierReturnsBase(t *testing.T) {
	e := atrest.New(nil)
	fs := e.FS()
	if fs == nil {
		t.Error("FS() with nil barrier returned nil, want filesys.Default")
	}
	if fs != filesys.Default {
		t.Errorf("FS() = %T, want filesys.Default", fs)
	}
}

// TestFS_NilReceiverReturnsDefault pins the nil-receiver contract: a nil
// *Encryptor has no base FS to hand back, so FS falls back to filesys.Default
// rather than dereferencing the receiver.
func TestFS_NilReceiverReturnsDefault(t *testing.T) {
	var e *atrest.Encryptor
	if fs := e.FS(); fs != filesys.Default {
		t.Errorf("nil Encryptor FS() = %T, want filesys.Default", fs)
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

// TestWrap_DisabledReturnsBase confirms that Wrap on a nil or nil-barrier
// Encryptor hands back the base FS itself, so a store writes plaintext only
// when there is no barrier.
func TestWrap_DisabledReturnsBase(t *testing.T) {
	base := filesys.Default
	var nilEnc *atrest.Encryptor
	for name, e := range map[string]*atrest.Encryptor{
		"nil-receiver": nilEnc,
		"nil-barrier":  atrest.New(nil),
	} {
		if got := e.Wrap(base); got != base {
			t.Errorf("%s: Wrap = %T, want the base FS", name, got)
		}
	}
}

// TestWrap_WithBarrierEncryptsContents confirms that Wrap installs encfs over
// the given base FS: bytes written through the wrapper are ciphertext in the
// base and read back as the plaintext through it.
func TestWrap_WithBarrierEncryptsContents(t *testing.T) {
	base := filesys.Default
	wrapped := atrest.New(newTestBarrier(t)).Wrap(base)
	if _, ok := wrapped.(*encfs.FS); !ok {
		t.Fatalf("Wrap = %T, want *encfs.FS", wrapped)
	}
	name := filepath.Join(t.TempDir(), "rec")
	const secret = "wrap-plaintext-must-not-reach-the-base"
	f, err := wrapped.Create(name)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte(secret)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := base.Open(name)
	if err != nil {
		t.Fatalf("base Open: %v", err)
	}
	onDisk, err := io.ReadAll(raw)
	_ = raw.Close()
	if err != nil {
		t.Fatalf("base ReadAll: %v", err)
	}
	if strings.Contains(string(onDisk), secret) {
		t.Fatal("base FS holds the plaintext; Wrap did not encrypt")
	}

	r, err := wrapped.Open(name)
	if err != nil {
		t.Fatalf("wrapped Open: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("wrapped ReadAll: %v", err)
	}
	if string(got) != secret {
		t.Errorf("read back %q, want %q", got, secret)
	}
}
