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
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// newTestLogger creates a quiet logger for migration tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestMigrateIfNeeded_NoPebbleDir(t *testing.T) {
	dataDir := t.TempDir()

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded() returned unexpected error: %v", err)
	}

	// Verify no databases directory was created.
	dbsDir := filepath.Join(dataDir, databasesDir)
	if _, err := os.Stat(dbsDir); !os.IsNotExist(err) {
		t.Errorf("databases directory should not exist when no pebble dir is present")
	}
}

func TestMigrateIfNeeded_NoPebbleDir_NilLogger(t *testing.T) {
	dataDir := t.TempDir()

	// Passing nil logger should not panic and should return nil.
	err := MigrateIfNeeded(nil, dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded(nil logger) returned unexpected error: %v", err)
	}
}

func TestMigrateIfNeeded_AlreadyMigrated(t *testing.T) {
	dataDir := t.TempDir()

	// Create both old pebble dir and new databases dir to simulate
	// an already-migrated state.
	oldPebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(oldPebbleDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(pebble) returned unexpected error: %v", err)
	}

	dbsDir := filepath.Join(dataDir, databasesDir)
	if err := os.MkdirAll(dbsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(databases) returned unexpected error: %v", err)
	}

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded() returned unexpected error: %v", err)
	}

	// The old pebble dir should still exist (not moved).
	if _, err := os.Stat(oldPebbleDir); os.IsNotExist(err) {
		t.Error("old pebble directory was removed during no-op migration")
	}
}

func TestMigrateIfNeeded_MigratePebble(t *testing.T) {
	dataDir := t.TempDir()

	// Create old pebble directory with a sentinel file.
	oldPebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(oldPebbleDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(pebble) returned unexpected error: %v", err)
	}
	sentinelPath := filepath.Join(oldPebbleDir, "CURRENT")
	if err := os.WriteFile(sentinelPath, []byte("manifest"), 0o600); err != nil {
		t.Fatalf("WriteFile(CURRENT) returned unexpected error: %v", err)
	}

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded() returned unexpected error: %v", err)
	}

	// Verify old pebble dir no longer exists.
	if _, err := os.Stat(oldPebbleDir); !os.IsNotExist(err) {
		t.Error("old pebble directory still exists after migration")
	}

	// Verify default database directory structure.
	defaultDataDir := filepath.Join(dataDir, databasesDir, "default", dataSubDir)
	info, err := os.Stat(defaultDataDir)
	if err != nil {
		t.Fatalf("default data directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("default data path is not a directory")
	}

	// Verify the sentinel file was moved.
	movedSentinel := filepath.Join(defaultDataDir, "CURRENT")
	data, err := os.ReadFile(movedSentinel)
	if err != nil {
		t.Fatalf("sentinel file not found at %s: %v", movedSentinel, err)
	}
	if string(data) != "manifest" {
		t.Errorf("sentinel file content = %q, want %q", string(data), "manifest")
	}

	// Verify meta.json contents.
	metaPath := filepath.Join(dataDir, databasesDir, "default", metaFile)
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("ReadFile(meta.json) returned unexpected error: %v", err)
	}

	var meta DatabaseMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("json.Unmarshal(meta.json) returned unexpected error: %v", err)
	}
	if meta.Name != "default" {
		t.Errorf("meta.Name = %q, want %q", meta.Name, "default")
	}
	if meta.Backend != BackendPebble {
		t.Errorf("meta.Backend = %q, want %q", meta.Backend, BackendPebble)
	}

	// Verify no vectors database was created (no vectors dir existed).
	vectorsDBDir := filepath.Join(dataDir, databasesDir, "vectors")
	if _, err := os.Stat(vectorsDBDir); !os.IsNotExist(err) {
		t.Error("vectors database directory should not exist when no vectors dir was present")
	}
}

func TestMigrateIfNeeded_MigratePebbleAndVectors(t *testing.T) {
	dataDir := t.TempDir()

	// Create old pebble directory with a sentinel file.
	oldPebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(oldPebbleDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(pebble) returned unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldPebbleDir, "CURRENT"), []byte("manifest"), 0o600); err != nil {
		t.Fatalf("WriteFile(CURRENT) returned unexpected error: %v", err)
	}

	// Create old vectors directory with a sentinel file.
	oldVectorDir := filepath.Join(dataDir, "vectors")
	if err := os.MkdirAll(oldVectorDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(vectors) returned unexpected error: %v", err)
	}
	vectorSentinel := filepath.Join(oldVectorDir, "index.bin")
	if err := os.WriteFile(vectorSentinel, []byte("vector-data"), 0o600); err != nil {
		t.Fatalf("WriteFile(index.bin) returned unexpected error: %v", err)
	}

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded() returned unexpected error: %v", err)
	}

	// Verify old directories no longer exist.
	if _, err := os.Stat(oldPebbleDir); !os.IsNotExist(err) {
		t.Error("old pebble directory still exists after migration")
	}
	if _, err := os.Stat(oldVectorDir); !os.IsNotExist(err) {
		t.Error("old vectors directory still exists after migration")
	}

	// Verify default KV database.
	defaultDataDir := filepath.Join(dataDir, databasesDir, "default", dataSubDir)
	if _, err := os.Stat(defaultDataDir); err != nil {
		t.Fatalf("default data directory does not exist: %v", err)
	}

	defaultMetaPath := filepath.Join(dataDir, databasesDir, "default", metaFile)
	defaultMetaData, err := os.ReadFile(defaultMetaPath)
	if err != nil {
		t.Fatalf("ReadFile(default meta.json) returned unexpected error: %v", err)
	}
	var defaultMeta DatabaseMeta
	if err := json.Unmarshal(defaultMetaData, &defaultMeta); err != nil {
		t.Fatalf("json.Unmarshal(default meta.json) returned unexpected error: %v", err)
	}
	if defaultMeta.Name != "default" {
		t.Errorf("default meta.Name = %q, want %q", defaultMeta.Name, "default")
	}
	if defaultMeta.Backend != BackendPebble {
		t.Errorf("default meta.Backend = %q, want %q", defaultMeta.Backend, BackendPebble)
	}

	// Verify vectors database.
	vectorsDataDir := filepath.Join(dataDir, databasesDir, "vectors", dataSubDir)
	if _, err := os.Stat(vectorsDataDir); err != nil {
		t.Fatalf("vectors data directory does not exist: %v", err)
	}

	// Verify the vector sentinel file was moved.
	movedVectorSentinel := filepath.Join(vectorsDataDir, "index.bin")
	vdata, err := os.ReadFile(movedVectorSentinel)
	if err != nil {
		t.Fatalf("vector sentinel file not found at %s: %v", movedVectorSentinel, err)
	}
	if string(vdata) != "vector-data" {
		t.Errorf("vector sentinel content = %q, want %q", string(vdata), "vector-data")
	}

	vectorsMetaPath := filepath.Join(dataDir, databasesDir, "vectors", metaFile)
	vectorsMetaData, err := os.ReadFile(vectorsMetaPath)
	if err != nil {
		t.Fatalf("ReadFile(vectors meta.json) returned unexpected error: %v", err)
	}
	var vectorsMeta DatabaseMeta
	if err := json.Unmarshal(vectorsMetaData, &vectorsMeta); err != nil {
		t.Fatalf("json.Unmarshal(vectors meta.json) returned unexpected error: %v", err)
	}
	if vectorsMeta.Name != "vectors" {
		t.Errorf("vectors meta.Name = %q, want %q", vectorsMeta.Name, "vectors")
	}
	if vectorsMeta.Backend != BackendPebble {
		t.Errorf("vectors meta.Backend = %q, want %q", vectorsMeta.Backend, BackendPebble)
	}
}

func TestMigrateIfNeeded_VectorsOnly(t *testing.T) {
	dataDir := t.TempDir()

	// Create both old pebble and vectors directories.
	// "VectorsOnly" here means we verify vectors migration works when
	// both pebble and vectors exist (pebble is required for migration to trigger).
	oldPebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(oldPebbleDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(pebble) returned unexpected error: %v", err)
	}

	oldVectorDir := filepath.Join(dataDir, "vectors")
	if err := os.MkdirAll(oldVectorDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(vectors) returned unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldVectorDir, "embeddings.dat"), []byte("embed"), 0o600); err != nil {
		t.Fatalf("WriteFile(embeddings.dat) returned unexpected error: %v", err)
	}

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err != nil {
		t.Fatalf("MigrateIfNeeded() returned unexpected error: %v", err)
	}

	// Both old directories should be gone.
	if _, err := os.Stat(oldPebbleDir); !os.IsNotExist(err) {
		t.Error("old pebble directory still exists after migration")
	}
	if _, err := os.Stat(oldVectorDir); !os.IsNotExist(err) {
		t.Error("old vectors directory still exists after migration")
	}

	// Default KV database should exist.
	defaultMetaPath := filepath.Join(dataDir, databasesDir, "default", metaFile)
	if _, err := os.Stat(defaultMetaPath); err != nil {
		t.Fatalf("default meta.json does not exist: %v", err)
	}

	// Vectors database should exist with the moved file.
	vectorsDataDir := filepath.Join(dataDir, databasesDir, "vectors", dataSubDir)
	embeddingsPath := filepath.Join(vectorsDataDir, "embeddings.dat")
	data, err := os.ReadFile(embeddingsPath)
	if err != nil {
		t.Fatalf("embeddings.dat not found after migration: %v", err)
	}
	if string(data) != "embed" {
		t.Errorf("embeddings.dat content = %q, want %q", string(data), "embed")
	}

	// Verify vectors meta.json.
	vectorsMetaPath := filepath.Join(dataDir, databasesDir, "vectors", metaFile)
	metaData, err := os.ReadFile(vectorsMetaPath)
	if err != nil {
		t.Fatalf("ReadFile(vectors meta.json) returned unexpected error: %v", err)
	}
	var meta DatabaseMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("json.Unmarshal(vectors meta.json) returned unexpected error: %v", err)
	}
	if meta.Name != "vectors" {
		t.Errorf("meta.Name = %q, want %q", meta.Name, "vectors")
	}
}

func TestMigrateIfNeeded_CreateDatabasesDirError(t *testing.T) {
	dataDir := t.TempDir()

	// Create old pebble directory so migration triggers.
	oldPebbleDir := filepath.Join(dataDir, "pebble")
	if err := os.MkdirAll(oldPebbleDir, 0o750); err != nil {
		t.Fatalf("MkdirAll(pebble) returned unexpected error: %v", err)
	}

	// Place a dangling symlink where the "databases" directory would be
	// created. os.Stat follows the symlink and fails (target missing), so
	// the migration proceeds past the "already migrated" guard, but
	// os.MkdirAll fails with "file exists" because the symlink itself
	// blocks directory creation. This works even as root, unlike chmod.
	dbsDirPath := filepath.Join(dataDir, "databases")
	if err := os.Symlink("/nonexistent/blocker", dbsDirPath); err != nil {
		t.Fatalf("Symlink(databases) returned unexpected error: %v", err)
	}

	err := MigrateIfNeeded(newTestLogger(), dataDir)
	if err == nil {
		t.Fatal("MigrateIfNeeded() returned nil, want *MigrationError")
	}
	var migErr *MigrationError
	if !errors.As(err, &migErr) {
		t.Fatalf("error type = %T, want *MigrationError", err)
	}
	if migErr.Step != "create databases dir" {
		t.Errorf("MigrationError.Step = %q, want %q", migErr.Step, "create databases dir")
	}
}

func TestMigrationError_Message(t *testing.T) {
	cause := os.ErrPermission
	err := &MigrationError{Step: "create databases dir", Err: cause}
	want := `database: migration failed at step "create databases dir": permission denied`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestMigrationError_Unwrap(t *testing.T) {
	cause := os.ErrPermission
	err := &MigrationError{Step: "move pebble", Err: cause}
	if unwrapped := err.Unwrap(); unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Error("errors.Is failed to match wrapped cause")
	}
	var target *MigrationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *MigrationError")
	}
	if target.Step != "move pebble" {
		t.Errorf("Step = %q, want %q", target.Step, "move pebble")
	}
}

func TestWriteMetaFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")

	meta := DatabaseMeta{
		Name:    "testdb",
		Backend: BackendPebble,
	}

	if err := writeMetaFile(path, meta); err != nil {
		t.Fatalf("writeMetaFile() returned unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() returned unexpected error: %v", err)
	}

	var got DatabaseMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() returned unexpected error: %v", err)
	}
	if got.Name != "testdb" {
		t.Errorf("Name = %q, want %q", got.Name, "testdb")
	}
	if got.Backend != BackendPebble {
		t.Errorf("Backend = %q, want %q", got.Backend, BackendPebble)
	}
}

func TestWriteMetaFile_InvalidPath(t *testing.T) {
	// Write to a path where the parent directory does not exist.
	path := filepath.Join(t.TempDir(), "nonexistent", "deep", "meta.json")

	meta := DatabaseMeta{
		Name:    "testdb",
		Backend: BackendMemory,
	}

	err := writeMetaFile(path, meta)
	if err == nil {
		t.Fatal("writeMetaFile() returned nil, want error for invalid path")
	}
}
