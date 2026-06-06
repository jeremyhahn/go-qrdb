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
	cfg.Seal.Strategy = "passphrase"
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

// ---------------------------------------------------------------------------
// Validate - server port validation (quic, mysql, pgsql)
// ---------------------------------------------------------------------------

// TestValidate_QUICPort_OutOfRange verifies that an out-of-range QUIC port
// is rejected.
func TestValidate_QUICPort_OutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"zero", 0, true},
		{"max_valid", 65535, true},
		{"negative", -1, false},
		{"too_high", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.QUICPort = tc.port
			err := cfg.Validate()
			if tc.valid {
				if err != nil {
					t.Errorf("QUICPort=%d: expected valid, got %v", tc.port, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("QUICPort=%d: expected error, got nil", tc.port)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != "server.quic_port" {
				t.Errorf("Field: got %q, want server.quic_port", ve.Field)
			}
		})
	}
}

// TestValidate_MySQLPort_OutOfRange verifies that an out-of-range MySQL port
// is rejected.
func TestValidate_MySQLPort_OutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"zero", 0, true},
		{"max_valid", 65535, true},
		{"negative", -1, false},
		{"too_high", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.MySQLPort = tc.port
			err := cfg.Validate()
			if tc.valid {
				if err != nil {
					t.Errorf("MySQLPort=%d: expected valid, got %v", tc.port, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("MySQLPort=%d: expected error, got nil", tc.port)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != "server.mysql_port" {
				t.Errorf("Field: got %q, want server.mysql_port", ve.Field)
			}
		})
	}
}

// TestValidate_PgSQLPort_OutOfRange verifies that an out-of-range PostgreSQL
// port is rejected.
func TestValidate_PgSQLPort_OutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"zero", 0, true},
		{"max_valid", 65535, true},
		{"negative", -1, false},
		{"too_high", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.PgSQLPort = tc.port
			err := cfg.Validate()
			if tc.valid {
				if err != nil {
					t.Errorf("PgSQLPort=%d: expected valid, got %v", tc.port, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("PgSQLPort=%d: expected error, got nil", tc.port)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != "server.pgsql_port" {
				t.Errorf("Field: got %q, want server.pgsql_port", ve.Field)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateVolumePools - all branches
// ---------------------------------------------------------------------------

// TestValidateVolumePools_EmptySlice verifies that an empty pool list is
// accepted without error.
func TestValidateVolumePools_EmptySlice(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for empty volume pools, got %v", err)
	}
}

// TestValidateVolumePools_Valid verifies that a well-formed pool list is
// accepted.
func TestValidateVolumePools_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "alpha", Paths: []string{"/data/a", "/data/b"}, Strategy: "round_robin"},
		{Name: "beta", Paths: []string{"/data/c"}, Strategy: "least_utilization"},
		{Name: "gamma", Paths: []string{"/data/d"}, Strategy: ""},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid pool list, got %v", err)
	}
}

// TestValidateVolumePools_EmptyName verifies that a pool with an empty name
// is rejected.
func TestValidateVolumePools_EmptyName(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "", Paths: []string{"/data/x"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty pool name, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[].name" {
		t.Errorf("Field: got %q, want storage.volume_pools[].name", ve.Field)
	}
}

// TestValidateVolumePools_DuplicateName verifies that two pools with the
// same name are rejected.
func TestValidateVolumePools_DuplicateName(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "shared", Paths: []string{"/data/1"}},
		{Name: "shared", Paths: []string{"/data/2"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate pool name, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[shared]" {
		t.Errorf("Field: got %q, want storage.volume_pools[shared]", ve.Field)
	}
}

// TestValidateVolumePools_NoPaths verifies that a pool with no paths is
// rejected.
func TestValidateVolumePools_NoPaths(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "empty", Paths: nil},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for pool with no paths, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[empty].paths" {
		t.Errorf("Field: got %q, want storage.volume_pools[empty].paths", ve.Field)
	}
}

// TestValidateVolumePools_EmptyPath verifies that a pool containing an empty
// path string is rejected. The error field encodes the path index using
// the local itoa helper.
func TestValidateVolumePools_EmptyPath(t *testing.T) {
	cfg := validConfig()
	// Put an empty path at index 0 to exercise itoa(0).
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "bad", Paths: []string{""}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty path at index 0, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[bad].paths[0]" {
		t.Errorf("Field: got %q, want storage.volume_pools[bad].paths[0]", ve.Field)
	}
}

// TestValidateVolumePools_EmptyPathAtNonZeroIndex exercises itoa with a
// non-zero index to cover the multi-digit and single-digit non-zero path.
func TestValidateVolumePools_EmptyPathAtNonZeroIndex(t *testing.T) {
	cfg := validConfig()
	// Path index 1 is empty; exercises itoa(1) and the loop path.
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "mixed", Paths: []string{"/data/ok", ""}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty path at index 1, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[mixed].paths[1]" {
		t.Errorf("Field: got %q, want storage.volume_pools[mixed].paths[1]", ve.Field)
	}
}

// TestValidateVolumePools_EmptyPathAtDoubleDigitIndex exercises itoa with an
// index >= 10 to cover the multi-digit loop in itoa.
func TestValidateVolumePools_EmptyPathAtDoubleDigitIndex(t *testing.T) {
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = "/data/ok"
	}
	paths[10] = "" // itoa(10) exercises the multi-digit branch

	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "large", Paths: paths},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty path at index 10, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[large].paths[10]" {
		t.Errorf("Field: got %q, want storage.volume_pools[large].paths[10]", ve.Field)
	}
}

// TestValidateVolumePools_InvalidStrategy verifies that an unsupported
// strategy value is rejected.
func TestValidateVolumePools_InvalidStrategy(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "pool1", Paths: []string{"/data/x"}, Strategy: "random"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid pool strategy, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[pool1].strategy" {
		t.Errorf("Field: got %q, want storage.volume_pools[pool1].strategy", ve.Field)
	}
}

// TestItoa_NegativeValue exercises the negative-number branch in the local
// itoa helper by triggering an empty path at a path index that would only
// arise from a negative index — which is impossible via the range loop. We
// instead call validateVolumePools directly via the Validate path; negative
// indices cannot occur in a range loop. To cover the negative branch we call
// itoa directly since it is a package-level function.
func TestItoa_Values(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{99, "99"},
		{100, "100"},
		{-1, "-1"},
		{-42, "-42"},
	}
	for _, tc := range tests {
		got := itoa(tc.n)
		if got != tc.want {
			t.Errorf("itoa(%d): got %q, want %q", tc.n, got, tc.want)
		}
	}
}
