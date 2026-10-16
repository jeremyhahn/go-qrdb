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
	"crypto/tls"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// validYAML is a well-formed configuration that passes all validation rules.
const validYAML = `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-test"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  sm_type: "memory"
storage:
  backend: "memory"
tls:
  enabled: false
logging:
  level: "info"
  format: "json"
discovery:
  method: "static"
`

// writeTemp writes content to a temporary YAML file and returns its path.
// The caller must not remove the file; t.TempDir handles cleanup.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return p
}

// validConfig returns a Config that passes Validate. Tests that exercise
// a single invalid field start from this baseline and mutate one field.
func validConfig() *Config {
	return &Config{
		Server: ServerConfig{
			AdminPort: 8080,
			GRPCPort:  9090,
			Host:      "127.0.0.1",
		},
		Protocols: ProtocolsConfig{
			REST: true,
			GRPC: true,
		},
		// Auth is enabled by default (zero value = enabled, the fail-safe);
		// a valid config therefore carries the admin identity fields.
		Auth: AuthConfig{
			AdminUsername: "admin",
			DefaultTenant: "system",
			CertTTL:       "8760h",
			PermCacheTTL:  30,
		},
		// Auth enabled (above) now requires seal enabled (go-qrdb#548);
		// "none" keeps this fixture's behavior identical to before that
		// rule existed -- no encryption, just the state transition
		// qrdb init needs.
		Seal: SealConfig{
			Enabled:  true,
			Strategy: "none",
		},
		Raft: RaftConfig{
			DataDir:     "/tmp/data",
			ShardID:     1,
			ReplicaID:   1,
			RaftAddress: "127.0.0.1:63001",
			SMType:      "pebble",
		},
		Storage: StorageConfig{
			Backend: "pebble",
		},
		TLS: TLSConfig{
			Enabled: false,
		},
		Unix: UnixConfig{
			SocketPath: "/var/run/qrdbd/qrdbd.sock",
		},
		MCP: MCPConfig{
			Port:     63005,
			Endpoint: "/mcp",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
		},
		Discovery: DiscoveryConfig{
			Method: "static",
		},
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig_ReturnsNonNil(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
}

func TestDefaultConfig_ServerDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Server.AdminPort != 63002 {
		t.Errorf("AdminPort: got %d, want 63002", cfg.Server.AdminPort)
	}
	if cfg.Server.GRPCPort != 63003 {
		t.Errorf("GRPCPort: got %d, want 63003", cfg.Server.GRPCPort)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Host: got %q, want %q", cfg.Server.Host, "0.0.0.0")
	}
}

func TestDefaultConfig_ProtocolDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Protocols.REST {
		t.Error("Protocols.REST: got false, want true")
	}
	if !cfg.Protocols.GRPC {
		t.Error("Protocols.GRPC: got false, want true")
	}
	if cfg.Protocols.Unix {
		t.Error("Protocols.Unix: got true, want false")
	}
	if cfg.Protocols.MCP {
		t.Error("Protocols.MCP: got true, want false")
	}
}

func TestDefaultConfig_RaftDefaults(t *testing.T) {
	cfg := DefaultConfig()
	r := cfg.Raft
	if r.DataDir != "./data" {
		t.Errorf("DataDir: got %q, want %q", r.DataDir, "./data")
	}
	if r.DeploymentID != 1 {
		t.Errorf("DeploymentID: got %d, want 1", r.DeploymentID)
	}
	if r.ShardID != 1 {
		t.Errorf("ShardID: got %d, want 1", r.ShardID)
	}
	if r.ReplicaID != 1 {
		t.Errorf("ReplicaID: got %d, want 1", r.ReplicaID)
	}
	if r.RaftAddress != "127.0.0.1:63001" {
		t.Errorf("RaftAddress: got %q, want %q", r.RaftAddress, "127.0.0.1:63001")
	}
	if r.Peers != "1=127.0.0.1:63001" {
		t.Errorf("Peers: got %q, want %q", r.Peers, "1=127.0.0.1:63001")
	}
	if r.SMType != "pebble" {
		t.Errorf("SMType: got %q, want %q", r.SMType, "pebble")
	}
	if r.Join {
		t.Error("Join: got true, want false")
	}
	if r.SnapshotEntries != 0 {
		t.Errorf("SnapshotEntries: got %d, want 0", r.SnapshotEntries)
	}
	if r.CompactionOverhead != 0 {
		t.Errorf("CompactionOverhead: got %d, want 0", r.CompactionOverhead)
	}
	if r.MaxInMemLogSize != 0 {
		t.Errorf("MaxInMemLogSize: got %d, want 0", r.MaxInMemLogSize)
	}
	if r.MaxTotalInMemLogSize != 0 {
		t.Errorf("MaxTotalInMemLogSize: got %d, want 0", r.MaxTotalInMemLogSize)
	}
}

func TestDefaultConfig_StorageDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Storage.Backend != "pebble" {
		t.Errorf("Backend: got %q, want %q", cfg.Storage.Backend, "pebble")
	}
	if cfg.Storage.CacheSize != 0 {
		t.Errorf("CacheSize: got %d, want 0", cfg.Storage.CacheSize)
	}
	if cfg.Storage.WALDir != "" {
		t.Errorf("WALDir: got %q, want empty", cfg.Storage.WALDir)
	}
	if cfg.Storage.MaxOpenFiles != 0 {
		t.Errorf("MaxOpenFiles: got %d, want 0", cfg.Storage.MaxOpenFiles)
	}
}

func TestDefaultConfig_TenantDefaults(t *testing.T) {
	cfg := DefaultConfig()
	dq := cfg.Tenant.DefaultQuotas
	if dq.MaxDatabases != 5 {
		t.Errorf("DefaultQuotas.MaxDatabases: got %d, want 5", dq.MaxDatabases)
	}
	if dq.MaxRaftGroups != 0 {
		t.Errorf("DefaultQuotas.MaxRaftGroups: got %d, want 0", dq.MaxRaftGroups)
	}
	if dq.MaxStorageBytes != 1<<30 {
		t.Errorf("DefaultQuotas.MaxStorageBytes: got %d, want %d", dq.MaxStorageBytes, 1<<30)
	}
	if dq.MaxReplicasPerGroup != 3 {
		t.Errorf("DefaultQuotas.MaxReplicasPerGroup: got %d, want 3", dq.MaxReplicasPerGroup)
	}
	if dq.MaxTotalReplicas != 3 {
		t.Errorf("DefaultQuotas.MaxTotalReplicas: got %d, want 3", dq.MaxTotalReplicas)
	}
	// ToQuotas must carry the replica caps through to quota.Quotas.
	qq := dq.ToQuotas()
	if qq.MaxReplicasPerGroup != 3 || qq.MaxTotalReplicas != 3 {
		t.Errorf("ToQuotas replica caps: got per-group=%d total=%d, want 3,3",
			qq.MaxReplicasPerGroup, qq.MaxTotalReplicas)
	}

	de := cfg.Tenant.DefaultEntitlements
	if len(de.AllowedIsolationModes) != 1 || de.AllowedIsolationModes[0] != "prefix" {
		t.Errorf("DefaultEntitlements.AllowedIsolationModes: got %v, want [prefix]", de.AllowedIsolationModes)
	}
	if de.AllowDedicatedRaft {
		t.Error("DefaultEntitlements.AllowDedicatedRaft: got true, want false")
	}
	if de.AllowVolumeStorage {
		t.Error("DefaultEntitlements.AllowVolumeStorage: got true, want false")
	}
	if de.AllowReplicatedAudit {
		t.Error("DefaultEntitlements.AllowReplicatedAudit: got true, want false")
	}

	// Conversion helpers produce the corresponding quota types.
	q := dq.ToQuotas()
	if q.MaxDatabases != 5 || q.MaxStorageBytes != 1<<30 {
		t.Errorf("ToQuotas() = %+v", q)
	}
	ents := de.ToEntitlements()
	if !ents.AllowsIsolation("prefix") || ents.AllowDedicatedRaft || ents.AllowVolumeStorage || ents.AllowReplicatedAudit {
		t.Errorf("ToEntitlements() = %+v", ents)
	}
}

func TestDefaultConfig_TLSDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled: got false, want true")
	}
	// ClientAuth defaults to empty ("derive from auth posture"): a non-empty
	// default is indistinguishable from an explicit operator selection and
	// would force RequireAndVerifyClientCert on every listener, making
	// cert-exempt endpoints like /health unreachable without a client cert.
	if cfg.TLS.ClientAuth != "" {
		t.Errorf("TLS.ClientAuth: got %q, want empty (auth-derived)", cfg.TLS.ClientAuth)
	}
	if cfg.TLS.MinVersion != "TLS1.3" {
		t.Errorf("TLS.MinVersion: got %q, want %q", cfg.TLS.MinVersion, "TLS1.3")
	}
	if cfg.TLS.CAFile != "" {
		t.Errorf("TLS.CAFile: got %q, want empty", cfg.TLS.CAFile)
	}
	if cfg.TLS.CertFile != "" {
		t.Errorf("TLS.CertFile: got %q, want empty", cfg.TLS.CertFile)
	}
	if cfg.TLS.KeyFile != "" {
		t.Errorf("TLS.KeyFile: got %q, want empty", cfg.TLS.KeyFile)
	}
}

func TestDefaultConfig_UnixDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Unix.SocketPath != "/var/run/qrdbd/qrdbd.sock" {
		t.Errorf("Unix.SocketPath: got %q, want %q", cfg.Unix.SocketPath, "/var/run/qrdbd/qrdbd.sock")
	}
}

func TestDefaultConfig_MCPDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MCP.Port != 63005 {
		t.Errorf("MCP.Port: got %d, want 63005", cfg.MCP.Port)
	}
	if cfg.MCP.Endpoint != "/mcp" {
		t.Errorf("MCP.Endpoint: got %q, want %q", cfg.MCP.Endpoint, "/mcp")
	}
}

func TestDefaultConfig_LoggingDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Logging.Level != "info" {
		t.Errorf("Level: got %q, want %q", cfg.Logging.Level, "info")
	}
	if cfg.Logging.Format != "text" {
		t.Errorf("Format: got %q, want %q", cfg.Logging.Format, "text")
	}
}

func TestDefaultConfig_DiscoveryDefaults(t *testing.T) {
	cfg := DefaultConfig()
	// Secure defaults: single-node static discovery, and no well-known
	// multicast PSK (multicast is an explicit, PSK-required opt-in).
	if cfg.Discovery.Method != "static" {
		t.Errorf("Method: got %q, want %q", cfg.Discovery.Method, "static")
	}
	if cfg.Discovery.MulticastSecret != "" {
		t.Errorf("MulticastSecret: got %q, want empty (no well-known default)", cfg.Discovery.MulticastSecret)
	}
	if cfg.Discovery.DNSDomain != "" {
		t.Errorf("DNSDomain: got %q, want empty", cfg.Discovery.DNSDomain)
	}
}

func TestDefaultConfig_WatchDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Watch.BufferSize != 1024 {
		t.Errorf("BufferSize: got %d, want 1024", cfg.Watch.BufferSize)
	}
	if cfg.Watch.MaxWatchers != 0 {
		t.Errorf("MaxWatchers: got %d, want 0", cfg.Watch.MaxWatchers)
	}
}

func TestDefaultConfig_LeaseDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Lease.MinTTL != 5 {
		t.Errorf("MinTTL: got %d, want 5", cfg.Lease.MinTTL)
	}
	if cfg.Lease.MaxTTL != 0 {
		t.Errorf("MaxTTL: got %d, want 0", cfg.Lease.MaxTTL)
	}
	if cfg.Lease.CheckInterval != 1 {
		t.Errorf("CheckInterval: got %d, want 1", cfg.Lease.CheckInterval)
	}
}

func TestDefaultConfig_IndexDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Index.MaxIndexes != 0 {
		t.Errorf("MaxIndexes: got %d, want 0", cfg.Index.MaxIndexes)
	}
}

func TestDefaultConfig_TimeSeriesDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TimeSeries.DefaultRetention != 0 {
		t.Errorf("DefaultRetention: got %d, want 0", cfg.TimeSeries.DefaultRetention)
	}
	if cfg.TimeSeries.MaxMetrics != 0 {
		t.Errorf("MaxMetrics: got %d, want 0", cfg.TimeSeries.MaxMetrics)
	}
}

func TestDefaultConfig_PassesValidation(t *testing.T) {
	// DefaultConfig has TLS enabled but no cert/key paths. Since the default
	// requires cert_file and key_file when enabled, we must supply them
	// or disable TLS to pass validation. This tests that a user who
	// provides certs can pass validation.
	cfg := DefaultConfig()
	cfg.TLS.CertFile = "/etc/tls/cert.pem"
	cfg.TLS.KeyFile = "/etc/tls/key.pem"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig with certs failed validation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Load - success
// ---------------------------------------------------------------------------

func TestLoad_ValidYAML(t *testing.T) {
	path := writeTemp(t, validYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Server.AdminPort != 8080 {
		t.Errorf("AdminPort: got %d, want 8080", cfg.Server.AdminPort)
	}
	if cfg.Server.GRPCPort != 9090 {
		t.Errorf("GRPCPort: got %d, want 9090", cfg.Server.GRPCPort)
	}
	if cfg.Raft.SMType != "memory" {
		t.Errorf("SMType: got %q, want %q", cfg.Raft.SMType, "memory")
	}
	if cfg.Storage.Backend != "memory" {
		t.Errorf("Backend: got %q, want %q", cfg.Storage.Backend, "memory")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Format: got %q, want %q", cfg.Logging.Format, "json")
	}
	if cfg.Discovery.Method != "static" {
		t.Errorf("Method: got %q, want %q", cfg.Discovery.Method, "static")
	}
}

func TestLoad_TenantReplicaQuotaDefaults(t *testing.T) {
	yaml := `
tenant:
  default_quotas:
    max_databases: 7
    max_replicas_per_group: 5
    max_total_replicas: 12
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	dq := cfg.Tenant.DefaultQuotas
	if dq.MaxReplicasPerGroup != 5 {
		t.Errorf("MaxReplicasPerGroup: got %d, want 5", dq.MaxReplicasPerGroup)
	}
	if dq.MaxTotalReplicas != 12 {
		t.Errorf("MaxTotalReplicas: got %d, want 12", dq.MaxTotalReplicas)
	}
}

func TestLoad_PartialYAML_DefaultsFillGaps(t *testing.T) {
	// Provide only server block; everything else should inherit defaults.
	partial := `
server:
  admin_port: 9000
  grpc_port: 9001
  host: "10.0.0.1"
raft:
  data_dir: "/tmp/partial"
  shard_id: 5
  replica_id: 3
  raft_address: "10.0.0.1:63001"
tls:
  enabled: false
`
	path := writeTemp(t, partial)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	// Overridden values.
	if cfg.Server.AdminPort != 9000 {
		t.Errorf("AdminPort: got %d, want 9000", cfg.Server.AdminPort)
	}
	if cfg.Raft.ShardID != 5 {
		t.Errorf("ShardID: got %d, want 5", cfg.Raft.ShardID)
	}
	// Default values preserved.
	if cfg.Logging.Level != "info" {
		t.Errorf("Level: got %q, want default %q", cfg.Logging.Level, "info")
	}
	if cfg.TLS.ClientAuth != "" {
		t.Errorf("ClientAuth: got %q, want default empty (auth-derived)", cfg.TLS.ClientAuth)
	}
	if cfg.TLS.MinVersion != "TLS1.3" {
		t.Errorf("MinVersion: got %q, want default %q", cfg.TLS.MinVersion, "TLS1.3")
	}
}

// ---------------------------------------------------------------------------
// Load - error paths
// ---------------------------------------------------------------------------

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/qrdb.yaml")
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load: expected *LoadError, got %T: %v", err, err)
	}
	if le.Path != "/nonexistent/path/qrdb.yaml" {
		t.Errorf("LoadError.Path: got %q, want %q", le.Path, "/nonexistent/path/qrdb.yaml")
	}
	if !errors.Is(le.Err, os.ErrNotExist) {
		t.Errorf("LoadError.Err: expected os.ErrNotExist, got %v", le.Err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "{{{{not: yaml: at: all")

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected error for invalid YAML, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load: expected *LoadError, got %T: %v", err, err)
	}
	if le.Path != path {
		t.Errorf("LoadError.Path: got %q, want %q", le.Path, path)
	}
}

func TestLoad_ValidationFailure(t *testing.T) {
	// YAML is syntactically valid but shard_id=0 fails validation.
	bad := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/test"
  shard_id: 0
  replica_id: 1
  raft_address: "127.0.0.1:63001"
tls:
  enabled: false
`
	path := writeTemp(t, bad)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load: expected validation error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Load: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "raft.shard_id" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "raft.shard_id")
	}
}

// ---------------------------------------------------------------------------
// Validate - table-driven tests for each validation rule
// ---------------------------------------------------------------------------

func TestValidate_ValidConfig(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestValidate_InvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		field  string
		reason string // substring match
	}{
		// server.admin_port
		{
			name:   "admin_port_zero",
			mutate: func(c *Config) { c.Server.AdminPort = 0 },
			field:  "server.admin_port",
			reason: "between 1 and 65535",
		},
		{
			name:   "admin_port_negative",
			mutate: func(c *Config) { c.Server.AdminPort = -1 },
			field:  "server.admin_port",
			reason: "between 1 and 65535",
		},
		{
			name:   "admin_port_too_high",
			mutate: func(c *Config) { c.Server.AdminPort = 65536 },
			field:  "server.admin_port",
			reason: "between 1 and 65535",
		},
		// server.grpc_port
		{
			name:   "grpc_port_negative",
			mutate: func(c *Config) { c.Server.GRPCPort = -1 },
			field:  "server.grpc_port",
			reason: "between 0 and 65535",
		},
		{
			name:   "grpc_port_too_high",
			mutate: func(c *Config) { c.Server.GRPCPort = 65536 },
			field:  "server.grpc_port",
			reason: "between 0 and 65535",
		},
		// server.host
		{
			name:   "host_empty",
			mutate: func(c *Config) { c.Server.Host = "" },
			field:  "server.host",
			reason: "must not be empty",
		},
		// server.secure_memory_budget_mib
		{
			name:   "secure_memory_budget_mib_negative",
			mutate: func(c *Config) { c.Server.SecureMemoryBudgetMiB = -1 },
			field:  "server.secure_memory_budget_mib",
			reason: "must not be negative",
		},
		{
			name:   "secure_memory_budget_mib_overflow",
			mutate: func(c *Config) { c.Server.SecureMemoryBudgetMiB = math.MaxInt64>>20 + 1 },
			field:  "server.secure_memory_budget_mib",
			reason: "overflow",
		},
		// raft.data_dir
		{
			name:   "data_dir_empty",
			mutate: func(c *Config) { c.Raft.DataDir = "" },
			field:  "raft.data_dir",
			reason: "must not be empty",
		},
		// raft.shard_id
		{
			name:   "shard_id_zero",
			mutate: func(c *Config) { c.Raft.ShardID = 0 },
			field:  "raft.shard_id",
			reason: "greater than 0",
		},
		// raft.replica_id
		{
			name:   "replica_id_zero",
			mutate: func(c *Config) { c.Raft.ReplicaID = 0 },
			field:  "raft.replica_id",
			reason: "greater than 0",
		},
		// raft.raft_address
		{
			name:   "raft_address_empty",
			mutate: func(c *Config) { c.Raft.RaftAddress = "" },
			field:  "raft.raft_address",
			reason: "must not be empty",
		},
		// raft.sm_type
		{
			name:   "sm_type_invalid",
			mutate: func(c *Config) { c.Raft.SMType = "badgerdb" },
			field:  "raft.sm_type",
			reason: "pebble, memory, mvcc",
		},
		// raft.sm_backend
		{
			name:   "sm_backend_invalid",
			mutate: func(c *Config) { c.Raft.SMBackend = "invalid-backend" },
			field:  "raft.sm_backend",
			reason: "standard, concurrent, fast, disk",
		},
		// storage.backend
		{
			name:   "backend_invalid",
			mutate: func(c *Config) { c.Storage.Backend = "sqlite" },
			field:  "storage.backend",
			reason: "pebble, memory",
		},
		// storage.cache_size
		{
			name:   "cache_size_negative",
			mutate: func(c *Config) { c.Storage.CacheSize = -1 },
			field:  "storage.cache_size",
			reason: "must not be negative",
		},
		// storage.max_open_files
		{
			name:   "max_open_files_negative",
			mutate: func(c *Config) { c.Storage.MaxOpenFiles = -1 },
			field:  "storage.max_open_files",
			reason: "must not be negative",
		},
		// tls: invalid client_auth
		{
			name: "tls_invalid_client_auth",
			mutate: func(c *Config) {
				c.TLS.ClientAuth = "mandatory"
			},
			field:  "tls.client_auth",
			reason: "none, request, require, require_and_verify",
		},
		// tls: invalid min_version
		{
			name: "tls_invalid_min_version",
			mutate: func(c *Config) {
				c.TLS.MinVersion = "TLS1.0"
			},
			field:  "tls.min_version",
			reason: "TLS1.2, TLS1.3",
		},
		// unix: enabled with empty socket_path
		{
			name: "unix_enabled_empty_socket_path",
			mutate: func(c *Config) {
				c.Protocols.Unix = true
				c.Unix.SocketPath = ""
			},
			field:  "unix.socket_path",
			reason: "must not be empty when unix protocol is enabled",
		},
		// mcp: port out of range (negative)
		{
			name:   "mcp_port_negative",
			mutate: func(c *Config) { c.MCP.Port = -1 },
			field:  "mcp.port",
			reason: "between 0 and 65535",
		},
		// mcp: port out of range (too high)
		{
			name:   "mcp_port_too_high",
			mutate: func(c *Config) { c.MCP.Port = 65536 },
			field:  "mcp.port",
			reason: "between 0 and 65535",
		},
		// mcp: endpoint without leading slash
		{
			name:   "mcp_endpoint_no_leading_slash",
			mutate: func(c *Config) { c.MCP.Endpoint = "mcp" },
			field:  "mcp.endpoint",
			reason: "must start with /",
		},
		// logging.level
		{
			name:   "log_level_invalid",
			mutate: func(c *Config) { c.Logging.Level = "trace" },
			field:  "logging.level",
			reason: "debug, info, warn, error",
		},
		// logging.format
		{
			name:   "log_format_invalid",
			mutate: func(c *Config) { c.Logging.Format = "xml" },
			field:  "logging.format",
			reason: "text, json",
		},
		// discovery.method
		{
			name:   "discovery_method_invalid",
			mutate: func(c *Config) { c.Discovery.Method = "consul" },
			field:  "discovery.method",
			reason: "static, multicast, dns",
		},
		// watch.buffer_size
		{
			name:   "watch_buffer_size_negative",
			mutate: func(c *Config) { c.Watch.BufferSize = -1 },
			field:  "watch.buffer_size",
			reason: "must not be negative",
		},
		// watch.max_watchers
		{
			name:   "watch_max_watchers_negative",
			mutate: func(c *Config) { c.Watch.MaxWatchers = -1 },
			field:  "watch.max_watchers",
			reason: "must not be negative",
		},
		// lease.min_ttl
		{
			name:   "lease_min_ttl_negative",
			mutate: func(c *Config) { c.Lease.MinTTL = -1 },
			field:  "lease.min_ttl",
			reason: "must not be negative",
		},
		// lease.max_ttl
		{
			name:   "lease_max_ttl_negative",
			mutate: func(c *Config) { c.Lease.MaxTTL = -1 },
			field:  "lease.max_ttl",
			reason: "must not be negative",
		},
		// lease.min_ttl exceeds max_ttl
		{
			name: "lease_min_ttl_exceeds_max_ttl",
			mutate: func(c *Config) {
				c.Lease.MinTTL = 100
				c.Lease.MaxTTL = 10
			},
			field:  "lease.min_ttl",
			reason: "must not exceed lease.max_ttl",
		},
		// lease.check_interval
		{
			name:   "lease_check_interval_negative",
			mutate: func(c *Config) { c.Lease.CheckInterval = -1 },
			field:  "lease.check_interval",
			reason: "must not be negative",
		},
		// index.max_indexes
		{
			name:   "index_max_indexes_negative",
			mutate: func(c *Config) { c.Index.MaxIndexes = -1 },
			field:  "index.max_indexes",
			reason: "must not be negative",
		},
		// timeseries.default_retention
		{
			name:   "timeseries_default_retention_negative",
			mutate: func(c *Config) { c.TimeSeries.DefaultRetention = -1 },
			field:  "timeseries.default_retention",
			reason: "must not be negative",
		},
		// timeseries.max_metrics
		{
			name:   "timeseries_max_metrics_negative",
			mutate: func(c *Config) { c.TimeSeries.MaxMetrics = -1 },
			field:  "timeseries.max_metrics",
			reason: "must not be negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate: expected error for field %q, got nil", tc.field)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != tc.field {
				t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, tc.field)
			}
			if !strings.Contains(ve.Reason, tc.reason) {
				t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, tc.reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Validate - boundary and special-case tests
// ---------------------------------------------------------------------------

func TestValidate_GRPCPortZero_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Server.GRPCPort = 0 // 0 = disabled, should be valid
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: GRPCPort=0 should be valid, got %v", err)
	}
}

func TestValidate_AdminPortBoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"min_valid", 1, true},
		{"max_valid", 65535, true},
		{"below_min", 0, false},
		{"above_max", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.AdminPort = tc.port
			err := cfg.Validate()
			if tc.valid && err != nil {
				t.Errorf("port %d: expected valid, got %v", tc.port, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("port %d: expected error, got nil", tc.port)
			}
		})
	}
}

func TestValidate_GRPCPortBoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"min_valid", 0, true},
		{"max_valid", 65535, true},
		{"below_min", -1, false},
		{"above_max", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Server.GRPCPort = tc.port
			err := cfg.Validate()
			if tc.valid && err != nil {
				t.Errorf("port %d: expected valid, got %v", tc.port, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("port %d: expected error, got nil", tc.port)
			}
		})
	}
}

func TestValidate_EmptySMType_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Raft.SMType = "" // empty is allowed (validation only checks non-empty)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty SMType should be valid, got %v", err)
	}
}

func TestValidate_EmptyBackend_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.Backend = "" // empty is allowed
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty Backend should be valid, got %v", err)
	}
}

func TestValidate_EmptyLogLevel_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Logging.Level = "" // empty is allowed
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty Level should be valid, got %v", err)
	}
}

func TestValidate_EmptyLogFormat_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Logging.Format = "" // empty is allowed
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty Format should be valid, got %v", err)
	}
}

func TestValidate_EmptyDiscoveryMethod_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Discovery.Method = "" // empty is allowed
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty Method should be valid, got %v", err)
	}
}

func TestValidate_TLSEnabled_WithCerts_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.Enabled = true
	cfg.TLS.CertFile = "/etc/tls/cert.pem"
	cfg.TLS.KeyFile = "/etc/tls/key.pem"
	cfg.TLS.ClientAuth = "require_and_verify"
	cfg.TLS.MinVersion = "TLS1.3"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: TLS with certs should be valid, got %v", err)
	}
}

func TestValidate_TLSDisabled_NoCert_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.Enabled = false
	cfg.TLS.CertFile = ""
	cfg.TLS.KeyFile = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: TLS disabled without certs should be valid, got %v", err)
	}
}

func TestValidate_TLSEnabled_ManualCerts_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.Enabled = true
	cfg.TLS.CAFile = "/etc/tls/ca.pem"
	cfg.TLS.CertFile = "/etc/tls/cert.pem"
	cfg.TLS.KeyFile = "/etc/tls/key.pem"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: TLS with manual certs should be valid, got %v", err)
	}
}

func TestValidate_AllValidSMTypes(t *testing.T) {
	allTypes := []string{
		"pebble", "memory", "mvcc",
		"standard", "concurrent", "fast", "disk", "highperf", "regular",
	}
	for _, smType := range allTypes {
		t.Run(smType, func(t *testing.T) {
			cfg := validConfig()
			cfg.Raft.SMType = smType
			if err := cfg.Validate(); err != nil {
				t.Errorf("SMType %q should be valid, got %v", smType, err)
			}
		})
	}
}

// TestValidate_MVCCFastIsRefused pins the removal of the mvcc-fast sm_type
// (go-qrdb#136): a config naming it is refused at load, not at startup.
func TestValidate_MVCCFastIsRefused(t *testing.T) {
	cfg := validConfig()
	cfg.Raft.SMType = "mvcc-fast"
	var ve *ValidationError
	if err := cfg.Validate(); !errors.As(err, &ve) || ve.Field != "raft.sm_type" {
		t.Fatalf("Validate(sm_type mvcc-fast) = %v; want a raft.sm_type ValidationError", err)
	}
}

func TestValidate_AllValidSMBackends(t *testing.T) {
	allBackends := []string{"standard", "concurrent", "fast", "disk"}
	for _, backend := range allBackends {
		t.Run(backend, func(t *testing.T) {
			cfg := validConfig()
			cfg.Raft.SMBackend = backend
			if err := cfg.Validate(); err != nil {
				t.Errorf("SMBackend %q should be valid, got %v", backend, err)
			}
		})
	}
}

func TestValidate_EmptySMBackend_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Raft.SMBackend = "" // empty is allowed (falls back to SMType)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty SMBackend should be valid, got %v", err)
	}
}

func TestValidate_AllValidBackends(t *testing.T) {
	for _, backend := range []string{"pebble", "memory"} {
		t.Run(backend, func(t *testing.T) {
			cfg := validConfig()
			cfg.Storage.Backend = backend
			if err := cfg.Validate(); err != nil {
				t.Errorf("Backend %q should be valid, got %v", backend, err)
			}
		})
	}
}

func TestValidate_AllValidLogLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			cfg := validConfig()
			cfg.Logging.Level = level
			if err := cfg.Validate(); err != nil {
				t.Errorf("Level %q should be valid, got %v", level, err)
			}
		})
	}
}

func TestValidate_AllValidLogFormats(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			cfg := validConfig()
			cfg.Logging.Format = format
			if err := cfg.Validate(); err != nil {
				t.Errorf("Format %q should be valid, got %v", format, err)
			}
		})
	}
}

func TestValidate_AllValidDiscoveryMethods(t *testing.T) {
	for _, method := range []string{"static", "multicast", "dns"} {
		t.Run(method, func(t *testing.T) {
			cfg := validConfig()
			cfg.Discovery.Method = method
			if err := cfg.Validate(); err != nil {
				t.Errorf("Method %q should be valid, got %v", method, err)
			}
		})
	}
}

func TestValidate_CacheSizeZero_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.CacheSize = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: CacheSize=0 should be valid, got %v", err)
	}
}

func TestValidate_MaxOpenFilesZero_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.MaxOpenFiles = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: MaxOpenFiles=0 should be valid, got %v", err)
	}
}

func TestValidate_PositiveCacheSize_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.CacheSize = 1 << 30 // 1GB
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: positive CacheSize should be valid, got %v", err)
	}
}

func TestValidate_PositiveMaxOpenFiles_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.MaxOpenFiles = 1024
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: positive MaxOpenFiles should be valid, got %v", err)
	}
}

func TestValidate_WatchZeroValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Watch.BufferSize = 0
	cfg.Watch.MaxWatchers = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Watch zero values should be valid, got %v", err)
	}
}

func TestValidate_LeaseZeroValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Lease.MinTTL = 0
	cfg.Lease.MaxTTL = 0
	cfg.Lease.CheckInterval = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Lease zero values should be valid, got %v", err)
	}
}

func TestLeaseCheckpointDefaultsAreTheLeasePackagesDefaults(t *testing.T) {
	cfg := DefaultConfig()
	interval, err := cfg.Lease.CheckpointIntervalDuration()
	if err != nil || interval != lease.DefaultCheckpointInterval {
		t.Fatalf("checkpoint interval = %v, %v; want %v", interval, err, lease.DefaultCheckpointInterval)
	}
	grace, err := cfg.Lease.FailoverGraceDuration()
	if err != nil || grace != lease.DefaultFailoverGrace {
		t.Fatalf("failover grace = %v, %v; want %v", grace, err, lease.DefaultFailoverGrace)
	}
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate with the defaults: %v", err)
	}
}

func TestValidate_LeaseCheckpoint(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		field  string // empty means valid
	}{
		{"interval_ok", func(c *Config) { c.Lease.CheckpointInterval = "250ms" }, ""},
		{"interval_default", func(c *Config) { c.Lease.CheckpointInterval = "5m" }, ""},
		{"interval_max", func(c *Config) { c.Lease.CheckpointInterval = "1h" }, ""},
		{"interval_zero", func(c *Config) { c.Lease.CheckpointInterval = "0s" }, "lease.checkpoint_interval"},
		{"interval_negative", func(c *Config) { c.Lease.CheckpointInterval = "-1s" }, "lease.checkpoint_interval"},
		{"interval_over_max", func(c *Config) { c.Lease.CheckpointInterval = "61m" }, "lease.checkpoint_interval"},
		{"interval_garbage", func(c *Config) { c.Lease.CheckpointInterval = "soon" }, "lease.checkpoint_interval"},
		{"grace_zero_ok", func(c *Config) { c.Lease.FailoverGrace = "0s" }, ""},
		{"grace_ok", func(c *Config) { c.Lease.FailoverGrace = "30s" }, ""},
		{"grace_negative", func(c *Config) { c.Lease.FailoverGrace = "-1s" }, "lease.failover_grace"},
		{"grace_over_max", func(c *Config) { c.Lease.FailoverGrace = "2h" }, "lease.failover_grace"},
		{"grace_garbage", func(c *Config) { c.Lease.FailoverGrace = "later" }, "lease.failover_grace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("Validate error = %v, want a ValidationError on %s", err, tc.field)
			}
		})
	}
}

func TestLoad_LeaseCheckpointKeys(t *testing.T) {
	path := writeTemp(t, validYAML+`
lease:
  checkpoint_interval: 500ms
  failover_grace: 12s
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := cfg.Lease.CheckpointIntervalDuration(); got != 500*time.Millisecond {
		t.Fatalf("checkpoint interval = %v, want 500ms", got)
	}
	if got, _ := cfg.Lease.FailoverGraceDuration(); got != 12*time.Second {
		t.Fatalf("failover grace = %v, want 12s", got)
	}
}

func TestValidate_LeaseMinEqualMax_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Lease.MinTTL = 10
	cfg.Lease.MaxTTL = 10
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Lease MinTTL=MaxTTL should be valid, got %v", err)
	}
}

func TestValidate_LeaseMinLessThanMax_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Lease.MinTTL = 5
	cfg.Lease.MaxTTL = 100
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Lease MinTTL<MaxTTL should be valid, got %v", err)
	}
}

func TestValidate_IndexZeroValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Index.MaxIndexes = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Index zero values should be valid, got %v", err)
	}
}

func TestValidate_TimeSeriesZeroValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.TimeSeries.DefaultRetention = 0
	cfg.TimeSeries.MaxMetrics = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: TimeSeries zero values should be valid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error type assertions and messages
// ---------------------------------------------------------------------------

func TestLoadError_ErrorString(t *testing.T) {
	le := &LoadError{
		Path: "/tmp/bad.yaml",
		Err:  os.ErrNotExist,
	}
	got := le.Error()
	if !strings.Contains(got, "/tmp/bad.yaml") {
		t.Errorf("LoadError.Error: missing path in %q", got)
	}
	if !strings.Contains(got, "failed to load") {
		t.Errorf("LoadError.Error: missing prefix in %q", got)
	}
}

func TestLoadError_Unwrap(t *testing.T) {
	inner := os.ErrPermission
	le := &LoadError{Path: "/tmp/x.yaml", Err: inner}
	if !errors.Is(le, os.ErrPermission) {
		t.Error("LoadError.Unwrap: errors.Is failed for inner error")
	}
}

func TestValidationError_ErrorString(t *testing.T) {
	ve := &ValidationError{
		Field:  "raft.shard_id",
		Reason: "must be greater than 0",
	}
	got := ve.Error()
	if !strings.Contains(got, "raft.shard_id") {
		t.Errorf("ValidationError.Error: missing field in %q", got)
	}
	if !strings.Contains(got, "must be greater than 0") {
		t.Errorf("ValidationError.Error: missing reason in %q", got)
	}
	if !strings.Contains(got, "validation failed") {
		t.Errorf("ValidationError.Error: missing prefix in %q", got)
	}
}

// ---------------------------------------------------------------------------
// Load - YAML overrides default values
// ---------------------------------------------------------------------------

func TestLoad_OverridesAllDefaults(t *testing.T) {
	fullYAML := `
server:
  admin_port: 1234
  grpc_port: 5678
  host: "10.0.0.5"
protocols:
  rest: false
  grpc: false
raft:
  data_dir: "/custom/data"
  deployment_id: 42
  shard_id: 7
  replica_id: 3
  raft_address: "10.0.0.5:63001"
  listen_address: "0.0.0.0:63001"
  peers: "3=10.0.0.5:63001,7=10.0.0.6:63001"
  join: true
  snapshot_entries: 10000
  compaction_overhead: 500
  max_in_mem_log_size: 1048576
  max_total_in_mem_log_size: 10485760
  sm_type: "memory"
storage:
  backend: "memory"
  cache_size: 536870912
  wal_dir: "/custom/wal"
  max_open_files: 4096
tls:
  enabled: false
  cert_file: ""
  key_file: ""
  client_auth: "none"
  min_version: "TLS1.2"
logging:
  level: "debug"
  format: "json"
discovery:
  method: "dns"
  multicast_secret: "secret123"
  dns_domain: "cluster.local"
`
	path := writeTemp(t, fullYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	// Server
	if cfg.Server.AdminPort != 1234 {
		t.Errorf("AdminPort: got %d, want 1234", cfg.Server.AdminPort)
	}
	if cfg.Server.GRPCPort != 5678 {
		t.Errorf("GRPCPort: got %d, want 5678", cfg.Server.GRPCPort)
	}
	if cfg.Server.Host != "10.0.0.5" {
		t.Errorf("Host: got %q, want %q", cfg.Server.Host, "10.0.0.5")
	}

	// Protocols
	if cfg.Protocols.REST {
		t.Error("REST: got true, want false")
	}
	if cfg.Protocols.GRPC {
		t.Error("GRPC: got true, want false")
	}

	// Raft
	if cfg.Raft.DataDir != "/custom/data" {
		t.Errorf("DataDir: got %q, want %q", cfg.Raft.DataDir, "/custom/data")
	}
	if cfg.Raft.DeploymentID != 42 {
		t.Errorf("DeploymentID: got %d, want 42", cfg.Raft.DeploymentID)
	}
	if cfg.Raft.ShardID != 7 {
		t.Errorf("ShardID: got %d, want 7", cfg.Raft.ShardID)
	}
	if cfg.Raft.ReplicaID != 3 {
		t.Errorf("ReplicaID: got %d, want 3", cfg.Raft.ReplicaID)
	}
	if cfg.Raft.RaftAddress != "10.0.0.5:63001" {
		t.Errorf("RaftAddress: got %q, want %q", cfg.Raft.RaftAddress, "10.0.0.5:63001")
	}
	if cfg.Raft.ListenAddress != "0.0.0.0:63001" {
		t.Errorf("ListenAddress: got %q, want %q", cfg.Raft.ListenAddress, "0.0.0.0:63001")
	}
	if cfg.Raft.Peers != "3=10.0.0.5:63001,7=10.0.0.6:63001" {
		t.Errorf("Peers: got %q", cfg.Raft.Peers)
	}
	if !cfg.Raft.Join {
		t.Error("Join: got false, want true")
	}
	if cfg.Raft.SnapshotEntries != 10000 {
		t.Errorf("SnapshotEntries: got %d, want 10000", cfg.Raft.SnapshotEntries)
	}
	if cfg.Raft.CompactionOverhead != 500 {
		t.Errorf("CompactionOverhead: got %d, want 500", cfg.Raft.CompactionOverhead)
	}
	if cfg.Raft.MaxInMemLogSize != 1048576 {
		t.Errorf("MaxInMemLogSize: got %d, want 1048576", cfg.Raft.MaxInMemLogSize)
	}
	if cfg.Raft.MaxTotalInMemLogSize != 10485760 {
		t.Errorf("MaxTotalInMemLogSize: got %d, want 10485760", cfg.Raft.MaxTotalInMemLogSize)
	}
	if cfg.Raft.SMType != "memory" {
		t.Errorf("SMType: got %q, want %q", cfg.Raft.SMType, "memory")
	}

	// Storage
	if cfg.Storage.Backend != "memory" {
		t.Errorf("Backend: got %q, want %q", cfg.Storage.Backend, "memory")
	}
	if cfg.Storage.CacheSize != 536870912 {
		t.Errorf("CacheSize: got %d, want 536870912", cfg.Storage.CacheSize)
	}
	if cfg.Storage.WALDir != "/custom/wal" {
		t.Errorf("WALDir: got %q, want %q", cfg.Storage.WALDir, "/custom/wal")
	}
	if cfg.Storage.MaxOpenFiles != 4096 {
		t.Errorf("MaxOpenFiles: got %d, want 4096", cfg.Storage.MaxOpenFiles)
	}

	// TLS
	if cfg.TLS.Enabled {
		t.Error("TLS.Enabled: got true, want false")
	}
	if cfg.TLS.ClientAuth != "none" {
		t.Errorf("TLS.ClientAuth: got %q, want %q", cfg.TLS.ClientAuth, "none")
	}
	if cfg.TLS.MinVersion != "TLS1.2" {
		t.Errorf("TLS.MinVersion: got %q, want %q", cfg.TLS.MinVersion, "TLS1.2")
	}

	// Logging
	if cfg.Logging.Level != "debug" {
		t.Errorf("Level: got %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Format: got %q, want %q", cfg.Logging.Format, "json")
	}

	// Discovery
	if cfg.Discovery.Method != "dns" {
		t.Errorf("Method: got %q, want %q", cfg.Discovery.Method, "dns")
	}
	if cfg.Discovery.MulticastSecret != "secret123" {
		t.Errorf("MulticastSecret: got %q, want %q", cfg.Discovery.MulticastSecret, "secret123")
	}
	if cfg.Discovery.DNSDomain != "cluster.local" {
		t.Errorf("DNSDomain: got %q, want %q", cfg.Discovery.DNSDomain, "cluster.local")
	}
}

// ---------------------------------------------------------------------------
// Load - empty file inherits all defaults
// ---------------------------------------------------------------------------

func TestLoad_EmptyFile_InheritsDefaults(t *testing.T) {
	// An empty YAML document leaves the default config intact. TLS is
	// enabled by default but cert/key files are not required (the daemon
	// auto-generates them via resolveTLS), so loading should succeed.
	path := writeTemp(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled: got false, want true")
	}
	// A file that omits tls.client_auth must yield the empty auth-derived
	// default so the daemon can distinguish "operator chose a mode" from
	// "derive the mode from the auth posture".
	if cfg.TLS.ClientAuth != "" {
		t.Errorf("TLS.ClientAuth: got %q, want empty (auth-derived)", cfg.TLS.ClientAuth)
	}
}

func TestLoad_EmptyFile_WithTLSDisabled_InheritsDefaults(t *testing.T) {
	// Disable TLS to allow loading with all other defaults.
	yaml := `
tls:
  enabled: false
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	expected := DefaultConfig()
	if cfg.Server.AdminPort != expected.Server.AdminPort {
		t.Errorf("AdminPort: got %d, want %d", cfg.Server.AdminPort, expected.Server.AdminPort)
	}
	if cfg.Raft.ShardID != expected.Raft.ShardID {
		t.Errorf("ShardID: got %d, want %d", cfg.Raft.ShardID, expected.Raft.ShardID)
	}
	if cfg.Logging.Level != expected.Logging.Level {
		t.Errorf("Level: got %q, want %q", cfg.Logging.Level, expected.Logging.Level)
	}
}

// ---------------------------------------------------------------------------
// Load - unreadable file
// ---------------------------------------------------------------------------

func TestLoad_UnreadableFile(t *testing.T) {
	// Use a directory in place of a file so os.ReadFile fails regardless
	// of whether we are running as root (chmod is ineffective for root).
	dir := t.TempDir()
	p := filepath.Join(dir, "noperm.yaml")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatalf("setup: Mkdir: %v", err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load: expected error for unreadable file, got nil")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load: expected *LoadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Load - YAML with new config sections
// ---------------------------------------------------------------------------

func TestLoad_NewSections_ParsedCorrectly(t *testing.T) {
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-sections"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  sm_type: "mvcc"
storage:
  backend: "pebble"
tls:
  enabled: false
logging:
  level: "info"
  format: "json"
discovery:
  method: "static"
watch:
  buffer_size: 2048
  max_watchers: 500
lease:
  min_ttl: 10
  max_ttl: 3600
  check_interval: 5
index:
  max_indexes: 64
timeseries:
  default_retention: 86400
  max_metrics: 10000
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	// Watch
	if cfg.Watch.BufferSize != 2048 {
		t.Errorf("Watch.BufferSize: got %d, want 2048", cfg.Watch.BufferSize)
	}
	if cfg.Watch.MaxWatchers != 500 {
		t.Errorf("Watch.MaxWatchers: got %d, want 500", cfg.Watch.MaxWatchers)
	}

	// Lease
	if cfg.Lease.MinTTL != 10 {
		t.Errorf("Lease.MinTTL: got %d, want 10", cfg.Lease.MinTTL)
	}
	if cfg.Lease.MaxTTL != 3600 {
		t.Errorf("Lease.MaxTTL: got %d, want 3600", cfg.Lease.MaxTTL)
	}
	if cfg.Lease.CheckInterval != 5 {
		t.Errorf("Lease.CheckInterval: got %d, want 5", cfg.Lease.CheckInterval)
	}

	// Index
	if cfg.Index.MaxIndexes != 64 {
		t.Errorf("Index.MaxIndexes: got %d, want 64", cfg.Index.MaxIndexes)
	}

	// TimeSeries
	if cfg.TimeSeries.DefaultRetention != 86400 {
		t.Errorf("TimeSeries.DefaultRetention: got %d, want 86400", cfg.TimeSeries.DefaultRetention)
	}
	if cfg.TimeSeries.MaxMetrics != 10000 {
		t.Errorf("TimeSeries.MaxMetrics: got %d, want 10000", cfg.TimeSeries.MaxMetrics)
	}
}

func TestLoad_NewSections_DefaultsFillGaps(t *testing.T) {
	// Provide only required sections; new sections should inherit defaults.
	partial := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-partial"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
tls:
  enabled: false
`
	path := writeTemp(t, partial)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	defaults := DefaultConfig()

	// Watch defaults preserved.
	if cfg.Watch.BufferSize != defaults.Watch.BufferSize {
		t.Errorf("Watch.BufferSize: got %d, want default %d",
			cfg.Watch.BufferSize, defaults.Watch.BufferSize)
	}
	if cfg.Watch.MaxWatchers != defaults.Watch.MaxWatchers {
		t.Errorf("Watch.MaxWatchers: got %d, want default %d",
			cfg.Watch.MaxWatchers, defaults.Watch.MaxWatchers)
	}

	// Lease defaults preserved.
	if cfg.Lease.MinTTL != defaults.Lease.MinTTL {
		t.Errorf("Lease.MinTTL: got %d, want default %d",
			cfg.Lease.MinTTL, defaults.Lease.MinTTL)
	}
	if cfg.Lease.MaxTTL != defaults.Lease.MaxTTL {
		t.Errorf("Lease.MaxTTL: got %d, want default %d",
			cfg.Lease.MaxTTL, defaults.Lease.MaxTTL)
	}
	if cfg.Lease.CheckInterval != defaults.Lease.CheckInterval {
		t.Errorf("Lease.CheckInterval: got %d, want default %d",
			cfg.Lease.CheckInterval, defaults.Lease.CheckInterval)
	}

	// Index defaults preserved.
	if cfg.Index.MaxIndexes != defaults.Index.MaxIndexes {
		t.Errorf("Index.MaxIndexes: got %d, want default %d",
			cfg.Index.MaxIndexes, defaults.Index.MaxIndexes)
	}

	// TimeSeries defaults preserved.
	if cfg.TimeSeries.DefaultRetention != defaults.TimeSeries.DefaultRetention {
		t.Errorf("TimeSeries.DefaultRetention: got %d, want default %d",
			cfg.TimeSeries.DefaultRetention, defaults.TimeSeries.DefaultRetention)
	}
	if cfg.TimeSeries.MaxMetrics != defaults.TimeSeries.MaxMetrics {
		t.Errorf("TimeSeries.MaxMetrics: got %d, want default %d",
			cfg.TimeSeries.MaxMetrics, defaults.TimeSeries.MaxMetrics)
	}

	// Unix defaults preserved.
	if cfg.Unix.SocketPath != defaults.Unix.SocketPath {
		t.Errorf("Unix.SocketPath: got %q, want default %q",
			cfg.Unix.SocketPath, defaults.Unix.SocketPath)
	}

	// MCP defaults preserved.
	if cfg.MCP.Port != defaults.MCP.Port {
		t.Errorf("MCP.Port: got %d, want default %d",
			cfg.MCP.Port, defaults.MCP.Port)
	}
	if cfg.MCP.Endpoint != defaults.MCP.Endpoint {
		t.Errorf("MCP.Endpoint: got %q, want default %q",
			cfg.MCP.Endpoint, defaults.MCP.Endpoint)
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig - WriteMode defaults
// ---------------------------------------------------------------------------

func TestDefaultConfig_WriteModeDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WriteMode.Default != "sync" {
		t.Errorf("WriteMode.Default: got %q, want %q", cfg.WriteMode.Default, "sync")
	}
	if cfg.WriteMode.Batch.MaxSize != 1000 {
		t.Errorf("WriteMode.Batch.MaxSize: got %d, want 1000", cfg.WriteMode.Batch.MaxSize)
	}
	if cfg.WriteMode.Batch.FlushIntervalMS != 50 {
		t.Errorf("WriteMode.Batch.FlushIntervalMS: got %d, want 50", cfg.WriteMode.Batch.FlushIntervalMS)
	}
	if cfg.WriteMode.Batch.QueueSize != 10000 {
		t.Errorf("WriteMode.Batch.QueueSize: got %d, want 10000", cfg.WriteMode.Batch.QueueSize)
	}
}

func TestDefaultConfig_WriteModeDefaults_PassesValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TLS.Enabled = false // disable TLS so we don't need cert paths
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig with WriteMode defaults failed validation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validate - WriteMode validation
// ---------------------------------------------------------------------------

func TestValidate_AllValidWriteModes(t *testing.T) {
	for _, mode := range []string{"", "sync", "wal_durable", "batched"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			cfg := validConfig()
			cfg.WriteMode.Default = mode
			if err := cfg.Validate(); err != nil {
				t.Errorf("WriteMode.Default %q should be valid, got %v", mode, err)
			}
		})
	}
}

func TestValidate_EmptyWriteMode_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Default = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: empty WriteMode.Default should be valid, got %v", err)
	}
}

func TestValidate_InvalidWriteMode(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Default = "eventual"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for invalid write_mode.default, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "write_mode.default" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "write_mode.default")
	}
	if !strings.Contains(ve.Reason, "sync, wal_durable, batched") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "sync, wal_durable, batched")
	}
}

func TestValidate_NegativeBatchMaxSize(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Batch.MaxSize = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for negative batch.max_size, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "write_mode.batch.max_size" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "write_mode.batch.max_size")
	}
	if !strings.Contains(ve.Reason, "must not be negative") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "must not be negative")
	}
}

func TestValidate_NegativeBatchFlushIntervalMS(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Batch.FlushIntervalMS = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for negative batch.flush_interval_ms, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "write_mode.batch.flush_interval_ms" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "write_mode.batch.flush_interval_ms")
	}
	if !strings.Contains(ve.Reason, "must not be negative") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "must not be negative")
	}
}

func TestValidate_NegativeBatchQueueSize(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Batch.QueueSize = -1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for negative batch.queue_size, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "write_mode.batch.queue_size" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "write_mode.batch.queue_size")
	}
	if !strings.Contains(ve.Reason, "must not be negative") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "must not be negative")
	}
}

func TestValidate_BatchZeroValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.WriteMode.Batch.MaxSize = 0
	cfg.WriteMode.Batch.FlushIntervalMS = 0
	cfg.WriteMode.Batch.QueueSize = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: batch zero values should be valid, got %v", err)
	}
}

func TestValidate_WriteModeInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		field  string
		reason string
	}{
		{
			name:   "write_mode_default_invalid",
			mutate: func(c *Config) { c.WriteMode.Default = "fast" },
			field:  "write_mode.default",
			reason: "sync, wal_durable, batched",
		},
		{
			name:   "batch_max_size_negative",
			mutate: func(c *Config) { c.WriteMode.Batch.MaxSize = -100 },
			field:  "write_mode.batch.max_size",
			reason: "must not be negative",
		},
		{
			name:   "batch_flush_interval_ms_negative",
			mutate: func(c *Config) { c.WriteMode.Batch.FlushIntervalMS = -5 },
			field:  "write_mode.batch.flush_interval_ms",
			reason: "must not be negative",
		},
		{
			name:   "batch_queue_size_negative",
			mutate: func(c *Config) { c.WriteMode.Batch.QueueSize = -42 },
			field:  "write_mode.batch.queue_size",
			reason: "must not be negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate: expected error for field %q, got nil", tc.field)
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
			}
			if ve.Field != tc.field {
				t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, tc.field)
			}
			if !strings.Contains(ve.Reason, tc.reason) {
				t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, tc.reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TLSConfig - CryptoTLSClientAuth helper
// ---------------------------------------------------------------------------

func TestTLSConfig_CryptoTLSClientAuth(t *testing.T) {
	tests := []struct {
		name       string
		clientAuth string
		want       tls.ClientAuthType
	}{
		{"none", "none", tls.NoClientCert},
		{"request", "request", tls.RequestClientCert},
		{"require", "require", tls.RequireAnyClientCert},
		{"require_and_verify", "require_and_verify", tls.RequireAndVerifyClientCert},
		{"default_on_empty", "", tls.RequireAndVerifyClientCert},
		{"default_on_unknown", "bogus", tls.RequireAndVerifyClientCert},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &TLSConfig{ClientAuth: tc.clientAuth}
			got := cfg.CryptoTLSClientAuth()
			if got != tc.want {
				t.Errorf("CryptoTLSClientAuth(%q): got %v, want %v", tc.clientAuth, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TLSConfig - CryptoTLSMinVersion helper
// ---------------------------------------------------------------------------

func TestTLSConfig_CryptoTLSMinVersion(t *testing.T) {
	tests := []struct {
		name       string
		minVersion string
		want       uint16
	}{
		{"TLS1.2", "TLS1.2", tls.VersionTLS12},
		{"TLS1.3", "TLS1.3", tls.VersionTLS13},
		{"default_on_empty", "", tls.VersionTLS13},
		{"default_on_unknown", "TLS1.0", tls.VersionTLS13},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &TLSConfig{MinVersion: tc.minVersion}
			got := cfg.CryptoTLSMinVersion()
			if got != tc.want {
				t.Errorf("CryptoTLSMinVersion(%q): got %d, want %d", tc.minVersion, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TLSConfig - Validation: cert/key are optional (auto-generated by daemon)
// ---------------------------------------------------------------------------

func TestTLSConfig_Validate_EmptyCertKeyAllowed(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.Enabled = true
	cfg.TLS.CertFile = ""
	cfg.TLS.KeyFile = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: expected nil error when cert/key omitted (auto-generated), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TLSConfig - Validation: invalid client_auth
// ---------------------------------------------------------------------------

func TestTLSConfig_Validate_InvalidClientAuth(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.ClientAuth = "mandatory"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for invalid client_auth, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "tls.client_auth" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "tls.client_auth")
	}
	if !strings.Contains(ve.Reason, "none, request, require, require_and_verify") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "none, request, require, require_and_verify")
	}
}

func TestTLSConfig_Validate_AllValidClientAuthModes(t *testing.T) {
	for _, mode := range []string{"", "none", "request", "require", "require_and_verify"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			cfg := validConfig()
			cfg.TLS.ClientAuth = mode
			if err := cfg.Validate(); err != nil {
				t.Errorf("ClientAuth %q should be valid, got %v", mode, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TLSConfig - Validation: invalid min_version
// ---------------------------------------------------------------------------

func TestTLSConfig_Validate_InvalidMinVersion(t *testing.T) {
	cfg := validConfig()
	cfg.TLS.MinVersion = "SSLv3"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for invalid min_version, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "tls.min_version" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "tls.min_version")
	}
	if !strings.Contains(ve.Reason, "TLS1.2, TLS1.3") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "TLS1.2, TLS1.3")
	}
}

func TestTLSConfig_Validate_AllValidMinVersions(t *testing.T) {
	for _, version := range []string{"", "TLS1.2", "TLS1.3"} {
		t.Run("version_"+version, func(t *testing.T) {
			cfg := validConfig()
			cfg.TLS.MinVersion = version
			if err := cfg.Validate(); err != nil {
				t.Errorf("MinVersion %q should be valid, got %v", version, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultConfig - TLS defaults verification
// ---------------------------------------------------------------------------

func TestDefaultConfig_TLSDefaults_ClientAuthAndMinVersion(t *testing.T) {
	cfg := DefaultConfig()

	// ClientAuth must default to empty: the daemon treats any non-empty value
	// as an explicit operator selection and applies it to every listener,
	// which makes cert-exempt endpoints like /health unreachable without a
	// client certificate. Empty means "derive from the auth posture"
	// (VerifyClientCertIfGiven when auth is enabled, NoClientCert otherwise).
	if cfg.TLS.ClientAuth != "" {
		t.Errorf("TLS.ClientAuth: got %q, want empty (auth-derived)", cfg.TLS.ClientAuth)
	}
	if cfg.TLS.MinVersion != "TLS1.3" {
		t.Errorf("TLS.MinVersion: got %q, want %q", cfg.TLS.MinVersion, "TLS1.3")
	}

	// Verify the helper method returns the correct crypto/tls value for the
	// MinVersion default.
	if cfg.TLS.CryptoTLSMinVersion() != tls.VersionTLS13 {
		t.Errorf("CryptoTLSMinVersion: got %d, want %d (TLS 1.3)", cfg.TLS.CryptoTLSMinVersion(), tls.VersionTLS13)
	}
}

// ---------------------------------------------------------------------------
// TLS - YAML parsing of new fields
// ---------------------------------------------------------------------------

func TestLoad_TLSNewFields_ParsedCorrectly(t *testing.T) {
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-tls"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
tls:
  enabled: true
  ca_file: "/etc/tls/ca.pem"
  cert_file: "/etc/tls/cert.pem"
  key_file: "/etc/tls/key.pem"
  client_auth: "request"
  min_version: "TLS1.2"
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if !cfg.TLS.Enabled {
		t.Error("TLS.Enabled: got false, want true")
	}
	if cfg.TLS.CAFile != "/etc/tls/ca.pem" {
		t.Errorf("TLS.CAFile: got %q, want %q", cfg.TLS.CAFile, "/etc/tls/ca.pem")
	}
	if cfg.TLS.CertFile != "/etc/tls/cert.pem" {
		t.Errorf("TLS.CertFile: got %q, want %q", cfg.TLS.CertFile, "/etc/tls/cert.pem")
	}
	if cfg.TLS.KeyFile != "/etc/tls/key.pem" {
		t.Errorf("TLS.KeyFile: got %q, want %q", cfg.TLS.KeyFile, "/etc/tls/key.pem")
	}
	if cfg.TLS.ClientAuth != "request" {
		t.Errorf("TLS.ClientAuth: got %q, want %q", cfg.TLS.ClientAuth, "request")
	}
	if cfg.TLS.MinVersion != "TLS1.2" {
		t.Errorf("TLS.MinVersion: got %q, want %q", cfg.TLS.MinVersion, "TLS1.2")
	}

	// Verify helper conversions match.
	if cfg.TLS.CryptoTLSClientAuth() != tls.RequestClientCert {
		t.Errorf("CryptoTLSClientAuth: got %v, want RequestClientCert", cfg.TLS.CryptoTLSClientAuth())
	}
	if cfg.TLS.CryptoTLSMinVersion() != tls.VersionTLS12 {
		t.Errorf("CryptoTLSMinVersion: got %d, want %d (TLS 1.2)", cfg.TLS.CryptoTLSMinVersion(), tls.VersionTLS12)
	}
}

// ---------------------------------------------------------------------------
// Unix socket - validation
// ---------------------------------------------------------------------------

func TestValidate_UnixEnabled_WithSocketPath_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Protocols.Unix = true
	cfg.Unix.SocketPath = "/tmp/qrdbd.sock"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Unix enabled with socket path should be valid, got %v", err)
	}
}

func TestValidate_UnixEnabled_EmptySocketPath_Error(t *testing.T) {
	cfg := validConfig()
	cfg.Protocols.Unix = true
	cfg.Unix.SocketPath = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for unix enabled with empty socket_path, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "unix.socket_path" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "unix.socket_path")
	}
	if !strings.Contains(ve.Reason, "must not be empty when unix protocol is enabled") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "must not be empty when unix protocol is enabled")
	}
}

func TestValidate_UnixDisabled_EmptySocketPath_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Protocols.Unix = false
	cfg.Unix.SocketPath = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: Unix disabled with empty socket_path should be valid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// MCP - validation
// ---------------------------------------------------------------------------

func TestValidate_MCPEnabled_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Protocols.MCP = true
	cfg.MCP.Port = 63005
	cfg.MCP.Endpoint = "/mcp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: MCP enabled with valid config should be valid, got %v", err)
	}
}

func TestValidate_MCPPortBoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		port  int
		valid bool
	}{
		{"min_valid", 0, true},
		{"max_valid", 65535, true},
		{"below_min", -1, false},
		{"above_max", 65536, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.MCP.Port = tc.port
			err := cfg.Validate()
			if tc.valid && err != nil {
				t.Errorf("port %d: expected valid, got %v", tc.port, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("port %d: expected error, got nil", tc.port)
			}
		})
	}
}

func TestValidate_MCPEndpointWithLeadingSlash_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.MCP.Endpoint = "/custom/mcp"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: MCP endpoint with leading slash should be valid, got %v", err)
	}
}

func TestValidate_MCPEndpointWithoutLeadingSlash_Error(t *testing.T) {
	cfg := validConfig()
	cfg.MCP.Endpoint = "mcp"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate: expected error for MCP endpoint without leading slash, got nil")
	}

	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate: expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "mcp.endpoint" {
		t.Errorf("ValidationError.Field: got %q, want %q", ve.Field, "mcp.endpoint")
	}
	if !strings.Contains(ve.Reason, "must start with /") {
		t.Errorf("ValidationError.Reason: got %q, want substring %q", ve.Reason, "must start with /")
	}
}

func TestValidate_MCPEmptyEndpoint_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.MCP.Endpoint = "" // empty is allowed (no prefix check)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: MCP empty endpoint should be valid, got %v", err)
	}
}

func TestValidate_MCPPortZero_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.MCP.Port = 0 // 0 = disabled, should be valid
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: MCP port 0 should be valid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Load - YAML with Unix and MCP fields
// ---------------------------------------------------------------------------

func TestLoad_UnixAndMCP_ParsedCorrectly(t *testing.T) {
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
protocols:
  rest: true
  grpc: true
  unix: true
  mcp: true
raft:
  data_dir: "/tmp/qrdb-unix-mcp"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: "memory"
tls:
  enabled: false
unix:
  socket_path: "/tmp/custom-qrdbd.sock"
mcp:
  port: 9999
  endpoint: "/api/mcp"
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	// Protocols
	if !cfg.Protocols.Unix {
		t.Error("Protocols.Unix: got false, want true")
	}
	if !cfg.Protocols.MCP {
		t.Error("Protocols.MCP: got false, want true")
	}

	// Unix
	if cfg.Unix.SocketPath != "/tmp/custom-qrdbd.sock" {
		t.Errorf("Unix.SocketPath: got %q, want %q", cfg.Unix.SocketPath, "/tmp/custom-qrdbd.sock")
	}

	// MCP
	if cfg.MCP.Port != 9999 {
		t.Errorf("MCP.Port: got %d, want 9999", cfg.MCP.Port)
	}
	if cfg.MCP.Endpoint != "/api/mcp" {
		t.Errorf("MCP.Endpoint: got %q, want %q", cfg.MCP.Endpoint, "/api/mcp")
	}
}

func TestLoad_UnixAndMCP_DefaultsFillGaps(t *testing.T) {
	// Only enable protocols; Unix and MCP sections should inherit defaults.
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
protocols:
  unix: true
  mcp: true
raft:
  data_dir: "/tmp/qrdb-defaults"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
tls:
  enabled: false
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	defaults := DefaultConfig()

	if cfg.Unix.SocketPath != defaults.Unix.SocketPath {
		t.Errorf("Unix.SocketPath: got %q, want default %q",
			cfg.Unix.SocketPath, defaults.Unix.SocketPath)
	}
	if cfg.MCP.Port != defaults.MCP.Port {
		t.Errorf("MCP.Port: got %d, want default %d",
			cfg.MCP.Port, defaults.MCP.Port)
	}
	if cfg.MCP.Endpoint != defaults.MCP.Endpoint {
		t.Errorf("MCP.Endpoint: got %q, want default %q",
			cfg.MCP.Endpoint, defaults.MCP.Endpoint)
	}
}

// ---------------------------------------------------------------------------
// Auth config validation
// ---------------------------------------------------------------------------

func TestDefaultConfig_AuthDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Auth.IsEnabled() {
		t.Error("Auth.Enabled: want true")
	}
	if cfg.Auth.AdminUsername != "admin" {
		t.Errorf("Auth.AdminUsername: got %q, want %q", cfg.Auth.AdminUsername, "admin")
	}
	if cfg.Auth.DefaultTenant != "system" {
		t.Errorf("Auth.DefaultTenant: got %q, want %q", cfg.Auth.DefaultTenant, "system")
	}
	if cfg.Auth.CertTTL != "8760h" {
		t.Errorf("Auth.CertTTL: got %q, want %q", cfg.Auth.CertTTL, "8760h")
	}
	if cfg.Auth.PermCacheTTL != 30 {
		t.Errorf("Auth.PermCacheTTL: got %d, want %d", cfg.Auth.PermCacheTTL, 30)
	}
}

func TestValidate_Auth_EmptyAdminUsername(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Enabled = BoolPtr(true)
	cfg.Auth.AdminUsername = ""
	cfg.Auth.DefaultTenant = "system"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty admin_username")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "auth.admin_username" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.admin_username")
	}
}

func TestValidate_Auth_EmptyDefaultTenant(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Enabled = BoolPtr(true)
	cfg.Auth.AdminUsername = "admin"
	cfg.Auth.DefaultTenant = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty default_tenant")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "auth.default_tenant" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.default_tenant")
	}
}

func TestValidate_Auth_InvalidCertTTL(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Enabled = BoolPtr(true)
	cfg.Auth.AdminUsername = "admin"
	cfg.Auth.DefaultTenant = "system"
	cfg.Auth.CertTTL = "not-a-duration"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid cert_ttl")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "auth.cert_ttl" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.cert_ttl")
	}
}

func TestValidate_Auth_ValidCertTTL(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Enabled = BoolPtr(true)
	cfg.Auth.AdminUsername = "admin"
	cfg.Auth.DefaultTenant = "system"
	cfg.Auth.CertTTL = "720h"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidate_Auth_NegativePermCacheTTL(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.PermCacheTTL = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative perm_cache_ttl")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "auth.perm_cache_ttl" {
		t.Errorf("Field: got %q, want %q", ve.Field, "auth.perm_cache_ttl")
	}
}

func TestValidate_Auth_DisabledSkipsValidation(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.Enabled = BoolPtr(false)
	cfg.Auth.AdminUsername = ""
	cfg.Auth.DefaultTenant = ""
	cfg.Auth.CertTTL = "garbage"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected disabled auth to skip validation, got %v", err)
	}
}

// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CORS config validation
// ---------------------------------------------------------------------------

func TestDefaultConfig_CORSDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.CORS.AllowedOrigins) != 1 || cfg.CORS.AllowedOrigins[0] != "*" {
		t.Errorf("CORS.AllowedOrigins: got %v, want [*]", cfg.CORS.AllowedOrigins)
	}
	if len(cfg.CORS.AllowedHeaders) != 2 {
		t.Errorf("CORS.AllowedHeaders: got %v, want [Authorization Content-Type]", cfg.CORS.AllowedHeaders)
	}
	if cfg.CORS.MaxAge != 3600 {
		t.Errorf("CORS.MaxAge: got %d, want %d", cfg.CORS.MaxAge, 3600)
	}
}

func TestValidate_CORS_NegativeMaxAge(t *testing.T) {
	cfg := validConfig()
	cfg.CORS.MaxAge = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative max_age")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "cors.max_age" {
		t.Errorf("Field: got %q, want %q", ve.Field, "cors.max_age")
	}
}

func TestValidate_CORS_ZeroMaxAge(t *testing.T) {
	cfg := validConfig()
	cfg.CORS.MaxAge = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected zero max_age to be valid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// YAML loading of new config sections
// ---------------------------------------------------------------------------

func TestLoad_AuthConfigFromYAML(t *testing.T) {
	yamlContent := validYAML + `
auth:
  enabled: true
  admin_username: "operator"
  default_tenant: "acme"
  cert_ttl: "720h"
  perm_cache_ttl: 60
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if !cfg.Auth.IsEnabled() {
		t.Error("Auth.Enabled: want true")
	}
	if cfg.Auth.AdminUsername != "operator" {
		t.Errorf("Auth.AdminUsername: got %q, want %q", cfg.Auth.AdminUsername, "operator")
	}
	if cfg.Auth.DefaultTenant != "acme" {
		t.Errorf("Auth.DefaultTenant: got %q, want %q", cfg.Auth.DefaultTenant, "acme")
	}
	if cfg.Auth.CertTTL != "720h" {
		t.Errorf("Auth.CertTTL: got %q, want %q", cfg.Auth.CertTTL, "720h")
	}
	if cfg.Auth.PermCacheTTL != 60 {
		t.Errorf("Auth.PermCacheTTL: got %d, want %d", cfg.Auth.PermCacheTTL, 60)
	}
}

func TestLoad_CORSConfigFromYAML(t *testing.T) {
	yamlContent := validYAML + `
cors:
  allowed_origins:
    - "https://app.example.com"
    - "https://admin.example.com"
  allowed_headers:
    - "Authorization"
    - "Content-Type"
    - "X-Request-ID"
  max_age: 7200
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if len(cfg.CORS.AllowedOrigins) != 2 {
		t.Fatalf("CORS.AllowedOrigins: got %d entries, want 2", len(cfg.CORS.AllowedOrigins))
	}
	if cfg.CORS.AllowedOrigins[0] != "https://app.example.com" {
		t.Errorf("CORS.AllowedOrigins[0]: got %q", cfg.CORS.AllowedOrigins[0])
	}
	if len(cfg.CORS.AllowedHeaders) != 3 {
		t.Fatalf("CORS.AllowedHeaders: got %d entries, want 3", len(cfg.CORS.AllowedHeaders))
	}
	if cfg.CORS.MaxAge != 7200 {
		t.Errorf("CORS.MaxAge: got %d, want %d", cfg.CORS.MaxAge, 7200)
	}
}

// ---------------------------------------------------------------------------
// Bootstrap config
// ---------------------------------------------------------------------------

func TestDefaultConfig_BootstrapDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Bootstrap.NoisePort != 63006 {
		t.Errorf("Bootstrap.NoisePort: got %d, want 63006", cfg.Bootstrap.NoisePort)
	}
	if cfg.Bootstrap.NoiseKey != "" {
		t.Error("Bootstrap.NoiseKey: want empty default")
	}
	if cfg.Bootstrap.NoiseAddr != "" {
		t.Error("Bootstrap.NoiseAddr: want empty default")
	}
}

func TestValidate_Bootstrap_InvalidNoisePort(t *testing.T) {
	cfg := validConfig()
	cfg.Bootstrap.NoisePort = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative noise port")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "bootstrap.noise_port" {
		t.Errorf("expected field bootstrap.noise_port, got %q", ve.Field)
	}
}

func TestValidate_Bootstrap_ValidNoisePort(t *testing.T) {
	cfg := validConfig()
	cfg.Bootstrap.NoisePort = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for noise port 0, got %v", err)
	}

	cfg.Bootstrap.NoisePort = 65535
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for noise port 65535, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SMBackend - YAML parsing
// ---------------------------------------------------------------------------

func TestLoad_SMBackend_Parsed(t *testing.T) {
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-smbackend"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  sm_type: "pebble"
  sm_backend: "concurrent"
storage:
  backend: "memory"
tls:
  enabled: false
logging:
  level: "info"
  format: "json"
discovery:
  method: "static"
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Raft.SMType != "pebble" {
		t.Errorf("SMType: got %q, want %q", cfg.Raft.SMType, "pebble")
	}
	if cfg.Raft.SMBackend != "concurrent" {
		t.Errorf("SMBackend: got %q, want %q", cfg.Raft.SMBackend, "concurrent")
	}
}

func TestLoad_SMBackend_EmptyWhenOmitted(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Raft.SMBackend != "" {
		t.Errorf("SMBackend: got %q, want empty when omitted from YAML", cfg.Raft.SMBackend)
	}
}

// ---------------------------------------------------------------------------
// Codec config
// ---------------------------------------------------------------------------

func TestDefaultConfig_CodecDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Codec.StorageCodec != "msgpack" {
		t.Errorf("Codec.StorageCodec: got %q, want %q", cfg.Codec.StorageCodec, "msgpack")
	}
	if cfg.Codec.WireCodec != "json" {
		t.Errorf("Codec.WireCodec: got %q, want %q", cfg.Codec.WireCodec, "json")
	}
	if !cfg.Codec.UseOptimized {
		t.Error("Codec.UseOptimized: got false, want true")
	}
}

func TestLoad_CodecConfig_ParsedFromYAML(t *testing.T) {
	yamlContent := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-codec"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
storage:
  backend: "memory"
tls:
  enabled: false
logging:
  level: "info"
  format: "json"
discovery:
  method: "static"
codec:
  storage_codec: "json"
  wire_codec: "yaml"
  use_optimized: false
`
	path := writeTemp(t, yamlContent)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Codec.StorageCodec != "json" {
		t.Errorf("Codec.StorageCodec: got %q, want %q", cfg.Codec.StorageCodec, "json")
	}
	if cfg.Codec.WireCodec != "yaml" {
		t.Errorf("Codec.WireCodec: got %q, want %q", cfg.Codec.WireCodec, "yaml")
	}
	if cfg.Codec.UseOptimized {
		t.Error("Codec.UseOptimized: got true, want false")
	}
}

func TestLoad_CodecConfig_DefaultsWhenOmitted(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if cfg.Codec.StorageCodec != "msgpack" {
		t.Errorf("Codec.StorageCodec: got %q, want %q (default)", cfg.Codec.StorageCodec, "msgpack")
	}
	if cfg.Codec.WireCodec != "json" {
		t.Errorf("Codec.WireCodec: got %q, want %q (default)", cfg.Codec.WireCodec, "json")
	}
	if !cfg.Codec.UseOptimized {
		t.Error("Codec.UseOptimized: got false, want true (default)")
	}
}

func TestValidate_Codec_InvalidStorageCodec(t *testing.T) {
	cfg := validConfig()
	cfg.Codec.StorageCodec = "avro"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported storage codec")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "codec.storage_codec" {
		t.Errorf("expected field codec.storage_codec, got %q", ve.Field)
	}
}

func TestValidate_Codec_InvalidWireCodec(t *testing.T) {
	cfg := validConfig()
	cfg.Codec.WireCodec = "bson"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported wire codec")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "codec.wire_codec" {
		t.Errorf("expected field codec.wire_codec, got %q", ve.Field)
	}
}

func TestValidate_Codec_EmptyValues_Allowed(t *testing.T) {
	cfg := validConfig()
	cfg.Codec.StorageCodec = ""
	cfg.Codec.WireCodec = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error for empty codec values, got %v", err)
	}
}

func TestValidate_Codec_AllValidTypes(t *testing.T) {
	validTypes := []string{"json", "msgpack", "yaml", "protobuf"}
	for _, ct := range validTypes {
		cfg := validConfig()
		cfg.Codec.StorageCodec = ct
		cfg.Codec.WireCodec = ct
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected no error for codec type %q, got %v", ct, err)
		}
	}
}

// TestAuthConfig_IsEnabled_ZeroValueIsSecure pins the fail-safe that closed
// the unified-server regression: an embedder that builds a bare Config (or
// DaemonConfig) literal without touching the Auth block MUST get an
// authenticated server. Only an explicit false disables authentication.
func TestAuthConfig_IsEnabled_ZeroValueIsSecure(t *testing.T) {
	var zero AuthConfig
	if !zero.IsEnabled() {
		t.Fatal("zero-value AuthConfig must report enabled: security is the default")
	}
	if !(AuthConfig{Enabled: BoolPtr(true)}).IsEnabled() {
		t.Fatal("explicit true must report enabled")
	}
	if (AuthConfig{Enabled: BoolPtr(false)}).IsEnabled() {
		t.Fatal("explicit false must report disabled")
	}
	if !DefaultConfig().Auth.IsEnabled() {
		t.Fatal("DefaultConfig must enable auth")
	}
}

// ---------------------------------------------------------------------------
// auth.audit_failure_policy (go-qrdb#335)
// ---------------------------------------------------------------------------

// TestLoad_AuditFailurePolicyOmittedIsEmpty pins the DEFAULT at the config
// layer. An omitted key must leave the field empty, because empty is what
// middleware.ParseAuditFailurePolicy resolves to fail-closed. A YAML default
// injected here -- "fail-open" by a copy-paste, or a non-empty string that
// stops matching the parser's table -- would move the security posture
// without touching the middleware that enforces it.
func TestLoad_AuditFailurePolicyOmittedIsEmpty(t *testing.T) {
	path := writeTemp(t, validYAML+`
auth:
  enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.AuditFailurePolicy != "" {
		t.Fatalf("Auth.AuditFailurePolicy: got %q, want empty (the fail-closed default)",
			cfg.Auth.AuditFailurePolicy)
	}
}

// TestLoad_AuditFailurePolicyFromYAML proves the operator's value actually
// reaches the struct. A setting that stops at the YAML file is one the node
// will ignore.
func TestLoad_AuditFailurePolicyFromYAML(t *testing.T) {
	for _, want := range []string{"fail-open", "fail-closed"} {
		t.Run(want, func(t *testing.T) {
			path := writeTemp(t, validYAML+`
auth:
  enabled: true
  audit_failure_policy: "`+want+`"
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Auth.AuditFailurePolicy != want {
				t.Errorf("Auth.AuditFailurePolicy: got %q, want %q", cfg.Auth.AuditFailurePolicy, want)
			}
		})
	}
}

// TestAuthConfig_ZeroValueAuditFailurePolicy: an embedder that builds an
// AuthConfig literal without an audit block gets the fail-closed posture,
// same as an omitted YAML key.
func TestAuthConfig_ZeroValueAuditFailurePolicy(t *testing.T) {
	var c AuthConfig
	if c.AuditFailurePolicy != "" {
		t.Fatalf("zero AuthConfig.AuditFailurePolicy = %q, want empty", c.AuditFailurePolicy)
	}
}
