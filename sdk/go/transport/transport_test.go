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

package transport

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", cfg.Timeout)
	}
	if cfg.TLSEnabled {
		t.Error("expected TLSEnabled false by default")
	}
	if cfg.Address != "" {
		t.Errorf("expected empty address, got %q", cfg.Address)
	}
}

func TestConfigValidate_ValidAddress(t *testing.T) {
	cfg := &Config{Address: "https://localhost:63002"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestConfigValidate_EmptyAddressWithTLSConfig(t *testing.T) {
	cfg := &Config{
		TLSConfig: &tls.Config{},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error with TLSConfig set, got %v", err)
	}
}

func TestConfigValidate_EmptyAddress(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty address")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "Address" {
		t.Errorf("expected field Address, got %q", cfgErr.Field)
	}
}

func TestConfigValidate_TLSWithoutCA(t *testing.T) {
	cfg := &Config{
		Address:    "https://localhost:63002",
		TLSEnabled: true,
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for TLS without CA file")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "TLSCAFile" {
		t.Errorf("expected field TLSCAFile, got %q", cfgErr.Field)
	}
}

func TestConfigValidate_TLSWithCustomConfig(t *testing.T) {
	cfg := &Config{
		Address:    "https://localhost:63002",
		TLSEnabled: true,
		TLSConfig:  &tls.Config{},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error with custom TLSConfig, got %v", err)
	}
}

func TestConfigValidate_CertWithoutKey(t *testing.T) {
	cfg := &Config{
		Address:     "https://localhost:63002",
		TLSCertFile: "/path/to/cert.pem",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for cert without key")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "TLSKeyFile" {
		t.Errorf("expected field TLSKeyFile, got %q", cfgErr.Field)
	}
}

func TestConfigValidate_KeyWithoutCert(t *testing.T) {
	cfg := &Config{
		Address:    "https://localhost:63002",
		TLSKeyFile: "/path/to/key.pem",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for key without cert")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if cfgErr.Field != "TLSCertFile" {
		t.Errorf("expected field TLSCertFile, got %q", cfgErr.Field)
	}
}

func TestWithAddress(t *testing.T) {
	cfg := ApplyOptions(WithAddress("https://example.com:8080"))
	if cfg.Address != "https://example.com:8080" {
		t.Errorf("expected address https://example.com:8080, got %q", cfg.Address)
	}
}

func TestWithTLS(t *testing.T) {
	cfg := ApplyOptions(WithTLS("/cert.pem", "/key.pem", "/ca.pem"))
	if !cfg.TLSEnabled {
		t.Error("expected TLSEnabled true")
	}
	if cfg.TLSCertFile != "/cert.pem" {
		t.Errorf("expected cert file /cert.pem, got %q", cfg.TLSCertFile)
	}
	if cfg.TLSKeyFile != "/key.pem" {
		t.Errorf("expected key file /key.pem, got %q", cfg.TLSKeyFile)
	}
	if cfg.TLSCAFile != "/ca.pem" {
		t.Errorf("expected CA file /ca.pem, got %q", cfg.TLSCAFile)
	}
}

func TestWithMTLS(t *testing.T) {
	cfg := ApplyOptions(WithMTLS("/cert.pem", "/key.pem", "/ca.pem"))
	if !cfg.TLSEnabled {
		t.Error("expected TLSEnabled true")
	}
	if cfg.TLSCertFile != "/cert.pem" {
		t.Errorf("expected cert file /cert.pem, got %q", cfg.TLSCertFile)
	}
}

func TestWithTLSConfig(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	cfg := ApplyOptions(WithTLSConfig(tlsCfg))
	if !cfg.TLSEnabled {
		t.Error("expected TLSEnabled true")
	}
	if cfg.TLSConfig != tlsCfg {
		t.Error("expected TLSConfig to be set")
	}
}

func TestWithTimeout(t *testing.T) {
	cfg := ApplyOptions(WithTimeout(5 * time.Second))
	if cfg.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", cfg.Timeout)
	}
}

func TestApplyOptions_DefaultTimeout(t *testing.T) {
	cfg := ApplyOptions()
	if cfg.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", cfg.Timeout)
	}
}

func TestApplyOptions_MultipleOptions(t *testing.T) {
	cfg := ApplyOptions(
		WithAddress("https://localhost:8080"),
		WithTimeout(10*time.Second),
		WithTLS("c.pem", "k.pem", "ca.pem"),
	)
	if cfg.Address != "https://localhost:8080" {
		t.Errorf("unexpected address: %q", cfg.Address)
	}
	if cfg.Timeout != 10*time.Second {
		t.Errorf("unexpected timeout: %v", cfg.Timeout)
	}
	if !cfg.TLSEnabled {
		t.Error("expected TLS enabled")
	}
}

func TestConfigError_Message(t *testing.T) {
	err := &ConfigError{Field: "Address", Reason: "is required"}
	expected := "transport: invalid config field Address: is required"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestConnectionError_Unwrap(t *testing.T) {
	inner := errors.New("dial failed")
	err := &ConnectionError{Address: "localhost:8080", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
	expected := "transport: connection failed to localhost:8080: dial failed"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestRequestError_Message(t *testing.T) {
	err := &RequestError{Method: "Get", StatusCode: 404}
	expected := "transport: request Get failed (status 404)"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestRequestError_WithInnerError(t *testing.T) {
	inner := errors.New("timeout")
	err := &RequestError{Method: "Put", Err: inner}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
	expected := "transport: request Put failed: timeout"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestKeyNotFoundError_Message(t *testing.T) {
	err := &KeyNotFoundError{Key: "mykey"}
	expected := "transport: key not found: mykey"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestNotLeaderError_KnownLeader(t *testing.T) {
	err := &NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "10.0.0.1:63001"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestNotLeaderError_UnknownLeader(t *testing.T) {
	err := &NotLeaderError{ShardID: 1}
	expected := "transport: not leader for shard 1, leader unknown"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestTLSError_Unwrap(t *testing.T) {
	inner := errors.New("handshake failed")
	err := &TLSError{Err: inner}
	if !errors.Is(err, inner) {
		t.Error("expected Unwrap to return inner error")
	}
}

func TestClosedError_Message(t *testing.T) {
	err := &ClosedError{}
	expected := "transport: connection is closed"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestDatabaseNotFoundError_Message(t *testing.T) {
	err := &DatabaseNotFoundError{Name: "analytics"}
	expected := "transport: database not found: analytics"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestDatabaseNotFoundError_EmptyName(t *testing.T) {
	err := &DatabaseNotFoundError{}
	expected := "transport: database not found: "
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestDatabaseExistsError_Message(t *testing.T) {
	err := &DatabaseExistsError{Name: "analytics"}
	expected := "transport: database already exists: analytics"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestDatabaseExistsError_EmptyName(t *testing.T) {
	err := &DatabaseExistsError{}
	expected := "transport: database already exists: "
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}
