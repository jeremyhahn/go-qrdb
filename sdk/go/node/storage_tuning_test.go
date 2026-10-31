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

package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestCreateNodeStorage_PebbleTakesTheStorageTuning pins that the storage
// section's cache_size, wal_dir and max_open_files reach Pebble on the node
// path: its OPTIONS file records them and the WAL is in wal_dir.
func TestCreateNodeStorage_PebbleTakesTheStorageTuning(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(t.TempDir(), "wal")
	cfg := qrdbconfig.StorageConfig{CacheSize: 16 << 20, WALDir: walDir, MaxOpenFiles: 222}

	stg, pdb, err := createNodeStorage(nil, database.BackendPebble, dataDir, nil, cfg)
	if err != nil {
		t.Fatalf("createNodeStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	if pdb == nil {
		t.Fatal("the pebble backend returned no *pebble.DB handle")
	}
	assertPebbleTuning(t, filepath.Join(dataDir, "pebble"), walDir,
		"cache_size=16777216", "max_open_files=222", "wal_dir="+walDir)
}

// TestCreateNodeStorage_FallbackToPebbleKeepsTheTuning pins that a backend
// type createNodeStorage does not name falls back to Pebble with the same
// tuning, so wal_dir is honoured whatever the database's backend says.
func TestCreateNodeStorage_FallbackToPebbleKeepsTheTuning(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(t.TempDir(), "wal")

	stg, _, err := createNodeStorage(nil, "", dataDir, nil, qrdbconfig.StorageConfig{WALDir: walDir})
	if err != nil {
		t.Fatalf("createNodeStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	assertPebbleTuning(t, filepath.Join(dataDir, "pebble"), walDir, "wal_dir="+walDir)
}

// assertPebbleTuning fails t unless the Pebble store in pebbleDir recorded
// every option in want and keeps its write-ahead log in walDir alone.
func assertPebbleTuning(t *testing.T, pebbleDir, walDir string, want ...string) {
	t.Helper()
	opts, err := filepath.Glob(filepath.Join(pebbleDir, "OPTIONS-*"))
	if err != nil || len(opts) == 0 {
		t.Fatalf("no pebble OPTIONS file in %s (err %v)", pebbleDir, err)
	}
	data, err := os.ReadFile(opts[len(opts)-1])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, w := range want {
		if !strings.Contains(string(data), w) {
			t.Errorf("pebble OPTIONS lacks %q", w)
		}
	}
	if wal, _ := filepath.Glob(filepath.Join(walDir, "*.log")); len(wal) == 0 {
		t.Errorf("no WAL file in wal_dir %s", walDir)
	}
	if wal, _ := filepath.Glob(filepath.Join(pebbleDir, "*.log")); len(wal) != 0 {
		t.Errorf("WAL files %v under the data directory, want them only in wal_dir", wal)
	}
}
