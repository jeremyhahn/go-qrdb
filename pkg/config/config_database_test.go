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

package config

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// DefaultConfig - Databases defaults
// ---------------------------------------------------------------------------

func TestConfig_DefaultConfig_HasDefaultDatabase(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.Databases) != 1 {
		t.Fatalf("Databases: got %d entries, want 1", len(cfg.Databases))
	}

	db := cfg.Databases[0]
	if db.Name != "system" {
		t.Errorf("Databases[0].Name: got %q, want %q", db.Name, "system")
	}
	if db.Engine != "kv" {
		t.Errorf("Databases[0].Engine: got %q, want %q", db.Engine, "kv")
	}
	if db.Backend != "pebble" {
		t.Errorf("Databases[0].Backend: got %q, want %q", db.Backend, "pebble")
	}
}

// ---------------------------------------------------------------------------
// Validate - valid database configs
// ---------------------------------------------------------------------------

func TestConfig_Validate_DatabaseConfig_Valid(t *testing.T) {
	tests := []struct {
		name      string
		databases []DatabaseConfig
	}{
		{
			name: "single_kv_pebble",
			databases: []DatabaseConfig{
				{Name: "default", Engine: "kv", Backend: "pebble"},
			},
		},
		{
			name: "single_timeseries_memory",
			databases: []DatabaseConfig{
				{Name: "metrics", Engine: "timeseries", Backend: "memory"},
			},
		},
		{
			name: "single_vector_raft",
			databases: []DatabaseConfig{
				{Name: "vectors", Engine: "vector", Backend: "raft"},
			},
		},
		{
			name: "single_kv_file",
			databases: []DatabaseConfig{
				{Name: "archive", Engine: "kv", Backend: "file"},
			},
		},
		{
			name: "multiple_databases",
			databases: []DatabaseConfig{
				{Name: "default", Engine: "kv", Backend: "pebble"},
				{Name: "metrics", Engine: "timeseries", Backend: "memory"},
				{Name: "embeddings", Engine: "vector", Backend: "raft"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Databases = tc.databases
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate: expected valid, got %v", err)
			}
		})
	}
}

func TestConfig_Validate_DatabaseConfig_EmptySlice(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty databases slice should be valid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validate - invalid database configs
// ---------------------------------------------------------------------------

func TestConfig_Validate_DatabaseConfig_EmptyName(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "", Engine: "kv", Backend: "pebble"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for empty database name, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if dce.Name != "" {
		t.Errorf("DatabaseConfigError.Name: got %q, want empty", dce.Name)
	}
	if !strings.Contains(dce.Message, "name must not be empty") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "name must not be empty")
	}
}

func TestConfig_Validate_DatabaseConfig_InvalidEngine(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "mydb", Engine: "graph", Backend: "pebble"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for invalid engine, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if dce.Name != "mydb" {
		t.Errorf("DatabaseConfigError.Name: got %q, want %q", dce.Name, "mydb")
	}
	if !strings.Contains(dce.Message, "engine must be one of") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "engine must be one of")
	}
}

func TestConfig_Validate_DatabaseConfig_InvalidBackend(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "mydb", Engine: "kv", Backend: "sqlite"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for invalid backend, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if dce.Name != "mydb" {
		t.Errorf("DatabaseConfigError.Name: got %q, want %q", dce.Name, "mydb")
	}
	if !strings.Contains(dce.Message, "backend must be one of") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "backend must be one of")
	}
}

func TestConfig_Validate_DatabaseConfig_DuplicateName(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "mydb", Engine: "kv", Backend: "pebble"},
		{Name: "mydb", Engine: "timeseries", Backend: "memory"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for duplicate database name, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if dce.Name != "mydb" {
		t.Errorf("DatabaseConfigError.Name: got %q, want %q", dce.Name, "mydb")
	}
	if !strings.Contains(dce.Message, "duplicate database name") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "duplicate database name")
	}
}

// ---------------------------------------------------------------------------
// Validate - all valid engines
// ---------------------------------------------------------------------------

func TestConfig_Validate_DatabaseConfig_AllValidEngines(t *testing.T) {
	for _, engine := range []string{"kv", "timeseries", "vector"} {
		t.Run(engine, func(t *testing.T) {
			cfg := validConfig()
			cfg.Databases = []DatabaseConfig{
				{Name: "testdb", Engine: engine, Backend: "pebble"},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("engine %q should be valid, got %v", engine, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validate - all valid backends
// ---------------------------------------------------------------------------

func TestConfig_Validate_DatabaseConfig_AllValidBackends(t *testing.T) {
	for _, backend := range []string{"memory", "file", "pebble", "raft"} {
		t.Run(backend, func(t *testing.T) {
			cfg := validConfig()
			cfg.Databases = []DatabaseConfig{
				{Name: "testdb", Engine: "kv", Backend: backend},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("backend %q should be valid, got %v", backend, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Backward compatibility - empty databases synthesizes default
// ---------------------------------------------------------------------------

func TestConfig_BackwardCompat_NoDatabases(t *testing.T) {
	// When the YAML does not include a "databases" key at all, the default
	// from DefaultConfig() is preserved (backend = "pebble"). This test
	// verifies old configs without a databases section still work.
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-compat"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: "memory"
tls:
  enabled: false
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if len(cfg.Databases) != 1 {
		t.Fatalf("Databases: got %d entries, want 1", len(cfg.Databases))
	}

	db := cfg.Databases[0]
	if db.Name != "system" {
		t.Errorf("Databases[0].Name: got %q, want %q", db.Name, "system")
	}
	if db.Engine != "kv" {
		t.Errorf("Databases[0].Engine: got %q, want %q", db.Engine, "kv")
	}
	// DefaultConfig provides backend="pebble"; absent YAML key preserves this.
	if db.Backend != "pebble" {
		t.Errorf("Databases[0].Backend: got %q, want %q (from DefaultConfig)", db.Backend, "pebble")
	}
}

func TestConfig_BackwardCompat_ExplicitEmptyDatabases(t *testing.T) {
	// When the YAML explicitly sets "databases: []", the backward compat
	// code in Load() synthesizes a default database using Storage.Backend.
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-compat-empty"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: "memory"
tls:
  enabled: false
databases: []
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if len(cfg.Databases) != 1 {
		t.Fatalf("Databases: got %d entries, want 1", len(cfg.Databases))
	}

	db := cfg.Databases[0]
	if db.Name != "system" {
		t.Errorf("Databases[0].Name: got %q, want %q", db.Name, "system")
	}
	if db.Engine != "kv" {
		t.Errorf("Databases[0].Engine: got %q, want %q", db.Engine, "kv")
	}
	// Backend is inherited from storage.backend.
	if db.Backend != "memory" {
		t.Errorf("Databases[0].Backend: got %q, want %q (from storage.backend)", db.Backend, "memory")
	}
}

func TestConfig_BackwardCompat_NoDatabases_DefaultBackendPebble(t *testing.T) {
	// When storage.backend is empty, the fallback is "pebble".
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-compat2"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: ""
tls:
  enabled: false
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if len(cfg.Databases) != 1 {
		t.Fatalf("Databases: got %d entries, want 1", len(cfg.Databases))
	}

	if cfg.Databases[0].Backend != "pebble" {
		t.Errorf("Databases[0].Backend: got %q, want %q (fallback)", cfg.Databases[0].Backend, "pebble")
	}
}

func TestConfig_BackwardCompat_ExplicitDatabases_Preserved(t *testing.T) {
	// When databases are explicitly set in YAML, they should not be overridden.
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-explicit"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: "memory"
tls:
  enabled: false
databases:
  - name: "primary"
    engine: "kv"
    backend: "raft"
  - name: "metrics"
    engine: "timeseries"
    backend: "memory"
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if len(cfg.Databases) != 2 {
		t.Fatalf("Databases: got %d entries, want 2", len(cfg.Databases))
	}

	if cfg.Databases[0].Name != "primary" {
		t.Errorf("Databases[0].Name: got %q, want %q", cfg.Databases[0].Name, "primary")
	}
	if cfg.Databases[0].Engine != "kv" {
		t.Errorf("Databases[0].Engine: got %q, want %q", cfg.Databases[0].Engine, "kv")
	}
	if cfg.Databases[0].Backend != "raft" {
		t.Errorf("Databases[0].Backend: got %q, want %q", cfg.Databases[0].Backend, "raft")
	}

	if cfg.Databases[1].Name != "metrics" {
		t.Errorf("Databases[1].Name: got %q, want %q", cfg.Databases[1].Name, "metrics")
	}
	if cfg.Databases[1].Engine != "timeseries" {
		t.Errorf("Databases[1].Engine: got %q, want %q", cfg.Databases[1].Engine, "timeseries")
	}
	if cfg.Databases[1].Backend != "memory" {
		t.Errorf("Databases[1].Backend: got %q, want %q", cfg.Databases[1].Backend, "memory")
	}
}

// ---------------------------------------------------------------------------
// DatabaseConfigError - error type verification
// ---------------------------------------------------------------------------

func TestDatabaseConfigError_ErrorString(t *testing.T) {
	dce := &DatabaseConfigError{
		Name:    "mydb",
		Message: "engine must be one of: kv, timeseries, vector",
	}
	got := dce.Error()
	if !strings.Contains(got, "mydb") {
		t.Errorf("DatabaseConfigError.Error: missing name in %q", got)
	}
	if !strings.Contains(got, "engine must be one of") {
		t.Errorf("DatabaseConfigError.Error: missing message in %q", got)
	}
	if !strings.Contains(got, "invalid database") {
		t.Errorf("DatabaseConfigError.Error: missing prefix in %q", got)
	}
}

func TestDatabaseConfigError_EmptyNameErrorString(t *testing.T) {
	dce := &DatabaseConfigError{
		Name:    "",
		Message: "name must not be empty",
	}
	got := dce.Error()
	if !strings.Contains(got, "name must not be empty") {
		t.Errorf("DatabaseConfigError.Error: missing message in %q", got)
	}
}

// ---------------------------------------------------------------------------
// Validate - empty engine and empty backend
// ---------------------------------------------------------------------------

func TestConfig_Validate_DatabaseConfig_EmptyEngine(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "mydb", Engine: "", Backend: "pebble"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for empty engine, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if !strings.Contains(dce.Message, "engine must be one of") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "engine must be one of")
	}
}

func TestConfig_Validate_DatabaseConfig_EmptyBackend(t *testing.T) {
	cfg := validConfig()
	cfg.Databases = []DatabaseConfig{
		{Name: "mydb", Engine: "kv", Backend: ""},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for empty backend, got nil")
	}

	var dce *DatabaseConfigError
	if !errors.As(err, &dce) {
		t.Fatalf("Validate: expected *DatabaseConfigError, got %T: %v", err, err)
	}
	if !strings.Contains(dce.Message, "backend must be one of") {
		t.Errorf("DatabaseConfigError.Message: got %q, want substring %q", dce.Message, "backend must be one of")
	}
}
