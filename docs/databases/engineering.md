# Databases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Database() transport.DatabaseClient
func (c *Client) DatabaseScoped(name string) *Client
```

`sdk/go/transport`:

```go
type DatabaseClient interface {
	CreateDatabase(ctx context.Context, req CreateDatabaseRequest) (*DatabaseInfo, error)
	GetDatabase(ctx context.Context, name string) (*DatabaseInfo, error)
	ListDatabases(ctx context.Context) ([]DatabaseInfo, error)
	DropDatabase(ctx context.Context, name string) error
	UpdateDatabaseProtocol(ctx context.Context, req UpdateDatabaseProtocolRequest) (*DatabaseInfo, error)
	UpdateDatabaseEtcd(ctx context.Context, req UpdateDatabaseEtcdRequest) (*DatabaseInfo, error)
	UpdateDatabaseRaft(ctx context.Context, req UpdateDatabaseRaftRequest) (*DatabaseInfo, error)
	UpdateDatabaseBarrier(ctx context.Context, req UpdateDatabaseBarrierRequest) (*DatabaseInfo, error)
	ScaleDatabase(ctx context.Context, req ScaleDatabaseRequest) (*DatabaseInfo, error)
	UpdateDatabaseConnection(ctx context.Context, req UpdateDatabaseConnectionRequest) (*DatabaseInfo, error)
	SealDatabase(ctx context.Context, name string) error
	UnsealDatabase(ctx context.Context, name, secret string) error
	DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierConfig, error)
}
```

### Types

```go
type DatabaseInfo struct {
	TenantID   string                    `json:"tenantId,omitempty"`
	Name       string                    `json:"name"`
	Engine     string                    `json:"engine"`    // kv | timeseries
	Backend    string                    `json:"backend"`   // pebble | memory | file | raft
	Status     string                    `json:"status"`
	Protocol   ProtocolConfig            `json:"protocol,omitempty"`
	DataDir    string                    `json:"dataDir,omitempty"`
	ShardID    uint64                    `json:"shardId,omitempty"`
	Mode       string                    `json:"mode,omitempty"`     // local | distributed
	Replicas   int                       `json:"replicas,omitempty"`
	Raft       *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier    *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection *DatabaseConnectionConfig `json:"connection,omitempty"`
}

type ProtocolConfig struct {
	MySQL      bool                `json:"mysql"`
	PostgreSQL bool                `json:"postgresql"`
	Etcd       *EtcdProtocolConfig `json:"etcd,omitempty"`
}
type EtcdProtocolConfig struct {
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

type DatabaseRaftConfig struct {
	ClusterMode    string   `json:"clusterMode"` // none | local | distributed
	ShardID        uint64   `json:"shardId,omitempty"`
	ReplicaID      uint64   `json:"replicaId,omitempty"`
	RaftAddress    string   `json:"raftAddress,omitempty"`
	Peers          []string `json:"peers,omitempty"`
	LocalNodeCount int      `json:"localNodeCount,omitempty"`
}

type DatabaseBarrierConfig struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`           // passphrase | shamir
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
	Secret         string `json:"secret,omitempty"`
}

type DatabaseConnectionConfig struct {
	Transport string `json:"transport"` // tcp | quic
	Address   string `json:"address"`
	CAFile    string `json:"caFile,omitempty"`
	CertFile  string `json:"certFile,omitempty"`
	KeyFile   string `json:"keyFile,omitempty"`
}

type ScaleDatabaseRequest struct {
	Name           string `json:"-"`
	TargetReplicas int    `json:"target_replicas"`
}
```

Note `Name` is `json:"-"` on the update and scale requests: it travels in the
path, not the body.

## Invariants

- **A per-database barrier requires a dedicated store.** It is permitted only on
  a database with an explicit `DataDir`, a dedicated-raft (`backend: raft`)
  database, or a database created for an IsolationVolume / IsolationRaft tenant.
  **Enabling one on a database sharing the default store is rejected**, because
  it would encrypt nothing another database could not already read.
- **Engine and backend are creation-time and immutable.** Protocol, etcd, raft
  cluster mode, barrier state, connection and replica count are all updatable;
  engine and backend are not.
- **`--cluster-mode` and `--replicas` apply to `backend: raft` only.**
  `cluster-mode` defaults to `local`, `replicas` to 1.
- **`ScaleDatabase` requires `TargetReplicas >= 1`** and works on
  dedicated-raft databases only. It adds or removes voting replicas in place —
  the database is not recreated.
- **etcd requires `engine: kv` and `backend: raft`**, plus TLS and auth enabled.
  Validation rejects any other combination at configuration time.
- **`DatabaseScoped` returns a new client** with its own immutable scope, so
  scopes never leak between clients; scoping to `system` or `""` yields an
  unscoped client. A transport with no per-database support returns the receiver
  unchanged.
- **`DropDatabase` deletes all of the database's data and is irreversible.**
- **A sealed database fails reads and writes closed** until unsealed with its
  secret.

## Permission model

Deliberately finer than one grant per verb:

| Permission | Gates |
|---|---|
| `databases:create` | creating any database |
| `databases:create_raft` | **in addition** for a raft-backed database — a dedicated Raft group is privileged provisioning |
| `databases:create_volume` | **in addition** for a volume-isolated database |
| `databases:read` | `GetDatabase` |
| `databases:list` | `ListDatabases` |
| `databases:update` | all configuration updates: barrier, connection, etcd, protocol, raft |
| `databases:scale` | changing the replication factor — distinct from `update` because it consumes quota and mutates cluster placement and Raft membership |
| `databases:delete` | dropping a database |
| `databases:seal` / `databases:unseal` | barrier custody, gated apart from configuration |
| `databases:barrier_status` | reading barrier state |

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Database name already exists | `database.AlreadyExistsError` | yes |
| Unknown database on any operation | not-found | **yes** — never implicitly created |
| Barrier enabled on a shared store | rejected | **yes** |
| `--cluster-mode` / `--replicas` on a non-raft backend | invalid-argument | **yes** |
| `TargetReplicas < 1` | invalid-argument | **yes** |
| etcd enabled on a non-`kv`/non-`raft` database | rejected at configuration time | **yes** |
| Unseal with a wrong secret | refused; the database stays sealed | **yes** |
| Operating on a sealed database | refused | **yes** |
| Missing any `databases:*` permission | permission denied | **yes** |

## Configuration

Databases are created and updated through the API rather than declared in the
daemon config file, except for those the daemon provisions at boot. See
[../configuration/](../configuration/) for the daemon-side `databases:` section
and [../barrier-seal/](../barrier-seal/) for the barrier fields.

## Persistence

Database metadata lives in the internal namespace, replicated like any other
state-machine record. A database with a `DataDir` owns that directory; one
without shares the default store.

## Test strategy

- **Unit**: lifecycle (create/get/list/drop), each update verb, the
  shared-store barrier refusal, scale bounds, the `engine`/`backend`
  immutability rule, and `DatabaseScoped` producing an independent client.
- **The shared-store barrier refusal is a security test**, not a config test —
  it is what keeps a barrier from being decorative.
- **Integration**: database parity across transports in the standalone stage;
  `scale` and dedicated-raft creation belong to the cluster stage, which is
  **not currently wired into CI**.

## Observability

- `DatabaseInfo.Status` is the health signal per database.
- `DatabaseBarrierStatus` reports seal state, mode and share counts.
- Create, drop, scale, seal and unseal are audited through
  [../audit/](../audit/).
