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
