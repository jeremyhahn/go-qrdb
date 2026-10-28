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
	"crypto/fips140"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdkgo "github.com/jeremyhahn/go-qrdb/sdk/go"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// startFlags holds the parsed CLI flags for the start command.
type startFlags struct {
	// processHardening is what secure.HardenProcess did at the top of the
	// start command. It is not a flag: RunE sets it, and runStart audits an
	// honoured opt-out (ProcessInspectionAllowed).
	processHardening secure.ProcessHardening

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
	// replicaForwarding and replicaForwardingTimeout are
	// replica_forwarding.enabled and replica_forwarding.timeout
	// (docs/replica-forwarding).
	replicaForwarding        bool
	replicaForwardingTimeout string
	// observability is the config file's observability section; no flag
	// moves it. configureObservability applies it.
	observability qrdbconfig.ObservabilityConfig
	// grpcWeb is the config file's server.grpc_web section; no flag moves
	// it. buildServerConfig carries it to the server, which serves gRPC-Web
	// from the REST server when it is enabled.
	grpcWeb qrdbconfig.GRPCWebConfig
	// cors is the config file's cors section; no flag moves it.
	// buildServerConfig carries it to the server, which hands the REST
	// server a CORS policy when the section lists an allowed origin.
	cors qrdbconfig.CORSConfig
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

	invitationTTL           string
	invitationRequireKeyPin bool
	invitationRedeemLimit   qrdbconfig.InvitationRedeemLimitConfig
	// revocationCacheTTL is the auth.revocation_cache_ttl duration string.
	// Empty means the default. See qrdbconfig.AuthConfig.RevocationCacheTTL —
	// it is the window during which a revoked certificate still authenticates.
	revocationCacheTTL string
	// auditFailurePolicy is the auth.audit_failure_policy string. Empty means
	// the default, "fail-closed". See qrdbconfig.AuthConfig.AuditFailurePolicy
	// — it decides whether a decision whose audit record could not be written
	// is refused or permitted.
	auditFailurePolicy string

	// mtlsBindingPolicy is the auth.mtls_binding_policy string. Empty means
	// the default, "enrolled". See qrdbconfig.AuthConfig.MTLSBindingPolicy.
	mtlsBindingPolicy string
	abacEnabled       bool
	abacPolicyDir     string
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
	peerAdminURLs      map[string]string
	sqlDatabase        string
	tlsCAFile          string
	tlsCertFile        string
	tlsKeyFile         string
	tlsClientAuth      string
	tlsClientAuthSet   bool
	bootstrapNoiseKey  string
	bootstrapP256Key   string
	bootstrapNoiseAddr string
	bootstrapNoisePort int
	bootstrapNoiseHost string
	bootstrapSPKIPin   string
	bootstrapDANEDNS   string
	bootstrapDANEDoT   bool
	bootstrapDirectURL string
	bootstrapAddr      string

	// bootstrapJoin* configure this node's JOINER side of member-side CSR
	// join: either the Noise_NK transport (Addr+Key) or the SPKI/Direct
	// HTTPS transport (HTTPURL, reusing bootstrapSPKIPin), plus the shared
	// admission token, resolved from exactly one of a file or an
	// environment variable (never inline). See config.ResolveJoinToken.
	bootstrapJoinAddr      string
	bootstrapJoinKey       string
	bootstrapJoinP256Key   string
	bootstrapJoinHTTPURL   string
	bootstrapJoinTokenFile string
	bootstrapJoinTokenEnv  string

	// bootstrapJoinApprovalPollInterval bounds how often Join re-presents a
	// tokenless CSR (no bootstrapJoinTokenFile/-Env configured) while it
	// sits in the signing member's replicated approval queue. Zero uses
	// clusterenroll.DefaultJoinApprovalPollInterval.
	bootstrapJoinApprovalPollInterval time.Duration

	// clusterJoin* configure this node's MEMBER side of member-side CSR
	// join: whether it signs CSRs for other nodes at all, and if so, the
	// Noise_NK listener's bind address. The SPKI/Direct transport needs no
	// separate listener -- it rides this node's existing admin HTTPS server
	// once enabled. There is no admission-token flag here any more: a
	// signing member validates against the single-use, identity-bound join
	// tokens an admin mints into the replicated store (clusterenroll.
	// MintJoinToken), not a static shared secret it was configured with.
	clusterJoinEnabled    bool
	clusterJoinListenHost string
	clusterJoinListenPort int

	leaseRead   bool
	checkQuorum bool

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

	// restoreDecryptionKeyRef overrides the key reference stored in the
	// restored backup's own envelope -- useful when the key file named at
	// create time is not at that path on this node. Left empty, the
	// stored reference is used, which is the common case since the key
	// file is typically kept OUTSIDE --data-dir and so survives whatever
	// loss --restore-backup-id is recovering from. See restore.go. It is
	// now a bare file NAME inside --backup-key-dir, never a path
	// (keywrap.SoftwareProvider confines every key file to one configured
	// directory; go-qrdb security review, backup-encrypt-default #B4).
	restoreDecryptionKeyRef string

	// allowPlaintextRestore is the operator's explicit opt-in to restore
	// --restore-backup-id's backup when its OWN stored state is
	// plaintext (BackupInfo.Encryption == "none"). Left false (the
	// default), the offline restore refuses a plaintext backup with a
	// typed error before touching the shard -- never inferred from the
	// backup's own metadata, which an attacker who also deletes the
	// backup's envelope object can make internally consistent (see
	// service.RestoreOptions.AllowPlaintext's own doc comment). See
	// restore.go.
	allowPlaintextRestore bool

	// backupKeyDir is the directory the built-in "software" key-wrap
	// provider's key files must live inside, confined via *os.Root. Every
	// --encryption-key-file / --decryption-key-file /
	// --restore-decryption-key-file this daemon is given is a bare file
	// name inside it, never a path. Kept OUTSIDE --data-dir and
	// --backup-dir on purpose, same as the rest of this node's key
	// material: a backup made with a key from this directory must still
	// be restorable after --data-dir is lost entirely.
	backupKeyDir string

	// dekKeyDir is backupKeyDir's counterpart for the tenant DEK keyring's
	// own "software" provider instance: every per-tenant key reference
	// the keyring's default KeyRefFunc computes (the tenant ID itself) is
	// a bare file name inside it, never a path. A distinct directory from
	// backupKeyDir because the two naming domains (tenant IDs vs.
	// operator-chosen backup key names) are unrelated; keyWrapInjection's
	// registry/provider, when set, are shared with the DEK keyring too.
	dekKeyDir string

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

	// allowDegradedVolumeBoot mirrors storageConfig.AllowDegradedVolumeBoot
	// but is bound to its own CLI flag (--allow-degraded-volume-boot) so a
	// flag set on the command line overrides the config file's value;
	// applyConfigToFlags folds it back into storageConfig once the config
	// file (if any) has been loaded. The default is false: a volume pool
	// that fails to open fails qrdbd start outright.
	allowDegradedVolumeBoot bool

	// embeddingConfig holds the embedding provider configuration loaded from
	// YAML. When no config file is provided, the empty default applies (no
	// providers, embedding disabled).
	embeddingConfig qrdbconfig.EmbeddingConfig

	// ragConfig holds the RAG pipeline configuration loaded from YAML.
	// When no config file is provided, defaults from DefaultConfig are used.
	ragConfig qrdbconfig.RAGConfig

	// importSessionConfig bounds a chunked import session, loaded from YAML
	// ("import_session:"). When no config file is provided, the zero value
	// resolves to the package defaults WithImportSessionLimits documents.
	importSessionConfig qrdbconfig.ImportSessionConfig

	// leaseConfig sets the lease checkpoint interval and failover grace,
	// loaded from YAML ("lease:"). The zero value resolves to the lease
	// package defaults.
	leaseConfig qrdbconfig.LeaseConfig

	// tenantConfig holds the tenant defaults (default quotas + entitlements)
	// loaded from YAML, applied to tenants whose own values are unset.
	tenantConfig qrdbconfig.TenantConfig

	// createFn is an optional pre-built state machine factory. When set,
	// initializeNode uses it instead of resolving from the SM registry.
	// This enables callers to inject subsystem-aware factories (e.g.
	// with Lessor, WatchHub, TimeSeries) that share instances with the
	// service layer.
	createFn sm.CreateFunc

	// walSystemBarrier is the system seal barrier the Raft WAL is written
	// under (see database.NewRaftWAL). It is nil when seal is disabled and is
	// set only after the barrier is unsealed, before the Host is constructed.
	walSystemBarrier *seal.EpochBarrier

	// walMux is the multiplex LogDB wired as the host's LogDB whenever seal is
	// enabled, on a single node or a cluster, whether the system barrier
	// encrypts or is still in plaintext mode. It owns the shared system-barrier
	// WAL and routes dedicated-WAL shards, a tenant's home group included, to
	// per-tenant-KEY WALs. It is nil only when seal is disabled.
	walMux *database.MultiplexLogDB

	// leaderEvents is the relay installed as the host's event listener; the
	// tenant infrastructure binds its IsolationRaft factory to it, so a node
	// that becomes a tenant home group's leader is told.
	leaderEvents *server.LeaderEvents
}

// raftNode is the interface that the start command requires for shard
// lifecycle operations. Both *quicraft.Host and test mocks satisfy it.
type raftNode interface {
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg quicraftconfig.Config) error
	LeaderID(shardID uint64) (uint64, bool, error)
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
	if f.configFile != "" {
		cfg, err := qrdbconfig.Load(f.configFile)
		if err != nil {
			return err
		}
		if err := cfg.ValidateCapabilities(); err != nil {
			return err
		}
		if err := objstore.RejectCredentialSettings(cfg.Storage.Objstore.Settings); err != nil {
			return &qrdbconfig.ValidationError{
				Field:  "storage.objstore.settings",
				Reason: "carries a credential inline; reference it by file under storage.objstore.credentials",
				Err:    err,
			}
		}
		applyConfigToFlags(cmd, f, cfg)
	} else if f.authEnabled {
		// No --config at all, and auth is enabled (its own flag default):
		// there is no YAML to default the seal subsystem the way
		// qrdbconfig.Load (DefaultConfig() overlaid by the file) already
		// does, and qrdbd start has no --seal-* flag of its own. Default it
		// here the identical way (go-qrdb#548): auth.enabled requires
		// seal.enabled, and a bare `qrdbd start` must boot its own defaults
		// rather than refuse them. "none" is identity passthrough, not
		// encryption; an operator who wants real encryption still needs
		// --config. Conditioned on f.authEnabled, not unconditional: an
		// operator who explicitly passes --auth-enabled=false is choosing
		// the simpler, seal-less boot the auth-requires-seal rule never
		// applies to, and this must not force a sealed window on them.
		f.sealConfig = qrdbconfig.DefaultConfig().Seal
	}

	// One validation path for both branches above (go-qrdb#548): overlay
	// the handful of fields flags can actually move -- Auth.Enabled, Seal,
	// Databases -- onto qrdbconfig.DefaultConfig(), the SAME starting point
	// qrdbconfig.Load uses before its own overlay, and run the result
	// through config.Validate. That catches a flag-only request's
	// auth/seal mismatch here, with the SAME typed ValidationError the
	// --config path already gets, instead of surfacing confusingly deep
	// inside boot. The base is DefaultConfig(), not buildServerConfig's
	// bare struct literal: buildServerConfig only ever fed
	// ValidateCapabilities, a narrower check, and leaves fields Validate
	// checks unconditionally (admin_username, among others) at their zero
	// value, which is not a flag-only request's fault to fail on. Databases
	// defaults the same way runStart's own fallback does (len(f.databases)
	// == 0 below), applied here too so an unset Databases does not itself
	// read as invalid.
	if len(f.databases) == 0 {
		f.databases = qrdbconfig.DefaultConfig().Databases
	}
	validateCfg := qrdbconfig.DefaultConfig()
	validateCfg.Auth.Enabled = qrdbconfig.BoolPtr(f.authEnabled)
	validateCfg.Auth.ClusterTrustDomain = f.clusterTrustDomain
	validateCfg.Seal = f.sealConfig
	validateCfg.Databases = f.databases
	if err := validateCfg.Validate(); err != nil {
		return err
	}
	// Resolve the trust domain only once its inputs are final:
	// --cluster-trust-domain overrides the YAML, and an empty value takes
	// the default, so the CA and the authenticator never see an empty one.
	f.clusterTrustDomain = validateCfg.Auth.EffectiveClusterTrustDomain()
	return nil
}

// configureObservability sets the process-wide signals start resolves
// before anything else runs: the global logger, early, so every pre-host log
// line (auto-generated replica ID, etc.) uses the requested format and
// level; and the slow-sync threshold every Raft WAL and storage backend
// observer applies, before any of them opens.
func configureObservability(f *startFlags) error {
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
	return f.observability.ApplySlowSyncThreshold()
}

// compiledProtocolListeners lists the wire protocols --host actually binds
// on this binary: the four tier-1 transports every build carries, plus
// MySQL and/or PgSQL only when their build tag compiled a listener for
// them. appendMySQLListener/appendPgSQLListener are no-ops on a binary
// without the matching tag (start_{mysql,pgsql}_flags_stub.go), so the help
// text never names a protocol this binary cannot bind --host to.
func compiledProtocolListeners() []string {
	protocols := []string{"REST/HTTP3", "gRPC", "QUIC", "MCP"}
	protocols = appendMySQLListener(protocols)
	protocols = appendPgSQLListener(protocols)
	return protocols
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
			// Process hardening is the FIRST thing start does: before the
			// configuration (which may name secret references) is read, before
			// the budget, before anything protected exists, so nothing this
			// process ever holds is readable by a debugger or a same-user
			// process through ptrace or /proc/<pid>/mem (go-qrdb#554). A
			// refusal is fatal. An honoured opt-out is carried to runStart,
			// which records it in the compliance audit trail once that trail
			// exists.
			hardening, herr := hardenSecureProcess(slog.Default())
			if herr != nil {
				return herr
			}
			f.processHardening = hardening

			if err := resolveStartFlags(cmd, f); err != nil {
				return err
			}

			if err := configureObservability(f); err != nil {
				return err
			}

			if err := resolveAutoConfig(cmd, f); err != nil {
				return err
			}
			if err := validateClusterJoinFlags(f); err != nil {
				return err
			}
			return runStart(f)
		},
	}

	cmd.Flags().StringVar(&f.configFile, "config", "", "Path to YAML configuration file")
	cmd.Flags().StringVar(&f.mode, "mode", "raft", "Run mode: raft (default; single- or multi-node consensus) or standalone (no raft; the whole data plane on one node, without the cluster, backup, rebalance or etcd families)")
	cmd.Flags().StringVar(&f.host, "host", qrdbconfig.DefaultServerHost, "Bind host for all API listeners ("+strings.Join(compiledProtocolListeners(), ", ")+"); maps to server.host / QRDB_SERVER_HOST")
	cmd.Flags().StringVar(&f.listen, "listen", "0.0.0.0:63001", "Listen address (host:port)")
	cmd.Flags().StringVar(&f.raftAddress, "raft-address", "127.0.0.1:63001", "Raft address visible to peers")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "./data", "Data directory for WAL and metadata")
	cmd.Flags().Uint64Var(&f.deploymentID, "deployment-id", 1, "Deployment ID for cluster isolation")
	cmd.Flags().StringVar(&f.smType, "sm", "pebble", "State machine type: pebble (default), mvcc, memory, standard, concurrent, fast, disk, highperf or regular. Every type serves KV, batches, the guarded verbs and transactions; only mvcc keeps revisions")
	cmd.Flags().StringVar(&f.smBackend, "sm-backend", "", "State machine backend: standard, concurrent, fast, disk")
	cmd.Flags().StringVar(&f.smStorage, "sm-storage", "", "Storage backend for the storage-generic state machines (sm type disk/fast): pebble (default), file, memory. Keep this identical across every replica of a shard")
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
	cmd.Flags().BoolVar(&f.replicaForwarding, "replica-forwarding", true, "Forward a request for a database this node holds no replica of to a node that does, as the caller (raft mode)")
	cmd.Flags().StringVar(&f.replicaForwardingTimeout, "replica-forwarding-timeout", "", "Bound on one replica forward, connection included (Go duration; default 10s)")
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
		"Number of applied entries between automatic snapshots (0 = default, 100000)")
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
	registerMySQLFlags(cmd, f)
	registerPgSQLFlags(cmd, f)
	registerSQLFlags(cmd, f)
	cmd.Flags().StringVar(&f.tlsCAFile, "tls-ca-file", "", "Path to CA certificate PEM file")
	cmd.Flags().StringVar(&f.tlsCertFile, "tls-cert-file", "", "Path to TLS certificate PEM file")
	cmd.Flags().StringVar(&f.tlsKeyFile, "tls-key-file", "", "Path to TLS private key PEM file")
	cmd.Flags().StringVar(&f.bootstrapNoiseKey, "bootstrap-noise-key", "", "Hex Curve25519 public key for Noise bootstrap")
	cmd.Flags().StringVar(&f.bootstrapP256Key, "bootstrap-p256-key", "", "Hex uncompressed P-256 public key of the bootstrap server, for the FIPS-approved suite (required under FIPS 140-3 only mode)")
	cmd.Flags().StringVar(&f.bootstrapNoiseAddr, "bootstrap-noise-addr", "", "host:port of Noise bootstrap server")
	cmd.Flags().IntVar(&f.bootstrapNoisePort, "bootstrap-noise-port", 63006, "TCP port for the Noise bootstrap (trust-anchor) server every admitted, unsealed member runs")
	cmd.Flags().StringVar(&f.bootstrapNoiseHost, "bootstrap-noise-host", "", "Bind host for the Noise bootstrap server; maps to bootstrap.noise_host, defaults to --host")
	cmd.Flags().StringVar(&f.bootstrapSPKIPin, "bootstrap-spki-pin", "", "Hex SHA-256 SPKI pin for bootstrap server")
	cmd.Flags().StringVar(&f.bootstrapDANEDNS, "bootstrap-dane-dns", "", "DNS server for DANE TLSA lookup (e.g. 8.8.8.8:53)")
	cmd.Flags().BoolVar(&f.bootstrapDANEDoT, "bootstrap-dane-dot", false, "Use DNS-over-TLS for DANE")
	cmd.Flags().StringVar(&f.bootstrapDirectURL, "bootstrap-direct-url", "", "Full HTTPS URL for direct bootstrap")
	cmd.Flags().StringVar(&f.bootstrapAddr, "bootstrap-addr", "", "host:port of first node's admin server (for SPKI/DANE)")
	cmd.Flags().StringVar(&f.bootstrapJoinAddr, "bootstrap-join-addr", "", "host:port of an admitted member's Noise_NK join listener (member-side CSR join)")
	cmd.Flags().StringVar(&f.bootstrapJoinKey, "bootstrap-join-key", "", "Hex Curve25519 public key of that join listener")
	cmd.Flags().StringVar(&f.bootstrapJoinP256Key, "bootstrap-join-p256-key", "", "Hex uncompressed P-256 public key of that join listener, for the FIPS-approved suite (required under FIPS 140-3 only mode)")
	cmd.Flags().StringVar(&f.bootstrapJoinHTTPURL, "bootstrap-join-url", "", "Base admin HTTPS URL of an admitted member, for the SPKI/Direct join transport (e.g. https://10.0.0.2:8443)")
	cmd.Flags().StringVar(&f.bootstrapJoinTokenFile, "bootstrap-join-token-file", "", "Path to a file holding the member-side join admission secret")
	cmd.Flags().StringVar(&f.bootstrapJoinTokenEnv, "bootstrap-join-token-env", "", "Name of an environment variable holding the member-side join admission secret")
	cmd.Flags().DurationVar(&f.bootstrapJoinApprovalPollInterval, "bootstrap-join-approval-poll-interval", 0,
		"How often to re-present a tokenless CSR while it awaits operator approval (default: clusterenroll.DefaultJoinApprovalPollInterval)")
	cmd.Flags().BoolVar(&f.clusterJoinEnabled, "cluster-join-enabled", false, "Sign CSRs for joining nodes (Noise_NK listener + SPKI/Direct HTTPS route), as an admitted member")
	cmd.Flags().StringVar(&f.clusterJoinListenHost, "cluster-join-listen-host", "", "Bind host for the Noise_NK join listener; defaults to --host")
	cmd.Flags().IntVar(&f.clusterJoinListenPort, "cluster-join-listen-port", clusterenroll.DefaultJoinPort, "TCP port for the Noise_NK join listener")
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
	cmd.Flags().StringVar(&f.restoreDecryptionKeyRef, "restore-decryption-key-file", "",
		"Name of the key file, inside --backup-key-dir, that overrides the key reference stored in the restored backup's own envelope (only needed if the backup's own key reference is not usable on this node)")
	cmd.Flags().BoolVar(&f.allowPlaintextRestore, "allow-plaintext-restore", false,
		"Explicit opt-in required to restore --restore-backup-id's backup when its own stored state is plaintext; refused with a typed error otherwise")
	cmd.Flags().BoolVar(&f.allowDegradedVolumeBoot, "allow-degraded-volume-boot", false,
		"Explicit opt-in to start qrdbd when a configured volume pool (storage.volume_pools) fails to open; starts every volume and database this node can and refuses only the ones on the failed pool, instead of the default: fail startup outright, naming every inaccessible volume and the reason")
	cmd.Flags().StringVar(&f.backupKeyDir, "backup-key-dir", "",
		"Directory the built-in \"software\" key-wrap provider's key files must live inside; every backup/restore key reference is a bare file name inside it, never a path (required to create or restore an encrypted backup with the built-in provider)")
	cmd.Flags().StringVar(&f.dekKeyDir, "dek-key-dir", "",
		"Directory the built-in \"software\" key-wrap provider's key files must live inside for the tenant DEK keyring; every tenant's key reference (its tenant ID, by default) is a bare file name inside it, never a path (required for dek encrypt/rotate with the built-in provider)")

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
	if !cmd.Flags().Changed("replica-forwarding") {
		f.replicaForwarding = cfg.ReplicaForwarding.On()
	}
	if !cmd.Flags().Changed("replica-forwarding-timeout") && cfg.ReplicaForwarding.Timeout != "" {
		f.replicaForwardingTimeout = cfg.ReplicaForwarding.Timeout
	}
	f.observability = cfg.Observability
	f.grpcWeb = cfg.Server.GRPCWeb
	f.cors = cfg.CORS
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
	f.invitationTTL = cfg.Auth.InvitationTTL
	f.invitationRequireKeyPin = cfg.Auth.InvitationRequireKeyPin
	f.invitationRedeemLimit = cfg.Auth.InvitationRedeemLimit
	f.revocationCacheTTL = cfg.Auth.RevocationCacheTTL
	f.auditFailurePolicy = cfg.Auth.AuditFailurePolicy
	f.mtlsBindingPolicy = cfg.Auth.MTLSBindingPolicy
	// ABAC decorator: config-only (no CLI flag).
	f.abacEnabled = cfg.Auth.ABAC.Enabled
	f.abacPolicyDir = cfg.Auth.ABAC.PolicyDir
	if !cmd.Flags().Changed("cluster-trust-domain") && cfg.Auth.ClusterTrustDomain != "" {
		f.clusterTrustDomain = cfg.Auth.ClusterTrustDomain
	}
	if m := lifecycle.PeerAdminURLMap(cfg.Auth.PeerAdminURLs); m != nil {
		f.peerAdminURLs = m
	}
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
	if !cmd.Flags().Changed("bootstrap-p256-key") && cfg.Bootstrap.P256Key != "" {
		f.bootstrapP256Key = cfg.Bootstrap.P256Key
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
	if !cmd.Flags().Changed("bootstrap-join-addr") && cfg.Bootstrap.JoinAddr != "" {
		f.bootstrapJoinAddr = cfg.Bootstrap.JoinAddr
	}
	if !cmd.Flags().Changed("bootstrap-join-key") && cfg.Bootstrap.JoinKey != "" {
		f.bootstrapJoinKey = cfg.Bootstrap.JoinKey
	}
	if !cmd.Flags().Changed("bootstrap-join-p256-key") && cfg.Bootstrap.JoinP256Key != "" {
		f.bootstrapJoinP256Key = cfg.Bootstrap.JoinP256Key
	}
	if !cmd.Flags().Changed("bootstrap-join-url") && cfg.Bootstrap.JoinHTTPURL != "" {
		f.bootstrapJoinHTTPURL = cfg.Bootstrap.JoinHTTPURL
	}
	if !cmd.Flags().Changed("bootstrap-join-token-file") && cfg.Bootstrap.JoinTokenFile != "" {
		f.bootstrapJoinTokenFile = cfg.Bootstrap.JoinTokenFile
	}
	if !cmd.Flags().Changed("bootstrap-join-approval-poll-interval") && cfg.Bootstrap.JoinApprovalPollInterval != 0 {
		f.bootstrapJoinApprovalPollInterval = cfg.Bootstrap.JoinApprovalPollInterval
	}
	if !cmd.Flags().Changed("bootstrap-join-token-env") && cfg.Bootstrap.JoinTokenEnv != "" {
		f.bootstrapJoinTokenEnv = cfg.Bootstrap.JoinTokenEnv
	}
	if !cmd.Flags().Changed("cluster-join-enabled") {
		f.clusterJoinEnabled = cfg.ClusterJoin.Enabled
	}
	if !cmd.Flags().Changed("cluster-join-listen-host") && cfg.ClusterJoin.ListenHost != "" {
		f.clusterJoinListenHost = cfg.ClusterJoin.ListenHost
	}
	if !cmd.Flags().Changed("cluster-join-listen-port") && cfg.ClusterJoin.ListenPort != 0 {
		f.clusterJoinListenPort = cfg.ClusterJoin.ListenPort
	}
	// A bind host left unset defaults to --host, exactly as
	// bootstrap-noise-host already does (go-qrdb#167): the join listener is
	// another socket a node contained to one interface must not silently
	// expose on every interface.
	if f.clusterJoinListenHost == "" {
		f.clusterJoinListenHost = f.host
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
	// definitions that barrier-mode tenants depend on. The one exception
	// is AllowDegradedVolumeBoot, which also has the
	// --allow-degraded-volume-boot flag: an explicit flag wins over the
	// config file's allow_degraded_volume_boot.
	f.storageConfig = cfg.Storage
	if cmd.Flags().Changed("allow-degraded-volume-boot") {
		f.storageConfig.AllowDegradedVolumeBoot = f.allowDegradedVolumeBoot
	}

	// Embedding and RAG configs are always inherited from YAML — there are
	// no CLI flag equivalents for the provider registry or chunking parameters.
	f.embeddingConfig = cfg.Embedding
	f.ragConfig = cfg.RAG

	// Import session config is always inherited from YAML — there is no
	// CLI flag equivalent for a chunked import session's quotas.
	f.importSessionConfig = cfg.ImportSession
	f.leaseConfig = cfg.Lease
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
	// with. See netport.FreePortOn (go-qrdb#257, go-qrdb#253).

	// Auto-select a free port if listen/raft-address use the default port.
	if !cmd.Flags().Changed("listen") && !cmd.Flags().Changed("raft-address") && f.configFile == "" {
		// The Raft transport is QUIC, so UDP alone. f.raftAddress is the
		// loopback address peers DIAL, not a bind, so the wildcard f.listen
		// is the whole bind surface and the scope to probe at -- and a free
		// wildcard bind implies the loopback one, never the reverse.
		port, err := netport.FreePortOn(raftListenHost, netport.NetworkUDP)
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
		port, err := netport.FreePortOn(f.host, netport.NetworkTCP, netport.NetworkUDP)
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
		port, err := netport.FreePortOn(f.host, netport.NetworkTCP)
		if err != nil {
			return err
		}
		f.grpcPort = port
		slog.Info("auto-selected gRPC port", "grpc_port", port)
	}

	// Auto-select QUIC port if not explicitly set and QUIC is enabled.
	if f.enableQUIC && !cmd.Flags().Changed("quic-port") && f.configFile == "" {
		// UDP-only, and likewise probed at f.host rather than at loopback.
		port, err := netport.FreePortOn(f.host, netport.NetworkUDP)
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
//
// hardenSecureProcess is indirected for the same reason: secure.HardenProcess
// makes the calling process non-dumpable (Linux) or refuses every later
// debugger attach (macOS), and neither can be undone, so the test binary that
// drives the start command must not apply it to itself. TestMain doubles it; the real
// mechanism is proven from a subprocess in pkg/secure.
var (
	hardenSecureProcess    = secure.HardenProcess
	newSecureAllocator     = secure.NewAllocatorWithinRLimit
	setSecureAllocator     = secure.SetAllocator
	processSecureAllocator = secure.ProcessAllocator
	wipeSecureMemory       = secure.WipeAll
)

// secureMemoryWipeTimeout bounds the shutdown sweep recordSecureMemoryWipe
// runs. There is no caller context at process shutdown to inherit a deadline
// from — this is the deliberate exception go-enclave's WipeAll documents:
// give a shutdown sweep a deadline taken at shutdown rather than
// context.Background() itself, which would wait as long as the borrows it
// owes take.
const secureMemoryWipeTimeout = 30 * time.Second

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
	ctx, cancel := context.WithTimeout(context.Background(), secureMemoryWipeTimeout)
	defer cancel()
	remaining, werr := wipeSecureMemory(ctx)
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
	// Discovery/peer flag validation runs FIRST, before anything with a
	// side effect -- the secure memory budget, TLS/bootstrap servers, and
	// (since a flag-only `qrdbd start` now defaults to seal enabled,
	// 2123f06c) the sealed-window listener, which blocks waiting for an
	// operator to unseal. Before this, an invalid --discovery value or a
	// malformed --peers entry reached that wait before ever being checked
	// -- initializeNode's own parsePeers call, and the discovery-method
	// dispatch, ran only after the sealed window opened, so bad flags HUNG
	// instead of exiting with a typed error. cluster.ValidateOptions
	// performs the identical checks BuildDiscovery's own builders apply,
	// without constructing anything or starting a side effect of its own.
	if err := cluster.ValidateOptions(f.clusterOptions()); err != nil {
		return err
	}

	// Refuse a protocol the config enables but this binary was not built
	// with, before any resource is allocated -- the secure-memory budget,
	// the TLS bootstrap material, a bound listener. resolveStartFlags
	// already calls this when --config names a YAML file; flags given
	// directly on the command line never went through that path, so
	// --enable-mysql (or --enable-pgsql) on a binary built without the
	// matching tag would otherwise fall through to startMySQLFn / startPgSQLFn
	// staying nil and the flag being silently dropped, which is exactly the
	// "silently ignored" outcome config.ValidateCapabilities exists to
	// prevent (pkg/config/mode.go). Calling it unconditionally here, ahead of
	// the per-protocol flag/YAML precedence resolved in resolveStartFlags,
	// makes the refusal reach every invocation shape rather than only the
	// --config one.
	//
	// buildServerConfig failing here is itself reported, not swallowed: its
	// one failure mode (MissingBindHostError) is a real misconfiguration
	// that deserves to surface at the earliest point it is detected, not be
	// silently skipped past so the capability gate quietly lets the request
	// through unexamined and some later step fails on it indirectly.
	capCheckCfg, cerr := buildServerConfig(f)
	if cerr != nil {
		return cerr
	}
	if err := capCheckCfg.ValidateCapabilities(); err != nil {
		return err
	}

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

	// clusterJoin is the member-side CSR-sign (Noise_NK) listener started
	// below once the cluster CA provider is confirmed persisted, mirroring
	// bootServers: declared here so the deferred Stop below covers every
	// return between its start and the explicit shutdown path, including the
	// early migration-recycle returns that never reach that path. The Stop
	// call is wrapped in a closure, unlike bootServers.Stop above, because
	// joinServer's final value is not known until well after this defer is
	// registered -- a bare `defer joinServer.Stop()` here would bind the
	// nil receiver immediately and never see the assignment below.
	var clusterJoin *lifecycle.ClusterJoin
	defer func() { clusterJoin.Stop() }()

	// memberAnchorAdv is the anchor-only Noise listener for a member that
	// resolveTLS did not already start one for (bootServers above): every
	// admitted, unsealed member now advertises the cluster CA trust anchor,
	// closing the single point of failure where only the node that
	// generated it ever could. Declared here for the same reason
	// clusterJoin is: the deferred Stop must cover every return between its
	// start (well below) and the explicit shutdown path, and it is wrapped
	// in a closure because its final value is not known yet.
	var memberAnchorAdv *clusterenroll.Advertiser
	defer func() { memberAnchorAdv.Stop() }()

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

	// pkg/clienttrust reads TLSConfig.ClientCAs fresh on every connection as
	// its Key A baseline (see wireClientTrustPool), so that field must carry
	// Key A before any listener can accept a connection. applyTLSClientAuth
	// above already sets it when the operator chose a client-auth mode
	// explicitly; set it here unconditionally too (idempotent when it
	// already matches) so the invariant holds on the default path as well,
	// which only assigns it later, right before server construction.
	bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool

	// Install the dynamic client-auth trust pool before any listener starts,
	// for the same reason: it must be live for the pre-host sealed-window
	// listener too. Until SetKeyBProvider is called (after the service graph
	// below constructs a CertProvider), the hook holds Key A only -- the
	// correct pre-unseal state.
	bundle.wireClientTrustPool()

	// The admin's login certificate is no longer generated here as a
	// per-node file (go-qrdb#548): a per-node admin key written to disk is
	// both node-local state (violates "no node-local state; everything
	// survives backup/restore") and an uncontrolled generate-path credential
	// (violates "generate is dev/test/evaluation only, key returned once and
	// never persisted"). The admin's certificate now comes from exactly one
	// of three paths through `qrdb init` (ceremony.CeremonyService.Initialize):
	// CSR-sign, bring-your-own-certificate, or generate-and-return-once --
	// see docs/firstboot/user.md. First boot binds whichever one the operator
	// chose once the admin account exists.

	// Create subsystems that must be shared between the state machine
	// and the service layer. The Lessor handles lease lifecycle inside
	// the SM, the WatchHub delivers change events to watchers, and
	// time-series storage is lazily created by the SM when the DB opens.
	//
	// The log registry is not among them: buildSMCreateFunc attaches a
	// fresh vlog.Chain -- the verifiable logs and the replicated audit
	// chain's instance -- to every mvcc/pebble state machine it creates,
	// unconditionally, exactly like lr is attached: it must never be
	// conditioned on per-node configuration, because a replica missing it
	// answers protocol.OpAuditAppend with a refusal while a sibling
	// replica that has it applies the write, which is state divergence
	// within the same Raft group, not a degraded feature. Whether the
	// opcode is ever proposed is the featuregate.AuditAppend gate's
	// decision, not this attachment's.
	lr := lease.NewLessor()
	watchHub := watch.NewHub()

	// Run one-time migration from flat layout to per-database structure.
	if err := database.MigrateIfNeeded(slog.Default(), f.dataDir); err != nil {
		return err
	}

	// Initialize the database manager. A raft database's shard ID is claimed
	// from the replicated store (lifecycle.BuildServices wires the claimer),
	// never issued from anything on this node's disk.
	dbManager := database.NewManager(slog.Default(), f.dataDir)
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
	if err := validateConfiguredDatabases(f); err != nil {
		return err
	}

	// The configured databases -- and --sql-database -- are provisioned
	// AFTER lifecycle.BuildServices below attaches the keyspace reclaimer
	// to dbManager (provisionDatabases): a database created without one
	// cannot clear a tombstone an earlier drop of its name left in the
	// store, and would refuse every write.

	// Register the signal handler before any blocking initialization
	// (seal subsystem build, optional pre-host unseal wait, host
	// construction) so that SIGINT/SIGTERM during these phases triggers
	// a clean cancellation rather than an abrupt process exit.
	sigCtx, sigStop := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

	// The trust pool's handshake hook only loads the snapshot this loop
	// builds; it never reads storage itself. Started once, for the whole
	// process: SetClusterCA, SetKeyBProvider and every Invalidate below
	// kick it to rebuild.
	go bundle.TrustPool().Run(sigCtx)

	// The compliance audit trail is opened BEFORE the seal subsystem, so it
	// exists through the sealed window: share holders claim their envelopes
	// and gather unseal quorums there, and those events are recorded on it
	// (lifecycle.Infra.AuditLogger). It is the node's one trail, for the
	// life of the process -- every boot iteration, an in-process migration's
	// included, records on it -- and the node does not start without it
	// (openComplianceAudit).
	// The audit failure posture is resolved before the trail is opened: the
	// holder operations of the sealed window follow it, and an unrecognised
	// value fails the boot here rather than after the window.
	auditPolicy, err := audit.ParseFailurePolicy(f.auditFailurePolicy)
	if err != nil {
		return err
	}
	// Before this point nothing has created --data-dir: that used to be a
	// side effect of initSealSubsystem's own os.MkdirAll on its "seal"
	// subdirectory, which ran before the audit trail did. Now that the
	// trail opens first, a data directory an operator (or an offline
	// restore test) wiped entirely and expects this process to recreate
	// has nothing to create it, and openTrailDir's os.OpenRoot fails
	// closed on a directory that does not exist yet.
	if err := os.MkdirAll(f.dataDir, 0o700); err != nil {
		return &DataDirError{Path: f.dataDir, Err: err}
	}
	complianceAudit, err := openComplianceAudit(sigCtx, f.dataDir, f.processHardening)
	if err != nil {
		return err
	}
	defer func() { _ = complianceAudit.Close() }()

	// System-scope enable-encryption recycles the data plane IN-PROCESS: the
	// migration handler enqueues onto migrateCh and the boot loop below tears the
	// data plane down, re-encrypts the system host's stores, adopts the keyed
	// root key, and re-runs the boot body (which rebuilds the seal subsystem from
	// the now-keyed root key). The OS process never exits. Tenant-scope migration
	// is handled separately by the per-tenant Host registry.
	migrateCh := make(chan *systemMigrationRequest, 1)
	sysMigrator := buildSysMigrator(f, migrateCh)
	// carriedCeremony is the ceremony service an in-process migration carries
	// from one boot iteration to the next. It holds the enrolled share holders
	// and the envelopes a migration to Shamir has just distributed; a fresh
	// instance would lose them and leave a sealed barrier no holder can open.
	var carriedCeremony *ceremony.CeremonyService
	// The API servers' sockets outlive every server this process runs -- the
	// sealed-window listener, the node's own server, and the one each boot
	// iteration rebuilds -- so no handover between them releases a port
	// (serverSockets).
	var sockets serverSockets
	defer sockets.Close()
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
			// (2) thread store.WithEncryptor into the SM factory, and (3)
			// run a pre-host sealed-window listener that holds the boot until
			// the operator drives unseal via the public API.
			var sealSub *sealSubsystem
			if f.sealConfig.Enabled {
				var subErr error
				sealSub, subErr = initSealSubsystem(f, sigCtx, complianceAudit)
				if subErr != nil {
					return subErr
				}
				defer func() { _ = sealSub.Close() }()
			}

			// Open every pool this node's configuration maps (storage.volume_pools)
			// through a fresh volume.Registry. A Lockable volume (e.g. go-xkms's
			// luks2 provider) is Tracked on sealSub.VolumeLifecycle HERE, before
			// the sealed-window unseal below: VolumeLifecycle's OnUnsealed observer
			// was registered before sealSub.BarrierRegistry's own (decision D6), so
			// "unlock before stores open" holds by construction order, but only if
			// the Lockable is tracked before unseal actually runs. The replicated
			// catalog (volume.Catalog) backing WithVolumeAllocator cannot be built
			// yet -- it needs the replicated KV service, which does not exist until
			// after the data-plane host comes up -- so that attach happens later,
			// at kvSvc/txSvc's definition below.
			nodeID := strconv.FormatUint(f.replicaID, 10)
			volumeRegistry, volRegErr := lifecycle.NewVolumeRegistry()
			if volRegErr != nil {
				return volRegErr
			}
			openVolumes, volumePoolRows := lifecycle.OpenConfiguredVolumes(sigCtx, nodeID, f.storageConfig.VolumePools, volumeRegistry, slog.Default())
			defer lifecycle.CloseOpenedVolumes(context.Background(), volumePoolRows, slog.Default())
			// Owner ruling 2026-10-06: a volume pool that fails to open at
			// boot fails qrdbd start outright by default, naming every
			// inaccessible volume and the reason — it does not serve with
			// missing data. --allow-degraded-volume-boot (or its config-file
			// equivalent storage.allow_degraded_volume_boot) is the explicit,
			// operator-chosen opt-in to start every volume and database this
			// node CAN open and skip the rest; OpenConfiguredVolumes has
			// already logged each failure at ERROR and recorded it in
			// qrdb_volume_pool_ready, and BuildNodeReport (below) keeps
			// reporting the skip in this node's health for as long as it
			// lasts.
			if failed := lifecycle.FailedOpens(volumePoolRows); len(failed) > 0 && !f.storageConfig.AllowDegradedVolumeBoot {
				return &volume.OpenAtBootError{Failures: failed}
			}
			if sealSub != nil && sealSub.VolumeLifecycle != nil {
				for _, row := range volumePoolRows {
					if lockable, ok := row.Vol.(volume.Lockable); ok {
						if terr := sealSub.VolumeLifecycle.Track(row.Name, lockable); terr != nil {
							return terr
						}
					}
				}
			}

			// Barrier-mode (IsolationBarrier) tenants that use the generic,
			// pool-name-free registration primitives (RegisterBarrierTenant and
			// friends) still need a per-tenant Pebble registry; attach it now so
			// it is available even before the replicated catalog is (below).
			perTenantReg := pebblestore.NewPerTenantRegistry()
			defer func() { _ = perTenantReg.CloseAll(context.Background()) }()
			if sealSub != nil && sealSub.BarrierRegistry != nil {
				sealSub.BarrierRegistry.Attach(
					seal.WithVolumeResolver(openVolumes),
					seal.WithPerTenantRegistry(perTenantReg),
				)
				// The resolver/per-tenant-registry half of the wiring is
				// observable here, before any unseal: unlike the allocator
				// (the replicated catalog, attached once the data-plane host
				// exists -- see the WithVolumeAllocator attach below), it
				// needs nothing the host provides. A seal strategy requiring
				// operator unseal never reaches that later attach at all, so
				// a test naming BOTH seal and a volume pool configured
				// together observes the wiring here.
				if len(f.storageConfig.VolumePools) > 0 && volumePoolAttached != nil {
					volumePoolAttached()
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
				sealedCfg, err := buildServerConfig(f)
				if err != nil {
					return err
				}
				handover, err := sockets.options(sealedCfg)
				if err != nil {
					return &SealedListenerStartupError{Err: err}
				}
				sealNode, err := awaitUnseal(sigCtx, f, bundle, sealSub, ceremonyCA, carriedCeremony, complianceAudit, handover...)
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
			var svcHost service.AdminHost
			var rnode raftNode
			var nodeCloser io.Closer
			// featureGate decides when this host's Raft groups may be sent
			// guarded writes; nil for a host with no transport, whose groups
			// hold only this binary (see raftbackend.NewFeatureGate).
			var featureGate *featuregate.Gate
			// localBackend is this node's primary engine backend type,
			// threaded into lifecycle.Infra.Backend below so BuildServices
			// decides vector storage's placement by the engine's declared
			// type (go-qrdb#578), not by f.dataDir's emptiness. Raft mode
			// (the else branch) never reassigns it: a replicated node
			// always persists, so the declared default -- Pebble -- is
			// already correct there.
			localBackend := database.BackendPebble
			if standalone {
				if len(f.databases) > 0 && f.databases[0].Backend != "" {
					localBackend = database.BackendType(f.databases[0].Backend)
				}
				if localBackend == database.BackendRaft {
					return &StandaloneRaftBackendError{}
				}
				stg, pdb, lerr := lifecycle.NewLocalStorage(localBackend, f.dataDir, enc, f.storageConfig.Objstore)
				if lerr != nil {
					return lerr
				}
				eng, lerr := lifecycle.NewLocalEngine(localBackend, stg, pdb, watchHub, lr)
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
				h, err := initializeNode(sigCtx, f, bundle, complianceAudit, auditPolicy)
				if err != nil {
					return err
				}
				host = h
				gate, gerr := raftbackend.NewFeatureGate(host, f.replicaID, shard.ShardKV)
				if gerr != nil {
					_ = shutdownNode(host)
					return gerr
				}
				featureGate = gate
				svcHost = newHostAdapter(host, raftbackend.WithFeatureGate(featureGate))
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
				bootHost = newBootstrapHostAdapter(newHostAdapter(host, raftbackend.WithFeatureGate(featureGate)))
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
			var pinBackend pin.PINBackend
			if sealSub != nil {
				sealBarrier = sealSub.SealBarrier
				credCoreSvc = sealSub.CredCoreSvc
				barrierRegistry = sealSub.BarrierRegistry
				pinBackend = sealSub.PINBackend
			}
			// nodeSeal is the node barrier as the background proposers see it
			// (go-qrdb#662): while it is sealed the node's log cannot take an
			// entry, so the advertisers skip their writes and lease expiry holds.
			var nodeSeal cluster.SealProbe
			if sealBarrier != nil {
				nodeSeal = sealBarrier
			}

			// Build the replicated volume-pool catalog and attach it as the
			// registry's volume.Allocator BEFORE lifecycle.BuildServices: the
			// TenantService BuildServices constructs needs this allocator
			// AT CONSTRUCTION TIME (reserveAndRegisterVolumeTenant reads it
			// off the registry via buildTenantServiceOpts), so the attach
			// cannot wait for BuildServices' own output. volume.NewCatalog
			// does no I/O at construction -- only Reserve/Resolve calls made
			// later, once an operator actually creates a tenant, touch the
			// shard -- so the KV/Transaction pair built directly over
			// svcHost here is safe to use well before the shard is ready to
			// serve a linearizable read.
			if barrierRegistry != nil {
				catalogKVSvc := lifecycle.NewVolumeCatalogKVServicer(service.NewKVService(svcHost, shard.ShardKV))
				catalogTxSvc := service.NewTransactionService(svcHost, shard.ShardKV)
				catalog, catErr := volume.NewCatalog(catalogKVSvc, catalogTxSvc, volumeRegistry)
				if catErr != nil {
					_ = shutdownNode(nodeCloser)
					return catErr
				}
				barrierRegistry.Attach(seal.WithVolumeAllocator(catalog))
				if volumePoolAttached != nil {
					volumePoolAttached()
				}
				// Security review finding 7: a pool this node opened under
				// a different provider than the catalog recorded for it
				// (a misconfigured node, e.g. "dir" where the cluster
				// expects "luks2") must never silently serve that pool's
				// tenants under the wrong storage semantics. Only
				// available now that the catalog exists; refuses just the
				// mismatched pool (invariant 6), never the node's boot.
				for _, mismatchErr := range lifecycle.CheckProviderConsistency(sigCtx, catalog, openVolumes, volumePoolRows) {
					slog.Default().Error("volume pool: provider mismatch; pool will not be served on this node",
						slog.Any("error", mismatchErr))
				}
			}

			// Construct all services via the shared lifecycle package. Seal-gated
			// services (barrier, ceremony, credential, tenant, cert) are wired when
			// sealBarrier and barrierRegistry are non-nil. The barrier/credential/
			// registry instances reused here are the same ones constructed earlier
			// for the pre-host sealed-window listener; the listener was stopped
			// before initializeNode, so this BuildServices call is the single
			// owner of the barrier services for the rest of the daemon's lifetime.
			// Tenant isolation posture is the cluster-wide isolation-policy
			// floor (pkg/tenant/isolation_policy.go), set below once svcNode
			// exists; FIPS-140 always tightens it to strict.

			// Reuse the ceremony core service from the sealed-window listener when
			// one exists: officer registrations performed while sealed are
			// cache-only and distributed shares live in the service's in-memory
			// enclaves, so constructing a fresh instance here would orphan both.
			ceremonyCoreSvc := carriedCeremony
			if sealedWindowNode != nil && sealedWindowNode.Ceremony != nil {
				if coreSvc, ok := service.CeremonyServiceCore(sealedWindowNode.Ceremony); ok {
					ceremonyCoreSvc = coreSvc
				}
			}
			carriedCeremony = nil

			// A durable backup repository makes the Backup service's artifacts
			// survive a daemon restart -- required for disaster recovery, since
			// an in-memory-only backup dies with the very process a DR restore
			// needs to recover from. Opt-in via --backup-dir; the file-backed
			// storage engine takes its directory lock per mutation and releases
			// it before returning, so restore.go's own short-lived open/close
			// against the same directory (during a --restore-backup-id boot)
			// waits at most one mutation behind this one.
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
			// The at-rest report reads the seal posture and the standalone
			// backend (databases[0]), which the server config does not carry.
			capCfg.Seal = f.sealConfig
			capCfg.Databases = f.databases
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
			profile.WarnUnencryptedAtRest(slog.Default())
			capabilities.Seal(profile)

			// This node's permitted-method vocabulary: every user store below
			// validates written policies against it, and it is bound to the
			// node's own auth middleware once bootstrapAuthEnforcement builds
			// it. Until then, and with auth disabled, it is the built-in tokens.
			authMethods := &middleware.AuthMethodVocabulary{}

			// Cluster sealing: the services seal through clusterSeal from the
			// start, and it seals node-locally until the monitor is set below,
			// once the host, the services and the feature gate exist.
			clusterSeal := clusterseal.NewDeferred()

			// A node seal pauses every Raft shard the host runs before any
			// key goes, and the node barrier's unseal resumes them; a
			// standalone node has no host and pauses nothing. The tenant
			// registries are given to it once they exist, below.
			shardPauser := server.NewShardPauser(host, barrierRegistry, slog.Default())
			var sealPauser service.ShardPauser
			if shardPauser != nil {
				sealPauser = shardPauser
			}

			svcNode, err := lifecycle.BuildServices(sigCtx, lifecycle.Infra{
				ClusterSealer:        clusterSeal,
				ShardPauser:          sealPauser,
				AuthMethodVocabulary: authMethods.IsAuthMethod,
				Host:                 svcHost,
				// The raft system shard runs --sm; a type without MVCC revision
				// history (the default pebble) is declared so, rather than
				// discovered from a failed read.
				NoRevisionHistory:         host != nil && !lifecycle.StateMachineKeepsRevisionHistory(f.smType),
				Profile:                   &profile,
				WatchHub:                  watchHub,
				BackupSink:                backupSink,
				DataDir:                   f.dataDir,
				Backend:                   localBackend,
				DatabaseManager:           dbManager,
				KVOptions:                 nil,
				SealBarrier:               sealBarrier,
				SealEnabled:               f.sealConfig.Enabled,
				BarrierRegistry:           barrierRegistry,
				RaftAddrToNodeID:          raftAddrToNodeIDMap(f.peers),
				CredentialCoreSvc:         credCoreSvc,
				CeremonyDataDir:           filepath.Join(f.dataDir, "seal"),
				CeremonyCA:                ceremonyCA,
				ServerCertPEM:             bundle.CertPEM,
				ClientCACertPEM:           bundle.CACertPEM,
				CeremonyCoreSvc:           ceremonyCoreSvc,
				AuditLogger:               complianceAudit,
				AuditFailurePolicy:        auditPolicy,
				KeyDir:                    f.backupKeyDir,
				DEKKeyDir:                 f.dekKeyDir,
				KeyWrapRegistry:           keyWrapInjection.registry,
				KeyWrapProvider:           keyWrapInjection.provider,
				ClusterTrustDomain:        f.clusterTrustDomain,
				InvitationTTL:             f.invitationTTL,
				InvitationRequireKeyPin:   f.invitationRequireKeyPin,
				InvitationRedeemLimit:     f.invitationRedeemLimit,
				MTLSBindingPolicy:         f.mtlsBindingPolicy,
				PINBackend:                pinBackend,
				Logger:                    slog.Default(),
				EmbeddingConfig:           f.embeddingConfig,
				RAGConfig:                 f.ragConfig,
				ImportSession:             f.importSessionConfig,
				DefaultTenantQuotas:       f.tenantConfig.DefaultQuotas.ToQuotas(),
				DefaultTenantEntitlements: f.tenantConfig.DefaultEntitlements.ToEntitlements(),
				Encryptor:                 enc,
				// Key A: in-memory bootstrap material (populated only when
				// ca-key.pem still exists on disk this boot) for the built-in
				// cakey.SoftwareProvider to adopt into the replicated store on
				// first use. See confirmClusterCAPersisted below. Withheld
				// entirely for a BYO-TLS bundle (bundle.ExplicitTLS): its
				// CACertPEM is the operator's own trust anchor, never
				// material qrdb generated, so it must never be offered to
				// the cluster CA provider as something to adopt.
				ClusterCACertPEM: clusterCABootstrapCertPEM(bundle),
				ClusterCAKeyPEM:  clusterCABootstrapKeyPEM(bundle),
				// A BYO-TLS node has no cluster CA key of its own: leave
				// ClusterCAProv nil rather than let the built-in provider
				// silently generate and persist a self-issued one. See
				// lifecycle.Infra.NoClusterCAKey.
				NoClusterCAKey: bundle.ExplicitTLS,
			})
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			// FIPS-140 always tightens the cluster-wide isolation-policy
			// floor to strict (pkg/tenant/isolation_policy.go); an operator
			// may also raise it explicitly via the isolation-policy verb.
			// Best-effort: the floor only ever tightens, so a failure here
			// (e.g. a peer already moved it, or this node raced another to
			// the first write) leaves the floor exactly as strict or
			// stricter than this call asked for, never weaker.
			if fips140.Enabled() {
				if setter, ok := svcNode.Tenant.(interface {
					SetClusterIsolationPolicy(ctx context.Context, next tenant.ClusterIsolationPolicy) error
				}); ok {
					if err := setter.SetClusterIsolationPolicy(sigCtx, tenant.ClusterIsolationPolicyStrict); err != nil {
						slog.Warn("tenant isolation: could not confirm the cluster floor is strict under FIPS-140",
							slog.Any("error", err))
					}
				}
			}
			// The node's long-lived watchers (Key B cache eviction, undone
			// tenant acknowledgement), composed in one place shared with
			// sdk/go/node; see lifecycle.StartNodeWatchers. Unconditional --
			// neither has anything to do with whether this node's OWN
			// cluster CA (Key A) is self-issued or BYO-TLS, unlike the block
			// below. Bound to sigCtx, the process-lifetime context, since
			// the watchers are meant to run for the life of the node, not
			// only until this boot sequence finishes.
			if err := lifecycle.StartNodeWatchers(sigCtx, svcNode); err != nil {
				slog.Warn("node watchers failed to start", slog.Any("error", err))
			}
			if svcNode.ClusterCAProv != nil {
				// A leader must be known before this read-through-Raft check
				// can mean anything: right after BuildServices the system
				// shard may not have settled its own leader election yet,
				// even on a single-node shard -- the same race
				// waitForLeaderElection exists to close for
				// bootstrapUserRoleStores below.
				if err := waitForLeaderElection(sigCtx, rnode, leaderWaitBudget(f), shard.ShardKV); err != nil {
					_ = svcNode.Stop(context.Background())
					_ = shutdownNode(nodeCloser)
					return err
				}
				if err := confirmClusterCAPersisted(sigCtx, svcNode.ClusterCAProv, f.dataDir, len(bundle.CAKeyPEM) > 0); err != nil {
					_ = svcNode.Stop(context.Background())
					_ = shutdownNode(nodeCloser)
					return err
				}
			}
			// Member-side CSR join, composed exactly as an embedded clustered
			// node composes it: the node's own identity recorded as admitted
			// whenever it holds a cluster CA key, and, with
			// --cluster-join-enabled, the join listener and the join-token
			// verb bound to one store. A BYO-TLS node starts no listener.
			clusterJoin, err = lifecycle.StartClusterJoin(sigCtx, svcNode, lifecycle.ClusterJoinConfig{
				Enabled:     f.clusterJoinEnabled,
				ListenHost:  f.clusterJoinListenHost,
				ListenPort:  f.clusterJoinListenPort,
				AdminPort:   f.adminPort,
				TrustDomain: f.clusterTrustDomain,
				DataDir:     f.dataDir,
				NodeCertPEM: bundle.CertPEM,
				Logger:      slog.Default(),
			})
			if err != nil {
				_ = svcNode.Stop(context.Background())
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

			// Every admitted, unsealed member serves the trust anchor from
			// here on, not only the node that generated it: once that node
			// is gone for good, FetchAnchor/Join can still reach any
			// surviving member. The node that generated the CA already
			// advertises via bootServers (resolveTLS's first-node path);
			// every other member -- joined, or restarted from a cached,
			// non-generating data directory -- starts the identical
			// listener here, from the same locally-cached certificate
			// (bundle.CACertPEM) rather than a second copy of the cluster
			// CA.
			if bootServers == nil && servesAnchor(bundle) {
				memberAnchorAdv, err = startMemberAnchorAdvertiser(f, bundle.CACertPEM, slog.Default())
				if err != nil {
					_ = shutdownNode(nodeCloser)
					return err
				}
			}

			// Provision the configured databases now that BuildServices has
			// attached the keyspace reclaimer (see provisionDatabases). It is
			// idempotent, so the in-process migration recycle that re-runs
			// this body finds every database already present.
			if err := provisionDatabases(bootCtx, f, dbManager, rnode); err != nil {
				_ = shutdownNode(nodeCloser)
				// A shutdown signal during the leader wait is a clean
				// shutdown, as it is during the auth bootstrap below.
				if sigCtx.Err() != nil {
					slog.Info("database provisioning canceled by shutdown signal")
					return nil
				}
				return err
			}

			// Refuse to run under a cluster that has activated a feature this
			// binary lacks, then advertise this binary's features and start
			// the gate. Provisioning above has already waited for the system
			// shard's leader, which the activation read needs.
			//
			// gateCloser withdraws the advertisement. Every shutdown path closes
			// it explicitly while the host is still up (see withdrawFeatures):
			// the deferred close below runs after shutdownNode, against a
			// closed host, and is only the backstop for an error return.
			var gateCloser io.Closer = nopCloser{}
			if host != nil {
				gateCloser, err = startFeatureGate(bootCtx, featureGate, svcNode, nodeSeal)
				if err != nil {
					_ = shutdownNode(nodeCloser)
					var unsupported *featuregate.UnsupportedFeatureError
					if sigCtx.Err() != nil && !errors.As(err, &unsupported) {
						slog.Info("feature gate start canceled by shutdown signal")
						return nil
					}
					return err
				}
				defer func() { _ = gateCloser.Close() }()
			}

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
			// awaitingAdminCredentialProbe's pointer identity is fixed here,
			// before auth.mtls_binding_policy's own boot-time lookup (below,
			// inside bootstrapAuthEnforcement) has run: ceremonySvc's
			// composition needs it NOW, as CeremonyServiceWithAdminRebind's
			// AdminBoundNotifier (go-qrdb#560 review B1), so a reissue that
			// binds a certificate while this node is already serving can
			// tell the SAME probe to leave "awaiting" -- never by re-reading
			// the user store. Initialize (called inside
			// bootstrapAuthEnforcement, once the lookup resolves) decides
			// its actual state; until then, and whenever policy is not
			// "required", it reports not-awaiting, a harmless no-op target
			// for AdminBound.
			awaitingAdminCredentialProbe := firstboot.NewAwaitingAdminCredentialProbe()
			// Ceremony-initialize's admin-credential step (go-qrdb#548) must run
			// on the Raft leader: the durable pending-admin-cert record it writes
			// is itself a Raft write, which a follower could never actually
			// commit. CeremonyServiceWithLeaderGate refuses UP FRONT, before the
			// ceremony issues or signs anything, naming the current leader so the
			// operator's next `qrdb init` lands there; it wraps
			// CeremonyServiceWithAdminRebind, which is what turns a second
			// CeremonyInitialize call (after the first already succeeded) into the
			// admin-credential retry path rather than ceremony.ErrAlreadyInitialized.
			if _, ok := service.CeremonyServiceCore(ceremonySvc); ok {
				members, perr := parsePeers(f.peers)
				if perr != nil {
					// f.peers is only ever "id=address" pairs under static
					// discovery (resolveStartFlags/buildHost's own earlier
					// validation already refused an unparseable one THERE);
					// any other discovery method (multicast, DNS, token) never
					// populates it in that shape, so a parse failure here is
					// expected and this leader check degrades to an empty
					// membership map rather than aborting an otherwise-valid
					// boot over a field this path does not own.
					if f.discoveryMethod == "static" {
						return perr
					}
					slog.Debug("ceremony leader-gate: --peers not in id=address form under non-static discovery; membership map empty", slog.String("error", perr.Error()))
				}
				leader := service.HostLeaderChecker{
					Host: svcHost, ShardID: shard.ShardKV, ReplicaID: f.replicaID, Members: members,
				}
				ceremonySvc = service.ComposeCeremonyServiceWithAdminCredentialWiring(
					ceremonySvc, svcNode.User, firstboot.UserCertBinder{Users: svcNode.User}, leader, awaitingAdminCredentialProbe)
			}
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
			// The compliance audit trail, opened before the seal subsystem
			// (openComplianceAudit), is injected into all protocol servers so
			// the /v1/audit/events endpoint is available, and is handed to the
			// user/role stores BEFORE they exist so IAM mutations
			// (create/update/delete/enable/disable, role permission changes)
			// emit an audit trail.
			runtimeKVAdapter := newKVServiceAdapter(authKVSvc, nil, nil)
			_ = runtimeKVAdapter // used below when auth is enabled

			// Under auth.mtls_binding_policy "required" every user store
			// refuses any change that would lock the bootstrap admin out of
			// certificate login (lifecycle.UserStoreOptions), and first boot
			// itself requires a certificate to bind at create time
			// (go-qrdb#548). An unrecognised value aborts here, before any
			// store exists.
			var userOpts []user.Option
			// The zero value of auth.MTLSBindingPolicy is now MTLSBindingRequired
			// (go-qrdb#548), so leaving this unset is no longer harmless: with
			// auth disabled there is no certificate login to gate at all, and
			// first boot must not impose the "required" pre-flight (await a
			// certificate, refuse without one) on a composition that enforces no
			// binding either way. "enrolled" -- the posture's own no-op value --
			// is the correct, explicit choice for the auth-disabled case.
			mtlsBindingPolicy := auth.MTLSBindingEnrolled
			if f.authEnabled {
				policy, perr := auth.ParseMTLSBindingPolicy(f.mtlsBindingPolicy)
				if perr != nil {
					_ = shutdownNode(nodeCloser)
					return &AuthBootstrapError{Step: "auth.mtls_binding_policy", Err: perr}
				}
				mtlsBindingPolicy = policy
				opts, perr := lifecycle.UserStoreOptions(f.mtlsBindingPolicy, slog.Default())
				if perr != nil {
					_ = shutdownNode(nodeCloser)
					return &AuthBootstrapError{Step: "auth.mtls_binding_policy", Err: perr}
				}
				userOpts = opts
			}
			// Every user store validates written permitted-method policies
			// against THIS node's auth-chain vocabulary (built-ins until the
			// middleware is bound below, and with auth disabled).
			userOpts = append(userOpts, user.WithAuthMethodVocabulary(authMethods.IsAuthMethod))
			mgmtResult, err := bootstrapUserRoleStores(sigCtx, bootKVSvc, authKVSvc, txSvc, rnode, leaderWaitBudget(f), shard.ShardKV, f.replicaID, tenantSvcInst, strictSoD, bootBarriers, bootStrategy, complianceAudit, mtlsBindingPolicy, ceremonyCoreSvc, ceremonyCA, bundle.CACertPEM, userOpts...)
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
				authResult, err = bootstrapAuthEnforcement(sigCtx, mgmtResult, f.dataDir, rnode, complianceAudit, strictSoD, f.clusterTrustDomain, qrdbconfig.AuthABACConfig{Enabled: f.abacEnabled, PolicyDir: f.abacPolicyDir}, svcNode.CertProv, bundle.TrustPool(), svcNode.PendingAdmins, firstAdminPins(svcNode.BarrierInit), svcNode.CertTargets, f.revocationCacheTTL, f.auditFailurePolicy, f.mtlsBindingPolicy, awaitingAdminCredentialProbe, svcNode.Watch)
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
				// Waited on before auditLogger closes (LIFO: deferred after
				// it, so it runs first) -- the watch goroutine, when one was
				// started, logs through resources this shutdown path is
				// about to tear down. adminWatchCancel is deferred AFTER
				// Wait() (go-qrdb#591 re-review, item A), so it runs BEFORE
				// it: sigCtx only cancels on a shutdown signal, so a
				// NON-signal error return from a later step in runStart
				// would otherwise leave nothing to stop the goroutine and
				// Wait() would block forever. Calling adminWatchCancel on
				// a clean signal shutdown too is harmless -- sigCtx is
				// already cancelled by then, so this is a no-op race with
				// an already-stopping goroutine, never a double-cancel of
				// anything live.
				if authResult.adminWatchWG != nil {
					defer authResult.adminWatchWG.Wait()
					defer authResult.adminWatchCancel()
				}
				authMethods.Bind(authResult.authMW)
			} else {
				slog.Warn("authentication disabled: all endpoints accessible without credentials")
			}

			// The system shard serves only once it can answer a linearizable
			// read: its leader is known AND has committed an entry in its term.
			// Until then every ReadIndex is refused with
			// quicraft.ErrReadIndexNotReady, so a listener started earlier
			// would answer its first requests -- the auth middleware's user
			// store lookups first among them -- with that error. This is the
			// wait every host-open path shares (server.WaitForShardServing).
			if host != nil {
				if err := server.WaitForShardServing(sigCtx, host, shard.ShardKV); err != nil {
					_ = shutdownNode(nodeCloser)
					if sigCtx.Err() != nil {
						slog.Info("system shard readiness wait canceled by shutdown signal")
						return nil
					}
					return &SystemShardServingError{Err: err}
				}
			}

			// Publish this node's own volume-pool health on a lease, the SAME
			// mechanism cluster.Advertiser uses to publish a cluster.Member: a
			// dead node reports nothing (engine-level lease expiry), not a
			// reader-side staleness guess. Revoked on shutdown via defer. The
			// catalog/allocator itself was already built and attached above,
			// before lifecycle.BuildServices, so the TenantService it
			// constructed could read it; this needs only mvccSvc/leaseSvc,
			// which do genuinely require the shard to be serving. A
			// standalone node on an engine that keeps no leases (file,
			// objstore) has nothing to publish the report on.
			if barrierRegistry != nil && (!standalone || lifecycle.LocalEngineKeepsLeases(localBackend)) {
				if mvccSvc != nil && leaseSvc != nil {
					reporter, repErr := volume.NewReporter(mvccSvc, leaseSvc, nodeID, volume.WithReporterSealProbe(nodeSeal))
					if repErr != nil {
						slog.Default().Warn("start: failed to construct volume-pool Reporter",
							slog.Any("error", repErr))
					} else {
						if serr := reporter.Start(bootCtx, func() volume.NodeReport {
							return lifecycle.BuildNodeReport(bootCtx, nodeID, volumePoolRows, slog.Default())
						}); serr != nil {
							slog.Default().Warn("start: failed to start volume-pool Reporter",
								slog.Any("error", serr))
						} else {
							defer func() { _ = reporter.Close() }()
						}
					}
				}
			}

			// The cluster seal monitor is installed below, after the tenant
			// registries it seals through exist. A node that installs one
			// hands BuildTenantInfra the sealer, which then starts no tenant
			// runtime before the monitor has read the records.
			installsSealMonitor := host != nil && barrierRegistry != nil
			var tenantSealer clusterseal.Sealer
			if installsSealMonitor {
				tenantSealer = clusterSeal
			}

			// Build per-tenant isolation infrastructure (IsolationVolume host
			// registry, IsolationRaft factory, TenantShardReconciler). Any
			// registries opened by the composer are closed on error before it
			// returns, so only shutdownNode is needed here on failure.
			ti, err := server.BuildTenantInfra(server.TenantInfraConfig{
				Ctx:        sigCtx,
				BarrierReg: barrierRegistry,
				TenantSvc:  tenantSvcInst,
				// The daemon builds its tenant service once and never swaps it,
				// so the service itself is the use-time lookup.
				TenantLookup:         tenantSvcInst,
				VolumePoolConfigured: barrierRegistry != nil && barrierRegistry.HasVolumePool(),
				Logger:               slog.Default(),
				Host:                 host,
				Manager:              dbManager,
				ReplicaID:            f.replicaID,
				RaftAddr:             f.raftAddress,
				SystemTenant:         tenantid.System,
				PlacementStore:       svcNode.PlacementStore,
				MultiplexLogDB:       f.walMux,
				LeaderEvents:         f.leaderEvents,
				FeatureGate:          featureGate,
				ClusterSealer:        tenantSealer,
				ImportSession:        f.importSessionConfig,
				SnapshotEntries:      f.snapshotEntries,
				CompactionOverhead:   f.compactionOverhead,
				Lease:                f.leaseConfig,
			})
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			tenantHostReg := ti.Hosts
			raftBackendReg := ti.Raft
			// An isolated tenant's records live in its home store: in raft
			// mode a dedicated Raft group of its own, on a standalone node its
			// barrier's own backend at its data root. Until one is attached
			// they fail closed. The served IAM services and the auth middleware
			// resolve through mgmtResult's resolver, so it gets the same home
			// stores.
			homesAttached := false
			switch {
			case standalone && barrierRegistry != nil:
				homesAttached = lifecycle.AttachLocalTenantHomes(svcNode, barrierRegistry, tenantHostReg, mgmtResult.storeResolver)
			case raftBackendReg != nil:
				homesAttached = lifecycle.AttachTenantHomes(svcNode, raftBackendReg, mgmtResult.storeResolver)
			}
			if !homesAttached && barrierRegistry != nil {
				slog.Warn("isolated tenants' home stores are not available on this node; their requests fail closed")
			}
			shardPauser.SetTenants(raftBackendReg, tenantHostReg)
			// Security review item 4(a): publish the just-built registry to
			// sealSub.VolumeLifecycle's close-before-lock closure, which has
			// been reading this holder (empty until now) since the seal
			// subsystem was constructed, long before tenantHostReg existed.
			if sealSub != nil && sealSub.TenantHostsHolder != nil {
				sealSub.TenantHostsHolder.Store(tenantHostReg)
			}

			// The cluster seal monitor reads the replicated seal records and
			// seals this node when they say so; until its first read, and
			// whenever it cannot read them for the lease, the node refuses
			// requests. It is active only once every node runs a release with
			// cluster sealing (featuregate.ClusterSeal); a raft node with no
			// transport has no gate and seals node-locally.
			if installsSealMonitor {
				timing, terr := f.sealConfig.ClusterSealTiming()
				if terr != nil {
					_ = shutdownNode(nodeCloser)
					return terr
				}
				monitor, merr := server.NewClusterSealMonitor(server.ClusterSealParts{
					KV:          kvSvc,
					Gate:        featureGate,
					Barriers:    barrierRegistry,
					Shards:      raftBackendReg,
					Hosts:       tenantHostReg,
					Pauser:      shardPauser,
					Databases:   svcNode.Database,
					Membership:  host,
					SystemShard: shard.ShardKV,
					ReplicaID:   f.replicaID,
					RaftAddress: f.raftAddress,
					Timing:      timing,
					Streams:     clusterSeal.Streams(),
					Logger:      slog.Default(),
				})
				if merr != nil {
					_ = shutdownNode(nodeCloser)
					return merr
				}
				clusterSeal.Set(monitor)
				go monitor.Run(bootCtx)
			}
			// The reconciler starts tenant shards, holding each whose record
			// is sealed. It asks through the sealer, which refuses nothing
			// until the monitor is Set, so it starts only now -- and the
			// monitor refuses every barrier until its first read.
			if ti.Reconciler != nil {
				// A node placement assigns to a tenant's group registers the
				// tenant's barrier shell, so status, init and unseal reach it.
				if shells, ok := svcNode.Tenant.(server.ShellRegistrar); ok {
					ti.Reconciler.SetShellRegistrar(shells)
				}
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

			// The peer admin registry resolves a peer's admin URL from its Raft
			// address: REST's leader forwarding proxies writes on a non-leader
			// to the current leader through it, and the cluster key plane
			// delivers tenant keys through it. Configured peer admin URLs
			// cover nodes sharing a host on distinct admin ports.
			registry := lifecycle.NewPeerAdminRegistry(lifecycle.PeerAdminConfig{
				RaftAddr:      f.raftAddress,
				AdvertiseHost: f.advertiseHost,
				AdminPort:     f.adminPort,
				Peers:         f.peerAdminURLs,
				Logger:        slog.Default(),
			})

			// The multi-node half of raft tenant isolation -- the cluster key
			// plane, the key distributor, the rebalance dependencies and the
			// replica host -- composed exactly as an embedded clustered node
			// composes it. The key plane reuses the auth middleware's
			// revocation checker, so auth.revocation_cache_ttl is one window
			// for this node rather than one per plane; nil with auth off,
			// which leaves the key plane as unenforced as every other plane.
			var ckRevocation auth.RevocationChecker
			if authResult != nil {
				ckRevocation = authResult.authMW.RevocationChecker()
			}
			// The node's software key from its TLS bundle, through the one
			// derivation an embedded clustered node's identity goes through
			// too; a pair that does not parse leaves Identity nil, and the
			// key plane and the platform-init signer off with a warning, as
			// every other missing piece does.
			nodeIdentity, err := lifecycle.NodeIdentity(&quicraftconfig.MTLSConfig{
				CACert: bundle.CACertPEM,
				Cert:   bundle.CertPEM,
				Key:    bundle.KeyPEM,
			})
			if err != nil {
				slog.Warn("clusterkeys: no usable node identity", slog.Any("error", err))
			}
			raftTenants := lifecycle.ComposeRaftTenantCluster(lifecycle.RaftTenantClusterConfig{
				Services:      svcNode,
				Host:          host,
				FeatureGate:   featureGate,
				Barriers:      barrierRegistry,
				DedicatedRaft: raftBackendReg != nil,
				RaftAddr:      f.raftAddress,
				PeerAdmin:     registry,
				TrustDomain:   f.clusterTrustDomain,
				CACertPool:    bundle.CACertPool,
				Identity:      nodeIdentity,
				Revocation:    ckRevocation,
				Logger:        slog.Default(),
			})

			// Phase D: cluster-wide enable-encryption coordination. Wired only when
			// the replicated coordination store is available (a KV-backed cluster).
			// When present, the transports write a replicated migration intent via
			// clusterMig and every node's reconciler performs its local recycle with
			// its own credential -- the request's, held in memory on the node that
			// accepted it, or the node's own file under
			// seal.migration_credentials_dir. No credential is in the intent.
			// Otherwise the direct system migrator is used unchanged.
			var clusterMig *clusterCoordinatorMigrator
			if host != nil && svcNode.ClusterMigStore != nil && barrierRegistry != nil {
				localMig := server.NewScopedEncryptionMigrator(
					sysMigrator, tenantHostReg, raftBackendReg, barrierRegistry, tenantSvcInst)
				topology := service.NewHostTopologyProvider(raftbackend.New(host), svcNode.Rebalance)
				ownerResolver := server.NewClusterOwnerResolver(
					svcNode.PlacementStore, topology, shard.ShardKV, f.raftAddress)
				localReplicaID := f.replicaID
				isLeader := func() bool {
					id, ok, lerr := host.LeaderID(shard.ShardKV)
					return lerr == nil && ok && id == localReplicaID
				}
				// A rolling step hands on only once its owner's Raft log has
				// caught up to the commit index the leader saw at its ack.
				coordOpts := []clustermig.CoordinatorOption{
					clustermig.WithCatchUp(server.NewReplicationProgress(host)),
					clustermig.WithLogger(slog.Default()),
				}
				if featureGate != nil {
					coordOpts = append(coordOpts, clustermig.WithRollingSteps(func() bool {
						return featureGate.Activated(featuregate.RollingMigration)
					}, f.raftAddress))
				}
				coord := clustermig.NewCoordinator(svcNode.ClusterMigStore, ownerResolver,
					isLeader, func() int64 { return time.Now().Unix() }, coordOpts...)
				migCredentials := clustermig.NewLocalCredentials(f.sealConfig.MigrationCredentialsDir)
				clusterMig = &clusterCoordinatorMigrator{
					coord:       coord,
					credentials: migCredentials,
					self:        f.raftAddress,
					barrierReg:  barrierRegistry,
					modeCheck:   tenantSvcInst,
					leader:      host,
					leaderShard: shard.ShardKV,
				}
				reconciler := clustermig.NewReconciler(svcNode.ClusterMigStore, f.raftAddress,
					&daemonLocalRecycler{migrator: localMig, barrierReg: barrierRegistry},
					migCredentials,
					func() int64 { return time.Now().Unix() }, slog.Default())
				go runClusterMigReconciler(bootCtx, reconciler, coord, svcNode.ClusterMigStore)
				slog.Info("cluster enable-encryption coordination enabled",
					slog.String("node", f.raftAddress))
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
			// SetClientAuthMode, never a direct mutation of
			// bundle.TLSConfig.ClientAuth/.ClientCAs: by this point the
			// pre-host sealed-window listener may have only just stopped
			// (its own graceful Stop is bounded by a fixed timeout and can
			// return before every in-flight handshake against it has
			// actually finished reading those fields), so an unsynchronized
			// write here would race an unsynchronized read there
			// (go-qrdb#591's review, item 3).
			if !f.tlsClientAuthSet {
				if f.authEnabled {
					bundle.SetClientAuthMode(tls.VerifyClientCertIfGiven, bundle.ClientCACertPool)
				} else {
					bundle.SetClientAuthMode(tls.NoClientCert, bundle.TLSConfig.ClientCAs)
				}
			}

			// Create and start the multi-protocol server.
			serverOpts := append(bundle.serverTLSOptions(),
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
			)
			// The data-plane families -- transactions, MVCC, lease, lock and
			// watch -- are served in BOTH modes, over the same engine, lessor
			// and watch hub (go-qrdb#62 for transactions, go-qrdb#582 for the
			// rest): a standalone node applies the commands in process
			// instead of through the log, so the semantics are the raft
			// state machine's by construction. What stays raft-only is
			// cluster-shaped -- the host, the collection sweep leader,
			// backup, rebalance -- and the etcd listener
			// (server.WithStandaloneMode).
			serverOpts = append(serverOpts,
				server.WithTransaction(txSvc),
				server.WithMVCC(mvccSvc),
				server.WithLease(leaseSvc),
				server.WithLockService(lockSvc),
				server.WithWatch(watchSvc),
			)
			if standalone {
				serverOpts = append(serverOpts, server.WithStandaloneMode())
			} else {
				serverOpts = append(serverOpts, server.WithHost(host))
				fwdOpt, fwdErr := replicaForwardingOption(bootCtx, f, standalone, placementChanges(svcNode), kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc)
				if fwdErr != nil {
					_ = shutdownNode(nodeCloser)
					return fwdErr
				}
				if fwdOpt != nil {
					serverOpts = append(serverOpts, fwdOpt)
				}
				// The collection sweeper finishes interrupted creates and
				// drops in the shared system store from the system shard's
				// leader only, so a cluster sweeps each database once per pass
				// rather than once per replica.
				if host != nil {
					sweepReplicaID := f.replicaID
					serverOpts = append(serverOpts, server.WithCollectionSweepLeader(func() bool {
						id, ok, lerr := host.LeaderID(shard.ShardKV)
						return lerr == nil && ok && id == sweepReplicaID
					}))
				}
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
			// The cluster-wide, per-tenant-opt-in replicated audit chain
			// (docs/audit/architecture.md#cluster-wide-replicated-trail) is
			// always wired alongside it: BuildServices always populates
			// ReplicatedAudit, and it degrades to "not entitled" for every
			// tenant until a per-tenant grant (or the platform default) turns
			// it on.
			serverOpts = append(serverOpts, server.WithReplicatedAuditLogger(svcNode.ReplicatedAudit))

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
				sigCtx, mgmtResult.storeResolver, mgmtResult.legacyFactorStore, complianceAudit, slog.Default(),
			); err == nil {
				serverOpts = append(serverOpts, opt)
			} else {
				slog.Error("user auth-factor service (M14) disabled", slog.Any("error", err))
			}

			if authResult != nil {
				serverOpts = append(serverOpts,
					server.WithAuthMiddleware(authResult.authMW),
				)
				if authResult.soAuthenticator != nil {
					serverOpts = append(serverOpts, server.WithSOAuthenticator(authResult.soAuthenticator))
				}
				// go-qrdb#548 review item 3: the probe's pointer identity is
				// always non-nil (go-qrdb#560 review B1 -- ceremony
				// composition, above, needs it before this policy is even
				// known), but it reports awaiting only under
				// auth.mtls_binding_policy "required"; installing it
				// unconditionally is a harmless no-op gate otherwise. See
				// bootstrapAuthEnforcement.
				if authResult.awaitingAdminCredentialProbe != nil {
					serverOpts = append(serverOpts, server.WithAwaitingAdminCredentialProbe(authResult.awaitingAdminCredentialProbe))
				}
			}
			if barrierSvc != nil {
				serverOpts = append(serverOpts, server.WithBarrierService(barrierSvc))
			}
			if pinBackend != nil {
				serverOpts = append(serverOpts, server.WithPINBackend(pinBackend))
			}
			if ceremonySvc != nil {
				serverOpts = append(serverOpts, server.WithCeremonyService(ceremonySvc))
			}
			if ceremonyCA != nil {
				serverOpts = append(serverOpts, server.WithCeremonyCA(ceremonyCA))
			}
			// bundle.SetClusterCA wires the dynamic client-auth trust pool's
			// cluster-CA exemption (wireClientTrustPool, installed before any
			// listener started): resolved on every rebuild of the pool's
			// snapshot to add the cluster CA to the widened pool, and fresh
			// per request to grant it on the
			// issuer-tenant binding (bundle.TrustPool, wired above as the
			// auth.IssuerVerifier bootstrapAuthEnforcement installed) and to
			// authenticateOnBehalf's cluster-peer check. Key B is trusted via
			// the per-tenant binding regardless of whether this ever resolves
			// to anything; what it adds is the cluster CA's own membership
			// and exemption (go-qrdb#560 review, B2-2).
			//
			// lifecycle.TrustPoolClusterCA is DELIBERATELY independent of
			// ceremonyCA's own nil-ness above (go-qrdb#591's review, B4):
			// ceremonyCA is resolved once, from the TLS bundle's own
			// CAKeyPEM/CACertPEM, BEFORE BuildServices runs, and is nil on
			// every boot after the first successful one -- cmd/qrdbd deletes
			// ca-key.pem from disk once Key A is confirmed durable in the
			// replicated store (see confirmClusterCAPersisted). Gating
			// SetClusterCA on ceremonyCA made every restart of a self-issued
			// cluster refuse every forwarded on-behalf write, because
			// IsClusterCA had nothing to compare against --
			// svcNode.ClusterCAProv, the replicated-store-backed
			// cakey.Provider BuildServices wires regardless of ceremonyCA, is
			// the correct, always-live source once a node has ever completed
			// first boot.
			if trustPoolClusterCA, caErr := lifecycle.TrustPoolClusterCA(svcNode.ClusterCAProv, bundle.ExplicitTLS, bundle.CACertPEM); caErr != nil {
				slog.Error("the dynamic trust pool's cluster-CA exemption and on-behalf forwarding will not work", slog.Any("error", caErr))
			} else if trustPoolClusterCA != nil {
				bundle.SetClusterCA(trustPoolClusterCA)
			}
			if svcNode.CertProv != nil {
				serverOpts = append(serverOpts, server.WithCertProvider(svcNode.CertProv))
				// From here on, the dynamic client-auth trust pool
				// (wireClientTrustPool, installed before any listener
				// started) trusts every tenant's Key B CA certificate in
				// addition to Key A.
				bundle.SetKeyBProvider(svcNode.CertProv)
				// ... and every tenant's registered trust anchors, each bound
				// to its own tenant by the same pool's issuer check.
				svcNode.WireTrustAnchors(bundle.TrustPool())
			}
			if svcNode.CertEnrollment != nil {
				serverOpts = append(serverOpts, server.WithCertEnrollment(svcNode.CertEnrollment))
			}
			if svcNode.TenantOnboarding != nil {
				serverOpts = append(serverOpts, server.WithTenantOnboarding(svcNode.TenantOnboarding))
			}
			if svcNode.Invitations != nil {
				serverOpts = append(serverOpts, server.WithInvitations(svcNode.Invitations))
			}
			if svcNode.Credential != nil {
				serverOpts = append(serverOpts, server.WithCredentialService(svcNode.Credential))
			}
			if svcNode.DEK != nil {
				serverOpts = append(serverOpts, server.WithDEKService(svcNode.DEK))
			}
			if svcNode.Log != nil {
				serverOpts = append(serverOpts, server.WithLogService(svcNode.Log))
			}
			if svcNode.Policy != nil {
				serverOpts = append(serverOpts, server.WithPolicyService(svcNode.Policy))
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
			// Cluster seals: the regular listener's gate also closes while
			// this node is held, cut off or not yet confirmed; sealed tenants
			// and databases are refused; open streams end with their seal.
			if host != nil && barrierRegistry != nil {
				serverOpts = append(serverOpts, server.WithClusterSeal(clusterSeal))
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
			serverOpts = append(serverOpts, raftTenants.ServerOptions()...)
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
			// Registered on servesAnchor, not on the presence of a Noise
			// server: a non-nil Advertiser means this node advertises
			// enrollment, not that a Noise listener is running
			// (clusterenroll.Advertiser) -- a FIPS 140-3 node gets a
			// non-nil, Noise-disabled Advertiser and still needs this
			// route, the approved half of the same surface. servesAnchor
			// is also what makes the route universal rather than
			// first-node-only: every admitted, unsealed member holding the
			// cluster CA (bootServers' first-node listener or
			// memberAnchorAdv's, started above for every other member)
			// registers it identically.
			if servesAnchor(bundle) {
				serverOpts = append(serverOpts, server.WithCABootstrap(bundle.CACertPEM))
			}
			// The SPKI/Direct HTTPS join route is the plain-HTTPS twin of the
			// Noise_NK joinServer started above: both are gated on
			// cluster-join-enabled and share the same cakey.Provider and
			// admission token, so a joining node may use either transport.
			// Registered only once joinServer has started, which happens
			// only after confirmClusterCAPersisted -- Key A is unseal-only,
			// so this route is deliberately never a member of the
			// sealed-window allowlist.
			serverOpts = append(serverOpts, clusterJoin.ServerOptions()...)
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
			dbRegistry := server.NewDatabaseServiceRegistry(lifecycle.SystemDatabase)
			dbRegistry.Register(lifecycle.SystemDatabase, svcNode.DatabaseServices(lifecycle.SystemDatabase, shard.ShardKV))
			serverOpts = append(serverOpts, server.WithDatabaseServiceRegistry(dbRegistry))

			if tenantSvcInst != nil {
				applyIsolationFloor(bootCtx, tenantSvcInst, f.tenantConfig.IsolationPolicy)
			}

			// The sockets the sealed-window listener served on, if it ran;
			// bound here otherwise.
			handover, err := sockets.options(serverCfg)
			if err != nil {
				_ = shutdownNode(nodeCloser)
				return err
			}
			serverOpts = append(serverOpts, handover...)

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
			// A standalone daemon is the only replica, so it always sweeps
			// (lifecycle.StartLocalLeaseExpiry). stopLeaseExpiry waits for the
			// sweeper to exit; the shutdown path calls it before closing the
			// backend the sweeper revokes through, and the defer covers every
			// other exit (it is idempotent).
			stopLeaseExpiry := func() {}
			if standalone {
				stopLeaseExpiry = lifecycle.StartLocalLeaseExpiry(bootCtx, lr, leaseSvc, slog.Default())
				defer stopLeaseExpiry()
			} else if leaseSvc != nil && host != nil {
				expiryOpts, err := service.LeaseExpiryOptions(svcHost, shard.ShardKV, f.leaseConfig)
				if err != nil {
					_ = shutdownNode(nodeCloser)
					return err
				}
				go lr.RunExpiry(bootCtx, lease.DefaultExpiryInterval,
					func() bool {
						leaderID, ok, lerr := host.LeaderID(shard.ShardKV)
						return lerr == nil && ok && leaderID == f.replicaID
					},
					leaseSvc.Revoke,
					slog.Default(),
					append(expiryOpts, lease.WithExpiryHold(sealedHold(nodeSeal)))...,
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
				if rec := buildQuotaReconciler(quotaAccountant, tenantSvcInst, f.tenantConfig, barrierRegistry); rec != nil {
					go rec.Run(bootCtx)
					slog.Info("storage-quota reconciler started")
				}
			}

			// Every raft-mode node publishes its lease-backed member record -- its
			// Raft address, the API endpoints it serves, and its liveness -- so a
			// peer can reach it by replica id (replica forwarding resolves its
			// targets from these records). Opt-in dynamic Raft membership does
			// that AND, on the leader, reconciles the Raft group as nodes join and
			// leave; without it, the advertiser publishes the record alone.
			var memberRecord memberRecordCloser
			if f.dynamicMembership && standalone {
				slog.Warn("dynamic membership ignored: requires mode: raft")
			}
			switch {
			case f.dynamicMembership && !standalone:
				membershipMgr, mErr := startDynamicMembership(bootCtx, f, kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc, nodeSeal)
				if mErr != nil {
					srv.Stop()
					_ = shutdownNode(nodeCloser)
					return mErr
				}
				memberRecord = membershipMgr
				slog.Info("dynamic membership manager started",
					"shard_id", f.shardID, "node_id", f.replicaID)
				if dynamicMembershipStarted != nil {
					dynamicMembershipStarted()
				}
			case !standalone:
				publisher, aErr := startNodeAdvertiser(bootCtx, f, kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc, nodeSeal)
				if aErr != nil {
					srv.Stop()
					_ = shutdownNode(nodeCloser)
					return aErr
				}
				memberRecord = publisher
			}

			slog.Info("qrdbd node started, waiting for signals")

			// Block until a shutdown signal or an in-process system encryption migration.
			select {
			case <-sigCtx.Done():
				slog.Info("received signal, shutting down")

				if memberRecord != nil {
					if err := memberRecord.Close(); err != nil {
						slog.Error("member record close failed", slog.Any("error", err))
					}
				}
				withdrawFeatures(gateCloser)

				// Clean up bootstrap servers and cert reloader.
				if bootServers != nil {
					bootServers.Stop()
				}
				memberAnchorAdv.Stop()
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
				if perTenantReg != nil {
					closed := perTenantReg.List()
					if err := perTenantReg.CloseAll(context.Background()); err != nil {
						slog.Error("per-tenant pebble close-all failed",
							slog.Any("error", err),
							slog.Int("count", len(closed)))
					} else {
						slog.Info("closed per-tenant pebbles on shutdown",
							slog.Int("count", len(closed)))
					}
				}

				srv.Stop()
				stopLeaseExpiry()
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
				// operator unseal with the newly-set credential). The ceremony service
				// is carried into the next iteration: a Shamir target's envelopes
				// live in it and must survive the restart.
				if svcNode != nil && svcNode.Ceremony != nil {
					if core, ok := service.CeremonyServiceCore(svcNode.Ceremony); ok {
						carriedCeremony = core
					}
				}
				withdrawFeatures(gateCloser)
				// The next iteration starts its own anchor listener on the same
				// Noise port, and memberAnchorAdv's deferred Stop only runs when
				// runStart itself returns, so release the port here.
				memberAnchorAdv.Stop()
				reboot, mErr := handleSystemMigration(req, f, srv, svcNode, host, raftBackendReg, tenantHostReg, memberRecord, bootCancel, sealSub)
				// handleSystemMigration stopped svcNode to release its store locks
				// for re-encryption; nil it so the deferred Stop above is skipped.
				svcNode = nil
				rebootForMigration = reboot
				if reboot && perTenantReg != nil {
					// handleSystemMigration closed the tenant Hosts; now release the
					// per-tenant volume-pool Pebble locks too, so the next boot
					// iteration does not fail to reattach volume tenants on their
					// still-locked per-tenant Pebbles.
					if cerr := perTenantReg.CloseAll(context.Background()); cerr != nil {
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
// reconciler cannot be built. barrierReg resolves each volume/raft tenant's
// local DataRoot through the volume-pool catalog (tenant.TenantInfo itself
// carries only the pool NAME, not a resolved path); a tenant this node
// cannot currently resolve (no local Assignment, or the pool unavailable
// on this node) is reported with an empty DataRoot, which the reconciler's
// own "!t.Volume || t.DataRoot == \"\"" guard already skips rather than
// erroring the whole pass. barrierReg may be nil (seal disabled), in which
// case every tenant resolves to an empty DataRoot.
func buildQuotaReconciler(acct *quota.Accountant, tenantSvc tenant.TenantServicer, tcfg qrdbconfig.TenantConfig, barrierReg *seal.BarrierRegistry) *quota.Reconciler {
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
				// A refused tenant's data is never opened, so never walked.
				if ti.ID == tenantid.System || ti.Refusal != nil {
					continue
				}
				isVolume := ti.EffectiveIsolationType() == tenant.IsolationVolume
				var dataRoot string
				if isVolume && barrierReg != nil {
					if root, rerr := barrierReg.LocalTenantDataRoot(ctx, ti.ID); rerr == nil {
						dataRoot = root
					}
				}
				out = append(out, quota.StorageTenant{
					ID:       ti.ID,
					DataRoot: dataRoot,
					Volume:   isVolume,
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
			Host:    f.host,
			GRPCWeb: f.grpcWeb,
		},
		CORS: f.cors,
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
func initializeNode(ctx context.Context, f *startFlags, bundle *TLSBundle, complianceAudit audit.AuditLogger, auditPolicy audit.FailurePolicy) (*quicraft.Host, error) {
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
	memberIdentities, err := resolveMemberIdentities(f, bundle)
	if err != nil {
		return nil, err
	}

	// Build host configuration.
	hostCfg := buildHostConfig(f, mtlsCfg)
	f.leaderEvents = server.NewLeaderEvents()
	hostCfg.EventListener = f.leaderEvents.Listener()

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

	// The Raft WAL's at-rest posture -- WithoutEncryption with seal disabled,
	// and otherwise the multiplex (shared system WAL plus per-tenant dedicated
	// WALs) on a single node or cluster, whether the barrier encrypts or is
	// still in plaintext mode -- is decided by database.NewRaftWAL, the one
	// path qrdbd and the SDK node share. f.walSystemBarrier is set in runStart
	// only after the barrier is unsealed, and is nil when seal is disabled.
	// qrdbd has always written the format it writes now, so an unmarked WAL
	// from an earlier build is adopted as that format.
	walFormat := database.WALFormatPlaintext
	if f.walSystemBarrier != nil {
		walFormat = database.WALFormatBarrier
	}
	wal, err := database.NewRaftWAL(database.RaftWALConfig{
		Dir:            hostCfg.WALDir,
		SystemBarrier:  f.walSystemBarrier,
		UnmarkedFormat: walFormat,
	})
	if err != nil {
		return nil, err
	}
	f.walMux = wal.Multiplex
	hostOpts = append(hostOpts, wal.Options...)
	slog.Info("raft WAL configured",
		slog.String("format", string(wal.Format)),
		slog.Bool("multiplex", wal.Multiplex != nil),
		slog.Bool("barrier_plaintext_mode", f.walSystemBarrier != nil && f.walSystemBarrier.IsPlaintextMode()))

	// Create the host.
	host, err := quicraft.NewHost(hostCfg, hostOpts...)
	if err != nil {
		_ = wal.Close()
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
		if err := restoreShardFromBackup(ctx, f, host, createFn, shardCfg, complianceAudit, auditPolicy); err != nil {
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

// advertisedAPIEndpoints builds the client-facing API endpoint map this node
// publishes into the cluster registry. Every value comes from this node's own
// configuration: the advertise host (explicit, else the host portion of the
// Raft address peers already use to reach us) plus the ports of the protocols
// actually enabled. A disabled protocol is omitted, and a wildcard bind host
// with no explicit advertise host publishes nothing rather than an address no
// peer can dial.
func advertisedAPIEndpoints(f *startFlags) (map[cluster.APIProtocol]string, error) {
	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{
			AdvertiseHost: f.advertiseHost,
			AdminPort:     f.adminPort,
			GRPCPort:      f.grpcPort,
			QUICPort:      f.quicPort,
		},
		Protocols: qrdbconfig.ProtocolsConfig{
			REST: f.enableREST,
			GRPC: f.enableGRPC,
			QUIC: f.enableQUIC,
			MCP:  f.enableMCP,
		},
		MCP:  qrdbconfig.MCPConfig{Port: f.mcpPort},
		Raft: qrdbconfig.RaftConfig{RaftAddress: f.raftAddress},
	}
	return cluster.AdvertisedAPIEndpoints(cfg)
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
	nodeSeal cluster.SealProbe,
) (*cluster.MembershipManager, error) {
	client, err := newEmbeddedClient(kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc)
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
		// Features: a node that registers here before it is a member is admitted
		// only if it advertises what the cluster's groups use (see
		// featuregate.Gate.AdmitReplica).
		cluster.Member{NodeID: f.replicaID, Address: f.raftAddress, APIEndpoints: endpoints, Features: featuregate.SupportedNames()},
		f.shardID,
		cluster.MembershipConfig{MinQuorum: cluster.QuorumSize(minPeers)},
		cluster.WithMembershipSealProbe(nodeSeal),
	)
	if err != nil {
		return nil, err
	}
	// Each attempt is bounded: a settling leader drops a registration without
	// answering it, and the next attempt re-registers the same record. The
	// manager's loops outlive the attempt; Close ends them.
	if err := server.WhileSystemShardSettling(ctx, "dynamic membership start", mgr.Start); err != nil {
		return nil, err
	}
	return mgr, nil
}

// memberRecordCloser is what keeps this node's member record published: the
// dynamic membership manager, or the node advertiser when dynamic membership
// is off. Closing it deregisters the record.
type memberRecordCloser interface {
	Close() error
}

// memberRecordRetryInterval paces the member record's publication while the
// system shard cannot yet accept it.
const memberRecordRetryInterval = 500 * time.Millisecond

// memberRecordPublisher publishes this node's lease-backed member record in
// the background and withdraws it on Close.
type memberRecordPublisher struct {
	cancel context.CancelFunc
	done   chan struct{}
	adv    atomic.Pointer[cluster.Advertiser]
}

// Close stops publishing, waits for the publisher to return, and withdraws
// the record if it was published.
func (p *memberRecordPublisher) Close() error {
	p.cancel()
	<-p.done
	if adv := p.adv.Swap(nil); adv != nil {
		return adv.Close()
	}
	return nil
}

// startNodeAdvertiser publishes this node's lease-backed member record --
// its Raft address, the API endpoints it serves and the features it supports
// -- and keeps the lease alive, without the dynamic-membership
// reconfiguration loop. It runs on every raft-mode node that does not run
// dynamic membership (which publishes the same record itself), so every
// member is reachable by replica id.
//
// Publishing is a replicated write, so it runs in the background, retrying
// until it lands: boot never waits on a quorum, and a shutdown during boot
// stops it rather than failing. Only a configuration it cannot publish --
// no embedded client, endpoints that do not resolve -- fails here.
func startNodeAdvertiser(
	ctx context.Context,
	f *startFlags,
	kvSvc service.KVServicer,
	adminSvc service.AdminServicer,
	mvccSvc service.MVCCKVServicer,
	leaseSvc service.LeaseServicer,
	watchSvc service.WatchServicer,
	nodeSeal cluster.SealProbe,
) (*memberRecordPublisher, error) {
	client, err := newEmbeddedClient(kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc)
	if err != nil {
		return nil, err
	}
	endpoints, err := advertisedAPIEndpoints(f)
	if err != nil {
		return nil, err
	}
	self := cluster.Member{NodeID: f.replicaID, Address: f.raftAddress, APIEndpoints: endpoints, Features: featuregate.SupportedNames()}
	pctx, cancel := context.WithCancel(ctx)
	p := &memberRecordPublisher{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		start, lastReport := time.Now(), time.Now()
		for attempts := 1; pctx.Err() == nil; attempts++ {
			adv, aerr := client.NewNodeAdvertiser(self, cluster.AdvertiseConfig{}, cluster.WithAdvertiseSealProbe(nodeSeal))
			if aerr != nil {
				slog.Error("member record: advertiser not built", slog.Any("error", aerr))
				return
			}
			// Each registration is bounded: a settling leader drops it
			// without answering. The refresh loop outlives the attempt.
			actx, acancel := context.WithTimeout(pctx, server.StartupReadAttemptTimeout)
			serr := adv.Register(actx)
			acancel()
			if serr == nil {
				serr = adv.KeepAlive(pctx)
			}
			if serr == nil {
				p.adv.Store(adv)
				slog.Info("member record published", "node_id", f.replicaID)
				return
			}
			_ = adv.Close()
			if now := time.Now(); now.Sub(lastReport) >= server.StartupWaitReportInterval {
				lastReport = now
				slog.Warn("member record still waiting to publish", "node_id", f.replicaID,
					"attempts", attempts, "elapsed", now.Sub(start), slog.Any("error", serr))
			}
			select {
			case <-pctx.Done():
			case <-time.After(memberRecordRetryInterval):
			}
		}
	}()
	return p, nil
}

// sealedHold adapts the node barrier's probe to a lease-expiry hold: the sweep
// proposes no revoke while the barrier is sealed. A nil probe never holds.
func sealedHold(p cluster.SealProbe) func() bool {
	if p == nil {
		return nil
	}
	return p.IsSealed
}

// newEmbeddedClient builds an in-process (embedded) qrdb SDK client over the
// live service layer: one lessor and one watch subsystem behind the data it
// serves and the node registries (membership, feature advertisements) it
// keeps.
func newEmbeddedClient(
	kvSvc service.KVServicer,
	adminSvc service.AdminServicer,
	mvccSvc service.MVCCKVServicer,
	leaseSvc service.LeaseServicer,
	watchSvc service.WatchServicer,
) (*sdkgo.Client, error) {
	tr, err := embedded.New(kvSvc, adminSvc,
		embedded.WithMVCCService(mvccSvc),
		embedded.WithLeaseService(leaseSvc),
		embedded.WithWatchService(watchSvc),
	)
	if err != nil {
		return nil, err
	}
	return sdkgo.NewClient(tr)
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
// here with the lessor, the replicated audit chain, watch hub and
// time-series storage, and with store.WithEncryptor, which wraps every
// Pebble file the state machine creates (sstables, WAL, manifest, snapshots)
// in the barrier-driven encfs. Every other type resolves from the registry
// over the data dir and --sm-storage, carrying the same encryptor: pebble and
// file storage are opened on that encfs too, and memory storage persists
// nothing.
//
// Every state machine the returned function creates gets a fresh vlog.Chain
// of its own: Chain.Restore replaces the whole head table with what one
// state machine's storage holds, so a chain shared by two Raft groups on this
// node would number one group's events after the other's and let either
// group's open or recovery overwrite the other's heads. The chain is attached
// unconditionally, exactly like lr -- see the lessor's construction site's
// comment for why this attachment must never be conditioned on per-node
// configuration.
//
// Every registry type is built with the node's lessor too
// (statemachine.WithKVLessor): it keeps its leases durably in its own
// keyspace, and the leader-side expiry sweeper this node runs over lr revokes
// them. A type that cannot keep leases is refused at start
// (SMLeasesUnsupportedError) rather than run without them: cluster membership
// and the feature gate are lease-backed, so it could not form a cluster.
func buildSMCreateFunc(f *startFlags, enc *atrest.Encryptor, lr *lease.Lessor, hub *watch.Hub) (sm.CreateFunc, error) {
	pebbleOpts := []store.Option{store.WithEncryptor(enc)}
	switch f.smType {
	case "mvcc":
		return store.NewMVCCCreateFuncWith(pebbleOpts,
			store.WithLessor(lr),
			store.WithLogs(),
			store.WithWatchHub(hub),
			store.WithTimeSeriesEnabled(),
		), nil
	case "pebble":
		return store.NewPebbleCreateFuncWith(pebbleOpts,
			func(s *store.PebbleStateMachine) {
				s.SetLessor(lr)
				s.SetLogs(vlog.NewChain())
				s.SetWatchHub(hub)
				s.SetTimeSeriesEnabled(true)
			},
		), nil
	default:
		return resolveCreateFunc(f.smType, f.smBackend,
			statemachine.StorageConfig{Root: f.dataDir, Kind: f.smStorage, Encryptor: enc},
			statemachine.WithKVLessor(lr))
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
func resolveCreateFunc(smType, smBackend string, sc statemachine.StorageConfig, opts ...statemachine.KVOption) (sm.CreateFunc, error) {
	createFn, err := statemachine.ResolveTypedWithStorage(smType, smBackend, sc, opts...)
	if err == nil {
		return createFn, nil
	}
	var notKV *statemachine.KVOptionsNotApplicableError
	if errors.As(err, &notKV) {
		return nil, &SMLeasesUnsupportedError{SMType: smType, Err: err}
	}
	var unknownStorage *statemachine.UnknownSMStorageError
	if errors.As(err, &unknownStorage) {
		return nil, &UnknownSMStorageError{Name: sc.Kind}
	}
	var notApplicable *statemachine.SMStorageNotApplicableError
	if errors.As(err, &notApplicable) {
		return nil, &SMStorageTypeMismatchError{Storage: sc.Kind, SMType: smType}
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

// raftAddrToNodeIDMap inverts parsePeers' id->address map into the
// address->decimal-id-string shape lifecycle.Infra.RaftAddrToNodeID wants
// (the same string form volume.NodeReport.NodeID carries). A parse failure
// yields an empty map rather than propagating the error: the volume-pool-
// health ReplicaFilter is optional (see BuildServices' own guard,
// len(infra.RaftAddrToNodeID) > 0), not a boot-blocking dependency, and
// --peers is validated elsewhere in the boot sequence regardless.
func raftAddrToNodeIDMap(peers []string) map[string]string {
	members, err := parsePeers(peers)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(members))
	for id, addr := range members {
		out[addr] = strconv.FormatUint(id, 10)
	}
	return out
}

// isSingleNode returns true when the peer configuration describes a
// single-node cluster (only the local node). Non-static discovery methods
// always assume multi-node.
func isSingleNode(f *startFlags) bool {
	return cluster.IsSingleNode(f.clusterOptions())
}

// resolveMemberIdentities returns the node identities this node seeds into the
// membership it founds, keyed by replica ID. The local entry is derived from
// the certificate the node holds, whether or not it runs a Raft transport: a
// single node runs none and checks no identity, but the membership it commits
// is the one every replica later admitted to the shard authorizes it by, and
// a founder committed with no identity is refused as missing_identity by the
// first member it adds. A multi-node member also takes every other replica's
// identity from --member-identities; a single node has no other member to
// name, so it seeds its own alone.
func resolveMemberIdentities(f *startFlags, bundle *TLSBundle) (map[uint64]string, error) {
	var selfCert []byte
	if bundle != nil {
		selfCert = bundle.CertPEM
	}
	if isSingleNode(f) {
		if len(selfCert) == 0 {
			return nil, nil
		}
		return cluster.ResolveMemberIdentities(nil, f.replicaID, selfCert)
	}
	return cluster.ResolveMemberIdentities(f.memberIdentities, f.replicaID, selfCert)
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

	// tenantSvc is the tenant servicer the stores were built over. The auth
	// middleware reads each principal's tenant Status from it, and the mTLS
	// authenticator holds a certificate's tenant to it, so a trust domain no
	// tenant was created for names no one. Nil when none was available.
	tenantSvc tenant.TenantServicer

	// legacyFactorStore is the UNCONFINED root store the pre-tenant
	// auth-factor records live in, handed to the go-qrdb#393 migration.
	legacyFactorStore kvstore.KVStore
}

// authBootstrapResult holds the auth enforcement middleware and audit logger
// created only when --auth-enabled=true.
type authBootstrapResult struct {
	auditLogger     audit.Logger
	authMW          *middleware.AuthMiddleware
	soAuthenticator *auth.SOAuthenticator

	// awaitingAdminCredentialProbe is non-nil whenever auth.mtls_binding_policy
	// is "required" (go-qrdb#548 review item 3): the daemon no longer
	// refuses to boot when the bootstrap admin holds no live certificate
	// binding. Instead it starts every listener with this probe installed
	// (server.WithAwaitingAdminCredentialProbe), serving ONLY ceremony
	// status and ceremony-initialize (the admin-credential retry) until a
	// certificate binds -- at which point the SAME probe reports
	// not-awaiting and every transport serves normally, with no restart.
	awaitingAdminCredentialProbe *firstboot.AwaitingAdminCredentialProbe

	// adminWatchWG tracks the firstboot.WatchForAdminBound goroutine, when
	// one was started, so runStart's shutdown path can wait for it to
	// actually exit instead of leaving it to be reaped by process exit
	// alone.
	adminWatchWG *sync.WaitGroup

	// adminWatchCancel ends the watch goroutine's own context. It must be
	// called BEFORE adminWatchWG.Wait() (go-qrdb#591 re-review, item A):
	// the goroutine runs on a context bootstrapAuthEnforcement derives for
	// it, not on sigCtx directly, because sigCtx is cancelled ONLY by a
	// shutdown signal -- a non-signal error return elsewhere in runStart
	// would otherwise make the deferred Wait() block forever on a watch
	// nothing had told to stop. Always non-nil, even when no watch
	// goroutine was actually started, so the caller can defer it
	// unconditionally.
	adminWatchCancel context.CancelFunc
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
//
// userOpts are applied to every user store it builds -- under
// auth.mtls_binding_policy "required", user.WithBootstrapAdminBindingRequired,
// so no transport can make a change that leaves the bootstrap admin unable to
// log in by certificate (delete, rename, disable or suspend it, drop mtls from
// its methods, or withdraw its last live binding).
func bootstrapUserRoleStores(ctx context.Context, bootKVSvc, runtimeKVSvc service.KVServicer, txSvc service.TransactionServicer, node raftNode, leaderWait time.Duration, shardID, replicaID uint64, tenantSvc tenant.TenantServicer, strictSoD bool, barriers *seal.BarrierRegistry, sealStrategy seal.StrategyID, auditLogger audit.AuditLogger, mtlsBindingPolicy auth.MTLSBindingPolicy, ceremonyCoreSvc *ceremony.CeremonyService, ceremonyCA ceremony.CeremonyCA, clientCACertPEM []byte, userOpts ...user.Option) (*mgmtBootstrapResult, error) {
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
	// second wait is cheap (LeaderID is a local, already-known answer
	// once a leader exists) and keeps this function independently correct
	// rather than relying on ordering against its caller.
	if err := waitForLeaderElection(ctx, node, leaderWait, shardID); err != nil {
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
		tenant.IdentityNamespace(tenantid.System),
	)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot store namespace", Err: err}
	}
	// bootStore's PutIfAbsent is the pending-admin-cert record's
	// create-if-absent guard (go-qrdb#548): NewNamespacedKVStore preserves
	// AtomicKVStore when its inner store has it, and guardedBootAdapter
	// (asserted MultiGuardedBatchKVStore just above) always does. Defensive
	// rather than a panic if that construction ever changes.
	pendingAdminStore, ok := bootStore.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &AuthBootstrapError{Step: "boot store atomic", Err: ErrTransactionServiceRequired}
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
	bootUserSvc, err := user.NewStore(bootStore, userIDGen, append([]user.Option{
		user.WithAuditLogger(auditLogger), user.WithIdentityIndex(bootIdentityIndex), user.WithAtomicIdentity()}, userOpts...)...)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot user store", Err: err}
	}
	bootHolders, err := user.NewRoleRevoker(bootUserSvc)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot role holder revoker", Err: err}
	}
	bootRoleSvc, err := role.NewStore(bootStore,
		role.WithAuditLogger(auditLogger), role.WithIdentityIndex(bootIdentityIndex), role.WithAtomicIdentity(),
		role.WithHolderRevoker(bootHolders))
	if err != nil {
		return nil, &AuthBootstrapError{Step: "boot role store", Err: err}
	}

	// If THIS node ran the ceremony (sealed-window Initialize or a retry
	// through CeremonyServiceWithAdminRebind), its *ceremony.CeremonyService
	// holds the bootstrap admin's issued-or-adopted certificate ONLY in
	// memory (go-qrdb#548): the seal/credential store cannot carry it
	// (per-node by design), so it is written here into bootStore -- the
	// REPLICATED system store first boot's own role/user records live in --
	// as soon as that store exists. A crash before this write loses the
	// in-memory value; the admin-credential step is retriable (re-run
	// `qrdb init`) until the first binding commits, so that gap is
	// recoverable rather than a permanent loss.
	//
	// The write is create-if-absent. On a multi-node cluster every node that
	// ran the ceremony races to record ITS OWN credential here; exactly one
	// wins. A loser is not a boot failure -- another node's credential
	// already won the race and will be the one first boot binds below, so
	// this node continues booting normally. Any OTHER error (a genuine store
	// fault) still fails the boot.
	//
	// A node whose replica already holds the record proposes nothing: every
	// boot after an in-process recycle or unseal reaches here with the same
	// core and its certificate (go-qrdb#662). A write that must run waits
	// out an election (service.LeadershipTransient) rather than exiting.
	if err := firstboot.WriteIssuedAdminCertIfAny(ctx, ceremonyCoreSvc, pendingAdminStore, service.LeadershipTransient); err != nil {
		return nil, &AuthBootstrapError{Step: "pending admin credential", Err: err}
	}

	// First-boot: create the system tenant, register built-in roles, and
	// create the admin user with the canonical Admin@system grant. The
	// system-tenant rule in pkg/auth/rbac promotes that single grant to
	// cross-tenant authority. Idempotent across daemon restarts.
	if err := runFirstBoot(ctx, node, leaderWait, shardID, bootUserSvc, bootRoleSvc, tenantSvc, strictSoD, barriers, sealStrategy, mtlsBindingPolicy, pendingAdminStore, ceremonyCA, clientCACertPEM); err != nil {
		return nil, err
	}

	// The binding committed (or the admin already held one): the pending
	// record, if any, has served its purpose. Best-effort -- the binding
	// already succeeded, so a failure here is logged, not fatal.
	if err := firstboot.ClearPendingAdminCert(ctx, pendingAdminStore); err != nil {
		slog.Warn("first-boot: failed to clear the pending admin credential record", slog.Any("error", err))
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
		runtimeStore, tenant.IdentityNamespace(tenantid.System))
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
	userSvc, err := user.NewStore(systemStore, userIDGen, append([]user.Option{
		user.WithAuditLogger(auditLogger), user.WithIdentityIndex(systemIdentityIndex), user.WithAtomicIdentity()}, userOpts...)...)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "user store", Err: err}
	}
	holders, err := user.NewRoleRevoker(userSvc)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "role holder revoker", Err: err}
	}
	roleSvc, err := role.NewStore(systemStore,
		role.WithAuditLogger(auditLogger), role.WithIdentityIndex(systemIdentityIndex), role.WithAtomicIdentity(),
		role.WithHolderRevoker(holders))
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
		routedUsers, ruErr := user.NewRoutingService(resolver, userIDGen, append([]user.Option{user.WithAuditLogger(auditLogger)}, userOpts...)...)
		if ruErr != nil {
			return nil, &AuthBootstrapError{Step: "routed user store", Err: ruErr}
		}
		routedHolders, rhErr := user.NewRoleRevoker(routedUsers)
		if rhErr != nil {
			return nil, &AuthBootstrapError{Step: "routed role holder revoker", Err: rhErr}
		}
		routedRoles, rrErr := role.NewRoutingService(resolver, role.WithAuditLogger(auditLogger), role.WithHolderRevoker(routedHolders))
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
		tenantSvc:         tenantSvc,
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
func bootstrapAuthEnforcement(ctx context.Context, mgmt *mgmtBootstrapResult, dataDir string, node raftNode, complianceAudit audit.AuditLogger, strictSoD bool, clusterTrustDomain string, abacCfg qrdbconfig.AuthABACConfig, certProv cert.CertProvider, issuerVerifier auth.IssuerVerifier, pendingAdmins middleware.PendingTenantAdmins, pins middleware.FirstAdminPins, certTargets middleware.CertTargetGuard, revocationCacheTTL string, auditFailurePolicy string, mtlsBindingPolicy string, awaitingAdminCredentialProbe *firstboot.AwaitingAdminCredentialProbe, watchSvc firstboot.AdminRecordWatcher) (*authBootstrapResult, error) {
	_ = node
	var adminWatchWG sync.WaitGroup
	// watchCtx is a child of ctx (sigCtx) with its OWN cancel
	// (go-qrdb#591 re-review, item A), created unconditionally -- whether
	// or not a watch goroutine ends up starting below -- so this is the
	// single, vet-recognizable "context.WithCancel immediately followed
	// by a deferred cancel" site the lostcancel analyzer expects. It is
	// released by this deferred cleanup on every return from this
	// function EXCEPT the final, successful one: that one hands
	// adminWatchCancel to the caller as part of *authBootstrapResult
	// (keepWatchCtx set just before it), since sigCtx is cancelled ONLY
	// by a shutdown signal and a non-signal error return elsewhere in
	// runStart must still be able to stop the watch goroutine, when one
	// was started, before runStart's own deferred adminWatchWG.Wait()
	// would otherwise block on it forever.
	watchCtx, adminWatchCancel := context.WithCancel(ctx)
	keepWatchCtx := false
	defer func() {
		if !keepWatchCtx {
			adminWatchCancel()
		}
	}()

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
	// fallbacks (user and role) stay on the shared-store boot services: they
	// serve the SYSTEM TENANT ONLY — bootUserSvc/bootRoleSvc are confined to
	// its identity namespace — for the boot window in which the ReadIndex
	// pipeline is not yet warm. For any other tenant, barrier-isolated or
	// not, the fallback is never consulted: the auth middleware and the RBAC
	// authorizer both gate on tenantid.IsSystem before reaching it, so
	// that tenant's caller gets the primary's transient error as a retryable
	// 503, not the fallback's "not found" read as a definitive denial.
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

	// The mTLS enrolment posture (go-qrdb#517). An unrecognised value aborts
	// the boot.
	//
	// The admin is read through the LINEARIZABLE runtime store, not the
	// StaleRead boot store: first-boot waited for the admin RECORD to
	// replicate, but a binding enrolled on the leader afterwards may not have
	// reached this follower yet, and judging the stale record would refuse a
	// boot that should succeed. A read that cannot be served yet is retried
	// for a bounded time before it is treated as a verdict.
	//
	// Under "required", a bootstrap admin that still cannot log in by
	// certificate after that bounded wait no longer aborts the boot
	// (go-qrdb#548 review item 3, reversing the earlier ruling: a node that
	// started would admit nobody able to repair it). Instead it boots into
	// "awaiting admin credential": every listener starts, but
	// awaitingAdminCredentialProbe makes every transport serve ONLY
	// ceremony status and ceremony-initialize (CeremonyServiceWithAdminRebind's
	// admin-credential retry) until a certificate binds -- at which point
	// the SAME probe reports not-awaiting and every transport serves
	// normally, with no restart. That retry is otherwise unreachable: the
	// daemon wires no CertBinder into first boot's OWN CA/Signer path, so a
	// fresh node under "required" has no other way to bind a certificate
	// than `qrdb init` against the leader.
	bindingPolicy, err := auth.ParseMTLSBindingPolicy(mtlsBindingPolicy)
	if err != nil {
		return nil, &AuthBootstrapError{Step: "auth.mtls_binding_policy", Err: err}
	}
	if bindingPolicy == auth.MTLSBindingRequired {
		lookupErr := firstboot.VerifyAdminCertBindingWithin(ctx, mgmt.userSvc, adminBindingWait)
		if lookupErr != nil {
			slog.Warn(`auth.mtls_binding_policy is "required" and the bootstrap admin holds no live certificate binding; `+
				`booting into "awaiting admin credential" -- re-run "qrdb init" against the Raft leader with `+
				`--admin-csr, --admin-cert or --admin-generate to bind one`,
				slog.String("error", lookupErr.Error()))
		}
		// A failed lookup (as opposed to a verdict) is NOT "awaiting"
		// (go-qrdb#560 review B1): Initialize returns it here as a typed
		// error so it is logged, while the probe itself is left reporting
		// not-awaiting -- an infrastructure fault distinct from the
		// admin's own binding state must not additionally lock the node
		// down to the two ceremony verbs this boot state serves.
		if initErr := awaitingAdminCredentialProbe.Initialize(bindingPolicy, lookupErr); initErr != nil {
			slog.Warn(`auth.mtls_binding_policy is "required" but the bootstrap admin's binding could not be verified; `+
				`serving normally rather than locking the node down over this fault alone`,
				slog.String("error", initErr.Error()))
		}
		// The boot-time lookup above decided ENTRY into "awaiting admin
		// credential"; EXIT is driven by the replicated store on every node,
		// not only the one that performs the reissue (go-qrdb#560 review A).
		// CeremonyServiceWithAdminRebind's AdminBound call only reaches this
		// probe when THIS node ran the reissue -- ordinarily the Raft leader,
		// since that retry is leader-gated. A follower learns the admin
		// bound only once the write replicates, so it watches the admin's
		// own user record for the first live binding and tells the SAME
		// probe, with no restart and no per-request re-read. Started only
		// when this boot actually decided "awaiting": a node that never
		// entered that state has nothing for this watch to change, and the
		// probe's AdminBound is permanent for the life of the probe, so a
		// later binding expiry never needs to re-arm it.
		if watchSvc != nil && awaitingAdminCredentialProbe.AwaitingAdminCredential() {
			adminWatchWG.Add(1)
			go func() {
				defer adminWatchWG.Done()
				if werr := firstboot.WatchForAdminBound(watchCtx, mgmt.userSvc, watchSvc, awaitingAdminCredentialProbe); werr != nil && watchCtx.Err() == nil {
					slog.Warn("watch for the bootstrap admin's certificate binding ended unexpectedly; "+
						"this node stays in \"awaiting admin credential\" until a restart or a local reissue",
						slog.String("error", werr.Error()))
				}
			}()
		}
	}

	authMW, err := wiring.BuildAuthMiddleware(wiring.AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         mgmt.servedRoleSvc,
		RoleServiceFallback: mgmt.bootRoleSvc,
		UserService:         mgmt.servedUserSvc,
		UserServiceFallback: mgmt.bootUserSvc,
		StrictSeparation:    strictSoD,
		ClusterTrustDomain:  clusterTrustDomain,
		Tenants:             mgmt.tenantSvc,
		ABACEnabled:         abacCfg.Enabled,
		ABACPolicyDir:       abacCfg.PolicyDir,
		AuditLogger:         auditLogger,
		AuditFailurePolicy:  auditFailure,
		// Certificate revocation is enforced on the authentication path, so
		// the certificate authority has to reach the middleware and not only
		// the cert API handlers. Without this the daemon records revocations,
		// lists them, prints them, and lets the revoked certificate keep
		// authenticating until it expires.
		CertProvider: certProv,
		// The issuer-tenant binding (go-qrdb#560): a Key B leaf's issuing CA
		// must be its own claimed tenant's registered CA, or the cluster CA.
		// issuerVerifier is bundle.TrustPool() -- the SAME object that
		// widens the listener's TLS ClientCAs with every tenant's Key B CA
		// (bundle.SetKeyBProvider/SetClusterCA) -- so Key B is never
		// trusted at the TLS layer without this binding also being
		// enforced here and on the on-behalf forwarding path (go-qrdb#560
		// review, B2-1/B2-2).
		IssuerVerifier: issuerVerifier,
		// The onboarding records tenant create writes: the trust-on-first-
		// init window of a dedicated tenant's barrier admits only the admin
		// that tenant was onboarded with.
		PendingTenantAdmins: pendingAdmins,
		// The first admin pinned for each barrier at onboarding: what that
		// window admits on a node initializing after the record is consumed.
		FirstAdminPins: pins,
		// A principal of a suspended or disabled tenant is refused.
		TenantStatusReader: mgmt.tenantSvc,
		// users:add_cert_binding is held to the certificate target guard.
		CertTargetGuard:    certTargets,
		RevocationCacheTTL: revocationTTL,
		MTLSBindingPolicy:  bindingPolicy,
	})
	if err != nil {
		return nil, &AuthBootstrapError{Step: "auth middleware", Err: err}
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

	keepWatchCtx = true
	return &authBootstrapResult{
		auditLogger:                  auditLogger,
		authMW:                       authMW,
		soAuthenticator:              soAuthenticator,
		awaitingAdminCredentialProbe: awaitingAdminCredentialProbe,
		adminWatchWG:                 &adminWatchWG,
		adminWatchCancel:             adminWatchCancel,
	}, nil
}

// firstAdminPins is init as the auth middleware's middleware.FirstAdminPins,
// or none on a node without barrier init records.
func firstAdminPins(init *lifecycle.BarrierInit) middleware.FirstAdminPins {
	if init == nil {
		return nil
	}
	return init
}

// adminBindingWait bounds the boot-time wait for the bootstrap admin's
// certificate bindings to be readable (firstboot.VerifyAdminCertBindingWithin).
// A variable so tests can shorten it; production uses the shared default.
var adminBindingWait = firstboot.AdminBindingLookupTimeout

// keyWrapInjection carries an optional keywrap.Registry/Provider override
// shared by this daemon's live BuildServices call (above) AND its own
// offline, boot-time disaster-recovery restore path
// (restoreShardFromBackup, restore.go) -- the same registry (or the same
// single extra provider) reaches both, so an embedder that registers a
// hardware or cloud-KMS key-wrap provider sees it honored whichever path
// a boot takes. Nil in production (the built-in "software" provider,
// confined to --backup-key-dir, is all qrdbd ships); a test overrides it
// to prove an injected fake provider is used by both paths (go-qrdb
// security review, backup-encrypt-default #B6). Never written outside
// init or a test.
var keyWrapInjection struct {
	registry keywrap.Registry
	provider keywrap.Provider
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
func runFirstBoot(ctx context.Context, node raftNode, leaderWait time.Duration, shardID uint64, userSvc user.UserServicer, roleSvc role.RoleServicer, tenantSvc tenant.TenantServicer, strictSoD bool, barriers *seal.BarrierRegistry, sealStrategy seal.StrategyID, mtlsBindingPolicy auth.MTLSBindingPolicy, pendingAdminCertStore firstboot.PendingAdminCertStore, ceremonyCA ceremony.CeremonyCA, clientCACertPEM []byte) error {
	// Phase 1: Wait for leader election. All operations (reads on leader
	// via SyncRead, writes via SyncPropose) require a leader.
	if err := waitForLeaderElection(ctx, node, leaderWait, shardID); err != nil {
		return err
	}

	// Phase 2: Check if admin user already exists using local state
	// machine reads (StaleRead via bootstrapHostAdapter). This returns
	// immediately without waiting for ReadIndex.
	//
	// The fast path only skips Phase 3 when the existing admin is ALREADY
	// the System user AND already holds a live certificate binding. An
	// admin created before go-qrdb#106 -- or one whose flag did not
	// persist for any other reason -- carries System: false, and
	// returning here unconditionally skipped the ONLY code path that ever
	// repairs that: firstboot.Initialize/BootstrapSystem's idempotent
	// createAdminUser, whose UsernameExistsError branch calls
	// reconcileExistingAdmin. A cluster upgraded across #106 would keep an
	// admin that was permanently System: false and therefore deletable,
	// forever, because every subsequent start took this same early return.
	//
	// go-qrdb#548 review, item 2: System: true was ALSO taken as proof the
	// admin needs nothing further, which skipped the B6 repair in the one
	// place that actually runs at boot -- an admin created but never
	// bound (a crash between createAdminUser and BindAdminCert, on an
	// EARLIER run) stayed unbound forever, because this fast path never
	// gave firstboot.Initialize's own needsBinding check a chance to run.
	// user.AdminLoginBlocker is the exact predicate needsBinding is built
	// from (verifyAdminBindings wraps it the same way), so this fast path
	// and the repair it falls through to can never disagree about what
	// "needs binding" means.
	//
	// The lookup is bounded like every startup read: one that does not
	// return in StartupReadAttemptTimeout reads as "no admin yet", and
	// Phase 3's idempotent path decides instead.
	actx, cancelLookup := context.WithTimeout(ctx, server.StartupReadAttemptTimeout)
	existingAdmin, ok := adminExists(actx, userSvc, tenantid.System)
	cancelLookup()
	if ok {
		if existingAdmin.System && user.AdminLoginBlocker(existingAdmin, time.Now().UTC()) == "" {
			slog.Debug("first-boot: admin user exists, is already the System user, and holds a live certificate binding, skipping")
			return nil
		}
		if existingAdmin.System {
			slog.Info("first-boot: admin user exists and is the System user, but holds no live certificate binding; running reconciliation to repair it",
				slog.Uint64("user_id", existingAdmin.ID))
		} else {
			slog.Info("first-boot: admin user exists but is not yet the System user; running reconciliation",
				slog.Uint64("user_id", existingAdmin.ID))
		}
		// Fall through to Phase 3: its idempotent init path reconciles the
		// existing admin's System flag and/or certificate binding without
		// minting a second user.
	}

	// Phase 2.5: Resolve the bootstrap admin's certificate, if any is
	// available, BEFORE running first-boot (go-qrdb#548). Under "required"
	// this is a bounded wait: the pending record may not have reached this
	// node's replica yet (it was written on whichever node ran the
	// ceremony), or the admin may already be bound (nothing to do). Under
	// "enrolled" it is a single, non-blocking check -- opportunistic, never
	// required, so an "enrolled" deployment never waits on it.
	adminCertPEM, err := firstboot.ResolveAdminCertForFirstBoot(ctx, pendingAdminCertStore, userSvc, mtlsBindingPolicy, adminBindingWait)
	if err != nil {
		// *NoPendingAdminCredentialError means the bounded wait ran out with
		// neither a live binding nor a pending record -- under "required",
		// that no longer aborts the boot (go-qrdb#560 review B2, matching
		// the reversal already in effect at bootstrapAuthEnforcement's own
		// boot-time check, go-qrdb#548 review item 3): first-boot proceeds
		// with no certificate to bind, leaving the admin unbound, and the
		// node comes up into "awaiting admin credential" once
		// bootstrapAuthEnforcement's own lookup decides that posture. Every
		// other error here (a genuine store fault) still aborts.
		var noCred *firstboot.NoPendingAdminCredentialError
		if !errors.As(err, &noCred) {
			return err
		}
		slog.Warn(`first-boot: no admin certificate binding and no pending admin credential record; `+
			`continuing without one -- the node will boot into "awaiting admin credential"`,
			slog.String("error", err.Error()))
	}
	var adminCertPool *x509.CertPool
	if len(adminCertPEM) > 0 {
		switch {
		case ceremonyCA != nil:
			if caBundle, caErr := ceremonyCA.CABundle(); caErr == nil {
				adminCertPool = firstboot.AdminCertPoolFromCABundle(caBundle)
			}
		case len(clientCACertPEM) > 0:
			// BYO-TLS (go-qrdb#548): there is no CeremonyCA to ask for a
			// bundle -- the operator's own configured client-CA trust
			// anchor, the same one ceremony-initialize's BYOC path
			// validated the presented certificate against, is the pool
			// first boot chains it to here too.
			adminCertPool = firstboot.AdminCertPoolFromCABundle(clientCACertPEM)
		}
	}
	adminBinder := firstboot.UserCertBinder{Users: userSvc}

	// Phase 3: Run the canonical first-boot path. It creates the system
	// tenant (WithInternalCall bypasses the reserved-id rejection), registers
	// all built-in roles, and creates the admin user with the Admin@system
	// grant. Idempotent.
	slog.Info("first-boot: bootstrapping system tenant + roles + admin user",
		slog.String("tenant", tenantid.System))

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
				MTLSBindingPolicy:  mtlsBindingPolicy,
				AdminCertPEM:       adminCertPEM,
				AdminCertPool:      adminCertPool,
			}, bootstrap.Dependencies{
				Tenants:      tenantSvc,
				Users:        userSvc,
				Roles:        roleSvc,
				RoleResolver: roleSvc,
				Binder:       adminBinder,
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
			MTLSBindingPolicy:  mtlsBindingPolicy,
			AdminCertPEM:       adminCertPEM,
			AdminCertPool:      adminCertPool,
			Binder:             adminBinder,
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
		return waitForAdminReplication(ctx, userSvc, tenantid.System)
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

// waitForLeaderElection waits, within leaderWait, until shardID has a known
// leader, reporting a failure as an auth-bootstrap step (see awaitLeader).
func waitForLeaderElection(ctx context.Context, node raftNode, leaderWait time.Duration, shardID uint64) error {
	if err := awaitLeader(ctx, node, leaderWait, shardID); err != nil {
		return &AuthBootstrapError{Step: "wait for leader", Err: err}
	}
	return nil
}

// leaderWaitElections is how many election timeouts of the system shard
// leaderWaitBudget allows for a leader to become known.
//
// One election costs between one and two election timeouts (go-quicraft
// randomizes each replica's timeout over [T, 2T)), plus a PreVote round, and a
// split vote repeats it. On a cluster's first boot the wait also covers the
// peers' own start-up: no election can complete until a quorum of replicas is
// running. 120 is chosen so that no configuration this daemon builds waits
// less than the fixed ~13 s budget it replaces: a single-node shard
// (cluster.SingleNodeElectionRTT x cluster.TransportlessRTTMillisecond, 125 ms)
// gets 15 s -- the same budget server.WaitForShardServing then gives the
// shard's first read -- and go-quicraft's defaults (10 x 200 ms) get 4 min.
const leaderWaitElections = 120

// leaderWaitBudget returns how long qrdbd waits for the system shard to elect
// a leader: leaderWaitElections election timeouts, where the election timeout
// is ElectionRTT x RTTMillisecond as go-quicraft computes it from the host and
// shard configurations this daemon starts the shard with (buildHostConfig,
// buildShardConfig), after go-quicraft fills their unset fields with its
// defaults.
//
// The budget is time, not a count of polls: a count means a different amount
// of wall time on every host and under every load, while an election's cost is
// a function of its configuration.
func leaderWaitBudget(f *startFlags) time.Duration {
	hc := buildHostConfig(f, nil)
	hc.SetDefaults()
	sc := buildShardConfig(f)
	sc.SetDefaults()
	electionTimeout := time.Duration(sc.ElectionRTT*hc.RTTMillisecond) * time.Millisecond
	return leaderWaitElections * electionTimeout
}

// awaitLeader waits until shardID has a known leader for as long as ctx lives,
// and returns ctx's error if it ends first. The wait has no budget of its own:
// each node is unsealed by its own operator, so a quorum can form minutes
// after this node is ready -- and after a system encryption migration's
// in-process restart, only once the peers have restarted and been unsealed
// too. A node that gave up instead would exit, and its peers would then be
// waiting on it.
//
// leaderWait (leaderWaitBudget) is the length of one round of the wait. A
// round that ends without a leader is logged with its
// *server.ShardLeaderWaitError -- naming the shard, the budget and the time
// actually spent -- and the next round begins.
func awaitLeader(ctx context.Context, node server.LeaderSource, leaderWait time.Duration, shardID uint64) error {
	if _, ok, err := node.LeaderID(shardID); err == nil && ok {
		return nil
	}
	slog.Info("waiting for a Raft leader; a quorum of members must be running and unsealed",
		"shard_id", shardID)
	for {
		err := server.WaitForShardLeader(ctx, node, shardID, leaderWait)
		var we *server.ShardLeaderWaitError
		if !errors.As(err, &we) {
			return err
		}
		slog.Warn("still waiting for a Raft leader; a quorum of members must be running and unsealed",
			"shard_id", shardID, "error", err)
	}
}

// adminExists checks whether the admin user already exists using a local
// StaleRead (via bootstrapHostAdapter), returning the record so a caller can
// inspect its System flag rather than only the boolean fact of its
// existence.
func adminExists(ctx context.Context, userSvc user.UserServicer, tenantID string) (*user.User, bool) {
	u, err := userSvc.GetUserByUsername(ctx, tenantID, "admin")
	if err != nil {
		return nil, false
	}
	return u, true
}

// waitForAdminReplication polls the local state machine until the admin
// user appears (replicated from the leader via Raft). Used by follower
// nodes that cannot run SyncPropose themselves.
func waitForAdminReplication(ctx context.Context, userSvc user.UserServicer, tenantID string) error {
	const timeout = 30 * time.Second
	deadline := time.Now().Add(timeout)
	backoff := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		// Each lookup is a startup read attempt with its own bound, so one
		// that never returns costs an attempt, not the boot.
		actx, cancel := server.StartupReadAttemptContext(ctx, deadline, server.StartupReadAttemptTimeout)
		_, ok := adminExists(actx, userSvc, tenantID)
		cancel()
		if ok {
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
	slog.Error("auth bootstrap: admin user not replicated before the deadline; boot fails",
		slog.Duration("timeout", timeout))
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
// It fires at up to two points: once when the resolver / per-tenant registry
// are attached (before any unseal -- needs nothing the data-plane host
// provides), and again when the replicated catalog is attached as the
// allocator (after the host exists, so after unseal for a strategy requiring
// one). Both report the SAME invariant under test -- "a configured volume
// pool reaches the registry" -- at the earliest point each half can.
//
// The wiring it reports is not observable from outside the process, while
// runStart installs its termination handler EARLY. A test that signals on a
// timer, or at t=0, can therefore cancel startup before either attachment and
// still see runStart return cleanly, passing without exercising the wiring it
// is named for (go-qrdb#83).
var volumePoolAttached func()

// dynamicMembershipStarted is invoked once the dynamic membership manager has
// registered this node and started. It is nil in production and set only by
// tests.
//
// Registration runs after the server is listening, so a test that signals
// when the port accepts can cancel it mid-flight and see the cancellation,
// not the outcome it is named for.
var dynamicMembershipStarted func()

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

// databaseSQLConfig converts a configuration file's database SQL settings to
// the database package's; nil stays nil (the defaults).
func databaseSQLConfig(c *qrdbconfig.DatabaseSQLConfig) *database.SQLConfig {
	if c == nil {
		return nil
	}
	return &database.SQLConfig{Concurrency: database.Concurrency(c.Concurrency)}
}
