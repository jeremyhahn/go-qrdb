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

// Package config provides Viper-based configuration loading and validation
// for the QRDB distributed key-value database. Configuration is sourced —
// in increasing order of precedence — from production defaults, a YAML
// config file, and QRDB_-prefixed environment variables.
package config

import (
	"crypto/tls"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	mapstructure "github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// envPrefix is the environment-variable prefix for config overrides. Nested
// keys map to upper-cased, underscore-joined names, e.g. the YAML key
// seal.shamir_total is overridable via QRDB_SEAL_SHAMIR_TOTAL.
const envPrefix = "QRDB"

// parseDuration parses a Go duration string and returns the duration.
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// DefaultInvitationTTLString is the default auth.invitation_ttl: one day.
const DefaultInvitationTTLString = "24h"

// MaxInvitationTTL is the longest auth.invitation_ttl accepted. An
// invitation token is a bearer secret for an account's first credential;
// one that stays redeemable for longer than a week is a standing
// credential, not an invitation.
const MaxInvitationTTL = 7 * 24 * time.Hour

// ParseInvitationTTL parses auth.invitation_ttl, treating empty as
// DefaultInvitationTTLString. It must be positive and at most
// MaxInvitationTTL.
func ParseInvitationTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultInvitationTTLString
	}
	d, err := parseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 || d > MaxInvitationTTL {
		return 0, &ValidationError{Field: "auth.invitation_ttl", Reason: "must be positive and at most " + MaxInvitationTTL.String()}
	}
	return d, nil
}

// RatePolicyConfig is a token-bucket rate policy: RatePerSecond sustained,
// Burst at once. Zero fields take the caller's default.
type RatePolicyConfig struct {
	RatePerSecond float64 `yaml:"rate_per_second"`
	Burst         int     `yaml:"burst"`
}

// InvitationRedeemLimitConfig is auth.invitation_redeem_limit.
type InvitationRedeemLimitConfig struct {
	// PerSource bounds redemptions from one source address.
	PerSource RatePolicyConfig `yaml:"per_source"`
	// PerTenant bounds redemptions into one tenant, from every source.
	PerTenant RatePolicyConfig `yaml:"per_tenant"`
}

// Defaults for auth.invitation_redeem_limit. A person redeems one token
// once; a few retries per source and a few dozen enrolments in a burst per
// tenant cover every legitimate use while making a guessing campaign --
// already hopeless against 256-bit tokens -- also slow and loud.
var (
	DefaultRedeemPerSource = RatePolicyConfig{RatePerSecond: 0.1, Burst: 5}
	DefaultRedeemPerTenant = RatePolicyConfig{RatePerSecond: 1, Burst: 30}
)

// Resolved resolves the redemption limits, applying the defaults to zero
// fields. A negative rate or burst is refused.
func (c InvitationRedeemLimitConfig) Resolved() (perSource, perTenant RatePolicyConfig, err error) {
	if perSource, err = c.PerSource.resolved("auth.invitation_redeem_limit.per_source", DefaultRedeemPerSource); err != nil {
		return RatePolicyConfig{}, RatePolicyConfig{}, err
	}
	if perTenant, err = c.PerTenant.resolved("auth.invitation_redeem_limit.per_tenant", DefaultRedeemPerTenant); err != nil {
		return RatePolicyConfig{}, RatePolicyConfig{}, err
	}
	return perSource, perTenant, nil
}

// resolved is p with def's value for each zero field.
func (p RatePolicyConfig) resolved(field string, def RatePolicyConfig) (RatePolicyConfig, error) {
	if p.RatePerSecond < 0 || p.Burst < 0 || math.IsNaN(p.RatePerSecond) || math.IsInf(p.RatePerSecond, 0) {
		return RatePolicyConfig{}, &ValidationError{Field: field, Reason: "rate_per_second and burst must be positive (zero takes the default)"}
	}
	out := def
	if p.RatePerSecond > 0 {
		out.RatePerSecond = p.RatePerSecond
	}
	if p.Burst > 0 {
		out.Burst = p.Burst
	}
	return out, nil
}

// Config is the top-level QRDB configuration. It is loaded from a YAML
// file and validated before use.
type Config struct {
	// Mode selects the run mode: "standalone" (no raft — standard
	// database operations plus the always-on security core) or "raft"
	// (single- or multi-node consensus with the full feature set).
	// Empty means "raft" for full backward compatibility.
	Mode string `yaml:"mode"`

	Server    ServerConfig    `yaml:"server"`
	Protocols ProtocolsConfig `yaml:"protocols"`
	Raft      RaftConfig      `yaml:"raft"`
	Storage   StorageConfig   `yaml:"storage"`
	TLS       TLSConfig       `yaml:"tls"`
	MTLS      MTLSConfig      `yaml:"mtls"`
	Unix      UnixConfig      `yaml:"unix"`
	MCP       MCPConfig       `yaml:"mcp"`
	Logging   LoggingConfig   `yaml:"logging"`
	Discovery DiscoveryConfig `yaml:"discovery"`

	// Observability configures the signals the node emits about itself:
	// the slow-sync warning's threshold.
	Observability ObservabilityConfig `yaml:"observability"`

	// ReplicaForwarding configures forwarding a request for a database this
	// node holds no replica of to a node that does.
	ReplicaForwarding ReplicaForwardingConfig `yaml:"replica_forwarding"`

	MVCC          MVCCConfig          `yaml:"mvcc"`
	Watch         WatchConfig         `yaml:"watch"`
	Lease         LeaseConfig         `yaml:"lease"`
	Index         IndexConfig         `yaml:"index"`
	TimeSeries    TimeSeriesConfig    `yaml:"timeseries"`
	WriteMode     WriteModeConfig     `yaml:"write_mode"`
	Auth          AuthConfig          `yaml:"auth"`
	CORS          CORSConfig          `yaml:"cors"`
	SQL           SQLConfig           `yaml:"sql"`
	Codec         CodecConfig         `yaml:"codec"`
	Seal          SealConfig          `yaml:"seal"`
	Bootstrap     BootstrapConfig     `yaml:"bootstrap"`
	ClusterJoin   ClusterJoinConfig   `yaml:"cluster_join"`
	Databases     []DatabaseConfig    `yaml:"databases,omitempty" json:"databases,omitempty"`
	Embedding     EmbeddingConfig     `yaml:"embedding"`
	RAG           RAGConfig           `yaml:"rag"`
	Tenant        TenantConfig        `yaml:"tenant"`
	Transport     TransportConfig     `yaml:"transport"`
	ImportSession ImportSessionConfig `yaml:"import_session"`
}

// TransportConfig bounds resources every API transport (REST, gRPC, the
// Unix domain socket, native QUIC, MCP) admits to an unauthenticated TLS
// peer, before any identity is established. Without it, a peer that never
// authenticates can still force the server to allocate up to whatever a
// transport's own framing allows -- 64 MiB per QUIC stream with up to 1000
// concurrent streams per connection, in one documented case (go-qrdb#576) --
// and open unlimited concurrent connections. These apply identically on
// every transport; there is no larger-message opt-in. A caller with more
// data than MaxMessageBytes splits it across multiple calls (see the
// chunked import session API, docs/transactions).
type TransportConfig struct {
	// MaxMessageBytes bounds a single request or response message any
	// transport accepts or produces: the REST request body
	// (http.MaxBytesReader, applied before routing), the gRPC and Unix
	// MaxRecvMsgSize, the QUIC wire frame payload (validated against this
	// bound before the frame's declared length is used to allocate a
	// buffer), and the MCP JSON-RPC request body. Default:
	// DefaultTransportMaxMessageBytes.
	MaxMessageBytes int `yaml:"max_message_bytes"`

	// MaxConnsPerPeer bounds the number of concurrent connections a
	// single remote IP may hold open against each network listener: the
	// REST HTTP/2 listener, the gRPC data-plane listener, the native QUIC
	// listener and the MCP HTTP listener. The Unix domain socket is
	// unaffected. Zero means "unconfigured" and resolves to
	// DefaultTransportMaxConnsPerPeer; a negative value is an explicit
	// opt-out and means unlimited.
	MaxConnsPerPeer int `yaml:"max_conns_per_peer"`

	// MaxStreamsPerConn bounds the number of concurrent streams a single
	// QUIC connection may have open against the native QUIC transport.
	// Zero means "unconfigured" and resolves to
	// DefaultTransportMaxStreamsPerConn; a negative value is an explicit
	// opt-out and means unlimited.
	MaxStreamsPerConn int `yaml:"max_streams_per_conn"`

	// ClientRateLimit is the pre-authentication request-rate limit per
	// client address, shared by every transport. Unset (the default)
	// applies no limit. See ClientRateLimitConfig.
	ClientRateLimit ClientRateLimitConfig `yaml:"client_rate_limit"`
}

// DefaultTransportMaxMessageBytes is the pre-auth per-message cap applied
// identically across every transport when TransportConfig.MaxMessageBytes
// is unset. 4 MiB comfortably carries any single unary KV, batch, or admin
// request; a caller with a larger payload (bulk import) uses the chunked
// session API instead of a single oversized message.
const DefaultTransportMaxMessageBytes = 4 << 20

// DefaultTransportMaxConnsPerPeer is the per-remote-IP concurrent
// connection cap applied to the gRPC and MCP TCP listeners when
// TransportConfig.MaxConnsPerPeer is unset.
const DefaultTransportMaxConnsPerPeer = 256

// DefaultTransportMaxStreamsPerConn is the per-QUIC-connection concurrent
// stream cap applied when TransportConfig.MaxStreamsPerConn is unset.
const DefaultTransportMaxStreamsPerConn = 128

// ResolvedMaxMessageBytes returns the configured pre-auth message cap, or
// DefaultTransportMaxMessageBytes when unset (zero). Callers use this
// instead of reading MaxMessageBytes directly so a Config built without
// going through DefaultConfig (as most unit tests do) still carries the
// production bound rather than silently admitting unbounded messages.
func (t TransportConfig) ResolvedMaxMessageBytes() int {
	if t.MaxMessageBytes <= 0 {
		return DefaultTransportMaxMessageBytes
	}
	return t.MaxMessageBytes
}

// ResolvedMaxConnsPerPeer returns the configured per-peer connection cap,
// or DefaultTransportMaxConnsPerPeer when unset (zero). A negative value
// is an explicit "unlimited" and passes through unchanged.
func (t TransportConfig) ResolvedMaxConnsPerPeer() int {
	if t.MaxConnsPerPeer == 0 {
		return DefaultTransportMaxConnsPerPeer
	}
	return t.MaxConnsPerPeer
}

// ResolvedMaxStreamsPerConn returns the configured per-connection QUIC
// stream cap, or DefaultTransportMaxStreamsPerConn when unset (zero). A
// negative value is an explicit "unlimited" and passes through unchanged.
func (t TransportConfig) ResolvedMaxStreamsPerConn() int {
	if t.MaxStreamsPerConn == 0 {
		return DefaultTransportMaxStreamsPerConn
	}
	return t.MaxStreamsPerConn
}

// ImportSessionConfig bounds a chunked bulk-import session
// (ImportBegin/ImportAppend/ImportCommit/ImportAbort; see
// docs/transactions/), independently of the per-call
// TransportConfig.MaxMessageBytes cap every individual chunk is also
// subject to. See docs/transactions/engineering.md for the full design.
type ImportSessionConfig struct {
	// MaxSessionBytes bounds the total staged bytes a single session may
	// accumulate across all its chunks. Zero or negative resolves to
	// DefaultImportSessionMaxBytes.
	MaxSessionBytes int64 `yaml:"max_session_bytes"`

	// MaxChunksPerSession bounds the chunk count of a single session.
	// Zero or negative resolves to DefaultImportSessionMaxChunks.
	MaxChunksPerSession int `yaml:"max_chunks_per_session"`

	// MaxSessionsPerTenant bounds the number of concurrent (not yet
	// committed, aborted, or expired) sessions a tenant may hold open at
	// once. Zero or negative resolves to DefaultImportSessionsPerTenant.
	MaxSessionsPerTenant int `yaml:"max_sessions_per_tenant"`

	// SessionTTLSeconds is the session's lease TTL in seconds, renewed on
	// every ImportAppend so only an abandoned session ever expires. Zero
	// or negative resolves to DefaultImportSessionTTLSeconds.
	SessionTTLSeconds int64 `yaml:"session_ttl_seconds"`
}

// DefaultImportSessionMaxBytes is the per-session total staged-bytes cap
// applied when ImportSessionConfig.MaxSessionBytes is unset.
const DefaultImportSessionMaxBytes int64 = 64 << 20

// DefaultImportSessionMaxChunks is the per-session chunk-count cap applied
// when ImportSessionConfig.MaxChunksPerSession is unset.
const DefaultImportSessionMaxChunks = 16384

// DefaultImportSessionsPerTenant is the per-tenant concurrent-session cap
// applied when ImportSessionConfig.MaxSessionsPerTenant is unset.
const DefaultImportSessionsPerTenant = 16

// DefaultImportSessionTTLSeconds is the session lease TTL applied when
// ImportSessionConfig.SessionTTLSeconds is unset.
const DefaultImportSessionTTLSeconds int64 = 3600

// ResolvedMaxSessionBytes returns the configured per-session byte budget,
// or DefaultImportSessionMaxBytes when unset (non-positive).
func (c ImportSessionConfig) ResolvedMaxSessionBytes() int64 {
	if c.MaxSessionBytes <= 0 {
		return DefaultImportSessionMaxBytes
	}
	return c.MaxSessionBytes
}

// ResolvedMaxChunksPerSession returns the configured per-session chunk
// count ceiling, or DefaultImportSessionMaxChunks when unset (non-positive).
func (c ImportSessionConfig) ResolvedMaxChunksPerSession() int {
	if c.MaxChunksPerSession <= 0 {
		return DefaultImportSessionMaxChunks
	}
	return c.MaxChunksPerSession
}

// ResolvedMaxSessionsPerTenant returns the configured per-tenant
// concurrent-session ceiling, or DefaultImportSessionsPerTenant when unset
// (non-positive).
func (c ImportSessionConfig) ResolvedMaxSessionsPerTenant() int {
	if c.MaxSessionsPerTenant <= 0 {
		return DefaultImportSessionsPerTenant
	}
	return c.MaxSessionsPerTenant
}

// ResolvedSessionTTLSeconds returns the configured session lease TTL, or
// DefaultImportSessionTTLSeconds when unset (non-positive).
func (c ImportSessionConfig) ResolvedSessionTTLSeconds() int64 {
	if c.SessionTTLSeconds <= 0 {
		return DefaultImportSessionTTLSeconds
	}
	return c.SessionTTLSeconds
}

// tenantIsolationPolicies is the set of values tenant.isolation_policy may
// take: the settable cluster floors (tenant.ClusterIsolationPolicy) and empty,
// which applies none.
var tenantIsolationPolicies = map[string]struct{}{
	"":                {},
	"strict":          {},
	"operator-access": {},
}

// TenantConfig configures tenant isolation behaviour.
type TenantConfig struct {
	// DefaultQuotas are the resource limits applied to tenants whose own
	// quotas are unset (nil). Conservative production defaults.
	DefaultQuotas TenantQuotaDefaults `yaml:"default_quotas"`

	// DefaultEntitlements are the feature entitlements applied to tenants whose
	// own entitlements are unset (nil). Conservative production defaults: only
	// shared prefix isolation, no dedicated raft/volume tiers.
	DefaultEntitlements TenantEntitlementDefaults `yaml:"default_entitlements"`

	// QuotaReconcileInterval is how often the background storage-quota
	// reconciler flushes the in-memory byte accumulator to the persisted
	// CurrentStorageBytes counters and recomputes the on-disk size of each
	// dedicated-volume tenant to correct drift. Accepts a Go duration string
	// (e.g. "5m", "30s"). Default: "5m". Overridable via
	// QRDB_TENANT_QUOTA_RECONCILE_INTERVAL.
	QuotaReconcileInterval string `yaml:"quota_reconcile_interval"`

	// IsolationPolicy is the cluster isolation-policy floor this node
	// applies at start: "strict" or "operator-access". Empty applies
	// nothing. The floor is a replicated record and tightens only, so this
	// key can set it or tighten it, never loosen it; a refused change is
	// logged and the stored floor stands. See docs/multi-tenancy/user.md.
	// Overridable via QRDB_TENANT_ISOLATION_POLICY.
	IsolationPolicy string `yaml:"isolation_policy"`
}

// DefaultQuotaReconcileIntervalString is the default
// tenant.quota_reconcile_interval when unset.
const DefaultQuotaReconcileIntervalString = "5m"

const (
	// DefaultLeaseCheckpointIntervalString is the default lease.checkpoint_interval.
	// It is etcd's lessor checkpoint interval.
	DefaultLeaseCheckpointIntervalString = "5m"

	// DefaultLeaseFailoverGraceString is the default lease.failover_grace.
	DefaultLeaseFailoverGraceString = "5s"

	// MaxLeaseCheckpointInterval bounds lease.checkpoint_interval.
	MaxLeaseCheckpointInterval = time.Hour

	// MaxLeaseFailoverGrace bounds lease.failover_grace.
	MaxLeaseFailoverGrace = time.Hour
)

// QuotaReconcileIntervalDuration returns the parsed QuotaReconcileInterval, or
// the default 5m when unset.
func (c TenantConfig) QuotaReconcileIntervalDuration() (time.Duration, error) {
	s := strings.TrimSpace(c.QuotaReconcileInterval)
	if s == "" {
		s = DefaultQuotaReconcileIntervalString
	}
	return parseDuration(s)
}

// TenantQuotaDefaults configures the operator-settable default resource quotas
// applied to tenants that have not had explicit quotas set. A value of -1 means
// unlimited; 0 means use the built-in system default.
type TenantQuotaDefaults struct {
	MaxDatabases          int64 `yaml:"max_databases"`
	MaxRaftGroups         int64 `yaml:"max_raft_groups"`
	MaxReplicasPerGroup   int64 `yaml:"max_replicas_per_group"`
	MaxTotalReplicas      int64 `yaml:"max_total_replicas"`
	MaxStorageBytes       int64 `yaml:"max_storage_bytes"`
	MaxDatabaseSizeBytes  int64 `yaml:"max_database_size_bytes"`
	MaxDedicatedWALGroups int64 `yaml:"max_dedicated_wal_groups"`
	// SystemBarrierQuota governs an isolated tenant's use of the shared system
	// barrier (bytes): 0 (default) makes the option unavailable, -1 unlimited,
	// >0 a cap accounted against the system tenant's storage.
	SystemBarrierQuota int64 `yaml:"system_barrier_quota"`
}

// ToQuotas converts the config defaults into a quota.Quotas value.
func (d TenantQuotaDefaults) ToQuotas() *quota.Quotas {
	return &quota.Quotas{
		MaxDatabases:          d.MaxDatabases,
		MaxRaftGroups:         d.MaxRaftGroups,
		MaxReplicasPerGroup:   d.MaxReplicasPerGroup,
		MaxTotalReplicas:      d.MaxTotalReplicas,
		MaxStorageBytes:       d.MaxStorageBytes,
		MaxDatabaseSizeBytes:  d.MaxDatabaseSizeBytes,
		MaxDedicatedWALGroups: d.MaxDedicatedWALGroups,
		SystemBarrierQuota:    d.SystemBarrierQuota,
	}
}

// TenantEntitlementDefaults configures the operator-settable default feature
// entitlements applied to tenants that have not had explicit entitlements set.
type TenantEntitlementDefaults struct {
	AllowedIsolationModes     []string `yaml:"allowed_isolation_modes"`
	AllowDedicatedRaft        bool     `yaml:"allow_dedicated_raft"`
	AllowVolumeStorage        bool     `yaml:"allow_volume_storage"`
	AllowSystemManagedBarrier bool     `yaml:"allow_system_managed_barrier"`
	AllowDedicatedWAL         bool     `yaml:"allow_dedicated_wal"`
	// AllowedBarrierModes lists the dedicated-barrier sealing strategies an
	// isolated tenant may select (e.g. none/passphrase/shamir or an xkms hardware
	// strategy id). Empty permits the built-in software defaults and fail-closes
	// hardware modes.
	AllowedBarrierModes []string `yaml:"allowed_barrier_modes"`
	// AllowedVolumePools lists the named volume pools (pkg/volume's
	// catalog) a "volume" or "raft" tenant may be placed on; the first
	// entry is the tenant's default pool. Fail-closed like
	// AllowedIsolationModes: empty entitles no pool, so an operator
	// running storage.volume_pools must also grant this to create any
	// IsolationVolume/IsolationRaft tenant.
	AllowedVolumePools []string `yaml:"allowed_volume_pools"`
	// AllowReplicatedAudit permits a tenant's audit events to be written to
	// the cluster-wide replicated hash chain
	// (docs/audit/architecture.md#cluster-wide-replicated-trail), in
	// addition to the always-on node-local trail. False (the secure
	// default) unless an operator's hardened compliance configuration sets
	// this so every tenant is covered without a per-tenant
	// tenants:set_entitlements grant.
	AllowReplicatedAudit bool `yaml:"allow_replicated_audit"`
}

// ToEntitlements converts the config defaults into a quota.Entitlements value.
func (d TenantEntitlementDefaults) ToEntitlements() *quota.Entitlements {
	modes := d.AllowedIsolationModes
	if len(modes) == 0 {
		modes = []string{quota.IsolationModePrefix}
	}
	return &quota.Entitlements{
		AllowedIsolationModes:     modes,
		AllowDedicatedRaft:        d.AllowDedicatedRaft,
		AllowVolumeStorage:        d.AllowVolumeStorage,
		AllowSystemManagedBarrier: d.AllowSystemManagedBarrier,
		AllowDedicatedWAL:         d.AllowDedicatedWAL,
		AllowedBarrierModes:       d.AllowedBarrierModes,
		AllowedVolumePools:        d.AllowedVolumePools,
		AllowReplicatedAudit:      d.AllowReplicatedAudit,
	}
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
// Backend is validated as a plain string to avoid a circular import with the
// database package.
type DatabaseConfig struct {
	// Name is the unique identifier for the database.
	Name string `yaml:"name" json:"name"`

	// Backend selects the storage backend: "memory", "file", "pebble", or "raft".
	Backend string `yaml:"backend" json:"backend"`

	// MySQL enables the MySQL wire protocol for this database.
	MySQL bool `yaml:"mysql,omitempty" json:"mysql,omitempty"`

	// PostgreSQL enables the PostgreSQL wire protocol for this database.
	PostgreSQL bool `yaml:"postgresql,omitempty" json:"postgresql,omitempty"`

	// Etcd enables the etcd wire protocol for this database. On a binary
	// without the qrdb_etcd build tag, a non-nil, enabled Etcd block is
	// refused at provisioning time (service.RefuseEtcdNotCompiled) rather
	// than silently accepted.
	Etcd *DatabaseEtcdConfig `yaml:"etcd,omitempty" json:"etcd,omitempty"`

	// SQL holds the database's SQL settings. When set, it is applied at
	// every start -- the file is the declaration -- and a database it is
	// absent from keeps whatever was last set through UpdateDatabaseSQL.
	SQL *DatabaseSQLConfig `yaml:"sql,omitempty" json:"sql,omitempty"`
}

// DatabaseEtcdConfig is a database's etcd wire protocol configuration in the
// configuration file.
type DatabaseEtcdConfig struct {
	// Enabled turns the etcd wire protocol on for this database.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// ListenAddr is the etcd gRPC server's listen address (e.g. ":2379").
	ListenAddr string `yaml:"listen_addr,omitempty" json:"listen_addr,omitempty"`

	// AdvertiseClientURLs are the client URLs advertised for etcd endpoint
	// discovery.
	AdvertiseClientURLs []string `yaml:"advertise_client_urls,omitempty" json:"advertise_client_urls,omitempty"`
}

// DatabaseSQLConfig is a database's SQL settings in the configuration file.
// See docs/row-locking/.
type DatabaseSQLConfig struct {
	// Concurrency is "optimistic" (the default when empty) or "pessimistic".
	Concurrency string `yaml:"concurrency" json:"concurrency,omitempty"`
}

// validDatabaseConcurrencies are the accepted sql.concurrency values; empty
// means the default.
var validDatabaseConcurrencies = map[string]struct{}{
	"":            {},
	"optimistic":  {},
	"pessimistic": {},
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

	// Host is the bind host for every API listener this node opens: the
	// admin/REST server (and its HTTP/3 sibling on the same port), gRPC,
	// the native QUIC transport, MCP, MySQL and PostgreSQL. It is a bind
	// host, not an address peers dial — see AdvertiseHost for that.
	//
	// It is settable, in the documented precedence order, by --host, by
	// QRDB_SERVER_HOST and by server.host in the YAML file, and it reaches
	// every listener through the *Config the daemon hands to server.New.
	// The default is DefaultServerHost.
	Host string `yaml:"host"`

	// AdvertiseHost is the host or IP that peers and clients should use to
	// reach this node's client-facing APIs. It is published into the cluster
	// registry alongside the configured ports so any consumer can map a
	// replica ID onto a reachable API address.
	//
	// It is distinct from Host, which is a bind address and is commonly a
	// wildcard that no remote peer can dial. When AdvertiseHost is unset the
	// node falls back to the host portion of Raft.RaftAddress, which is by
	// definition the address peers already use to reach this node. Deployments
	// where the API and Raft planes are reached over different addresses (NAT,
	// split networking) must set it explicitly.
	AdvertiseHost string `yaml:"advertise_host"`

	// SecureMemoryBudgetMiB bounds the process-wide locked-memory (mlock)
	// budget every secure.Buffer allocation and Secret.Open this node makes —
	// cluster DEKs, credentials, PINs — admits against
	// (enclave.NewAllocatorWithinRLimit + enclave.SetAllocator, installed by
	// `qrdbd start` before anything allocates, go-qrdb#114).
	//
	// Zero, the default, means "not configured": `qrdbd start` derives the
	// budget from the host's RLIMIT_MEMLOCK as the smaller of
	// DefaultSecureMemoryBudgetMiB and the limit less a fixed headroom for
	// locked memory outside the budget, and logs the figure and its source.
	// A positive value is used as given, and one that exceeds the host's
	// discovered RLIMIT_MEMLOCK ceiling is refused at startup, naming the
	// limit and how to raise it, rather than failing an allocation later under
	// load. Sourced from --secure-memory-budget-mib,
	// QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB or server.secure_memory_budget_mib.
	SecureMemoryBudgetMiB int64 `yaml:"secure_memory_budget_mib"`

	// GRPCWeb serves the gRPC-Web protocol for every gRPC service the node
	// hosts from the admin/REST server, under /grpc-web/, through the gRPC
	// server's own interceptor chain. Off by default; enabling it requires
	// protocols.grpc.
	GRPCWeb GRPCWebConfig `yaml:"grpc_web"`
}

// DefaultSecureMemoryBudgetMiB is the locked-memory budget an unconfigured
// node gets when the host's RLIMIT_MEMLOCK leaves room for it, and the ceiling
// of the budget derived from a smaller limit: generous enough for cluster
// DEKs, credentials and PINs (all KB-scale) with headroom for concurrent
// unseal/rotation traffic, while still bounding what an authenticated caller
// can drive this process to mlock. Like DefaultServerHost, this is the ONLY
// place the literal appears. DefaultConfig leaves SecureMemoryBudgetMiB at
// zero ("derive from the host"), so a configured value is always
// distinguishable from this default.
const DefaultSecureMemoryBudgetMiB int64 = 64

// DefaultServerHost is the defaults-layer bind host for every API listener.
// The wildcard preserves the historical out-of-the-box behaviour (a node
// answers on every interface); a deployment that wants containment overrides
// it through server.host, QRDB_SERVER_HOST or --host. This is the ONLY place
// the literal is allowed to appear: DefaultConfig and the --host flag default
// both read it, so there is a single value for the config path to override.
const DefaultServerHost = "0.0.0.0"

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

	// MemberIdentities is a comma-separated list of the Raft member
	// identities of the OTHER replicas, in the format "replicaID=identity"
	// (e.g., "2=spki:3f9a…,3=spki:c01d…"). An identity is "spki:" plus the
	// hex SHA-256 of the replica's certificate SubjectPublicKeyInfo — what
	// `qrdbd node-identity` prints for a certificate file.
	//
	// It is required on every multi-node replica: go-quicraft admits a
	// member by its identity and by nothing else. A founding replica must
	// name every member of the initial membership; a joining replica must
	// name at least one existing member it will accept its bootstrap from.
	// The local replica's own entry is derived from its certificate and may
	// be omitted; if present it must match. Ignored for a single-node
	// cluster, which has no Raft transport.
	MemberIdentities string `yaml:"member_identities"`

	// Join indicates this node is joining an existing cluster rather
	// than forming a new one.
	Join bool `yaml:"join"`

	// SnapshotEntries is the number of applied entries between automatic
	// snapshots. 0 uses the go-quicraft default of 100000 entries.
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
	// identifies a logical engine. Valid values: "standard", "concurrent",
	// "fast", "disk", or empty (which means use SMType directly for
	// registry lookup).
	SMBackend string `yaml:"sm_backend"`

	// SMStorage selects the storage backend the storage-generic state
	// machines (sm_type "disk" or "fast") compose: "pebble" (default),
	// "file", or "memory". Objstore is refused — it is a snapshot/backup
	// tier, never the hot apply path. The pebble-native state machines
	// ("pebble", "mvcc") ignore this knob and validation rejects a
	// non-pebble value combined with them.
	//
	// Every replica of a given shard SHOULD run the identical sm_storage.
	// Raft replicates the command log, not a storage representation, so a
	// shard whose replicas compose different backends still applies the
	// identical sequence of commands to each and is not unsafe by
	// construction — but nothing today verifies this at cluster join, so a
	// mismatch is caught only indirectly (for instance, a StateMachineHash
	// comparison that happens to diverge because of a backend-specific
	// defect, the way go-qrdb#592 did before it was fixed). Keep it uniform
	// across a shard's replicas; there is no operational reason to mix it.
	SMStorage string `yaml:"sm_storage"`

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
	// Backend selects the storage engine: "pebble", "memory", "file" or
	// "objstore". Empty means "pebble".
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

	// VolumePools maps this node's local mounts to catalog pool names.
	// Each entry must name a pool that already exists in the replicated
	// catalog (decision D2) to be served; node configuration carries only
	// the name-to-mount mapping, never the catalog itself. An empty list
	// disables volume-pool placement on this node — keyspace-mode tenants
	// are unaffected.
	VolumePools []VolumePoolConfig `yaml:"volume_pools,omitempty"`

	// AllowDegradedVolumeBoot permits qrdbd start to continue when one or
	// more entries of VolumePools fails to open. The default (false)
	// fails startup outright, naming every inaccessible volume and the
	// reason, and the node exits non-zero rather than serve with some
	// tenants' data unopened. Set true (or pass
	// --allow-degraded-volume-boot) to start every volume and database
	// this node CAN open and skip only the ones on a pool that failed;
	// each skip is logged at ERROR, reported in this node's health
	// report and exported as qrdb_volume_pool_ready, and a request for a
	// database on a skipped pool gets a typed, classified "volume
	// unavailable" error rather than succeeding or 404ing.
	AllowDegradedVolumeBoot bool `yaml:"allow_degraded_volume_boot,omitempty"`

	// Objstore configures the object-storage key-value backend, used when
	// Backend (or a database's backend) is "objstore".
	Objstore ObjstoreConfig `yaml:"objstore,omitempty"`
}

// ObjstoreConfig configures the object-storage key-value backend. The backend
// wraps a go-objstore Storage so any go-objstore provider can serve as a flat
// KV store (suited to blob/backup workloads, not hot consensus paths).
type ObjstoreConfig struct {
	// Provider selects the go-objstore backend: "local", "memory", "s3",
	// "gcs", or "azure". Empty defaults to "local", rooted under the
	// node data directory.
	Provider string `yaml:"provider,omitempty"`

	// Settings carries provider-specific configuration passed verbatim to the
	// go-objstore factory (for example "bucket", "region", "endpoint",
	// "path"). For the local provider an empty "path" defaults to the node
	// data directory. A credential is never a setting: one found here is
	// refused at validation; reference it under Credentials.
	Settings map[string]string `yaml:"settings,omitempty"`

	// Credentials references the provider credential by file. Empty means
	// the provider authenticates through its SDK's default credential chain.
	Credentials ObjstoreCredentialsConfig `yaml:"credentials,omitempty"`
}

// ObjstoreCredentialsConfig references the object-storage credential by file,
// never by value: the configuration holds paths, and the storage engine reads
// each file into a protected-memory handle only while it opens the provider.
// Each file must be a regular file owned by the daemon's user and accessible
// by nobody else (mode 0600 or 0400); one trailing line ending is ignored.
type ObjstoreCredentialsConfig struct {
	// KeyFile is the path to the access key identifier (an S3 access key
	// ID). It requires SecretFile.
	KeyFile string `yaml:"key_file,omitempty"`

	// SecretFile is the path to the secret access key, or to the one key a
	// provider authenticates with alone (an Azure storage account key).
	SecretFile string `yaml:"secret_file,omitempty"`
}

// validate refuses a key file without a secret file. field is the
// dot-separated path of the ObjstoreConfig, for the error. A credential
// inline in Settings is refused by go-objstore's own
// common.RejectCredentialSettings, which this package does not import: a
// lean standalone embedder compiles pkg/config without go-objstore. qrdbd
// start runs it at load, and objstore.New runs it at every open.
func (c ObjstoreConfig) validate(field string) error {
	if c.Credentials.KeyFile != "" && c.Credentials.SecretFile == "" {
		return &ValidationError{Field: field + ".credentials.secret_file", Reason: "is required when key_file is set"}
	}
	return nil
}

// VolumePoolConfig maps one catalog pool name to this node's local mount
// (or directory) and the provider that opens it. The catalog (pkg/volume),
// not this struct, is the authority on pool existence (invariant 8); a name
// with no catalog entry is reported in this node's health report as
// "unknown pool" and never served, without failing boot.
type VolumePoolConfig struct {
	// Name must match a pool already created in the replicated catalog
	// (decision D2) to be served on this node.
	Name string `yaml:"name"`

	// Path is the mount point ("luks2") or directory ("dir") this pool
	// resolves to on this node. Must be an absolute path.
	Path string `yaml:"path"`

	// Provider selects the volume.Provider that opens Path: "dir"
	// (default) or a provider registered by composition (e.g. "luks2").
	// Must equal the catalog's provider for this pool; a mismatch is a
	// runtime refusal, not a load error, because the catalog is not
	// readable at load time.
	Provider string `yaml:"provider,omitempty"`

	// Options carries provider-specific configuration, e.g. for "luks2":
	// {"device": "/dev/nvme1n1"}.
	Options map[string]string `yaml:"options,omitempty"`
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
	// Valid values: "none", "request", "require", "require_and_verify".
	// Empty means "derive from the auth posture": the daemon selects
	// VerifyClientCertIfGiven when auth is enabled (so public endpoints
	// like /health remain reachable without a client certificate) and
	// NoClientCert when auth is disabled. Setting any explicit value
	// overrides that derivation on every listener, including /health.
	ClientAuth string `yaml:"client_auth"`

	// MinVersion specifies the minimum TLS version. Default is "TLS1.3".
	// Only "TLS1.2" and "TLS1.3" are valid values.
	MinVersion string `yaml:"min_version"`

	// AdditionalCAs is the list of additional CA PEM file paths layered
	// onto the server-side ClientCAs pool. The union is deduplicated by
	// canonical filename so the same CA referenced from multiple paths
	// is loaded once.
	AdditionalCAs []string `yaml:"additional_cas"`
}

// MTLSConfig configures dedicated mTLS trust material that is distinct
// from the primary server CA. ClientCAFiles are merged into the server
// listener's ClientCAs pool for verifying client certificates without
// affecting the trust chain used to authenticate the server itself.
type MTLSConfig struct {
	// ClientCAFiles is the list of additional CA PEM file paths used
	// solely for client certificate verification. The union is
	// deduplicated by canonical filename.
	ClientCAFiles []string `yaml:"client_ca_files"`
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

// DiscoveryConfig configures peer discovery. Initial static members are
// taken from raft.peers; the fields here tune the multicast, DNS SRV, and
// token discovery methods. The discovery engine and bootstrap orchestration
// live in pkg/discovery and pkg/cluster.
type DiscoveryConfig struct {
	// Method selects the discovery mechanism: "static", "multicast",
	// "dns", or "token".
	Method string `yaml:"method"`

	// MulticastSecret is the shared secret for multicast discovery
	// authentication.
	MulticastSecret string `yaml:"multicast_secret"`

	// MulticastGroup overrides the multicast address (host:port). Optional;
	// the discovery default applies when empty.
	MulticastGroup string `yaml:"multicast_group"`

	// DNSDomain is the DNS domain used for DNS SRV peer discovery.
	DNSDomain string `yaml:"dns_domain"`

	// DNSService is the SRV service label for DNS discovery (default "_raft").
	DNSService string `yaml:"dns_service"`

	// DNSProto is the SRV protocol label for DNS discovery (default "_udp").
	DNSProto string `yaml:"dns_proto"`

	// TokenEndpoint is the URL of the token discovery service. Required for
	// the "token" method.
	TokenEndpoint string `yaml:"token_endpoint"`

	// Token is the shared bootstrap token sent as a bearer credential to the
	// token discovery service. Required for the "token" method.
	Token string `yaml:"token"`

	// TokenClusterSize is the expected number of nodes token discovery waits
	// for before returning. Zero falls back to MinPeers.
	TokenClusterSize int `yaml:"token_cluster_size"`

	// AllowInsecureTokenHTTP permits http:// token endpoints for development
	// and testing. Production endpoints must use https://.
	AllowInsecureTokenHTTP bool `yaml:"allow_insecure_token_http"`

	// MinPeers is the minimum cluster size used for quorum during bootstrap.
	// Zero uses the cluster package default (3). It also serves as the quorum
	// floor for the dynamic membership manager's auto-removal guard.
	MinPeers int `yaml:"min_peers"`

	// DynamicMembership enables the opt-in lease/watch dynamic Raft membership
	// manager. OFF by default. When true, the daemon registers itself in the
	// membership registry and, on the leader, reconciles the Raft group as
	// nodes join and leave (auto-removal is guarded by MinPeers so a transient
	// blip cannot drop the group below quorum).
	DynamicMembership bool `yaml:"dynamic_membership"`
}

// MVCCConfig configures the MVCC versioned key-value engine.
//
// There is no compaction setting here: MVCC history is compacted per
// database, only by the tenant that owns it, and the node keyspace only by the
// platform's policy -- both replicated records set through the API, never a
// node's configuration (see docs/key-value/).
type MVCCConfig struct {
	// Enabled toggles the MVCC service in either mode. Nil means
	// enabled.
	Enabled *bool `yaml:"enabled"`
}

// WatchConfig configures the real-time watch notification system.
type WatchConfig struct {
	// Enabled toggles the watch service in either mode. Nil means
	// enabled.
	Enabled *bool `yaml:"enabled"`

	// BufferSize is the per-watcher event channel buffer size. Larger
	// values reduce back-pressure at the cost of memory.
	BufferSize int `yaml:"buffer_size"`

	// MaxWatchers is the maximum number of concurrent watchers allowed.
	// Zero means unlimited.
	MaxWatchers int `yaml:"max_watchers"`
}

// LeaseConfig configures the distributed lease management system.
type LeaseConfig struct {
	// Enabled toggles the lease service in either mode. Nil means
	// enabled.
	Enabled *bool `yaml:"enabled"`

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

	// CheckpointInterval is how often the Raft leader replicates the remaining
	// TTL of every lease that outlives the interval, as a Go duration ("5m"),
	// as etcd's lessor does. A replica that becomes leader counts a
	// checkpointed lease from what it had left rather than from a full TTL;
	// the leader also checkpoints every lease on demand before it hands
	// leadership away. A shorter interval tightens the bound on a lease that
	// is no longer renewed, at the cost of a log write per lease per interval.
	// Empty means DefaultLeaseCheckpointIntervalString. Must be above zero and
	// at most MaxLeaseCheckpointInterval.
	CheckpointInterval string `yaml:"checkpoint_interval"`

	// FailoverGrace is the time added, beyond one full TTL, to every lease's
	// deadline when a replica becomes leader, as a Go duration ("5s"). It
	// covers renewals the previous leader never checkpointed and the time
	// clients need to find the new leader. Empty means
	// DefaultLeaseFailoverGraceString; "0s" is valid. Must not be negative and
	// is at most MaxLeaseFailoverGrace.
	FailoverGrace string `yaml:"failover_grace"`
}

// CheckpointIntervalDuration returns CheckpointInterval as a duration, applying
// the default when it is unset.
func (c LeaseConfig) CheckpointIntervalDuration() (time.Duration, error) {
	s := strings.TrimSpace(c.CheckpointInterval)
	if s == "" {
		s = DefaultLeaseCheckpointIntervalString
	}
	return parseDuration(s)
}

// FailoverGraceDuration returns FailoverGrace as a duration, applying the
// default when it is unset.
func (c LeaseConfig) FailoverGraceDuration() (time.Duration, error) {
	s := strings.TrimSpace(c.FailoverGrace)
	if s == "" {
		s = DefaultLeaseFailoverGraceString
	}
	return parseDuration(s)
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
	// The field is a pointer so the zero value of the struct is SECURE BY
	// DEFAULT: nil means "not configured" and IsEnabled treats it as
	// enabled. Embedders that build a DaemonConfig literal without an Auth
	// block therefore get an authenticated server, not a silently open one
	// — the exact regression that shipped when this was a plain bool.
	// Set it explicitly to false to opt out.
	Enabled *bool `yaml:"enabled"`

	// AdminUsername is the certificate CN for the bootstrap admin user.
	// Default: "admin".
	AdminUsername string `yaml:"admin_username"`

	// DefaultTenant is the certificate OU for the bootstrap admin's tenant.
	// Default: "system" (the structurally privileged platform tenant).
	DefaultTenant string `yaml:"default_tenant"`

	// CertTTL is the default validity duration for generated client
	// certificates. Parsed as a Go duration string (e.g., "8760h").
	// Default: "8760h" (1 year).
	CertTTL string `yaml:"cert_ttl"`

	// InvitationTTL is how long an invitation (users:invite) stays
	// redeemable. The token is a bearer secret, so it is short-lived: a Go
	// duration, positive and at most MaxInvitationTTL. Default: "24h".
	InvitationTTL string `yaml:"invitation_ttl"`

	// InvitationRequireKeyPin refuses any invitation that is not pinned to
	// the invited user's key (users:invite --pin-key-fingerprint). On, a
	// stolen token is useless without the user's private key; compliance
	// profiles turn it on. Default: false.
	InvitationRequireKeyPin bool `yaml:"invitation_require_key_pin"`

	// InvitationRedeemLimit throttles invitation redemption -- the one verb
	// any caller may make with no credential -- per source address and per
	// tenant. A throttled redemption gets the same opaque refusal as a bad
	// token. Zero fields take the defaults (see DefaultRedeemPerSource and
	// DefaultRedeemPerTenant).
	InvitationRedeemLimit InvitationRedeemLimitConfig `yaml:"invitation_redeem_limit"`

	// PermCacheTTL is the permission cache time-to-live in seconds.
	// Cached permission sets are evicted after this duration.
	// Default: 30.
	PermCacheTTL int `yaml:"perm_cache_ttl"`

	// RevocationCacheTTL is how long the authentication path caches a
	// NOT-REVOKED answer for a client certificate. Parsed as a Go duration
	// string (e.g. "30s").
	//
	// READ IT AS A SECURITY WINDOW, NOT A PERFORMANCE KNOB: this is the
	// maximum time a certificate that has JUST BEEN REVOKED continues to
	// authenticate on a node that had already seen it. Raising it buys a
	// cheaper authentication path and widens that window by exactly the same
	// amount.
	//
	// "0" disables caching: every request consults replicated revocation
	// state, which closes the window entirely at the cost of a key-value read
	// per request. A REVOKED answer is never cached with an expiry and is
	// unaffected by this setting, because revocation is a one-way door.
	//
	// Empty uses the default, "30s".
	RevocationCacheTTL string `yaml:"revocation_cache_ttl"`

	// SeparationOfDuties selects the RBAC posture. When false (default,
	// "standard"), the bootstrap admin is granted a full operational
	// owner capability so a single identity can run the entire database
	// — the convenient posture for commercial deployments that do not
	// require regulatory separation of duties. When true ("strict"), the
	// canonical FIPS 140-3 / PCI-DSS least-privilege role model is
	// enforced: admin is control-plane only, data-plane requires the
	// operator role, governance requires the SO role, and the SO->crypto
	// deny-list applies.
	//
	// This setting only RAISES strictness. A binary built with FIPS 140-3
	// mode (GOFIPS140) — i.e. crypto/fips140.Enabled() — is always strict
	// regardless of this flag, so a FIPS deployment cannot be relaxed into
	// the convenience posture.
	//
	// Default: false.
	SeparationOfDuties bool `yaml:"separation_of_duties"`

	// SODualFactor enables Security Officer dual-factor enforcement on
	// the REST transport. When true, routes mapped to SO-gated
	// permissions (ceremony initialize / enroll-custodian /
	// revoke-custodian / sign-csr, seal rekey / rotate-root-key /
	// generate-recovery / shamir-initialize) additionally require a
	// valid in-band SO PIN via the X-QRDB-SO-PIN header, verified
	// against the Argon2id commit stored at SO bootstrap. Requests over
	// plain HTTP are rejected before the header is read. Requires the
	// SO authenticator to be wired (server.WithSOAuthenticator); when
	// the flag is set without a verifier the middleware is not
	// installed. Default: false.
	SODualFactor bool `yaml:"so_dual_factor"`

	// ClusterTrustDomain is the operator-pinned trust domain used for
	// cluster-peer node identities. It is encoded into every auto-generated
	// node certificate as a SPIFFE URI SAN
	// (spiffe://<trust-domain>/node/<raftAddress>) and is pinned by both the
	// clusterkeys.Receiver (checking incoming peer certs) and the
	// clusterkeys.Sender (verifying recipient delivery certs). A single value
	// must be consistent across all nodes in the cluster.
	//
	// It is also the cluster's own trust domain, the one a certificate
	// naming no tenant is under. A tenant is its own trust domain: a client
	// certificate names a tenant only when its first subject OU and the trust
	// domain of its SPIFFE ID URI SAN (spiffe://<tenantID>/<kind>/<name>)
	// agree, and the mTLS authenticator refuses a tenant equal to this trust
	// domain (fail-closed). The same value reaches the built-in certificate
	// authority (cert.WithTrustDomain), which refuses to mint for a tenant
	// named like it, and tenant create refuses such a tenant ID.
	// Default: "qrdb.cluster" (see EffectiveClusterTrustDomain).
	ClusterTrustDomain string `yaml:"cluster_trust_domain"`

	// PeerAdminURLs is an optional list of peer Raft address → admin HTTPS
	// base URL mappings (e.g. raft_addr "127.0.0.1:7946", admin_url
	// "https://127.0.0.1:8443"). The cluster key sender (Phase 4c-1c) uses
	// this list to locate each follower node's /v1/cluster/nodekey and
	// /v1/cluster/keydelivery endpoints. When a Raft address is absent from
	// this list, the sender falls back to replacing the Raft port with the
	// local admin port — which is correct for production clusters where every
	// node runs the same admin port on a distinct host, but incorrect for
	// single-host deployments (e.g. integration tests) where each node uses a
	// different admin port. Specifying the full peer list via this field
	// eliminates the fallback ambiguity entirely. Default: nil.
	//
	// A list of structs is used rather than a YAML map keyed by IP address
	// because Viper's default key delimiter (".") causes dotted IP addresses
	// used as YAML map keys to be parsed as nested path components, corrupting
	// the decoded value.
	PeerAdminURLs []PeerAdminEntry `yaml:"peer_admin_urls"`

	// AuditFailurePolicy selects what the authorization middleware does when
	// a decision's audit record CANNOT BE WRITTEN (go-qrdb#335).
	//
	//   "fail-closed" (default, and what an empty value means)
	//       The request is REFUSED, with 503 / UNAVAILABLE. No authorization
	//       decision is made unless its audit record persisted -- an
	//       unrecorded grant is precisely the event audit exists to prevent.
	//
	//   "fail-open"
	//       The request PROCEEDS and the failure is counted on
	//       qrdb_auth_audit_write_failures_total{outcome="permitted"}.
	//
	// IT IS A COMPLIANCE SETTING, NOT A TUNING KNOB. "fail-open" gives up the
	// property that every authorization decision this node made is in the
	// trail; the trail acquires a gap whose size is exactly that counter. The
	// lever exists because fail-closed has a genuine availability cliff -- a
	// full audit disk denies every authorized request -- and an operator
	// facing that at 3am needs something better than patching the binary.
	//
	// Setting it to "fail-open" logs a warning at EVERY startup naming the
	// property given up. It is not a fallback: nothing degrades to it
	// automatically, nothing retries and then permits, and the node does
	// exactly what this value says for as long as it says it.
	//
	// An unrecognised value FAILS THE BOOT rather than resolving to either
	// posture. Default: "fail-closed".
	AuditFailurePolicy string `yaml:"audit_failure_policy"`

	// MTLSBindingPolicy selects whether a certificate login must present a
	// certificate enrolled on the account it names (go-qrdb#517).
	//
	//   "required" (default, and what an empty value means)
	//       EVERY certificate login must present a certificate enrolled on the
	//       account it names. An account with no bindings, and a certificate
	//       naming no account, are refused (401 / UNAUTHENTICATED).
	//
	//   "enrolled"
	//       An account's bindings are enforced only once it has one. An
	//       account with no bindings authenticates on the certificate's CN and
	//       tenant alone.
	//
	// "required" is a lockout risk, so a node whose bootstrap admin could
	// not log in under it does not refuse to boot: it serves ONLY the
	// ceremony status and admin-credential-retry endpoints ("awaiting admin
	// credential") until the admin exists and holds at least one unexpired
	// certificate binding. Migrate by enrolling every account's certificate
	// under "enrolled" first; roll back by setting "enrolled" again.
	//
	// An unrecognised value FAILS THE BOOT rather than resolving to either
	// posture. Default: "required".
	MTLSBindingPolicy string `yaml:"mtls_binding_policy"`

	// ABAC configures the optional attribute-based access control decorator
	// layered over the RBAC authorizer. When enabled, YAML policies from
	// PolicyDir are evaluated before RBAC: an explicit deny overrides an
	// RBAC allow, an explicit allow grants beyond RBAC, and unmatched
	// requests fall through to RBAC. Default: disabled.
	ABAC AuthABACConfig `yaml:"abac"`
}

// DefaultRevocationCacheTTLString is the default auth.revocation_cache_ttl
// when unset.
//
// It is the window during which a certificate that has just been revoked
// still authenticates on a node that had already seen it. Thirty seconds
// keeps the per-request authentication cost off the replicated store in the
// overwhelmingly common not-revoked case while bounding that window to
// something an operator can state in an incident timeline.
const DefaultRevocationCacheTTLString = "30s"

// RevocationCacheTTLDuration returns the parsed RevocationCacheTTL, or the
// default when unset. A returned zero means caching is disabled and every
// request consults replicated revocation state.
func (c AuthConfig) RevocationCacheTTLDuration() (time.Duration, error) {
	return ParseRevocationCacheTTL(c.RevocationCacheTTL)
}

// ParseRevocationCacheTTL parses a revocation-cache TTL string, treating empty
// as DefaultRevocationCacheTTLString.
//
// It is exported because the value has to be resolved at the auth-wiring site
// as well as off the config struct, and this is a security window: resolving
// it twice with two spellings of "what does empty mean" is how the configured
// value and the enforced value drift apart. An unparseable value is an error,
// never a silent fall back to the default.
func ParseRevocationCacheTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultRevocationCacheTTLString
	}
	return parseDuration(s)
}

// IsEnabled reports whether authentication and authorization are active.
// A nil Enabled — the zero value, an omitted YAML key, an embedder that
// never touched the Auth block — means ENABLED: security is the default
// and disabling it requires an explicit false.
func (c AuthConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// BoolPtr returns a pointer to b, for the explicit-value configuration
// fields whose zero value must be distinguishable from "not configured"
// (AuthConfig.Enabled).
func BoolPtr(b bool) *bool { return &b }

// DefaultClusterTrustDomain is the cluster trust domain when none is
// configured.
const DefaultClusterTrustDomain = "qrdb.cluster"

// EffectiveClusterTrustDomain is ClusterTrustDomain, or
// DefaultClusterTrustDomain when it is empty.
func (c AuthConfig) EffectiveClusterTrustDomain() string {
	if c.ClusterTrustDomain != "" {
		return c.ClusterTrustDomain
	}
	return DefaultClusterTrustDomain
}

// AuthABACConfig configures the ABAC authorization decorator.
type AuthABACConfig struct {
	// Enabled activates ABAC policy evaluation on top of RBAC.
	// Default: false.
	Enabled bool `yaml:"enabled"`

	// PolicyDir is the directory containing YAML policy files (*.yaml,
	// *.yml), loaded once at startup. Required when Enabled is true.
	PolicyDir string `yaml:"policy_dir"`
}

// PeerAdminEntry maps a single peer's Raft address to its admin HTTPS base URL.
// It is an element of AuthConfig.PeerAdminURLs.
type PeerAdminEntry struct {
	// RaftAddr is the peer's Raft address (e.g. "127.0.0.1:7946").
	RaftAddr string `yaml:"raft_addr"`
	// AdminURL is the peer's admin HTTPS base URL (e.g. "https://127.0.0.1:8443").
	AdminURL string `yaml:"admin_url"`
}

// CORSConfig configures Cross-Origin Resource Sharing for the REST API,
// enabling browser-based access.
type CORSConfig struct {
	// AllowedOrigins is the list of allowed request origins; ["*"] admits
	// any origin. Default: none, which turns CORS off: the REST server
	// sends no CORS headers.
	AllowedOrigins []string `yaml:"allowed_origins"`

	// AllowedHeaders is the list of allowed request headers.
	// Default: ["Authorization", "Content-Type"].
	AllowedHeaders []string `yaml:"allowed_headers"`

	// MaxAge is the maximum age (in seconds) for preflight cache.
	// Default: 3600.
	MaxAge int `yaml:"max_age"`
}

// GRPCWebConfig is the server.grpc_web block: gRPC-Web on the REST server.
// The zero value is off. pkg/server hands it to pkg/api/grpcweb, whose
// Config has the same fields and judges the CORS ones.
type GRPCWebConfig struct {
	// Enabled serves gRPC-Web under /grpc-web/.
	Enabled bool `yaml:"enabled"`

	// AllowedOrigins lists the browser origins CORS admits, each a
	// serialized origin (https://app.example.com, with a port when it is not
	// the scheme's default) or "*" for any origin. Empty admits no
	// cross-origin browser call.
	AllowedOrigins []string `yaml:"allowed_origins"`

	// AllowCredentials lets a cross-origin browser call carry credentials.
	// It cannot be combined with the "*" origin.
	AllowCredentials bool `yaml:"allow_credentials"`
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

	// QueryTimeout is the maximum duration for a single SQL statement on the
	// MySQL wire, enforced by the server; a statement past it is cancelled
	// and answered ER_QUERY_TIMEOUT (3024). It is also what bounds a
	// statement whose client has disconnected: the server does not watch the
	// socket while a statement runs. Parsed as a Go duration string; "0"
	// disables it. Default: "30s".
	QueryTimeout string `yaml:"query_timeout"`

	// WaitTimeout is MySQL's wait_timeout: the maximum time a MySQL wire
	// connection may sit idle with no command from the client before the
	// server closes it. A connection holding an open server-side cursor
	// (COM_STMT_FETCH) that the client stopped fetching from is idle by this
	// same definition, so this is also what releases the resources that
	// cursor's statement holds -- the gap QueryTimeout does not cover,
	// because the cursor is served without ever reaching the query handler
	// QueryTimeout bounds. Parsed as a Go duration string; "0" disables it.
	// Default: "8h" (28800s, MySQL's wait_timeout default).
	WaitTimeout string `yaml:"wait_timeout"`

	// WriteTimeout is MySQL's net_write_timeout: the maximum time the server
	// waits for a single write to a stalled client -- one that stopped
	// reading a result set -- before closing the connection. Parsed as a Go
	// duration string; "0" disables it. Default: "60s" (MySQL's
	// net_write_timeout default).
	WriteTimeout string `yaml:"write_timeout"`
}

// DefaultSQLQueryTimeout is sql.query_timeout's default.
const DefaultSQLQueryTimeout = 30 * time.Second

// DefaultSQLWaitTimeout is MySQL's wait_timeout default: the number of
// seconds an idle connection -- one issuing no command, including one
// holding an open server-side cursor its client stopped fetching from -- may
// sit before the server closes it.
const DefaultSQLWaitTimeout = 28800 * time.Second

// DefaultSQLWriteTimeout is MySQL's net_write_timeout default: the number of
// seconds the server waits for a single write to a stalled client before
// closing the connection.
const DefaultSQLWriteTimeout = 60 * time.Second

// QueryTimeoutDuration returns sql.query_timeout as a duration:
// DefaultSQLQueryTimeout when unset, zero when it is "0" (no timeout).
func (c SQLConfig) QueryTimeoutDuration() (time.Duration, error) {
	if c.QueryTimeout == "" {
		return DefaultSQLQueryTimeout, nil
	}
	d, err := parseDuration(c.QueryTimeout)
	if err != nil || d < 0 {
		return 0, &ValidationError{Field: "sql.query_timeout", Reason: "must be a valid Go duration string, zero or more"}
	}
	return d, nil
}

// WaitTimeoutDuration resolves WaitTimeout: unset is the default (8h),
// "0" disables the bound, and a malformed or negative duration is refused.
func (c SQLConfig) WaitTimeoutDuration() (time.Duration, error) {
	if c.WaitTimeout == "" {
		return DefaultSQLWaitTimeout, nil
	}
	d, err := parseDuration(c.WaitTimeout)
	if err != nil || d < 0 {
		return 0, &ValidationError{Field: "sql.wait_timeout", Reason: "must be a valid Go duration string, zero or more"}
	}
	return d, nil
}

// WriteTimeoutDuration resolves WriteTimeout: unset is the default (60s),
// "0" disables the bound, and a malformed or negative duration is refused.
func (c SQLConfig) WriteTimeoutDuration() (time.Duration, error) {
	if c.WriteTimeout == "" {
		return DefaultSQLWriteTimeout, nil
	}
	d, err := parseDuration(c.WriteTimeout)
	if err != nil || d < 0 {
		return 0, &ValidationError{Field: "sql.write_timeout", Reason: "must be a valid Go duration string, zero or more"}
	}
	return d, nil
}

// BootstrapConfig configures TLS bootstrap for cluster enrollment.
// Joining nodes use these settings to securely obtain the cluster CA
// certificate from an existing node before TLS is established.
type BootstrapConfig struct {
	// NoiseKey is the hex-encoded Curve25519 public key of the Noise
	// bootstrap server on the first node. Required for Noise method.
	NoiseKey string `yaml:"noise_key"`

	// P256Key is the hex-encoded uncompressed P-256 public key (65 bytes) of
	// the same bootstrap server's FIPS-approved suite
	// (TSBOOT1_P256_AESGCM_SHA256), published by the first node under the same
	// name. It pins the server when this node fetches the anchor over the
	// bootstrap wire, and is the only suite a node in FIPS 140-3 mode may use
	// there. Requires NoiseAddr; may be set alongside NoiseKey, in which case
	// Noise is used where the process may use it and P-256 otherwise.
	P256Key string `yaml:"p256_key"`

	// NoiseAddr is the host:port of the Noise bootstrap server.
	NoiseAddr string `yaml:"noise_addr"`

	// NoisePort is the TCP port for the Noise bootstrap server
	// (first node only). Default: 63006.
	NoisePort int `yaml:"noise_port"`

	// NoiseHost is the bind host for the Noise NK bootstrap/enrollment
	// server started on the first node. It is a bind host in the same
	// sense as Server.Host, and when unset the daemon uses Server.Host so
	// a node that was contained to one interface does not silently expose
	// its enrollment listener on every interface (go-qrdb#167).
	NoiseHost string `yaml:"noise_host"`

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

	// JoinAddr is the host:port of an admitted member's member-side
	// CSR-sign listener (clusterenroll.JoinServer), for the Noise_NK
	// member-side join transport. Mutually exclusive with JoinHTTPURL in
	// practice: Noise is preferred when both are set.
	JoinAddr string `yaml:"join_addr"`

	// JoinKey is the hex-encoded Curve25519 public key of that listener.
	// Required alongside JoinAddr unless JoinP256Key is set.
	JoinKey string `yaml:"join_key"`

	// JoinP256Key is the hex-encoded uncompressed P-256 public key (65 bytes)
	// of that listener's FIPS-approved suite. Required alongside JoinAddr
	// unless JoinKey is set; with both, Noise is used where the process may
	// use it and P-256 otherwise (FIPS 140-3 mode).
	JoinP256Key string `yaml:"join_p256_key"`

	// JoinHTTPURL is the base admin HTTPS URL of an admitted member (e.g.
	// "https://10.0.0.2:8443"), for the SPKI/Direct member-side join
	// transport (clusterenroll.JoinHTTPPath). Trust in the connection is
	// established the same way SPKIPin/DirectURL already establish it for
	// the anchor fetch.
	JoinHTTPURL string `yaml:"join_http_url"`

	// JoinTokenFile is the path to a file holding the single-use,
	// identity-bound join token an admin minted for this node
	// (clusterenroll.MintJoinToken), not a static shared secret. Mutually
	// exclusive with JoinTokenEnv; the token is never configured inline in
	// YAML. ResolveJoinToken refuses a group- or world-readable file, one
	// not owned by this process's own user, or a value shorter than
	// MinJoinTokenLength.
	JoinTokenFile string `yaml:"join_token_file"`

	// JoinTokenEnv is the name of an environment variable holding the
	// single-use join token. Mutually exclusive with JoinTokenFile.
	JoinTokenEnv string `yaml:"join_token_env"`

	// JoinApprovalPollInterval bounds how often a tokenless CSR (neither
	// JoinTokenFile nor JoinTokenEnv set) is re-presented while it sits in
	// the signing member's replicated approval queue. Zero uses
	// clusterenroll.DefaultJoinApprovalPollInterval.
	JoinApprovalPollInterval time.Duration `yaml:"join_approval_poll_interval"`
}

// ClusterJoinConfig configures this node's member-side CSR-sign surface:
// the capability an ADMITTED member offers to sign a joining node's CSR
// with the cluster CA (Key A). Any admitted, unsealed member may enable
// this, not only the first node (see docs/clustering/architecture.md,
// "Member-side CSR join").
type ClusterJoinConfig struct {
	// Enabled turns on both transports this node offers for signing a
	// joining node's CSR: the Noise_NK listener (ListenHost/ListenPort) and
	// the SPKI/Direct HTTPS route (POST /v1/enroll/join on the existing
	// admin listener). Default false: a node does not sign joins for
	// others unless the operator configures it to.
	Enabled bool `yaml:"enabled"`

	// ListenHost is the bind host for the Noise_NK join listener. Required
	// when Enabled, for the same containment reason Bootstrap.NoiseHost is.
	ListenHost string `yaml:"listen_host"`

	// ListenPort is the TCP port for the Noise_NK join listener. Zero
	// defaults to clusterenroll.DefaultJoinPort.
	//
	// There is no admission-token field here: a signing member validates
	// presented join tokens against the single-use, identity-bound tokens
	// an admin mints into the replicated store
	// (clusterenroll.MintJoinToken/ConsumeJoinToken), not a static shared
	// secret this node would otherwise need configured.
	ListenPort int `yaml:"listen_port"`
}

// SealConfig configures the cryptographic barrier that encrypts data at rest.
type SealConfig struct {
	// Enabled controls whether the seal barrier is active.
	// Default: true (go-qrdb#548). auth.enabled defaults to true too and now
	// requires Enabled: true -- the bootstrap admin's certificate binding
	// depends on `qrdb init` being able to bring the system barrier up at
	// all, which does not exist when the seal is off. A deployment that
	// does not want encryption sets Strategy "none" rather than Enabled
	// false.
	Enabled bool `yaml:"enabled"`

	// Strategy selects the sealing strategy: "none", "passphrase" or
	// "shamir". Default: "none" (go-qrdb#548) -- a barrier-present-but-
	// unencrypted strategy that still satisfies the Enabled-true
	// requirement above with no secret material.
	Strategy string `yaml:"strategy"`

	// RootKeyPath is the storage key under which the sealed root key
	// blob is persisted. Default: "__sys:seal/root_key".
	RootKeyPath string `yaml:"root_key_path"`

	// ShamirThreshold is the minimum shares required to unseal when
	// strategy is "shamir". Must be >= 2 and <= ShamirTotal.
	ShamirThreshold int `yaml:"shamir_threshold"`

	// ShamirTotal is the total number of Shamir shares to generate.
	ShamirTotal int `yaml:"shamir_total"`

	// ShareTTL is how long a Shamir share-holder quorum round stays open
	// before submitted shares expire and must be resubmitted. Accepts a Go
	// duration string (e.g. "15m", "30s"). Default: "15m". Overridable via
	// QRDB_SEAL_SHARE_TTL.
	ShareTTL string `yaml:"share_ttl"`

	// CredentialStrategy selects the credential storage strategy:
	// "manual" or "barrier". Default: "barrier".
	CredentialStrategy string `yaml:"credential_strategy"`

	// ClusterLease is how long a successful read of the cluster seal records
	// lets this node serve; a node that cannot read them for longer stops
	// serving, keeping its keys. Go duration; default "15s".
	ClusterLease string `yaml:"cluster_lease"`

	// ClusterPollInterval is how often this node reads the cluster seal
	// records. At most a third of ClusterLease. Go duration; default "1s".
	ClusterPollInterval string `yaml:"cluster_poll_interval"`

	// ClusterAckTimeout bounds how long a node waits for every node to
	// acknowledge a node-barrier seal before wiping its own key. Go duration;
	// default "5s".
	ClusterAckTimeout string `yaml:"cluster_ack_timeout"`

	// MigrationCredentialsDir holds this node's own credentials for a
	// cluster enable-encryption migration it did not accept the request for:
	// "system" for the node barrier and "tenant-<id>" for a tenant barrier.
	// Each file must be mode 0600 or stricter. Empty: the node can only take
	// part in migrations whose credential it holds by other means.
	MigrationCredentialsDir string `yaml:"migration_credentials_dir"`
}

// DefaultShareTTLString is the default seal.share_ttl when unset.
const DefaultShareTTLString = "15m"

// ShareTTLDuration returns the parsed ShareTTL, or the default 15m when unset.
func (c SealConfig) ShareTTLDuration() (time.Duration, error) {
	s := strings.TrimSpace(c.ShareTTL)
	if s == "" {
		s = DefaultShareTTLString
	}
	return parseDuration(s)
}

// validSealStrategies contains the accepted seal strategy strings.
// "passphrase" is the canonical name for the passphrase-protected software
// seal strategy. "none" is a barrier-present-but-unencrypted strategy: the
// at-rest store is wrapped by the barrier in zero-epoch (plaintext) format so
// it can later be migrated to a keyed strategy in place via
// `qrdb barrier enable-encryption` (see docs/seal/enabling-encryption.md). It
// is the canonical source state for enabling encryption after the fact.
var validSealStrategies = map[string]struct{}{
	"passphrase": {},
	"shamir":     {},
	"none":       {},
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
			AdminPort:             63002,
			GRPCPort:              63003,
			QUICPort:              63004,
			MySQLPort:             3306,
			PgSQLPort:             5432,
			Host:                  DefaultServerHost,
			SecureMemoryBudgetMiB: 0, // derive from RLIMIT_MEMLOCK at start
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
			Enabled: true,
			// ClientAuth is intentionally left empty: a non-empty value is
			// treated by the daemon as an explicit operator selection and
			// disables the auth-derived mTLS mode (VerifyClientCertIfGiven
			// when auth is enabled), which would make cert-exempt endpoints
			// like /health unreachable without a client certificate.
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
			// Single-node by default: no network auto-join, no shared secret.
			// Multicast is an explicit opt-in that requires an operator-chosen
			// PSK (the protocol is authenticated-only); there is deliberately
			// no well-known default secret.
			Method:          "static",
			MulticastSecret: "",
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
		Tenant: TenantConfig{
			// Conservative multi-tenant defaults: a tenant with no explicit
			// quotas/entitlements may create up to 5 databases, consume up to
			// 1 GiB, and use only shared prefix isolation. Dedicated raft and
			// volume tiers are denied until the operator grants them.
			DefaultQuotas: TenantQuotaDefaults{
				MaxDatabases:        5,
				MaxRaftGroups:       0,
				MaxReplicasPerGroup: 3,
				MaxTotalReplicas:    3,
				MaxStorageBytes:     1 << 30, // 1 GiB
			},
			DefaultEntitlements: TenantEntitlementDefaults{
				AllowedIsolationModes: []string{quota.IsolationModePrefix},
				AllowDedicatedRaft:    false,
				AllowVolumeStorage:    false,
			},
			QuotaReconcileInterval: DefaultQuotaReconcileIntervalString,
		},
		Auth: AuthConfig{
			Enabled:       BoolPtr(true),
			AdminUsername: "admin",
			DefaultTenant: "system",
			CertTTL:       "8760h",
			PermCacheTTL:  30,
			// The window during which a revoked certificate still
			// authenticates. See AuthConfig.RevocationCacheTTL.
			RevocationCacheTTL: DefaultRevocationCacheTTLString,
		},
		CORS: CORSConfig{
			AllowedHeaders: []string{"Authorization", "Content-Type"},
			MaxAge:         3600,
		},
		SQL: SQLConfig{
			DefaultDatabase: "qrdb",
			MaxConnections:  100,
			QueryTimeout:    "30s",
			WaitTimeout:     "8h",
			WriteTimeout:    "60s",
		},
		Codec: CodecConfig{
			StorageCodec: "msgpack",
			WireCodec:    "json",
			UseOptimized: true,
		},
		// Enabled defaults to true, strategy "none" (go-qrdb#548): auth
		// defaults to enabled, and auth.enabled now requires seal.enabled,
		// since the bootstrap admin's credential depends on `qrdb init`
		// being able to bring the system barrier up at all. "none" is
		// identity passthrough -- no ciphertext, just the state transition
		// `qrdb init` needs -- for a deployment that does not want
		// encryption; set strategy: passphrase (or stronger) to turn it on.
		Seal: SealConfig{
			Enabled:            true,
			Strategy:           "none",
			RootKeyPath:        "__sys:seal/root_key",
			ShareTTL:           DefaultShareTTLString,
			CredentialStrategy: "barrier",
		},
		Bootstrap: BootstrapConfig{
			NoisePort: 63006,
		},
		Databases: []DatabaseConfig{
			{Name: "system", Backend: "pebble"},
		},
		RAG: RAGConfig{
			DefaultChunkStrategy: "fixed",
			DefaultChunkSize:     512,
			DefaultOverlap:       50,
			MaxDocumentSize:      0,
			DefaultTopK:          5,
		},
		Transport: TransportConfig{
			MaxMessageBytes:   DefaultTransportMaxMessageBytes,
			MaxConnsPerPeer:   DefaultTransportMaxConnsPerPeer,
			MaxStreamsPerConn: DefaultTransportMaxStreamsPerConn,
		},
		ImportSession: ImportSessionConfig{
			MaxSessionBytes:      DefaultImportSessionMaxBytes,
			MaxChunksPerSession:  DefaultImportSessionMaxChunks,
			MaxSessionsPerTenant: DefaultImportSessionsPerTenant,
			SessionTTLSeconds:    DefaultImportSessionTTLSeconds,
		},
	}
}

// Load builds the configuration from (lowest to highest precedence):
// production defaults, the YAML file at path, and QRDB_-prefixed environment
// variables. The file is read by Viper; environment variables can override
// any key (e.g. QRDB_SEAL_SHAMIR_TOTAL for seal.shamir_total).
//
// The file is decoded strictly: any key that names no field of the Config
// schema fails the load with a typed *UnknownFieldError naming the key, its
// full path and, where known, its line -- rather than the key being silently
// dropped and whatever it was supposed to configure staying off.
func Load(path string) (*Config, error) {
	// #nosec G304 - path is the operator-supplied --config file, the same
	// shape already excluded for pkg/clustertls's CA/cert/key paths; no
	// transport handler reaches this argument with a request value.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	if err := validateKnownFields(path, data); err != nil {
		return nil, err
	}

	v, err := newConfigViper(path)
	if err != nil {
		return nil, err
	}

	// Start from defaults so any field absent from every source retains its
	// production default, then overlay Viper's merged file + env values using
	// the struct's existing yaml tags.
	cfg := DefaultConfig()
	if err := v.Unmarshal(cfg, func(dc *mapstructure.DecoderConfig) {
		dc.TagName = "yaml"
	}); err != nil {
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
			{Name: "system", Backend: backend},
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// newConfigViper constructs a Viper bound to the QRDB config sources: the
// production defaults (registered as known keys so AutomaticEnv can override
// any field even when the file omits it), the YAML file at path, and
// QRDB_-prefixed environment variables.
func newConfigViper(path string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigType("yaml")

	// Register every default-bearing key so environment overrides flow
	// through Unmarshal regardless of whether the file mentions the key.
	defaults, err := configToMap(DefaultConfig())
	if err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}
	if err := v.MergeConfigMap(defaults); err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	// Overlay the YAML file (replaces matching default keys).
	v.SetConfigFile(path)
	if err := v.MergeInConfig(); err != nil {
		return nil, &LoadError{Path: path, Err: err}
	}

	// Highest precedence: QRDB_-prefixed env vars, with nested keys joined
	// by underscores (seal.shamir_total -> QRDB_SEAL_SHAMIR_TOTAL).
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	return v, nil
}

// configToMap renders a Config to a generic map keyed by its yaml tags, used
// to seed Viper's defaults so every key is known for env-var resolution.
func configToMap(cfg *Config) (map[string]any, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
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

// validSMTypes contains the accepted state machine type strings. This list
// mirrors what the state machine registry actually provides (the registry's
// built-ins plus the daemon-registered "pebble"/"mvcc") — a
// value accepted here but absent from the registry would pass validation
// and then fail at startup with UnknownSMError, which is exactly the
// phantom-entry trap ("kv", "timeseries", "vector", "cached-disk") this
// list previously contained.
var validSMTypes = map[string]struct{}{
	"pebble":     {},
	"memory":     {},
	"mvcc":       {},
	"standard":   {},
	"concurrent": {},
	"fast":       {},
	"disk":       {},
	"highperf":   {},
	"regular":    {},
}

// validSMBackends contains the accepted state machine backend strings.
// Same registry-mirroring rule as validSMTypes.
var validSMBackends = map[string]struct{}{
	"standard":   {},
	"concurrent": {},
	"fast":       {},
	"disk":       {},
}

// validBackends contains the accepted storage backend strings.
var validBackends = map[string]struct{}{
	"pebble":   {},
	"memory":   {},
	"file":     {},
	"objstore": {},
}

// validVolumePoolStrategies contains the accepted volume-pool
// strategy strings. round_robin is implemented; least_utilization is
// declared so YAML configs can reference it, but the strategy itself
// validateVolumePools verifies pool names are unique and non-empty, every
// pool has an absolute, non-empty path, no two pools' paths overlap, and no
// pool path is inside dataDir or contains it (a volume pool and the node's
// own data directory must stay disjoint). Provider and Options are
// node-local and carry no validity constraint here; a provider mismatch
// with the catalog is a runtime refusal (engineering.md: "Configuration").
func validateVolumePools(pools []VolumePoolConfig, dataDir string) error {
	if len(pools) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(pools))
	for _, p := range pools {
		if p.Name == "" {
			return &ValidationError{
				Field:  "storage.volume_pools[].name",
				Reason: "must not be empty",
			}
		}
		if _, dup := seen[p.Name]; dup {
			return &ValidationError{
				Field:  "storage.volume_pools[" + p.Name + "]",
				Reason: "duplicate pool name",
			}
		}
		seen[p.Name] = struct{}{}
		if p.Path == "" {
			return &ValidationError{
				Field:  "storage.volume_pools[" + p.Name + "].path",
				Reason: "must not be empty",
			}
		}
		if !strings.HasPrefix(p.Path, "/") {
			return &ValidationError{
				Field:  "storage.volume_pools[" + p.Name + "].path",
				Reason: "must be an absolute path",
			}
		}
		if dataDir != "" && validation.PathsOverlap(p.Path, dataDir) {
			return &ValidationError{
				Field:  "storage.volume_pools[" + p.Name + "].path",
				Reason: "must not be inside, or contain, the node's data directory",
			}
		}
	}
	for i := range pools {
		for j := i + 1; j < len(pools); j++ {
			if validation.PathsOverlap(pools[i].Path, pools[j].Path) {
				return &ValidationError{
					Field:  "storage.volume_pools[" + pools[i].Name + "].path",
					Reason: "overlaps storage.volume_pools[" + pools[j].Name + "].path",
				}
			}
		}
	}
	return nil
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
	"token":     {},
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
	"memory":   {},
	"file":     {},
	"pebble":   {},
	"raft":     {},
	"objstore": {},
}

// validateGRPCWeb checks that an enabled server.grpc_web has the gRPC server
// it is served through. Its CORS fields are the gRPC-Web handler's to judge:
// pkg/server checks them at Start and reports them as a *ValidationError
// naming the field.
func (c *Config) validateGRPCWeb() error {
	if c.Server.GRPCWeb.Enabled && !c.Protocols.GRPC {
		return &ValidationError{Field: "server.grpc_web.enabled", Reason: "requires protocols.grpc: gRPC-Web is served through the gRPC server"}
	}
	return nil
}

// Validate checks the configuration for required fields and valid values.
// It returns the first validation error encountered, or nil if the
// configuration is valid.
func (c *Config) Validate() error {
	if err := c.validateMode(); err != nil {
		return err
	}
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
	if c.Server.SecureMemoryBudgetMiB < 0 {
		// Zero means "unconfigured, derive from RLIMIT_MEMLOCK" (cmd/qrdbd
		// resolveSecureMemoryBudget); a negative value is a
		// configuration mistake with no meaning of its own and must fail
		// closed here, at config load, rather than reach runStart and either
		// silently default or, worse, be shifted into bytes (go-qrdb#114
		// review, round 2).
		return &ValidationError{Field: "server.secure_memory_budget_mib", Reason: "must not be negative"}
	}
	if c.Server.SecureMemoryBudgetMiB > math.MaxInt64>>20 {
		// cmd/qrdbd converts this value to bytes via a left shift by 20; a
		// value this large would overflow int64 and silently install a much
		// SMALLER effective budget than configured instead of failing
		// (go-qrdb#114 review, round 2).
		return &ValidationError{Field: "server.secure_memory_budget_mib", Reason: "too large: converting to bytes would overflow"}
	}
	if err := c.validateGRPCWeb(); err != nil {
		return err
	}

	if c.Transport.MaxMessageBytes < 0 {
		// Zero means "unconfigured": ResolvedMaxMessageBytes resolves it to
		// DefaultTransportMaxMessageBytes, the same "0 derives the
		// production default" convention as
		// Server.SecureMemoryBudgetMiB. A negative value has no meaning of
		// its own and must fail closed here rather than be silently
		// resolved to a cap, which would reopen go-qrdb#576.
		return &ValidationError{Field: "transport.max_message_bytes", Reason: "must not be negative"}
	}
	if err := c.Transport.ClientRateLimit.validate(); err != nil {
		return err
	}

	if c.Raft.DataDir == "" {
		return &ValidationError{Field: "raft.data_dir", Reason: "must not be empty"}
	}
	if !c.IsStandalone() {
		if c.Raft.ShardID == 0 {
			return &ValidationError{Field: "raft.shard_id", Reason: "must be greater than 0"}
		}
		// The node's own group sits below the user range, whose IDs the
		// cluster issues to databases.
		if c.Raft.ShardID >= shard.UserMin {
			return &ValidationError{Field: "raft.shard_id",
				Reason: "must be below " + strconv.FormatUint(shard.UserMin, 10) + ": shard IDs from there up are issued to databases"}
		}
		if c.Raft.ReplicaID == 0 {
			return &ValidationError{Field: "raft.replica_id", Reason: "must be greater than 0"}
		}
		if c.Raft.RaftAddress == "" {
			return &ValidationError{Field: "raft.raft_address", Reason: "must not be empty"}
		}
	}
	if c.Raft.SMType != "" {
		if _, ok := validSMTypes[c.Raft.SMType]; !ok {
			return &ValidationError{
				Field:  "raft.sm_type",
				Reason: "must be one of: pebble, memory, mvcc, standard, concurrent, fast, disk, highperf, regular",
			}
		}
	}
	if c.Raft.SMBackend != "" {
		if _, ok := validSMBackends[c.Raft.SMBackend]; !ok {
			return &ValidationError{
				Field:  "raft.sm_backend",
				Reason: "must be one of: standard, concurrent, fast, disk",
			}
		}
	}
	if err := c.validateSMStorage(); err != nil {
		return err
	}

	if c.Storage.Backend != "" {
		if _, ok := validBackends[c.Storage.Backend]; !ok {
			return &ValidationError{Field: "storage.backend", Reason: "must be one of: pebble, memory, file, objstore"}
		}
	}
	if err := c.Storage.Objstore.validate("storage.objstore"); err != nil {
		return err
	}
	if c.Storage.CacheSize < 0 {
		return &ValidationError{Field: "storage.cache_size", Reason: "must not be negative"}
	}
	if c.Storage.MaxOpenFiles < 0 {
		return &ValidationError{Field: "storage.max_open_files", Reason: "must not be negative"}
	}
	if err := validateVolumePools(c.Storage.VolumePools, c.Raft.DataDir); err != nil {
		return err
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
			return &ValidationError{Field: "discovery.method", Reason: "must be one of: static, multicast, dns, token"}
		}
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
	checkpoint, err := c.Lease.CheckpointIntervalDuration()
	if err != nil {
		return &ValidationError{Field: "lease.checkpoint_interval", Reason: "must be a duration such as 1s: " + err.Error()}
	}
	if checkpoint <= 0 || checkpoint > MaxLeaseCheckpointInterval {
		return &ValidationError{Field: "lease.checkpoint_interval", Reason: "must be above zero and at most " + MaxLeaseCheckpointInterval.String()}
	}
	grace, err := c.Lease.FailoverGraceDuration()
	if err != nil {
		return &ValidationError{Field: "lease.failover_grace", Reason: "must be a duration such as 5s: " + err.Error()}
	}
	if grace < 0 || grace > MaxLeaseFailoverGrace {
		return &ValidationError{Field: "lease.failover_grace", Reason: "must not be negative and at most " + MaxLeaseFailoverGrace.String()}
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
	if c.Auth.IsEnabled() {
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
	if _, err := ParseInvitationTTL(c.Auth.InvitationTTL); err != nil {
		return &ValidationError{Field: "auth.invitation_ttl", Reason: "must be a positive Go duration of at most " + MaxInvitationTTL.String() + " (" + err.Error() + ")"}
	}
	if _, _, err := c.Auth.InvitationRedeemLimit.Resolved(); err != nil {
		return err
	}
	if c.Auth.RevocationCacheTTL != "" {
		d, err := parseDuration(c.Auth.RevocationCacheTTL)
		if err != nil {
			return &ValidationError{Field: "auth.revocation_cache_ttl", Reason: "must be a valid Go duration string"}
		}
		if d < 0 {
			return &ValidationError{Field: "auth.revocation_cache_ttl", Reason: "must not be negative"}
		}
	}
	if c.Auth.ABAC.Enabled && strings.TrimSpace(c.Auth.ABAC.PolicyDir) == "" {
		return &ValidationError{Field: "auth.abac.policy_dir", Reason: "must not be empty when abac is enabled"}
	}
	// An unrecognised binding posture must not reach a boot that could guess
	// at it; reject it with the rest of the configuration.
	if _, err := auth.ParseMTLSBindingPolicy(c.Auth.MTLSBindingPolicy); err != nil {
		return &ValidationError{Field: "auth.mtls_binding_policy", Reason: `must be "enrolled", "required" or empty`}
	}
	// The trust domain is minted into certificate SANs and pinned by the
	// authenticator, so it must survive the SPIFFE round trip both apply
	// (go-qrdb#519). A value that does not would make the CA refuse every
	// certificate at issue time; refuse it at load instead.
	if err := spiffe.ValidateTrustDomain(c.Auth.EffectiveClusterTrustDomain()); err != nil {
		return &ValidationError{Field: "auth.cluster_trust_domain", Reason: "must be a bare host name that round-trips through a SPIFFE ID (" + err.Error() + ")"}
	}

	// CORS validation.
	if c.CORS.MaxAge < 0 {
		return &ValidationError{Field: "cors.max_age", Reason: "must not be negative"}
	}

	// SQL validation.
	if c.SQL.MaxConnections < 0 {
		return &ValidationError{Field: "sql.max_connections", Reason: "must not be negative"}
	}
	if _, err := c.SQL.QueryTimeoutDuration(); err != nil {
		return err
	}
	if _, err := c.SQL.WaitTimeoutDuration(); err != nil {
		return err
	}
	if _, err := c.SQL.WriteTimeoutDuration(); err != nil {
		return err
	}
	if _, err := c.ReplicaForwarding.TimeoutDuration(); err != nil {
		return err
	}
	if _, err := c.Observability.SlowSyncThresholdDuration(); err != nil {
		return err
	}

	// SQL protocols require TLS and auth.
	if c.Protocols.MySQL || c.Protocols.PostgreSQL {
		if !c.TLS.Enabled {
			return &ValidationError{
				Field:  "tls.enabled",
				Reason: "TLS must be enabled when MySQL or PostgreSQL protocols are active",
			}
		}
		if !c.Auth.IsEnabled() {
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
				return &ValidationError{Field: "seal.strategy", Reason: "must be one of: passphrase, shamir, none"}
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
		if strings.TrimSpace(c.Seal.ShareTTL) != "" {
			d, err := parseDuration(c.Seal.ShareTTL)
			if err != nil {
				return &ValidationError{Field: "seal.share_ttl", Reason: "must be a valid Go duration (e.g. 15m)"}
			}
			if d <= 0 {
				return &ValidationError{Field: "seal.share_ttl", Reason: "must be greater than zero"}
			}
		}
		if _, err := c.Seal.ClusterSealTiming(); err != nil {
			return err
		}
	} else if c.Auth.IsEnabled() {
		// Auth requires the seal subsystem (go-qrdb#548): the bootstrap
		// admin's certificate binding depends on `qrdb init` being able to
		// bring up the system barrier and run the ceremony, which does not
		// exist at all when seal.enabled is false. A deployment that does
		// not want encryption runs the seal with strategy "none" --
		// identity passthrough, no ciphertext -- rather than omitting it.
		return &ValidationError{Field: "seal.enabled", Reason: `auth.enabled requires seal.enabled: true (use seal.strategy: none for no encryption)`}
	}

	// Tenant validation.
	if strings.TrimSpace(c.Tenant.QuotaReconcileInterval) != "" {
		d, err := parseDuration(c.Tenant.QuotaReconcileInterval)
		if err != nil {
			return &ValidationError{Field: "tenant.quota_reconcile_interval", Reason: "must be a valid Go duration (e.g. 5m)"}
		}
		if d <= 0 {
			return &ValidationError{Field: "tenant.quota_reconcile_interval", Reason: "must be greater than zero"}
		}
	}
	if _, ok := tenantIsolationPolicies[c.Tenant.IsolationPolicy]; !ok {
		return &ValidationError{Field: "tenant.isolation_policy", Reason: `must be "strict" or "operator-access"`}
	}

	// Database validation.
	seen := make(map[string]struct{}, len(c.Databases))
	for _, db := range c.Databases {
		if db.Name == "" {
			return &DatabaseConfigError{Name: "", Message: "name must not be empty"}
		}
		if _, ok := validDatabaseBackends[db.Backend]; !ok {
			return &DatabaseConfigError{Name: db.Name, Message: "backend must be one of: memory, file, pebble, raft, objstore"}
		}
		if db.SQL != nil {
			if _, ok := validDatabaseConcurrencies[db.SQL.Concurrency]; !ok {
				return &DatabaseConfigError{Name: db.Name, Message: "sql.concurrency must be one of: optimistic, pessimistic"}
			}
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
	if c.Bootstrap.JoinTokenFile != "" && c.Bootstrap.JoinTokenEnv != "" {
		return &ValidationError{Field: "bootstrap.join_token_file", Reason: "mutually exclusive with bootstrap.join_token_env; configure exactly one"}
	}
	// bootstrap.join_token_file/-env are deliberately NOT required here: a
	// join transport configured with neither is a valid configuration,
	// meaning "submit through the signing member's replicated approval
	// queue" rather than "present an admission secret" -- see
	// clusterenroll.BootstrapOptions.JoinToken's own doc comment and
	// docs/clustering/architecture.md's "Approval queue" section.
	if c.Bootstrap.JoinAddr != "" && c.Bootstrap.JoinKey == "" && c.Bootstrap.JoinP256Key == "" {
		return &ValidationError{Field: "bootstrap.join_key", Reason: "required when bootstrap.join_addr is set (or set bootstrap.join_p256_key)"}
	}
	if c.Bootstrap.JoinKey != "" && c.Bootstrap.JoinAddr == "" {
		return &ValidationError{Field: "bootstrap.join_addr", Reason: "required when bootstrap.join_key is set"}
	}
	if c.Bootstrap.JoinP256Key != "" && c.Bootstrap.JoinAddr == "" {
		return &ValidationError{Field: "bootstrap.join_addr", Reason: "required when bootstrap.join_p256_key is set"}
	}
	if c.Bootstrap.P256Key != "" && c.Bootstrap.NoiseAddr == "" {
		return &ValidationError{Field: "bootstrap.noise_addr", Reason: "required when bootstrap.p256_key is set"}
	}
	for _, k := range []struct{ field, value string }{
		{"bootstrap.p256_key", c.Bootstrap.P256Key},
		{"bootstrap.join_p256_key", c.Bootstrap.JoinP256Key},
	} {
		if err := validateP256PublicKeyHex(k.field, k.value); err != nil {
			return err
		}
	}
	if c.Bootstrap.JoinHTTPURL != "" && !strings.HasPrefix(c.Bootstrap.JoinHTTPURL, "https://") {
		return &ValidationError{Field: "bootstrap.join_http_url", Reason: "must use https://; the SPKI/Direct join transport carries the join token and the issued leaf under TLS only"}
	}

	// Cluster join (member-side CSR signing surface) validation.
	if c.ClusterJoin.Enabled {
		if c.ClusterJoin.ListenHost == "" {
			return &ValidationError{Field: "cluster_join.listen_host", Reason: "required when cluster_join.enabled is true"}
		}
		if c.ClusterJoin.ListenPort < 0 || c.ClusterJoin.ListenPort > 65535 {
			return &ValidationError{Field: "cluster_join.listen_port", Reason: "must be between 0 and 65535"}
		}
		// The replicated join-token store this node validates presented
		// tokens against is only reachable once the barrier is unsealed,
		// and cakey.SoftwareProvider's own IsSealed gate refuses Key A
		// itself while sealed either way -- but a deployment with seal
		// disabled entirely has no barrier state machine to ever leave
		// "sealed", which would make every sealed-state assumption in the
		// join path (and in confirmClusterCAPersisted's own) silently
		// vacuous rather than enforced. Refused at config load, not
		// discovered at the first join attempt.
		if !c.Seal.Enabled {
			return &ValidationError{Field: "cluster_join.enabled", Reason: "requires seal.enabled; member-side CSR signing has no meaning without a barrier to gate Key A behind"}
		}
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
