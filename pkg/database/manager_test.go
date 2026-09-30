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
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// failingCloseBackendError is a sentinel error used by failingCloseBackend.
type failingCloseBackendError struct{}

func (e *failingCloseBackendError) Error() string {
	return "backend: simulated close failure in manager tests"
}

// failingCloseBackend implements Backend but returns an error on Close.
type failingCloseBackend struct{}

func (b *failingCloseBackend) Get(_ context.Context, _ []byte) ([]byte, error)    { return nil, nil }
func (b *failingCloseBackend) Put(_ context.Context, _, _ []byte) error           { return nil }
func (b *failingCloseBackend) Delete(_ context.Context, _ []byte) error           { return nil }
func (b *failingCloseBackend) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (b *failingCloseBackend) Range(_ context.Context, _, _ []byte, _ int) ([]KeyValue, error) {
	return nil, nil
}
func (b *failingCloseBackend) Batch(_ context.Context, _ []BatchOp) error { return nil }
func (b *failingCloseBackend) Close() error {
	return &failingCloseBackendError{}
}

// newTestManager creates a Manager rooted at a temporary directory.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewManager(logger, dataDir)
}

// validMemoryConfig returns a DatabaseConfig with the given name using the
// in-memory backend and the KV engine. This config has NO dedicated store
// (empty DataDir, non-Raft backend): HasDedicatedStore() is false, so it must
// not be used with Barrier.Enabled -- use validDedicatedMemoryConfig instead.
func validMemoryConfig(name string) DatabaseConfig {
	return DatabaseConfig{
		Name:    name,
		Engine:  EngineKV,
		Backend: BackendMemory,
	}
}

// validDedicatedMemoryConfig returns a DatabaseConfig identical to
// validMemoryConfig but with an explicit DataDir, so HasDedicatedStore()
// reports true and the config is eligible to enable a per-database
// encryption barrier.
func validDedicatedMemoryConfig(t *testing.T, name string) DatabaseConfig {
	t.Helper()
	cfg := validMemoryConfig(name)
	cfg.DataDir = t.TempDir()
	return cfg
}

func TestManager_Create(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("testdb")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if db.Name() != "testdb" {
		t.Errorf("Name() = %q, want %q", db.Name(), "testdb")
	}
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}

	// Verify it appears in List.
	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(list))
	}
	if list[0].Name != "testdb" {
		t.Errorf("List()[0].Name = %q, want %q", list[0].Name, "testdb")
	}
}

func TestManager_Create_InvalidConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	tests := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{
			name: "empty_name",
			cfg:  DatabaseConfig{Name: "", Engine: EngineKV, Backend: BackendMemory},
		},
		{
			name: "invalid_name_chars",
			cfg:  DatabaseConfig{Name: "my.db", Engine: EngineKV, Backend: BackendMemory},
		},
		{
			name: "invalid_engine",
			cfg:  DatabaseConfig{Name: "testdb", Engine: EngineType("bad"), Backend: BackendMemory},
		},
		{
			name: "invalid_backend",
			cfg:  DatabaseConfig{Name: "testdb", Engine: EngineKV, Backend: BackendType("bad")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mgr.Create(tt.cfg)
			if err == nil {
				t.Fatal("Create() returned nil, want error for invalid config")
			}
		})
	}
}

func TestManager_Create_Duplicate(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("dupdb")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("first Create() returned unexpected error: %v", err)
	}

	_, err := mgr.Create(cfg)
	if err == nil {
		t.Fatal("second Create() returned nil, want *AlreadyExistsError")
	}
	var existsErr *AlreadyExistsError
	if !errors.As(err, &existsErr) {
		t.Fatalf("second Create() error type = %T, want *AlreadyExistsError", err)
	}
	if existsErr.Name != "dupdb" {
		t.Errorf("AlreadyExistsError.Name = %q, want %q", existsErr.Name, "dupdb")
	}
}

func TestManager_Create_PebbleBackend(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "pebble-db",
		Engine:  EngineKV,
		Backend: BackendPebble,
	}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create(pebble) returned unexpected error: %v", err)
	}
	if db.BackendType() != BackendPebble {
		t.Errorf("BackendType() = %q, want %q", db.BackendType(), BackendPebble)
	}
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}

	// Verify the meta.json file was written.
	metaPath := filepath.Join(dataDir, databasesDir, "pebble-db", metaFile)
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Errorf("meta.json not found at %s", metaPath)
	}
}

func TestManager_Create_FileBackend(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "file-db",
		Engine:  EngineKV,
		Backend: BackendFile,
	}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create(file) returned unexpected error: %v", err)
	}
	if db.BackendType() != BackendFile {
		t.Errorf("BackendType() = %q, want %q", db.BackendType(), BackendFile)
	}
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}

	// Verify the data directory was created.
	dataDirPath := filepath.Join(dataDir, databasesDir, "file-db", dataSubDir)
	info, err := os.Stat(dataDirPath)
	if os.IsNotExist(err) {
		t.Errorf("data directory not found at %s", dataDirPath)
	} else if !info.IsDir() {
		t.Errorf("data path is not a directory: %s", dataDirPath)
	}
}

func TestManager_Open(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a database with the first manager, then close the manager.
	mgr1 := NewManager(logger, dataDir)
	cfg := validMemoryConfig("open-db")
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Create a new manager and open the existing database.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("open-db")
	if err != nil {
		t.Fatalf("Open() returned unexpected error: %v", err)
	}
	if db.Name() != "open-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "open-db")
	}
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}
	if db.Engine() != EngineKV {
		t.Errorf("Engine() = %q, want %q", db.Engine(), EngineKV)
	}
	if db.BackendType() != BackendMemory {
		t.Errorf("BackendType() = %q, want %q", db.BackendType(), BackendMemory)
	}
}

func TestManager_Open_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.Open("nonexistent")
	if err == nil {
		t.Fatal("Open() returned nil, want error for non-existent database")
	}
	var metaErr *MetaReadError
	if !errors.As(err, &metaErr) {
		t.Fatalf("Open() error type = %T, want *MetaReadError", err)
	}
}

func TestManager_Open_AlreadyOpen(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("already-open-db")
	created, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Open the same database again; it should return the existing instance.
	opened, err := mgr.Open("already-open-db")
	if err != nil {
		t.Fatalf("Open() returned unexpected error: %v", err)
	}
	if opened != created {
		t.Error("Open() returned a different instance, want same pointer as Create()")
	}
}

func TestManager_Get(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("get-db")
	created, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	got, err := mgr.Get("get-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got != created {
		t.Error("Get() returned a different instance than Create()")
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.Get("missing-db")
	if err == nil {
		t.Fatal("Get() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Get() error type = %T, want *NotFoundError", err)
	}
	if notFoundErr.Name != "missing-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "missing-db")
	}
}

func TestManager_Drop(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("drop-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if err := mgr.Drop(context.Background(), "drop-db"); err != nil {
		t.Fatalf("Drop() returned unexpected error: %v", err)
	}

	// Verify removed from List.
	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() returned %d entries after Drop(), want 0", len(list))
	}

	// Verify Get returns NotFoundError.
	_, err := mgr.Get("drop-db")
	if err == nil {
		t.Fatal("Get() after Drop() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Get() after Drop() error type = %T, want *NotFoundError", err)
	}

	// Verify directory was deleted.
	dbDir := filepath.Join(dataDir, databasesDir, "drop-db")
	if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
		t.Errorf("database directory still exists at %s after Drop()", dbDir)
	}
}

func TestManager_Drop_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.Drop(context.Background(), "phantom-db")
	if err == nil {
		t.Fatal("Drop() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("Drop() error type = %T, want *NotFoundError", err)
	}
	if notFoundErr.Name != "phantom-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "phantom-db")
	}
}

func TestManager_List(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		if _, err := mgr.Create(validMemoryConfig(name)); err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
	}

	list := mgr.List()
	if len(list) != 3 {
		t.Fatalf("List() returned %d entries, want 3", len(list))
	}

	// Verify sorted order.
	expected := []string{"alpha", "beta", "gamma"}
	for i, want := range expected {
		if list[i].Name != want {
			t.Errorf("List()[%d].Name = %q, want %q", i, list[i].Name, want)
		}
	}
}

func TestManager_List_Empty(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() on empty manager returned %d entries, want 0", len(list))
	}
}

func TestManager_Close(t *testing.T) {
	mgr := newTestManager(t)

	names := []string{"close-a", "close-b"}
	dbs := make([]*Database, 0, len(names))
	for _, name := range names {
		db, err := mgr.Create(validMemoryConfig(name))
		if err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
		dbs = append(dbs, db)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Both databases should be closed.
	for _, db := range dbs {
		if got := db.Status(); got != StatusClosed {
			t.Errorf("database %q status = %q after manager Close(), want %q",
				db.Name(), got, StatusClosed)
		}
	}

	// List should be empty after close.
	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() after Close() returned %d entries, want 0", len(list))
	}
}

func TestManager_OpenAll(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create two databases and close the manager.
	mgr1 := NewManager(logger, dataDir)
	names := []string{"openall-a", "openall-b"}
	for _, name := range names {
		if _, err := mgr1.Create(validMemoryConfig(name)); err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Create a fresh manager and call OpenAll.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() returned unexpected error: %v", err)
	}

	list := mgr2.List()
	if len(list) != 2 {
		t.Fatalf("List() after OpenAll() returned %d entries, want 2", len(list))
	}

	// Verify both databases are accessible and open.
	for _, name := range names {
		db, err := mgr2.Get(name)
		if err != nil {
			t.Fatalf("Get(%q) after OpenAll() returned unexpected error: %v", name, err)
		}
		if db.Status() != StatusOpen {
			t.Errorf("database %q status = %q after OpenAll(), want %q", name, db.Status(), StatusOpen)
		}
	}
}

func TestManager_OpenAll_EmptyDir(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// No databases directory exists yet; OpenAll should succeed without error.
	if err := mgr.OpenAll(); err != nil {
		t.Fatalf("OpenAll() on empty data dir returned unexpected error: %v", err)
	}

	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() after OpenAll() on empty dir returned %d entries, want 0", len(list))
	}
}

func TestManager_DataDir(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	if got := mgr.DataDir(); got != dataDir {
		t.Errorf("DataDir() = %q, want %q", got, dataDir)
	}
}

func TestManager_DataDir_Stable(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	dir1 := mgr.DataDir()
	dir2 := mgr.DataDir()
	if dir1 != dir2 {
		t.Errorf("DataDir() returned different values on consecutive calls: %q vs %q", dir1, dir2)
	}
}

func TestManager_NewManager_NilLogger(t *testing.T) {
	dataDir := t.TempDir()
	mgr := NewManager(nil, dataDir)
	defer func() { _ = mgr.Close() }()

	// Should use slog.Default() without panicking.
	cfg := validMemoryConfig("nil-logger-db")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() with nil logger returned unexpected error: %v", err)
	}
	if db.Name() != "nil-logger-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "nil-logger-db")
	}
}

func TestManager_Create_RaftBackend_PlaceholderBackend(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "raft-db",
		Engine:  EngineKV,
		Backend: BackendRaft,
	}
	// A raft-backed database is created with an in-memory placeholder Backend so
	// its meta.json and catalog membership are tracked; the authoritative data
	// plane is the per-database shard on the shared system Host (IsolationRaft),
	// wired at the server layer.
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create(raft) returned unexpected error: %v", err)
	}
	if db.Info().Backend != BackendRaft {
		t.Fatalf("Create(raft) backend = %q, want %q", db.Info().Backend, BackendRaft)
	}
}

func TestManager_List_SortOrder(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	// Create in reverse alphabetical order to verify sorting.
	names := []string{"zebra", "apple", "mango"}
	for _, name := range names {
		if _, err := mgr.Create(validMemoryConfig(name)); err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
	}

	list := mgr.List()
	expected := []string{"apple", "mango", "zebra"}
	if len(list) != len(expected) {
		t.Fatalf("List() returned %d entries, want %d", len(list), len(expected))
	}
	for i, want := range expected {
		if list[i].Name != want {
			t.Errorf("List()[%d].Name = %q, want %q", i, list[i].Name, want)
		}
	}
}

func TestManager_Drop_VerifiesDirectoryCleanup(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "pebble-drop",
		Engine:  EngineKV,
		Backend: BackendPebble,
	}
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Verify the database directory exists before drop.
	dbDir := filepath.Join(dataDir, databasesDir, "pebble-drop")
	if _, err := os.Stat(dbDir); os.IsNotExist(err) {
		t.Fatalf("database directory does not exist before Drop(): %s", dbDir)
	}

	if err := mgr.Drop(context.Background(), "pebble-drop"); err != nil {
		t.Fatalf("Drop() returned unexpected error: %v", err)
	}

	// Verify the database directory is removed.
	if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
		t.Errorf("database directory still exists after Drop(): %s", dbDir)
	}
}

func TestManager_OpenAll_SkipsNonDirectoryEntries(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create the databases directory with a plain file inside (not a DB dir).
	dbsDir := filepath.Join(dataDir, databasesDir)
	if err := os.MkdirAll(dbsDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() returned unexpected error: %v", err)
	}
	spuriousFile := filepath.Join(dbsDir, "not-a-database.txt")
	if err := os.WriteFile(spuriousFile, []byte("junk"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned unexpected error: %v", err)
	}

	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	// OpenAll should skip files and not return an error.
	if err := mgr.OpenAll(); err != nil {
		t.Fatalf("OpenAll() returned unexpected error: %v", err)
	}

	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() returned %d entries, want 0", len(list))
	}
}

func TestManager_Close_Idempotent(t *testing.T) {
	mgr := newTestManager(t)

	cfg := validMemoryConfig("idempotent-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if err := mgr.Close(); err != nil {
		t.Fatalf("first Close() returned unexpected error: %v", err)
	}

	// Second close should succeed (no databases left).
	if err := mgr.Close(); err != nil {
		t.Fatalf("second Close() returned unexpected error: %v", err)
	}
}

func TestManager_Create_VerifiesMetaJSON(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "meta-check",
		Engine:  EngineTimeSeries,
		Backend: BackendMemory,
	}
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	metaPath := filepath.Join(dataDir, databasesDir, "meta-check", metaFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned unexpected error: %v", metaPath, err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Fatal("meta.json is empty")
	}

	// Verify basic content without importing encoding/json in assertions.
	if !containsSubstring(content, `"name": "meta-check"`) {
		t.Errorf("meta.json missing name field, content: %s", content)
	}
	if !containsSubstring(content, `"engine": "timeseries"`) {
		t.Errorf("meta.json missing engine field, content: %s", content)
	}
	if !containsSubstring(content, `"backend": "memory"`) {
		t.Errorf("meta.json missing backend field, content: %s", content)
	}
}

func TestManager_Open_CorruptedMetaJSON(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create the database directory with corrupted meta.json.
	dbDir := filepath.Join(dataDir, databasesDir, "corrupt-db")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() returned unexpected error: %v", err)
	}
	metaPath := filepath.Join(dbDir, metaFile)
	if err := os.WriteFile(metaPath, []byte("{invalid json"), 0o600); err != nil {
		t.Fatalf("WriteFile() returned unexpected error: %v", err)
	}

	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.Open("corrupt-db")
	if err == nil {
		t.Fatal("Open() returned nil, want *MetaReadError for corrupted JSON")
	}
	var metaErr *MetaReadError
	if !errors.As(err, &metaErr) {
		t.Fatalf("Open() error type = %T, want *MetaReadError", err)
	}
}

func TestManager_Drop_ClosedDatabase(t *testing.T) {
	mgr := newTestManager(t)

	cfg := validMemoryConfig("drop-closed-db")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Close the database before dropping.
	if err := db.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Drop should succeed even when the database is already closed.
	if err := mgr.Drop(context.Background(), "drop-closed-db"); err != nil {
		t.Fatalf("Drop() on closed database returned unexpected error: %v", err)
	}

	// Verify removed from listing.
	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("List() returned %d entries after Drop(), want 0", len(list))
	}
}

func TestManager_OpenAll_SkipsCorruptedDatabases(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a valid database.
	mgr1 := NewManager(logger, dataDir)
	if _, err := mgr1.Create(validMemoryConfig("valid-db")); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Create a corrupted database directory (directory with no meta.json).
	corruptDir := filepath.Join(dataDir, databasesDir, "broken-db")
	if err := os.MkdirAll(corruptDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() returned unexpected error: %v", err)
	}

	// OpenAll should open the valid database and skip the broken one.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() returned unexpected error: %v", err)
	}

	list := mgr2.List()
	if len(list) != 1 {
		t.Fatalf("List() after OpenAll() returned %d entries, want 1", len(list))
	}
	if list[0].Name != "valid-db" {
		t.Errorf("List()[0].Name = %q, want %q", list[0].Name, "valid-db")
	}
}

func TestManager_Open_PebbleBackend(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a pebble-backed database, then close the manager.
	mgr1 := NewManager(logger, dataDir)
	cfg := DatabaseConfig{
		Name:    "pebble-reopen",
		Engine:  EngineKV,
		Backend: BackendPebble,
	}
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create(pebble) returned unexpected error: %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Reopen with a new manager.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("pebble-reopen")
	if err != nil {
		t.Fatalf("Open(pebble) returned unexpected error: %v", err)
	}
	if db.BackendType() != BackendPebble {
		t.Errorf("BackendType() = %q, want %q", db.BackendType(), BackendPebble)
	}
	if db.Status() != StatusOpen {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusOpen)
	}
}

func TestManager_Drop_CloseError(t *testing.T) {
	mgr := newTestManager(t)

	// Inject a database with a failing backend directly into the sync.Map.
	cfg := DatabaseConfig{
		Name:    "fail-drop-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
	}
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test/fail-drop-db")
	mgr.databases.Store("fail-drop-db", db)
	// The catalog directory a real database has, so the drop gets past
	// persisting its local fence to the close under test.
	if err := os.MkdirAll(mgr.dbDir("fail-drop-db"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err := mgr.Drop(context.Background(), "fail-drop-db")
	if err == nil {
		t.Fatal("Drop() returned nil, want *DropError")
	}
	var dropErr *DropError
	if !errors.As(err, &dropErr) {
		t.Fatalf("Drop() error type = %T, want *DropError", err)
	}
	if dropErr.Name != "fail-drop-db" {
		t.Errorf("DropError.Name = %q, want %q", dropErr.Name, "fail-drop-db")
	}
}

func TestManager_Close_WithFailingBackend(t *testing.T) {
	mgr := newTestManager(t)

	// Inject a database with a failing backend directly into the sync.Map.
	cfg := DatabaseConfig{
		Name:    "fail-close-mgr-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
	}
	db := NewDatabase(cfg, &failingCloseBackend{}, "/tmp/test/fail-close-mgr-db")
	mgr.databases.Store("fail-close-mgr-db", db)

	err := mgr.Close()
	if err == nil {
		t.Fatal("Close() returned nil, want error from failing backend")
	}
}

func TestManager_List_InfoContents(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "info-list-db",
		Engine:  EngineTimeSeries,
		Backend: BackendMemory,
	}
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d entries, want 1", len(list))
	}

	info := list[0]
	if info.Engine != EngineTimeSeries {
		t.Errorf("Info.Engine = %q, want %q", info.Engine, EngineTimeSeries)
	}
	if info.Backend != BackendMemory {
		t.Errorf("Info.Backend = %q, want %q", info.Backend, BackendMemory)
	}
	if info.Status != StatusOpen {
		t.Errorf("Info.Status = %q, want %q", info.Status, StatusOpen)
	}
}

// ---------------------------------------------------------------------------
// Shard allocator tests
// ---------------------------------------------------------------------------

// validRaftConfig returns a DatabaseRaftConfig using the given shard ID.
// Pass shardID == 0 to request auto-allocation.
func validRaftConfig(shardID uint64) *DatabaseRaftConfig {
	return &DatabaseRaftConfig{
		ClusterMode: RaftNone,
		ShardID:     shardID,
	}
}

func TestManager_CreateWithAllocator(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	alloc := shard.NewAllocator()
	mgr := NewManager(logger, dataDir, WithAllocator(alloc))
	defer func() { _ = mgr.Close() }()

	names := []string{"alloc-a", "alloc-b", "alloc-c"}
	seen := make(map[uint64]string, len(names))

	for _, name := range names {
		cfg := DatabaseConfig{
			Name:    name,
			Engine:  EngineKV,
			Backend: BackendMemory,
			Raft:    validRaftConfig(0), // ShardID == 0 → auto-allocate
		}
		db, err := mgr.Create(cfg)
		if err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
		id := db.ShardID()
		if id == 0 {
			t.Errorf("database %q: ShardID() = 0 after auto-allocation, want non-zero", name)
		}
		if prev, exists := seen[id]; exists {
			t.Errorf("duplicate shard ID %d assigned to %q and %q", id, name, prev)
		}
		seen[id] = name
	}
}

func TestManager_CreatePreservesExplicitShardID(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	alloc := shard.NewAllocator()
	mgr := NewManager(logger, dataDir, WithAllocator(alloc))
	defer func() { _ = mgr.Close() }()

	const wantShardID uint64 = 42

	cfg := DatabaseConfig{
		Name:    "explicit-shard-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
		Raft:    validRaftConfig(wantShardID),
	}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if got := db.ShardID(); got != wantShardID {
		t.Errorf("ShardID() = %d, want %d (explicit shard ID must not be overwritten)", got, wantShardID)
	}
}

func TestManager_OpenAllResumesAllocator(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Phase 1: create three databases with auto-allocated shard IDs.
	alloc1 := shard.NewAllocator()
	mgr1 := NewManager(logger, dataDir, WithAllocator(alloc1))

	names := []string{"resume-a", "resume-b", "resume-c"}
	var maxShardID uint64
	for _, name := range names {
		cfg := DatabaseConfig{
			Name:    name,
			Engine:  EngineKV,
			Backend: BackendMemory,
			Raft:    validRaftConfig(0),
		}
		db, err := mgr1.Create(cfg)
		if err != nil {
			t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
		}
		if id := db.ShardID(); id > maxShardID {
			maxShardID = id
		}
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	if maxShardID == 0 {
		t.Fatal("no shard IDs were allocated in phase 1")
	}

	// Phase 2: create a new manager with a fresh allocator starting at UserMin,
	// call OpenAll to re-discover existing databases, then create a new database.
	alloc2 := shard.NewAllocator()
	mgr2 := NewManager(logger, dataDir, WithAllocator(alloc2))
	defer func() { _ = mgr2.Close() }()

	if err := mgr2.OpenAll(); err != nil {
		t.Fatalf("OpenAll() returned unexpected error: %v", err)
	}

	// The allocator must have advanced past maxShardID.
	if got := alloc2.Next(); got <= maxShardID {
		t.Errorf("allocator.Next() = %d after OpenAll, want > %d (max shard ID from phase 1)", got, maxShardID)
	}

	// A newly created database must receive a shard ID strictly greater than
	// every shard ID assigned in phase 1.
	newCfg := DatabaseConfig{
		Name:    "resume-new",
		Engine:  EngineKV,
		Backend: BackendMemory,
		Raft:    validRaftConfig(0),
	}
	newDB, err := mgr2.Create(newCfg)
	if err != nil {
		t.Fatalf("Create(resume-new) returned unexpected error: %v", err)
	}
	if newID := newDB.ShardID(); newID <= maxShardID {
		t.Errorf("new database ShardID() = %d, want > %d (must not reuse phase-1 shard IDs)", newID, maxShardID)
	}
}

// containsSubstring reports whether s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

// searchSubstring performs a simple substring search.
func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestManager_WithBackendFactory(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Use a custom factory via WithBackendFactory option.
	customFactory := NewEmbeddedBackendFactory()
	mgr := NewManager(logger, dataDir, WithBackendFactory(customFactory))
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("factory-db")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() with custom factory returned unexpected error: %v", err)
	}
	if db.Name() != "factory-db" {
		t.Errorf("Name() = %q, want %q", db.Name(), "factory-db")
	}
}

func TestManager_UpdateEtcd_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateEtcd("nonexistent", &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"})
	if err == nil {
		t.Fatal("UpdateEtcd() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateEtcd() error type = %T, want *NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent")
	}
}

func TestManager_UpdateEtcd_InvalidConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("etcd-invalid-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Etcd enabled but missing ListenAddr should fail validation.
	err := mgr.UpdateEtcd("etcd-invalid-db", &EtcdConfig{Enabled: true, ListenAddr: ""})
	if err == nil {
		t.Fatal("UpdateEtcd() returned nil, want error for invalid etcd config")
	}
	var addrErr *EtcdListenAddrRequiredError
	if !errors.As(err, &addrErr) {
		t.Fatalf("UpdateEtcd() error type = %T, want *EtcdListenAddrRequiredError", err)
	}
}

func TestManager_UpdateRaft_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateRaft("nonexistent", &DatabaseRaftConfig{ClusterMode: RaftNone})
	if err == nil {
		t.Fatal("UpdateRaft() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateRaft() error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateRaft_InvalidConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("raft-invalid-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Invalid cluster mode should fail validation.
	err := mgr.UpdateRaft("raft-invalid-db", &DatabaseRaftConfig{ClusterMode: RaftClusterMode("bad-mode")})
	if err == nil {
		t.Fatal("UpdateRaft() returned nil, want error for invalid raft config")
	}
	var raftErr *InvalidRaftClusterModeError
	if !errors.As(err, &raftErr) {
		t.Fatalf("UpdateRaft() error type = %T, want *InvalidRaftClusterModeError", err)
	}
}

func TestManager_UpdateBarrier_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateBarrier("nonexistent", &DatabaseBarrierConfig{Enabled: true})
	if err == nil {
		t.Fatal("UpdateBarrier() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateBarrier() error type = %T, want *NotFoundError", err)
	}
}

// TestManager_UpdateBarrier_Enable_RejectedNotMigrated verifies that
// enabling a barrier via UpdateBarrier on a database that was created
// WITHOUT one active is rejected: this API never re-encrypts existing data,
// so a barrier's Enabled state is fixed at creation time.
func TestManager_UpdateBarrier_Enable_RejectedNotMigrated(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validDedicatedMemoryConfig(t, "barrier-update-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	barrierCfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	err := mgr.UpdateBarrier("barrier-update-db", barrierCfg)
	if err == nil {
		t.Fatal("UpdateBarrier() enabling a never-active barrier = nil, want *BarrierMigrationNotSupportedError")
	}
	var migErr *BarrierMigrationNotSupportedError
	if !errors.As(err, &migErr) {
		t.Fatalf("error type = %T, want *BarrierMigrationNotSupportedError", err)
	}
	if migErr.Direction != "enable" {
		t.Errorf("BarrierMigrationNotSupportedError.Direction = %q, want %q", migErr.Direction, "enable")
	}

	// The rejected update must not have been persisted.
	db, getErr := mgr.Get("barrier-update-db")
	if getErr != nil {
		t.Fatalf("Get() returned unexpected error: %v", getErr)
	}
	if db.BarrierEnabled() {
		t.Error("BarrierEnabled() = true after a rejected UpdateBarrier, want false")
	}
}

// TestManager_UpdateBarrier_Disable_RejectedWhenActive verifies that
// disabling an ACTIVELY barrier-wrapped database via UpdateBarrier is
// rejected: doing so without decrypting the existing ciphertext would
// corrupt (not remove) protection, since subsequent reads would stop
// decrypting data that is still encrypted on disk.
func TestManager_UpdateBarrier_Disable_RejectedWhenActive(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	cfg := validDedicatedMemoryConfig(t, "barrier-active-disable-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds()); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}

	err := mgr.UpdateBarrier("barrier-active-disable-db", &DatabaseBarrierConfig{Enabled: false})
	if err == nil {
		t.Fatal("UpdateBarrier() disabling an active barrier = nil, want *BarrierMigrationNotSupportedError")
	}
	var migErr *BarrierMigrationNotSupportedError
	if !errors.As(err, &migErr) {
		t.Fatalf("error type = %T, want *BarrierMigrationNotSupportedError", err)
	}
	if migErr.Direction != "disable" {
		t.Errorf("BarrierMigrationNotSupportedError.Direction = %q, want %q", migErr.Direction, "disable")
	}

	db, getErr := mgr.Get("barrier-active-disable-db")
	if getErr != nil {
		t.Fatalf("Get() returned unexpected error: %v", getErr)
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() = false after a rejected UpdateBarrier(disable), want true")
	}
}

// TestManager_UpdateBarrier_IdempotentRefresh_Allowed verifies that
// UpdateBarrier still performs the one safe transition: re-affirming
// Enabled=true on a database whose barrier is already active, which
// refreshes non-secret bookkeeping (Mode/SharesRequired/SharesTotal)
// without changing the active encryption state.
func TestManager_UpdateBarrier_IdempotentRefresh_Allowed(t *testing.T) {
	bm := newTestBarrierManager(t)
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()

	cfg := validDedicatedMemoryConfig(t, "barrier-idempotent-db")
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds()); err != nil {
		t.Fatalf("CreateWithCredentials() error = %v", err)
	}

	barrierCfg := &DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"}
	if err := mgr.UpdateBarrier("barrier-idempotent-db", barrierCfg); err != nil {
		t.Fatalf("UpdateBarrier() re-affirming an active barrier returned unexpected error: %v", err)
	}

	db, err := mgr.Get("barrier-idempotent-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() = false after idempotent UpdateBarrier, want true")
	}
	info := db.Info()
	if info.Barrier == nil || !info.Barrier.Enabled {
		t.Error("Info().Barrier.Enabled = false after idempotent UpdateBarrier, want true")
	}
}

func TestManager_UpdateConnection_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	conn := &DatabaseConnectionConfig{Transport: "grpc", Address: "127.0.0.1:9090"}
	err := mgr.UpdateConnection("nonexistent", conn)
	if err == nil {
		t.Fatal("UpdateConnection() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateConnection() error type = %T, want *NotFoundError", err)
	}
}

func TestManager_UpdateConnection_InvalidConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("conn-invalid-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Missing address should fail validation.
	err := mgr.UpdateConnection("conn-invalid-db", &DatabaseConnectionConfig{Transport: "grpc", Address: ""})
	if err == nil {
		t.Fatal("UpdateConnection() returned nil, want error for invalid connection config")
	}
	var connErr *ConnectionRequiredError
	if !errors.As(err, &connErr) {
		t.Fatalf("UpdateConnection() error type = %T, want *ConnectionRequiredError", err)
	}
}

func TestManager_UpdateConnection_Success(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("conn-update-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	conn := &DatabaseConnectionConfig{Transport: "grpc", Address: "127.0.0.1:9090"}
	if err := mgr.UpdateConnection("conn-update-db", conn); err != nil {
		t.Fatalf("UpdateConnection() returned unexpected error: %v", err)
	}

	db, err := mgr.Get("conn-update-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	info := db.Info()
	if info.Connection == nil {
		t.Fatal("Info().Connection = nil, want non-nil after UpdateConnection")
	}
	if info.Connection.Transport != "grpc" {
		t.Errorf("Connection.Transport = %q, want %q", info.Connection.Transport, "grpc")
	}
}

func TestManager_UpdateEtcd_NilConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("etcd-nil-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// nil etcd config should be accepted (disabling etcd).
	if err := mgr.UpdateEtcd("etcd-nil-db", nil); err != nil {
		t.Fatalf("UpdateEtcd(nil) returned unexpected error: %v", err)
	}
}

func TestManager_UpdateRaft_NilConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("raft-nil-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// nil raft config should be accepted.
	if err := mgr.UpdateRaft("raft-nil-db", nil); err != nil {
		t.Fatalf("UpdateRaft(nil) returned unexpected error: %v", err)
	}
}

func TestManager_UpdateConnection_NilConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("conn-nil-db")
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// nil connection config should be accepted.
	if err := mgr.UpdateConnection("conn-nil-db", nil); err != nil {
		t.Fatalf("UpdateConnection(nil) returned unexpected error: %v", err)
	}
}

// TestManager_UpdateReplicas_NotFound verifies UpdateReplicas returns
// *NotFoundError for a database that does not exist, mirroring UpdateRaft's
// not-found behavior.
func TestManager_UpdateReplicas_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateReplicas("nonexistent", 3)
	if err == nil {
		t.Fatal("UpdateReplicas() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateReplicas() error type = %T, want *NotFoundError", err)
	}
}

// TestManager_UpdateReplicas_UpdatesInMemoryConfig verifies UpdateReplicas
// updates the in-memory Database config immediately (reflected by Info() and
// Get() without a reopen).
func TestManager_UpdateReplicas_UpdatesInMemoryConfig(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("replicas-mem-db")
	cfg.Replicas = 1
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	if err := mgr.UpdateReplicas("replicas-mem-db", 3); err != nil {
		t.Fatalf("UpdateReplicas() returned unexpected error: %v", err)
	}

	db, err := mgr.Get("replicas-mem-db")
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got := db.Info().Replicas; got != 3 {
		t.Errorf("Info().Replicas = %d, want 3", got)
	}
	if got := db.Config().Replicas; got != 3 {
		t.Errorf("Config().Replicas = %d, want 3", got)
	}
}

// TestManager_UpdateReplicas_PersistsToMetaJSON verifies UpdateReplicas
// round-trips through meta.json: a manager reopened against the same data
// directory after UpdateReplicas observes the new replica count, exactly like
// UpdateRaft's persisted DatabaseRaftConfig does.
func TestManager_UpdateReplicas_PersistsToMetaJSON(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mgr1 := NewManager(logger, dataDir)
	cfg := validMemoryConfig("replicas-persist-db")
	cfg.Replicas = 1
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if err := mgr1.UpdateReplicas("replicas-persist-db", 5); err != nil {
		t.Fatalf("UpdateReplicas() returned unexpected error: %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("replicas-persist-db")
	if err != nil {
		t.Fatalf("Open() returned unexpected error: %v", err)
	}
	if got := db.Info().Replicas; got != 5 {
		t.Errorf("reopened Info().Replicas = %d, want 5 (persisted via meta.json)", got)
	}
}

func TestManager_BarrierManager_ReturnsNil(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	if bm := mgr.BarrierManager(); bm != nil {
		t.Errorf("BarrierManager() = %v, want nil for manager without barrier", bm)
	}
}

func TestManager_Create_WithSQL(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:     "sql-create-db",
		Engine:   EngineKV,
		Backend:  BackendMemory,
		Protocol: ProtocolConfig{MySQL: true, PostgreSQL: true},
	}
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Verify the in-memory config.
	sql := db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false, want true")
	}
	if !sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = false, want true")
	}

	// Verify Info() includes Protocol.
	info := db.Info()
	if !info.Protocol.MySQL {
		t.Error("Info().Protocol.MySQL = false, want true")
	}
	if !info.Protocol.PostgreSQL {
		t.Error("Info().Protocol.PostgreSQL = false, want true")
	}

	// Verify meta.json contains the Protocol config.
	metaPath := filepath.Join(dataDir, databasesDir, "sql-create-db", metaFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned unexpected error: %v", metaPath, err)
	}
	content := string(data)
	if !containsSubstring(content, `"mysql": true`) {
		t.Errorf("meta.json missing mysql field, content: %s", content)
	}
	if !containsSubstring(content, `"postgresql": true`) {
		t.Errorf("meta.json missing postgresql field, content: %s", content)
	}
}

func TestManager_Open_ReadsSQL(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Create a database with Protocol config, then close the manager.
	mgr1 := NewManager(logger, dataDir)
	cfg := DatabaseConfig{
		Name:     "sql-reopen-db",
		Engine:   EngineKV,
		Backend:  BackendMemory,
		Protocol: ProtocolConfig{MySQL: true, PostgreSQL: false},
	}
	if _, err := mgr1.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}
	if err := mgr1.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	// Create a new manager and reopen the database.
	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("sql-reopen-db")
	if err != nil {
		t.Fatalf("Open() returned unexpected error: %v", err)
	}

	sql := db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false after reopen, want true")
	}
	if sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = true after reopen, want false")
	}
}

func TestManager_UpdateProtocol_Success(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := validMemoryConfig("sql-update-mgr-db")
	db, err := mgr.Create(cfg)
	if err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Initial Protocol config should be zero.
	sql := db.ProtocolConfig()
	if sql.MySQL || sql.PostgreSQL {
		t.Fatal("initial ProtocolConfig() is not zero value")
	}

	// Update Protocol config via manager.
	if err := mgr.UpdateProtocol("sql-update-mgr-db", ProtocolConfig{MySQL: true, PostgreSQL: true}); err != nil {
		t.Fatalf("UpdateProtocol() returned unexpected error: %v", err)
	}

	// Verify in-memory state updated.
	sql = db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false after UpdateProtocol, want true")
	}
	if !sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = false after UpdateProtocol, want true")
	}
}

func TestManager_UpdateProtocol_NotFound(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	err := mgr.UpdateProtocol("nonexistent-db", ProtocolConfig{MySQL: true})
	if err == nil {
		t.Fatal("UpdateProtocol() returned nil, want *NotFoundError")
	}
	var notFoundErr *NotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("UpdateProtocol() error type = %T, want *NotFoundError", err)
	}
	if notFoundErr.Name != "nonexistent-db" {
		t.Errorf("NotFoundError.Name = %q, want %q", notFoundErr.Name, "nonexistent-db")
	}
}

func TestManager_UpdateProtocol_PersistsToMeta(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(logger, dataDir)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:     "sql-persist-db",
		Engine:   EngineKV,
		Backend:  BackendMemory,
		Protocol: ProtocolConfig{MySQL: false, PostgreSQL: false},
	}
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() returned unexpected error: %v", err)
	}

	// Update Protocol config.
	if err := mgr.UpdateProtocol("sql-persist-db", ProtocolConfig{MySQL: true, PostgreSQL: true}); err != nil {
		t.Fatalf("UpdateProtocol() returned unexpected error: %v", err)
	}

	// Read meta.json directly from disk and verify.
	metaPath := filepath.Join(dataDir, databasesDir, "sql-persist-db", metaFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned unexpected error: %v", metaPath, err)
	}
	content := string(data)
	if !containsSubstring(content, `"mysql": true`) {
		t.Errorf("meta.json missing mysql=true after UpdateProtocol, content: %s", content)
	}
	if !containsSubstring(content, `"postgresql": true`) {
		t.Errorf("meta.json missing postgresql=true after UpdateProtocol, content: %s", content)
	}

	// Also verify the meta.json can be re-read by a fresh manager.
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	mgr2 := NewManager(logger, dataDir)
	defer func() { _ = mgr2.Close() }()

	db, err := mgr2.Open("sql-persist-db")
	if err != nil {
		t.Fatalf("Open() returned unexpected error: %v", err)
	}

	sql := db.ProtocolConfig()
	if !sql.MySQL {
		t.Error("ProtocolConfig().MySQL = false after reopen, want true")
	}
	if !sql.PostgreSQL {
		t.Error("ProtocolConfig().PostgreSQL = false after reopen, want true")
	}
}
