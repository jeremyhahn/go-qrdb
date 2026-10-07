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
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// timeSeriesPrefixes are the key prefixes used by the time-series engine.
var timeSeriesPrefixes = []string{"ts:", "idx:metric:", "idx:tag:", "retention:"}

// MigrateIfNeeded checks whether a one-time migration from the flat
// PebbleDB layout to the per-database directory structure is required.
// Migration is needed when {dataDir}/pebble/ exists but {dataDir}/databases/ does not.
//
// The migration:
//  1. Creates databases/default/ as a KV database pointing to the existing pebble dir (symlink or move)
//  2. If vector data exists at {dataDir}/vectors/, creates databases/vectors/
//  3. Logs migration progress
//
// NOTE: TimeSeries key separation (scanning and moving TS keys to a separate DB)
// is deferred to a future phase because it requires PebbleDB iteration which
// is complex and risk-prone during migration. Phase 1's key filtering already
// hides TS keys from the KV browser.
func MigrateIfNeeded(logger *slog.Logger, dataDir string) error {
	if logger == nil {
		logger = slog.Default()
	}

	oldPebbleDir := filepath.Join(dataDir, "pebble")
	newDbsDir := filepath.Join(dataDir, databasesDir)

	// Check if migration is needed.
	if _, err := os.Stat(oldPebbleDir); os.IsNotExist(err) {
		return nil // no old layout, nothing to migrate
	}
	if _, err := os.Stat(newDbsDir); err == nil {
		return nil // new layout already exists, already migrated
	}

	logger.Info("migrating to per-database directory layout",
		slog.String("data_dir", dataDir))

	// Step 1: Create databases directory.
	if err := os.MkdirAll(newDbsDir, 0o750); err != nil {
		return &MigrationError{Step: "create databases dir", Err: err}
	}

	// Step 2: Move pebble/ -> databases/default/data/
	defaultDBDir := filepath.Join(newDbsDir, "default")
	if err := os.MkdirAll(defaultDBDir, 0o750); err != nil {
		return &MigrationError{Step: "create default db dir", Err: err}
	}

	// Durable move: this rename is the migration's commit point for the KV
	// data, and it crosses directories. If the source's loss of "pebble/" is
	// durable but the destination's gain of "default/data" is not, a crash
	// recovers a data dir with no name at all; if the reverse, MigrateIfNeeded
	// re-runs, finds databases/ present, and returns early -- leaving the old
	// layout beside the new one (go-qrdb#268).
	defaultDataDir := filepath.Join(defaultDBDir, dataSubDir)
	if err := fsync.Rename(oldPebbleDir, defaultDataDir); err != nil {
		return &MigrationError{Step: "move pebble to default/data", Err: err}
	}

	// Write meta.json for default KV database.
	meta := DatabaseMeta{
		Name:    "default",
		Backend: BackendPebble,
	}
	if err := writeMetaFile(filepath.Join(defaultDBDir, metaFile), meta); err != nil {
		return &MigrationError{Step: "write default meta.json", Err: err}
	}

	logger.Info("migrated KV data to default database",
		slog.String("path", defaultDataDir))

	// Step 3: Check for vector data.
	oldVectorDir := filepath.Join(dataDir, "vectors")
	if info, err := os.Stat(oldVectorDir); err == nil && info.IsDir() {
		vectorDBDir := filepath.Join(newDbsDir, "vectors")
		if err := os.MkdirAll(vectorDBDir, 0o750); err != nil {
			return &MigrationError{Step: "create vectors db dir", Err: err}
		}

		vectorDataDir := filepath.Join(vectorDBDir, dataSubDir)
		if err := fsync.Rename(oldVectorDir, vectorDataDir); err != nil {
			return &MigrationError{Step: "move vectors to vectors/data", Err: err}
		}

		vectorMeta := DatabaseMeta{
			Name:    "vectors",
			Backend: BackendPebble,
		}
		if err := writeMetaFile(filepath.Join(vectorDBDir, metaFile), vectorMeta); err != nil {
			return &MigrationError{Step: "write vectors meta.json", Err: err}
		}

		logger.Info("migrated vector data to vectors database",
			slog.String("path", vectorDataDir))
	}

	logger.Info("migration complete")
	return nil
}

// isTimeSeriesKey reports whether the key belongs to the time-series engine.
func isTimeSeriesKey(key string) bool {
	for _, prefix := range timeSeriesPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// writeMetaFile writes a DatabaseMeta as JSON to the given path.
func writeMetaFile(path string, meta DatabaseMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return &MetaWriteError{Path: path, Err: err}
	}
	return os.WriteFile(path, data, 0o600)
}
