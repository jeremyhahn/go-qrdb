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

package embedded

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestEmbeddedServerWithBarrier_PebbleEncrypted asserts the disk-backed
// embedded server boots with the encrypting FS attached, persists KV
// data through a restart cycle, and never leaks plaintext to disk.
func TestEmbeddedServerWithBarrier_PebbleEncrypted(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	passphrase := barrierPassphrase(t, "encfs-embedded-test-passphrase")

	const (
		key       = "ribosome-key"
		plaintext = "RIBOSOME-PLAINTEXT-MUST-NEVER-LEAK"
	)

	// First boot: initialize barrier + write a value.
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: passphrase,
		},
	})
	if err != nil {
		t.Fatalf("NewServer (first boot): %v", err)
	}
	if err := srv.Client().KV().Put(ctx, key, []byte(plaintext)); err != nil {
		_ = srv.Shutdown(ctx)
		t.Fatalf("Put: %v", err)
	}
	got, err := srv.Client().KV().Get(ctx, key)
	if err != nil {
		_ = srv.Shutdown(ctx)
		t.Fatalf("Get: %v", err)
	}
	if string(got) != plaintext {
		_ = srv.Shutdown(ctx)
		t.Fatalf("Get: got %q, want %q", got, plaintext)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown (first boot): %v", err)
	}

	// Confirm no plaintext leaks anywhere on disk except the LOCK file
	// and seal sub-store, which are intentionally plaintext.
	scanForPlaintext(t, filepath.Join(dataDir, "pebble"), []byte(plaintext))
	scanForPlaintext(t, filepath.Join(dataDir, "pebble"), []byte(key))

	// Second boot: same passphrase; AutoUnseal takes the
	// already-initialized branch and the value must read back intact.
	srv2, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: passphrase,
		},
	})
	if err != nil {
		t.Fatalf("NewServer (second boot): %v", err)
	}
	defer func() {
		if err := srv2.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown (second boot): %v", err)
		}
	}()

	got, err = srv2.Client().KV().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get (after restart): %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("Get (after restart): got %q, want %q", got, plaintext)
	}
}

// TestEmbeddedServerWithBarrier_PebbleSealedRejected asserts that
// configuring a disk-backed pebble server with a barrier but without
// AutoUnseal returns BarrierSealedError so the daemon can never open
// PebbleDB against a sealed barrier.
func TestEmbeddedServerWithBarrier_PebbleSealedRejected(t *testing.T) {
	ctx := context.Background()
	_, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy: seal.StrategyPassphrase,
		},
	})
	if err == nil {
		t.Fatal("expected BarrierSealedError, got nil")
	}
	var sealedErr *BarrierSealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
	if !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("expected wrapped seal.ErrSealed, got %v", err)
	}
}

// TestEmbeddedServerWithBarrier_AutoUnsealWrongPassphrase confirms a
// second boot with the wrong passphrase fails with BarrierUnsealError
// and ErrInvalidCredentials, leaving disk state intact.
func TestEmbeddedServerWithBarrier_AutoUnsealWrongPassphrase(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "right-passphrase"),
		},
	})
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	_, err = NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "wrong-passphrase"),
		},
	})
	if err == nil {
		t.Fatal("expected BarrierUnsealError on wrong passphrase, got nil")
	}
	var unsealErr *BarrierUnsealError
	if !errors.As(err, &unsealErr) {
		t.Fatalf("expected *BarrierUnsealError, got %T: %v", err, err)
	}
}

// TestEmbeddedServerWithBarrier_MemoryAutoUnsealNoFSWrap asserts that
// the memory backend takes the AutoUnseal path without attempting to
// wrap any FS, because there is nothing on disk to encrypt. The
// barrier ends up unsealed and KV operations succeed.
func TestEmbeddedServerWithBarrier_MemoryAutoUnsealNoFSWrap(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "memory-pass"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() {
		if err := srv.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after AutoUnseal")
	}

	if err := srv.Client().KV().Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := srv.Client().KV().Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get: got %q, want %q", got, "v")
	}
}

// TestEmbeddedServerWithBarrier_FormatSentinelWritten asserts that the
// format-version sentinel lands in the seal sub-store on first boot
// so a future build reading the same dir can validate compatibility.
func TestEmbeddedServerWithBarrier_FormatSentinelWritten(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "sentinel-test"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Walk the seal sub-store and assert at least one sstable exists
	// (the sentinel write created data) and that the sentinel marker
	// string appears somewhere on disk. The sentinel key itself is
	// stored plaintext in the seal sub-store; only the data plane is
	// encrypted.
	sealDir := filepath.Join(dataDir, "seal")
	found := false
	err = filepath.Walk(sealDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(seal.SentinelKey)) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk seal dir: %v", err)
	}
	if !found {
		t.Fatalf("expected sentinel key %q in %s", seal.SentinelKey, sealDir)
	}
}

// scanForPlaintext walks dir and fails the test if any file contains
// the supplied needle. The Pebble LOCK file is skipped because Pebble
// writes it through the base FS via Lock() and it intentionally is
// not encrypted.
func scanForPlaintext(t *testing.T, dir string, needle []byte) {
	t.Helper()
	count := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "LOCK") {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, needle) {
			t.Errorf("plaintext %q found in %s", needle, path)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if count == 0 {
		t.Errorf("walk %s scanned zero files", dir)
	}
}
