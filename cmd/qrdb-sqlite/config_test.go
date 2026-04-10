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

//go:build sqlite_vtable

package main

import (
	"errors"
	"testing"
)

// validClientConfig returns a fully valid client-mode gateway config.
// Tests that need to break exactly one rule can copy-and-mutate this.
func validClientConfig() gatewayConfig {
	return gatewayConfig{
		mode:        modeClient,
		server:      "localhost:63003",
		consistency: consistencyLinearizable,
		writeMode:   writeModeLeader,
		chunkSize:   defaultChunkSize,
	}
}

// validEmbeddedConfig returns a fully valid embedded-mode gateway config.
func validEmbeddedConfig() gatewayConfig {
	return gatewayConfig{
		mode:        modeEmbedded,
		configFile:  "/etc/qrdb/qrdb.yaml",
		consistency: consistencyLinearizable,
		writeMode:   writeModeLeader,
		chunkSize:   defaultChunkSize,
	}
}

// ---------------------------------------------------------------------------
// Happy path – valid configs must not return an error.
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_ValidClientConfig(t *testing.T) {
	cfg := validClientConfig()
	if err := cfg.validate(); err != nil {
		t.Errorf("validate() returned unexpected error: %v", err)
	}
}

func TestGatewayConfig_Validate_ValidEmbeddedConfig(t *testing.T) {
	cfg := validEmbeddedConfig()
	if err := cfg.validate(); err != nil {
		t.Errorf("validate() returned unexpected error: %v", err)
	}
}

func TestGatewayConfig_Validate_AllConsistencyLevels(t *testing.T) {
	levels := []string{
		consistencyLinearizable,
		consistencySerializable,
		consistencyBoundedStaleness,
		consistencyEventual,
	}
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			cfg := validClientConfig()
			cfg.consistency = level
			if err := cfg.validate(); err != nil {
				t.Errorf("validate() with consistency=%q returned error: %v", level, err)
			}
		})
	}
}

func TestGatewayConfig_Validate_AllWriteModes(t *testing.T) {
	modes := []string{writeModeLeader, writeModeLocal}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			cfg := validClientConfig()
			cfg.writeMode = mode
			if err := cfg.validate(); err != nil {
				t.Errorf("validate() with writeMode=%q returned error: %v", mode, err)
			}
		})
	}
}

func TestGatewayConfig_Validate_ExplicitTransportTypes(t *testing.T) {
	transports := []string{transportREST, transportGRPC, transportUnix, transportMCP, transportQUIC}
	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			cfg := validClientConfig()
			cfg.transport = tr
			if err := cfg.validate(); err != nil {
				t.Errorf("validate() with transport=%q returned error: %v", tr, err)
			}
		})
	}
}

func TestGatewayConfig_Validate_EmptyTransport_IsAllowedInClientMode(t *testing.T) {
	// An empty transport field in client mode is valid — transport is auto-detected.
	cfg := validClientConfig()
	cfg.transport = ""
	if err := cfg.validate(); err != nil {
		t.Errorf("validate() with empty transport returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Invalid mode
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_InvalidMode(t *testing.T) {
	cfg := validClientConfig()
	cfg.mode = "standalone"
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for invalid mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "mode" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "mode")
	}
}

func TestGatewayConfig_Validate_EmptyMode(t *testing.T) {
	cfg := validClientConfig()
	cfg.mode = ""
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for empty mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "mode" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "mode")
	}
}

// ---------------------------------------------------------------------------
// Client mode – missing server
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_ClientMode_MissingServer(t *testing.T) {
	cfg := validClientConfig()
	cfg.server = ""
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error when server is empty in client mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "server" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "server")
	}
}

// ---------------------------------------------------------------------------
// Client mode – invalid transport
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_ClientMode_InvalidTransport(t *testing.T) {
	cfg := validClientConfig()
	cfg.transport = "websocket"
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for invalid transport in client mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "transport" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "transport")
	}
}

// ---------------------------------------------------------------------------
// Embedded mode – missing config file
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_EmbeddedMode_MissingConfigFile(t *testing.T) {
	cfg := validEmbeddedConfig()
	cfg.configFile = ""
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error when configFile is empty in embedded mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "config" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "config")
	}
}

// ---------------------------------------------------------------------------
// Invalid consistency
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_InvalidConsistency(t *testing.T) {
	cfg := validClientConfig()
	cfg.consistency = "eventual-plus"
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for invalid consistency level")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "consistency" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "consistency")
	}
}

func TestGatewayConfig_Validate_EmptyConsistency_IsInvalid(t *testing.T) {
	cfg := validClientConfig()
	cfg.consistency = ""
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for empty consistency level")
	}
}

// ---------------------------------------------------------------------------
// Invalid write mode
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_InvalidWriteMode(t *testing.T) {
	cfg := validClientConfig()
	cfg.writeMode = "async"
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for invalid write mode")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "write-mode" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "write-mode")
	}
}

// ---------------------------------------------------------------------------
// Invalid chunk size
// ---------------------------------------------------------------------------

func TestGatewayConfig_Validate_ZeroChunkSize(t *testing.T) {
	cfg := validClientConfig()
	cfg.chunkSize = 0
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for zero chunk-size")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "chunk-size" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "chunk-size")
	}
}

func TestGatewayConfig_Validate_NegativeChunkSize(t *testing.T) {
	cfg := validClientConfig()
	cfg.chunkSize = -1
	err := cfg.validate()
	if err == nil {
		t.Fatal("validate() should return error for negative chunk-size")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error should be *ConfigError, got %T", err)
	}
	if cfgErr.Field != "chunk-size" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "chunk-size")
	}
}

// ---------------------------------------------------------------------------
// Constants – ensure expected values are unchanged
// ---------------------------------------------------------------------------

func TestConstants_ModeValues(t *testing.T) {
	if modeEmbedded != "embedded" {
		t.Errorf("modeEmbedded = %q, want %q", modeEmbedded, "embedded")
	}
	if modeClient != "client" {
		t.Errorf("modeClient = %q, want %q", modeClient, "client")
	}
}

func TestConstants_TransportValues(t *testing.T) {
	want := map[string]string{
		"transportREST": "rest",
		"transportGRPC": "grpc",
		"transportUnix": "unix",
		"transportMCP":  "mcp",
		"transportQUIC": "quic",
	}
	got := map[string]string{
		"transportREST": transportREST,
		"transportGRPC": transportGRPC,
		"transportUnix": transportUnix,
		"transportMCP":  transportMCP,
		"transportQUIC": transportQUIC,
	}
	for name, wantVal := range want {
		if gotVal := got[name]; gotVal != wantVal {
			t.Errorf("%s = %q, want %q", name, gotVal, wantVal)
		}
	}
}

func TestConstants_DefaultValues(t *testing.T) {
	if defaultMode != modeClient {
		t.Errorf("defaultMode = %q, want %q", defaultMode, modeClient)
	}
	if defaultTransport != transportGRPC {
		t.Errorf("defaultTransport = %q, want %q", defaultTransport, transportGRPC)
	}
	if defaultConsistency != consistencyLinearizable {
		t.Errorf("defaultConsistency = %q, want %q", defaultConsistency, consistencyLinearizable)
	}
	if defaultWriteMode != writeModeLeader {
		t.Errorf("defaultWriteMode = %q, want %q", defaultWriteMode, writeModeLeader)
	}
	if defaultChunkSize != 256 {
		t.Errorf("defaultChunkSize = %d, want 256", defaultChunkSize)
	}
}

// ---------------------------------------------------------------------------
// validModes / validTransports / validConsistencyLevels / validWriteModes maps
// ---------------------------------------------------------------------------

func TestValidModes_ContainsExpectedKeys(t *testing.T) {
	expected := []string{modeEmbedded, modeClient}
	for _, m := range expected {
		if _, ok := validModes[m]; !ok {
			t.Errorf("validModes missing expected key %q", m)
		}
	}
	if len(validModes) != len(expected) {
		t.Errorf("validModes has %d entries, want %d", len(validModes), len(expected))
	}
}

func TestValidTransports_ContainsExpectedKeys(t *testing.T) {
	expected := []string{transportREST, transportGRPC, transportUnix, transportMCP, transportQUIC}
	for _, tr := range expected {
		if _, ok := validTransports[tr]; !ok {
			t.Errorf("validTransports missing expected key %q", tr)
		}
	}
	if len(validTransports) != len(expected) {
		t.Errorf("validTransports has %d entries, want %d", len(validTransports), len(expected))
	}
}

func TestValidConsistencyLevels_ContainsExpectedKeys(t *testing.T) {
	expected := []string{
		consistencyLinearizable,
		consistencySerializable,
		consistencyBoundedStaleness,
		consistencyEventual,
	}
	for _, level := range expected {
		if _, ok := validConsistencyLevels[level]; !ok {
			t.Errorf("validConsistencyLevels missing expected key %q", level)
		}
	}
	if len(validConsistencyLevels) != len(expected) {
		t.Errorf("validConsistencyLevels has %d entries, want %d", len(validConsistencyLevels), len(expected))
	}
}

func TestValidWriteModes_ContainsExpectedKeys(t *testing.T) {
	expected := []string{writeModeLeader, writeModeLocal}
	for _, wm := range expected {
		if _, ok := validWriteModes[wm]; !ok {
			t.Errorf("validWriteModes missing expected key %q", wm)
		}
	}
	if len(validWriteModes) != len(expected) {
		t.Errorf("validWriteModes has %d entries, want %d", len(validWriteModes), len(expected))
	}
}
