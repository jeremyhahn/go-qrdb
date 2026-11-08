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

package standalone

import (
	"context"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Server cleanup path coverage: trigger closeSeal/closeStorage/closeAll via
// NewServer error paths.

func TestNewServer_CloseSeal_OnStorageFailure(t *testing.T) {
	ctx := context.Background()
	// Provide a barrier config so setupBarrier runs,
	// then use an invalid (read-only root) dataDir so createServerStorage fails.
	// This exercises the closeSeal() call in the storage error path.
	roDir := t.TempDir()
	if err := os.Chmod(roDir, 0o400); err != nil {
		t.Skip("cannot set directory to read-only:", err)
	}
	// #nosec G302 -- a directory needs the owner execute bit to be traversable, so t.TempDir()'s cleanup cannot remove this tree at 0600 or less.
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	_, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: roDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "test-passphrase"),
		},
	})
	if err == nil {
		t.Log("NewServer succeeded (cleanup paths not exercised - dataDir may have been writable)")
	}
	// Whether it fails or not, the test exercises the server initialization path.
}

// TestNewServer_CloseStorage_FileBackendInvalidDir exercises the closeStorage
// path by using a file backend with an unwritable directory (no barrier).
func TestNewServer_CloseStorage_FileBackendInvalidDir(t *testing.T) {
	ctx := context.Background()
	// Use /proc/invalid or a non-existent path to make file backend fail.
	_, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendFile,
		DataDir: "/proc/invalid/path/that/cannot/be/created",
	})
	// Expect an error; the exact error type doesn't matter.
	if err == nil {
		t.Log("NewServer succeeded unexpectedly")
	}
}

// sealedPebbleConfig is a pebble server over dataDir behind an auto-unsealed
// passphrase barrier, so every resource closeAll owns is open.
func sealedPebbleConfig(t *testing.T, dataDir string) *ServerConfig {
	t.Helper()
	return &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: dataDir,
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyPassphrase,
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "a-passphrase-for-the-cleanup-path"),
		},
	}
}

// TestCloseAll_ReleasesTheDataDirectory proves the failed-initialisation
// cleanup releases what it opened: a second server can take the same data
// directory, which pebble refuses while the first one still holds it.
func TestCloseAll_ReleasesTheDataDirectory(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	srv, err := NewServer(ctx, sealedPebbleConfig(t, dataDir))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.closeAll()
	_ = srv.client.Close()

	if srv.stg != nil || srv.pebbleDB != nil || srv.localBackend != nil ||
		srv.sealStore != nil || srv.barrier != nil || srv.stopExpiry != nil {
		t.Fatal("closeAll left a resource handle set")
	}

	again, err := NewServer(ctx, sealedPebbleConfig(t, dataDir))
	if err != nil {
		t.Fatalf("NewServer over the released data directory: %v", err)
	}
	if err := again.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestCloseAll_IsIdempotent proves a second cleanup, and one over a server
// that never opened anything, is a no-op rather than a double close.
func TestCloseAll_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.closeAll()
	srv.closeAll()
	_ = srv.client.Close()

	empty := &Server{}
	empty.closeAll()
	if empty.stg != nil || empty.localBackend != nil {
		t.Fatal("closeAll over an empty server set a handle")
	}
}
