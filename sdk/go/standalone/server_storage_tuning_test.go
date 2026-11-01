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

package standalone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// TestNewServer_PebbleTakesTheStorageTuning pins that ServerConfig's
// CacheSize, WALDir and MaxOpenFiles reach Pebble: its OPTIONS file records
// them and the write-ahead log is in WALDir, not under DataDir.
func TestNewServer_PebbleTakesTheStorageTuning(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	walDir := filepath.Join(t.TempDir(), "wal")
	srv, err := NewServer(ctx, &ServerConfig{
		Backend:      database.BackendPebble,
		DataDir:      dataDir,
		CacheSize:    32 << 20,
		WALDir:       walDir,
		MaxOpenFiles: 123,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(ctx) })

	pebbleDir := filepath.Join(dataDir, "pebble")
	options := readPebbleOptions(t, pebbleDir)
	for _, want := range []string{"cache_size=33554432", "max_open_files=123", "wal_dir=" + walDir} {
		if !strings.Contains(options, want) {
			t.Errorf("pebble OPTIONS lacks %q", want)
		}
	}
	if len(globWAL(t, walDir)) == 0 {
		t.Errorf("no WAL file in WALDir %s", walDir)
	}
	if got := globWAL(t, pebbleDir); len(got) != 0 {
		t.Errorf("WAL files %v under DataDir, want them only in WALDir", got)
	}
}

// TestCreateServerStorage_ZeroTuningKeepsTheWALUnderDataDir pins that an
// unset WALDir leaves the write-ahead log beside the data.
func TestCreateServerStorage_ZeroTuningKeepsTheWALUnderDataDir(t *testing.T) {
	dataDir := t.TempDir()
	stg, pdb, err := createServerStorage(database.BackendPebble, dataDir, nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("createServerStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	if pdb == nil {
		t.Fatal("the pebble backend returned no *pebble.DB handle")
	}
	pebbleDir := filepath.Join(dataDir, "pebble")
	if len(globWAL(t, pebbleDir)) == 0 {
		t.Error("no WAL file under DataDir with WALDir unset")
	}
	want := fmt.Sprintf("max_open_files=%d", pebbleStorage.DefaultConfig(pebbleDir).MaxOpenFiles)
	if !strings.Contains(readPebbleOptions(t, pebbleDir), want) {
		t.Errorf("pebble OPTIONS lacks the default %q", want)
	}
}

// readPebbleOptions returns the newest OPTIONS file Pebble wrote in dir.
func readPebbleOptions(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "OPTIONS-*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no pebble OPTIONS file in %s (err %v)", dir, err)
	}
	data, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(data)
}

// globWAL returns the Pebble write-ahead log files in dir.
func globWAL(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return matches
}
