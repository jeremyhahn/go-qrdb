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
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Load - uncovered branches
// ---------------------------------------------------------------------------

// TestLoad_InvalidYAMLUnmarshal verifies that Load returns a LoadError when
// the YAML content cannot be unmarshalled into Config due to a type mismatch.
func TestLoad_InvalidYAMLUnmarshal(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.yaml")
	// admin_port expects int; providing a nested map triggers an unmarshal error.
	if err := os.WriteFile(path, []byte("server:\n  admin_port:\n    key: value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for type-mismatched YAML, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LoadError, got %T: %v", err, err)
	}
	if le.Path != path {
		t.Errorf("LoadError.Path: got %q, want %q", le.Path, path)
	}
}

// TestLoad_ValidationError verifies that Load propagates a validation error
// when the loaded YAML produces a config that fails Validate.
func TestLoad_ValidationError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "invalid.yaml")
	// admin_port = 0 causes a ValidationError.
	content := `
server:
  admin_port: 0
  host: localhost
raft:
  data_dir: /tmp/raft
  shard_id: 1
  replica_id: 1
  raft_address: localhost:63001
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error from Load, got nil")
	}
}

// TestLoad_DefaultDatabaseBackendFallbackToPebble verifies that when
// databases is explicitly set to an empty list and storage.backend is
// empty, the synthesised default database backend falls back to "pebble".
func TestLoad_DefaultDatabaseBackendFallbackToPebble(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cfg.yaml")
	// Explicitly empty databases and empty storage backend to exercise the
	// "backend == ''" fallback inside Load.
	content := `
server:
  admin_port: 8080
  host: localhost
raft:
  data_dir: /tmp/raft
  shard_id: 1
  replica_id: 1
  raft_address: localhost:63001
storage:
  backend: ""
databases: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Databases) == 0 {
		t.Fatal("expected synthesised default database when databases is empty")
	}
	if loaded.Databases[0].Backend != "pebble" {
		t.Errorf("default database backend fallback: got %q, want %q", loaded.Databases[0].Backend, "pebble")
	}
}

// ---------------------------------------------------------------------------
// Validate - Seal section
// ---------------------------------------------------------------------------

// TestValidate_Seal_InvalidStrategy verifies that an unsupported seal
// strategy is rejected.
func TestValidate_Seal_InvalidStrategy(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "hashicorp"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid seal strategy")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "seal.strategy" {
		t.Errorf("Field: got %q, want seal.strategy", ve.Field)
	}
}

// TestValidate_Seal_ShamirThresholdTooLow verifies that a Shamir threshold
// below 2 is rejected.
func TestValidate_Seal_ShamirThresholdTooLow(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 1
	cfg.Seal.ShamirTotal = 5
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for shamir_threshold < 2")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "seal.shamir_threshold" {
		t.Errorf("Field: got %q, want seal.shamir_threshold", ve.Field)
	}
}

// TestValidate_Seal_ShamirTotalBelowThreshold verifies that a shamir_total
// less than shamir_threshold is rejected.
func TestValidate_Seal_ShamirTotalBelowThreshold(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 3
	cfg.Seal.ShamirTotal = 2
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for shamir_total < shamir_threshold")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "seal.shamir_total" {
		t.Errorf("Field: got %q, want seal.shamir_total", ve.Field)
	}
}

// TestValidate_Seal_InvalidCredentialStrategy verifies that an unsupported
// credential strategy is rejected.
func TestValidate_Seal_InvalidCredentialStrategy(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "software"
	cfg.Seal.CredentialStrategy = "vault"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid credential strategy")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "seal.credential_strategy" {
		t.Errorf("Field: got %q, want seal.credential_strategy", ve.Field)
	}
}

// TestValidate_Seal_ValidShamirConfig verifies that a fully valid Shamir
// configuration is accepted.
func TestValidate_Seal_ValidShamirConfig(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 3
	cfg.Seal.ShamirTotal = 5
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid shamir config, got: %v", err)
	}
}

// TestValidate_Seal_Disabled_SkipsValidation verifies that a disabled seal
// does not trigger validation of strategy fields.
func TestValidate_Seal_Disabled_SkipsValidation(t *testing.T) {
	cfg := validConfig()
	cfg.Seal.Enabled = false
	cfg.Seal.Strategy = "hashicorp" // would fail if seal were enabled
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config when seal is disabled, got: %v", err)
	}
}
