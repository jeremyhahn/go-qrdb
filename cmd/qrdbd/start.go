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

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/mvccadapter"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/bootstrap"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/discovery"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/spf13/cobra"
)

// startFlags holds the parsed CLI flags for the start command.
type startFlags struct {
	configFile      string
	listen          string
	raftAddress     string
	dataDir         string
	deploymentID    uint64
	smType          string
	smBackend       string
	discoveryMethod string
	peers           []string
	// shardID is deprecated. System services always use shard.ShardKV (1).
	// Kept for backward compatibility with existing YAML configs and CLI scripts.
	shardID              uint64
	replicaID            uint64
	multicastSecret      string
	dnsDomain            string
	adminPort            int
	grpcPort             int
	maxInMemLogSize      uint64
	maxTotalInMemLogSize uint64
	snapshotEntries      uint64
	compactionOverhead   uint64
	join                 bool
	logFormat            string
	logLevel             string
	debugRaft            bool
	enableREST           bool
	enableGRPC           bool
	enableQUIC           bool
	quicPort             int
	enableUnix           bool
	unixSocketPath       string
	enableMCP            bool
	mcpPort              int
	mcpEndpoint          string
	enableMySQL          bool
	mysqlPort            int
	enablePgSQL          bool
	pgsqlPort            int
	authEnabled          bool
	sqlDatabase          string
	tlsCAFile            string
	tlsCertFile          string
	tlsKeyFile           string
	tlsClientAuth        string
	bootstrapNoiseKey    string
	bootstrapNoiseAddr   string
	bootstrapNoisePort   int
	bootstrapSPKIPin     string
	bootstrapDANEDNS     string
	bootstrapDANEDoT     bool
	bootstrapDirectURL   string
	bootstrapAddr        string
	leaseRead            bool
	checkQuorum          bool

	// databases holds the database configurations loaded from the YAML
	// config file. When no config file is provided, this defaults to a
	// single "default" KV database with a pebble backend.
	databases []qrdbconfig.DatabaseConfig

	// codecConfig holds the dual-codec configuration loaded from YAML.
	// When no config file is provided, defaults from DefaultConfig are used.
	codecConfig qrdbconfig.CodecConfig

	// sealConfig holds the seal barrier configuration loaded from YAML.
	// When no config file is provided, defaults from DefaultConfig are used.
	sealConfig qrdbconfig.SealConfig

	// createFn is an optional pre-built state machine factory. When set,
	// initializeNode uses it instead of resolving from the SM registry.
	// This enables callers to inject subsystem-aware factories (e.g.
	// with Lessor, WatchHub, TimeSeries) that share instances with the
	// service layer.
	createFn sm.CreateFunc
}

// raftNode is the interface that the start command requires for shard
// lifecycle operations. Both *quicraft.Host and test mocks satisfy it.
type raftNode interface {
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg quicraftconfig.Config) error
	GetLeaderID(shardID uint64) (uint64, bool, error)
	Close() error
}

func init() {
	// Register "pebble" and "mvcc" state machine types in the centralized
	// registry. These live in the store package which cannot be imported
	// from statemachine (circular dependency), so they are registered here
	// at program startup.
	statemachine.RegisterWithDescriptor("pebble", func() sm.CreateFunc {
		return sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
			return store.NewPebbleStateMachine(nil)
		})
	}, statemachine.Descriptor{
		Description: "PebbleDB-backed on-disk state machine",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
		},
	})
	statemachine.RegisterWithDescriptor("mvcc", func() sm.CreateFunc {
		return store.NewMVCCCreateFunc()
	}, statemachine.Descriptor{
		Description: "MVCC state machine with transactions, leases, indexes, and time-series",
		Persistence: statemachine.PersistenceDisk,
		Kind:        statemachine.SMKindDisk,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
			statemachine.CapabilityMVCC,
			statemachine.CapabilityTransaction,
			statemachine.CapabilityBatch,
			statemachine.CapabilityLease,
			statemachine.CapabilityIndex,
			statemachine.CapabilityTimeSeries,
			statemachine.CapabilityWatch,
		},
	})

	// Register "mvcc-fast" state machine type. This uses the
	// mvccadapter package to bridge the statemachine.MVCCStore
	// interface to mvcc.RaftStorage, avoiding the import cycle
	// that prevents placing this in the statemachine package.
	statemachine.RegisterWithDescriptor("mvcc-fast", func() sm.CreateFunc {
		return mvccadapter.NewMVCCFastCreateFunc(
			defaultMVCCFastStorageFactory(),
			defaultMVCCFastLogger(),
		)
	}, statemachine.Descriptor{
		Description: "High-performance fast SM with MVCC support, PebbleDB backing",
		Persistence: statemachine.PersistenceHybrid,
		Kind:        statemachine.SMKindRegular,
		Capabilities: []statemachine.Capability{
			statemachine.CapabilityKV,
			statemachine.CapabilityMVCC,
			statemachine.CapabilityTransaction,
		},
	})
}

// defaultMVCCFastStorageFactory returns a storage factory that creates
// PebbleDB storage instances using per-shard temp directories. This
// is suitable for development and testing. Production deployments
// should provide a factory with a configured data directory.
func defaultMVCCFastStorageFactory() func(shardID, replicaID uint64) storage.Storage {
	return func(shardID, replicaID uint64) storage.Storage {
		dir, err := os.MkdirTemp("", fmt.Sprintf("qrdb-mvcc-fast-%d-%d-*", shardID, replicaID))
		if err != nil {
			panic("qrdbd: failed to create temp dir for mvcc-fast storage: " + err.Error())
		}
		cfg := pebblestore.TestConfig(filepath.Join(dir, "data"))
		s, err := pebblestore.New(cfg)
		if err != nil {
			panic("qrdbd: failed to create pebble storage for mvcc-fast: " + err.Error())
		}
		return s
	}
}

// defaultMVCCFastLogger creates a minimal logger for the mvcc-fast
// state machine in the default registry. Production deployments
// should provide a properly configured logger.
func defaultMVCCFastLogger() *logger.Logger {
	log, err := logger.New(logger.LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		panic("qrdbd: failed to create default mvcc-fast logger: " + err.Error())
	}
	return log
}

// defaultVectorLogger creates a minimal logger for the vector service.
// Production deployments should provide a properly configured logger.
func defaultVectorLogger() *logger.Logger {
	log, err := logger.New(logger.LogConfig{
		Level:  "warn",
		Format: "json",
		Output: "stderr",
	})
	if err != nil {
		panic("qrdbd: failed to create default vector logger: " + err.Error())
	}
	return log
}

// discoveryBuilder is a function that builds a discovery.Method from the
// start flags. Each discovery method has its own builder.
type discoveryBuilder func(f *startFlags) (discovery.Method, error)

// discoveryBuilders maps discovery method names to their builder functions.
var discoveryBuilders = map[string]discoveryBuilder{
	"static":    buildStaticDiscovery,
	"multicast": buildMulticastDiscovery,
	"dns":       buildDNSDiscovery,
}

func newStartCmd() *cobra.Command {
	f := &startFlags{}

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a QuicRaft DB node",
		Long: "Start a QuicRaft DB node. Supports two configuration modes:\n" +
			"  1. YAML config file via --config flag\n" +
			"  2. CLI flags (backward compatible)\n" +
			"CLI flags override YAML values when both are provided.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Load YAML config if --config is provided, then let CLI
			// flags override any YAML values that were explicitly set.
			if f.configFile != "" {
				cfg, err := qrdbconfig.Load(f.configFile)
				if err != nil {
					return err
				}
				applyConfigToFlags(cmd, f, cfg)
			}

			// Configure the global logger early so all pre-host log
			// lines (auto-generated replica ID, etc.) use the
			// requested format and level.
			logCfg := quicraftconfig.LogConfig{
				Format: quicraftconfig.LogFormat(f.logFormat),
				Level:  quicraftconfig.LogLevel(f.logLevel),
			}
			logCfg.SetDefaults()
			if f.debugRaft {
				logCfg.Level = quicraftconfig.LogLevelDebug
			}
			if err := logCfg.Validate(); err != nil {
				return err
			}
			quicraftconfig.SetDefaultLogger(logCfg)

			if err := resolveAutoConfig(cmd, f); err != nil {
				return err
			}
			return runStart(f)
		},
	}

	cmd.Flags().StringVar(&f.configFile, "config", "", "Path to YAML configuration file")
	cmd.Flags().StringVar(&f.listen, "listen", "0.0.0.0:63001", "Listen address (host:port)")
	cmd.Flags().StringVar(&f.raftAddress, "raft-address", "127.0.0.1:63001", "Raft address visible to peers")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "./data", "Data directory for WAL and metadata")
	cmd.Flags().Uint64Var(&f.deploymentID, "deployment-id", 1, "Deployment ID for cluster isolation")
	cmd.Flags().StringVar(&f.smType, "sm", "pebble", "State machine type: pebble, memory, concurrent")
	cmd.Flags().StringVar(&f.smBackend, "sm-backend", "", "State machine backend: standard, concurrent, fast, disk, cached-disk")
	cmd.Flags().StringVar(&f.discoveryMethod, "discovery", "multicast", "Discovery method: multicast, static, dns")
	cmd.Flags().StringSliceVar(&f.peers, "peers", nil, "Static peers, format id=address (e.g., 1=127.0.0.1:63001)")
	cmd.Flags().Uint64Var(&f.shardID, "shard-id", shard.ShardKV, "Shard ID (deprecated: system services always use the system shard)")
	cmd.Flags().Uint64Var(&f.replicaID, "replica-id", 1, "Replica ID")
	cmd.Flags().StringVar(&f.multicastSecret, "multicast-secret", "quicraft", "Shared secret for multicast authentication")
	cmd.Flags().StringVar(&f.dnsDomain, "dns-domain", "", "DNS SRV domain for DNS discovery")
	cmd.Flags().IntVar(&f.adminPort, "admin-port", 63002, "Admin HTTP server port")
	cmd.Flags().IntVar(&f.grpcPort, "grpc-port", 63003, "gRPC server port")
	cmd.Flags().Uint64Var(&f.maxInMemLogSize, "max-in-mem-log-size", 0,
		"Per-shard in-memory log size limit in bytes before rate limiting (0=disabled)")
	cmd.Flags().Uint64Var(&f.maxTotalInMemLogSize, "max-total-in-mem-log-size", 0,
		"Global in-memory log size limit across all shards in bytes (0=disabled)")
	cmd.Flags().StringVar(&f.logFormat, "log-format", "text", "Log output format: text, json")
	cmd.Flags().StringVar(&f.logLevel, "log-level", "info", "Minimum log level: debug, info, warn, error")
	cmd.Flags().BoolVar(&f.debugRaft, "debug-raft", false, "Enable verbose Raft protocol logging (sets log level to debug)")
	cmd.Flags().Uint64Var(&f.snapshotEntries, "snapshot-entries", 0,
		"Number of applied entries between automatic snapshots (0=disabled)")
	cmd.Flags().Uint64Var(&f.compactionOverhead, "compaction-overhead", 0,
		"Number of entries to keep after compaction (0=use default 5000)")
	cmd.Flags().BoolVar(&f.join, "join", false,
		"Join an existing cluster (for node replacement)")
	cmd.Flags().BoolVar(&f.enableREST, "enable-rest", true, "Enable REST API server")
	cmd.Flags().BoolVar(&f.enableGRPC, "enable-grpc", true, "Enable gRPC API server")
	cmd.Flags().BoolVar(&f.enableQUIC, "enable-quic", false, "Enable native QUIC transport server")
	cmd.Flags().IntVar(&f.quicPort, "quic-port", 63004, "Native QUIC transport server port")
	cmd.Flags().BoolVar(&f.enableUnix, "enable-unix", false, "Enable Unix socket server")
	cmd.Flags().StringVar(&f.unixSocketPath, "unix-socket-path", "/var/run/qrdbd/qrdbd.sock", "Unix socket file path")
	cmd.Flags().BoolVar(&f.enableMCP, "enable-mcp", false, "Enable MCP server")
	cmd.Flags().IntVar(&f.mcpPort, "mcp-port", 63005, "MCP server port")
	cmd.Flags().StringVar(&f.mcpEndpoint, "mcp-endpoint", "/mcp", "MCP server endpoint path")
	cmd.Flags().BoolVar(&f.authEnabled, "auth-enabled", true, "Enable authentication and authorization")
	cmd.Flags().StringVar(&f.tlsClientAuth, "tls-client-auth", "require_and_verify", "TLS client authentication mode (none, request, require_and_verify)")
	cmd.Flags().BoolVar(&f.enableMySQL, "enable-mysql", false, "Enable MySQL wire protocol server")
	cmd.Flags().IntVar(&f.mysqlPort, "mysql-port", 3306, "MySQL wire protocol server port")
	cmd.Flags().BoolVar(&f.enablePgSQL, "enable-pgsql", false, "Enable PostgreSQL wire protocol server")
	cmd.Flags().IntVar(&f.pgsqlPort, "pgsql-port", 5432, "PostgreSQL wire protocol server port")
	cmd.Flags().StringVar(&f.sqlDatabase, "sql-database", "qrdb", "Default SQL database name")
	cmd.Flags().StringVar(&f.tlsCAFile, "tls-ca-file", "", "Path to CA certificate PEM file")
	cmd.Flags().StringVar(&f.tlsCertFile, "tls-cert-file", "", "Path to TLS certificate PEM file")
	cmd.Flags().StringVar(&f.tlsKeyFile, "tls-key-file", "", "Path to TLS private key PEM file")
	cmd.Flags().StringVar(&f.bootstrapNoiseKey, "bootstrap-noise-key", "", "Hex Curve25519 public key for Noise bootstrap")
	cmd.Flags().StringVar(&f.bootstrapNoiseAddr, "bootstrap-noise-addr", "", "host:port of Noise bootstrap server")
	cmd.Flags().IntVar(&f.bootstrapNoisePort, "bootstrap-noise-port", 63006, "TCP port for Noise bootstrap server (first node)")
	cmd.Flags().StringVar(&f.bootstrapSPKIPin, "bootstrap-spki-pin", "", "Hex SHA-256 SPKI pin for bootstrap server")
	cmd.Flags().StringVar(&f.bootstrapDANEDNS, "bootstrap-dane-dns", "", "DNS server for DANE TLSA lookup (e.g. 8.8.8.8:53)")
	cmd.Flags().BoolVar(&f.bootstrapDANEDoT, "bootstrap-dane-dot", false, "Use DNS-over-TLS for DANE")
	cmd.Flags().StringVar(&f.bootstrapDirectURL, "bootstrap-direct-url", "", "Full HTTPS URL for direct bootstrap")
	cmd.Flags().StringVar(&f.bootstrapAddr, "bootstrap-addr", "", "host:port of first node's admin server (for SPKI/DANE)")
	cmd.Flags().BoolVar(&f.leaseRead, "lease-read", true,
		"Enable lease-based linearizable reads (requires --check-quorum)")
	cmd.Flags().BoolVar(&f.checkQuorum, "check-quorum", true,
		"Enable leader step-down on quorum loss (required for --lease-read)")

	return cmd
}

// applyConfigToFlags applies YAML configuration values to startFlags for
// fields that were not explicitly set via CLI flags. CLI flags always take
// precedence over YAML values.
func applyConfigToFlags(cmd *cobra.Command, f *startFlags, cfg *qrdbconfig.Config) {
	if !cmd.Flags().Changed("listen") {
		f.listen = cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.AdminPort)
	}
	if !cmd.Flags().Changed("raft-address") {
		f.raftAddress = cfg.Raft.RaftAddress
	}
	if !cmd.Flags().Changed("data-dir") {
		f.dataDir = cfg.Raft.DataDir
	}
	if !cmd.Flags().Changed("deployment-id") {
		f.deploymentID = cfg.Raft.DeploymentID
	}
	if !cmd.Flags().Changed("sm") {
		f.smType = cfg.Raft.SMType
	}
	if !cmd.Flags().Changed("sm-backend") {
		f.smBackend = cfg.Raft.SMBackend
	}
	if !cmd.Flags().Changed("discovery") {
		f.discoveryMethod = cfg.Discovery.Method
	}
	if !cmd.Flags().Changed("peers") && cfg.Raft.Peers != "" {
		f.peers = strings.Split(cfg.Raft.Peers, ",")
	}
	if !cmd.Flags().Changed("shard-id") {
		f.shardID = cfg.Raft.ShardID
	}
	if !cmd.Flags().Changed("replica-id") {
		f.replicaID = cfg.Raft.ReplicaID
	}
	if !cmd.Flags().Changed("multicast-secret") && cfg.Discovery.MulticastSecret != "" {
		f.multicastSecret = cfg.Discovery.MulticastSecret
	}
	if !cmd.Flags().Changed("dns-domain") && cfg.Discovery.DNSDomain != "" {
		f.dnsDomain = cfg.Discovery.DNSDomain
	}
	if !cmd.Flags().Changed("admin-port") {
		f.adminPort = cfg.Server.AdminPort
	}
	if !cmd.Flags().Changed("grpc-port") {
		f.grpcPort = cfg.Server.GRPCPort
	}
	if !cmd.Flags().Changed("max-in-mem-log-size") {
		f.maxInMemLogSize = cfg.Raft.MaxInMemLogSize
	}
	if !cmd.Flags().Changed("max-total-in-mem-log-size") {
		f.maxTotalInMemLogSize = cfg.Raft.MaxTotalInMemLogSize
	}
	if !cmd.Flags().Changed("snapshot-entries") {
		f.snapshotEntries = cfg.Raft.SnapshotEntries
	}
	if !cmd.Flags().Changed("compaction-overhead") {
		f.compactionOverhead = cfg.Raft.CompactionOverhead
	}
	if !cmd.Flags().Changed("join") {
		f.join = cfg.Raft.Join
	}
	if !cmd.Flags().Changed("log-format") {
		f.logFormat = cfg.Logging.Format
	}
	if !cmd.Flags().Changed("log-level") {
		f.logLevel = cfg.Logging.Level
	}
	if !cmd.Flags().Changed("enable-rest") {
		f.enableREST = cfg.Protocols.REST
	}
	if !cmd.Flags().Changed("enable-grpc") {
		f.enableGRPC = cfg.Protocols.GRPC
	}
	if !cmd.Flags().Changed("enable-quic") {
		f.enableQUIC = cfg.Protocols.QUIC
	}
	if !cmd.Flags().Changed("quic-port") {
		f.quicPort = cfg.Server.QUICPort
	}
	if !cmd.Flags().Changed("enable-unix") {
		f.enableUnix = cfg.Protocols.Unix
	}
	if !cmd.Flags().Changed("unix-socket-path") && cfg.Unix.SocketPath != "" {
		f.unixSocketPath = cfg.Unix.SocketPath
	}
	if !cmd.Flags().Changed("enable-mcp") {
		f.enableMCP = cfg.Protocols.MCP
	}
	if !cmd.Flags().Changed("mcp-port") && cfg.MCP.Port != 0 {
		f.mcpPort = cfg.MCP.Port
	}
	if !cmd.Flags().Changed("mcp-endpoint") && cfg.MCP.Endpoint != "" {
		f.mcpEndpoint = cfg.MCP.Endpoint
	}
	if !cmd.Flags().Changed("auth-enabled") {
		f.authEnabled = cfg.Auth.Enabled
	}
	if !cmd.Flags().Changed("enable-mysql") {
		f.enableMySQL = cfg.Protocols.MySQL
	}
	if !cmd.Flags().Changed("mysql-port") {
		f.mysqlPort = cfg.Server.MySQLPort
	}
	if !cmd.Flags().Changed("enable-pgsql") {
		f.enablePgSQL = cfg.Protocols.PostgreSQL
	}
	if !cmd.Flags().Changed("pgsql-port") {
		f.pgsqlPort = cfg.Server.PgSQLPort
	}
	if !cmd.Flags().Changed("sql-database") && cfg.SQL.DefaultDatabase != "" {
		f.sqlDatabase = cfg.SQL.DefaultDatabase
	}
	if !cmd.Flags().Changed("tls-ca-file") && cfg.TLS.CAFile != "" {
		f.tlsCAFile = cfg.TLS.CAFile
	}
	if !cmd.Flags().Changed("tls-cert-file") && cfg.TLS.CertFile != "" {
		f.tlsCertFile = cfg.TLS.CertFile
	}
	if !cmd.Flags().Changed("tls-key-file") && cfg.TLS.KeyFile != "" {
		f.tlsKeyFile = cfg.TLS.KeyFile
	}
	if !cmd.Flags().Changed("tls-client-auth") && cfg.TLS.ClientAuth != "" {
		f.tlsClientAuth = cfg.TLS.ClientAuth
	}
	if !cmd.Flags().Changed("bootstrap-noise-key") && cfg.Bootstrap.NoiseKey != "" {
		f.bootstrapNoiseKey = cfg.Bootstrap.NoiseKey
	}
	if !cmd.Flags().Changed("bootstrap-noise-addr") && cfg.Bootstrap.NoiseAddr != "" {
		f.bootstrapNoiseAddr = cfg.Bootstrap.NoiseAddr
	}
	if !cmd.Flags().Changed("bootstrap-noise-port") {
		f.bootstrapNoisePort = cfg.Bootstrap.NoisePort
	}
	if !cmd.Flags().Changed("bootstrap-spki-pin") && cfg.Bootstrap.SPKIPin != "" {
		f.bootstrapSPKIPin = cfg.Bootstrap.SPKIPin
	}
	if !cmd.Flags().Changed("bootstrap-dane-dns") && cfg.Bootstrap.DANEDNS != "" {
		f.bootstrapDANEDNS = cfg.Bootstrap.DANEDNS
	}
	if !cmd.Flags().Changed("bootstrap-dane-dot") {
		f.bootstrapDANEDoT = cfg.Bootstrap.DANEDoT
	}
	if !cmd.Flags().Changed("bootstrap-direct-url") && cfg.Bootstrap.DirectURL != "" {
		f.bootstrapDirectURL = cfg.Bootstrap.DirectURL
	}
	if !cmd.Flags().Changed("bootstrap-addr") && cfg.Bootstrap.Addr != "" {
		f.bootstrapAddr = cfg.Bootstrap.Addr
	}

	if !cmd.Flags().Changed("lease-read") && cfg.Raft.LeaseRead != nil {
		f.leaseRead = *cfg.Raft.LeaseRead
	}
	if !cmd.Flags().Changed("check-quorum") && cfg.Raft.CheckQuorum != nil {
		f.checkQuorum = *cfg.Raft.CheckQuorum
	}

	// Database configs are always inherited from YAML (no CLI flag equivalent).
	if len(cfg.Databases) > 0 {
		f.databases = cfg.Databases
	}

	// Codec config is always inherited from YAML (no CLI flag equivalent).
	f.codecConfig = cfg.Codec

	// Seal config is always inherited from YAML (no CLI flag equivalent).
	f.sealConfig = cfg.Seal
}

// resolveAutoConfig auto-generates replica-id, port, and data-dir for
// non-static discovery methods when the user hasn't explicitly set them.
// This allows users to run `qrdbd start --discovery multicast --multicast-secret test`
// in multiple terminals without manually assigning unique values.
func resolveAutoConfig(cmd *cobra.Command, f *startFlags) error {
	if f.discoveryMethod == "static" {
		return nil
	}

	// Auto-generate replica-id if not explicitly set.
	if !cmd.Flags().Changed("replica-id") && f.configFile == "" {
		id, err := generateReplicaID()
		if err != nil {
			return err
		}
		f.replicaID = id
		slog.Info("auto-generated replica ID", "replica_id", f.replicaID)
	}

	// Auto-select a free port if listen/raft-address use the default port.
	if !cmd.Flags().Changed("listen") && !cmd.Flags().Changed("raft-address") && f.configFile == "" {
		port, err := findFreeUDPPort()
		if err != nil {
			return err
		}
		f.listen = fmt.Sprintf("0.0.0.0:%d", port)
		f.raftAddress = fmt.Sprintf("127.0.0.1:%d", port)
		slog.Info("auto-selected port", "port", port)
	}

	// Auto-select admin port if not explicitly set.
	if !cmd.Flags().Changed("admin-port") && f.configFile == "" {
		port, err := findFreeTCPPort()
		if err != nil {
			return err
		}
		f.adminPort = port
		slog.Info("auto-selected admin port", "admin_port", port)
	}

	// Auto-select gRPC port if not explicitly set.
	if !cmd.Flags().Changed("grpc-port") && f.configFile == "" {
		port, err := findFreeTCPPort()
		if err != nil {
			return err
		}
		f.grpcPort = port
		slog.Info("auto-selected gRPC port", "grpc_port", port)
	}

	// Auto-select QUIC port if not explicitly set and QUIC is enabled.
	if f.enableQUIC && !cmd.Flags().Changed("quic-port") && f.configFile == "" {
		port, err := findFreeUDPPort()
		if err != nil {
			return err
		}
		f.quicPort = port
		slog.Info("auto-selected QUIC port", "quic_port", port)
	}

	// Append replica-id to data-dir to avoid collisions when not explicitly set.
	if !cmd.Flags().Changed("data-dir") && f.configFile == "" {
		f.dataDir = fmt.Sprintf("./data/%d", f.replicaID)
		slog.Info("auto-configured data directory", "data_dir", f.dataDir)
	}

	return nil
}

// generateReplicaID generates a random replica ID using crypto/rand.
// The result is masked to 48 bits to keep it human-readable while still
// having negligible collision probability.
func generateReplicaID() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	// Mask to 48 bits and ensure non-zero.
	id := binary.LittleEndian.Uint64(buf[:]) & 0xFFFFFFFFFFFF
	if id == 0 {
		id = 1
	}
	return id, nil
}

// findFreeUDPPort finds and returns an available UDP port on localhost.
func findFreeUDPPort() (int, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return port, nil
}

// findFreeTCPPort finds and returns an available TCP port on localhost.
func findFreeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// runStart executes the start command logic: load or generate TLS, build
// config, create the host, start the shard, build service layer, create
// the multi-protocol server, and block until a termination signal is
// received.
func runStart(f *startFlags) error {
	// Resolve TLS: explicit files -> cached certs -> generate/bootstrap.
	bundle, bootServers, err := resolveTLS(f, slog.Default())
	if err != nil {
		return err
	}

	// Enable TLS certificate hot-reload when certs are file-backed.
	// The CertReloader watches the data directory for cert changes and
	// atomically swaps the certificate on every TLS handshake.
	certPath := filepath.Join(f.dataDir, "node-cert.pem")
	keyPath := filepath.Join(f.dataDir, "node-key.pem")
	if fileExists(certPath) && fileExists(keyPath) {
		if reloadErr := wireCertReloader(bundle, certPath, keyPath, slog.Default()); reloadErr != nil {
			slog.Warn("TLS cert hot-reload not available", "error", reloadErr)
		} else {
			slog.Info("TLS cert hot-reload enabled", "watch_dir", f.dataDir)
		}
	}

	// Create subsystems that must be shared between the state machine
	// and the service layer. The Lessor handles lease lifecycle inside
	// the SM, the WatchHub delivers change events to watchers, and
	// time-series storage is lazily created by the SM when the DB opens.
	lr := lease.NewLessor()
	watchHub := watch.NewHub()

	// Run one-time migration from flat layout to per-database structure.
	if err := database.MigrateIfNeeded(slog.Default(), f.dataDir); err != nil {
		return err
	}

	// Initialize database manager.
	dbManager := database.NewManager(slog.Default(), f.dataDir)
	defer func() { _ = dbManager.Close() }()

	// Use default database configs when no YAML config was provided.
	if len(f.databases) == 0 {
		f.databases = qrdbconfig.DefaultConfig().Databases
	}

	// Create or open configured databases.
	for _, dbCfg := range f.databases {
		dCfg := database.DatabaseConfig{
			Name:    dbCfg.Name,
			Engine:  database.EngineType(dbCfg.Engine),
			Backend: database.BackendType(dbCfg.Backend),
		}
		if _, err := dbManager.Create(dCfg); err != nil {
			// Try opening if it already exists (idempotent startup).
			var ae *database.AlreadyExistsError
			if errors.As(err, &ae) {
				if _, err := dbManager.Open(dbCfg.Name); err != nil {
					return err
				}
				continue
			}
			return err
		}
	}

	// Ensure the SQL database exists when --sql-database is set and at
	// least one SQL wire protocol is enabled. This covers the common case
	// where the CLI flag specifies a database name that is not in the
	// static Databases config list.
	if f.sqlDatabase != "" && (f.enableMySQL || f.enablePgSQL) {
		if _, err := dbManager.Get(f.sqlDatabase); err != nil {
			var nfe *database.NotFoundError
			if errors.As(err, &nfe) {
				// Determine backend from the state machine type.
				backend := database.BackendPebble
				if f.smType == "memory" || f.smType == "concurrent" {
					backend = database.BackendMemory
				}
				sqlCfg := database.DatabaseConfig{
					Name:    f.sqlDatabase,
					Engine:  database.EngineKV,
					Backend: backend,
					Protocol: database.ProtocolConfig{
						MySQL:      f.enableMySQL,
						PostgreSQL: f.enablePgSQL,
					},
				}
				if _, err := dbManager.Create(sqlCfg); err != nil {
					return err
				}
			} else {
				return err
			}
		} else {
			// Database already exists; ensure SQL protocols are enabled.
			if err := dbManager.UpdateProtocol(f.sqlDatabase, database.ProtocolConfig{
				MySQL:      f.enableMySQL,
				PostgreSQL: f.enablePgSQL,
			}); err != nil {
				return err
			}
		}
	}

	// Build a subsystem-aware state machine factory for "mvcc" and
	// "pebble" SM types. Other types fall back to the registry default.
	switch f.smType {
	case "mvcc":
		f.createFn = store.NewMVCCCreateFuncWith(nil,
			store.WithLessor(lr),
			store.WithWatchHub(watchHub),
			store.WithTimeSeriesEnabled(),
		)
	case "pebble":
		f.createFn = sm.NewDiskCreateFunc(func(_, _ uint64) sm.DiskStateMachine {
			s := store.NewPebbleStateMachine(nil)
			s.SetLessor(lr)
			s.SetWatchHub(watchHub)
			s.SetTimeSeriesEnabled(true)
			return s
		})
	}

	// Register the signal handler before node initialization so that
	// SIGINT/SIGTERM during shard startup (which may block on leader
	// election) triggers a clean cancellation rather than an abrupt
	// process exit.
	sigCtx, sigStop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

	host, err := initializeNode(f, bundle)
	if err != nil {
		return err
	}

	// SIGHUP triggers manual TLS certificate reload (for scripted rotation).
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		for range sighupCh {
			handleSIGHUP(bundle.Reloader, slog.Default())
		}
	}()
	defer signal.Stop(sighupCh)

	// Build the service layer.
	adapter := newHostAdapter(host)

	// Create the codec manager from configuration. When the codec config
	// has non-empty values, the manager is created and injected into the
	// KV service for storage encoding and wire transcoding.
	var kvServiceOpts []service.KVServiceOption
	if f.codecConfig.StorageCodec != "" && f.codecConfig.WireCodec != "" {
		codecMgr, err := codec.NewManagerFromConfig(&codec.Config{
			StorageCodec: f.codecConfig.StorageCodec,
			WireCodec:    f.codecConfig.WireCodec,
			UseOptimized: f.codecConfig.UseOptimized,
		})
		if err != nil {
			return err
		}
		kvServiceOpts = append(kvServiceOpts, service.WithCodecManager(codecMgr))
	}

	// Use a bootstrap-specific adapter that reads from the local SM
	// (StaleRead) instead of going through ReadIndex. This avoids
	// a startup deadlock where the Raft ReadIndex pipeline is not
	// yet warmed up after leader election.
	bootAdapter := &bootstrapHostAdapter{hostAdapter: adapter}
	bootKVSvc := service.NewKVService(bootAdapter, shard.ShardKV)

	// Build barrier and ceremony services when seal is enabled. The barrier
	// wraps a dedicated PebbleDB storage backend with transparent AES-GCM
	// encryption. The credential and ceremony services are layered on top.
	var sealBarrier *seal.StorageBarrier
	var credCoreSvc *credentials.CredentialService
	var barrierRegistry *seal.BarrierRegistry
	if f.sealConfig.Enabled {
		sealDataDir := filepath.Join(f.dataDir, "seal")
		if err := os.MkdirAll(sealDataDir, 0o700); err != nil {
			_ = shutdownNode(host)
			return &SealStorageError{Err: err}
		}
		sealStore, err := pebblestore.New(&pebblestore.Config{DataDir: sealDataDir})
		if err != nil {
			_ = shutdownNode(host)
			return &SealStorageError{Err: err}
		}
		defer func() { _ = sealStore.Close() }()

		sealBackend := seal.NewStorageAdapter(sealStore)
		barrierCfg := seal.BarrierConfig{
			RootKeyPath:  f.sealConfig.RootKeyPath,
			DeploymentID: f.deploymentID,
		}

		var strategies []seal.SealingStrategy
		switch f.sealConfig.Strategy {
		case "shamir":
			shamirStrat, shamirErr := seal.NewShamirStrategy(
				sealBackend, f.sealConfig.ShamirThreshold, f.sealConfig.ShamirTotal)
			if shamirErr != nil {
				_ = shutdownNode(host)
				return &SealStrategyError{Strategy: "shamir", Err: shamirErr}
			}
			strategies = append(strategies, shamirStrat)
			barrierCfg.Shamir = &seal.ShamirConfig{
				Threshold:   f.sealConfig.ShamirThreshold,
				TotalShares: f.sealConfig.ShamirTotal,
			}
		default: // "software"
			// The SoftwareStrategy requires a passphrase at construction
			// time. Use a bootstrap passphrase derived from the deployment
			// ID to create the strategy. The actual passphrase for
			// barrier initialization is provided via the BarrierInitialize
			// API and must match this bootstrap passphrase.
			bootstrapPass := deriveBootstrapPassphrase(f.deploymentID)
			softStrat, softErr := seal.NewSoftwareStrategy(bootstrapPass)
			if softErr != nil {
				_ = shutdownNode(host)
				return &SealStrategyError{Strategy: "software", Err: softErr}
			}
			strategies = append(strategies, softStrat)
		}

		var barrierErr error
		sealBarrier, barrierErr = seal.NewStorageBarrier(
			slog.Default(), sealBackend, barrierCfg, strategies...)
		if barrierErr != nil {
			_ = shutdownNode(host)
			return &SealBarrierError{Err: barrierErr}
		}

		// Build credential service for ceremony support.
		credStrategy := f.sealConfig.CredentialStrategy
		if credStrategy == "" {
			credStrategy = "barrier"
		}
		var credErr error
		credCoreSvc, credErr = credentials.NewCredentialService(
			&credentials.Config{Strategy: credStrategy}, sealBarrier, slog.Default())
		if credErr != nil {
			_ = shutdownNode(host)
			return &CredentialServiceError{Err: credErr}
		}

		// Initialize barrier registry for multi-tenant encryption isolation.
		// The system barrier is the root; tenant barriers are created on demand
		// when tenants are provisioned via the TenantService API.
		reg, regErr := seal.NewBarrierRegistry(slog.Default(), sealBarrier)
		if regErr != nil {
			_ = shutdownNode(host)
			return &BarrierRegistryError{Err: regErr}
		}
		barrierRegistry = reg

		slog.Info("seal barrier initialized",
			slog.String("strategy", f.sealConfig.Strategy),
			slog.String("data_dir", sealDataDir))
	}

	// Initialize tenant DEK manager for per-tenant data encryption key
	// management. The cluster DEK manager wraps the seal barrier to provide
	// a shared cluster-level DEK that wraps each tenant's isolated DEK.
	// A dedicated PebbleDB directory persists the wrapped tenant DEKs.
	var tenantDEKMgr dek.TenantDEKManager
	if sealBarrier != nil && barrierRegistry != nil {
		clusterDEKMgr, cdErr := dek.NewClusterDEKManager(sealBarrier, slog.Default())
		if cdErr != nil {
			_ = shutdownNode(host)
			return &ClusterDEKError{Err: cdErr}
		}
		dekDataDir := filepath.Join(f.dataDir, "dek")
		if err := os.MkdirAll(dekDataDir, 0o700); err != nil {
			_ = shutdownNode(host)
			return &DEKStorageError{Err: err}
		}
		dekStore, dekStoreErr := pebblestore.New(&pebblestore.Config{DataDir: dekDataDir})
		if dekStoreErr != nil {
			_ = shutdownNode(host)
			return &DEKStorageError{Err: dekStoreErr}
		}
		defer func() { _ = dekStore.Close() }()
		dekBackend := seal.NewStorageAdapter(dekStore)
		tdm, tdmErr := dek.NewTenantDEKManager(clusterDEKMgr, dekBackend, slog.Default(), nil)
		if tdmErr != nil {
			_ = shutdownNode(host)
			return &TenantDEKError{Err: tdmErr}
		}
		tenantDEKMgr = tdm
		slog.Info("tenant DEK manager initialized", slog.String("data_dir", dekDataDir))
	}

	// Construct all services via the shared lifecycle package. Seal-gated
	// services (barrier, ceremony, credential, tenant, cert) are wired when
	// sealBarrier and barrierRegistry are non-nil.
	svcNode, err := lifecycle.BuildServices(sigCtx, lifecycle.Infra{
		Host:              adapter,
		WatchHub:          watchHub,
		DataDir:           f.dataDir,
		DatabaseManager:   dbManager,
		KVOptions:         kvServiceOpts,
		SealBarrier:       sealBarrier,
		BarrierRegistry:   barrierRegistry,
		CredentialCoreSvc: credCoreSvc,
		CeremonyDataDir:   filepath.Join(f.dataDir, "seal"),
		Logger:            slog.Default(),
	})
	if err != nil {
		_ = shutdownNode(host)
		return err
	}
	defer func() { _ = svcNode.Stop(context.Background()) }()

	// Unpack the services that the rest of this function references by name.
	// The lifecycle Node owns lifecycle management (vector storage close, etc.);
	// we keep named local variables here for readability in the server assembly.
	adminSvc := svcNode.Admin
	kvSvc := svcNode.KV
	batchSvc := svcNode.Batch
	txSvc := svcNode.Transaction
	mvccSvc := svcNode.MVCC
	leaseSvc := svcNode.Lease
	tsSvc := svcNode.TimeSeries
	indexSvc := svcNode.Index
	watchSvc := svcNode.Watch
	lockSvc := svcNode.Lock
	collectionSvc := svcNode.Collection
	dbSvc := svcNode.Database
	vectorSvc := svcNode.Vector
	barrierSvc := svcNode.Barrier
	ceremonySvc := svcNode.Ceremony
	tenantSvcInst := svcNode.Tenant

	// Create a codec-free KV service for the auth middleware. System stores
	// (user, role, audit) write raw JSON — they must NOT go through the
	// storage/wire codec pipeline, which expects user data to be encoded
	// in the configured storage codec (e.g. msgpack).
	authKVSvc := service.NewKVService(adapter, shard.ShardKV)

	// Bootstrap authentication infrastructure (user/role stores, RBAC,
	// audit logger, auth middleware). Uses bootKVSvc (StaleRead) for
	// initial bootstrap operations and authKVSvc (SyncRead, no codec) for
	// the runtime auth middleware, ensuring followers see committed state.
	var authResult *authBootstrapResult
	if f.authEnabled {
		authResult, err = bootstrapAuth(sigCtx, bootKVSvc, authKVSvc, f.dataDir, host, shard.ShardKV)
		if err != nil {
			_ = shutdownNode(host)
			// Context cancellation from signal is a clean shutdown, not an error.
			if sigCtx.Err() != nil {
				slog.Info("auth bootstrap canceled by shutdown signal")
				return nil
			}
			return err
		}
		defer func() { _ = authResult.auditLogger.Close() }()
	} else {
		slog.Warn("authentication disabled: all endpoints accessible without credentials")
	}

	// Warm up the ReadIndex pipeline by performing a SyncRead. After
	// a leader election (including restart), Raft requires a committed
	// no-op entry before ReadIndex is operational. Without this warmup,
	// the auth middleware's SyncRead-backed user store lookups would
	// fail until the pipeline is ready, returning 503 to early clients.
	warmupReadIndex(sigCtx, kvSvc)

	// Generate admin client certificate for mTLS authentication with the
	// status and peers CLI commands. Only possible when the CA private key
	// is available (auto-generated TLS) and auth is enabled. When external
	// TLS is provided, the operator must supply their own client certificates.
	if f.authEnabled && len(bundle.CAKeyPEM) > 0 {
		if err := generateAdminClientCert(bundle, f.dataDir, "admin", tenant.DefaultTenantID); err != nil {
			_ = shutdownNode(host)
			return err
		}
	}

	// Build leader forwarding resolver so writes on non-leader nodes
	// are transparently proxied to the current leader. Uses HTTPS scheme
	// since the admin server now runs TLS.
	adminPortStr := strconv.Itoa(f.adminPort)
	registry := rest.NewPeerAdminRegistryTLS(adminPortStr)

	// Register the local node so other nodes forwarding back to us can
	// be detected as a loop (and for symmetry with peer registrations).
	localAdminURL := "https://" + net.JoinHostPort("127.0.0.1", adminPortStr)
	registry.Register(f.raftAddress, localAdminURL)

	// Build server configuration using the QRDB config type required by
	// the server.New constructor.
	serverCfg := buildServerConfig(f)

	// Configure mTLS on the TLS listener. When auth is enabled,
	// VerifyClientCertIfGiven allows public endpoints (like /health) to
	// work without a client certificate while still validating certs when
	// provided. When auth is disabled, NoClientCert disables client cert
	// verification entirely.
	if f.authEnabled {
		bundle.TLSConfig.ClientAuth = tls.VerifyClientCertIfGiven
		bundle.TLSConfig.ClientCAs = bundle.CACertPool
	} else {
		bundle.TLSConfig.ClientAuth = tls.NoClientCert
	}

	// Create and start the multi-protocol server.
	serverOpts := []server.ServerOption{
		server.WithTLS(bundle.TLSConfig),
		server.WithCACertPool(bundle.CACertPool),
		server.WithLeaderForwarding(registry),
		server.WithHost(host),
		server.WithBatch(batchSvc),
		server.WithTransaction(txSvc),
		server.WithMVCC(mvccSvc),
		server.WithLease(leaseSvc),
		server.WithLockService(lockSvc),
		server.WithTimeSeries(tsSvc),
		server.WithIndex(indexSvc),
		server.WithWatch(watchSvc),
		server.WithVector(vectorSvc),
		server.WithCollectionService(collectionSvc),
		server.WithDatabaseService(dbSvc),
		server.WithDatabaseManager(dbManager),
	}
	if authResult != nil {
		serverOpts = append(serverOpts,
			server.WithUserService(authResult.userSvc),
			server.WithRoleService(authResult.roleSvc),
			server.WithAuthMiddleware(authResult.authMW),
		)
	}
	if barrierSvc != nil {
		serverOpts = append(serverOpts, server.WithBarrierService(barrierSvc))
	}
	if ceremonySvc != nil {
		serverOpts = append(serverOpts, server.WithCeremonyService(ceremonySvc))
	}
	if svcNode.CertProv != nil {
		serverOpts = append(serverOpts, server.WithCertProvider(svcNode.CertProv))
	}
	if svcNode.Credential != nil {
		serverOpts = append(serverOpts, server.WithCredentialService(svcNode.Credential))
	}
	if barrierRegistry != nil {
		serverOpts = append(serverOpts, server.WithBarrierRegistry(barrierRegistry))
	}
	if tenantDEKMgr != nil {
		serverOpts = append(serverOpts, server.WithTenantDEKManager(tenantDEKMgr))
	}
	if tenantSvcInst != nil {
		serverOpts = append(serverOpts, server.WithTenantService(tenantSvcInst))
	}
	if bootServers != nil {
		serverOpts = append(serverOpts, server.WithCABootstrap(bundle.CACertPEM, bundle.CAKeyPEM))
	}
	if h := webUIHandler(); h != nil {
		serverOpts = append(serverOpts, server.WithWebUI(h))
	}

	// Build per-database service registry for multi-database routing.
	// Currently all databases share the system shard services. As per-database
	// shard allocation (Phase 1.2) is enabled, each database will get its
	// own service set backed by a dedicated shard.
	dbRegistry := server.NewDatabaseServiceRegistry("default")
	dbRegistry.Register("default", &server.DatabaseServices{
		Name:        "default",
		ShardID:     shard.ShardKV,
		KV:          kvSvc,
		MVCC:        mvccSvc,
		Batch:       batchSvc,
		Transaction: txSvc,
		Lease:       leaseSvc,
		TimeSeries:  tsSvc,
		Index:       indexSvc,
		Watch:       watchSvc,
		Collection:  collectionSvc,
	})
	serverOpts = append(serverOpts, server.WithDatabaseServiceRegistry(dbRegistry))

	srv := server.New(serverCfg, kvSvc, adminSvc, serverOpts...)
	if err := srv.Start(); err != nil {
		_ = shutdownNode(host)
		return err
	}

	slog.Info("qrdbd node started, waiting for signals")

	// Block until SIGINT or SIGTERM (already captured by sigCtx).
	<-sigCtx.Done()
	slog.Info("received signal, shutting down")

	// Clean up bootstrap servers and cert reloader.
	if bootServers != nil {
		bootServers.Stop()
	}
	if bundle.Reloader != nil {
		_ = bundle.Reloader.Stop()
	}

	srv.Stop()
	return shutdownNode(host)
}

// buildServerConfig constructs a *qrdbconfig.Config from the parsed start
// flags for use by the multi-protocol server orchestrator.
func buildServerConfig(f *startFlags) *qrdbconfig.Config {
	return &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{
			AdminPort: f.adminPort,
			GRPCPort:  f.grpcPort,
			QUICPort:  f.quicPort,
			MySQLPort: f.mysqlPort,
			PgSQLPort: f.pgsqlPort,
			Host:      "0.0.0.0",
		},
		Protocols: qrdbconfig.ProtocolsConfig{
			REST:       f.enableREST,
			GRPC:       f.enableGRPC,
			QUIC:       f.enableQUIC,
			Unix:       f.enableUnix,
			MCP:        f.enableMCP,
			MySQL:      f.enableMySQL,
			PostgreSQL: f.enablePgSQL,
		},
		Unix: qrdbconfig.UnixConfig{
			SocketPath: f.unixSocketPath,
		},
		MCP: qrdbconfig.MCPConfig{
			Port:     f.mcpPort,
			Endpoint: f.mcpEndpoint,
		},
		Auth: qrdbconfig.AuthConfig{
			Enabled: f.authEnabled,
		},
		SQL: qrdbconfig.SQLConfig{
			DefaultDatabase: f.sqlDatabase,
		},
		Raft: qrdbconfig.RaftConfig{
			DataDir:              f.dataDir,
			DeploymentID:         f.deploymentID,
			ShardID:              f.shardID,
			ReplicaID:            f.replicaID,
			RaftAddress:          f.raftAddress,
			ListenAddress:        f.listen,
			SMType:               f.smType,
			MaxInMemLogSize:      f.maxInMemLogSize,
			MaxTotalInMemLogSize: f.maxTotalInMemLogSize,
			SnapshotEntries:      f.snapshotEntries,
			CompactionOverhead:   f.compactionOverhead,
			Join:                 f.join,
		},
		TLS: qrdbconfig.TLSConfig{
			Enabled:    true,
			CAFile:     f.tlsCAFile,
			CertFile:   f.tlsCertFile,
			KeyFile:    f.tlsKeyFile,
			ClientAuth: f.tlsClientAuth,
			MinVersion: "TLS1.3",
		},
		Logging: qrdbconfig.LoggingConfig{
			Level:  f.logLevel,
			Format: f.logFormat,
		},
		Discovery: qrdbconfig.DiscoveryConfig{
			Method:          f.discoveryMethod,
			MulticastSecret: f.multicastSecret,
			DNSDomain:       f.dnsDomain,
		},
	}
}

// deriveBootstrapPassphrase generates a deterministic passphrase from the
// deployment ID for sealing the barrier's root key. The passphrase is used
// to construct the SoftwareStrategy at startup. Users must provide this
// same passphrase via the BarrierInitialize API to initialize the barrier.
func deriveBootstrapPassphrase(deploymentID uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, deploymentID)
	// Use a fixed prefix + deployment ID as the bootstrap passphrase.
	// The user must call barrier init with this passphrase to initialize.
	passphrase := make([]byte, 0, 32)
	passphrase = append(passphrase, []byte("qrdb-seal-bootstrap:")...)
	passphrase = append(passphrase, buf...)
	return passphrase
}

// initializeNode performs all initialization: validates flags, creates the
// host, and starts the shard. Returns the running host or an error.
// The bundle is used to configure mTLS on the QUIC transport for
// multi-node clusters (nil for single-node which uses WithoutTransport).
func initializeNode(f *startFlags, bundle *TLSBundle) (*quicraft.Host, error) {
	slog.Info("starting qrdbd node",
		"listen", f.listen,
		"raft_address", f.raftAddress,
		"data_dir", f.dataDir,
		"shard_id", f.shardID,
		"replica_id", f.replicaID,
		"sm", f.smType,
		"sm_backend", f.smBackend,
		"discovery", f.discoveryMethod,
	)

	// Resolve the state machine factory. Use the pre-built factory if
	// one was injected (e.g. with subsystem options), otherwise look
	// up the default factory from the registry.
	createFn := f.createFn
	if createFn == nil {
		var err error
		createFn, err = resolveCreateFunc(f.smType, f.smBackend)
		if err != nil {
			return nil, err
		}
	}

	// Validate peers early for static multi-node clusters so that
	// PeerParseError surfaces before transport initialization which
	// requires mTLS and would produce a less helpful error.
	if f.discoveryMethod == "static" && !isSingleNode(f) {
		if _, err := parsePeers(f.peers); err != nil {
			return nil, err
		}
	}

	// Build mTLS config for transport (nil for single-node).
	var mtlsCfg *quicraftconfig.MTLSConfig
	if !isSingleNode(f) && bundle != nil {
		mtlsCfg = &quicraftconfig.MTLSConfig{
			CACert: bundle.CACertPEM,
			Cert:   bundle.CertPEM,
			Key:    bundle.KeyPEM,
		}
	}

	// Build host configuration.
	hostCfg := buildHostConfig(f, mtlsCfg)

	// Determine host options based on peer count.
	// Single-node (no remote peers) uses WithoutTransport.
	var hostOpts []quicraft.HostOption
	if isSingleNode(f) {
		hostOpts = append(hostOpts, quicraft.WithoutTransport())
		slog.Info("single-node mode, transport disabled")
	}

	// Create the host.
	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		return nil, err
	}

	// Build shard configuration.
	shardCfg := buildShardConfig(f)

	// Start the shard using the selected discovery method.
	if err := startShard(f, host, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, err
	}

	return host, nil
}

// waitForSignal blocks until SIGINT or SIGTERM is received.
func waitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())
}

// shutdownNode gracefully closes the node and logs the result.
func shutdownNode(node io.Closer) error {
	if err := node.Close(); err != nil {
		slog.Error("error during shutdown", "error", err)
		return err
	}
	slog.Info("qrdbd node stopped")
	return nil
}

// resolveCreateFunc looks up the state machine factory by type and
// optional backend in the centralized statemachine.Registry and returns
// the CreateFunc, or a local UnknownSMError if the combination is not
// registered.
func resolveCreateFunc(smType, smBackend string) (sm.CreateFunc, error) {
	createFn, err := statemachine.ResolveTyped(smType, smBackend)
	if err != nil {
		name := smType
		if smBackend != "" {
			name = smType + ":" + smBackend
		}
		return nil, &UnknownSMError{Name: name}
	}
	return createFn, nil
}

// buildHostConfig constructs a HostConfig from the parsed start flags.
// When mtls is non-nil it is attached to the TransportConfig for QUIC
// transport mTLS.
func buildHostConfig(f *startFlags, mtls *quicraftconfig.MTLSConfig) quicraftconfig.HostConfig {
	walDir := filepath.Join(f.dataDir, "wal")
	nodeHostDir := filepath.Join(f.dataDir, "nodehost")

	return quicraftconfig.HostConfig{
		WALDir:               walDir,
		NodeHostDir:          nodeHostDir,
		RaftAddress:          f.raftAddress,
		ListenAddress:        f.listen,
		DeploymentID:         f.deploymentID,
		MaxTotalInMemLogSize: f.maxTotalInMemLogSize,
		TransportConfig: quicraftconfig.TransportConfig{
			MTLSConfig: mtls,
		},
		LogConfig: quicraftconfig.LogConfig{
			Format: quicraftconfig.LogFormat(f.logFormat),
			Level:  quicraftconfig.LogLevel(f.logLevel),
		},
	}
}

// buildShardConfig constructs a shard Config from the parsed start flags.
func buildShardConfig(f *startFlags) quicraftconfig.Config {
	cfg := quicraftconfig.Config{
		ShardID:            f.shardID,
		ReplicaID:          f.replicaID,
		MaxInMemLogSize:    f.maxInMemLogSize,
		SnapshotEntries:    f.snapshotEntries,
		CompactionOverhead: f.compactionOverhead,
		LeaseRead:          f.leaseRead,
	}
	if !f.checkQuorum {
		cfg.CheckQuorum = quicraftconfig.BoolPtr(false)
	}
	return cfg
}

// startShard starts the Raft shard using the configured discovery method.
func startShard(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	if f.discoveryMethod == "static" {
		return startShardStatic(f, node, createFn, shardCfg)
	}
	return startShardWithBootstrap(f, node, createFn, shardCfg)
}

// startShardStatic starts a shard with static discovery by parsing the
// --peers flag directly into a member map.
func startShardStatic(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	members, err := parsePeers(f.peers)
	if err != nil {
		return err
	}

	if len(members) == 0 {
		return &NoPeersError{}
	}

	slog.Info("starting shard with static peers",
		"shard_id", f.shardID,
		"members", len(members),
	)

	return node.StartShard(members, f.join, createFn, shardCfg)
}

// startShardWithBootstrap starts a shard using the bootstrapper with the
// configured discovery method.
func startShardWithBootstrap(f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	builder, ok := discoveryBuilders[f.discoveryMethod]
	if !ok {
		return &UnknownDiscoveryError{Name: f.discoveryMethod}
	}

	disc, err := builder(f)
	if err != nil {
		return err
	}

	bootstrapCfg := bootstrap.Config{
		NodeID:      f.replicaID,
		Address:     f.raftAddress,
		ShardID:     f.shardID,
		ReplicaID:   f.replicaID,
		Discovery:   disc,
		CreateFn:    createFn,
		ShardConfig: shardCfg,
	}

	b := bootstrap.NewBootstrapper(bootstrapCfg, node)

	slog.Info("bootstrapping shard",
		"shard_id", f.shardID,
		"discovery", f.discoveryMethod,
	)

	return b.Bootstrap(context.Background())
}

// buildStaticDiscovery creates a StaticDiscovery from the parsed peers.
func buildStaticDiscovery(f *startFlags) (discovery.Method, error) {
	members, err := parsePeers(f.peers)
	if err != nil {
		return nil, err
	}

	peers := make([]discovery.Peer, 0, len(members))
	for id, addr := range members {
		peers = append(peers, discovery.Peer{NodeID: id, Address: addr})
	}

	return discovery.NewStaticDiscovery(discovery.StaticConfig{Peers: peers}), nil
}

// buildMulticastDiscovery creates a MulticastDiscovery from the flags.
func buildMulticastDiscovery(f *startFlags) (discovery.Method, error) {
	if f.multicastSecret == "" {
		return nil, &MissingFlagError{Flag: "multicast-secret"}
	}

	cfg := discovery.MulticastConfig{
		NodeID:       f.replicaID,
		Address:      f.raftAddress,
		DeploymentID: f.deploymentID,
		SharedSecret: []byte(f.multicastSecret),
		MinPeers:     defaultMinPeers - 1, // other peers excluding self
	}

	md, err := discovery.NewMulticastDiscovery(cfg)
	if err != nil {
		return nil, err
	}
	md.Start()

	return md, nil
}

// defaultMinPeers is the default minimum cluster size for bootstrap.
const defaultMinPeers = 3

// buildDNSDiscovery creates a DNSDiscovery from the flags.
func buildDNSDiscovery(f *startFlags) (discovery.Method, error) {
	if f.dnsDomain == "" {
		return nil, &MissingFlagError{Flag: "dns-domain"}
	}

	cfg := discovery.DNSConfig{
		Service: "_raft",
		Proto:   "_udp",
		Domain:  f.dnsDomain,
	}

	return discovery.NewDNSDiscovery(cfg), nil
}

// parsePeers parses a slice of "id=address" strings into a member map.
func parsePeers(peers []string) (map[uint64]string, error) {
	members := make(map[uint64]string, len(peers))
	for _, p := range peers {
		idx := strings.IndexByte(p, '=')
		if idx < 1 {
			return nil, &PeerParseError{Input: p}
		}

		id, err := strconv.ParseUint(p[:idx], 10, 64)
		if err != nil {
			return nil, &PeerParseError{Input: p}
		}

		addr := p[idx+1:]
		if addr == "" {
			return nil, &PeerParseError{Input: p}
		}

		members[id] = addr
	}
	return members, nil
}

// isSingleNode returns true when the peer configuration describes a
// single-node cluster (only the local node). Non-static discovery
// methods always assume multi-node.
func isSingleNode(f *startFlags) bool {
	if f.discoveryMethod != "static" {
		return false
	}
	if len(f.peers) == 0 {
		return true
	}
	if len(f.peers) == 1 {
		members, err := parsePeers(f.peers)
		if err != nil {
			return false
		}
		return len(members) == 1
	}
	return false
}

// ---------------------------------------------------------------------------
// Auth bootstrap
// ---------------------------------------------------------------------------

// authBootstrapResult holds the services and middleware created during
// authentication infrastructure bootstrap.
type authBootstrapResult struct {
	userSvc     user.UserServicer
	roleSvc     role.RoleServicer
	auditLogger audit.Logger
	authMW      *middleware.AuthMiddleware
}

// bootstrapAuth initializes the authentication and authorization
// infrastructure: user store, role store, audit logger, RBAC authorizer,
// and auth middleware. It bootstraps built-in roles and the admin user
// on first run. All operations are idempotent.
//
// Two KV services are required:
//   - bootKVSvc uses StaleRead for bootstrap operations when the Raft
//     ReadIndex pipeline is not yet warmed up.
//   - runtimeKVSvc uses SyncRead (linearizable) for the auth middleware
//     so that follower nodes always see the latest committed state when
//     resolving user identities and permissions at request time.
func bootstrapAuth(ctx context.Context, bootKVSvc, runtimeKVSvc service.KVServicer, dataDir string, node raftNode, shardID uint64) (*authBootstrapResult, error) {
	// Create bootstrap stores backed by StaleRead for initial setup.
	bootStore := newKVServiceAdapter(bootKVSvc, nil, nil)
	bootUserSvc, err := user.NewStore(bootStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot user store", Err: err}
	}
	bootRoleSvc, err := role.NewStore(bootStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot role store", Err: err}
	}

	// Bootstrap: create built-in roles and admin user (idempotent).
	// Uses StaleRead-backed stores since ReadIndex may not be available yet.
	tenantID := tenant.DefaultTenantID
	if err := bootstrapRolesAndAdmin(ctx, node, shardID, bootUserSvc, bootRoleSvc, tenantID); err != nil {
		return nil, err
	}

	// Create runtime stores backed by linearizable SyncRead for the
	// auth middleware. This ensures follower nodes always resolve user
	// identities against the latest committed Raft state.
	runtimeStore := newKVServiceAdapter(runtimeKVSvc, nil, nil)

	userSvc, err := user.NewStore(runtimeStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "user store", Err: err}
	}
	roleSvc, err := role.NewStore(runtimeStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "role store", Err: err}
	}

	// Create audit logger with default config. The file backend writes
	// to the data directory so audit entries survive restarts.
	auditCfg := audit.DefaultConfig()
	auditCfg.FilePath = filepath.Join(dataDir, "audit.log")
	auditLogger, err := audit.New(auditCfg, runtimeStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "audit logger", Err: err}
	}

	// Create RBAC authorizer backed by the runtime role store.
	rbacAuth := rbac.New(roleSvc)

	// Create auth middleware with mTLS authenticator, user-store
	// enrichment, and audit logging. The fallback user service uses
	// StaleRead (local state machine) which is safe because bootstrap
	// ensures user/role data is replicated before servers start. This
	// handles the window after restart where SyncRead (ReadIndex) is
	// not yet operational.
	authMW := middleware.New(rbacAuth,
		middleware.WithAuthenticator(auth.NewMTLSAuthenticator()),
		middleware.WithUserService(userSvc),
		middleware.WithUserServiceFallback(bootUserSvc),
		middleware.WithAuditLogger(auditLogger),
	)

	return &authBootstrapResult{
		userSvc:     userSvc,
		roleSvc:     roleSvc,
		auditLogger: auditLogger,
		authMW:      authMW,
	}, nil
}

// bootstrapRolesAndAdmin creates the built-in roles and admin user for
// the given tenant. This is idempotent: if the admin user already exists,
// the function returns immediately.
//
// In multi-node clusters, only the leader can execute SyncPropose. This
// function attempts creation on all nodes but gracefully falls back to
// waiting for admin replication on followers that receive NotLeader errors.
func bootstrapRolesAndAdmin(ctx context.Context, node raftNode, shardID uint64, userSvc user.UserServicer, roleSvc role.RoleServicer, tenantID string) error {
	// Phase 1: Wait for leader election. All operations (reads on leader
	// via SyncRead, writes via SyncPropose) require a leader.
	if err := waitForLeaderElection(ctx, node, shardID); err != nil {
		return err
	}

	// Phase 2: Check if admin user already exists using local state
	// machine reads (StaleRead via bootstrapHostAdapter). This returns
	// immediately without waiting for ReadIndex.
	if adminExists(ctx, userSvc, tenantID) {
		slog.Debug("auth bootstrap: admin user exists, skipping")
		return nil
	}

	// Phase 3: Attempt to create built-in roles and admin user through
	// Raft consensus. On the leader this succeeds. On followers,
	// SyncPropose returns NotLeader — we catch that and fall back to
	// waiting for the leader's writes to replicate.
	slog.Info("bootstrapping built-in roles and admin user", "tenant", tenantID)
	if err := tryCreateRolesAndAdmin(ctx, userSvc, roleSvc, tenantID); err != nil {
		// Creation failed, likely because this node is a follower.
		// Wait for the leader to create the admin and replicate it.
		slog.Info("auth bootstrap: waiting for leader to replicate admin",
			"reason", err)
		return waitForAdminReplication(ctx, userSvc, tenantID)
	}
	return nil
}

// waitForLeaderElection polls GetLeaderID until a leader is known.
func waitForLeaderElection(ctx context.Context, node raftNode, shardID uint64) error {
	backoff := 50 * time.Millisecond
	const maxAttempts = 30
	for attempt := range maxAttempts {
		_, ok, lerr := node.GetLeaderID(shardID)
		if lerr == nil && ok {
			return nil
		}
		if attempt == maxAttempts-1 {
			return &AuthBootstrapError{
				Step: "wait for leader",
				Err:  fmt.Errorf("no leader after %d attempts", maxAttempts),
			}
		}
		slog.Debug("auth bootstrap: waiting for leader election",
			"attempt", attempt+1, "backoff", backoff)
		select {
		case <-ctx.Done():
			return &AuthBootstrapError{Step: "wait for leader", Err: ctx.Err()}
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 500*time.Millisecond {
			backoff = 500 * time.Millisecond
		}
	}
	return nil
}

// warmupReadIndex is a best-effort attempt to warm up the Raft ReadIndex
// pipeline by performing a SyncRead. After leader election (including
// restart), the leader must commit a no-op entry before ReadIndex is
// operational. This reduces the window where the auth middleware returns
// 503. If it doesn't succeed within a few quick attempts, the function
// returns without blocking — the auth middleware's 503 response and
// client retry logic handle the remaining warm-up period.
func warmupReadIndex(ctx context.Context, kvSvc service.KVServicer) {
	const maxAttempts = 5
	backoff := 200 * time.Millisecond
	for attempt := range maxAttempts {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := kvSvc.Get(readCtx, "__sys:warmup")
		cancel()
		if err == nil {
			return
		}
		// KeyNotFoundError means SyncRead succeeded (ReadIndex pipeline
		// is operational) — the key just doesn't exist.
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if attempt == maxAttempts-1 {
			slog.Debug("warmup ReadIndex: best-effort attempts exhausted, proceeding",
				"attempts", maxAttempts, "last_error", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// adminExists checks whether the admin user already exists using
// a local StaleRead (via bootstrapHostAdapter).
func adminExists(ctx context.Context, userSvc user.UserServicer, tenantID string) bool {
	_, err := userSvc.GetUserByUsername(ctx, tenantID, "admin")
	return err == nil
}

// tryCreateRolesAndAdmin creates built-in roles and the admin user.
// Returns nil on success, or an error if any operation fails. Tolerates
// role-already-exists and user-already-exists for idempotency.
func tryCreateRolesAndAdmin(ctx context.Context, userSvc user.UserServicer, roleSvc role.RoleServicer, tenantID string) error {
	const opTimeout = 5 * time.Second

	builtins := role.BuiltInRoles(tenantID)
	var superadminRoleID string

	for _, r := range builtins {
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		created, createErr := roleSvc.CreateRole(opCtx, &role.CreateRoleRequest{
			ID:          r.ID,
			Name:        r.Name,
			TenantID:    r.TenantID,
			Permissions: r.Permissions,
		})
		cancel()
		if createErr != nil {
			var nameExists *role.RoleNameExistsError
			if errors.As(createErr, &nameExists) {
				// Role already exists — use the deterministic ID
				// from BuiltInRoles which all nodes share.
				if r.Name == "superadmin" {
					superadminRoleID = r.ID
				}
				slog.Debug("built-in role already exists", "name", r.Name)
				continue
			}
			return &AuthBootstrapError{Step: "create role " + r.Name, Err: createErr}
		}
		if r.Name == "superadmin" {
			superadminRoleID = created.ID
		}
		slog.Info("created built-in role", "name", r.Name, "id", created.ID)
	}

	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	adminUser, createErr := userSvc.CreateUser(opCtx, &user.CreateUserRequest{
		Username: "admin",
		TenantID: tenantID,
		RoleIDs:  []string{superadminRoleID},
	})
	cancel()
	if createErr != nil {
		var usernameExists *user.UsernameExistsError
		if errors.As(createErr, &usernameExists) {
			slog.Info("admin user already exists")
			return nil
		}
		return &AuthBootstrapError{Step: "create admin user", Err: createErr}
	}

	slog.Info("created admin user",
		"id", adminUser.ID,
		"username", adminUser.Username,
		"tenant", tenantID,
	)
	return nil
}

// waitForAdminReplication polls the local state machine until the admin
// user appears (replicated from the leader via Raft). Used by follower
// nodes that cannot run SyncPropose themselves.
func waitForAdminReplication(ctx context.Context, userSvc user.UserServicer, tenantID string) error {
	const timeout = 30 * time.Second
	deadline := time.Now().Add(timeout)
	backoff := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		if adminExists(ctx, userSvc, tenantID) {
			slog.Info("auth bootstrap: admin user replicated from leader")
			return nil
		}
		select {
		case <-ctx.Done():
			return &AuthBootstrapError{Step: "wait for admin replication", Err: ctx.Err()}
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > time.Second {
			backoff = time.Second
		}
	}
	return &AuthBootstrapError{
		Step: "wait for admin replication",
		Err:  fmt.Errorf("admin user not replicated within %v", timeout),
	}
}

// handleSIGHUP processes a SIGHUP signal by reloading TLS certificates.
// When the reloader is nil (e.g., when certificates were loaded from
// explicit paths without file watching), the signal is silently ignored.
func handleSIGHUP(reloader *CertReloader, logger *slog.Logger) {
	if reloader == nil {
		return
	}
	if err := reloader.Reload(); err != nil {
		logger.Error("SIGHUP cert reload failed", "error", err)
	} else {
		logger.Info("TLS certificate reloaded via SIGHUP")
	}
}
