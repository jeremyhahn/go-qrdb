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
// Configuration
// ---------------------------------------------------------------------------

// DatabaseConfig defines the configuration for creating or opening a database.
type DatabaseConfig struct {
	TenantID     string                    `yaml:"tenant_id,omitempty"     json:"tenant_id,omitempty"`
	Name         string                    `yaml:"name"                    json:"name"`
	Engine       EngineType                `yaml:"engine"                  json:"engine"`
	Backend      BackendType               `yaml:"backend"                 json:"backend"`
	Protocol     ProtocolConfig            `yaml:"protocol"                json:"protocol"`
	DataDir      string                    `yaml:"data_dir,omitempty"      json:"data_dir,omitempty"`
	Mode         DatabaseMode              `yaml:"mode"                    json:"mode,omitempty"`
	StorageCodec string                    `yaml:"storage_codec,omitempty" json:"storage_codec,omitempty"`
	Raft         *DatabaseRaftConfig       `yaml:"raft"                    json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `yaml:"barrier"                 json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `yaml:"connection"              json:"connection,omitempty"`
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
	} else {
		// Embedded mode requires a valid backend.
		if !c.Backend.Valid() {
			return &InvalidBackendError{Backend: string(c.Backend)}
		}
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
	Raft         *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `json:"connection,omitempty"`
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
	Raft         *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier      *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `json:"connection,omitempty"`
}
