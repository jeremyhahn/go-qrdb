# Databases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11

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

- **A per-database barrier must be on the data path.** It is permitted only on
  a non-raft database with an explicit `DataDir`
  (`DatabaseConfig.BarrierOnDataPath`), and the tenant-scoped create path never
  takes `DataDir` from the caller, so only the system tenant can create one.
  Everything else is rejected: a database sharing the default store with
  `database.SharedStoreBarrierUnsupportedError`; a `backend: raft` database, and
  any database of an IsolationVolume / IsolationRaft tenant, with
  `database.BarrierOffDataPathError` naming the store the data is really served
  from. The volume/raft-tenant refusal is made in
  `DatabaseService.CreateForTenantWithCredentials` before any quota
  reservation, because the configuration alone cannot see the tenant's
  isolation mode. A barrier-enabled record written before the refusal still
  opens — sealed, with a warning — so it stays fail-closed rather than
  dropping out of the catalog. `database.Manager.Open` warns for a raft
  database's record; a volume- or raft-isolated tenant's record is warned for
  at boot by `service.WarnBarriersOffDataPath` once the tenant service exists,
  because the manager cannot see tenant isolation. Both log
  `database.BarrierOffDataPathWarning`.
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
  All of it means the keyspace as well as the directory — see
  [architecture.md](architecture.md) "What a drop reclaims" for the shapes and
  the order. Four properties follow and are tested as such:
  - **Everything it deletes is scoped by tenant.** Every prefix and every index
    name the reclamation is given is rooted at the composite `<tenant>/<db>/`
    namespace. A drop is reachable by any tenant that owns a database and it
    runs against a shared store, so a prefix carrying a database and no tenant
    is one tenant's lever on another's data. Pre-tenant-namespace collection
    records are the one shape that looks database-scoped, and they are reclaimed
    by nothing — see architecture.md and
    [../collections/engineering.md](../collections/engineering.md).
  - **The reclamation runs before the catalog record is removed**, so an
    interrupted drop leaves a live, empty database rather than an unreachable
    keyspace, and re-issuing the drop finishes it.
  - **The reclamation is idempotent.** An already-empty keyspace is success,
    not a not-found error.
  - **A reclamation failure fails the drop.** Nothing is removed, and the
    drop is retried. A drop whose store cannot be resolved deletes nothing at
    all, because the resolution is the first thing it does.
- **A second concurrent drop of the same database is refused**, not merged.
  Exactly one caller may report success, because the tenant-scoped drop
  releases quota on success.
- **A sealed database fails reads and writes closed** until unsealed with its
  secret. The service registry refuses every accessor — KV, MVCC, watch, lease,
  index, time series, vector, transaction, batch, table, sequence, privilege,
  lock and collection — with `service.BarrierSealedError{Database: <name>}` for
  a database the caller owns whose status is `sealed`. Another tenant naming it
  gets the ordinary not-found answer. **The refusal is enforced per node.** A
  database's barrier and its `sealed` status live on the node that sealed it,
  not in replicated state: in a cluster, sealing a database on one node does
  not seal it on the others, and a node on which it is unsealed keeps serving
  it. Seal it on every node that serves it.
- **Every local backend offers the guarded writes; only the remote proxy does
  not.** `PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete` are available on
  a database backed by `memory`, `file` or `pebble` — with or without a
  per-database barrier over it — as well as on `raft`. The capability is
  discovered by asserting the backend to `database.AtomicBackend` rather than
  being part of `database.Backend`, because the remote proxy cannot honour it
  until its wire protocol carries the verbs; it refuses with
  `database.ErrAtomicWriteUnsupported` instead of emulating them with a read
  and a write.
- **A local backend's guarded write is atomic against every other write on the
  same backend**, unguarded ones included. The read and the write are one
  critical section, and `Put`, `Delete` and `Batch` enter the same one, so no
  write can land between a guarded operation's observation and its decision. A
  guarantee that held only while callers cooperated would be advisory, which is
  worse than the refusal above — a refusal is at least honest about what the
  caller did not get.
- **`Batch` is still all-or-nothing, and for the same reason as before.** The
  property is the storage engine's batch commit; serializing batches against
  the guarded verbs adds exclusion in the other direction (a batch cannot
  commit inside a guarded operation's window) and takes nothing away.

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
| Barrier enabled on a shared store | `database.SharedStoreBarrierUnsupportedError` | **yes** |
| Barrier enabled where the data is served from another store (raft backend, volume/raft-isolated tenant) | `database.BarrierOffDataPathError` | **yes** |
| `--cluster-mode` / `--replicas` on a non-raft backend | invalid-argument | **yes** |
| `TargetReplicas < 1` | invalid-argument | **yes** |
| etcd enabled on a non-`kv`/non-`raft` database | rejected at configuration time | **yes** |
| Unseal with a wrong secret | refused; the database stays sealed | **yes** |
| Operating on a sealed database | `service.BarrierSealedError` naming the database, on every engine | **yes** |
| The store holding a database's keys is unresolvable | `service.ReclaimStoreUnavailableError` (503 / `UNAVAILABLE`, whatever the resolver reported); the drop fails and nothing is removed | **yes** |
| A key, index or vector namespace cannot be reclaimed | `database.KeyspaceReclaimError` wrapping the cause; the drop fails and nothing is removed | **yes** |
| A drop is already in flight for the database | `database.DropInProgressError` | **yes** |
| Missing any `databases:*` permission | permission denied | **yes** |

## Configuration

Databases are created and updated through the API rather than declared in the
daemon config file, except for those the daemon provisions at boot. See
[../configuration/](../configuration/) for the daemon-side `databases:` section
and [../barrier-seal/](../barrier-seal/) for the barrier fields.

## Persistence

Database metadata lives in the internal namespace, replicated like any other
state-machine record. A database with a `DataDir` owns that directory; one
without shares the default store — and for a shared-store database the
directory holds configuration only, which is why its teardown cannot be a
directory removal.

The seam that reclaims the keyspace is `database.KeyspaceReclaimer`,
implemented by `service.KeyspaceReclaimService` and wired into the database
manager where the manager and the data-plane engines first meet. It enumerates
through `KVServicer.ListAll`, the unfiltered listing, rather than `List` —
see [architecture.md](architecture.md) for why a teardown must not read
through the discovery guard. A composer
serving volume-isolated tenants additionally attaches a
`service.StoreResolver`, which routes those tenants' keys to their dedicated
per-tenant Host instead of the system store.

**The same service also implements `tenant.KeyspaceReclaimer`**, the
tenant-scoped peer used by `DeleteTenant`. A database's keyspace and a
tenant's are the same shapes of key over different scopes — the tenant scope
is one segment shorter and adds the placement range — so there is one walk,
not two. See
[../multi-tenancy/architecture.md](../multi-tenancy/architecture.md) for the
tenant scope and for why deleting a tenant refuses while it still owns
databases.

`Manager.TenantDatabases` is the catalog that refusal reads. It reports what
**this node's** catalog holds: a database's `meta.json` is node-local while
its keys are in the replicated store, which is why the tenant reclamation
scans the tenant's whole namespace rather than trusting the list to have
bounded it.

## Test strategy

- **Unit**: lifecycle (create/get/list/drop), **what a drop reclaims and in
  what order** (every key shape, the index-name shape, vector namespaces,
  idempotence, retry after a failed reclamation, and the refusal of an
  unscoped request), **what a drop must NOT reclaim** (two tenants with a
  database of the same name and unattributable pre-namespace collection
  records: one tenant's drop leaves the other's records and its own
  namespaced data is still reclaimed — both halves, because a fix that stops
  deleting everything by stopping the legitimate reclamation is not a fix),
  each update verb, the
  shared-store barrier refusal, scale bounds, the `engine`/`backend`
  immutability rule, and `DatabaseScoped` producing an independent client.
- **The shared-store barrier refusal is a security test**, not a config test —
  it is what keeps a barrier from being decorative.
- **The reclamation has a test against a real engine and a real key/value
  service**, not only against in-memory doubles, and it probes residue by
  exact key rather than by listing. Both properties are load bearing: a double
  that skipped the discovery guard, and an assertion that listed through it,
  each independently hide a teardown that deletes nothing. Doubles standing in
  for the key/value service apply the guard for the same reason.
- **Integration**: database parity across transports in the standalone stage;
  `scale` and dedicated-raft creation belong to the cluster stage, which is
  **not currently wired into CI**.

## Observability

- `DatabaseInfo.Status` is the health signal per database.
- `DatabaseBarrierStatus` reports seal state, mode and share counts.
- Create, drop, scale, seal and unseal are audited through
  [../audit/](../audit/).
