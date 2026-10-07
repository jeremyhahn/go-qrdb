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

package database

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// plaintextMarker is a highly distinctive byte sequence used as the value
// written through a barrier-encrypted database. It must never appear
// verbatim anywhere under the database's on-disk data directory: finding it
// would mean the barrier is not actually encrypting stored values.
var plaintextMarker = []byte("QRDB-BARRIER-ENCRYPTION-PROOF-MARKER-3f9a7c21")

// grepDirForMarker reports whether marker appears verbatim in any regular
// file under root.
func grepDirForMarker(t *testing.T, root string, marker []byte) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// Files may legitimately disappear/rotate mid-walk (e.g. a
			// Pebble compaction); that is not evidence of a plaintext leak.
			return nil
		}
		if bytes.Contains(data, marker) {
			t.Logf("plaintext marker found in %s", path)
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%s): %v", root, err)
	}
	return found
}

// TestBarrierBackend_EncryptsAtRest_FileBackend is the CRITICAL #2 proof: a
// value written through a barrier-enabled database's Backend must not
// appear in plaintext anywhere in the on-disk data directory, a sealed
// barrier must make Get fail closed, and unsealing must restore access to
// the original plaintext.
func TestBarrierBackend_EncryptsAtRest_FileBackend(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	dataDir := t.TempDir()
	cfg := DatabaseConfig{
		Name:    "atrest-file-db",
		Backend: BackendFile,
		DataDir: dataDir,
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	}

	ctx := context.Background()
	db, err := mgr.CreateWithCredentials(ctx, cfg, testCreds())
	if err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}
	if !db.BarrierEnabled() {
		t.Fatal("BarrierEnabled() = false after CreateWithCredentials with barrier, want true")
	}

	key := []byte("secret-key-1")
	if err := db.Backend().Put(ctx, key, plaintextMarker); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// The plaintext must round-trip correctly while the barrier is unsealed.
	got, err := db.Backend().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() while unsealed error = %v", err)
	}
	if !bytes.Equal(got, plaintextMarker) {
		t.Fatalf("Get() while unsealed = %q, want %q", got, plaintextMarker)
	}

	// Proof: the raw on-disk store must NEVER contain the plaintext value.
	if grepDirForMarker(t, dataDir, plaintextMarker) {
		t.Error("plaintext marker found under the database's on-disk data directory; value was not encrypted at rest")
	}

	// Seal: the barrier's own registry seal must make Get fail closed
	// through the SAME wrapped Backend the database uses for data ops.
	if err := bm.SealDatabase("atrest-file-db"); err != nil {
		t.Fatalf("SealDatabase() error = %v", err)
	}
	if _, err := db.Backend().Get(ctx, key); err == nil {
		t.Fatal("Get() after SealDatabase() = nil error, want *SealedError")
	} else {
		var sealedErr *SealedError
		if !errors.As(err, &sealedErr) {
			t.Fatalf("Get() after seal error type = %T, want *SealedError", err)
		}
	}
	if err := db.Backend().Put(ctx, []byte("secret-key-2"), plaintextMarker); err == nil {
		t.Fatal("Put() after SealDatabase() = nil error, want *SealedError")
	}

	// Unseal: access must be restored and the original plaintext recovered.
	if err := bm.UnsealDatabase(ctx, "atrest-file-db", testCreds()); err != nil {
		t.Fatalf("UnsealDatabase() error = %v", err)
	}
	got2, err := db.Backend().Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() after unseal error = %v", err)
	}
	if !bytes.Equal(got2, plaintextMarker) {
		t.Fatalf("Get() after unseal = %q, want %q", got2, plaintextMarker)
	}

	// The barrier's own bootstrap key material lives in a directory separate
	// from the data-plane DataDir and must never contain the plaintext
	// either (defense-in-depth; it stores wrapped key bytes, not app data).
	barrierDir := filepath.Join(mgr.dbDir("atrest-file-db"), barrierMetaSubDir)
	if grepDirForMarker(t, barrierDir, plaintextMarker) {
		t.Error("plaintext marker found under the barrier metadata directory")
	}
}

// TestBarrierBackend_EncryptsAtRest_PebbleBackend mirrors the file-backend
// proof for the Pebble backend, the production-representative persistent
// engine.
func TestBarrierBackend_EncryptsAtRest_PebbleBackend(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	dataDir := t.TempDir()
	cfg := DatabaseConfig{
		Name:    "atrest-pebble-db",
		Backend: BackendPebble,
		DataDir: dataDir,
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	}

	ctx := context.Background()
	db, err := mgr.CreateWithCredentials(ctx, cfg, testCreds())
	if err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}

	key := []byte("secret-key-1")
	if err := db.Backend().Put(ctx, key, plaintextMarker); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if grepDirForMarker(t, dataDir, plaintextMarker) {
		t.Error("plaintext marker found under the database's on-disk data directory; value was not encrypted at rest")
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// After a full close (which flushes and closes the Pebble store -- WAL,
	// memtable, and any flushed sstables), the on-disk files must still
	// never contain the plaintext.
	if grepDirForMarker(t, dataDir, plaintextMarker) {
		t.Error("plaintext marker found on disk after Close(); value was not encrypted at rest")
	}
}
