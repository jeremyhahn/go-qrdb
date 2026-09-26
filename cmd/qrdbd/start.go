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
	"crypto/ecdsa"
	"crypto/fips140"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/discovery"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdkgo "github.com/jeremyhahn/go-qrdb/sdk/go"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// startFlags holds the parsed CLI flags for the start command.
type startFlags struct {
	mode            string
	configFile      string
	listen          string
	raftAddress     string
	dataDir         string
	deploymentID    uint64
	smType          string
	smBackend       string
	smStorage       string
	discoveryMethod string
	peers           []string
	// memberIdentities are the "replicaID=spki:<hex>" Raft member identities
	// of the other replicas (go-qrdb#556). See config.RaftConfig.MemberIdentities.
	memberIdentities []string
	// shardID is deprecated. System services always use shard.ShardKV (1).
	// Kept for backward compatibility with existing YAML configs and CLI scripts.
	shardID              uint64
	replicaID            uint64
	multicastSecret      string
	multicastGroup       string
	dnsDomain            string
	dnsService           string
	dnsProto             string
	discoveryEndpoint    string
	discoveryToken       string
	discoveryClusterSize int
	discoveryInsecure    bool
	clusterMinPeers      int
	dynamicMembership    bool
	advertiseHost        string
	// host is the bind host every API listener uses (REST/HTTP3, gRPC,
	// QUIC, MCP, MySQL, PgSQL). It reaches them through buildServerConfig
	// -> qrdbconfig.Config.Server.Host -> server.New. Sourced from --host,
	// QRDB_SERVER_HOST or server.host under the documented precedence; its
	// flag default is qrdbconfig.DefaultServerHost, the defaults layer.
	host      string
	adminPort int
	grpcPort  int
	// secureMemoryBudgetMiB bounds the process-wide locked-memory budget every
	// secure.Buffer/Secret this node allocates admits against (installed in
	// runStart via secure.NewAllocatorWithinRLimit + secure.SetAllocator,
	// go-qrdb#114). Sourced from --secure-memory-budget-mib,
	// QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB or server.secure_memory_budget_mib;
	// 0, the default, means "derive from the host" (resolveSecureMemoryBudget).
	secureMemoryBudgetMiB int64
	maxInMemLogSize       uint64
	maxTotalInMemLogSize  uint64
	snapshotEntries       uint64
	compactionOverhead    uint64
	join                  bool
	logFormat             string
	logLevel              string
	debugRaft             bool
	enableREST            bool
	enableGRPC            bool
	enableQUIC            bool
	quicPort              int
	enableUnix            bool
	unixSocketPath        string
	enableMCP             bool
	mcpPort               int
	mcpEndpoint           string
	enableMySQL           bool
	mysqlPort             int
	enablePgSQL           bool
	pgsqlPort             int
	authEnabled           bool
	separationOfDuties    bool
	spiffeTrustDomain     string
	// revocationCacheTTL is the auth.revocation_cache_ttl duration string.
	// Empty means the default. See qrdbconfig.AuthConfig.RevocationCacheTTL —
	// it is the window during which a revoked certificate still authenticates.
	revocationCacheTTL string
	// auditFailurePolicy is the auth.audit_failure_policy string. Empty means
	// the default, "fail-closed". See qrdbconfig.AuthConfig.AuditFailurePolicy
	// — it decides whether a decision whose audit record could not be written
	// is refused or permitted.
	auditFailurePolicy string
	abacEnabled        bool
	abacPolicyDir      string
	// clusterTrustDomain is the cluster-wide SPIFFE trust domain used for
	// node identity. It is encoded into auto-generated node certificates as
	// a URI SAN (spiffe://<clusterTrustDomain>/node/<raftAddress>) and must
	// be the same on every node. The default "qrdb.cluster" is suitable for
	// most deployments; production clusters should use a dedicated domain.
	clusterTrustDomain string
	// peerAdminURLs is a static map from each peer's Raft address to its
	// admin HTTPS base URL. It is populated from cfg.Auth.PeerAdminURLs and
	// pre-seeds the peerAdminRegistry so the cluster key sender can reach
	// follower nodes even when they share a host but use distinct admin ports.
	peerAdminURLs         map[string]string
	strictTenantIsolation bool
	sqlDatabase           string
	tlsCAFile             string
	tlsCertFile           string
	tlsKeyFile            string
	tlsClientAuth         string
	tlsClientAuthSet      bool
	bootstrapNoiseKey     string
	bootstrapNoiseAddr    string
	bootstrapNoisePort    int
	bootstrapNoiseHost    string
	bootstrapSPKIPin      string
	bootstrapDANEDNS      string
	bootstrapDANEDoT      bool
	bootstrapDirectURL    string
	bootstrapAddr         string
	leaseRead             bool
	checkQuorum           bool

	// backupDir roots a durable, file-backed BackupSink used by the Backup
	// service (create/list/get/verify/delete/restore) so backup artifacts
	// survive a daemon restart. Empty (default) keeps backups in-memory only
	// (lost on process exit), matching prior behavior. This directory is also
	// where --restore-backup-id reads the backup artifact from.
	backupDir string

	// restoreBackupID, when non-empty, makes this boot perform an OFFLINE,
	// one-shot restore of the named backup into the primary system shard
	// (shard.ShardKV) BEFORE the shard is started for normal service and
	// before any API listener opens. It requires backupDir. See restore.go.
	restoreBackupID string

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

	// storageConfig holds the storage subsystem configuration loaded
	// from YAML — including volume pools that barrier-mode tenants
	// draw from when allocating a per-tenant Pebble. When no config
	// file is provided, the empty default applies (no pools).
	storageConfig qrdbconfig.StorageConfig

	// embeddingConfig holds the embedding provider configuration loaded from
	// YAML. When no config file is provided, the empty default applies (no
	// providers, embedding disabled).
	embeddingConfig qrdbconfig.EmbeddingConfig

	// ragConfig holds the RAG pipeline configuration loaded from YAML.
	// When no config file is provided, defaults from DefaultConfig are used.
	ragConfig qrdbconfig.RAGConfig

	// tenantConfig holds the tenant defaults (default quotas + entitlements)
	// loaded from YAML, applied to tenants whose own values are unset.
	tenantConfig qrdbconfig.TenantConfig

	// createFn is an optional pre-built state machine factory. When set,
	// initializeNode uses it instead of resolving from the SM registry.
	// This enables callers to inject subsystem-aware factories (e.g.
	// with Lessor, WatchHub, TimeSeries) that share instances with the
	// service layer.
	createFn sm.CreateFunc

	// walBarrier is the system seal barrier wired into the shared Raft
	// LogDB/WAL so committed entries (keys and values) are encrypted at
	// rest under the same barrier that encrypts the state-machine Pebble.
	// It is nil when seal is disabled (plaintext WAL is acceptable then)
	// and is only set after the barrier is unsealed, before the Host is
	// constructed, so the LogDB opens against an available barrier.
	walBarrier quicraft.Barrier

	// walSystemBarrier is the same system barrier as a concrete *seal.EpochBarrier
	// (the underlying type of walBarrier). It is used to open the shared WAL inside
	// the multiplex LogDB and to register it by barrier identity (4d Option C).
	walSystemBarrier *seal.EpochBarrier

	// walMux is the multiplex LogDB wired as the shared host's LogDB in
	// clustered/raft mode when seal is enabled. It owns the shared system-barrier
	// WAL and routes opted-in dedicated-raft shards to per-tenant-KEY WALs. It is
	// nil for single-node/standalone hosts and when seal is disabled, leaving the
	// shared-WAL (Route A) path byte-identical to before.
	walMux *database.MultiplexLogDB
}

// raftNode is the interface that the start command requires for shard
// lifecycle operations. Both *quicraft.Host and test mocks satisfy it.
type raftNode interface {
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg quicraftconfig.Config) error
	GetLeaderID(shardID uint64) (uint64, bool, error)
	Close() error
}

func init() {
	// "pebble" and "mvcc" live in pkg/store, which pkg/statemachine cannot
	// import, so they join the registry here at program startup.
	store.RegisterStateMachines()
}

// clusterOptions translates the parsed start flags into the transport-neutral
// cluster.Options consumed by the discovery engine and bootstrap
// orchestration in pkg/cluster.
func (f *startFlags) clusterOptions() cluster.Options {
	return cluster.Options{
		Method:                 f.discoveryMethod,
		NodeID:                 f.replicaID,
		RaftAddress:            f.raftAddress,
		MinPeers:               f.clusterMinPeers,
		Peers:                  f.peers,
		MulticastSecret:        f.multicastSecret,
		MulticastGroup:         f.multicastGroup,
		DeploymentID:           f.deploymentID,
		DNSDomain:              f.dnsDomain,
		DNSService:             f.dnsService,
		DNSProto:               f.dnsProto,
		TokenEndpoint:          f.discoveryEndpoint,
		Token:                  f.discoveryToken,
		TokenClusterSize:       f.discoveryClusterSize,
		AllowInsecureTokenHTTP: f.discoveryInsecure,
	}
}

// newStartCmd builds the start command. Callers that need to read the
// configuration it resolves use newStartCmdWithFlags instead.
func newStartCmd() *cobra.Command {
	cmd, _ := newStartCmdWithFlags()
	return cmd
}

// resolveStartFlags performs the configuration half of the start command:
// the YAML file named by --config is loaded and merged BENEATH the flags, so
// an explicitly set flag always wins.
//
// Split out of RunE so a caller can resolve a configuration and read the
// result without starting a node. The tests that prove --config is honoured
// had no other way to see the merged values, so they ran the command to
// failure and asserted on WHICH error emerged first -- true only while
// nothing else in the process could fail earlier, which under three
// concurrent copies of the package it could (go-qrdb#84).
func resolveStartFlags(cmd *cobra.Command, f *startFlags) error {
	// Track whether the operator explicitly chose a TLS
	// client-auth mode. Only an explicit selection (flag or
	// YAML) overrides the auth-derived listener default; the
	// flag's literal default is never force-applied because
	// the listener must keep serving certificate-less public
	// endpoints such as /health.
	f.tlsClientAuthSet = cmd.Flags().Changed("tls-client-auth")

	// Load YAML config if --config is provided, then let CLI
	// flags override any YAML values that were explicitly set.
	if f.configFile == "" {
		return nil
	}
	cfg, err := qrdbconfig.Load(f.configFile)
	if err != nil {
		return err
	}
	if err := cfg.ValidateCapabilities(); err != nil {
		return err
	}
	applyConfigToFlags(cmd, f, cfg)
	return nil
}

// newStartCmdWithFlags builds the start command together with the startFlags
// its flags are bound to. newStartCmd is the ordinary entry point; this form
// exists for callers that need to inspect the resolved configuration.
func newStartCmdWithFlags() (*cobra.Command, *startFlags) {
	f := &startFlags{}

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a QuicRaft DB node",
		Long: "Start a QuicRaft DB node. Supports two configuration modes:\n" +
			"  1. YAML config file via --config flag\n" +
			"  2. CLI flags (backward compatible)\n" +
			"CLI flags override YAML values when both are provided.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := resolveStartFlags(cmd, f); err != nil {
				return err
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
	cmd.Flags().StringVar(&f.mode, "mode", "raft", "Run mode: raft (default; single- or multi-node consensus) or standalone (no raft, standard database operations only)")
	cmd.Flags().StringVar(&f.host, "host", qrdbconfig.DefaultServerHost, "Bind host for all API listeners (REST/HTTP3, gRPC, QUIC, MCP, MySQL, PgSQL); maps to server.host / QRDB_SERVER_HOST")
	cmd.Flags().StringVar(&f.listen, "listen", "0.0.0.0:63001", "Listen address (host:port)")
	cmd.Flags().StringVar(&f.raftAddress, "raft-address", "127.0.0.1:63001", "Raft address visible to peers")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "./data", "Data directory for WAL and metadata")
	cmd.Flags().Uint64Var(&f.deploymentID, "deployment-id", 1, "Deployment ID for cluster isolation")
	cmd.Flags().StringVar(&f.smType, "sm", "pebble", "State machine type: pebble (default), mvcc, memory, standard, concurrent, fast, disk, highperf or regular. Every type serves KV, batches, the guarded verbs and transactions; only mvcc keeps revisions")
	cmd.Flags().StringVar(&f.smBackend, "sm-backend", "", "State machine backend: standard, concurrent, fast, disk")
	cmd.Flags().StringVar(&f.smStorage, "sm-storage", "", "Storage backend for the storage-generic state machines (sm type disk/fast): pebble (default), file, memory")
	cmd.Flags().StringVar(&f.discoveryMethod, "discovery", "static", "Discovery method: static (default, single-node), multicast, dns, token")
	cmd.Flags().StringSliceVar(&f.peers, "peers", nil, "Static peers, format id=address (e.g., 1=127.0.0.1:63001)")
	cmd.Flags().StringSliceVar(&f.memberIdentities, "member-identities", nil, "Raft member identities of the other replicas, format id=spki:<hex> (print one with qrdbd node-identity); required for a multi-node cluster")
	cmd.Flags().Uint64Var(&f.shardID, "shard-id", shard.ShardKV, "Shard ID (deprecated: system services always use the system shard)")
	cmd.Flags().Uint64Var(&f.replicaID, "replica-id", 1, "Replica ID")
	cmd.Flags().StringVar(&f.multicastSecret, "multicast-secret", "", "Shared secret (PSK) for multicast authentication; required when --discovery=multicast (no default — the protocol is authenticated-only)")
	cmd.Flags().StringVar(&f.multicastGroup, "multicast-group", "", "Multicast group address host:port (default 239.255.77.77:63777)")
	cmd.Flags().StringVar(&f.dnsDomain, "dns-domain", "", "DNS SRV domain for DNS discovery")
	cmd.Flags().StringVar(&f.dnsService, "dns-service", "_raft", "DNS SRV service label")
	cmd.Flags().StringVar(&f.dnsProto, "dns-proto", "_udp", "DNS SRV protocol label")
	cmd.Flags().StringVar(&f.discoveryEndpoint, "discovery-endpoint", "", "Token discovery service URL (token method)")
	cmd.Flags().StringVar(&f.discoveryToken, "discovery-token", "", "Shared bootstrap token (token method)")
	cmd.Flags().IntVar(&f.discoveryClusterSize, "discovery-cluster-size", 0, "Expected cluster size for token discovery (0 = min peers)")
	cmd.Flags().BoolVar(&f.discoveryInsecure, "discovery-insecure-http", false, "Allow http:// token discovery endpoint (dev/testing only)")
	cmd.Flags().IntVar(&f.clusterMinPeers, "cluster-min-peers", 0, "Minimum cluster size for bootstrap quorum + dynamic-membership removal guard (0 = default 3)")
	cmd.Flags().BoolVar(&f.dynamicMembership, "dynamic-membership", false, "Enable opt-in lease/watch dynamic Raft membership (off by default)")
	cmd.Flags().StringVar(&f.advertiseHost, "advertise-host", "", "Host or IP peers/clients use to reach this node's APIs (default: host portion of --raft-address)")
	cmd.Flags().IntVar(&f.adminPort, "admin-port", 63002, "Admin HTTP server port")
	cmd.Flags().IntVar(&f.grpcPort, "grpc-port", 63003, "gRPC server port")
	cmd.Flags().Int64Var(&f.secureMemoryBudgetMiB, "secure-memory-budget-mib", qrdbconfig.DefaultConfig().Server.SecureMemoryBudgetMiB,
		"Process-wide locked-memory (mlock) budget in MiB for cluster DEKs, credentials and PINs "+
			"(0 = derive from RLIMIT_MEMLOCK: the smaller of "+strconv.FormatInt(qrdbconfig.DefaultSecureMemoryBudgetMiB, 10)+
			" MiB and the limit less "+strconv.FormatInt(secureMemoryHeadroomBytes>>20, 10)+" MiB headroom); "+
			"an explicit value above the host's RLIMIT_MEMLOCK is refused at startup; maps to server.secure_memory_budget_mib / QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB")
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
	cmd.Flags().StringVar(&f.bootstrapNoiseHost, "bootstrap-noise-host", "", "Bind host for the Noise bootstrap server (first node); maps to bootstrap.noise_host, defaults to --host")
	cmd.Flags().StringVar(&f.bootstrapSPKIPin, "bootstrap-spki-pin", "", "Hex SHA-256 SPKI pin for bootstrap server")
	cmd.Flags().StringVar(&f.bootstrapDANEDNS, "bootstrap-dane-dns", "", "DNS server for DANE TLSA lookup (e.g. 8.8.8.8:53)")
	cmd.Flags().BoolVar(&f.bootstrapDANEDoT, "bootstrap-dane-dot", false, "Use DNS-over-TLS for DANE")
	cmd.Flags().StringVar(&f.bootstrapDirectURL, "bootstrap-direct-url", "", "Full HTTPS URL for direct bootstrap")
	cmd.Flags().StringVar(&f.bootstrapAddr, "bootstrap-addr", "", "host:port of first node's admin server (for SPKI/DANE)")
	cmd.Flags().BoolVar(&f.leaseRead, "lease-read", true,
		"Enable lease-based linearizable reads (requires --check-quorum)")
	cmd.Flags().BoolVar(&f.checkQuorum, "check-quorum", true,
		"Enable leader step-down on quorum loss (required for --lease-read)")
	cmd.Flags().StringVar(&f.clusterTrustDomain, "cluster-trust-domain", "qrdb.cluster",
		"Cluster-wide SPIFFE trust domain for node identity (e.g. spiffe://<domain>/node/<raftAddr>); must be identical on all cluster members")
	cmd.Flags().StringVar(&f.backupDir, "backup-dir", "",
		"Directory for a durable, file-backed backup repository (empty = in-memory only, lost on restart)")
	cmd.Flags().StringVar(&f.restoreBackupID, "restore-backup-id", "",
		"Perform a one-shot OFFLINE restore of this backup ID into the system shard before serving traffic (requires --backup-dir)")

	return cmd, f
}

// applyConfigToFlags applies YAML configuration values to startFlags for
// fields that were not explicitly set via CLI flags. CLI flags always take
// precedence over YAML values.
func applyConfigToFlags(cmd *cobra.Command, f *startFlags, cfg *qrdbconfig.Config) {
	if !cmd.Flags().Changed("mode") && cfg.Mode != "" {
		f.mode = cfg.Mode
	}
	if !cmd.Flags().Changed("listen") {
		// f.listen is the Raft host bind address (HostConfig/RaftConfig
		// ListenAddress), not the admin HTTP listener. Source it from the Raft
		// config — the dedicated listen_address when set, else the advertised
		// raft_address — rather than the admin port, which would bind the Raft
		// transport to the wrong port.
		if cfg.Raft.ListenAddress != "" {
			f.listen = cfg.Raft.ListenAddress
		} else {
			f.listen = cfg.Raft.RaftAddress
		}
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
	if !cmd.Flags().Changed("sm-storage") && cfg.Raft.SMStorage != "" {
		f.smStorage = cfg.Raft.SMStorage
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
	if !cmd.Flags().Changed("member-identities") && cfg.Raft.MemberIdentities != "" {
		f.memberIdentities = strings.Split(cfg.Raft.MemberIdentities, ",")
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
	if !cmd.Flags().Changed("multicast-group") && cfg.Discovery.MulticastGroup != "" {
		f.multicastGroup = cfg.Discovery.MulticastGroup
	}
	if !cmd.Flags().Changed("dns-domain") && cfg.Discovery.DNSDomain != "" {
		f.dnsDomain = cfg.Discovery.DNSDomain
	}
	if !cmd.Flags().Changed("dns-service") && cfg.Discovery.DNSService != "" {
		f.dnsService = cfg.Discovery.DNSService
	}
	if !cmd.Flags().Changed("dns-proto") && cfg.Discovery.DNSProto != "" {
		f.dnsProto = cfg.Discovery.DNSProto
	}
	if !cmd.Flags().Changed("discovery-endpoint") && cfg.Discovery.TokenEndpoint != "" {
		f.discoveryEndpoint = cfg.Discovery.TokenEndpoint
	}
	if !cmd.Flags().Changed("discovery-token") && cfg.Discovery.Token != "" {
		f.discoveryToken = cfg.Discovery.Token
	}
	if !cmd.Flags().Changed("discovery-cluster-size") && cfg.Discovery.TokenClusterSize != 0 {
		f.discoveryClusterSize = cfg.Discovery.TokenClusterSize
	}
	if !cmd.Flags().Changed("discovery-insecure-http") && cfg.Discovery.AllowInsecureTokenHTTP {
		f.discoveryInsecure = cfg.Discovery.AllowInsecureTokenHTTP
	}
	if !cmd.Flags().Changed("cluster-min-peers") && cfg.Discovery.MinPeers != 0 {
		f.clusterMinPeers = cfg.Discovery.MinPeers
	}
	if !cmd.Flags().Changed("dynamic-membership") && cfg.Discovery.DynamicMembership {
		f.dynamicMembership = cfg.Discovery.DynamicMembership
	}
	if !cmd.Flags().Changed("advertise-host") && cfg.Server.AdvertiseHost != "" {
		f.advertiseHost = cfg.Server.AdvertiseHost
	}
	if !cmd.Flags().Changed("host") && cfg.Server.Host != "" {
		f.host = cfg.Server.Host
	}
	if !cmd.Flags().Changed("admin-port") {
		f.adminPort = cfg.Server.AdminPort
	}
	if !cmd.Flags().Changed("grpc-port") {
		f.grpcPort = cfg.Server.GRPCPort
	}
	if !cmd.Flags().Changed("secure-memory-budget-mib") {
		f.secureMemoryBudgetMiB = cfg.Server.SecureMemoryBudgetMiB
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
		f.authEnabled = cfg.Auth.IsEnabled()
	}
	// RBAC posture: config can only raise strictness; a FIPS 140-3 build
	// (crypto/fips140.Enabled()) is always strict regardless of config.
	f.separationOfDuties = cfg.Auth.SeparationOfDuties
	f.spiffeTrustDomain = cfg.Auth.SPIFFETrustDomain
	f.revocationCacheTTL = cfg.Auth.RevocationCacheTTL
	f.auditFailurePolicy = cfg.Auth.AuditFailurePolicy
	// ABAC decorator: config-only (no CLI flag), mirroring the SPIFFE
	// trust domain above.
	f.abacEnabled = cfg.Auth.ABAC.Enabled
	f.abacPolicyDir = cfg.Auth.ABAC.PolicyDir
	if !cmd.Flags().Changed("cluster-trust-domain") && cfg.Auth.ClusterTrustDomain != "" {
		f.clusterTrustDomain = cfg.Auth.ClusterTrustDomain
	}
	if len(cfg.Auth.PeerAdminURLs) > 0 {
		m := make(map[string]string, len(cfg.Auth.PeerAdminURLs))
		for _, e := range cfg.Auth.PeerAdminURLs {
			if e.RaftAddr != "" && e.AdminURL != "" {
				m[e.RaftAddr] = e.AdminURL
			}
		}
		if len(m) > 0 {
			f.peerAdminURLs = m
		}
	}
	// Tenant isolation posture mirrors RBAC: config can raise strictness;
	// FIPS-140 always enforces it.
	f.strictTenantIsolation = cfg.Tenant.StrictIsolation
	f.tenantConfig = cfg.Tenant
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
		f.tlsClientAuthSet = true
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
	if !cmd.Flags().Changed("bootstrap-noise-host") && cfg.Bootstrap.NoiseHost != "" {
		f.bootstrapNoiseHost = cfg.Bootstrap.NoiseHost
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

	// Storage config (including volume_pools) is always inherited from
	// YAML — there is no CLI flag equivalent for the multi-path pool
	// definitions that barrier-mode tenants depend on.
	f.storageConfig = cfg.Storage

	// Embedding and RAG configs are always inherited from YAML — there are
	// no CLI flag equivalents for the provider registry or chunking parameters.
	f.embeddingConfig = cfg.Embedding
	f.ragConfig = cfg.RAG
}

// raftListenHost is the host the auto-selected Raft listener BINDS at, and
// raftAdvertiseHost is the host peers DIAL. They differ deliberately, and they
// match the --listen / --raft-address flag defaults; the bind host is the one
// a port probe must use, because it is the wider of the two.
const (
	raftListenHost    = "0.0.0.0"
	raftAdvertiseHost = "127.0.0.1"
)

// resolveAutoConfig auto-generates replica-id, port, and data-dir for
// non-static discovery methods when the user hasn't explicitly set them.
// This allows users to run `qrdbd start --discovery multicast --multicast-secret <16+ byte psk>`
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

	// Every auto-selected port below is probed AT THE HOST IT WILL BE BOUND
	// AT and ON EVERY PROTOCOL ITS LISTENER FAMILY OCCUPIES. Both properties
	// are required and neither implies the other: TCP and UDP are separate
	// port namespaces, and a number free on 127.0.0.1 can be held by another
	// process on a non-loopback address that a wildcard bind then collides
	// with. See freePortOn (go-qrdb#257, go-qrdb#253).

	// Auto-select a free port if listen/raft-address use the default port.
	if !cmd.Flags().Changed("listen") && !cmd.Flags().Changed("raft-address") && f.configFile == "" {
		// The Raft transport is QUIC, so UDP alone. f.raftAddress is the
		// loopback address peers DIAL, not a bind, so the wildcard f.listen
		// is the whole bind surface and the scope to probe at -- and a free
		// wildcard bind implies the loopback one, never the reverse.
		port, err := freePortOn(raftListenHost, networkUDP)
		if err != nil {
			return err
		}
		f.listen = net.JoinHostPort(raftListenHost, strconv.Itoa(port))
		f.raftAddress = net.JoinHostPort(raftAdvertiseHost, strconv.Itoa(port))
		slog.Info("auto-selected port", "port", port)
	}

	// Auto-select admin port if not explicitly set.
	if !cmd.Flags().Changed("admin-port") && f.configFile == "" {
		// BOTH protocols: the REST listener takes this number on TCP and its
		// HTTP/3 sibling co-binds the same number on UDP whenever TLS is
		// configured (pkg/api/rest/server.go). A TCP-only probe is what
		// go-qrdb#257 reports -- the daemon refusing to start on a port the
		// probe called free, because a UDP holder had it all along.
		port, err := freePortOn(f.host, networkTCP, networkUDP)
		if err != nil {
			return err
		}
		f.adminPort = port
		slog.Info("auto-selected admin port", "admin_port", port)
	}

	// Auto-select gRPC port if not explicitly set.
	if !cmd.Flags().Changed("grpc-port") && f.configFile == "" {
		// gRPC is TCP-only and has no same-numbered UDP sibling, so the
		// protocol here is right already. The HOST is what was wrong: this
		// probed loopback while the listener binds f.host.
		port, err := freePortOn(f.host, networkTCP)
		if err != nil {
			return err
		}
		f.grpcPort = port
		slog.Info("auto-selected gRPC port", "grpc_port", port)
	}

	// Auto-select QUIC port if not explicitly set and QUIC is enabled.
	if f.enableQUIC && !cmd.Flags().Changed("quic-port") && f.configFile == "" {
		// UDP-only, and likewise probed at f.host rather than at loopback.
		port, err := freePortOn(f.host, networkUDP)
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

// applyTLSClientAuth applies the operator-selected TLS client-auth mode
// to the bundle's shared listener TLS configuration. It is called only
// when the operator explicitly chose a mode (--tls-client-auth flag or
// YAML tls.client_auth); unset deployments keep the auth-derived
// default (VerifyClientCertIfGiven when auth is enabled, NoClientCert
// otherwise) so certificate-less public endpoints such as /health stay
// reachable. The CA pool is attached as ClientCAs whenever client
// certificates may be presented: it is the verification pool for the
// verifying modes and the acceptable-CA hint in the CertificateRequest
// for the non-verifying ones.
func applyTLSClientAuth(f *startFlags, bundle *TLSBundle) {
	cfg := qrdbconfig.TLSConfig{ClientAuth: f.tlsClientAuth}
	mode := cfg.CryptoTLSClientAuth()
	bundle.TLSConfig.ClientAuth = mode
	if mode != tls.NoClientCert {
		bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool
	}
}

// newSecureAllocator, setSecureAllocator, processSecureAllocator and
// wipeSecureMemory indirect the process-wide, one-shot go-enclave lifecycle
// calls runStart makes, purely for testability.
//
// SetAllocator can be satisfied only once per process, and WipeAll destroys
// the process Sealer's keys PERMANENTLY (enclave.WipeAll's documentation) —
// neither is something a test can undo. This package's own test suite calls
// runStart directly, many times, in one shared test binary (TestRunStart_*,
// the *_FullStartup tests, and every test driving newStartCmdWithFlags's
// RunE through cmd.Execute()), which is many more times than the real
// go-enclave lifecycle can be exercised before the SECOND call fails closed
// on SetAllocator and the FIRST WipeAll poisons sealing for every later test
// in the binary — measured, not theorized: that is exactly what happened
// when these were wired to secure.NewAllocatorWithinRLimit / secure.SetAllocator
// / secure.WipeAll directly. TestMain (signal_guard_test.go) installs
// no-op-safe doubles over newSecureAllocator, setSecureAllocator and
// wipeSecureMemory for the whole test binary (and over
// discoverLockedMemoryLimit, so no test depends on the host's RLIMIT_MEMLOCK);
// production main()/RunE never touches them and gets the real go-enclave
// calls.
//
// processSecureAllocator is NOT read-only in general: when no process
// allocator exists yet, ProcessAllocator installs go-enclave's lazy default.
// runStart calls it only after setSecureAllocator has reported
// ErrAllocatorInstalled — when an allocator already exists — so on that one
// path it reads and installs nothing, which is why TestMain leaves it real.
// It is indirected for the test double the ErrAllocatorInstalled mismatch
// check needs (go-qrdb#114 review, round 3).
var (
	newSecureAllocator     = secure.NewAllocatorWithinRLimit
	setSecureAllocator     = secure.SetAllocator
	processSecureAllocator = secure.ProcessAllocator
	wipeSecureMemory       = secure.WipeAll
)

// recordSecureMemoryWipe sweeps mlocked memory via wipeSecureMemory and
// reports what it found. Split out of runStart, rather than inlined in its
// defer, so it is unit-testable against a synthetic *error without driving
// runStart to a real clean return — which needs a fully started node and
// belongs to the *_FullStartup tests, not to a test of this function alone.
//
// A non-nil *err (an earlier failure already in flight) is never overwritten:
// a wipe problem discovered on the way out of an already-failing runStart is
// still logged, but the ORIGINAL failure is what the caller needs to see and
// act on. Only a clean run (err == nil) is turned into the wipe failure, so a
// caller checking runStart's return value cannot mistake an incomplete wipe
// for success.
func recordSecureMemoryWipe(err *error) {
	remaining, werr := wipeSecureMemory()
	if remaining == 0 && werr == nil {
		return
	}
	wipeErr := &SecureMemoryWipeError{Remaining: remaining, Err: werr}
	slog.Error("secure memory wipe incomplete at shutdown", "error", wipeErr)
	if *err == nil {
		*err = wipeErr
	}
}

// runStart executes the start command logic: load or generate TLS, build
// config, create the host, start the shard, build service layer, create
// the multi-protocol server, and block until a termination signal is
// received.
//
// It is the ONE subcommand that installs the process-wide locked-memory
// budget (setSecureAllocator) and sweeps it on the way out (wipeSecureMemory):
// `start` is the only qrdbd invocation that allocates protected memory —
// cluster DEKs, credentials, PINs — so `status`/`peers`/
// `version` are not made to satisfy an mlock budget they never use
// (go-qrdb#114 review). Both run first/last respectively: the budget install
// is the first statement in this function, before anything else can
// allocate, and its defer is registered first so it is the LAST to run,
// after every other cleanup in this function has released whatever
// protected memory it held. The budget itself comes from
// resolveSecureMemoryBudget: the configured figure when one is set, and
// otherwise one derived from the host's RLIMIT_MEMLOCK; the chosen figure and
// its source are logged once it is installed.
//
// ErrAllocatorInstalled from setSecureAllocator is NOT unconditionally
// treated as a failure: it means a budget already exists — installed by an
// earlier call in this same process, or by a library's lazy default — and
// refusing to start over that would make qrdbd unable to run a second time in
// a process that already has one for an unrelated, entirely benign reason.
// But it is only benign when the ALREADY-installed allocator's own limit
// equals what --secure-memory-budget-mib configured; otherwise the operator's
// configured figure was silently discarded in favor of whatever installed it
// first (typically the RLIMIT-sized lazy default), which is exactly the
// silent-budget-loss go-qrdb#114's review flagged (round 3) — so a mismatch
// is fatal precisely because a MATCH is what makes the non-fatal case safe.
func runStart(f *startFlags) (err error) {
	budgetMiB := f.secureMemoryBudgetMiB
	resolved, rerr := resolveSecureMemoryBudget(budgetMiB)
	if rerr != nil {
		return rerr
	}
	budgetBytes := resolved.bytes
	budget, berr := newSecureAllocator(budgetBytes)
	if berr != nil {
		return &SecureMemoryBudgetError{BudgetMiB: budgetMiB, Err: berr}
	}
	if serr := setSecureAllocator(budget); serr != nil {
		if !errors.Is(serr, secure.ErrAllocatorInstalled) {
			return &SecureMemoryBudgetError{BudgetMiB: budgetMiB, Err: serr}
		}
		installed, paerr := processSecureAllocator()
		if paerr != nil {
			return &SecureMemoryBudgetError{BudgetMiB: budgetMiB, Err: paerr}
		}
		if installedBytes := installed.Limit(); installedBytes != budgetBytes {
			return &SecureMemoryBudgetError{BudgetMiB: budgetMiB, Err: &SecureMemoryBudgetMismatchError{
				ConfiguredBytes: budgetBytes,
				InstalledBytes:  installedBytes,
			}}
		}
	}
	slog.Info("secure memory budget installed",
		"budget_bytes", budgetBytes,
		"source", string(resolved.source),
		"rlimit_memlock_source", resolved.limit.Source.String(),
		"rlimit_memlock_bytes", resolved.limit.Bytes,
	)
	defer recordSecureMemoryWipe(&err)

	// Resolve TLS: explicit files -> cached certs -> generate/bootstrap.
	bundle, bootServers, err := resolveTLS(f, slog.Default())
	if err != nil {
		return err
	}
	// Released on EVERY path out of runStart, not only the signal-shutdown
	// branch far below. There are 34 returns between here and there, and
	// before go-qrdb#241 none of them leaked anything: the cached path -- the
	// one every restart takes -- handed back a nil advertiser, so the absence
	// of the listener was also the absence of the leak. Resuming it gives this
	// path a live resource it never owned, and the existing cleanup was
	// written when it could not.
	//
	// What leaks is not only a socket and a goroutine: the Advertiser holds
	// the Noise static key in protected memory, so an early error would keep
	// cluster key material alive for the life of the process, and the held
	// port would fail the next bind.
	//
	// Stop is nil-safe and idempotent, so this is safe both when no advertiser
	// was started and alongside the explicit Stop on the shutdown path.
	defer bootServers.Stop()

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
	// The reloader's lifetime is THIS function's, so it is released here
	// rather than only on the clean-shutdown path far below.
	//
	// It holds an inotify instance, and inotify instances are a per-user
	// kernel resource, not a per-process one. The clean path already stops it;
	// every other return between here and there did not, leaking one instance
	// for the life of the process. In a daemon that is at most one and the
	// process is exiting anyway — in a TEST BINARY it accumulates across every
	// runStart in the package. Measured before this defer: the cmd/qrdbd
	// TestRunStart family left 38 instances open in one run, against a runner
	// limit of 128, which is what exhausted both CI hosts (go-qrdb#95).
	//
	// Stop is idempotent by design, so the existing call on the shutdown path
	// stays correct and this one is a no-op after it.
	if reloader := bundle.Reloader; reloader != nil {
		defer func() { _ = reloader.Stop() }()
	}

	// Apply an explicitly configured TLS client-auth mode to the shared
	// listener TLS config BEFORE any listener starts. The pre-host
	// sealed-window server reuses bundle.TLSConfig, and the Security
	// Officer share-claim handshake requires the listener to request the
	// client certificate (--tls-client-auth=request) so the handler can
	// verify the officer's cert thumbprint. When the operator does not
	// set a mode the auth-derived default is applied later (see the
	// ClientAuth block ahead of server construction).
	if f.tlsClientAuthSet {
		applyTLSClientAuth(f, bundle)
	}

	// Generate the admin client certificate immediately after TLS is
	// resolved, before any seal/barrier boot phase. Only possible when the
	// CA private key is available (auto-generated single-node TLS) and auth
	// is enabled; with external TLS the operator supplies their own client
	// certs (CAKeyPEM empty -> skipped). This MUST happen before the pre-host
	// sealed-window listener blocks on unseal: on first boot the barrier is
	// uninitialized and stays sealed until an operator drives
	// POST /v1/barrier/initialize, and that operator needs the admin client
	// cert to authenticate. Deferring cert generation until after unseal
	// would deadlock that bootstrap.
	if f.authEnabled && len(bundle.CAKeyPEM) > 0 {
		if err := generateAdminClientCert(bundle, f.dataDir, "admin", tenant.SystemTenantID); err != nil {
			return err
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

	// Initialize database manager. A shard allocator assigns a unique shard ID
	// to every raft-backed database (its Raft config carries ShardID == 0 at
	// create); OpenAll advances the allocator past the highest persisted shard
	// ID so IDs are never reused across restarts. Non-raft databases (Raft ==
	// nil) are unaffected. The allocated IDs drive the dedicated per-database
	// raft shards of IsolationRaft tenants (Phase 4b).
	shardAllocator := shard.NewAllocator()
	dbManager := database.NewManager(slog.Default(), f.dataDir, database.WithAllocator(shardAllocator))
	defer func() { _ = dbManager.Close() }()

	// Rehydrate every database persisted under {dataDir}/databases so that
	// databases created in a prior run are known after restart. This is
	// idempotent with the config-listed open loop below (Open/Create are
	// no-ops for already-open databases).
	if err := dbManager.OpenAll(); err != nil {
		return err
	}

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

	// Register the signal handler before any blocking initialization
	// (seal subsystem build, optional pre-host unseal wait, host
	// construction) so that SIGINT/SIGTERM during these phases triggers
	// a clean cancellation rather than an abrupt process exit.
	sigCtx, sigStop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

	// System-scope enable-encryption recycles the data plane IN-PROCESS: the
	// migration handler enqueues onto migrateCh and the boot loop below tears the
	// data plane down, re-encrypts the system host's stores, adopts the keyed
	// root key, and re-runs the boot body (which rebuilds the seal subsystem from
	// the now-keyed root key). The OS process never exits. Tenant-scope migration
	// is handled separately by the per-tenant Host registry.
	migrateCh := make(chan *systemMigrationRequest, 1)
	sysMigrator := buildSysMigrator(f, migrateCh)
	for {
		var rebootForMigration bool
		bootErr := func() error {
			// Per-iteration context: an in-process migration cancels it so this
			// iteration's long-lived goroutines (lease sweeper, quota/tenant
			// reconcilers, dynamic membership) stop before the data plane is torn
			// down. It is a child of sigCtx, so process shutdown still cancels it.
			bootCtx, bootCancel := context.WithCancel(sigCtx)
			defer bootCancel()

			// Build the seal subsystem BEFORE the state machine factory and
			// before host construction. The data-plane Pebble instance is opened
			// on top of seal/encfs which requires the barrier to be unsealed;
			// we therefore (1) build the barrier+credential+registry up front,
			// (2) thread store.WithEncryptingFS into the SM factory, and (3)
			// run a pre-host sealed-window listener that holds the boot until
			// the operator drives unseal via the public API.
			var sealSub *sealSubsystem
			if f.sealConfig.Enabled {
				var subErr error
				sealSub, subErr = initSealSubsystem(f, sigCtx)
				if subErr != nil {
					return subErr
				}
				defer func() { _ = sealSub.Close() }()
			}

			// Build the volume-pool subsystem when the YAML config declares
			// pools. Barrier-mode tenants registered via RegisterTenant(ctx,
			// id, nil, ...) draw their per-tenant Pebble path from this pool.
			// When no pools are configured the subsystem is nil and the seal
			// registry rejects pooled RegisterTenant calls with
			// ErrPoolNotConfigured — keyspace-mode tenants are unaffected.
			volumePool, err := initVolumePoolSubsystem(f.dataDir, f.storageConfig.VolumePools, slog.Default())
			if err != nil {
				return err
			}
			if volumePool != nil {
				defer func() { _ = volumePool.Close() }()
				// Attach the pool resources to the barrier registry built in
				// initSealSubsystem so RegisterTenant can satisfy nil-backend
				// calls for barrier-mode tenants. When sealSub is nil (seal
				// disabled) the pool subsystem still tracks per-tenant Pebble
				// lifecycle for direct callers that build their own registry.
				if sealSub != nil && sealSub.BarrierRegistry != nil {
					sealSub.BarrierRegistry.Attach(
						seal.WithVolumePool(volumePool.Manager, volumePool.StateStore, volumePool.DefaultPoolName),
						seal.WithPerTenantRegistry(volumePool.PerTenantRegistry),
					)
					if volumePoolAttached != nil {
						volumePoolAttached()
					}
				}
			}

			// Build the single at-rest Encryptor for this node. Every Pebble store
			// opened by the daemon (MVCC state machine, vector storage) routes through
			// this one instance so the barrier-driven encfs wiring lives in one place.
			// A nil sealSub produces a no-op Encryptor; plaintext deployments and tests
			// are unaffected.
			var sealBarrierForEnc *seal.StorageBarrier
			if sealSub != nil {
				sealBarrierForEnc = sealSub.SealBarrier
			}
			enc := atrest.New(sealBarrierForEnc)

			// Wire the same system barrier into the shared Raft LogDB/WAL so the
			// replication log is encrypted at rest, not just the state-machine
			// Pebble. Without this the committed KV entries (keys and values) sit
			// in cleartext in the WAL until log compaction. The underlying
			// EpochBarrier satisfies quicraft.Barrier; it MUST be unsealed before
			// NewHost opens the LogDB. The boot sequence below drives the barrier
			// through the sealed-window unseal (awaitUnseal) before initializeNode
			// constructs the Host, so by the time WithBarrier is engaged the
			// barrier is unsealed — the same precondition the SM Pebble relies on.
			// When seal is disabled sealBarrierForEnc is nil and the WAL stays
			// plaintext (a no-op, consistent with encryption being off).
			if sealBarrierForEnc != nil {
				f.walSystemBarrier = sealBarrierForEnc.EpochBarrier()
				f.walBarrier = f.walSystemBarrier
			}

			// Build the state machine factory with this node's subsystems and
			// its at-rest encryptor; see buildSMCreateFunc.
			if f.mode != capabilities.ModeStandalone {
				createFn, err := buildSMCreateFunc(f, enc, lr, watchHub)
				if err != nil {
					return err
				}
				f.createFn = createFn
			}

			// The ceremony CA signs Security Officer client-certificate CSRs so
			// the resulting certs chain to the same CA the server trusts for
			// mTLS — the prerequisite for the share-free officer claim flow.
			// nil with BYO TLS certificates (no CA private key available).
			ceremonyCA, err := ceremonyCAFromBundle(bundle)
			if err != nil {
				return err
			}

			// Pre-host sealed-window listener. While the barrier is sealed the
			// data plane cannot start (Pebble.Open would fail with ErrSealed
			// because every file open is encfs-wrapped). We bring up an
			// API-only server with the seal services and the sealed-window
			// probe installed, then poll the barrier until the operator drives
			// the public unseal API. Once IsSealed flips false we tear the
			// listener down and let the regular boot continue. The returned
			// seal Node is threaded into BuildServices below so that officer
			// registrations and enclave-held shares from the sealed window
			// survive into the post-unseal service graph.
			var sealedWindowNode *lifecycle.Node
			if sealSub != nil && sealSub.SealBarrier.IsSealed() {
				sealNode, err := awaitUnseal(sigCtx, f, bundle, sealSub, ceremonyCA)
				if err == nil && barrierUnsealed != nil {
					barrierUnsealed()
				}
				var canceled *awaitUnsealCanceledError
				if errors.As(err, &canceled) {
					slog.Info("daemon shutdown during sealed boot")
					return nil
				}
				if err != nil {
					return err
				}
				sealedWindowNode = sealNode
			}

			// Run-mode branch: raft mode constructs the QuicRaft host; mode
			// "standalone" composes the canonical raft-free data plane
			// (lifecycle.NewLocalStorage/NewLocalEngine -> backend.LocalBackend)
			// shared with sdk/go/node.NewStandaloneNode. host stays nil in
			// standalone and every raft-only stage below is guarded on it.
			standalone := f.mode == capabilities.ModeStandalone
			var host *quicraft.Host
			var svcHost service.Host
			var rnode raftNode
			var nodeCloser io.Closer
			if standalone {
				localBackend := database.BackendPebble
				if len(f.databases) > 0 && f.databases[0].Backend != "" {
					localBackend = database.BackendType(f.databases[0].Backend)
				}
				if localBackend == database.BackendRaft {
					return &StandaloneRaftBackendError{}
				}
				var dataPlaneFS vfs.FS
				if sealSub != nil && !sealSub.SealBarrier.IsSealed() && localBackend == database.BackendPebble {
					dataPlaneFS = encfs.New(vfs.Default, sealSub.SealBarrier)
				}
				stg, pdb, lerr := lifecycle.NewLocalStorage(localBackend, f.dataDir, dataPlaneFS, f.storageConfig.Objstore)
				if lerr != nil {
					return lerr
				}
				eng, lerr := lifecycle.NewLocalEngine(localBackend, stg, pdb, watchHub)
				if lerr != nil {
					_ = stg.Close()
					return lerr
				}
				local := backend.NewLocalBackend(eng)
				svcHost = local
				rnode = local
				nodeCloser = localDataPlaneCloser{local: local, storage: stg}
				slog.Info("standalone mode: raft-free data plane composed",
					slog.String("backend", string(localBackend)))
			} else {
				h, err := initializeNode(sigCtx, f, bundle)
				if err != nil {
					return err
				}
				host = h
				svcHost = newHostAdapter(host)
				rnode = host
				nodeCloser = host
			}

			// SIGHUP triggers manual TLS certificate reload (for scripted rotation).
			sighupCh := make(chan os.Signal, 1)
			signal.Notify(sighupCh, syscall.SIGHUP)
			go func() {
				for range sighupCh {
					handleSIGHUP(bundle.Reloader, slog.Default())
					if sighupHandled != nil {
						sighupHandled()
					}
				}
			}()
			defer signal.Stop(sighupCh)

			// Build the service layer over the mode-resolved host. In raft
			// mode a bootstrap-specific adapter reads from the local SM
			// (StaleRead) instead of going through ReadIndex, avoiding a
			// startup deadlock where the ReadIndex pipeline is not yet
			// warmed up after leader election; the LocalBackend has no
			// ReadIndex pipeline, so standalone uses it directly.
			bootHost := svcHost
			if host != nil {
				bootHost = newBootstrapHostAdapter(newHostAdapter(host))
			}
			bootKVSvc := service.NewKVService(bootHost, shard.ShardKV)

			// The seal subsystem (barrier, credential service, barrier registry)
			// was constructed earlier, before host initialization, so the data
			// plane could be opened on top of seal/encfs once the operator drove
			// unseal. Pull the references out here for use in DEK construction
			// and the lifecycle service graph.
			var sealBarrier *seal.StorageBarrier
			var credCoreSvc *credentials.CredentialService
			var barrierRegistry *seal.BarrierRegistry
			if sealSub != nil {
				sealBarrier = sealSub.SealBarrier
				credCoreSvc = sealSub.CredCoreSvc
				barrierRegistry = sealSub.BarrierRegistry
			}

			// Initialize tenant DEK manager for per-tenant data encryption key
			// management. The cluster DEK manager wraps the seal barrier to provide
			// a shared cluster-level DEK that wraps each tenant's isolated DEK.
			// A dedicated PebbleDB directory persists the wrapped tenant DEKs.
			var tenantDEKMgr dek.TenantDEKManager
			if sealBarrier != nil && barrierRegistry != nil {
				clusterDEKMgr, cdErr := dek.NewClusterDEKManager(sealBarrier, slog.Default())
				if cdErr != nil {
					_ = shutdownNode(nodeCloser)
					return &ClusterDEKError{Err: cdErr}
				}
				dekDataDir := filepath.Join(f.dataDir, "dek")
				if err := os.MkdirAll(dekDataDir, 0o700); err != nil {
					_ = shutdownNode(nodeCloser)
					return &DEKStorageError{Err: err}
				}
				// SyncWrites is forced on: the wrapped per-tenant DEKs persisted here
				// have no Raft-replicated backstop, so an unsynced write can be lost
				// on crash while appearing to have succeeded.
				dekStore, dekStoreErr := pebblestore.New(&pebblestore.Config{DataDir: dekDataDir, SyncWrites: true})
				if dekStoreErr != nil {
					_ = shutdownNode(nodeCloser)
					return &DEKStorageError{Err: dekStoreErr}
				}
				defer func() { _ = dekStore.Close() }()
				dekBackend := seal.NewStorageAdapter(dekStore)
				tdm, tdmErr := dek.NewTenantDEKManager(clusterDEKMgr, dekBackend, slog.Default(), nil)
				if tdmErr != nil {
					_ = shutdownNode(nodeCloser)
					return &TenantDEKError{Err: tdmErr}
				}
				tenantDEKMgr = tdm
				slog.Info("tenant DEK manager initialized", slog.String("data_dir", dekDataDir))
			}

			// Construct all services via the shared lifecycle package. Seal-gated
			// services (barrier, ceremony, credential, tenant, cert) are wired when
			// sealBarrier and barrierRegistry are non-nil. The barrier/credential/
			// registry instances reused here are the same ones constructed earlier
			// for the pre-host sealed-window listener; the listener was stopped
			// before initializeNode, so this BuildServices call is the single
			// owner of the barrier services for the rest of the daemon's lifetime.
			// Strict tenant isolation: FIPS-140 always enforces it; operators opt in
			// via config.Tenant.StrictIsolation (stored in f.strictTenantIsolation by
			// applyConfigToFlags). Mirrors the strictSoD resolution pattern above.
			strictTenant := fips140.Enabled() || f.strictTenantIsolation
			if !strictTenant {
				slog.Warn("tenant isolation: standard mode active — un-tenanted vector namespace creation is NOT refused; " +
					"identities with no explicit tenant pool into the 'system' tenant. " +
					"Set tenant.strict_isolation: true (or build with FIPS-140) to enforce per-tenant isolation.")
			}

			// Reuse the ceremony core service from the sealed-window listener when
			// one exists: officer registrations performed while sealed are
			// cache-only and distributed shares live in the service's in-memory
			// enclaves, so constructing a fresh instance here would orphan both.
			var ceremonyCoreSvc *ceremony.CeremonyService
			if sealedWindowNode != nil && sealedWindowNode.Ceremony != nil {
				if coreSvc, ok := service.CeremonyServiceCore(sealedWindowNode.Ceremony); ok {
					ceremonyCoreSvc = coreSvc
				}
			}

			// A durable backup repository makes the Backup service's artifacts
			// survive a daemon restart -- required for disaster recovery, since
			// an in-memory-only backup dies with the very process a DR restore
			// needs to recover from. Opt-in via --backup-dir; the file-backed
			// storage engine holds no OS-level lock, so restore.go's own
			// short-lived open/close against the same directory (during a
			// --restore-backup-id boot) never contends with this one.
			var backupSink service.BackupSink
			if f.backupDir != "" {
				backupFileStore, berr := file.New(f.backupDir)
				if berr != nil {
					_ = shutdownNode(nodeCloser)
					return &RestoreBackupError{Phase: "open-backup-store", Err: berr}
				}
				defer func() { _ = backupFileStore.Close() }()
				backupSink = backupsink.New(backupFileStore)
			}

			// Seal the effective capability profile (build tags x mode x
			// config) once per boot, before the service graph and any
			// transport exists; the same profile is pinned per-instance on
			// the AdminService below.
			capCfg, err := buildServerConfig(f)
			if err != nil {
				return err
			}
			profile := capCfg.EffectiveCapabilities()
			if !standalone {
				// A raft node serves what its state machine serves: the
				// report names the real --sm, not every feature the binary
				// could serve on some other one.
				profile, err = statemachine.NarrowCapabilities(profile, f.smType, f.smBackend)
				if err != nil {
					return &UnknownSMError{Name: f.smType}
				}
			}
			capabilities.Seal(profile)

			svcNode, err := lifecycle.BuildServices(sigCtx, lifecycle.Infra{
				Host:                      svcHost,
				Profile:                   &profile,
				WatchHub:                  watchHub,
				BackupSink:                backupSink,
				DataDir:                   f.dataDir,
				DatabaseManager:           dbManager,
				KVOptions:                 nil,
				SealBarrier:               sealBarrier,
				SealEnabled:               f.sealConfig.Enabled,
				BarrierRegistry:           barrierRegistry,
				TenantDEKManager:          tenantDEKMgr,
				CredentialCoreSvc:         credCoreSvc,
				CeremonyDataDir:           filepath.Join(f.dataDir, "seal"),
				CeremonyCA:                ceremonyCA,
				CeremonyCoreSvc:           ceremonyCoreSvc,
				Logger:                    slog.Default(),
				EmbeddingConfig:           f.embeddingConfig,
				RAGConfig:                 f.ragConfig,
				StrictTenantIsolation:     strictTenant,
				DefaultTenantQuotas:       f.tenantConfig.DefaultQuotas.ToQuotas(),
				DefaultTenantEntitlements: f.tenantConfig.DefaultEntitlements.ToEntitlements(),
				Encryptor:                 enc,
			})
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			// Guarded so the system-migration path (which closes svcNode early to
			// release its vector-store Pebble lock for re-encryption, then nils it)
			// does not double-Stop — lifecycle.Node.Stop is not idempotent.
			defer func() {
				if svcNode != nil {
					_ = svcNode.Stop(context.Background())
				}
			}()

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
			authKVSvc := service.NewKVService(svcHost, shard.ShardKV)

			// Always bootstrap user and role stores. These are organizational
			// primitives that operators may need regardless of whether auth
			// enforcement is enabled (e.g. pre-provisioning identities before
			// enabling auth, managing users in dev/test with auth disabled).
			//
			// Two KV services are used:
			//   - bootKVSvc (StaleRead) for initial bootstrap operations when
			//     ReadIndex is not yet warmed up.
			//   - authKVSvc (SyncRead, no codec) for runtime store operations,
			//     ensuring linearizable reads for user/role CRUD.
			//
			// Resolve the RBAC posture once: a FIPS 140-3 build forces strict
			// separation of duties; otherwise the config flag selects it. The
			// value flows into both the first-boot role provisioning and the RBAC
			// authorizer so seeding and enforcement agree.
			strictSoD := fips140.Enabled() || f.separationOfDuties
			slog.Info("RBAC posture resolved",
				slog.Bool("separation_of_duties", strictSoD),
				slog.Bool("fips140", fips140.Enabled()),
				slog.Bool("config_separation_of_duties", f.separationOfDuties),
			)
			// When seal is enabled the system barrier has already been
			// initialized+unsealed out-of-band (admin barrier API for passphrase, the
			// SO ceremony for shamir) during awaitUnseal above. Route first-boot
			// through the seal-aware bootstrap.BootstrapSystem so the orchestrator
			// observes the already-unsealed barrier (BarrierAlreadyInitialized) and
			// runs first-boot without demanding any credential. The strategy mapping
			// mirrors initSealSubsystem: "shamir" → shamir, everything else (the
			// daemon's deferred-software default) → passphrase. When seal is disabled
			// sealSub is nil and bootstrapUserRoleStores keeps the direct
			// firstboot.Initialize path (BootstrapSystem requires a barrier registry).
			var bootBarriers *seal.BarrierRegistry
			bootStrategy := bootstrap.StrategyPassphrase
			if sealSub != nil {
				bootBarriers = sealSub.BarrierRegistry
				if f.sealConfig.Strategy == "shamir" {
					bootStrategy = bootstrap.StrategyShamir
				}
			}
			// Compliance-grade AuditLogger is always constructed, regardless of
			// --auth-enabled, because FIPS/FedRAMP/PCI-DSS/HIPAA require an audit
			// trail even in environments where RBAC is disabled. The AuditLogger is
			// injected into all protocol servers so the /v1/audit/events endpoint
			// is available and seal/ceremony/custodian operations emit events. It is
			// built BEFORE the user/role stores so IAM mutations (create/update/
			// delete/enable/disable, role permission changes) emit an audit trail.
			runtimeKVAdapter := newKVServiceAdapter(authKVSvc, nil, nil)
			chainFile, err := audit.NewFileLogger(filepath.Join(f.dataDir, "audit-events.ndjson"))
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return &AuthBootstrapError{Step: "compliance audit file", Err: err}
			}
			complianceAudit := audit.NewAggregateLogger(
				chainFile,
				audit.NewRingBuffer(10000),
				audit.NewSlogSink(),
			)
			defer func() { _ = complianceAudit.Close() }()
			_ = runtimeKVAdapter // used below when auth is enabled

			mgmtResult, err := bootstrapUserRoleStores(sigCtx, bootKVSvc, authKVSvc, txSvc, rnode, shard.ShardKV, f.replicaID, tenantSvcInst, strictSoD, bootBarriers, bootStrategy, complianceAudit)
			if err != nil {
				_ = shutdownNode(nodeCloser)
				if sigCtx.Err() != nil {
					slog.Info("user/role store bootstrap canceled by shutdown signal")
					return nil
				}
				return err
			}

			// Attach the compliance audit logger to the seal subsystem's
			// credential service (built before the logger existed) so credential
			// submit/store/rotate/delete/gc emit an audit trail.
			if sealSub != nil && sealSub.CredCoreSvc != nil {
				sealSub.CredCoreSvc.SetAuditLogger(complianceAudit)
			}

			// Bootstrap authentication enforcement (RBAC, legacy audit logger, auth
			// middleware, custodian service, SO authenticator, auth factor service).
			// Only enabled when --auth-enabled=true. The user and role stores are
			// already initialized above and reused here.
			var authResult *authBootstrapResult
			if f.authEnabled {
				authResult, err = bootstrapAuthEnforcement(sigCtx, mgmtResult, f.dataDir, rnode, complianceAudit, strictSoD, f.spiffeTrustDomain, qrdbconfig.AuthABACConfig{Enabled: f.abacEnabled, PolicyDir: f.abacPolicyDir}, svcNode.CertProv, f.revocationCacheTTL, f.auditFailurePolicy)
				if err != nil {
					_ = shutdownNode(nodeCloser)
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

			// Phase 2: make the volume pool a cluster-wide replicated concept. Bind the
			// pool's round-robin cursor to the replicated system KV (so the cursor is a
			// single value that survives leader failover) and attach the replicated
			// tenant→relative-slot assignment store to the barrier registry (so every
			// replica resolves the SAME relative slot for a tenant — realized at its own
			// local pool base — instead of running a divergent node-local pool.Pick).
			// Both must be wired before RestoreVolumeTenants below, which seeds the
			// assignment from node-local DataRoot records for pre-Phase-2 clusters.
			if volumePool != nil && kvSvc != nil {
				if volumePool.ReplicatedState != nil {
					if bindErr := volumePool.ReplicatedState.Bind(lifecycle.NewKVStoreAdapter(kvSvc)); bindErr != nil {
						slog.Default().Warn("start: failed to bind replicated volume-pool cursor; falling back to node-local pick",
							slog.Any("error", bindErr))
					}
				}
				if barrierRegistry != nil {
					barrierRegistry.Attach(seal.WithReplicatedSlotStore(
						lifecycle.NewReplicatedSealBackend(kvSvc, txSvc)))
				}
			}

			// Build per-tenant isolation infrastructure (IsolationVolume host
			// registry, IsolationRaft factory, TenantShardReconciler). Any
			// registries opened by the composer are closed on error before it
			// returns, so only shutdownNode is needed here on failure.
			ti, err := server.BuildTenantInfra(server.TenantInfraConfig{
				Ctx:            sigCtx,
				BarrierReg:     barrierRegistry,
				TenantSvc:      tenantSvcInst,
				VolumePool:     volumePool,
				Logger:         slog.Default(),
				Host:           host,
				Manager:        dbManager,
				ReplicaID:      f.replicaID,
				RaftAddr:       f.raftAddress,
				SystemTenant:   tenant.SystemTenantID,
				PlacementStore: svcNode.PlacementStore,
				MultiplexLogDB: f.walMux,
			})
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			tenantHostReg := ti.Hosts
			raftBackendReg := ti.Raft
			if ti.Reconciler != nil {
				go ti.Reconciler.Run(bootCtx)
			}
			// Wire the IsolationRaft factory into the already-built backup
			// service as its ShardRestarter, enabling ONLINE restore (the
			// guarded stop -> import -> restart sequence, see
			// service.RestoreShardOnline) for dedicated per-database Raft
			// shards. This must happen here rather than at BuildServices time:
			// raftBackendReg does not exist until BuildTenantInfra runs, which
			// itself depends on svcNode.PlacementStore from BuildServices.
			if raftBackendReg != nil && svcNode.Backup != nil {
				service.SetShardRestarter(svcNode.Backup, raftBackendReg)
			}

			// Storage-byte quota accountant (Phase 3). Enforces each non-system
			// tenant's MaxStorageBytes on the write path (rejecting over-limit writes
			// with quota.ExceededError -> HTTP 429) and accumulates usage in memory;
			// the reconciler below flushes it to the persisted counter and corrects
			// drift. Requires the tenant service (the quota.QuotaProvider + the
			// Increment/Decrement usage mutator). The wrapper installs only for tenants
			// with a positive limit, so unlimited/system workloads are unaffected.
			quotaAccountant := buildQuotaAccountant(tenantSvcInst)

			// Build leader forwarding resolver so writes on non-leader nodes
			// are transparently proxied to the current leader. Uses HTTPS scheme
			// since the admin server now runs TLS.
			adminPortStr := strconv.Itoa(f.adminPort)
			registry := rest.NewPeerAdminRegistryTLS(adminPortStr)

			// Register the local node so other nodes forwarding back to us can
			// be detected as a loop (and for symmetry with peer registrations).
			//
			// The entry is keyed by f.raftAddress -- the identity peers use --
			// so its value must be the URL a PEER would reach us on, which is
			// the advertise host. A loopback literal here published "reach me
			// at 127.0.0.1" into the shared registry (go-qrdb#167), which is
			// also strictly worse than the registry's own derivation from the
			// Raft address host. A node whose advertise host is a wildcard has
			// no such URL, so it registers nothing rather than a wrong one.
			if selfAdminURL, ok := selfAdminURL(f); ok {
				registry.Register(f.raftAddress, selfAdminURL)
			} else {
				slog.Warn("no advertisable host for this node's admin URL; skipping self-registration in the peer registry",
					"raft_address", f.raftAddress,
					"advertise_host", f.advertiseHost)
			}

			// Pre-register peer admin URLs from config (Phase 4c-1c). This is required
			// when cluster nodes share a host but use distinct admin ports (e.g.
			// integration tests). In production clusters where every node uses the
			// same admin port on a distinct host, the fallback derivation inside
			// peerAdminRegistry.ResolveAdminURL is sufficient and this map is empty.
			for peerRaftAddr, peerAdminURL := range f.peerAdminURLs {
				if peerRaftAddr == f.raftAddress {
					continue // local node already registered above
				}
				registry.Register(peerRaftAddr, peerAdminURL)
			}

			// Phase 4c-1c: Cluster peer key-transport (clusterkeys). Build the HPKE
			// receiver and sender only when the seal subsystem and placement table are
			// both present (clustered, IsolationRaft-capable deployment). Single-node or
			// non-clustered nodes skip this and leave the routes un-mounted.
			//
			// clusterTrustDomain is the single invariant that ties the node cert SPIFFE
			// SAN (stamped into auto-generated certs by resolveTLS), the receiver's
			// peer-cert verification, and the sender's recipient-cert authentication.
			// All three MUST use the same value; it is sourced from f.clusterTrustDomain
			// which defaults to "qrdb.cluster" and can be overridden via the
			// --cluster-trust-domain flag or YAML auth.cluster_trust_domain.
			var ckReceiver *clusterkeys.Receiver
			var ckSender *clusterkeys.Sender
			// nodePriv is lifted to the boot-body scope so the Phase D cluster
			// enable-encryption coordination (below) can reuse the node's HPKE key
			// for sealing/opening per-node migration credentials.
			var nodePriv *ecdsa.PrivateKey

			// The cluster key plane is a THIRD certificate-authorization site,
			// alongside the mTLS authenticator and REST's on-behalf forwarding
			// path. Its routes are on the REST public allowlist by design — they
			// authorize by node identity plus the placement table, not user RBAC —
			// so they never reach the middleware and inherit none of its checks.
			// Reuse the middleware's own checker rather than building a second
			// cache over the same provider: one cache means
			// auth.revocation_cache_ttl is one window for this node rather than
			// one per plane.
			//
			// Nil when --auth-enabled=false, because no middleware is built then.
			// That leaves the key plane exactly as unenforced as every other plane
			// on such a node.
			var ckRevocation auth.RevocationChecker
			if authResult != nil {
				ckRevocation = authResult.authMW.RevocationChecker()
			}
			if host != nil && barrierRegistry != nil && svcNode.PlacementStore != nil {
				// Parse the node's ECDSA P-256 private key from the TLS bundle. This is
				// the same key that the node certificate was issued for, making it the
				// HPKE recipient key (DHKEM-P-256). A parse failure disables key transport
				// rather than aborting the daemon — single-node and passphrase-sealed
				// deployments do not strictly need this.
				if block, _ := pem.Decode(bundle.KeyPEM); block != nil {
					if p, parseErr := x509.ParseECPrivateKey(block.Bytes); parseErr == nil {
						nodePriv = p
					} else {
						slog.Warn("clusterkeys: failed to parse node private key, peer key transport disabled",
							slog.Any("error", parseErr))
					}
				}
				if nodePriv != nil {
					kdReceiver, kdErr := keydelivery.NewReceiver(nodePriv, f.raftAddress)
					if kdErr != nil {
						slog.Warn("clusterkeys: key delivery receiver init failed, peer key transport disabled",
							slog.Any("error", kdErr))
					} else {
						var ckrErr error
						ckReceiver, ckrErr = clusterkeys.NewReceiver(
							kdReceiver,
							barrierRegistry,
							svcNode.PlacementStore,
							f.clusterTrustDomain,
							f.raftAddress,
							bundle.CACertPool,
							ckRevocation,
							bundle.CertPEM,
							slog.Default(),
						)
						if ckrErr != nil {
							slog.Warn("clusterkeys: cluster key receiver init failed, peer key transport disabled",
								slog.Any("error", ckrErr))
						}
					}
				}
			}
			if ckReceiver != nil {
				// Build an mTLS *http.Client presenting the node cert and trusting the
				// cluster CA pool. TLS 1.3 is pinned; the cert is loaded from the bundle
				// to avoid touching the file system after hot-reload may have swapped it.
				nodeCert, certErr := tls.X509KeyPair(bundle.CertPEM, bundle.KeyPEM)
				if certErr != nil {
					slog.Warn("clusterkeys: failed to build mTLS client cert, sender disabled",
						slog.Any("error", certErr))
				} else {
					mtlsTransport := &http.Transport{
						TLSClientConfig: &tls.Config{
							Certificates: []tls.Certificate{nodeCert},
							RootCAs:      bundle.CACertPool,
							MinVersion:   tls.VersionTLS13,
						},
						ForceAttemptHTTP2: true,
					}
					mtlsClient := &http.Client{
						Timeout:   15 * time.Second,
						Transport: mtlsTransport,
					}
					var senderErr error
					ckSender, senderErr = clusterkeys.NewSender(
						registry,
						mtlsClient,
						bundle.CACertPool,
						f.clusterTrustDomain,
						ckRevocation,
						slog.Default(),
					)
					if senderErr != nil {
						slog.Warn("clusterkeys: sender init failed, peer key distribution disabled",
							slog.Any("error", senderErr))
					}
				}
			}
			// Post-construction injection: wire the sender into the DatabaseService.
			// lifecycle.BuildServices constructs DatabaseService before the sender is
			// available (the sender needs the LeaderAdminResolver which is created
			// here), so SetDistributor is used instead of a construction-time option.
			if ckSender != nil {
				if concrete, ok := dbSvc.(*service.DatabaseService); ok {
					concrete.SetDistributor(ckSender, barrierRegistry, f.raftAddress)
				}
			}

			// Phase 4c-2: post-construction injection into RebalanceService.
			// lifecycle.BuildServices constructs RebalanceService before the
			// clusterkeys.Sender (and the placement store) are available, so
			// SetRebalanceDeps mirrors the SetDistributor pattern used above.
			// All three dependencies are nil-guarded: when any is absent (single-node
			// or non-IsolationRaft deployment) SetRebalanceDeps disables the
			// dedicated-tenant MOVE path and the generic system-shard path is used.
			if svcNode.Rebalance != nil {
				if concrete, ok := svcNode.Rebalance.(*service.RebalanceService); ok {
					concrete.SetRebalanceDeps(
						svcNode.PlacementStore,
						ckSender,
						barrierRegistry,
						f.raftAddress,
					)
				}
			}

			// Phase 4c-1d: wire the Raft host into DatabaseService so CreateForTenant
			// can call AddFollowerNode for each follower replica immediately after the
			// placement is persisted, and into RebalanceService so a dedicated-tenant
			// shard MOVE can admit its destination replica observer-first
			// (RaftHost.AddObserver) and evict its source replica
			// (RaftHost.RemoveFollowerNode) -- see RebalanceService.moveTenantGroupShard.
			// raftBackendReg being non-nil implies the shared host is configured for
			// IsolationRaft tenants. The raftFollowerAdder adapter wraps
			// host.RequestAddNode/RequestAddObserver/RequestRemoveNode + rs.Result()
			// into the narrow service.RaftHost interface; the SAME instance is shared
			// by both services since it is stateless beyond the host reference.
			if raftBackendReg != nil {
				raftHostAdapter := &raftFollowerAdder{host: host}
				if concrete, ok := dbSvc.(*service.DatabaseService); ok {
					concrete.SetRaftHost(raftHostAdapter)
				}
				if svcNode.Rebalance != nil {
					if concrete, ok := svcNode.Rebalance.(*service.RebalanceService); ok {
						concrete.SetRaftHost(raftHostAdapter)
					}
				}
			}

			// Phase D: cluster-wide enable-encryption coordination. Wired only when
			// the replicated coordination store and the node HPKE key are both
			// available (a TLS-secured, KV-backed deployment). When present, the
			// transports write a replicated migration intent (with per-node sealed
			// credentials) via clusterMig and every node's reconciler performs its
			// local recycle; otherwise the direct system migrator is used unchanged.
			var clusterMig *clusterCoordinatorMigrator
			if host != nil && svcNode.ClusterMigStore != nil && nodePriv != nil && barrierRegistry != nil {
				localMig := server.NewScopedEncryptionMigrator(
					sysMigrator, tenantHostReg, raftBackendReg, barrierRegistry, tenantSvcInst)
				topology := service.NewHostTopologyProvider(raftbackend.New(host), svcNode.Rebalance)
				ownerResolver := server.NewClusterOwnerResolver(
					svcNode.PlacementStore, topology, shard.ShardKV, f.raftAddress)
				sealer := &daemonCredentialSealer{
					sender: ckSender, selfNode: f.raftAddress, selfPub: &nodePriv.PublicKey,
				}
				localReplicaID := f.replicaID
				isLeader := func() bool {
					id, ok, lerr := host.GetLeaderID(shard.ShardKV)
					return lerr == nil && ok && id == localReplicaID
				}
				coord := clustermig.NewCoordinator(svcNode.ClusterMigStore, ownerResolver, sealer,
					isLeader, func() int64 { return time.Now().Unix() })

				migReceiver, mrErr := keydelivery.NewReceiver(nodePriv, f.raftAddress,
					keydelivery.WithTTL(migrationCredentialTTL), keydelivery.WithoutReplayGuard())
				if mrErr != nil {
					slog.Warn("cluster enable-encryption disabled: credential receiver init failed",
						slog.Any("error", mrErr))
				} else {
					clusterMig = &clusterCoordinatorMigrator{
						coord:      coord,
						barrierReg: barrierRegistry,
						modeCheck:  tenantSvcInst,
					}
					reconciler := clustermig.NewReconciler(svcNode.ClusterMigStore, f.raftAddress,
						&daemonLocalRecycler{migrator: localMig, barrierReg: barrierRegistry},
						&daemonCredentialOpener{receiver: migReceiver, selfNode: f.raftAddress},
						func() int64 { return time.Now().Unix() }, slog.Default())
					go runClusterMigReconciler(bootCtx, reconciler, coord, svcNode.ClusterMigStore)
					slog.Info("cluster enable-encryption coordination enabled",
						slog.String("node", f.raftAddress))
				}
			}

			// Build server configuration using the QRDB config type required by
			// the server.New constructor.
			serverCfg, err := buildServerConfig(f)
			if err != nil {
				return err
			}

			// Configure mTLS on the TLS listener. An explicit operator selection
			// (--tls-client-auth flag or YAML tls.client_auth) was already applied
			// after resolveTLS and always wins. Otherwise the mode is derived from
			// the auth posture: when auth is enabled, VerifyClientCertIfGiven
			// allows public endpoints (like /health) to work without a client
			// certificate while still validating certs when provided; when auth
			// is disabled, NoClientCert disables client cert verification
			// entirely.
			if !f.tlsClientAuthSet {
				if f.authEnabled {
					bundle.TLSConfig.ClientAuth = tls.VerifyClientCertIfGiven
					bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool
				} else {
					bundle.TLSConfig.ClientAuth = tls.NoClientCert
				}
			}

			// Create and start the multi-protocol server.
			serverOpts := []server.ServerOption{
				server.WithTLS(bundle.TLSConfig),
				server.WithCACertPool(bundle.CACertPool),
				server.WithLeaderForwarding(registry),
				server.WithBatch(batchSvc),
				server.WithTimeSeries(tsSvc),
				server.WithIndex(indexSvc),
				server.WithVector(vectorSvc),
				server.WithCollectionService(collectionSvc),
				server.WithDatabaseService(dbSvc),
				server.WithDatabaseManager(dbManager),
				// The seam that routes a volume-isolated tenant's DROP
				// DATABASE reclamation to its dedicated per-tenant Host
				// rather than the system store (go-qrdb#412). The reclaim
				// service itself is already wired into dbManager by
				// lifecycle.BuildServices; only the tenant routing is the
				// server's to supply.
				server.WithKeyspaceReclaimService(svcNode.KeyspaceReclaim),
				// The reclaim sweeper (go-qrdb#547), built by
				// lifecycle.BuildServices and started below: the server
				// installs the resolver that routes each pending job to
				// the node that sweeps it and to its store, through the
				// same registry the transports resolve requests with.
				server.WithReclaimSweeper(svcNode.ReclaimSweeper),
			}
			// The transaction/CAS family is served in BOTH modes (owner
			// ruling 2026-08-23, go-qrdb#62): a standalone node is
			// engine-direct and single-process, so a compare-and-swap there
			// is already linearizable without consensus. It is wired as a
			// singleton here and passed through by the gated per-database
			// registry, so the network surface matches what the SDK's
			// in-process transport serves.
			//
			// The remaining raft-only API surface is composed only in raft
			// mode; in standalone the transports refuse those families with a
			// typed "requires mode: raft" error (nil singletons + the gated
			// per-database registry, see server.WithStandaloneMode). The
			// underlying engine still serves MVCC operations internally for
			// the SQL catalog and collections.
			serverOpts = append(serverOpts, server.WithTransaction(txSvc))
			if standalone {
				serverOpts = append(serverOpts, server.WithStandaloneMode())
			} else {
				serverOpts = append(serverOpts,
					server.WithHost(host),
					server.WithMVCC(mvccSvc),
					server.WithLease(leaseSvc),
					server.WithLockService(lockSvc),
					server.WithWatch(watchSvc),
				)
			}
			// User and role management services are always wired — they are
			// organizational primitives independent of auth enforcement. The
			// SERVED services are the StoreResolver-routed variants (when a
			// barrier registry is present): a barrier-isolated tenant's IAM is
			// persisted in its own TenantBarrier-backed store and a SEALED
			// barrier tenant's IAM fails closed (tenant.ErrTenantSealed → 503)
			// rather than leaking through the shared system store. System/prefix
			// tenants resolve transparently to the shared store.
			serverOpts = append(serverOpts,
				server.WithUserService(mgmtResult.servedUserSvc),
				server.WithRoleService(mgmtResult.servedRoleSvc),
			)
			// Compliance audit logger is always wired so the /v1/audit/events
			// endpoint is available regardless of auth-enabled state.
			serverOpts = append(serverOpts, server.WithAuditLogger(complianceAudit))

			// Auth factor service is constructed tenant-routed (factor
			// implementations return ErrFactorDisabled for absent hardware,
			// which handlers map to HTTP 501 -- the correct response for
			// "feature not available in this deployment"). Exposed via
			// /v1/authfactors/* for consumers like xkms. See
			// buildAuthFactorServiceOption for why this is the routed
			// service rather than the flat one authKVSvc would give it
			// (go-qrdb#464), and why there is no migration for records a
			// previous build wrote flat.
			if opt, err := buildAuthFactorServiceOption(mgmtResult.storeResolver, defaultAuthFactors()); err == nil {
				serverOpts = append(serverOpts, opt)
			} else {
				slog.Error("auth factor service disabled", slog.Any("error", err))
			}

			// User-side N-factor service (M14) is always constructed, giving
			// the standalone daemon the same "qrdb auth login" / "qrdb auth
			// factor" surface the SDK's embedded transport already exposes
			// (sdk/go/node.go buildLoginOpts). See buildUserAuthFactorServiceOption.
			if opt, err := buildUserAuthFactorServiceOption(
				sigCtx, mgmtResult.storeResolver, mgmtResult.legacyFactorStore, slog.Default(),
			); err == nil {
				serverOpts = append(serverOpts, opt)
			} else {
				slog.Error("user auth-factor service (M14) disabled", slog.Any("error", err))
			}

			if authResult != nil {
				serverOpts = append(serverOpts,
					server.WithAuthMiddleware(authResult.authMW),
				)
				if authResult.custodianSvc != nil {
					serverOpts = append(serverOpts, server.WithCustodianService(authResult.custodianSvc))
				}
				if authResult.soAuthenticator != nil {
					serverOpts = append(serverOpts, server.WithSOAuthenticator(authResult.soAuthenticator))
				}
			}
			if barrierSvc != nil {
				serverOpts = append(serverOpts, server.WithBarrierService(barrierSvc))
			}
			if ceremonySvc != nil {
				serverOpts = append(serverOpts, server.WithCeremonyService(ceremonySvc))
			}
			if ceremonyCA != nil {
				serverOpts = append(serverOpts, server.WithCeremonyCA(ceremonyCA))
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
			// When the storage barrier is enabled, install the sealed-window
			// middleware on REST and the equivalent interceptors on gRPC.
			// While the barrier is sealed every endpoint not on the allowlist
			// returns 503 / Unavailable; once unsealed both wrappers become
			// passthrough. See pkg/api/rest/middleware_sealed.go and
			// pkg/api/grpc/sealed_window_interceptor.go for the canonical
			// allowlists.
			if sealBarrier != nil {
				serverOpts = append(serverOpts, server.WithSealedWindowProbe(sealBarrier))
			}
			if tenantDEKMgr != nil {
				serverOpts = append(serverOpts, server.WithTenantDEKManager(tenantDEKMgr))
			}
			if tenantHostReg != nil {
				serverOpts = append(serverOpts, server.WithTenantHostRegistry(tenantHostReg))
			}
			// System-scope enable-encryption migrator. The server's scoped-migrator
			// composite routes ScopeSystem here (in-process recycle via the boot loop)
			// and ScopeTenant to the tenant Host registry above.
			if !standalone {
				serverOpts = append(serverOpts, server.WithEncryptionMigrator(sysMigrator))
			}
			// Phase D: when cluster coordination is wired, the transports hit the
			// coordinator (write replicated intent + seal per-node credentials)
			// instead of recycling locally; the per-node reconcilers do the recycle.
			if clusterMig != nil {
				serverOpts = append(serverOpts, server.WithClusterEncryptionMigrator(clusterMig))
			}
			if raftBackendReg != nil {
				serverOpts = append(serverOpts, server.WithRaftBackendFactory(raftBackendReg))
			}
			if quotaAccountant != nil {
				serverOpts = append(serverOpts, server.WithQuotaAccountant(quotaAccountant))
			}
			if ckReceiver != nil {
				serverOpts = append(serverOpts, server.WithClusterKeyReceiver(ckReceiver))
			}
			if tenantSvcInst != nil {
				serverOpts = append(serverOpts, server.WithTenantService(tenantSvcInst))
				// The structurally-privileged system tenant is created earlier in
				// startup by pkg/firstboot.Initialize (called from
				// bootstrapUserRoleStores). No additional CreateTenant call is
				// needed here; first-boot is idempotent across daemon restarts.
			}
			if svcNode.Backup != nil && !standalone {
				serverOpts = append(serverOpts, server.WithBackupService(svcNode.Backup))
			}
			if svcNode.Rebalance != nil && !standalone {
				serverOpts = append(serverOpts, server.WithRebalanceService(svcNode.Rebalance))
			}
			// A non-nil advertiser means this node advertises enrollment, NOT
			// that a Noise listener is running (clusterenroll.Advertiser). The
			// distinction is what keeps a FIPS 140-3 first node joinable: Noise
			// is excluded there, and /v1/ca/bootstrap -- the endpoint SPKI, DANE
			// and direct HTTPS all fetch -- is the approved half of the same
			// surface, so it has to be registered on exactly this condition
			// rather than on the presence of a Noise server.
			if bootServers != nil {
				serverOpts = append(serverOpts, server.WithCABootstrap(bundle.CACertPEM))
			}
			if svcNode.Embedding != nil {
				serverOpts = append(serverOpts, server.WithEmbedding(svcNode.Embedding))
			}
			if svcNode.RAG != nil {
				serverOpts = append(serverOpts, server.WithRAG(svcNode.RAG))
			}
			if h := webUIHandler(); h != nil {
				serverOpts = append(serverOpts, server.WithWebUI(h))
			}

			// Build per-database service registry for multi-database routing.
			// Currently all databases share the system shard services. As per-database
			// shard allocation (Phase 1.2) is enabled, each database will get its
			// own service set backed by a dedicated shard.
			dbRegistry := server.NewDatabaseServiceRegistry("system")
			dbRegistry.Register("system", &server.DatabaseServices{
				Name:        "system",
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
				Vector:      vectorSvc,
			})
			serverOpts = append(serverOpts, server.WithDatabaseServiceRegistry(dbRegistry))

			srv := server.New(serverCfg, kvSvc, adminSvc, serverOpts...)
			if err := srv.Start(); err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			if serverServing != nil {
				serverServing()
			}

			// Leader-side lease-expiry sweeper. Leases granted with a TTL must be
			// auto-revoked when they elapse so their attached keys are deleted and
			// watchers receive the DELETE notification. Only the Raft leader proposes
			// the revoke (routed through consensus via leaseSvc.Revoke); every replica
			// applies it. The leadership gate is re-evaluated each tick, so the loop is
			// correct across elections and a no-op on followers. Skipped when the lease
			// service is absent (embeddable deployments without lease support).
			if leaseSvc != nil && host != nil {
				go lr.RunExpiry(bootCtx, lease.DefaultExpiryInterval,
					func() bool {
						leaderID, ok, lerr := host.GetLeaderID(shard.ShardKV)
						return lerr == nil && ok && leaderID == f.replicaID
					},
					leaseSvc.Revoke,
					slog.Default(),
				)
			}

			// Reclaim-job sweeper (go-qrdb#547). It runs on every daemon --
			// standalone included, where host is nil and there is no leader
			// to ask -- and decides per job which node sweeps it; see
			// startReclaimSweeper.
			var isSystemLeader func() bool
			if host != nil {
				isSystemLeader = systemShardLeader(host, f.replicaID)
			}
			startReclaimSweeper(bootCtx, svcNode.ReclaimSweeper, service.DefaultReclaimInterval, isSystemLeader, slog.Default())

			// Storage-quota reconciler (Phase 3). Periodically flushes the in-memory
			// byte accumulator to the persisted CurrentStorageBytes counters and, for
			// each dedicated-volume tenant, recomputes the true on-disk size of its
			// DataRoot to correct drift. Stops cleanly on signal (sigCtx cancellation),
			// performing a final flush. Shared/prefix tenants rely on the flushed
			// accumulator value (documented approximation). Skipped when the accountant
			// or tenant service is absent.
			if quotaAccountant != nil && tenantSvcInst != nil {
				if rec := buildQuotaReconciler(quotaAccountant, tenantSvcInst, f.tenantConfig); rec != nil {
					go rec.Run(bootCtx)
					slog.Info("storage-quota reconciler started")
				}
			}

			// Opt-in dynamic Raft membership: register self under a lease and, on the
			// leader, reconcile the Raft group as nodes join and leave. OFF by default.
			var membershipMgr *cluster.MembershipManager
			if f.dynamicMembership && standalone {
				slog.Warn("dynamic membership ignored: requires mode: raft")
			}
			if f.dynamicMembership && !standalone {
				membershipMgr, err = startDynamicMembership(bootCtx, f, kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc)
				if err != nil {
					srv.Stop()
					_ = shutdownNode(nodeCloser)
					return err
				}
				slog.Info("dynamic membership manager started",
					"shard_id", f.shardID, "node_id", f.replicaID)
			}

			slog.Info("qrdbd node started, waiting for signals")

			// Block until a shutdown signal or an in-process system encryption migration.
			select {
			case <-sigCtx.Done():
				slog.Info("received signal, shutting down")

				if membershipMgr != nil {
					if err := membershipMgr.Close(); err != nil {
						slog.Error("membership manager close failed", slog.Any("error", err))
					}
				}

				// Clean up bootstrap servers and cert reloader.
				if bootServers != nil {
					bootServers.Stop()
				}
				if bundle.Reloader != nil {
					_ = bundle.Reloader.Stop()
				}

				// Stop all dedicated per-database raft shards first so their Pebble locks on
				// {DataRoot}/databases/{db} are released (the shared host itself is closed
				// later, below) before the barrier per-tenant Pebbles are closed.
				if raftBackendReg != nil {
					if err := raftBackendReg.CloseAll(); err != nil {
						slog.Error("dedicated raft shard close-all failed", slog.Any("error", err))
					}
				}

				// Close all per-tenant volume Hosts first so their Raft shards stop and
				// their Pebble locks on {DataRoot}/databases are released before the
				// barrier per-tenant Pebbles (which live under {DataRoot}/barrier) are
				// closed below.
				if tenantHostReg != nil {
					if err := tenantHostReg.CloseAll(); err != nil {
						slog.Error("per-tenant volume host close-all failed", slog.Any("error", err))
					}
				}

				// Close all per-tenant Pebbles allocated through the volume pool.
				// We use a short-lived background context here because sigCtx is
				// already cancelled by the time we reach this point.
				if volumePool != nil && volumePool.PerTenantRegistry != nil {
					closed := volumePool.PerTenantRegistry.List()
					if err := volumePool.PerTenantRegistry.CloseAll(context.Background()); err != nil {
						slog.Error("per-tenant pebble close-all failed",
							slog.Any("error", err),
							slog.Int("count", len(closed)))
					} else {
						slog.Info("closed per-tenant pebbles on shutdown",
							slog.Int("count", len(closed)))
					}
				}

				srv.Stop()
				return shutdownNode(nodeCloser)
			case req := <-migrateCh:
				if host == nil {
					// Unreachable: the migrator is not wired in standalone.
					slog.Error("system encryption migration is not supported in standalone mode")
					return &StandaloneMigrationError{}
				}
				// In-process system encryption migration: quiesce, re-encrypt the
				// system host's stores, adopt the keyed root key, and signal the boot
				// loop to re-initialize (the barrier comes back up sealed, pending
				// operator unseal with the newly-set credential).
				reboot, mErr := handleSystemMigration(req, f, srv, svcNode, host, raftBackendReg, tenantHostReg, membershipMgr, bootCancel, sealSub)
				// handleSystemMigration stopped svcNode to release its store locks
				// for re-encryption; nil it so the deferred Stop above is skipped.
				svcNode = nil
				rebootForMigration = reboot
				if reboot && volumePool != nil && volumePool.PerTenantRegistry != nil {
					// handleSystemMigration closed the tenant Hosts; now release the
					// per-tenant volume-pool Pebble locks too. VolumePoolSubsystem.Close
					// (the deferred call) only closes the pool state store, so without
					// this the next boot iteration would fail to reattach volume tenants
					// on their still-locked per-tenant Pebbles.
					if cerr := volumePool.PerTenantRegistry.CloseAll(context.Background()); cerr != nil {
						slog.Error("per-tenant pebble close during migration failed", slog.Any("error", cerr))
					}
				}
				return mErr
			}
		}()
		if bootErr != nil {
			return bootErr
		}
		if !rebootForMigration {
			return nil
		}
		slog.Info("re-initializing data plane in-process after system encryption migration")
	}
}

// buildQuotaAccountant constructs the storage-byte quota accountant from the
// tenant service, which supplies both the quota lookup (quota.QuotaProvider)
// and the durable usage mutator (Increment/Decrement). Returns nil when the
// tenant service is absent or is not a quota.QuotaProvider, in which case no
// storage-byte enforcement is wired (unlimited behaviour).
func buildQuotaAccountant(tenantSvc tenant.TenantServicer) *quota.Accountant {
	if tenantSvc == nil {
		return nil
	}
	provider, ok := tenantSvc.(quota.QuotaProvider)
	if !ok {
		return nil
	}
	acct, err := quota.NewAccountant(quota.AccountantConfig{
		Quotas: provider.Quotas,
		Flush: func(ctx context.Context, tenantID string, delta int64) error {
			switch {
			case delta > 0:
				_, ferr := tenantSvc.IncrementQuotaUsage(ctx, tenantID, quota.ResourceStorageBytes, delta)
				return ferr
			case delta < 0:
				_, ferr := tenantSvc.DecrementQuotaUsage(ctx, tenantID, quota.ResourceStorageBytes, -delta)
				return ferr
			default:
				return nil
			}
		},
		Logger: slog.Default(),
	})
	if err != nil {
		slog.Error("storage-quota accountant init failed", slog.Any("error", err))
		return nil
	}
	return acct
}

// quotaUsageSetter is the subset of the tenant service the reconciler needs to
// overwrite a tenant's authoritative usage (bypassing limit enforcement). It is
// satisfied by the concrete tenant service implementations.
type quotaUsageSetter interface {
	SetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, value int64) (*tenant.TenantInfo, error)
}

// buildQuotaReconciler constructs the storage-quota reconciler that flushes the
// accountant and corrects per-volume-tenant on-disk usage. Returns nil when the
// tenant service cannot set absolute usage (no SetQuotaUsage support) or the
// reconciler cannot be built.
func buildQuotaReconciler(acct *quota.Accountant, tenantSvc tenant.TenantServicer, tcfg qrdbconfig.TenantConfig) *quota.Reconciler {
	setter, ok := tenantSvc.(quotaUsageSetter)
	if !ok {
		slog.Warn("tenant service does not support SetQuotaUsage; storage-quota reconciler disabled")
		return nil
	}
	interval, err := tcfg.QuotaReconcileIntervalDuration()
	if err != nil {
		slog.Warn("invalid tenant.quota_reconcile_interval; using default",
			slog.String("default", quota.DefaultReconcileInterval.String()),
			slog.Any("error", err))
		interval = quota.DefaultReconcileInterval
	}
	rec, err := quota.NewReconciler(quota.ReconcilerConfig{
		Accountant: acct,
		Interval:   interval,
		Tenants: func(ctx context.Context) ([]quota.StorageTenant, error) {
			infos, lerr := tenantSvc.ListTenants(ctx)
			if lerr != nil {
				return nil, lerr
			}
			out := make([]quota.StorageTenant, 0, len(infos))
			for i := range infos {
				ti := &infos[i]
				if ti.ID == tenant.SystemTenantID {
					continue
				}
				out = append(out, quota.StorageTenant{
					ID:       ti.ID,
					DataRoot: ti.DataRoot,
					Volume:   ti.EffectiveIsolationType() == tenant.IsolationVolume,
				})
			}
			return out, nil
		},
		SetUsage: func(ctx context.Context, tenantID string, bytes int64) error {
			_, serr := setter.SetQuotaUsage(ctx, tenantID, quota.ResourceStorageBytes, bytes)
			return serr
		},
		Logger: slog.Default(),
	})
	if err != nil {
		slog.Error("storage-quota reconciler init failed", slog.Any("error", err))
		return nil
	}
	return rec
}

// buildServerConfig constructs a *qrdbconfig.Config from the parsed start
// flags for use by the multi-protocol server orchestrator.
func buildServerConfig(f *startFlags) (*qrdbconfig.Config, error) {
	// Fail closed on an absent bind host rather than let net.Listen read ""
	// as "every interface". See MissingBindHostError for why silence is the
	// wrong answer here.
	if f.host == "" {
		return nil, &MissingBindHostError{}
	}
	return &qrdbconfig.Config{
		Mode: f.mode,
		Server: qrdbconfig.ServerConfig{
			AdminPort: f.adminPort,
			GRPCPort:  f.grpcPort,
			QUICPort:  f.quicPort,
			MySQLPort: f.mysqlPort,
			PgSQLPort: f.pgsqlPort,
			// The resolved bind host, not a literal. Every listener the
			// orchestrator opens joins it with its port, so server.host,
			// QRDB_SERVER_HOST and --host reach all of them (go-qrdb#167:
			// this line used to discard all three).
			Host: f.host,
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
			Enabled: qrdbconfig.BoolPtr(f.authEnabled),
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
			Method:                 f.discoveryMethod,
			MulticastSecret:        f.multicastSecret,
			MulticastGroup:         f.multicastGroup,
			DNSDomain:              f.dnsDomain,
			DNSService:             f.dnsService,
			DNSProto:               f.dnsProto,
			TokenEndpoint:          f.discoveryEndpoint,
			Token:                  f.discoveryToken,
			TokenClusterSize:       f.discoveryClusterSize,
			AllowInsecureTokenHTTP: f.discoveryInsecure,
			MinPeers:               f.clusterMinPeers,
			DynamicMembership:      f.dynamicMembership,
		},
	}, nil
}

// initializeNode performs all initialization: validates flags, creates the
// host, and starts the shard. Returns the running host or an error.
// The bundle is used to configure mTLS on the QUIC transport for
// multi-node clusters (nil for single-node which uses WithoutTransport).
func initializeNode(ctx context.Context, f *startFlags, bundle *TLSBundle) (*quicraft.Host, error) {
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
	// up the default factory from the registry — anchoring the registry's
	// default storage factory under the real data dir FIRST, so
	// disk-persistent registry SMs ("disk", "fast") open durable,
	// restart-stable per-replica paths instead of fresh temp directories.
	createFn := f.createFn
	if createFn == nil {
		var err error
		createFn, err = resolveCreateFunc(f.smType, f.smBackend,
			statemachine.StorageConfig{Root: f.dataDir, Kind: f.smStorage})
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
	// A multi-node Raft transport cannot use a kernel-assigned port: it is
	// advertised to peers as configured (go-qrdb#374).
	if !isSingleNode(f) {
		if err := cluster.CheckRaftTransportPort("--raft-address", f.raftAddress); err != nil {
			return nil, err
		}
		if err := cluster.CheckRaftTransportPort("--listen", f.listen); err != nil {
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

	// Resolve the Raft member identities before anything touches the disk: a
	// host with a transport admits a member by its identity and by nothing
	// else (go-qrdb#556), and the local entry comes from this node's own
	// certificate.
	var memberIdentities map[uint64]string
	if !isSingleNode(f) {
		var selfCert []byte
		if mtlsCfg != nil {
			selfCert = mtlsCfg.Cert
		}
		ids, err := cluster.ResolveMemberIdentities(f.memberIdentities, f.replicaID, selfCert)
		if err != nil {
			return nil, err
		}
		memberIdentities = ids
	}

	// Build host configuration.
	hostCfg := buildHostConfig(f, mtlsCfg)

	// Determine host options based on peer count.
	// Single-node (no remote peers) uses WithoutTransport.
	//
	// isSingleNode is the SAME predicate buildHostConfig and buildShardConfig
	// use to decide the transport-less Raft tunings. The tunings are sound
	// only for a host with no network, so the two must never diverge.
	var hostOpts []quicraft.HostOption
	if isSingleNode(f) {
		hostOpts = append(hostOpts, quicraft.WithoutTransport())
		slog.Info("single-node mode, transport disabled")
	}

	// Encrypt the shared Raft LogDB/WAL at rest under the system barrier
	// when seal is enabled. f.walBarrier is set in runStart only after the
	// barrier is unsealed (and is nil when seal is disabled), so the LogDB
	// always opens against an available barrier here.
	//
	// 4d Option C: in clustered/raft mode wrap the shared system-barrier WAL in a
	// multiplex LogDB so dedicated-raft shards that opt into a per-tenant-KEY WAL
	// can be routed to their own tenant-barrier-encrypted WAL store at attach
	// time, while WithShardBarriers routes those shards' snapshots to the tenant
	// barrier too. Shards on the shared WAL resolve a nil per-shard barrier and
	// fall back to the host-wide system barrier, keeping Route A byte-identical.
	// Single-node hosts keep the plain WithBarrier path unchanged.
	if f.walBarrier != nil {
		if !isSingleNode(f) && f.walSystemBarrier != nil {
			walDir := filepath.Join(f.dataDir, "wal")
			sharedWAL, err := waldb.Open(walDir, waldb.WithBarrier(f.walSystemBarrier))
			if err != nil {
				return nil, err
			}
			mux, err := database.NewMultiplexLogDB(sharedWAL, f.walSystemBarrier)
			if err != nil {
				_ = sharedWAL.Close()
				return nil, err
			}
			f.walMux = mux
			hostOpts = append(hostOpts,
				quicraft.WithLogDB(mux),
				quicraft.WithBarrier(f.walBarrier),
				quicraft.WithShardBarriers(func(shardID uint64) quicraft.Barrier {
					if b := mux.BarrierForShard(shardID); b != nil {
						return b
					}
					return nil
				}),
			)
			slog.Info("raft WAL at-rest encryption enabled (multiplex: shared system barrier + per-tenant dedicated WALs)")
		} else {
			hostOpts = append(hostOpts, quicraft.WithBarrier(f.walBarrier))
			slog.Info("raft WAL at-rest encryption enabled (system barrier)")
		}
	} else {
		// Seal disabled: the Raft log is stored in plaintext, as every other
		// byte on this node is. go-quicraft >= 0.14 requires the choice to be
		// stated rather than defaulted.
		hostOpts = append(hostOpts, quicraft.WithoutEncryption())
	}

	// Create the host.
	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		return nil, err
	}
	if identity := host.NodeIdentity(); identity != "" {
		// Safe to log: a hash of a public key. It is the value every other
		// replica needs in its --member-identities for this one.
		slog.Info("raft member identity", "replica_id", f.replicaID, "identity", identity)
	}

	// Build shard configuration.
	shardCfg := buildShardConfig(f)
	shardCfg.MemberIdentities = memberIdentities

	// A one-shot offline restore takes over shard startup entirely: it
	// bootstraps the shard, stops it, imports the backup artifact, and
	// starts it again from the recovered state. See restore.go.
	if f.restoreBackupID != "" {
		if err := restoreShardFromBackup(ctx, f, host, createFn, shardCfg); err != nil {
			_ = host.Close()
			return nil, err
		}
		return host, nil
	}

	// Start the shard using the selected discovery method.
	if err := startShard(ctx, f, host, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, err
	}

	return host, nil
}

// resolveAdvertiseHost returns the host peers and clients use to reach this
// node's APIs: the explicit advertise host when set, else the host portion of
// the Raft address, which is by definition an address peers already dial. The
// result may still be a wildcard (an unconfigured node with a wildcard Raft
// address); callers check cluster.AdvertisableHost before publishing it.
func resolveAdvertiseHost(f *startFlags) string {
	if f.advertiseHost != "" {
		return f.advertiseHost
	}
	return cluster.HostFromAddress(f.raftAddress)
}

// selfAdminURL builds the admin HTTPS base URL this node publishes for itself
// in the leader-forward peer registry. The entry is keyed by the node's Raft
// address -- the identity peers use -- so the value has to be a URL a PEER can
// dial, which is the advertise host and the admin port. A node with no
// advertisable host (wildcard Raft address, no advertise host) has no such URL
// and reports false so the caller registers nothing: the registry's own
// derivation from the Raft address host is a better answer than a wrong one.
func selfAdminURL(f *startFlags) (string, bool) {
	host := resolveAdvertiseHost(f)
	if !cluster.AdvertisableHost(host) {
		return "", false
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(f.adminPort)), true
}

// advertisedAPIEndpoints builds the client-facing API endpoint map this node
// publishes into the cluster registry. Every value comes from this node's own
// configuration: the advertise host (explicit, else the host portion of the
// Raft address peers already use to reach us) plus the ports of the protocols
// actually enabled. A disabled protocol is omitted, and a wildcard bind host
// with no explicit advertise host publishes nothing rather than an address no
// peer can dial.
func advertisedAPIEndpoints(f *startFlags) (map[cluster.APIProtocol]string, error) {
	host := resolveAdvertiseHost(f)

	ports := make(map[cluster.APIProtocol]int, len(cluster.APIProtocols))
	if f.enableREST {
		ports[cluster.APIProtocolREST] = f.adminPort
	}
	if f.enableGRPC {
		ports[cluster.APIProtocolGRPC] = f.grpcPort
	}
	if f.enableQUIC {
		ports[cluster.APIProtocolQUIC] = f.quicPort
	}
	if f.enableMCP {
		ports[cluster.APIProtocolMCP] = f.mcpPort
	}
	return cluster.BuildAPIEndpoints(host, ports)
}

// startDynamicMembership builds an in-process (embedded) qrdb SDK client over
// the live service layer and starts the opt-in dynamic membership manager.
// The same embedded client serves data and drives membership, so there is one
// lessor and one watch subsystem behind both. The auto-removal quorum guard is
// derived from cluster-min-peers.
func startDynamicMembership(
	ctx context.Context,
	f *startFlags,
	kvSvc service.KVServicer,
	adminSvc service.AdminServicer,
	mvccSvc service.MVCCKVServicer,
	leaseSvc service.LeaseServicer,
	watchSvc service.WatchServicer,
) (*cluster.MembershipManager, error) {
	tr, err := embedded.New(kvSvc, adminSvc,
		embedded.WithMVCCService(mvccSvc),
		embedded.WithLeaseService(leaseSvc),
		embedded.WithWatchService(watchSvc),
	)
	if err != nil {
		return nil, err
	}
	client, err := sdkgo.NewClient(tr)
	if err != nil {
		return nil, err
	}

	minPeers := f.clusterMinPeers
	if minPeers <= 0 {
		minPeers = 3
	}
	endpoints, err := advertisedAPIEndpoints(f)
	if err != nil {
		return nil, err
	}

	mgr, err := client.NewMembershipManager(
		cluster.Member{NodeID: f.replicaID, Address: f.raftAddress, APIEndpoints: endpoints},
		f.shardID,
		cluster.MembershipConfig{MinQuorum: cluster.QuorumSize(minPeers)},
	)
	if err != nil {
		return nil, err
	}
	if err := mgr.Start(ctx); err != nil {
		return nil, err
	}
	return mgr, nil
}

// waitForSignal blocks until SIGINT or SIGTERM is received.
func waitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())
}

// shutdownNode gracefully closes the node and logs the result.
// localDataPlaneCloser tears down the standalone (raft-free) data plane:
// the LocalBackend first (stops accepting applies), then the storage it
// wraps. It satisfies the io.Closer contract shutdownNode expects from the
// raft host in raft mode.
type localDataPlaneCloser struct {
	local   *backend.LocalBackend
	storage storage.Storage
}

// Close closes the LocalBackend and then the underlying storage.
func (c localDataPlaneCloser) Close() error {
	err := c.local.Close()
	if serr := c.storage.Close(); err == nil {
		err = serr
	}
	return err
}

func shutdownNode(node io.Closer) error {
	if err := node.Close(); err != nil {
		slog.Error("error during shutdown", "error", err)
		return err
	}
	slog.Info("qrdbd node stopped")
	return nil
}

// buildSMCreateFunc builds the raft state machine factory for --sm with the
// node's subsystems and at-rest encryptor. "mvcc" and "pebble" are composed
// here with the lessor, watch hub and time-series storage, and with
// store.WithEncryptor, which wraps every Pebble file the state machine creates
// (sstables, WAL, manifest, snapshots) in the barrier-driven encfs. Every other
// type resolves from the registry over the data dir and --sm-storage, carrying
// the same encryptor: pebble storage is opened on that encfs too, and storage
// that cannot be encrypted (file) is refused here, at start, while the seal
// barrier is enabled (SealedSMStoragePlaintextError), rather than opened.
func buildSMCreateFunc(f *startFlags, enc *atrest.Encryptor, lr *lease.Lessor, hub *watch.Hub) (sm.CreateFunc, error) {
	pebbleOpts := []store.Option{store.WithEncryptor(enc)}
	switch f.smType {
	case "mvcc":
		return store.NewMVCCCreateFuncWith(pebbleOpts,
			store.WithLessor(lr),
			store.WithWatchHub(hub),
			store.WithTimeSeriesEnabled(),
		), nil
	case "pebble":
		return store.NewPebbleCreateFuncWith(pebbleOpts,
			func(s *store.PebbleStateMachine) {
				s.SetLessor(lr)
				s.SetWatchHub(hub)
				s.SetTimeSeriesEnabled(true)
			},
		), nil
	default:
		return resolveCreateFunc(f.smType, f.smBackend,
			statemachine.StorageConfig{Root: f.dataDir, Kind: f.smStorage, Encryptor: enc})
	}
}

// resolveCreateFunc looks up the state machine factory by type and optional
// backend in the centralized statemachine.Registry, building a
// storage-generic state machine over sc (the data dir and --sm-storage), and
// translates the registry's refusals into this command's errors: an
// unregistered combination is UnknownSMError, an unknown --sm-storage is
// UnknownSMStorageError, and a non-pebble --sm-storage for a state machine
// that carries its own storage is SMStorageTypeMismatchError. Which types are
// storage-generic is the registry's answer (Descriptor.StorageGeneric), not a
// list kept here.
func resolveCreateFunc(smType, smBackend string, sc statemachine.StorageConfig) (sm.CreateFunc, error) {
	createFn, err := statemachine.ResolveTypedWithStorage(smType, smBackend, sc)
	if err == nil {
		return createFn, nil
	}
	var unknownStorage *statemachine.UnknownSMStorageError
	if errors.As(err, &unknownStorage) {
		return nil, &UnknownSMStorageError{Name: sc.Kind}
	}
	var notApplicable *statemachine.SMStorageNotApplicableError
	if errors.As(err, &notApplicable) {
		return nil, &SMStorageTypeMismatchError{Storage: sc.Kind, SMType: smType}
	}
	var unencrypted *statemachine.SMStorageUnencryptedError
	if errors.As(err, &unencrypted) {
		return nil, &SealedSMStoragePlaintextError{SMType: smType, Storage: sc.Kind, Err: err}
	}
	name := smType
	if smBackend != "" {
		name = smType + ":" + smBackend
	}
	return nil, &UnknownSMError{Name: name}
}

// buildHostConfig constructs a HostConfig from the parsed start flags.
// When mtls is non-nil it is attached to the TransportConfig for QUIC
// transport mTLS.
func buildHostConfig(f *startFlags, mtls *quicraftconfig.MTLSConfig) quicraftconfig.HostConfig {
	walDir := filepath.Join(f.dataDir, "wal")
	nodeHostDir := filepath.Join(f.dataDir, "nodehost")

	hc := quicraftconfig.HostConfig{
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

	// A single-node daemon builds its host WithoutTransport (initializeNode),
	// so it would otherwise inherit go-quicraft's WAN RTT estimate (200 ms) —
	// an election budget sized for a link this host does not have.
	// See cluster.TransportlessRTTMillisecond (go-qrdb#250).
	if isSingleNode(f) {
		cluster.ApplyTransportlessHostTuning(&hc)
	}
	return hc
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

	// No peer can reach a transport-less host, so a single-node daemon's
	// system shard can never grow its voting membership and its quorum is
	// permanently itself. See cluster.SingleNodeElectionRTT.
	//
	// --join is excluded even though it cannot succeed without a transport:
	// the shorter election timeout is sound only for a shard that is
	// single-node by intent, and a misconfiguration must not be what grants
	// it. Dedicated per-database shards started later on this same host by
	// RaftBackendFactory keep the go-quicraft default — that factory's host
	// may be clustered, and the exemption is not sound there.
	if isSingleNode(f) && !f.join {
		cluster.ApplyTransportlessShardTuning(&cfg)
	}
	return cfg
}

// startShard starts the Raft shard using the configured discovery method.
func startShard(ctx context.Context, f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	if f.discoveryMethod == "static" {
		return startShardStatic(f, node, createFn, shardCfg)
	}
	return startShardWithBootstrap(ctx, f, node, createFn, shardCfg)
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

// startShardWithBootstrap starts a shard using the cluster bootstrap
// orchestration with the configured discovery method. The discovery engine,
// quorum logic, and member-map construction all live in pkg/cluster; the
// daemon only supplies its flags as cluster.Options.
func startShardWithBootstrap(ctx context.Context, f *startFlags, node raftNode, createFn sm.CreateFunc, shardCfg quicraftconfig.Config) error {
	slog.Info("bootstrapping shard",
		"shard_id", f.shardID,
		"discovery", f.discoveryMethod,
	)

	return cluster.BootstrapShard(ctx, node, f.clusterOptions(), f.shardID, createFn, shardCfg)
}

// parsePeers parses a slice of "id=address" strings into a member map. It
// delegates to the canonical cluster.ParsePeers so the daemon and embedders
// share one parser and one PeerParseError contract.
func parsePeers(peers []string) (map[uint64]string, error) {
	return cluster.ParsePeers(peers)
}

// isSingleNode returns true when the peer configuration describes a
// single-node cluster (only the local node). Non-static discovery methods
// always assume multi-node.
func isSingleNode(f *startFlags) bool {
	return cluster.IsSingleNode(f.clusterOptions())
}

// buildStaticDiscovery is a daemon convenience wrapper that builds the
// static discovery method from the start flags. The construction logic
// lives in pkg/cluster; this only fixes the method and delegates.
func buildStaticDiscovery(f *startFlags) (discovery.Method, error) {
	opts := f.clusterOptions()
	opts.Method = "static"
	return cluster.BuildDiscovery(opts)
}

// buildMulticastDiscovery is a daemon convenience wrapper for the multicast
// discovery method. See buildStaticDiscovery.
func buildMulticastDiscovery(f *startFlags) (discovery.Method, error) {
	opts := f.clusterOptions()
	opts.Method = "multicast"
	return cluster.BuildDiscovery(opts)
}

// buildDNSDiscovery is a daemon convenience wrapper for the DNS SRV
// discovery method. See buildStaticDiscovery.
func buildDNSDiscovery(f *startFlags) (discovery.Method, error) {
	opts := f.clusterOptions()
	opts.Method = "dns"
	return cluster.BuildDiscovery(opts)
}

// ---------------------------------------------------------------------------
// Auth bootstrap
// ---------------------------------------------------------------------------

// mgmtBootstrapResult holds the user and role stores created unconditionally
// during startup, independent of whether auth enforcement is enabled.
//
// userSvc/roleSvc are the plain shared-store services used by the internal
// auth plane (RBAC authorizer, auth-middleware enrichment): they read the
// system-tenant IAM records that bootstrap seeds and that authorization
// consults on every request.
//
// servedUserSvc/servedRoleSvc are the services wired into the protocol API
// surface (server.WithUserService / server.WithRoleService). When a barrier
// registry and tenant servicer are present they are StoreResolver-routed
// RoutingServices: prefix/system tenants transparently resolve to the shared
// store (so system IAM behaves identically to the plain service), while a
// barrier-isolated tenant resolves to its own TenantBarrier-backed store and
// a SEALED barrier tenant fails closed with tenant.ErrTenantSealed instead of
// leaking IAM through the shared store. When no barrier registry is present
// they alias the plain services.
type mgmtBootstrapResult struct {
	userSvc       user.UserServicer
	roleSvc       role.RoleServicer
	servedUserSvc user.UserServicer // resolver-routed service exposed on the API surface
	servedRoleSvc role.RoleServicer // resolver-routed service exposed on the API surface
	bootUserSvc   user.UserServicer // StaleRead-backed fallback for auth middleware
	bootRoleSvc   role.RoleServicer // StaleRead-backed fallback for RBAC authorizer

	// storeResolver maps a tenant to the store holding its IAM data. It is
	// the same resolver servedUserSvc and servedRoleSvc route through, and it
	// is carried out of here so the user-side auth-factor service routes
	// through THAT resolver rather than a second one built over the same
	// store — two resolvers over one store would be two caches of the same
	// per-tenant wrappers, and the sealed/unsealed decision each one makes is
	// only as live as its own view. Nil when no tenant servicer was available
	// to build one.
	storeResolver tenant.StoreResolver

	// legacyFactorStore is the UNCONFINED root store the pre-tenant
	// auth-factor records live in, handed to the go-qrdb#393 migration.
	legacyFactorStore kvstore.KVStore
}

// authBootstrapResult holds the auth enforcement middleware and audit logger
// created only when --auth-enabled=true.
type authBootstrapResult struct {
	auditLogger     audit.Logger
	authMW          *middleware.AuthMiddleware
	custodianSvc    custodian.Servicer
	soAuthenticator *auth.SOAuthenticator
}

// bootstrapUserRoleStores initializes the user and role stores unconditionally.
// These are organizational primitives available regardless of auth enforcement
// state, allowing operators to pre-provision identities before enabling auth.
//
// Two KV services are used:
//   - bootKVSvc (StaleRead) for initial bootstrap operations when ReadIndex
//     is not yet warmed up.
//   - runtimeKVSvc (SyncRead, no codec) for runtime store operations,
//     ensuring linearizable reads for user/role CRUD.
//
// tenantSvc is the daemon's tenant servicer; first-boot uses it to create
// the structurally-privileged system tenant. Required (non-nil): tests
// supply a small in-memory tenant servicer; production startup wires
// the real one from svcNode.Tenant.
func bootstrapUserRoleStores(ctx context.Context, bootKVSvc, runtimeKVSvc service.KVServicer, txSvc service.TransactionServicer, node raftNode, shardID, replicaID uint64, tenantSvc tenant.TenantServicer, strictSoD bool, barriers *seal.BarrierRegistry, sealStrategy seal.StrategyID, auditLogger audit.AuditLogger) (*mgmtBootstrapResult, error) {
	// User IDs are Snowflake-assigned by the user store; map the Raft
	// ReplicaID onto the Snowflake node field (zero maps to node 1 per the
	// standalone convention).
	idNode := replicaID
	if idNode == 0 {
		idNode = 1
	}
	userIDGen, err := dao.NewSnowflakeGenerator(idNode)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "user id generator", Err: err}
	}

	// A leader must be known before the capability probe below can mean
	// anything: PutAllIfUnchanged proposes through Raft, and a probe run
	// before any leader is elected would see NotLeader and could not tell
	// "no leader yet" apart from "this state machine cannot do this at
	// all". runFirstBoot waits again once it starts its own sequence; that
	// second wait is cheap (GetLeaderID is a local, already-known answer
	// once a leader exists) and keeps this function independently correct
	// rather than relying on ordering against its caller.
	if err := waitForLeaderElection(ctx, node, shardID); err != nil {
		return nil, &AuthBootstrapError{Step: "wait for leader", Err: err}
	}

	// Create bootstrap stores backed by StaleRead for initial setup.
	//
	// CONFINED to the system tenant's identity namespace, exactly as
	// StoreResolver confines what it hands out. Firstboot seeds the admin user
	// and the built-in roles here, and the RBAC authorizer reads them back
	// later through the RESOLVER-routed service — so if these two composed
	// different prefixes the admin would have no resolvable role bindings and
	// every authorization check would deny while the data plane kept working.
	// That is not hypothetical: it is what the integration suite caught.
	//
	// tenant.IdentityNamespace is the single formula both sides call. Building
	// the prefix by hand here is what the whole class of write-path/read-path
	// divergences has come from.
	//
	// GUARDED: the boot store is where firstboot creates the built-in roles
	// and the platform admin, and go-qrdb#555 found that a Raft leadership
	// change between the identity claim and the record's own first write —
	// or between any two of the record's own several writes — left a
	// permanently unrecoverable partial state, because completing OR rolling
	// back the remainder needs the very leadership that was just lost. The
	// guarded adapter is what lets role.WithAtomicIdentity /
	// user.WithAtomicIdentity below commit the claim and every write as ONE
	// proposal instead. txSvc is threaded in for exactly this; every raft
	// node this daemon builds constructs one unconditionally
	// (pkg/server/lifecycle.BuildServices), so this does not newly require
	// anything a production node does not already have.
	guardedBootAdapter, gerr := newGuardedKVServiceAdapter(bootKVSvc, txSvc, nil, nil)
	if gerr != nil {
		return nil, &AuthBootstrapError{Step: "boot store adapter", Err: gerr}
	}
	// FAIL CLOSED here, not at the first CreateRole/CreateUser call: a
	// non-nil txSvc makes guardedBootAdapter satisfy
	// kvstore.MultiGuardedBatchKVStore at the TYPE level unconditionally, but
	// only a state machine with the MVCC/transaction subsystem can evaluate
	// the batch. kvstore.ProbeGuardedBatch asks the shard this daemon
	// actually started, with a genuine no-op proposal. The leader wait above
	// has already run, so an inconclusive probe is a failure here too, not
	// something to start past — except NotLeader; see requireGuardedBatch.
	//
	// This runs on EVERY start, not only the first: the runtime stores below
	// use the same atomic create path for every role and user created after
	// first boot, so a node that cannot honour it must not serve.
	guardedBatch, ok := guardedBootAdapter.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		// Unreachable: newGuardedKVServiceAdapter's success return always
		// implements this. Defensive rather than a panic if that changes.
		return nil, &AuthBootstrapError{Step: "boot store adapter", Err: ErrTransactionServiceRequired}
	}
	if err := requireGuardedBatch(ctx, guardedBatch); err != nil {
		return nil, err
	}
	bootStore, err := kvstore.NewNamespacedKVStore(
		guardedBootAdapter,
		tenant.IdentityNamespace(tenant.SystemTenantID),
	)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot store namespace", Err: err}
	}
	// The placement index lives in the system tenant's identity namespace,
	// which is exactly what bootStore is confined to — so the bootstrap path
	// writes its entries to the same physical keys the resolver-backed
	// readers look them up under. Wiring it here means EVERY user and role
	// this daemon writes is placed, including the ones firstboot seeds before
	// a StoreResolver exists. A write path that skipped it would be the same
	// divergence, one layer down.
	bootIdentityIndex, err := tenant.NewIdentityIndex(bootStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot placement index", Err: err}
	}
	// WithAtomicIdentity is safe here — and ONLY here, never on a routed
	// store — because bootIdentityIndex is built with tenant.NewIdentityIndex
	// over bootStore itself: the claim and every record write this store
	// makes land in the SAME physical store, by construction, not by
	// assumption. See role.WithAtomicIdentity / user.WithAtomicIdentity.
	bootUserSvc, err := user.NewStore(bootStore, userIDGen,
		user.WithAuditLogger(auditLogger), user.WithIdentityIndex(bootIdentityIndex), user.WithAtomicIdentity())
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot user store", Err: err}
	}
	bootRoleSvc, err := role.NewStore(bootStore,
		role.WithAuditLogger(auditLogger), role.WithIdentityIndex(bootIdentityIndex), role.WithAtomicIdentity())
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot role store", Err: err}
	}

	// First-boot: create the system tenant, register built-in roles, and
	// create the admin user with the canonical Admin@system grant. The
	// system-tenant rule in pkg/auth/rbac promotes that single grant to
	// cross-tenant authority. Idempotent across daemon restarts.
	if err := runFirstBoot(ctx, node, shardID, bootUserSvc, bootRoleSvc, tenantSvc, strictSoD, barriers, sealStrategy); err != nil {
		return nil, err
	}

	// Create runtime stores backed by linearizable SyncRead for management
	// operations. This ensures CRUD handlers see the latest committed state.
	//
	// runtimeStore stays UNCONFINED: it is what StoreResolver wraps, and the
	// resolver applies the per-tenant namespace itself. systemStore is the
	// system tenant's confined view, used by the plain (non-routed) IAM
	// services so they address the same keys the routed services do for the
	// system tenant — the invariant the comment on mgmtBootstrapResult
	// describes and that authorization depends on.
	//
	// GUARDED, for the same go-qrdb#555 reason bootStore is above: userSvc
	// and roleSvc are reached by the admin API for every role/user create
	// AFTER first-boot too, not only during it, and this is the one
	// canonical construction bootstrapAuthEnforcement's own runtime store
	// reuses (mgmtBootstrapResult.legacyFactorStore) rather than building a
	// second guarded adapter over the same underlying KVServicer.
	// Not re-probed: runtimeKVSvc and bootKVSvc read the SAME shard through
	// the SAME txSvc, so the capability kvstore.ProbeGuardedBatch already
	// ran against bootStore above answers identically here — the state
	// machine's transaction support is a property of the shard, not of
	// which KVServicer consistency mode wraps it.
	runtimeStore, gerr := newGuardedKVServiceAdapter(runtimeKVSvc, txSvc, nil, nil)
	if gerr != nil {
		return nil, &AuthBootstrapError{Step: "runtime store adapter", Err: gerr}
	}
	systemStore, err := kvstore.NewNamespacedKVStore(
		runtimeStore, tenant.IdentityNamespace(tenant.SystemTenantID))
	if err != nil {
		return nil, &AuthBootstrapError{Step: "system store namespace", Err: err}
	}

	// Same placement index, reached through the system tenant's confined
	// runtime view: the plain services address the identical keys the boot
	// services and the routed services do.
	systemIdentityIndex, err := tenant.NewIdentityIndex(systemStore)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "system placement index", Err: err}
	}
	userSvc, err := user.NewStore(systemStore, userIDGen,
		user.WithAuditLogger(auditLogger), user.WithIdentityIndex(systemIdentityIndex), user.WithAtomicIdentity())
	if err != nil {
		return nil, &AuthBootstrapError{Step: "user store", Err: err}
	}
	roleSvc, err := role.NewStore(systemStore,
		role.WithAuditLogger(auditLogger), role.WithIdentityIndex(systemIdentityIndex), role.WithAtomicIdentity())
	if err != nil {
		return nil, &AuthBootstrapError{Step: "role store", Err: err}
	}

	// Services exposed on the protocol API surface. When a barrier registry and
	// tenant servicer are present, route IAM (user/role) reads and writes
	// through a StoreResolver so barrier-isolated tenants persist their IAM in
	// their own TenantBarrier-backed store and a SEALED barrier tenant fails
	// closed (tenant.ErrTenantSealed) instead of leaking through the shared
	// store. The system tenant (structurally a prefix tenant) always resolves
	// to runtimeStore, so system-tenant IAM served through the routed service
	// is byte-for-byte the shared-store behaviour bootstrap seeded above.
	//
	// ROUTING DOES NOT DEPEND ON THE BARRIER REGISTRY. It used to: with
	// sealing disabled the caller passes a nil registry, this branch was
	// skipped, and servedUserSvc/servedRoleSvc stayed the plain stores
	// confined only to "system/" — which select no tenant namespace and do
	// not route by-id through OwningTenant. A tenant caller holding another
	// tenant's user or role id reached the shared primary record instead of
	// its own namespace. That is the same fail-open pkg/server/lifecycle had,
	// in the daemon, and keyspace isolation is not a property of encryption.
	//
	// tenant.NewStoreResolver accepts a nil registry: prefix and system
	// tenants get their confined "{tenant}/" view, and a dedicated-isolation
	// tenant fails closed with ErrTenantSealed because there is no barrier to
	// honour its ask. So the only precondition left is a tenant servicer.
	servedUserSvc := userSvc
	servedRoleSvc := roleSvc
	var storeResolver tenant.StoreResolver
	if tenantSvc != nil {
		resolver, resErr := tenant.NewStoreResolver(runtimeStore, barriers, tenantSvc)
		if resErr != nil {
			return nil, &AuthBootstrapError{Step: "store resolver", Err: resErr}
		}
		routedUsers, ruErr := user.NewRoutingService(resolver, userIDGen, user.WithAuditLogger(auditLogger))
		if ruErr != nil {
			return nil, &AuthBootstrapError{Step: "routed user store", Err: ruErr}
		}
		routedRoles, rrErr := role.NewRoutingService(resolver, role.WithAuditLogger(auditLogger))
		if rrErr != nil {
			return nil, &AuthBootstrapError{Step: "routed role store", Err: rrErr}
		}
		servedUserSvc = routedUsers
		servedRoleSvc = routedRoles
		storeResolver = resolver
	}

	return &mgmtBootstrapResult{
		userSvc:       userSvc,
		roleSvc:       roleSvc,
		servedUserSvc: servedUserSvc,
		servedRoleSvc: servedRoleSvc,
		bootUserSvc:   bootUserSvc,
		bootRoleSvc:   bootRoleSvc,

		storeResolver:     storeResolver,
		legacyFactorStore: runtimeStore,
	}, nil
}

// bootstrapAuthEnforcement initializes the auth enforcement layer: RBAC
// authorizer, audit logger, and auth middleware. It reuses the already-
// initialized user and role stores from mgmtResult. Called only when
// --auth-enabled=true. The complianceAudit parameter is the compliance-grade
// AuditLogger constructed earlier (always present regardless of auth flag).
//
// The RBAC[+ABAC] authorizer, mTLS authenticator, and AuthMiddleware are
// built through pkg/auth/wiring.BuildAuthMiddleware — the SAME constructor
// sdk/go/node.Node calls when wiring an embedded unified server — so a
// daemon-run node and an embedded node (go-xkms xkmsd, go-quicbaas
// quicbaasd) enforce authn/authz identically.
func bootstrapAuthEnforcement(ctx context.Context, mgmt *mgmtBootstrapResult, dataDir string, node raftNode, complianceAudit audit.AuditLogger, strictSoD bool, spiffeTrustDomain string, abacCfg qrdbconfig.AuthABACConfig, certProv cert.CertProvider, revocationCacheTTL string, auditFailurePolicy string) (*authBootstrapResult, error) {
	_ = ctx
	_ = node

	// The custodian service store, reused from mgmt.legacyFactorStore rather
	// than built again here: bootstrapUserRoleStores already constructed it
	// as the ONE guarded adapter over this daemon's runtime KVServicer, and
	// a second adapter instance over the identical underlying service would
	// be the exact duplication the guarded-adapter wiring is meant to avoid
	// (go-qrdb#555 owner ruling: "one canonical construction; nothing
	// duplicated"). It already satisfies kvstore.GuardedBatchKVStore
	// (go-qrdb#21) for the same reason it does there.
	runtimeStore := mgmt.legacyFactorStore

	// Per-decision audit rides the SAME tamper-evident chained event log as
	// every other audited operation, via the decision bridge — one audit
	// plane, no unchained side file. This is the identical wiring the SDK
	// node uses (sdk/go/node), so a daemon-run node and an embedded node
	// audit authorization decisions identically. A nil compliance logger is
	// rejected below by the custodian service (fail closed); the decision
	// bridge is constructed only over a real sink.
	var auditLogger audit.Logger
	if complianceAudit != nil {
		auditLogger = audit.NewDecisionLogger(complianceAudit)
	}

	// Build the RBAC[+ABAC]-authorized, mTLS-authenticated auth middleware.
	// The strict flag voids the Owner convenience role under FIPS 140-3 /
	// configured separation of duties, so a strict binary enforces
	// least-privilege even on a role store bootstrapped under the standard
	// posture. The StaleRead fallbacks keep authorization/authentication
	// working when linearizable reads are unavailable (ReadIndex cold, or
	// host draining) — e.g. so an authorized Drain request can still be
	// admitted on a draining node. Pin the operator-configured SPIFFE trust
	// domain when set so the mTLS authenticator derives the tenant from the
	// peer certificate's tenant-scoped SPIFFE SAN and rejects foreign trust
	// domains (fail-closed). ABAC decoration wraps the SINGLE authorizer
	// handed to the middleware, so every transport gets the composed
	// provider; a load or compile failure aborts the boot (fail closed).
	// The middleware MUST authenticate and authorize against the SAME
	// user/role services the API surface serves (servedUserSvc /
	// servedRoleSvc, the tenant-routed ones when a barrier registry
	// exists): a barrier-isolated tenant's IAM records live in that
	// tenant's own TenantBarrier-backed store, so resolving identities
	// against the plain shared-store services would miss every such
	// tenant's users and roles — authenticating their certificates to an
	// identity with no roles and denying every request. The StaleRead
	// fallbacks stay on the shared-store boot services: they exist for the
	// system tenant's boot window, and for a barrier tenant a shared-store
	// fallback answers not-found (deny), which is the fail-closed answer.
	// The revocation cache TTL is the window during which a revoked
	// certificate still authenticates, so an unparseable value aborts the boot
	// rather than quietly reverting to a default the operator did not choose.
	revocationTTL, err := qrdbconfig.ParseRevocationCacheTTL(revocationCacheTTL)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "auth.revocation_cache_ttl", Err: err}
	}

	// The audit failure posture is resolved HERE, at the boot, so an
	// unrecognised value aborts rather than silently selecting a posture the
	// operator did not ask for. Selecting fail-open logs its warning when the
	// middleware option is applied — once, at every start.
	auditFailure, err := audit.ParseFailurePolicy(auditFailurePolicy)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "auth.audit_failure_policy", Err: err}
	}

	authMW, err := wiring.BuildAuthMiddleware(wiring.AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         mgmt.servedRoleSvc,
		RoleServiceFallback: mgmt.bootRoleSvc,
		UserService:         mgmt.servedUserSvc,
		UserServiceFallback: mgmt.bootUserSvc,
		StrictSeparation:    strictSoD,
		SPIFFETrustDomain:   spiffeTrustDomain,
		ABACEnabled:         abacCfg.Enabled,
		ABACPolicyDir:       abacCfg.PolicyDir,
		AuditLogger:         auditLogger,
		AuditFailurePolicy:  auditFailure,
		// Certificate revocation is enforced on the authentication path, so
		// the certificate authority has to reach the middleware and not only
		// the cert API handlers. Without this the daemon records revocations,
		// lists them, prints them, and lets the revoked certificate keep
		// authenticating until it expires.
		CertProvider:       certProv,
		RevocationCacheTTL: revocationTTL,
	})
	if err != nil {
		return nil, &AuthBootstrapError{Step: "auth middleware", Err: err}
	}

	// Custodian service: persists non-secret custodian metadata under
	// __sys:custodian/<id>. Shares are never persisted -- they live in
	// secure.Buffer only during enrollment delivery and unseal submission.
	custodianSvc, err := custodian.NewService(runtimeStore, complianceAudit, slog.Default())
	if err != nil {
		return nil, &AuthBootstrapError{Step: "custodian service", Err: err}
	}

	// SO authenticator: hash-commits the SO PIN at bootstrap (Argon2id or
	// PBKDF2-SHA256 depending on GOFIPS140), verifies via constant-time
	// compare on each SO-permissioned request. PIN itself is never stored.
	// The SO PIN verification is a GATE, so it reads the same audit failure
	// posture the authorization middleware above was built with -- one
	// operator lever, auth.audit_failure_policy, resolved once at line
	// auditFailure and handed to both (go-qrdb#445).
	soAuthenticator, err := auth.NewSOAuthenticator(runtimeStore, complianceAudit, slog.Default(),
		auth.WithSOAuditFailurePolicy(auditFailure))
	if err != nil {
		return nil, &AuthBootstrapError{Step: "SO authenticator", Err: err}
	}

	return &authBootstrapResult{
		auditLogger:     auditLogger,
		authMW:          authMW,
		custodianSvc:    custodianSvc,
		soAuthenticator: soAuthenticator,
	}, nil
}

// decorateWithABAC wraps the given authorizer with the ABAC policy
// decorator when ABAC is enabled in the configuration. When disabled it
// returns the inner authorizer unchanged. Policies are loaded from the
// configured policy directory at construction; any load or compilation
// error is returned so the caller can abort the boot rather than run with
// a partially applied policy set. Delegates to the shared
// pkg/auth/wiring.DecorateABAC helper (also used by sdk/go/node.Node) so
// the two embedders decorate identically.
func decorateWithABAC(inner auth.Authorizer, cfg qrdbconfig.AuthABACConfig) (auth.Authorizer, error) {
	return wiring.DecorateABAC(inner, cfg.Enabled, cfg.PolicyDir, nil)
}

// defaultAuthFactors returns the auth factor implementations to register
// with the AuthFactorService when running qrdbd standalone.
//
// qrdb itself owns only software-only factors per the locked architecture
// (hardware-coupled FIDO2 / PIV / PKCS#11 factors live in go-xkms and are
// injected by higher-level binaries via WithFactorRegistry at composition
// time). For a standalone qrdbd this returns an empty slice; the
// AuthFactorService is constructed but holds no factors until something
// is explicitly registered.
func defaultAuthFactors() []authfactors.Factor {
	return nil
}

// runFirstBoot orchestrates the daemon's first-boot path: it waits for
// Raft leader election, checks whether the platform admin user is
// already present (StaleRead via bootstrapHostAdapter), and otherwise
// invokes pkg/firstboot.Initialize to create the system tenant +
// built-in roles + admin user.
//
// Raft coordination stays in the daemon: in multi-node clusters only
// the leader can execute SyncPropose. firstboot.Initialize itself is
// transport-agnostic; when this node is a follower the underlying
// SyncPropose returns NotLeader, this function detects the failure
// and falls back to waitForAdminReplication so the follower waits for
// the leader's writes to replicate.
//
// tenantSvc is required: firstboot.Initialize creates the system
// tenant via this servicer using WithInternalCall(ctx) to bypass the
// reserved-id rejection on CreateTenant. Production startup always
// supplies a non-nil tenant servicer; tests do the same via a small
// in-memory tenant servicer.
func runFirstBoot(ctx context.Context, node raftNode, shardID uint64, userSvc user.UserServicer, roleSvc role.RoleServicer, tenantSvc tenant.TenantServicer, strictSoD bool, barriers *seal.BarrierRegistry, sealStrategy seal.StrategyID) error {
	// Phase 1: Wait for leader election. All operations (reads on leader
	// via SyncRead, writes via SyncPropose) require a leader.
	if err := waitForLeaderElection(ctx, node, shardID); err != nil {
		return err
	}

	// Phase 2: Check if admin user already exists using local state
	// machine reads (StaleRead via bootstrapHostAdapter). This returns
	// immediately without waiting for ReadIndex.
	if adminExists(ctx, userSvc, tenant.SystemTenantID) {
		slog.Debug("first-boot: admin user exists, skipping")
		return nil
	}

	// Phase 3: Run the canonical first-boot path. It creates the system
	// tenant (WithInternalCall bypasses the reserved-id rejection), registers
	// all built-in roles, and creates the admin user with the Admin@system
	// grant. Idempotent.
	slog.Info("first-boot: bootstrapping system tenant + roles + admin user",
		slog.String("tenant", tenant.SystemTenantID))

	runInit := func() error {
		if barriers != nil {
			// Seal enabled: route through the seal-aware orchestrator. By this
			// point the system barrier has already been initialized+unsealed
			// out-of-band (admin barrier API for passphrase, SO ceremony for
			// shamir) during awaitUnseal, so we pass an EMPTY passphrase.
			// BootstrapSystem observes the unsealed barrier
			// (BarrierAlreadyInitialized), demands no credential, and runs
			// first-boot. A still-sealed barrier here is an operator error (unseal
			// not completed) surfaced as a SystemBarrierInitError, NOT routed to
			// the follower-wait below.
			_, bsErr := bootstrap.BootstrapSystem(ctx, bootstrap.SystemConfig{
				Strategy:           sealStrategy,
				SeparationOfDuties: strictSoD,
			}, bootstrap.Dependencies{
				Tenants:      tenantSvc,
				Users:        userSvc,
				Roles:        roleSvc,
				RoleResolver: roleSvc,
				Barriers:     barriers,
				Logger:       slog.Default(),
			})
			return bsErr
		}
		// Seal disabled (no barrier registry): keep the direct first-boot
		// path. BootstrapSystem requires a non-nil barrier registry, so wiring
		// it here would force a synthetic barrier with no benefit.
		_, fbErr := firstboot.Initialize(ctx, firstboot.Dependencies{
			Tenants:            tenantSvc,
			Users:              userSvc,
			Roles:              roleSvc,
			RoleResolver:       roleSvc,
			Logger:             slog.Default(),
			SeparationOfDuties: strictSoD,
		})
		return fbErr
	}

	// A freshly elected leader rejects linearizable reads with
	// quicraft.ErrReadIndexNotReady until its first entry of the current
	// term commits. First-boot's existence checks hit exactly that window
	// on THIS node when it IS the leader; misclassifying it as the
	// follower case below would wait 30s for a replication that never
	// comes on a single node. Retry the transient out (bounded) before
	// classifying the failure.
	var firstBootErr error
	readyDeadline := time.Now().Add(15 * time.Second)
	for {
		initErr := runInit()
		if initErr == nil {
			return nil
		}
		if errors.Is(initErr, quicraft.ErrReadIndexNotReady) && time.Now().Before(readyDeadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		if barriers != nil {
			// Only the first-boot leg of BootstrapSystem is eligible for the
			// follower NotLeader retry; barrier/credential failures are real
			// errors and propagate unchanged.
			var fbErr *bootstrap.FirstBootError
			if !errors.As(initErr, &fbErr) {
				return initErr
			}
			firstBootErr = fbErr
		} else {
			firstBootErr = initErr
		}
		break
	}

	if firstBootErr != nil {
		// Only a NotLeader condition means "the leader will do this instead".
		// Every other failure is this node's own, and waiting for a
		// replication that nobody is going to perform turns it into a 30 s
		// timeout reported as "admin user not replicated" — a message that
		// names the symptom and hides the cause. It cost a real diagnosis:
		// a state machine that could not apply kv.OpPutIfAbsent surfaced here
		// as a replication timeout, and six tests recorded the resulting
		// startup failure as acceptable (go-qrdb#86).
		if !isNotLeader(firstBootErr) {
			return firstBootErr
		}
		slog.Info("first-boot: waiting for leader to replicate admin",
			slog.Any("reason", firstBootErr))
		return waitForAdminReplication(ctx, userSvc, tenant.SystemTenantID)
	}
	return nil
}

// isNotLeader reports whether err is the "this node is not the leader"
// condition, at either layer that expresses it.
//
// The service layer translates quicraft's NotLeaderError into its own, so a
// first-boot failure may carry either depending on how deep the proposal got.
// Both are checked because matching only one would send a genuine follower
// down the error path this function exists to keep it out of.
func isNotLeader(err error) bool {
	var svcErr *service.NotLeaderError
	if errors.As(err, &svcErr) {
		return true
	}
	var raftErr *quicraft.NotLeaderError
	return errors.As(err, &raftErr)
}

// requireGuardedBatch refuses to start a node whose state machine cannot
// evaluate a guarded batch, asking it with kvstore.ProbeGuardedBatch.
//
// Three answers end the start: "unsupported" (the --sm type has no
// MVCC/transaction subsystem), and any inconclusive failure other than
// NotLeader — the caller has already waited for a leader, so a missing one
// is not the explanation.
//
// NotLeader is the exception because it is not inconclusive about THIS
// question so much as unanswerable here: only the leader can propose, so a
// follower's probe always returns it, exactly as runFirstBoot's own writes
// do. Every replica of the shard runs the same state machine, so the
// leader's probe answers for all of them; a follower refusing on NotLeader
// would stop every multi-node cluster from starting.
func requireGuardedBatch(ctx context.Context, guarded kvstore.MultiGuardedBatchKVStore) error {
	capable, err := kvstore.ProbeGuardedBatch(ctx, guarded)
	switch {
	case err != nil && isNotLeader(err):
		slog.Info("guarded batch capability probe: not the leader; the leader's probe decides")
		return nil
	case err != nil:
		return &AuthBootstrapError{Step: "guarded batch capability probe", Err: err}
	case !capable:
		return &AuthBootstrapError{Step: "atomic role/user creation requires an --sm state machine with transaction support", Err: kvstore.ErrAtomicWriteUnsupported}
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
				Err:  &LeaderElectionTimeoutError{Attempts: maxAttempts},
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
		Err:  &AdminReplicationTimeoutError{Timeout: timeout},
	}
}

// serverServing is invoked once the regular multi-protocol server has started.
// It is nil in production and set only by tests.
//
// This is the END of startup, and it is a different milestone from
// barrierUnsealed: everything between them — initializeNode, first boot,
// service construction, the server bind — can still fail. A test that stops at
// the unseal has proved the barrier opened and nothing about the handover to a
// serving node, which is the transition it is usually named for
// (go-qrdb#96).
var serverServing func()

// barrierUnsealed is invoked once awaitUnseal has returned successfully, i.e.
// the barrier is open and startup is proceeding to the Raft host, first boot
// and the regular server. It is nil in production and set only by tests.
//
// The transition is not observable from outside the process. While sealed, the
// daemon serves a pre-host sealed-window listener on the admin port; after the
// unseal the regular server takes over THE SAME PORT, so a TCP dial cannot
// tell the two apart. A test that gated on the port accepting was therefore
// satisfied by the sealed window and never reached full startup at all, while
// being named for it (go-qrdb#83, #96).
var barrierUnsealed func()

// volumePoolAttached is invoked once the volume pool has been attached to the
// barrier registry. It is nil in production and set only by tests.
//
// The wiring it reports is not observable from outside the process, and it
// happens LATE — after the seal and volume-pool subsystems are built — while
// runStart installs its termination handler EARLY. A test that signals on a
// timer, or at t=0, can therefore cancel startup before the attachment and
// still see runStart return cleanly, passing without exercising the wiring it
// is named for (go-qrdb#83).
var volumePoolAttached func()

// sighupHandled is invoked after each SIGHUP has been processed. It is nil in
// production and set only by tests.
//
// It exists because SIGHUP handling is otherwise unobservable from outside the
// process. A test can see that the SIGNAL was delivered — its own
// signal.Notify channel receives a copy — but delivery says nothing about
// whether runStart's goroutine has run handleSIGHUP yet, and a test that
// signals SIGTERM on the strength of its own copy can shut the daemon down
// first. The reload path then never runs and the test passes anyway
// (go-qrdb#83).
var sighupHandled func()

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

// raftFollowerAdder adapts *quicraft.Host to the service.RaftHost interface.
// It wraps RequestAddNode + rs.Result() into a single blocking call that
// returns only an error, shielding DatabaseService from the quicraft import
// and from RequestState lifecycle management.
type raftFollowerAdder struct {
	host *quicraft.Host
}

// AddFollowerNode calls RequestAddNode on the shared Raft host and blocks
// until the config change commits or ctx is cancelled. Used ONLY for
// founding members at create time (service.DatabaseService.CreateForTenant);
// see AddObserver for scale-up.
func (a *raftFollowerAdder) AddFollowerNode(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	rs, err := a.host.RequestAddNode(ctx, shardID, replicaID, raftAddr, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// AddObserver calls RequestAddObserver on the shared Raft host and blocks
// until the config change commits or ctx is cancelled. Used exclusively by
// DatabaseService.ScaleForTenant's scale-up path (scaleUp) to admit a
// dynamically added replica as a NON-VOTING observer, so scale-up never
// transiently changes -- and therefore never breaks -- the group's existing
// voting quorum (see the elastic-scaling design doc §B.2/§D.4). The
// observer is later promoted to a full voting member asynchronously by
// server.TenantShardReconciler's Promoter (see raftPromoter), which reuses
// RequestAddNode against this same, already-admitted replica ID -- go-quicraft
// treats that as an observer -> voter promotion rather than a fresh add.
func (a *raftFollowerAdder) AddObserver(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	rs, err := a.host.RequestAddObserver(ctx, shardID, replicaID, raftAddr, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// RemoveFollowerNode calls RequestRemoveNode on the shared Raft host and
// blocks until the config change commits or ctx is cancelled. Used by
// DatabaseService.ScaleForTenant's scale-down path.
func (a *raftFollowerAdder) RemoveFollowerNode(ctx context.Context, shardID, replicaID uint64) error {
	rs, err := a.host.RequestRemoveNode(ctx, shardID, replicaID, 0)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		rs.Release()
		return ctx.Err()
	case result := <-rs.ResultC():
		return result.Err
	}
}

// LeaderReplicaID returns the shard's current leader replica ID via the
// shared Raft host. Used by DatabaseService.ScaleForTenant's scale-down
// replica-selection policy so the current leader is removed last.
func (a *raftFollowerAdder) LeaderReplicaID(_ context.Context, shardID uint64) (uint64, bool, error) {
	return a.host.GetLeaderID(shardID)
}

// RequestLeaderTransfer calls RequestLeaderTransfer on the shared Raft host.
// Used by service.RemoveFollowerNode's caller (removeReplicaFromGroup, see
// database_service.go's transferLeadershipAwayFromRemoved) to move
// leadership away from a replica before evicting it, so the config change
// removing it is driven to full replication by a leader that survives the
// removal rather than the replica being removed.
func (a *raftFollowerAdder) RequestLeaderTransfer(ctx context.Context, shardID, targetReplicaID uint64) error {
	return a.host.RequestLeaderTransfer(ctx, shardID, targetReplicaID)
}
