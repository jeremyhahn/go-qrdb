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

// Package config provides YAML-based configuration loading and validation
// for the QRDB distributed key-value database.
package config

import (
	"crypto/tls"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level QRDB configuration. It is loaded from a YAML
// file and validated before use.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Protocols  ProtocolsConfig  `yaml:"protocols"`
	Raft       RaftConfig       `yaml:"raft"`
	Storage    StorageConfig    `yaml:"storage"`
	TLS        TLSConfig        `yaml:"tls"`
	Unix       UnixConfig       `yaml:"unix"`
	MCP        MCPConfig        `yaml:"mcp"`
	Logging    LoggingConfig    `yaml:"logging"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	MVCC       MVCCConfig       `yaml:"mvcc"`
	Watch      WatchConfig      `yaml:"watch"`
	Lease      LeaseConfig      `yaml:"lease"`
	Index      IndexConfig      `yaml:"index"`
	TimeSeries TimeSeriesConfig `yaml:"timeseries"`
	WriteMode  WriteModeConfig  `yaml:"write_mode"`
}

// ServerConfig configures the QRDB server's network listeners.
type ServerConfig struct {
	// AdminPort is the HTTP port for admin/REST API endpoints.
	AdminPort int `yaml:"admin_port"`

	// GRPCPort is the port for the gRPC data-plane service.
	GRPCPort int `yaml:"grpc_port"`

	// Host is the bind address for all listeners.
	Host string `yaml:"host"`
}

// ProtocolsConfig controls which API protocols are enabled.
type ProtocolsConfig struct {
	// REST enables the HTTP/REST admin and KV API.
	REST bool `yaml:"rest"`

	// GRPC enables the gRPC KV service.
	GRPC bool `yaml:"grpc"`

	// Unix enables the gRPC-over-Unix-socket service.
	Unix bool `yaml:"unix"`

	// MCP enables the Model Context Protocol service.
	MCP bool `yaml:"mcp"`
}

// RaftConfig configures the QuicRaft consensus layer.
type RaftConfig struct {
	// DataDir is the base directory for Raft data files.
	DataDir string `yaml:"data_dir"`

	// DeploymentID isolates Raft clusters on the same network.
	DeploymentID uint64 `yaml:"deployment_id"`

	// ShardID is the Raft shard (group) identifier.
	ShardID uint64 `yaml:"shard_id"`

	// ReplicaID is this node's replica identifier within the shard.
	ReplicaID uint64 `yaml:"replica_id"`

	// RaftAddress is the advertised address for Raft peer communication.
	RaftAddress string `yaml:"raft_address"`

	// ListenAddress is the bind address for Raft peer communication.
	// When empty, RaftAddress is used.
	ListenAddress string `yaml:"listen_address"`

	// Peers is a comma-separated list of initial cluster members in
	// the format "replicaID=address" (e.g., "1=10.0.0.1:63001,2=10.0.0.2:63001").
	Peers string `yaml:"peers"`

	// Join indicates this node is joining an existing cluster rather
	// than forming a new one.
	Join bool `yaml:"join"`

	// SnapshotEntries controls how often automatic snapshots are taken.
	// 0 disables automatic snapshots.
	SnapshotEntries uint64 `yaml:"snapshot_entries"`

	// CompactionOverhead is the number of log entries to keep after
	// a snapshot for follower catch-up.
	CompactionOverhead uint64 `yaml:"compaction_overhead"`

	// MaxInMemLogSize limits the in-memory Raft log size per shard
	// in bytes. 0 uses the default.
	MaxInMemLogSize uint64 `yaml:"max_in_mem_log_size"`

	// MaxTotalInMemLogSize limits the total in-memory Raft log size
	// across all shards in bytes. 0 uses the default.
	MaxTotalInMemLogSize uint64 `yaml:"max_total_in_mem_log_size"`

	// SMType selects the state machine backend: "pebble" or "memory".
	SMType string `yaml:"sm_type"`
}

// StorageConfig configures the storage backend.
type StorageConfig struct {
	// Backend selects the storage engine: "pebble" or "memory".
	Backend string `yaml:"backend"`

	// CacheSize is the PebbleDB block cache size in bytes.
	// 0 uses the PebbleDB default.
	CacheSize int64 `yaml:"cache_size"`

	// WALDir sets a separate directory for the PebbleDB WAL.
	// Empty uses the default data directory.
	WALDir string `yaml:"wal_dir"`

	// MaxOpenFiles limits the number of open files for PebbleDB.
	// 0 uses the PebbleDB default.
	MaxOpenFiles int `yaml:"max_open_files"`
}

// TLSConfig configures TLS for all server protocols.
type TLSConfig struct {
	// Enabled controls whether TLS is active.
	Enabled bool `yaml:"enabled"`

	// CAFile is the path to the CA certificate for client verification.
	// If empty and ClientAuth requires verification, the OS trust store is used.
	CAFile string `yaml:"ca_file"`

	// CertFile is the path to the server certificate.
	CertFile string `yaml:"cert_file"`

	// KeyFile is the path to the server private key.
	KeyFile string `yaml:"key_file"`

	// ClientAuth specifies the client authentication mode.
	// Valid values: "none", "request", "require", "require_and_verify" (default).
	ClientAuth string `yaml:"client_auth"`

	// MinVersion specifies the minimum TLS version. Default is "TLS1.3".
	// Only "TLS1.2" and "TLS1.3" are valid values.
	MinVersion string `yaml:"min_version"`
}

// CryptoTLSClientAuth converts the ClientAuth string to tls.ClientAuthType.
func (c *TLSConfig) CryptoTLSClientAuth() tls.ClientAuthType {
	v, ok := clientAuthModes[c.ClientAuth]
	if !ok {
		return tls.RequireAndVerifyClientCert
	}
	return v
}

// CryptoTLSMinVersion converts the MinVersion string to a uint16 TLS version.
func (c *TLSConfig) CryptoTLSMinVersion() uint16 {
	v, ok := tlsMinVersions[c.MinVersion]
	if !ok {
		return tls.VersionTLS13
	}
	return v
}

// clientAuthModes maps configuration string values to tls.ClientAuthType.
var clientAuthModes = map[string]tls.ClientAuthType{
	"none":               tls.NoClientCert,
	"request":            tls.RequestClientCert,
	"require":            tls.RequireAnyClientCert,
	"require_and_verify": tls.RequireAndVerifyClientCert,
}

// tlsMinVersions maps configuration string values to TLS version constants.
var tlsMinVersions = map[string]uint16{
	"TLS1.2": tls.VersionTLS12,
	"TLS1.3": tls.VersionTLS13,
}

// validClientAuthModes contains the accepted client auth mode strings.
var validClientAuthModes = map[string]struct{}{
	"none":               {},
	"request":            {},
	"require":            {},
	"require_and_verify": {},
}

// validTLSMinVersions contains the accepted TLS minimum version strings.
var validTLSMinVersions = map[string]struct{}{
	"TLS1.2": {},
	"TLS1.3": {},
}

// UnixConfig configures the Unix domain socket server.
type UnixConfig struct {
	// SocketPath is the path to the Unix domain socket.
	// Default: /var/run/qrdbd/qrdbd.sock
	SocketPath string `yaml:"socket_path"`
}

// MCPConfig configures the Model Context Protocol server.
type MCPConfig struct {
	// Port is the TCP port for the MCP HTTP server.
	Port int `yaml:"port"`

	// Endpoint is the HTTP path for MCP requests.
	// Default: /mcp
	Endpoint string `yaml:"endpoint"`
}

// LoggingConfig configures structured logging.
type LoggingConfig struct {
	// Level is the minimum log level: "debug", "info", "warn", "error".
	Level string `yaml:"level"`

	// Format is the log output format: "text" or "json".
	Format string `yaml:"format"`
}

// DiscoveryConfig configures peer discovery.
type DiscoveryConfig struct {
	// Method selects the discovery mechanism: "static", "multicast", or "dns".
	Method string `yaml:"method"`

	// MulticastSecret is the shared secret for multicast discovery
	// authentication.
	MulticastSecret string `yaml:"multicast_secret"`

	// DNSDomain is the DNS domain used for DNS-based peer discovery.
	DNSDomain string `yaml:"dns_domain"`
}

// MVCCConfig configures the MVCC versioned key-value engine.
type MVCCConfig struct {
	// CompactionRetention is the minimum number of revisions to retain
	// after compaction. Zero disables automatic compaction retention.
	CompactionRetention int64 `yaml:"compaction_retention"`

	// RevisionLimit is the maximum number of historical revisions to
	// retain per key. Zero means unlimited.
	RevisionLimit int64 `yaml:"revision_limit"`
}

// WatchConfig configures the real-time watch notification system.
type WatchConfig struct {
	// BufferSize is the per-watcher event channel buffer size. Larger
	// values reduce back-pressure at the cost of memory.
	BufferSize int `yaml:"buffer_size"`

	// MaxWatchers is the maximum number of concurrent watchers allowed.
	// Zero means unlimited.
	MaxWatchers int `yaml:"max_watchers"`
}

// LeaseConfig configures the distributed lease management system.
type LeaseConfig struct {
	// MinTTL is the minimum lease TTL in seconds that the server
	// will accept. Requests for shorter TTLs are clamped to this value.
	MinTTL int64 `yaml:"min_ttl"`

	// MaxTTL is the maximum lease TTL in seconds that the server
	// will accept. Requests for longer TTLs are clamped to this value.
	// Zero means unlimited.
	MaxTTL int64 `yaml:"max_ttl"`

	// CheckInterval is the interval in seconds between lease expiry
	// checks. Lower values improve expiry accuracy at the cost of
	// CPU usage.
	CheckInterval int64 `yaml:"check_interval"`
}

// IndexConfig configures the secondary indexing system.
type IndexConfig struct {
	// MaxIndexes is the maximum number of secondary indexes allowed.
	// Zero means unlimited.
	MaxIndexes int `yaml:"max_indexes"`
}

// TimeSeriesConfig configures the time-series storage engine.
type TimeSeriesConfig struct {
	// DefaultRetention is the default data retention period in seconds.
	// Data points older than this are eligible for automatic deletion.
	// Zero disables automatic retention.
	DefaultRetention int64 `yaml:"default_retention"`

	// MaxMetrics is the maximum number of distinct metric names allowed.
	// Zero means unlimited.
	MaxMetrics int `yaml:"max_metrics"`
}

// WriteModeConfig configures the default write durability mode and batch
// aggregator settings. Write modes control the tradeoff between write
// latency, throughput, and durability guarantees.
type WriteModeConfig struct {
	// Default is the default write mode: "sync", "wal_durable", or "batched".
	// Empty defaults to "sync".
	Default string `yaml:"default"`

	// Batch configures the batch aggregator used when the write mode
	// is "batched".
	Batch BatchConfig `yaml:"batch"`
}

// BatchConfig configures the batch aggregator's grouping behavior.
type BatchConfig struct {
	// MaxSize is the maximum number of operations per batch before
	// a flush is triggered. Default: 1000.
	MaxSize int `yaml:"max_size"`

	// FlushIntervalMS is the maximum time in milliseconds before a
	// batch is flushed, even if it has not reached MaxSize. Default: 50.
	FlushIntervalMS int `yaml:"flush_interval_ms"`

	// QueueSize is the buffered channel size for pending proposals.
	// Default: 10000.
	QueueSize int `yaml:"queue_size"`
}

// DefaultConfig returns a Config populated with production-ready defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			AdminPort: 63002,
			GRPCPort:  63003,
			Host:      "0.0.0.0",
		},
		Protocols: ProtocolsConfig{
			REST: true,
			GRPC: true,
		},
		Raft: RaftConfig{
			DataDir:      "./data",
			DeploymentID: 1,
			ShardID:      1,
			ReplicaID:    1,
			RaftAddress:  "127.0.0.1:63001",
			Peers:        "1=127.0.0.1:63001",
			SMType:       "pebble",
		},
		Storage: StorageConfig{
			Backend: "pebble",
		},
		TLS: TLSConfig{
			Enabled:    true,
			ClientAuth: "require_and_verify",
			MinVersion: "TLS1.3",
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
			Method:          "multicast",
			MulticastSecret: "quicraft",
		},
		MVCC: MVCCConfig{
			CompactionRetention: 1000,
		},
		Watch: WatchConfig{
			BufferSize: 1024,
		},
		Lease: LeaseConfig{
			MinTTL:        5,
			MaxTTL:        0,
			CheckInterval: 1,
		},
		WriteMode: WriteModeConfig{
			Default: "sync",
			Batch: BatchConfig{
				MaxSize:         1000,
				FlushIntervalMS: 50,
				QueueSize:       10000,
			},
		},
	}
}

// Load reads and parses a YAML configuration file from the given path.
// The file must contain valid YAML that maps to the Config struct.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validLogLevels contains the accepted logging level strings.
var validLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

// validLogFormats contains the accepted logging format strings.
var validLogFormats = map[string]struct{}{
	"text": {},
	"json": {},
}

// validSMTypes contains the accepted state machine type strings.
var validSMTypes = map[string]struct{}{
	"pebble": {},
	"memory": {},
	"mvcc":   {},
}

// validBackends contains the accepted storage backend strings.
var validBackends = map[string]struct{}{
	"pebble": {},
	"memory": {},
}

// validWriteModes contains the accepted write mode strings.
var validWriteModes = map[string]struct{}{
	"sync":        {},
	"wal_durable": {},
	"batched":     {},
}

// validDiscoveryMethods contains the accepted discovery method strings.
var validDiscoveryMethods = map[string]struct{}{
	"static":    {},
	"multicast": {},
	"dns":       {},
}

// Validate checks the configuration for required fields and valid values.
// It returns the first validation error encountered, or nil if the
// configuration is valid.
func (c *Config) Validate() error {
	if c.Server.AdminPort <= 0 || c.Server.AdminPort > 65535 {
		return &ValidationError{Field: "server.admin_port", Reason: "must be between 1 and 65535"}
	}
	if c.Server.GRPCPort < 0 || c.Server.GRPCPort > 65535 {
		return &ValidationError{Field: "server.grpc_port", Reason: "must be between 0 and 65535"}
	}
	if c.Server.Host == "" {
		return &ValidationError{Field: "server.host", Reason: "must not be empty"}
	}

	if c.Raft.DataDir == "" {
		return &ValidationError{Field: "raft.data_dir", Reason: "must not be empty"}
	}
	if c.Raft.ShardID == 0 {
		return &ValidationError{Field: "raft.shard_id", Reason: "must be greater than 0"}
	}
	if c.Raft.ReplicaID == 0 {
		return &ValidationError{Field: "raft.replica_id", Reason: "must be greater than 0"}
	}
	if c.Raft.RaftAddress == "" {
		return &ValidationError{Field: "raft.raft_address", Reason: "must not be empty"}
	}
	if c.Raft.SMType != "" {
		if _, ok := validSMTypes[c.Raft.SMType]; !ok {
			return &ValidationError{Field: "raft.sm_type", Reason: "must be one of: pebble, memory, mvcc"}
		}
	}

	if c.Storage.Backend != "" {
		if _, ok := validBackends[c.Storage.Backend]; !ok {
			return &ValidationError{Field: "storage.backend", Reason: "must be one of: pebble, memory"}
		}
	}
	if c.Storage.CacheSize < 0 {
		return &ValidationError{Field: "storage.cache_size", Reason: "must not be negative"}
	}
	if c.Storage.MaxOpenFiles < 0 {
		return &ValidationError{Field: "storage.max_open_files", Reason: "must not be negative"}
	}

	// TLS validation.
	if c.TLS.Enabled {
		if c.TLS.CertFile == "" {
			return &ValidationError{Field: "tls.cert_file", Reason: "required when tls is enabled"}
		}
		if c.TLS.KeyFile == "" {
			return &ValidationError{Field: "tls.key_file", Reason: "required when tls is enabled"}
		}
	}
	if c.TLS.ClientAuth != "" {
		if _, ok := validClientAuthModes[c.TLS.ClientAuth]; !ok {
			return &ValidationError{Field: "tls.client_auth", Reason: "must be one of: none, request, require, require_and_verify"}
		}
	}
	if c.TLS.MinVersion != "" {
		if _, ok := validTLSMinVersions[c.TLS.MinVersion]; !ok {
			return &ValidationError{Field: "tls.min_version", Reason: "must be one of: TLS1.2, TLS1.3"}
		}
	}

	// Unix socket validation.
	if c.Protocols.Unix {
		if c.Unix.SocketPath == "" {
			return &ValidationError{Field: "unix.socket_path", Reason: "must not be empty when unix protocol is enabled"}
		}
	}

	// MCP validation.
	if c.MCP.Port < 0 || c.MCP.Port > 65535 {
		return &ValidationError{Field: "mcp.port", Reason: "must be between 0 and 65535"}
	}
	if c.MCP.Endpoint != "" && !strings.HasPrefix(c.MCP.Endpoint, "/") {
		return &ValidationError{Field: "mcp.endpoint", Reason: "must start with /"}
	}

	if c.Logging.Level != "" {
		if _, ok := validLogLevels[c.Logging.Level]; !ok {
			return &ValidationError{Field: "logging.level", Reason: "must be one of: debug, info, warn, error"}
		}
	}
	if c.Logging.Format != "" {
		if _, ok := validLogFormats[c.Logging.Format]; !ok {
			return &ValidationError{Field: "logging.format", Reason: "must be one of: text, json"}
		}
	}

	if c.Discovery.Method != "" {
		if _, ok := validDiscoveryMethods[c.Discovery.Method]; !ok {
			return &ValidationError{Field: "discovery.method", Reason: "must be one of: static, multicast, dns"}
		}
	}

	// MVCC validation.
	if c.MVCC.CompactionRetention < 0 {
		return &ValidationError{Field: "mvcc.compaction_retention", Reason: "must not be negative"}
	}
	if c.MVCC.RevisionLimit < 0 {
		return &ValidationError{Field: "mvcc.revision_limit", Reason: "must not be negative"}
	}

	// Watch validation.
	if c.Watch.BufferSize < 0 {
		return &ValidationError{Field: "watch.buffer_size", Reason: "must not be negative"}
	}
	if c.Watch.MaxWatchers < 0 {
		return &ValidationError{Field: "watch.max_watchers", Reason: "must not be negative"}
	}

	// Lease validation.
	if c.Lease.MinTTL < 0 {
		return &ValidationError{Field: "lease.min_ttl", Reason: "must not be negative"}
	}
	if c.Lease.MaxTTL < 0 {
		return &ValidationError{Field: "lease.max_ttl", Reason: "must not be negative"}
	}
	if c.Lease.MaxTTL > 0 && c.Lease.MinTTL > c.Lease.MaxTTL {
		return &ValidationError{Field: "lease.min_ttl", Reason: "must not exceed lease.max_ttl"}
	}
	if c.Lease.CheckInterval < 0 {
		return &ValidationError{Field: "lease.check_interval", Reason: "must not be negative"}
	}

	// Index validation.
	if c.Index.MaxIndexes < 0 {
		return &ValidationError{Field: "index.max_indexes", Reason: "must not be negative"}
	}

	// TimeSeries validation.
	if c.TimeSeries.DefaultRetention < 0 {
		return &ValidationError{Field: "timeseries.default_retention", Reason: "must not be negative"}
	}
	if c.TimeSeries.MaxMetrics < 0 {
		return &ValidationError{Field: "timeseries.max_metrics", Reason: "must not be negative"}
	}

	// WriteMode validation.
	if c.WriteMode.Default != "" {
		if _, ok := validWriteModes[c.WriteMode.Default]; !ok {
			return &ValidationError{Field: "write_mode.default", Reason: "must be one of: sync, wal_durable, batched"}
		}
	}
	if c.WriteMode.Batch.MaxSize < 0 {
		return &ValidationError{Field: "write_mode.batch.max_size", Reason: "must not be negative"}
	}
	if c.WriteMode.Batch.FlushIntervalMS < 0 {
		return &ValidationError{Field: "write_mode.batch.flush_interval_ms", Reason: "must not be negative"}
	}
	if c.WriteMode.Batch.QueueSize < 0 {
		return &ValidationError{Field: "write_mode.batch.queue_size", Reason: "must not be negative"}
	}

	return nil
}
