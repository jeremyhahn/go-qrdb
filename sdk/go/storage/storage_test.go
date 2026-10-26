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

package storage_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

// plaintextMarker is a value no Pebble file holds unless it was written in
// the clear.
var plaintextMarker = []byte("sdk-storage-plaintext-marker-7f3a9c1e5b2d")

// unsealedBarrier returns a passphrase-sealed StorageBarrier over in-memory
// seal storage, initialized and therefore unsealed.
func unsealedBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(logger, seal.NewStorageAdapter(sdkstorage.NewMemoryStorage()),
		seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(context.Background(), []byte("sdk-storage-test-passphrase"))
	if err := barrier.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return barrier
}

// writeAndReopen opens a Pebble store under cfg, writes the marker, closes
// it, reopens it under the same configuration and returns the value read
// back.
func writeAndReopen(t *testing.T, cfg *sdkstorage.PebbleConfig) []byte {
	t.Helper()
	stg, err := sdkstorage.NewPebbleStorage(cfg)
	if err != nil {
		t.Fatalf("NewPebbleStorage: %v", err)
	}
	if err := stg.Set([]byte("k"), plaintextMarker); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := stg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stg, err = sdkstorage.NewPebbleStorage(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = stg.Close() }()
	got, err := stg.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return got
}

// filesContain reports whether any regular file under dir holds needle.
func filesContain(t *testing.T, dir string, needle []byte) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found = bytes.Contains(data, needle)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

// TestEncryptedPebbleConfig_NilBarrierIsPlainDefault checks that a nil
// barrier yields the default configuration unchanged, whose files hold the
// written value in the clear.
func TestEncryptedPebbleConfig_NilBarrierIsPlainDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := sdkstorage.EncryptedPebbleConfig(dir, nil)
	want := sdkstorage.DefaultPebbleConfig(dir)
	if cfg.DataDir != dir || cfg.FS != want.FS {
		t.Fatalf("EncryptedPebbleConfig(nil) = {DataDir:%q FS:%v}, want the default {DataDir:%q FS:%v}", cfg.DataDir, cfg.FS, dir, want.FS)
	}
	if got := writeAndReopen(t, cfg); !bytes.Equal(got, plaintextMarker) {
		t.Fatalf("read back %q, want %q", got, plaintextMarker)
	}
	if !filesContain(t, dir, plaintextMarker) {
		t.Fatal("an unencrypted store holds no plaintext copy of the value: the contrast case below proves nothing")
	}
}

// TestEncryptedPebbleConfig_BarrierEncryptsAtRest checks that a barrier
// installs an encrypting filesystem: the value round-trips across a reopen
// and no file on disk holds it in the clear.
func TestEncryptedPebbleConfig_BarrierEncryptsAtRest(t *testing.T) {
	dir := t.TempDir()
	cfg := sdkstorage.EncryptedPebbleConfig(dir, unsealedBarrier(t))
	if cfg.DataDir != dir {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, dir)
	}
	if cfg.FS == nil {
		t.Fatal("EncryptedPebbleConfig with a barrier left FS unset")
	}
	if got := writeAndReopen(t, cfg); !bytes.Equal(got, plaintextMarker) {
		t.Fatalf("read back %q, want %q", got, plaintextMarker)
	}
	if filesContain(t, dir, plaintextMarker) {
		t.Fatal("a barrier-encrypted store holds the value in the clear")
	}
}
