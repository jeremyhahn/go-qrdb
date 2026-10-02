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

package database

import (
	"regexp"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
)

// namePattern matches valid database names: alphanumeric characters, hyphens,
// and underscores only. Dots, slashes, spaces, and other special characters
// are rejected.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ---------------------------------------------------------------------------
// Engine types
// ---------------------------------------------------------------------------

// EngineType identifies the data engine used by a database.
type EngineType string

const (
	// EngineKV selects a general-purpose key-value engine.
	EngineKV EngineType = "kv"

	// EngineTimeSeries selects a time-series optimized engine.
	EngineTimeSeries EngineType = "timeseries"

	// EngineVector selects a vector-search engine.
	EngineVector EngineType = "vector"
)

// validEngines is the set of recognized engine types, used for O(1) lookup.
var validEngines = map[EngineType]struct{}{
	EngineKV:         {},
	EngineTimeSeries: {},
	EngineVector:     {},
}

// Valid reports whether the engine type is recognized.
func (e EngineType) Valid() bool {
	_, ok := validEngines[e]
	return ok
}

// ---------------------------------------------------------------------------
// Backend types
// ---------------------------------------------------------------------------

// BackendType identifies the storage backend used by a database.
type BackendType string

const (
	// BackendMemory uses an in-memory store (non-persistent).
	BackendMemory BackendType = "memory"

	// BackendFile uses a flat-file storage backend.
	BackendFile BackendType = "file"

	// BackendPebble uses PebbleDB for persistent local storage.
	BackendPebble BackendType = "pebble"

	// BackendRaft uses Raft consensus for distributed, replicated storage.
	BackendRaft BackendType = "raft"

	// BackendObjstore uses an object store (go-objstore: local, s3, gcs,
	// azure, minio, ...) as a flat key-value backend. Like BackendFile it is a
	// simple KV engine (no MVCC); it is intended for blob/backup-oriented
	// workloads, not hot consensus paths.
	BackendObjstore BackendType = "objstore"
)

// validBackends is the set of recognized backend types, used for O(1) lookup.
var validBackends = map[BackendType]struct{}{
	BackendMemory:   {},
	BackendFile:     {},
	BackendPebble:   {},
	BackendRaft:     {},
	BackendObjstore: {},
}

// Valid reports whether the backend type is recognized.
func (b BackendType) Valid() bool {
	_, ok := validBackends[b]
	return ok
}

// ---------------------------------------------------------------------------
// Database mode
// ---------------------------------------------------------------------------

// DatabaseMode identifies how a database accesses its storage layer.
type DatabaseMode string

const (
	// ModeEmbedded runs storage in-process using a local backend.
	ModeEmbedded DatabaseMode = "embedded"

	// ModeRemote proxies operations to a remote qrdb server.
	ModeRemote DatabaseMode = "remote"
)

// validModes is the set of recognized database modes, used for O(1) lookup.
var validModes = map[DatabaseMode]struct{}{
	ModeEmbedded: {},
	ModeRemote:   {},
}

// Valid reports whether the database mode is recognized.
func (m DatabaseMode) Valid() bool {
	_, ok := validModes[m]
	return ok
}

// ---------------------------------------------------------------------------
// Raft cluster mode
// ---------------------------------------------------------------------------

// RaftClusterMode identifies the Raft consensus topology for a database.
type RaftClusterMode string

const (
	// RaftNone disables Raft consensus (standalone storage).
	RaftNone RaftClusterMode = "none"

	// RaftLocal runs a local development cluster with multiple in-process replicas.
	RaftLocal RaftClusterMode = "local"

	// RaftDistributed runs a production distributed cluster across network nodes.
	RaftDistributed RaftClusterMode = "distributed"
)

// validRaftClusterModes is the set of recognized Raft cluster modes.
var validRaftClusterModes = map[RaftClusterMode]struct{}{
	RaftNone:        {},
	RaftLocal:       {},
	RaftDistributed: {},
}

// Valid reports whether the Raft cluster mode is recognized.
func (r RaftClusterMode) Valid() bool {
	_, ok := validRaftClusterModes[r]
	return ok
}

// ---------------------------------------------------------------------------
// Discovery method
// ---------------------------------------------------------------------------

// DiscoveryMethod identifies how Raft peers are discovered during cluster formation.
type DiscoveryMethod string

const (
	// DiscoveryStatic uses a fixed peer list provided at configuration time.
	DiscoveryStatic DiscoveryMethod = "static"

	// DiscoveryMulticast uses UDP multicast with HMAC-SHA256 authentication.
	DiscoveryMulticast DiscoveryMethod = "multicast"

	// DiscoveryDNS uses DNS SRV record lookup for peer discovery.
	DiscoveryDNS DiscoveryMethod = "dns"

	// DiscoveryToken uses a token-based discovery service for dynamic peer exchange.
	DiscoveryToken DiscoveryMethod = "token"
)

// validDiscoveryMethods is the set of recognized discovery methods, used for O(1) lookup.
var validDiscoveryMethods = map[DiscoveryMethod]struct{}{
	DiscoveryStatic:    {},
	DiscoveryMulticast: {},
	DiscoveryDNS:       {},
	DiscoveryToken:     {},
}

// Valid reports whether the discovery method is recognized.
func (d DiscoveryMethod) Valid() bool {
	_, ok := validDiscoveryMethods[d]
	return ok
}

// ---------------------------------------------------------------------------
// Discovery configuration structs
// ---------------------------------------------------------------------------

// StaticDiscoveryConfig holds configuration for static peer discovery.
type StaticDiscoveryConfig struct {
	Peers        []string `yaml:"peers"        json:"peers"`
	SharedSecret string   `yaml:"sharedSecret" json:"sharedSecret,omitempty"`
}

// MulticastDiscoveryConfig holds configuration for UDP multicast peer discovery.
type MulticastDiscoveryConfig struct {
	SharedSecret     string `yaml:"sharedSecret"     json:"sharedSecret"`
	MulticastGroup   string `yaml:"multicastGroup"   json:"multicastGroup,omitempty"`
	Interface        string `yaml:"interface"         json:"interface,omitempty"`
	MinPeers         int    `yaml:"minPeers"          json:"minPeers,omitempty"`
	AnnounceInterval string `yaml:"announceInterval" json:"announceInterval,omitempty"`
	DiscoverTimeout  string `yaml:"discoverTimeout"  json:"discoverTimeout,omitempty"`
}

// Validate checks that the multicast discovery config has required fields.
func (m *MulticastDiscoveryConfig) Validate() error {
	if strings.TrimSpace(m.SharedSecret) == "" {
		return &DiscoverySharedSecretRequiredError{Method: string(DiscoveryMulticast)}
	}
	return nil
}

// DNSDiscoveryConfig holds configuration for DNS SRV record peer discovery.
type DNSDiscoveryConfig struct {
	Service       string `yaml:"service"       json:"service,omitempty"`
	Proto         string `yaml:"proto"         json:"proto,omitempty"`
	Domain        string `yaml:"domain"        json:"domain"`
	LookupTimeout string `yaml:"lookupTimeout" json:"lookupTimeout,omitempty"`
}

// Validate checks that the DNS discovery config has required fields.
func (d *DNSDiscoveryConfig) Validate() error {
	if strings.TrimSpace(d.Domain) == "" {
		return &DiscoveryDomainRequiredError{}
	}
	return nil
}

// TokenDiscoveryConfig holds configuration for token-based discovery.
type TokenDiscoveryConfig struct {
	Endpoint    string `yaml:"endpoint"    json:"endpoint"`
	Token       string `yaml:"token"       json:"token"`
	ClusterSize int    `yaml:"clusterSize" json:"clusterSize"`
	Timeout     string `yaml:"timeout"     json:"timeout,omitempty"`
}

// Validate checks that the token discovery config has required fields.
func (tc *TokenDiscoveryConfig) Validate() error {
	if strings.TrimSpace(tc.Endpoint) == "" {
		return &DiscoveryEndpointRequiredError{}
	}
	if strings.TrimSpace(tc.Token) == "" {
		return &DiscoveryTokenRequiredError{}
	}
	if tc.ClusterSize < 1 {
		return &DiscoveryClusterSizeError{Size: tc.ClusterSize}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Per-database configuration structs
// ---------------------------------------------------------------------------

// DatabaseRaftConfig holds Raft consensus settings for a single database.
type DatabaseRaftConfig struct {
	ClusterMode    RaftClusterMode           `yaml:"clusterMode"    json:"clusterMode"`
	ShardID        uint64                    `yaml:"shardId"        json:"shardId,omitempty"`
	ReplicaID      uint64                    `yaml:"replicaId"      json:"replicaId,omitempty"`
	RaftAddress    string                    `yaml:"raftAddress"    json:"raftAddress,omitempty"`
	Peers          []string                  `yaml:"peers"          json:"peers,omitempty"`
	LocalNodeCount int                       `yaml:"localNodeCount" json:"localNodeCount,omitempty"`
	Discovery      DiscoveryMethod           `yaml:"discovery"      json:"discovery,omitempty"`
	Static         *StaticDiscoveryConfig    `yaml:"static"         json:"static,omitempty"`
	Multicast      *MulticastDiscoveryConfig `yaml:"multicast"      json:"multicast,omitempty"`
	DNS            *DNSDiscoveryConfig       `yaml:"dns"            json:"dns,omitempty"`
	Token          *TokenDiscoveryConfig     `yaml:"token"          json:"token,omitempty"`
}

// Validate checks that the Raft config is internally consistent.
func (r *DatabaseRaftConfig) Validate() error {
	if !r.ClusterMode.Valid() {
		return &InvalidRaftClusterModeError{Mode: string(r.ClusterMode)}
	}

	// Discovery validation only applies to distributed mode.
	if r.ClusterMode != RaftDistributed {
		return nil
	}

	// Default to static discovery when not specified.
	if r.Discovery == "" {
		r.Discovery = DiscoveryStatic
	}

	if !r.Discovery.Valid() {
		return &InvalidDiscoveryMethodError{Method: string(r.Discovery)}
	}

	// Use map-based dispatch for discovery-specific validation.
	validators := map[DiscoveryMethod]func() error{
		DiscoveryStatic: func() error {
			// Static discovery uses the top-level Peers field or the Static config.
			return nil
		},
		DiscoveryMulticast: func() error {
			if r.Multicast == nil {
				return &DiscoveryConfigRequiredError{Method: string(DiscoveryMulticast)}
			}
			return r.Multicast.Validate()
		},
		DiscoveryDNS: func() error {
			if r.DNS == nil {
				return &DiscoveryConfigRequiredError{Method: string(DiscoveryDNS)}
			}
			return r.DNS.Validate()
		},
		DiscoveryToken: func() error {
			if r.Token == nil {
				return &DiscoveryConfigRequiredError{Method: string(DiscoveryToken)}
			}
			return r.Token.Validate()
		},
	}

	if validate, ok := validators[r.Discovery]; ok {
		return validate()
	}

	return nil
}

const (
	// BarrierModePassphrase selects a single-passphrase (software KDF)
	// sealing strategy for a per-database encryption barrier. This is the
	// only strategy EnsureBarrier currently implements.
	BarrierModePassphrase = "passphrase"

	// BarrierModeShamir selects M-of-N Shamir quorum sealing for a
	// per-database encryption barrier. Accepted as a configuration value but
	// not yet implemented by EnsureBarrier, which rejects it with
	// UnsupportedBarrierStrategyError.
	BarrierModeShamir = "shamir"
)

// DatabaseBarrierConfig holds encryption barrier settings for a single database.
type DatabaseBarrierConfig struct {
	Enabled        bool   `yaml:"enabled"        json:"enabled"`
	Mode           string `yaml:"mode"           json:"mode,omitempty"`           // "passphrase" or "shamir"
	SharesRequired int    `yaml:"sharesRequired" json:"sharesRequired,omitempty"` // shamir threshold
	SharesTotal    int    `yaml:"sharesTotal"    json:"sharesTotal,omitempty"`    // shamir total shares
}

// DatabaseConnectionConfig holds connection settings for a remote database.
type DatabaseConnectionConfig struct {
	Transport string `yaml:"transport" json:"transport"` // "unix", "grpc", "rest", "quic"
	Address   string `yaml:"address"   json:"address"`
	CAFile    string `yaml:"caFile"    json:"caFile,omitempty"`
	CertFile  string `yaml:"certFile"  json:"certFile,omitempty"`
	KeyFile   string `yaml:"keyFile"   json:"keyFile,omitempty"`
}

// Validate checks that connection config has required fields.
func (c *DatabaseConnectionConfig) Validate() error {
	if strings.TrimSpace(c.Transport) == "" || strings.TrimSpace(c.Address) == "" {
		return &ConnectionRequiredError{}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Status types
// ---------------------------------------------------------------------------

// StatusType represents the operational status of a database.
type StatusType string

const (
	// StatusOpen indicates the database is open and accepting operations.
	StatusOpen StatusType = "open"

	// StatusClosed indicates the database has been closed.
	StatusClosed StatusType = "closed"

	// StatusError indicates the database is in an error state.
	StatusError StatusType = "error"

	// StatusSealed indicates the database barrier is sealed and operations are blocked.
	StatusSealed StatusType = "sealed"

	// StatusDropping is the status a database REPORTS while Manager.Drop
	// fences it ahead of reclaiming its keyspace (see Database.Dropping):
	// Manager.Get refuses every caller with DroppingError whatever the
	// underlying status is (open or sealed). It is never stored as the
	// status itself -- the fence is a separate flag, which is what lets Drop
	// keep asking whether there is an open backend to release -- and it ends
	// when the drop completes (the catalog entry is removed) or a failed
	// drop rolls the fence back so the drop can be retried.
	StatusDropping StatusType = "dropping"
)

// ---------------------------------------------------------------------------
// Protocol configuration
// ---------------------------------------------------------------------------

// ProtocolConfig controls which wire protocols are enabled for a database.
type ProtocolConfig struct {
	MySQL      bool        `yaml:"mysql"      json:"mysql"`
	PostgreSQL bool        `yaml:"postgresql" json:"postgresql"`
	Etcd       *EtcdConfig `yaml:"etcd"       json:"etcd,omitempty"`
}

// EtcdConfig holds per-database etcd wire protocol configuration.
type EtcdConfig struct {
	Enabled             bool     `yaml:"enabled"               json:"enabled"`
	ListenAddr          string   `yaml:"listen_addr"           json:"listen_addr"`
	AdvertiseClientURLs []string `yaml:"advertise_client_urls" json:"advertise_client_urls,omitempty"`
}

// etcdEngineError returns the typed refusal for enabling etcd on a database
// of engine, or nil. Disabling is always allowed.
func etcdEngineError(name string, engine EngineType, etcd *EtcdConfig) error {
	if etcd == nil || !etcd.Enabled || engine == EngineKV {
		return nil
	}
	return &EtcdEngineUnsupportedError{Name: name, Engine: engine}
}

// Validate checks that the etcd config has required fields when enabled.
func (e *EtcdConfig) Validate() error {
	if !e.Enabled {
		return nil
	}
	if strings.TrimSpace(e.ListenAddr) == "" {
		return &EtcdListenAddrRequiredError{}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SQL configuration
// ---------------------------------------------------------------------------

// Concurrency is a database's default SQL concurrency mode: whether DML in
// an SQL transaction takes row locks (pessimistic) or relies on the
// commit-time compare-and-swap alone (optimistic). SELECT ... FOR UPDATE and
// FOR SHARE take row locks in either mode. See docs/row-locking/.
type Concurrency string

const (
	// ConcurrencyOptimistic is the default: DML takes no locks, and a
	// conflicting commit fails with MySQL 1213.
	ConcurrencyOptimistic Concurrency = "optimistic"

	// ConcurrencyPessimistic makes DML take exclusive row locks, with current
	// reads, so concurrent writers of a row wait instead of failing at commit.
	ConcurrencyPessimistic Concurrency = "pessimistic"
)

// concurrencies is the set of accepted modes.
var concurrencies = map[Concurrency]bool{
	ConcurrencyOptimistic:  true,
	ConcurrencyPessimistic: true,
}

// SQLConfig holds a database's SQL settings.
type SQLConfig struct {
	// Concurrency is the database's default concurrency mode. Empty means
	// ConcurrencyOptimistic.
	Concurrency Concurrency `yaml:"concurrency" json:"concurrency,omitempty"`
}

// Validate refuses a concurrency mode that is neither empty nor one of the
// defined modes.
func (c *SQLConfig) Validate() error {
	if c.Concurrency == "" || concurrencies[c.Concurrency] {
		return nil
	}
	return &InvalidConcurrencyError{Concurrency: string(c.Concurrency)}
}

// EffectiveConcurrency returns the mode c selects: ConcurrencyOptimistic for
// a nil config or an empty mode.
func (c *SQLConfig) EffectiveConcurrency() Concurrency {
	if c == nil || c.Concurrency == "" {
		return ConcurrencyOptimistic
	}
	return c.Concurrency
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// DatabaseConfig defines the configuration for creating or opening a database.
type DatabaseConfig struct {
	TenantID     string         `yaml:"tenant_id,omitempty"     json:"tenant_id,omitempty"`
	Name         string         `yaml:"name"                    json:"name"`
	Engine       EngineType     `yaml:"engine"                  json:"engine"`
	Backend      BackendType    `yaml:"backend"                 json:"backend"`
	Protocol     ProtocolConfig `yaml:"protocol"                json:"protocol"`
	DataDir      string         `yaml:"data_dir,omitempty"      json:"data_dir,omitempty"`
	Mode         DatabaseMode   `yaml:"mode"                    json:"mode,omitempty"`
	StorageCodec string         `yaml:"storage_codec,omitempty" json:"storage_codec,omitempty"`
	// Replicas is the requested replication factor for a dedicated-raft
	// (IsolationRaft) database: the number of voting replicas to place across
	// the cluster. It is meaningful only for raft-backed databases; for other
	// backends/engines it is ignored. Unset (0) defaults to 1 for raft. The
	// actual multi-node placement is performed by a later stage; this stage
	// carries, validates, and quota-meters the value.
	Replicas int `yaml:"replicas,omitempty"      json:"replicas,omitempty"`
	// DedicatedWAL opts a dedicated-raft (IsolationRaft) database into a
	// per-tenant-KEY WAL: its Raft log is written to a dedicated WAL store
	// barrier-encrypted under the owning tenant's barrier instead of the shared
	// system-barrier WAL (4d Option C). It is meaningful only for raft-backed
	// databases and is gated by the dedicated_wal entitlement plus the
	// MaxDedicatedWALGroups quota; when either is unavailable the create falls
	// back to the shared WAL. For all other backends it is ignored.
	DedicatedWAL bool                      `yaml:"dedicated_wal,omitempty" json:"dedicated_wal,omitempty"`
	Raft         *DatabaseRaftConfig       `yaml:"raft"                    json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `yaml:"barrier"                 json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `yaml:"connection"              json:"connection,omitempty"`
	SQL          *SQLConfig                `yaml:"sql"                     json:"sql,omitempty"`
}

// Directory names a volume-isolated tenant's own Raft host keeps under
// {DataRoot}/databases, beside that tenant's per-database directories
// ({DataRoot}/databases/<name>). They are the single source for both the host
// layout and the names a database may not take.
const (
	// TenantHostWALDirName is the tenant host's WAL directory.
	TenantHostWALDirName = "wal"
	// TenantHostNodeHostDirName is the tenant host's node-host directory.
	TenantHostNodeHostDirName = "nodehost"
)

// reservedNames is the set of database names that collide with a directory
// the node keeps beside per-database directories, lower-cased so the check
// also holds on a case-insensitive filesystem.
var reservedNames = map[string]struct{}{
	TenantHostWALDirName:      {},
	TenantHostNodeHostDirName: {},
}

// IsReservedName reports whether name is reserved for node state and may not
// name a database.
func IsReservedName(name string) bool {
	_, reserved := reservedNames[strings.ToLower(name)]
	return reserved
}

// Validate checks that the configuration is complete and consistent.
// It returns a typed error when any field is missing or invalid.
//
// Empty Mode defaults to ModeEmbedded for backward compatibility.
// Remote mode requires a non-nil Connection config.
// Embedded mode requires a valid Backend.
func (c *DatabaseConfig) Validate() error {
	if c.Name == "" || !namePattern.MatchString(c.Name) {
		return &InvalidNameError{Name: c.Name}
	}
	if IsReservedName(c.Name) {
		return &ReservedNameError{Name: c.Name}
	}
	if !c.Engine.Valid() {
		return &InvalidEngineError{Engine: string(c.Engine)}
	}

	// Default empty mode to embedded for backward compatibility.
	if c.Mode == "" {
		c.Mode = ModeEmbedded
	}
	if !c.Mode.Valid() {
		return &InvalidModeError{Mode: string(c.Mode)}
	}

	if c.Mode == ModeRemote {
		if c.Connection == nil {
			return &ConnectionRequiredError{}
		}
		if err := c.Connection.Validate(); err != nil {
			return err
		}
	} else if !c.Backend.Valid() {
		// Embedded mode requires a valid backend.
		return &InvalidBackendError{Backend: string(c.Backend)}
	}

	if c.Raft != nil {
		if err := c.Raft.Validate(); err != nil {
			return err
		}
	}

	if c.Protocol.Etcd != nil {
		if err := c.Protocol.Etcd.Validate(); err != nil {
			return err
		}
		if err := etcdEngineError(c.Name, c.Engine, c.Protocol.Etcd); err != nil {
			return err
		}
	}

	if c.SQL != nil {
		if err := c.SQL.Validate(); err != nil {
			return err
		}
	}

	// Validate per-database storage codec when specified.
	if c.StorageCodec != "" {
		available := codec.AvailableCodecs()
		found := false
		for _, name := range available {
			if name == c.StorageCodec {
				found = true
				break
			}
		}
		if !found {
			return &InvalidStorageCodecError{Codec: c.StorageCodec}
		}
	}

	return nil
}

// HasDedicatedStore reports whether this database's storage is physically
// dedicated to it, as opposed to sharing the system's default store keyspace
// with every other prefix-isolated database on the node. A database gets a
// dedicated store when it is routed to its own directory -- an
// IsolationVolume tenant's DataRoot, an IsolationRaft tenant's dedicated
// shard directory, or an explicit operator-supplied DataDir -- or when it is
// backed by a dedicated Raft group (BackendRaft). A database left at the
// package-computed default path ({dataDir}/databases/{name}/data) shares the
// same physical store class as every other database created the same way and
// is not dedicated.
//
// Only a database with a dedicated store can host a cryptographically
// meaningful per-database encryption barrier: EnsureBarrier rejects enabling
// one on a non-dedicated database with SharedStoreBarrierUnsupportedError,
// since a barrier layered over shared storage would be inert state with no
// real isolation effect (the shared store's own at-rest encryption already
// governs the underlying bytes).
func (c *DatabaseConfig) HasDedicatedStore() bool {
	return c.Backend == BackendRaft || strings.TrimSpace(c.DataDir) != ""
}

// BarrierOnDataPath reports whether a per-database barrier on this database
// would wrap the store its data is served from. That holds only for a
// non-raft database with a store of its own (an explicit DataDir): the
// Manager wraps that store in a BarrierBackend and the request resolver
// serves the database from it. A backend: raft database has a dedicated
// store too (HasDedicatedStore), but its data is served from its Raft group,
// never from the Backend the Manager opens for it, so a barrier there would
// wrap an unused placeholder.
//
// The configuration alone cannot see the owning tenant's isolation mode. A
// volume- or raft-isolated tenant serves every database from the tenant's
// own store, so the service layer refuses a barrier for those tenants on top
// of this check; see BarrierOffDataPathError.
func (c *DatabaseConfig) BarrierOnDataPath() bool {
	return c.Backend != BackendRaft && strings.TrimSpace(c.DataDir) != ""
}

// ---------------------------------------------------------------------------
// Metadata
// ---------------------------------------------------------------------------

// DatabaseMeta is the on-disk metadata stored in meta.json for each database.
type DatabaseMeta struct {
	TenantID     string                    `json:"tenant_id,omitempty"`
	Name         string                    `json:"name"`
	Engine       EngineType                `json:"engine"`
	Backend      BackendType               `json:"backend"`
	Protocol     ProtocolConfig            `json:"protocol,omitempty"`
	DataDir      string                    `json:"data_dir,omitempty"`
	Mode         DatabaseMode              `json:"mode,omitempty"`
	StorageCodec string                    `json:"storage_codec,omitempty"`
	Replicas     int                       `json:"replicas,omitempty"`
	DedicatedWAL bool                      `json:"dedicated_wal,omitempty"`
	Raft         *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `json:"connection,omitempty"`
	SQL          *SQLConfig                `json:"sql,omitempty"`

	// Dropping is true from the moment Manager.Drop fences this database
	// (step 0, before it reclaims the keyspace) until the drop finishes and
	// removes this record entirely. A node that restarts while it is true
	// reopens the database fenced (it reports StatusDropping; its stored
	// status is StatusOpen), so a crash mid-drop does not silently drop the
	// fence and let a write land in a keyspace whose reclamation was
	// interrupted (go-qrdb#482). A failed drop that rolls its store fence
	// back clears it again, which is what makes the drop retryable rather
	// than stuck.
	Dropping bool `json:"dropping,omitempty"`

	// Incarnation is the database's incarnation (see
	// Database.Incarnation). Absent in metadata written before incarnations
	// existed, which reopens the database with an empty one.
	Incarnation string `json:"incarnation,omitempty"`
}

// ---------------------------------------------------------------------------
// Runtime info
// ---------------------------------------------------------------------------

// DatabaseInfo provides runtime information about a database instance.
type DatabaseInfo struct {
	TenantID     string                    `json:"tenant_id,omitempty"`
	Name         string                    `json:"name"`
	Engine       EngineType                `json:"engine"`
	Backend      BackendType               `json:"backend"`
	Status       StatusType                `json:"status"`
	Protocol     ProtocolConfig            `json:"protocol,omitempty"`
	DataDir      string                    `json:"data_dir,omitempty"`
	ShardID      uint64                    `json:"shard_id,omitempty"` // only raft mode
	Mode         DatabaseMode              `json:"mode,omitempty"`
	StorageCodec string                    `json:"storage_codec,omitempty"`
	Replicas     int                       `json:"replicas,omitempty"`      // only raft mode
	DedicatedWAL bool                      `json:"dedicated_wal,omitempty"` // only raft mode
	Raft         *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `json:"connection,omitempty"`
	SQL          *SQLConfig                `json:"sql,omitempty"`
	Compaction   *compaction.Policy        `json:"compaction,omitempty"`
}
