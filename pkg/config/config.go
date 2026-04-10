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
	"time"

	"gopkg.in/yaml.v3"
)

// parseDuration parses a Go duration string and returns the duration.
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

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
	Auth       AuthConfig       `yaml:"auth"`
	Audit      AuditConfig      `yaml:"audit"`
	CORS       CORSConfig       `yaml:"cors"`
	SQL        SQLConfig        `yaml:"sql"`
	Codec      CodecConfig      `yaml:"codec"`
	Seal       SealConfig       `yaml:"seal"`
	Bootstrap  BootstrapConfig  `yaml:"bootstrap"`
	Databases  []DatabaseConfig `yaml:"databases,omitempty" json:"databases,omitempty"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	RAG        RAGConfig        `yaml:"rag"`
}

// CodecConfig configures the dual-codec system for storage and wire encoding.
type CodecConfig struct {
	// StorageCodec selects the encoding format for storage operations.
	// Valid values: "msgpack" (default), "json", "yaml", "protobuf".
	StorageCodec string `yaml:"storage_codec"`

	// WireCodec selects the encoding format for API wire responses.
	// Valid values: "json" (default), "msgpack", "yaml", "protobuf".
	WireCodec string `yaml:"wire_codec"`

	// UseOptimized enables sync.Pool buffer reuse for zero-alloc hot paths.
	UseOptimized bool `yaml:"use_optimized"`
}

// DatabaseConfig defines a single database instance in the configuration.
// Engine and Backend are validated as plain strings to avoid circular imports
// with the database package.
type DatabaseConfig struct {
	// Name is the unique identifier for the database.
	Name string `yaml:"name" json:"name"`

	// Engine selects the database engine: "kv", "timeseries", or "vector".
	Engine string `yaml:"engine" json:"engine"`

	// Backend selects the storage backend: "memory", "file", "pebble", or "raft".
	Backend string `yaml:"backend" json:"backend"`

	// MySQL enables the MySQL wire protocol for this database.
	MySQL bool `yaml:"mysql,omitempty" json:"mysql,omitempty"`

	// PostgreSQL enables the PostgreSQL wire protocol for this database.
	PostgreSQL bool `yaml:"postgresql,omitempty" json:"postgresql,omitempty"`
}

// ServerConfig configures the QRDB server's network listeners.
type ServerConfig struct {
	// AdminPort is the HTTP port for admin/REST API endpoints.
	AdminPort int `yaml:"admin_port"`

	// GRPCPort is the port for the gRPC data-plane service.
	GRPCPort int `yaml:"grpc_port"`

	// QUICPort is the port for the native binary QUIC transport.
	QUICPort int `yaml:"quic_port"`

	// MySQLPort is the port for the MySQL wire protocol server.
	MySQLPort int `yaml:"mysql_port"`

	// PgSQLPort is the port for the PostgreSQL wire protocol server.
	PgSQLPort int `yaml:"pgsql_port"`

	// Host is the bind address for all listeners.
	Host string `yaml:"host"`
}

// ProtocolsConfig controls which API protocols are enabled.
type ProtocolsConfig struct {
	// REST enables the HTTP/REST admin and KV API.
	REST bool `yaml:"rest"`

	// GRPC enables the gRPC KV service.
	GRPC bool `yaml:"grpc"`

	// QUIC enables the native binary QUIC transport.
	QUIC bool `yaml:"quic"`

	// Unix enables the gRPC-over-Unix-socket service.
	Unix bool `yaml:"unix"`

	// MCP enables the Model Context Protocol service.
	MCP bool `yaml:"mcp"`

	// MySQL enables the MySQL wire protocol server.
	MySQL bool `yaml:"mysql"`

	// PostgreSQL enables the PostgreSQL wire protocol server.
	PostgreSQL bool `yaml:"postgresql"`
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

	// SMBackend selects the state machine backend variant when SMType
	// identifies a logical engine (e.g., "kv", "timeseries", "vector").
	// Valid values: "standard", "concurrent", "fast", "disk", "cached-disk",
	// or empty (which means use SMType directly for registry lookup).
	SMBackend string `yaml:"sm_backend"`

	// LeaseRead enables lease-based linearizable reads. When true, the Raft
	// leader serves ReadIndex requests immediately when its leadership lease
	// is still valid, without a heartbeat round-trip. Requires CheckQuorum.
	// Default: true (nil = true).
	LeaseRead *bool `yaml:"lease_read"`

	// CheckQuorum enables the leader to step down if it cannot reach a
	// quorum within an election timeout. Required when LeaseRead is enabled.
	// Default: true (nil = true).
	CheckQuorum *bool `yaml:"check_quorum"`
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

// AuthConfig configures the passwordless mTLS authentication and
// RBAC authorization system.
type AuthConfig struct {
	// Enabled controls whether authentication and authorization are active.
	// Default: true.
	Enabled bool `yaml:"enabled"`

	// AdminUsername is the certificate CN for the bootstrap admin user.
	// Default: "admin".
	AdminUsername string `yaml:"admin_username"`

	// DefaultTenant is the certificate OU for the default tenant.
	// Default: "default".
	DefaultTenant string `yaml:"default_tenant"`

	// CertTTL is the default validity duration for generated client
	// certificates. Parsed as a Go duration string (e.g., "8760h").
	// Default: "8760h" (1 year).
	CertTTL string `yaml:"cert_ttl"`

	// PermCacheTTL is the permission cache time-to-live in seconds.
	// Cached permission sets are evicted after this duration.
	// Default: 30.
	PermCacheTTL int `yaml:"perm_cache_ttl"`
}

// AuditConfig configures the dual-backend audit logging system.
type AuditConfig struct {
	// Enabled controls whether audit logging is active.
	// Default: true.
	Enabled bool `yaml:"enabled"`

	// KVBackend enables the KVStore audit backend. Audit entries are
	// stored at __sys:audit/{tenantID}/{timestamp_nano}.
	// Default: false.
	KVBackend bool `yaml:"kv_backend"`

	// FileBackend enables the file-based audit backend using slog
	// JSON format.
	// Default: true.
	FileBackend bool `yaml:"file_backend"`

	// FilePath is the path to the audit log file. When empty, defaults
	// to "{dataDir}/audit.log".
	FilePath string `yaml:"file_path"`
}

// CORSConfig configures Cross-Origin Resource Sharing for the REST API,
// enabling browser-based access.
type CORSConfig struct {
	// AllowedOrigins is the list of allowed request origins.
	// Default: ["*"].
	AllowedOrigins []string `yaml:"allowed_origins"`

	// AllowedHeaders is the list of allowed request headers.
	// Default: ["Authorization", "Content-Type"].
	AllowedHeaders []string `yaml:"allowed_headers"`

	// MaxAge is the maximum age (in seconds) for preflight cache.
	// Default: 3600.
	MaxAge int `yaml:"max_age"`
}

// SQLConfig configures the SQL wire protocol layer shared by both
// MySQL and PostgreSQL protocol servers.
type SQLConfig struct {
	// DefaultDatabase is the database name reported to SQL clients.
	// Default: "qrdb".
	DefaultDatabase string `yaml:"default_database"`

	// MaxConnections is the maximum number of concurrent SQL connections
	// across all SQL protocol servers. Zero means unlimited.
	MaxConnections int `yaml:"max_connections"`

	// QueryTimeout is the maximum duration for a single SQL query.
	// Parsed as a Go duration string. Default: "30s".
	QueryTimeout string `yaml:"query_timeout"`
}

// BootstrapConfig configures TLS bootstrap for cluster enrollment.
// Joining nodes use these settings to securely obtain the cluster CA
// certificate from an existing node before TLS is established.
type BootstrapConfig struct {
	// NoiseKey is the hex-encoded Curve25519 public key of the Noise
	// bootstrap server on the first node. Required for Noise method.
	NoiseKey string `yaml:"noise_key"`

	// NoiseAddr is the host:port of the Noise bootstrap server.
	NoiseAddr string `yaml:"noise_addr"`

	// NoisePort is the TCP port for the Noise bootstrap server
	// (first node only). Default: 63006.
	NoisePort int `yaml:"noise_port"`

	// SPKIPin is the hex-encoded SHA-256 SPKI pin of the bootstrap
	// server's TLS certificate. Required for SPKI method.
	SPKIPin string `yaml:"spki_pin"`

	// DANEDNS is the DNS server address for DANE TLSA lookup
	// (e.g., "8.8.8.8:53"). Required for DANE method.
	DANEDNS string `yaml:"dane_dns"`

	// DANEDoT enables DNS-over-TLS for DANE lookups.
	DANEDoT bool `yaml:"dane_dot"`

	// DirectURL is the full HTTPS URL for direct CA bundle download.
	// Required for Direct method. Example: "https://10.0.0.1:63002/v1/ca/bootstrap".
	DirectURL string `yaml:"direct_url"`

	// Addr is the host:port of the first node's admin server. Used
	// by SPKI and DANE methods to construct the bootstrap server URL.
	Addr string `yaml:"addr"`
}

// SealConfig configures the cryptographic barrier that encrypts data at rest.
type SealConfig struct {
	// Enabled controls whether the seal barrier is active.
	// When false (default), data is stored unencrypted.
	Enabled bool `yaml:"enabled"`

	// Strategy selects the sealing strategy: "software" or "shamir".
	// Default: "software".
	Strategy string `yaml:"strategy"`

	// RootKeyPath is the storage key under which the sealed root key
	// blob is persisted. Default: "__sys:seal/root_key".
	RootKeyPath string `yaml:"root_key_path"`

	// ShamirThreshold is the minimum shares required to unseal when
	// strategy is "shamir". Must be >= 2 and <= ShamirTotal.
	ShamirThreshold int `yaml:"shamir_threshold"`

	// ShamirTotal is the total number of Shamir shares to generate.
	ShamirTotal int `yaml:"shamir_total"`

	// CredentialStrategy selects the credential storage strategy:
	// "manual" or "barrier". Default: "barrier".
	CredentialStrategy string `yaml:"credential_strategy"`
}

// validSealStrategies contains the accepted seal strategy strings.
var validSealStrategies = map[string]struct{}{
	"software": {},
	"shamir":   {},
}

// validCredentialStrategies contains the accepted credential strategy strings.
var validCredentialStrategies = map[string]struct{}{
	"manual":  {},
	"barrier": {},
}

// DefaultConfig returns a Config populated with production-ready defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			AdminPort: 63002,
			GRPCPort:  63003,
			QUICPort:  63004,
			MySQLPort: 3306,
			PgSQLPort: 5432,
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
		Auth: AuthConfig{
			Enabled:       true,
			AdminUsername: "admin",
			DefaultTenant: "default",
			CertTTL:       "8760h",
			PermCacheTTL:  30,
		},
		Audit: AuditConfig{
			Enabled:     true,
			KVBackend:   false,
			FileBackend: true,
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
			AllowedHeaders: []string{"Authorization", "Content-Type"},
			MaxAge:         3600,
		},
		SQL: SQLConfig{
			DefaultDatabase: "qrdb",
			MaxConnections:  100,
			QueryTimeout:    "30s",
		},
		Codec: CodecConfig{
			StorageCodec: "msgpack",
			WireCodec:    "json",
			UseOptimized: true,
		},
		Seal: SealConfig{
			Enabled:            false,
			Strategy:           "software",
			RootKeyPath:        "__sys:seal/root_key",
			CredentialStrategy: "barrier",
		},
		Bootstrap: BootstrapConfig{
			NoisePort: 63006,
		},
		Databases: []DatabaseConfig{
			{Name: "default", Engine: "kv", Backend: "pebble"},
		},
		RAG: RAGConfig{
			DefaultChunkStrategy: "fixed",
			DefaultChunkSize:     512,
			DefaultOverlap:       50,
			MaxDocumentSize:      0,
			DefaultTopK:          5,
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

	// Backward compatibility: synthesize a default database entry when
	// the YAML omits the databases section entirely. The backend is
	// inherited from Storage.Backend, falling back to "pebble".
	if len(cfg.Databases) == 0 {
		backend := cfg.Storage.Backend
		if backend == "" {
			backend = "pebble"
		}
		cfg.Databases = []DatabaseConfig{
			{Name: "default", Engine: "kv", Backend: backend},
		}
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
	"pebble":      {},
	"memory":      {},
	"mvcc":        {},
	"kv":          {},
	"timeseries":  {},
	"vector":      {},
	"standard":    {},
	"concurrent":  {},
	"fast":        {},
	"disk":        {},
	"cached-disk": {},
}

// validSMBackends contains the accepted state machine backend strings.
var validSMBackends = map[string]struct{}{
	"standard":    {},
	"concurrent":  {},
	"fast":        {},
	"disk":        {},
	"cached-disk": {},
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

// validDatabaseEngines contains the accepted database engine strings.
var validDatabaseEngines = map[string]struct{}{
	"kv":         {},
	"timeseries": {},
	"vector":     {},
}

// validCodecTypes contains the accepted codec type strings.
var validCodecTypes = map[string]struct{}{
	"json":     {},
	"msgpack":  {},
	"yaml":     {},
	"protobuf": {},
}

// validDatabaseBackends contains the accepted database backend strings.
var validDatabaseBackends = map[string]struct{}{
	"memory": {},
	"file":   {},
	"pebble": {},
	"raft":   {},
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
	if c.Server.QUICPort < 0 || c.Server.QUICPort > 65535 {
		return &ValidationError{Field: "server.quic_port", Reason: "must be between 0 and 65535"}
	}
	if c.Server.MySQLPort < 0 || c.Server.MySQLPort > 65535 {
		return &ValidationError{Field: "server.mysql_port", Reason: "must be between 0 and 65535"}
	}
	if c.Server.PgSQLPort < 0 || c.Server.PgSQLPort > 65535 {
		return &ValidationError{Field: "server.pgsql_port", Reason: "must be between 0 and 65535"}
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
			return &ValidationError{
				Field:  "raft.sm_type",
				Reason: "must be one of: pebble, memory, mvcc, kv, timeseries, vector, standard, concurrent, fast, disk, cached-disk",
			}
		}
	}
	if c.Raft.SMBackend != "" {
		if _, ok := validSMBackends[c.Raft.SMBackend]; !ok {
			return &ValidationError{
				Field:  "raft.sm_backend",
				Reason: "must be one of: standard, concurrent, fast, disk, cached-disk",
			}
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

	// TLS validation: cert/key files are not required because the daemon
	// auto-generates them via resolveTLS when omitted. Only validate
	// optional fields when they are explicitly provided.
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

	// Auth validation.
	if c.Auth.Enabled {
		if c.Auth.AdminUsername == "" {
			return &ValidationError{Field: "auth.admin_username", Reason: "must not be empty when auth is enabled"}
		}
		if c.Auth.DefaultTenant == "" {
			return &ValidationError{Field: "auth.default_tenant", Reason: "must not be empty when auth is enabled"}
		}
		if c.Auth.CertTTL != "" {
			if _, err := parseDuration(c.Auth.CertTTL); err != nil {
				return &ValidationError{Field: "auth.cert_ttl", Reason: "must be a valid Go duration string"}
			}
		}
	}
	if c.Auth.PermCacheTTL < 0 {
		return &ValidationError{Field: "auth.perm_cache_ttl", Reason: "must not be negative"}
	}

	// Audit validation.
	if c.Audit.Enabled && !c.Audit.KVBackend && !c.Audit.FileBackend {
		return &ValidationError{Field: "audit", Reason: "at least one backend must be enabled when audit is enabled"}
	}

	// CORS validation.
	if c.CORS.MaxAge < 0 {
		return &ValidationError{Field: "cors.max_age", Reason: "must not be negative"}
	}

	// SQL validation.
	if c.SQL.MaxConnections < 0 {
		return &ValidationError{Field: "sql.max_connections", Reason: "must not be negative"}
	}
	if c.SQL.QueryTimeout != "" {
		if _, err := parseDuration(c.SQL.QueryTimeout); err != nil {
			return &ValidationError{Field: "sql.query_timeout", Reason: "must be a valid Go duration string"}
		}
	}

	// SQL protocols require TLS and auth.
	if c.Protocols.MySQL || c.Protocols.PostgreSQL {
		if !c.TLS.Enabled {
			return &ValidationError{
				Field:  "tls.enabled",
				Reason: "TLS must be enabled when MySQL or PostgreSQL protocols are active",
			}
		}
		if !c.Auth.Enabled {
			return &ValidationError{
				Field:  "auth.enabled",
				Reason: "auth must be enabled when MySQL or PostgreSQL protocols are active",
			}
		}
		if c.SQL.DefaultDatabase == "" {
			return &ValidationError{
				Field:  "sql.default_database",
				Reason: "must not be empty when SQL protocols are enabled",
			}
		}
	}

	// Codec validation.
	if c.Codec.StorageCodec != "" {
		if _, ok := validCodecTypes[c.Codec.StorageCodec]; !ok {
			return &ValidationError{Field: "codec.storage_codec", Reason: "must be one of: json, msgpack, yaml, protobuf"}
		}
	}
	if c.Codec.WireCodec != "" {
		if _, ok := validCodecTypes[c.Codec.WireCodec]; !ok {
			return &ValidationError{Field: "codec.wire_codec", Reason: "must be one of: json, msgpack, yaml, protobuf"}
		}
	}

	// Seal validation.
	if c.Seal.Enabled {
		if c.Seal.Strategy != "" {
			if _, ok := validSealStrategies[c.Seal.Strategy]; !ok {
				return &ValidationError{Field: "seal.strategy", Reason: "must be one of: software, shamir"}
			}
		}
		if c.Seal.Strategy == "shamir" {
			if c.Seal.ShamirThreshold < 2 {
				return &ValidationError{Field: "seal.shamir_threshold", Reason: "must be >= 2 for shamir strategy"}
			}
			if c.Seal.ShamirTotal < c.Seal.ShamirThreshold {
				return &ValidationError{Field: "seal.shamir_total", Reason: "must be >= shamir_threshold"}
			}
		}
		if c.Seal.CredentialStrategy != "" {
			if _, ok := validCredentialStrategies[c.Seal.CredentialStrategy]; !ok {
				return &ValidationError{Field: "seal.credential_strategy", Reason: "must be one of: manual, barrier"}
			}
		}
	}

	// Database validation.
	seen := make(map[string]struct{}, len(c.Databases))
	for _, db := range c.Databases {
		if db.Name == "" {
			return &DatabaseConfigError{Name: "", Message: "name must not be empty"}
		}
		if _, ok := validDatabaseEngines[db.Engine]; !ok {
			return &DatabaseConfigError{Name: db.Name, Message: "engine must be one of: kv, timeseries, vector"}
		}
		if _, ok := validDatabaseBackends[db.Backend]; !ok {
			return &DatabaseConfigError{Name: db.Name, Message: "backend must be one of: memory, file, pebble, raft"}
		}
		if _, dup := seen[db.Name]; dup {
			return &DatabaseConfigError{Name: db.Name, Message: "duplicate database name"}
		}
		seen[db.Name] = struct{}{}
	}

	// Bootstrap validation.
	if c.Bootstrap.NoisePort < 0 || c.Bootstrap.NoisePort > 65535 {
		return &ValidationError{Field: "bootstrap.noise_port", Reason: "must be between 0 and 65535"}
	}

	return nil
}

// EmbeddingConfig configures the embedding service for vector generation.
// Providers listed here are registered on service startup and made available
// for use by the REST, gRPC, and MCP API layers.
type EmbeddingConfig struct {
	// DefaultProvider is the name of the provider to use when none is specified
	// in a request. Must match one of the names in Providers.
	DefaultProvider string `yaml:"default_provider"`

	// CacheCapacity is the maximum number of embeddings to hold in the
	// in-memory LRU cache. Set to 0 to disable caching entirely.
	CacheCapacity int `yaml:"cache_capacity"`

	// Providers is the list of configured embedding provider endpoints.
	Providers []EmbeddingProviderConfig `yaml:"providers"`
}

// RAGConfig configures the Retrieval-Augmented Generation pipeline.
type RAGConfig struct {
	// DefaultChunkStrategy selects the default chunking algorithm.
	// Valid values: "fixed", "sentence", "paragraph".
	DefaultChunkStrategy string `yaml:"default_chunk_strategy"`

	// DefaultChunkSize is the target chunk size in characters.
	DefaultChunkSize int `yaml:"default_chunk_size"`

	// DefaultOverlap is the number of overlapping characters between adjacent
	// chunks when using the fixed chunking strategy.
	DefaultOverlap int `yaml:"default_overlap"`

	// MaxDocumentSize is the maximum allowed document byte length.
	// Zero means unlimited.
	MaxDocumentSize int64 `yaml:"max_document_size"`

	// DefaultTopK is the maximum number of retrieval results returned when
	// the caller does not specify a TopK value.
	DefaultTopK int `yaml:"default_top_k"`
}

// EmbeddingProviderConfig defines a single embedding provider endpoint.
type EmbeddingProviderConfig struct {
	// Name is the unique identifier for this provider within the registry.
	Name string `yaml:"name"`

	// BaseURL is the OpenAI-compatible API base URL, e.g.
	// "http://localhost:11434/v1" for a local Ollama instance.
	BaseURL string `yaml:"base_url"`

	// APIKey is the bearer token for authentication. Leave empty for
	// providers that do not require authentication (e.g. local Ollama).
	APIKey string `yaml:"api_key"`

	// Model is the default embedding model for this provider. Used when the
	// caller does not specify a model in the request.
	Model string `yaml:"model"`

	// Timeout is the per-request timeout in Go duration format (e.g. "30s").
	// Defaults to 30s when empty.
	Timeout string `yaml:"timeout"`

	// MaxRetries is the maximum number of retry attempts for transient
	// failures (5xx responses and network errors). Defaults to 3.
	MaxRetries int `yaml:"max_retries"`
}
