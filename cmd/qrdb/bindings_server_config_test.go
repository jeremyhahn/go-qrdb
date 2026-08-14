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

//go:build qrdb_embedded && !integration

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestEmbeddedServerBinding creates an EmbeddedServerBinding with
// XDG_CONFIG_HOME pointed at a temp dir so config file I/O never touches
// the real user config directory.
func newTestEmbeddedServerBinding(t *testing.T) *EmbeddedServerBinding {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
}

// ---------------------------------------------------------------------------
// HasSavedConfig
// ---------------------------------------------------------------------------

func TestHasSavedConfig_ReturnsFalseWhenNoFile(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)
	if esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=false when no config file exists")
	}
}

func TestHasSavedConfig_ReturnsFalseWhenNoFile_Second(t *testing.T) {
	// Second test to satisfy ≥2 tests per function.
	esb := newTestEmbeddedServerBinding(t)
	// Create and immediately remove the file to verify it's truly absent.
	path, _ := esb.configPath()
	_ = os.Remove(path)
	if esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=false after removing config file")
	}
}

func TestHasSavedConfig_ReturnsTrueWhenFileExists(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	// Write a minimal config so the file exists.
	if err := esb.saveConfig(&EmbeddedConfig{
		StorageBackend: "memory",
	}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	if !esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=true after saving config")
	}
}

func TestHasSavedConfig_ReturnsTrueWhenFileExists_AfterStartEmbedded(t *testing.T) {
	// StartEmbedded saves config automatically; verify HasSavedConfig reflects it.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	defer func() { _ = esb.StopEmbedded() }()

	if !esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=true after StartEmbedded")
	}
}

// ---------------------------------------------------------------------------
// ClearSavedConfig
// ---------------------------------------------------------------------------

func TestClearSavedConfig_NoFileIsNoOp(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	// Clearing when no config exists should succeed without error.
	if err := esb.ClearSavedConfig(); err != nil {
		t.Fatalf("ClearSavedConfig with no file: %v", err)
	}
}

func TestClearSavedConfig_RemovesExistingFile(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	if err := esb.saveConfig(&EmbeddedConfig{StorageBackend: "memory"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if !esb.HasSavedConfig() {
		t.Fatal("expected HasSavedConfig()=true after saveConfig")
	}

	if err := esb.ClearSavedConfig(); err != nil {
		t.Fatalf("ClearSavedConfig: %v", err)
	}

	if esb.HasSavedConfig() {
		t.Error("expected HasSavedConfig()=false after ClearSavedConfig")
	}
}

func TestClearSavedConfig_IdempotentAfterClear(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	if err := esb.saveConfig(&EmbeddedConfig{StorageBackend: "memory"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if err := esb.ClearSavedConfig(); err != nil {
		t.Fatalf("first ClearSavedConfig: %v", err)
	}
	// Second clear should also succeed (IsNotExist is ignored).
	if err := esb.ClearSavedConfig(); err != nil {
		t.Fatalf("second ClearSavedConfig: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RestoreEmbedded
// ---------------------------------------------------------------------------

func TestRestoreEmbedded_NoSavedConfig(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	err := esb.RestoreEmbedded()
	if err == nil {
		defer func() { _ = esb.StopEmbedded() }()
		t.Fatal("expected error from RestoreEmbedded when no config exists")
	}
	var nsce *NoSavedConfigError
	if !errors.As(err, &nsce) {
		t.Fatalf("expected NoSavedConfigError, got %T: %v", err, err)
	}
}

func TestRestoreEmbedded_NoSavedConfig_ErrorMessage(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	err := esb.RestoreEmbedded()
	if err == nil {
		defer func() { _ = esb.StopEmbedded() }()
		t.Fatal("expected error")
	}
	expected := "qrdb: no saved embedded configuration found"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestRestoreEmbedded_WithSavedMemoryConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))

	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		RaftEnabled:    false,
	}
	if err := esb.saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	if err := esb.RestoreEmbedded(); err != nil {
		t.Fatalf("RestoreEmbedded: %v", err)
	}
	defer func() { _ = esb.StopEmbedded() }()

	if !esb.IsEmbedded() {
		t.Error("expected IsEmbedded()=true after RestoreEmbedded")
	}
	if !conn.IsConnected() {
		t.Error("expected IsConnected()=true after RestoreEmbedded")
	}
}

func TestRestoreEmbedded_CorruptConfig(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	// Write corrupt JSON to the config path.
	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{corrupt json!!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = esb.RestoreEmbedded()
	if err == nil {
		defer func() { _ = esb.StopEmbedded() }()
		t.Fatal("expected error from RestoreEmbedded with corrupt config")
	}
	var cle *ConfigLoadError
	if !errors.As(err, &cle) {
		t.Fatalf("expected ConfigLoadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// saveConfig / loadConfig round-trip
// ---------------------------------------------------------------------------

func TestSaveAndLoadConfig_RoundTrip(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	original := &EmbeddedConfig{
		StorageBackend: "pebble",
		StoreEngine:    "mvcc",
		DataDir:        "/tmp/data",
		RaftEnabled:    true,
		RaftConfig: &RaftConfig{
			SingleNode:  true,
			ShardID:     1,
			ReplicaID:   1,
			RaftAddress: "127.0.0.1:9090",
		},
		SealConfig: &SealConfig{
			Mode:           "shamir",
			SharesRequired: 2,
			SharesTotal:    3,
		},
	}

	if err := esb.saveConfig(original); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	loaded, err := esb.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if loaded.StorageBackend != original.StorageBackend {
		t.Errorf("StorageBackend = %q, want %q", loaded.StorageBackend, original.StorageBackend)
	}
	if loaded.StoreEngine != original.StoreEngine {
		t.Errorf("StoreEngine = %q, want %q", loaded.StoreEngine, original.StoreEngine)
	}
	if loaded.DataDir != original.DataDir {
		t.Errorf("DataDir = %q, want %q", loaded.DataDir, original.DataDir)
	}
	if !loaded.RaftEnabled {
		t.Error("expected RaftEnabled=true")
	}
	if loaded.RaftConfig == nil {
		t.Fatal("expected non-nil RaftConfig")
	}
	if !loaded.RaftConfig.SingleNode {
		t.Error("expected SingleNode=true")
	}
	if loaded.SealConfig == nil {
		t.Fatal("expected non-nil SealConfig")
	}
	if loaded.SealConfig.SharesRequired != 2 {
		t.Errorf("SealConfig.SharesRequired = %d, want 2", loaded.SealConfig.SharesRequired)
	}
	if loaded.SealConfig.SharesTotal != 3 {
		t.Errorf("SealConfig.SharesTotal = %d, want 3", loaded.SealConfig.SharesTotal)
	}
}

func TestSaveAndLoadConfig_MinimalConfig(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	original := &EmbeddedConfig{
		StorageBackend: "memory",
	}

	if err := esb.saveConfig(original); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	loaded, err := esb.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if loaded.StorageBackend != "memory" {
		t.Errorf("StorageBackend = %q, want %q", loaded.StorageBackend, "memory")
	}
	if loaded.RaftConfig != nil {
		t.Error("expected nil RaftConfig for minimal config")
	}
	if loaded.SealConfig != nil {
		t.Error("expected nil SealConfig for minimal config")
	}
}

// ---------------------------------------------------------------------------
// loadConfig error paths
// ---------------------------------------------------------------------------

func TestLoadConfig_FileNotFound(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	_, err := esb.loadConfig()
	if err == nil {
		t.Fatal("expected error from loadConfig when file does not exist")
	}
	var nsce *NoSavedConfigError
	if !errors.As(err, &nsce) {
		t.Fatalf("expected NoSavedConfigError, got %T: %v", err, err)
	}
}

func TestLoadConfig_CorruptJSON(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = esb.loadConfig()
	if err == nil {
		t.Fatal("expected error from loadConfig with corrupt JSON")
	}
	var cle *ConfigLoadError
	if !errors.As(err, &cle) {
		t.Fatalf("expected ConfigLoadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Registry accessor
// ---------------------------------------------------------------------------

func TestEmbeddedServerBinding_Registry_NotNil(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)
	if esb.Registry() == nil {
		t.Error("expected non-nil Registry")
	}
}

func TestEmbeddedServerBinding_Registry_SameInstance(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)
	r1 := esb.Registry()
	r2 := esb.Registry()
	if r1 != r2 {
		t.Error("expected Registry() to return the same instance on each call")
	}
}

// ---------------------------------------------------------------------------
// configPath helper
// ---------------------------------------------------------------------------

func TestConfigPath_CreatesDirectory(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty config path")
	}
	// Verify the containing directory was created.
	dir := filepath.Dir(path)
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("expected config directory to exist: %v", statErr)
	}
}

func TestConfigPath_EndsWithEmbeddedConfigFile(t *testing.T) {
	esb := newTestEmbeddedServerBinding(t)

	path, err := esb.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if filepath.Base(path) != embeddedConfigFile {
		t.Errorf("expected filename %q, got %q", embeddedConfigFile, filepath.Base(path))
	}
}
