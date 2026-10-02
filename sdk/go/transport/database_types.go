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

package transport

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Database management types
// ---------------------------------------------------------------------------

// ProtocolConfig represents the wire protocol configuration for a database.
type ProtocolConfig struct {
	MySQL      bool                `json:"mysql"`
	PostgreSQL bool                `json:"postgresql"`
	Etcd       *EtcdProtocolConfig `json:"etcd,omitempty"`
}

// EtcdProtocolConfig represents the etcd wire protocol configuration.
type EtcdProtocolConfig struct {
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

// DatabaseInfo represents information about a database. It is a response:
// its Raft and Barrier blocks are read views that cannot hold a secret. A
// secret set on a database is write-only, reported only as a *Set flag.
type DatabaseInfo struct {
	TenantID     string                    `json:"tenantId,omitempty"`
	Name         string                    `json:"name"`
	Engine       string                    `json:"engine"`
	Backend      string                    `json:"backend"`
	Status       string                    `json:"status"`
	Protocol     ProtocolConfig            `json:"protocol,omitempty"`
	DataDir      string                    `json:"dataDir,omitempty"`
	ShardID      uint64                    `json:"shardId,omitempty"`
	Mode         string                    `json:"mode,omitempty"`
	Replicas     int                       `json:"replicas,omitempty"`
	DedicatedWAL bool                      `json:"dedicatedWal,omitempty"`
	Raft         *DatabaseRaftInfo         `json:"raft,omitempty"`
	Barrier      *DatabaseBarrierInfo      `json:"barrier,omitempty"`
	Connection   *DatabaseConnectionConfig `json:"connection,omitempty"`
	SQL          *DatabaseSQLConfig        `json:"sql,omitempty"`
}

// DatabaseRaftInfo is a database's Raft configuration as a response reports
// it. Discovery secrets are write-only: each is reported only as set.
type DatabaseRaftInfo struct {
	ClusterMode    string                  `json:"clusterMode"`
	ShardID        uint64                  `json:"shardId,omitempty"`
	ReplicaID      uint64                  `json:"replicaId,omitempty"`
	RaftAddress    string                  `json:"raftAddress,omitempty"`
	Peers          []string                `json:"peers,omitempty"`
	LocalNodeCount int                     `json:"localNodeCount,omitempty"`
	Discovery      string                  `json:"discovery,omitempty"`
	Static         *StaticDiscoveryInfo    `json:"static,omitempty"`
	Multicast      *MulticastDiscoveryInfo `json:"multicast,omitempty"`
	DNS            *DNSDiscoveryInfo       `json:"dns,omitempty"`
	Token          *TokenDiscoveryInfo     `json:"token,omitempty"`
}

// StaticDiscoveryInfo reports static peer discovery.
type StaticDiscoveryInfo struct {
	Peers []string `json:"peers,omitempty"`
	// SharedSecretSet reports that an HMAC shared secret is configured.
	SharedSecretSet bool `json:"sharedSecretSet,omitempty"`
}

// MulticastDiscoveryInfo reports UDP multicast peer discovery.
type MulticastDiscoveryInfo struct {
	// SharedSecretSet reports that the HMAC shared secret is configured.
	SharedSecretSet  bool   `json:"sharedSecretSet,omitempty"`
	MulticastGroup   string `json:"multicastGroup,omitempty"`
	Interface        string `json:"interface,omitempty"`
	MinPeers         int    `json:"minPeers,omitempty"`
	AnnounceInterval string `json:"announceInterval,omitempty"`
	DiscoverTimeout  string `json:"discoverTimeout,omitempty"`
}

// DNSDiscoveryInfo reports DNS SRV peer discovery. It holds no secret.
type DNSDiscoveryInfo struct {
	Service       string `json:"service,omitempty"`
	Proto         string `json:"proto,omitempty"`
	Domain        string `json:"domain"`
	LookupTimeout string `json:"lookupTimeout,omitempty"`
}

// TokenDiscoveryInfo reports token-based peer discovery.
type TokenDiscoveryInfo struct {
	Endpoint string `json:"endpoint"`
	// TokenSet reports that the bootstrap token is configured.
	TokenSet    bool   `json:"tokenSet,omitempty"`
	ClusterSize int    `json:"clusterSize"`
	Timeout     string `json:"timeout,omitempty"`
}

// DatabaseBarrierInfo is a database's encryption barrier configuration as a
// response reports it. The barrier passphrase is never stored, so no
// response can carry it.
type DatabaseBarrierInfo struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
}

// DatabaseSQLConfig holds a database's SQL settings. See docs/row-locking/.
type DatabaseSQLConfig struct {
	// Concurrency is "optimistic" (the default when empty) or "pessimistic".
	Concurrency string `json:"concurrency,omitempty"`
}

// UpdateDatabaseSQLRequest represents a request to update a database's SQL
// settings. A nil SQL restores the defaults.
type UpdateDatabaseSQLRequest struct {
	Name string             `json:"-"`
	SQL  *DatabaseSQLConfig `json:"sql"`
}

// DatabaseRaftConfig holds Raft consensus settings a request sets on a
// database. A response reports them as a DatabaseRaftInfo.
type DatabaseRaftConfig struct {
	ClusterMode    string   `json:"clusterMode"`
	ShardID        uint64   `json:"shardId,omitempty"`
	ReplicaID      uint64   `json:"replicaId,omitempty"`
	RaftAddress    string   `json:"raftAddress,omitempty"`
	Peers          []string `json:"peers,omitempty"`
	LocalNodeCount int      `json:"localNodeCount,omitempty"`
}

// DatabaseBarrierConfig holds encryption barrier settings for a database.
type DatabaseBarrierConfig struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`

	// Secret carries the barrier passphrase for the initial Initialize call
	// when Enabled is set on a CreateDatabaseRequest. It is never persisted
	// (the server's on-disk DatabaseBarrierConfig / meta.json has no secret
	// field) and is only meaningful on the request path; a DatabaseInfo
	// reports the barrier as a DatabaseBarrierInfo, which has no secret.
	Secret string `json:"secret,omitempty"`
}

// DatabaseConnectionConfig holds remote connection settings for a database.
type DatabaseConnectionConfig struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
	CAFile    string `json:"caFile,omitempty"`
	CertFile  string `json:"certFile,omitempty"`
	KeyFile   string `json:"keyFile,omitempty"`
}

// CreateDatabaseRequest represents a request to create a database.
type CreateDatabaseRequest struct {
	TenantID   string                    `json:"tenantId,omitempty"`
	Name       string                    `json:"name"`
	Engine     string                    `json:"engine"`
	Backend    string                    `json:"backend"`
	Protocol   ProtocolConfig            `json:"protocol,omitempty"`
	DataDir    string                    `json:"dataDir,omitempty"`
	Mode       string                    `json:"mode,omitempty"`
	Replicas   int                       `json:"replicas,omitempty"`
	Raft       *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier    *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection *DatabaseConnectionConfig `json:"connection,omitempty"`
	SQL        *DatabaseSQLConfig        `json:"sql,omitempty"`
}

// UpdateDatabaseRaftRequest represents a request to update Raft config.
type UpdateDatabaseRaftRequest struct {
	Name string              `json:"-"`
	Raft *DatabaseRaftConfig `json:"raft"`
}

// ScaleDatabaseRequest represents a request to change the replication factor
// of an existing dedicated-raft database.
type ScaleDatabaseRequest struct {
	Name           string `json:"-"`
	TargetReplicas int    `json:"target_replicas"`
}

// UpdateDatabaseBarrierRequest represents a request to update barrier config.
type UpdateDatabaseBarrierRequest struct {
	Name    string                 `json:"-"`
	Barrier *DatabaseBarrierConfig `json:"barrier"`
}

// UpdateDatabaseConnectionRequest represents a request to update connection config.
type UpdateDatabaseConnectionRequest struct {
	Name       string                    `json:"-"`
	Connection *DatabaseConnectionConfig `json:"connection"`
}

// UpdateDatabaseProtocolRequest represents a request to update the wire
// protocol configuration for a database.
type UpdateDatabaseProtocolRequest struct {
	Name       string `json:"-"`
	MySQL      bool   `json:"mysql"`
	PostgreSQL bool   `json:"postgresql"`
}

// UpdateDatabaseEtcdRequest represents a request to update the etcd wire
// protocol configuration for a database.
type UpdateDatabaseEtcdRequest struct {
	Name                string   `json:"-"`
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

// ListDatabasesResponse holds the list of databases returned by a list
// operation.
type ListDatabasesResponse struct {
	Databases []DatabaseInfo `json:"databases"`
}

// ---------------------------------------------------------------------------
// Database management operations
// ---------------------------------------------------------------------------

// DatabaseClient provides database management operations.
type DatabaseClient interface {
	// CreateDatabase creates a new database with the specified engine
	// and storage backend.
	CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseInfo, error)

	// GetDatabase returns information about a specific database.
	GetDatabase(ctx context.Context, name string) (*DatabaseInfo, error)

	// ListDatabases returns information about all databases.
	ListDatabases(ctx context.Context) ([]DatabaseInfo, error)

	// DropDatabase removes a database by name.
	DropDatabase(ctx context.Context, name string) error

	// UpdateDatabaseProtocol updates the wire protocol configuration
	// for a database and returns the updated database information.
	UpdateDatabaseProtocol(ctx context.Context, req UpdateDatabaseProtocolRequest) (*DatabaseInfo, error)

	// UpdateDatabaseEtcd updates the etcd wire protocol configuration
	// for a database.
	UpdateDatabaseEtcd(ctx context.Context, req UpdateDatabaseEtcdRequest) (*DatabaseInfo, error)

	// UpdateDatabaseRaft updates the Raft consensus configuration
	// for a database.
	UpdateDatabaseRaft(ctx context.Context, req UpdateDatabaseRaftRequest) (*DatabaseInfo, error)

	// UpdateDatabaseBarrier updates the encryption barrier configuration
	// for a database.
	UpdateDatabaseBarrier(ctx context.Context, req UpdateDatabaseBarrierRequest) (*DatabaseInfo, error)

	// ScaleDatabase changes the replication factor of an existing
	// dedicated-raft database, adding or removing voting replicas without
	// recreating the database.
	ScaleDatabase(ctx context.Context, req ScaleDatabaseRequest) (*DatabaseInfo, error)

	// UpdateDatabaseConnection updates the remote connection configuration
	// for a database.
	UpdateDatabaseConnection(ctx context.Context, req UpdateDatabaseConnectionRequest) (*DatabaseInfo, error)

	// UpdateDatabaseSQL updates a database's SQL settings -- its
	// concurrency mode -- and returns the updated database information.
	UpdateDatabaseSQL(ctx context.Context, req UpdateDatabaseSQLRequest) (*DatabaseInfo, error)

	// SealDatabase seals the encryption barrier for a database.
	SealDatabase(ctx context.Context, name string) error

	// UnsealDatabase unseals the encryption barrier for a database using the
	// provided secret.
	UnsealDatabase(ctx context.Context, name, secret string) error

	// DatabaseBarrierStatus returns the encryption-barrier status for a database.
	DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierStatus, error)
}

// DatabaseBarrierStatus reports a database barrier's sealed state and active
// strategy metadata.
type DatabaseBarrierStatus struct {
	// Sealed is true when the barrier is in sealed state.
	Sealed bool `json:"sealed"`

	// Strategy identifies the sealing strategy protecting the root key.
	Strategy string `json:"strategy"`

	// HardwareBacked is true when the active strategy uses dedicated hardware.
	HardwareBacked bool `json:"hardware_backed"`

	// InitializedAt records when the barrier was first initialized.
	InitializedAt time.Time `json:"initialized_at"`
}
