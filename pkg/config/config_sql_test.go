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

// validSQLConfig returns a Config that passes Validate with MySQL protocol
// enabled. It satisfies all prerequisites: TLS enabled with cert/key paths,
// auth enabled with admin username and default tenant, and SQL defaults set.
func validSQLConfig() *Config {
	cfg := validConfig()
	cfg.Protocols.MySQL = true
	cfg.TLS.Enabled = true
	cfg.TLS.CertFile = "/etc/tls/cert.pem"
	cfg.TLS.KeyFile = "/etc/tls/key.pem"
	cfg.Auth.Enabled = true
	cfg.Auth.AdminUsername = "admin"
	cfg.Auth.DefaultTenant = "default"
	cfg.SQL.DefaultDatabase = "qrdb"
	cfg.SQL.MaxConnections = 100
	cfg.SQL.QueryTimeout = "30s"
	return cfg
}

// ---------------------------------------------------------------------------
// SQL protocol requires TLS
// ---------------------------------------------------------------------------

func TestValidate_MySQL_RequiresTLS(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = true
	cfg.TLS.Enabled = false

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when MySQL enabled without TLS")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "tls.enabled" {
		t.Errorf("Field: got %q, want %q", ve.Field, "tls.enabled")
	}
	if !strings.Contains(ve.Reason, "TLS must be enabled") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "TLS must be enabled")
	}
}

func TestValidate_PgSQL_RequiresTLS(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = false
	cfg.Protocols.PostgreSQL = true
	cfg.TLS.Enabled = false

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when PostgreSQL enabled without TLS")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "tls.enabled" {
		t.Errorf("Field: got %q, want %q", ve.Field, "tls.enabled")
	}
	if !strings.Contains(ve.Reason, "TLS must be enabled") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "TLS must be enabled")
	}
}

// ---------------------------------------------------------------------------
// SQL protocol requires auth
// ---------------------------------------------------------------------------

func TestValidate_MySQL_RequiresAuth(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = true
	cfg.Auth.Enabled = false

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when MySQL enabled without auth")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "auth.enabled" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.enabled")
	}
	if !strings.Contains(ve.Reason, "auth must be enabled") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "auth must be enabled")
	}
}

func TestValidate_PgSQL_RequiresAuth(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = false
	cfg.Protocols.PostgreSQL = true
	cfg.Auth.Enabled = false

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when PostgreSQL enabled without auth")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "auth.enabled" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.enabled")
	}
	if !strings.Contains(ve.Reason, "auth must be enabled") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "auth must be enabled")
	}
}

// ---------------------------------------------------------------------------
// SQL protocol requires default database
// ---------------------------------------------------------------------------

func TestValidate_SQL_EmptyDefaultDatabase(t *testing.T) {
	cfg := validSQLConfig()
	cfg.SQL.DefaultDatabase = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when SQL protocol enabled with empty default_database")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "sql.default_database" {
		t.Errorf("Field: got %q, want %q", ve.Field, "sql.default_database")
	}
	if !strings.Contains(ve.Reason, "must not be empty when SQL protocols are enabled") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "must not be empty when SQL protocols are enabled")
	}
}

// ---------------------------------------------------------------------------
// SQL valid config
// ---------------------------------------------------------------------------

func TestValidate_SQL_ValidConfig(t *testing.T) {
	cfg := validSQLConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for valid SQL config, got %v", err)
	}
}

func TestValidate_SQL_ValidConfig_PostgreSQL(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = false
	cfg.Protocols.PostgreSQL = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for valid PostgreSQL config, got %v", err)
	}
}

func TestValidate_SQL_ValidConfig_BothProtocols(t *testing.T) {
	cfg := validSQLConfig()
	cfg.Protocols.MySQL = true
	cfg.Protocols.PostgreSQL = true

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error when both MySQL and PostgreSQL are enabled with valid config, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQL max connections
// ---------------------------------------------------------------------------

func TestValidate_SQL_NegativeMaxConnections(t *testing.T) {
	cfg := validConfig()
	cfg.SQL.MaxConnections = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative max_connections")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "sql.max_connections" {
		t.Errorf("Field: got %q, want %q", ve.Field, "sql.max_connections")
	}
	if !strings.Contains(ve.Reason, "must not be negative") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "must not be negative")
	}
}

func TestValidate_SQL_ZeroMaxConnections(t *testing.T) {
	cfg := validConfig()
	cfg.SQL.MaxConnections = 0

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for zero max_connections (unlimited), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SQL query timeout
// ---------------------------------------------------------------------------

func TestValidate_SQL_InvalidQueryTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.SQL.QueryTimeout = "invalid"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid query_timeout")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "sql.query_timeout" {
		t.Errorf("Field: got %q, want %q", ve.Field, "sql.query_timeout")
	}
	if !strings.Contains(ve.Reason, "must be a valid Go duration string") {
		t.Errorf("Reason: got %q, want substring %q", ve.Reason, "must be a valid Go duration string")
	}
}

func TestValidate_SQL_ValidQueryTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.SQL.QueryTimeout = "30s"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for valid query_timeout, got %v", err)
	}
}

func TestValidate_SQL_EmptyQueryTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.SQL.QueryTimeout = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for empty query_timeout (skips parsing), got %v", err)
	}
}

func TestValidate_SQL_QueryTimeout_VariousDurations(t *testing.T) {
	durations := []string{"1s", "500ms", "5m", "1h30m", "100us"}
	for _, d := range durations {
		t.Run(d, func(t *testing.T) {
			cfg := validConfig()
			cfg.SQL.QueryTimeout = d
			if err := cfg.Validate(); err != nil {
				t.Errorf("expected no error for query_timeout=%q, got %v", d, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MySQL port validation
// ---------------------------------------------------------------------------

func TestValidate_MySQLPort_Invalid(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{name: "negative", port: -1},
		{name: "too_large", port: 99999},
		{name: "exceeds_max", port: 65536},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.MySQLPort = tc.port

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for MySQLPort=%d", tc.port)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != "server.mysql_port" {
				t.Errorf("Field: got %q, want %q", ve.Field, "server.mysql_port")
			}
			if !strings.Contains(ve.Reason, "must be between 0 and 65535") {
				t.Errorf("Reason: got %q, want substring %q", ve.Reason, "must be between 0 and 65535")
			}
		})
	}
}

func TestValidate_MySQLPort_Valid(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{name: "zero", port: 0},
		{name: "default", port: 3306},
		{name: "max", port: 65535},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.MySQLPort = tc.port

			if err := cfg.Validate(); err != nil {
				t.Fatalf("expected no error for MySQLPort=%d, got %v", tc.port, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PgSQL port validation
// ---------------------------------------------------------------------------

func TestValidate_PgSQLPort_Invalid(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{name: "negative", port: -1},
		{name: "too_large", port: 99999},
		{name: "exceeds_max", port: 65536},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.PgSQLPort = tc.port

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for PgSQLPort=%d", tc.port)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != "server.pgsql_port" {
				t.Errorf("Field: got %q, want %q", ve.Field, "server.pgsql_port")
			}
			if !strings.Contains(ve.Reason, "must be between 0 and 65535") {
				t.Errorf("Reason: got %q, want substring %q", ve.Reason, "must be between 0 and 65535")
			}
		})
	}
}

func TestValidate_PgSQLPort_Valid(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{name: "zero", port: 0},
		{name: "default", port: 5432},
		{name: "max", port: 65535},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.PgSQLPort = tc.port

			if err := cfg.Validate(); err != nil {
				t.Fatalf("expected no error for PgSQLPort=%d, got %v", tc.port, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig SQL defaults
// ---------------------------------------------------------------------------

func TestDefaultConfig_SQLDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.SQL.DefaultDatabase != "qrdb" {
		t.Errorf("SQL.DefaultDatabase: got %q, want %q", cfg.SQL.DefaultDatabase, "qrdb")
	}
	if cfg.SQL.MaxConnections != 100 {
		t.Errorf("SQL.MaxConnections: got %d, want 100", cfg.SQL.MaxConnections)
	}
	if cfg.SQL.QueryTimeout != "30s" {
		t.Errorf("SQL.QueryTimeout: got %q, want %q", cfg.SQL.QueryTimeout, "30s")
	}
}

func TestDefaultConfig_SQLPortDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Server.MySQLPort != 3306 {
		t.Errorf("Server.MySQLPort: got %d, want 3306", cfg.Server.MySQLPort)
	}
	if cfg.Server.PgSQLPort != 5432 {
		t.Errorf("Server.PgSQLPort: got %d, want 5432", cfg.Server.PgSQLPort)
	}
}

func TestDefaultConfig_SQLProtocolsDisabled(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Protocols.MySQL {
		t.Error("Protocols.MySQL: got true, want false (disabled by default)")
	}
	if cfg.Protocols.PostgreSQL {
		t.Error("Protocols.PostgreSQL: got true, want false (disabled by default)")
	}
}
