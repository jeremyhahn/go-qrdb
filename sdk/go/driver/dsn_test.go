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

package driver

import (
	"errors"
	"testing"
)

// TestParseDSN_EmbeddedValid tests valid embedded DSN strings.
func TestParseDSN_EmbeddedValid(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantScheme  string
		wantHost    string
		wantDB      string
		wantBackend string
		wantEngine  string
		wantDataDir string
	}{
		{
			name:        "memory with database param",
			dsn:         "embedded://memory?database=mydb",
			wantScheme:  "embedded",
			wantHost:    "memory",
			wantDB:      "mydb",
			wantBackend: "memory",
			wantEngine:  "kv",
			wantDataDir: "",
		},
		{
			name:        "pebble with path and database",
			dsn:         "embedded:///var/data/qrdb?backend=pebble&database=testdb",
			wantScheme:  "embedded",
			wantHost:    "",
			wantDB:      "testdb",
			wantBackend: "pebble",
			wantEngine:  "kv",
			wantDataDir: "/var/data/qrdb",
		},
		{
			name:        "file with path and database",
			dsn:         "embedded:///tmp/qrdb?backend=file&database=filedb",
			wantScheme:  "embedded",
			wantHost:    "",
			wantDB:      "filedb",
			wantBackend: "file",
			wantEngine:  "kv",
			wantDataDir: "/tmp/qrdb",
		},
		{
			name:        "with explicit engine param",
			dsn:         "embedded://memory?database=tsdb&engine=timeseries",
			wantScheme:  "embedded",
			wantHost:    "memory",
			wantDB:      "tsdb",
			wantBackend: "memory",
			wantEngine:  "timeseries",
			wantDataDir: "",
		},
		{
			name:        "backend param overrides host",
			dsn:         "embedded://memory?database=mydb&backend=pebble",
			wantScheme:  "embedded",
			wantHost:    "memory",
			wantDB:      "mydb",
			wantBackend: "pebble",
			wantEngine:  "kv",
		},
		{
			name:        "no host defaults to memory",
			dsn:         "embedded:///?database=defdb",
			wantScheme:  "embedded",
			wantHost:    "",
			wantDB:      "defdb",
			wantBackend: "memory",
			wantEngine:  "kv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseDSN(tt.dsn)
			if err != nil {
				t.Fatalf("ParseDSN(%q) unexpected error: %v", tt.dsn, err)
			}
			if cfg.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", cfg.Scheme, tt.wantScheme)
			}
			if cfg.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
			}
			if cfg.Database != tt.wantDB {
				t.Errorf("Database = %q, want %q", cfg.Database, tt.wantDB)
			}
			if cfg.Backend != tt.wantBackend {
				t.Errorf("Backend = %q, want %q", cfg.Backend, tt.wantBackend)
			}
			if cfg.Engine != tt.wantEngine {
				t.Errorf("Engine = %q, want %q", cfg.Engine, tt.wantEngine)
			}
			if cfg.DataDir != tt.wantDataDir {
				t.Errorf("DataDir = %q, want %q", cfg.DataDir, tt.wantDataDir)
			}
		})
	}
}

// TestParseDSN_NetworkValid tests valid network DSN strings.
func TestParseDSN_NetworkValid(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		wantScheme string
		wantHost   string
		wantDB     string
		wantCA     string
		wantCert   string
		wantKey    string
	}{
		{
			name:       "rest without tls",
			dsn:        "rest://localhost:63001/mydb",
			wantScheme: "rest",
			wantHost:   "localhost:63001",
			wantDB:     "mydb",
		},
		{
			name:       "grpc without tls",
			dsn:        "grpc://host.example.com:50051/prod",
			wantScheme: "grpc",
			wantHost:   "host.example.com:50051",
			wantDB:     "prod",
		},
		{
			name:       "rest with tls params",
			dsn:        "rest://localhost:63001/mydb?ca=/etc/ca.pem&cert=/etc/client.crt&key=/etc/client.key",
			wantScheme: "rest",
			wantHost:   "localhost:63001",
			wantDB:     "mydb",
			wantCA:     "/etc/ca.pem",
			wantCert:   "/etc/client.crt",
			wantKey:    "/etc/client.key",
		},
		{
			name:       "grpc with ca only",
			dsn:        "grpc://server:9090/testdb?ca=/path/to/ca.pem",
			wantScheme: "grpc",
			wantHost:   "server:9090",
			wantDB:     "testdb",
			wantCA:     "/path/to/ca.pem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseDSN(tt.dsn)
			if err != nil {
				t.Fatalf("ParseDSN(%q) unexpected error: %v", tt.dsn, err)
			}
			if cfg.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", cfg.Scheme, tt.wantScheme)
			}
			if cfg.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
			}
			if cfg.Database != tt.wantDB {
				t.Errorf("Database = %q, want %q", cfg.Database, tt.wantDB)
			}
			if cfg.CAFile != tt.wantCA {
				t.Errorf("CAFile = %q, want %q", cfg.CAFile, tt.wantCA)
			}
			if cfg.CertFile != tt.wantCert {
				t.Errorf("CertFile = %q, want %q", cfg.CertFile, tt.wantCert)
			}
			if cfg.KeyFile != tt.wantKey {
				t.Errorf("KeyFile = %q, want %q", cfg.KeyFile, tt.wantKey)
			}
		})
	}
}

// TestParseDSN_Invalid tests DSN strings that should return errors.
func TestParseDSN_Invalid(t *testing.T) {
	tests := []struct {
		name          string
		dsn           string
		wantErrType   string
		wantParseErr  bool
		wantSchemeErr bool
	}{
		{
			name:         "empty DSN",
			dsn:          "",
			wantParseErr: true,
		},
		{
			name:          "unsupported scheme",
			dsn:           "mysql://localhost/db",
			wantSchemeErr: true,
		},
		{
			name:          "postgres scheme",
			dsn:           "postgres://localhost/db",
			wantSchemeErr: true,
		},
		{
			name:         "embedded missing database",
			dsn:          "embedded://memory",
			wantParseErr: true,
		},
		{
			name:         "rest missing host",
			dsn:          "rest:///mydb",
			wantParseErr: true,
		},
		{
			name:         "rest missing database in path",
			dsn:          "rest://localhost:8080",
			wantParseErr: true,
		},
		{
			name:         "grpc missing host",
			dsn:          "grpc:///mydb",
			wantParseErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseDSN(tt.dsn)
			if err == nil {
				t.Fatalf("ParseDSN(%q) expected error, got cfg: %+v", tt.dsn, cfg)
			}

			if tt.wantParseErr {
				var pe *DSNParseError
				if !errors.As(err, &pe) {
					t.Errorf("expected *DSNParseError, got %T: %v", err, err)
				}
			}

			if tt.wantSchemeErr {
				var se *UnsupportedSchemeError
				if !errors.As(err, &se) {
					t.Errorf("expected *UnsupportedSchemeError, got %T: %v", err, err)
				}
			}
		})
	}
}

// TestParseDSN_Defaults verifies that backend and engine default correctly.
func TestParseDSN_Defaults(t *testing.T) {
	cfg, err := ParseDSN("embedded:///?database=defaults")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Backend != "memory" {
		t.Errorf("default Backend = %q, want %q", cfg.Backend, "memory")
	}
	if cfg.Engine != "kv" {
		t.Errorf("default Engine = %q, want %q", cfg.Engine, "kv")
	}
}

// TestParseDSN_SchemeConstants verifies scheme constants have expected values.
func TestParseDSN_SchemeConstants(t *testing.T) {
	if SchemeEmbedded != "embedded" {
		t.Errorf("SchemeEmbedded = %q, want %q", SchemeEmbedded, "embedded")
	}
	if SchemeREST != "rest" {
		t.Errorf("SchemeREST = %q, want %q", SchemeREST, "rest")
	}
	if SchemeGRPC != "grpc" {
		t.Errorf("SchemeGRPC = %q, want %q", SchemeGRPC, "grpc")
	}
}
