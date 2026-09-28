# Databases — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> Sections headed **Target** are the requirements contract for the multi-model
> database program described in [architecture.md](architecture.md). They are
> pending owner review and are not implemented. Every other section describes
> the shipped code. Open decisions are referenced as OD-n and listed at the end
> of architecture.md.

## Exported surface

`sdk/go`:

```go
func (c *Client) Database() transport.DatabaseClient
func (c *Client) DatabaseScoped(name string) (*Client, error)
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

`UpdateDatabaseConnection` is removed if OD-9 is ruled A.

### Types (target)

The `Engine` field is gone from every type below. `Binding` is new and
read-only; it reports what the resolver decided, so an operator can see where a
database's data lives without reading the server's configuration.

```go
type DatabaseInfo struct {
	TenantID   string                    `json:"tenantId,omitempty"`
	Name       string                    `json:"name"`
	Backend    string                    `json:"backend"` // memory | file | pebble | raft | objstore
	Status     string                    `json:"status"`
	Protocol   ProtocolConfig            `json:"protocol,omitempty"`
	DataDir    string                    `json:"dataDir,omitempty"`
	ShardID    uint64                    `json:"shardId,omitempty"`
	Mode       string                    `json:"mode,omitempty"`
	Replicas   int                       `json:"replicas,omitempty"`
	Raft       *DatabaseRaftConfig       `json:"raft,omitempty"`
	Barrier    *DatabaseBarrierConfig    `json:"barrier,omitempty"`
	Connection *DatabaseConnectionConfig `json:"connection,omitempty"` // removed if OD-9 = A
	Binding    *DatabaseBinding          `json:"binding,omitempty"`    // new, read-only
}

// DatabaseBinding is the resolved placement of the database's storage.
type DatabaseBinding struct {
	Kind       string   `json:"kind"`            // local | raft
	Nodes      []string `json:"nodes,omitempty"` // owning node (local) or replica nodes (raft)
	Encryption string   `json:"encryption"`      // node | tenant | tenant+database
	Generation uint64   `json:"generation"`      // increments on every create of this name
}

type DatabaseRaftConfig struct {
	ClusterMode    string   `json:"clusterMode"` // none | local | distributed
	ShardID        uint64   `json:"shardId,omitempty"`
	ReplicaID      uint64   `json:"replicaId,omitempty"`
	RaftAddress    string   `json:"raftAddress,omitempty"`
	Peers          []string `json:"peers,omitempty"`
	LocalNodeCount int      `json:"localNodeCount,omitempty"`
	StateStorage   string   `json:"stateStorage,omitempty"` // new (OD-8 A): pebble | file | memory
}
```

`ProtocolConfig`, `EtcdProtocolConfig`, `DatabaseBarrierConfig`,
`DatabaseConnectionConfig` and `ScaleDatabaseRequest` are unchanged. `Name` is
`json:"-"` on the update and scale requests: it travels in the path, not the
body.

### Server-side types (target)

`pkg/database`:

```go
// BindingKind is the kind of host a database's writes are applied by.
type BindingKind uint8

const (
	BindingLocal BindingKind = iota + 1 // engine-direct host over the database's own store
	BindingRaft                         // the database's Raft group
)

// TenantPlacement is what ResolveBinding needs to know about the owning tenant.
// It is filled by the server from tenant.TenantInfo so pkg/database does not
// import pkg/tenant.
type TenantPlacement struct {
	TenantID   string
	Isolation  string // prefix | barrier | volume | raft
	DataRoot   string // empty for prefix and barrier tenants
	HasBarrier bool
}

// NodePlacement is what ResolveBinding needs to know about the node.
type NodePlacement struct {
	RunMode          string // raft | standalone
	DataDir          string
	MultiNode        bool
	Defaults         Defaults
	ObjstoreProfiles map[string]struct{} // configured provider profiles
}

// Defaults fills fields a create left empty. Applied once, at creation, and
// written into the record; never consulted again for that database.
type Defaults struct {
	Backend      BackendType
	Replicas     int
	ClusterMode  RaftClusterMode
	StateStorage string
}

// EncryptionOwner names whose key encrypts a binding's store at rest.
type EncryptionOwner uint8

const (
	EncryptNode   EncryptionOwner = iota + 1 // prefix tenants: the node at-rest encryptor
	EncryptTenant                            // barrier, volume and raft tenants: the tenant barrier
)

// Binding is the resolved, concrete placement of one (tenant, database).
type Binding struct {
	TenantID        string
	Database        string
	Generation      uint64
	Backend         BackendType
	Kind            BindingKind
	DataRoot        string // every engine's files live under this directory
	ShardID         uint64 // BindingRaft only
	StateStorage    string // BindingRaft only: the state machine's local store
	Encryption      EncryptionOwner
	DatabaseBarrier bool // values additionally encrypted under the database's barrier (OD-6 A)
}

// ResolveBinding is pure: no I/O. Every refusal is a typed error.
func ResolveBinding(meta DatabaseMeta, tenant TenantPlacement, node NodePlacement) (Binding, error)
```

`pkg/server` materializes a `Binding` into an open store, a `service.Host`
(`backend.LocalBackend` for `BindingLocal`, the Raft shard for `BindingRaft`),
a watch hub and a lease driver, then calls the **one** bundle builder —
today's `buildDataPlaneServices`, extended with the vector service — over that
host. The bundle is cached per `(tenant, database, generation)`.
`buildDedicatedStoreServices`, `service.NewNamedBackendKVService`'s data-plane
use, the in-memory placeholder in `openStorage` and the node-local vector store
in `pkg/server/lifecycle` are deleted, not kept beside the new path.

## Invariants

### Shipped

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
- **Names that collide with node state are reserved.** A volume-isolated
  tenant's databases live at `{DataRoot}/databases/<name>`, beside the
  tenant's own Raft host, whose WAL and node-host state are
  `{DataRoot}/databases/wal` and `{DataRoot}/databases/nodehost`. Those names
  (`database.TenantHostWALDirName`, `database.TenantHostNodeHostDirName`,
  matched case-insensitively) are refused for every database by
  `DatabaseConfig.Validate` with `database.ReservedNameError`, so no create
  path can place a database inside that state.
- **Engine and backend are creation-time and immutable.** Protocol, etcd, raft
  cluster mode, barrier state, connection and replica count are all updatable;
  engine and backend are not.
- **`--cluster-mode` and `--replicas` apply to `backend: raft` only.**
  `cluster-mode` defaults to `local`, `replicas` to 1.
- **`ScaleDatabase` requires `TargetReplicas >= 1`** and works on
  dedicated-raft databases only. It adds or removes voting replicas in place —
  the database is not recreated.
- **etcd requires `engine: kv`**, a node in mode `raft`, and TLS and auth
  enabled. Another engine is refused at create and on update
  (`database.EtcdEngineUnsupportedError`); a standalone node's listener refuses
  to start (MVCC, leases and watches are raft-only). Any backend serves it.
  *(Corrected: `backend: raft` was documented as required and was neither required nor checked -- the listener serves any backend through the service registry -- and `engine: kv` was documented as validated and was not.)*
- **`DatabaseScoped` returns a new client** with its own immutable scope, so
  scopes never leak between clients; scoping to `system` or `""` yields the
  receiver. A caller that names a database is never handed the default one: a transport
  that cannot confine its data plane to the database refuses with
  `transport.DatabaseScopeUnsupportedError`. The embedded transport scopes
  through the node's per-(tenant, database) service registry
  (`embedded.WithServiceRegistry`, wired by `sdk/go/node` to the running
  server's registry, which the node composes over its own database catalog as
  `qrdbd` does), resolving each data-plane call for the
  context's tenant as the network transports' server does per request; without
  a registry every scoped call is refused. Time-series retention and metric
  listing, whose service methods take no context, are resolved by the
  transport for the caller's tenant; the scoped service refuses them rather
  than guess a tenant. The per-family adapters are generated from the
  `pkg/service` interfaces (`sdk/go/transport/embedded/internal/scopegen`, run
  by `go generate`), and CI's `make check-generated` fails while the committed
  file is stale. A scoped transport serves exactly the families its
  unscoped transport serves: on a standalone node, which serves no MVCC,
  leases or watches, its scoped clients refuse them too. Its SQL query resolves the named database through the
  same registry, on a scoped and an unscoped transport alike, so it reads the
  database's own store and a sealed database is refused.
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
  `kvstore.ErrAtomicWriteUnsupported` instead of emulating them with a read
  and a write.
- **A local backend's guarded write is atomic against every other write on the
  same backend**, unguarded ones included. The read and the write are one
  critical section, and `Put`, `Delete` and `Batch` enter the same one, so no
  write can land between a guarded operation's observation and its decision.
- **`Batch` is all-or-nothing** on memory, file and pebble. It is best effort on
  objstore, where a mid-commit failure may leave a partial result.

### Target

- **One binding per database, and every engine is built over it.** For every
  `(tenant, database)` the service bundle has a non-nil servicer for every
  compiled-in engine — KV, MVCC, batch, transactions, leases, watches, indexes,
  time series, vector, collections — and for every service derived from them
  (sequences, the relational catalog, privileges, locks). A nil servicer for a
  compiled-in engine is a defect, not a capability statement.
- **The recorded backend is the store.** No node flag, configuration entry, SQL
  statement or boot path substitutes a backend for the one in the record.
- **Defaults are applied once.** A field left empty at creation is filled from
  the node's `database_defaults` and persisted; resolution never reads the
  defaults for an existing database. Changing a node default changes new
  databases only.
- **Every write is applied through the binding's host, inside the engine's apply
  batch.** No engine writes to its store outside the batch: time-series points,
  metric and tag indexes, secondary index entries, vector records and vector
  metadata indexes are all batch writes, committed atomically with the entry
  that produced them, on every backend.
- **Every write carries the drop fence.** A write is a guarded command whose
  guard is "this database's fence key is absent", evaluated at apply time on
  every replica. This holds for every engine and for local bindings as well as
  raft ones.
- **Every engine uses the binding's encryptor and data root.** An engine that
  obtains an encryptor or a directory from anywhere else — the node's
  `Encryptor`, the node's `DataDir` — is a tenant-isolation defect.
- **Every storage backend's `Batch` is all-or-nothing after recovery**,
  objstore included (commit journal), because the apply batch depends on it.
- **The composite `<tenant>/<db>/` prefix is not applied** to a database with
  its own store; its keys are at the root of that store. The prefix remains only
  on the control-plane store.
- **Resolution failures are typed and fail closed.** A binding that cannot be
  resolved or materialized is an error to the caller; it never falls back to the
  control-plane store or to another backend.

## Permission model

The two provisioning permissions are enforced where both the request's
backend and the tenant's isolation tier are known: the database service every
network transport (REST, gRPC, Unix, QUIC, MCP) and both SQL wire listeners
create through (`service.NewProvisioningAuthorizedDatabaseService`, composed by
the server whenever authentication is on). The transports' entry check can
check only the route's `databases:create`. The re-check runs against the
caller's tenant, charges no second rate-limit token and records only a
refusal; a refusal is `service.ProvisioningDeniedError`, answered as its
cause's class (`403` / `PERMISSION_DENIED` for a missing grant, `503` /
`UNAVAILABLE` for an audit sink that could not record it). A tenant whose tier
cannot be read is refused. *(Corrected: neither permission was checked
anywhere before this release.)*

Deliberately finer than one grant per verb:

| Permission | Gates |
|---|---|
| `databases:create` | creating any database — including SQL `CREATE DATABASE`, which creates a database owned by the session's tenant, inside its quota |
| `databases:create_raft` | **in addition** for a raft-backed database — `backend: raft`, or any database of a raft-isolated tenant — because a dedicated Raft group is privileged provisioning |
| `databases:create_volume` | **in addition** for a volume-isolated database — any database of a volume-isolated tenant |
| `databases:read` | `GetDatabase` |
| `databases:list` | `ListDatabases` |
| `databases:update` | all configuration updates: barrier, connection, etcd, protocol, raft |
| `databases:scale` | changing the replication factor — distinct from `update` because it consumes quota and mutates cluster placement and Raft membership |
| `databases:delete` | dropping a database — including SQL `DROP DATABASE`, which may drop only a database the session's tenant owns |
| `databases:seal` / `databases:unseal` | barrier custody, gated apart from configuration |
| `databases:barrier_status` | reading barrier state |

Unchanged by the target design. A create that takes `backend: raft` from the
node defaults (OD-3 A) is checked for `databases:create_raft` exactly as an
explicit one is: the check is on the resolved record, not on the request.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Database name already exists | `database.AlreadyExistsError` | yes |
| Database name reserved for node state (`wal`, `nodehost`, any case) | `database.ReservedNameError`, before any directory is made | **yes** |
| Unknown database on any operation | not-found | **yes** — never implicitly created |
| Another tenant's database on any tenant-scoped operation | the same `database.NotFoundError` as a missing one | **yes** — no access denial that would reveal the name is held |
| Barrier enabled on a shared store (shipped) | `database.SharedStoreBarrierUnsupportedError` | **yes** |
| Barrier enabled where the data is served from another store (raft backend, volume/raft-isolated tenant) (shipped) | `database.BarrierOffDataPathError` | **yes** |
| `--cluster-mode` / `--replicas` on a non-raft backend (shipped: `replicas` is silently zeroed) | target: `database.RaftOptionsRequireRaftBackendError` | **yes** |
| `TargetReplicas < 1` | invalid-argument | **yes** |
| etcd enabled on a non-`kv` database | `database.EtcdEngineUnsupportedError` at create and on update *(corrected: documented as non-`kv`/non-`raft`, "at configuration time"; nothing checked it, and any backend serves etcd)* | **yes** |
| Unseal with a wrong secret | refused; the database stays sealed | **yes** |
| Operating on a sealed database | `service.BarrierSealedError` naming the database, on every engine | **yes** |
| The store holding a database's keys is unresolvable | `service.ReclaimStoreUnavailableError` (503 / `UNAVAILABLE`, whatever the resolver reported); the drop fails and nothing is removed | **yes** |
| A key, index or vector namespace cannot be reclaimed | `database.KeyspaceReclaimError` wrapping the cause; the drop fails and nothing is removed | **yes** |
| A drop is already in flight for the database | `database.DropInProgressError` | **yes** |
| Missing any `databases:*` permission | permission denied | **yes** |
| Target: `backend: raft` on a standalone node | `database.RaftBackendUnavailableError` | **yes** |
| Target: barrier on `backend: memory` | `database.BarrierRequiresPersistentBackendError` | **yes** |
| Target: etcd on a non-raft backend | `database.EtcdRequiresRaftBackendError` | **yes** |
| Target: `data_dir` outside a volume/raft tenant's root | `database.DataDirOutsideTenantRootError` | **yes** |
| Target: `file`/`objstore` for a volume/raft tenant (OD-7 B) | `database.BackendIsolationConflictError` | **yes** |
| Target: non-replicated backend in a multi-node cluster (OD-2 B) | `database.LocalBackendInClusterError` | **yes** |
| Target: a binding cannot be materialized (store will not open, shard will not start) | `database.BindingUnavailableError` wrapping the cause; 503 / `UNAVAILABLE` | **yes** |
| Target: a write lands after the drop fence | the guard refusal, surfaced to the writer as the drop-fence contract defines it | **yes** |
| Target: a create request carrying `engine` (OD-13 A) | unknown-field invalid-argument | **yes** |

Every new error is a struct type in `pkg/database/errors.go` with the fields
that identify the database and the conflicting setting, mapped once in each
transport's error table.

## Configuration

Databases are created and updated through the API; the daemon's `databases:`
section provisions a list at boot, each entry with its own settings.

### Target: node defaults

```yaml
database_defaults:
  backend: raft            # OD-3 A: raft in raft mode, pebble in standalone, when unset
  replicas: 3              # raft only
  raft:
    cluster_mode: distributed
    state_storage: pebble  # OD-8 A

databases:                 # provisioned at boot; each entry is a full create
  - name: app
    backend: pebble
  - name: metrics
    backend: raft
    replicas: 3
```

`databases[].engine` is removed (OD-13 decides whether a stale key is refused
or ignored). The node's control-plane store is configured where it is today —
`raft.sm_type` / `raft.sm_storage` in raft mode, `storage.backend` in
standalone mode — and holds no database other than the system tenant's
`system` database.

See [../configuration/](../configuration/) and
[../barrier-seal/](../barrier-seal/).

## Persistence

### Shipped

Database metadata is `meta.json` under the database's configuration directory,
node-local. Dedicated-raft databases additionally have a replicated placement
record, which is their existence authority across nodes. A database with a
`DataDir` owns that directory; one without shares the default store — and for a
shared-store database the directory holds configuration only, which is why its
teardown cannot be a directory removal.

The seam that reclaims the keyspace is `database.KeyspaceReclaimer`,
implemented by `service.KeyspaceReclaimService` and wired into the database
manager where the manager and the data-plane engines first meet. It enumerates
through `KVServicer.ListAll`, the unfiltered listing, rather than `List` —
see [architecture.md](architecture.md) for why a teardown must not read
through the discovery guard. A composer serving volume-isolated tenants
additionally attaches a `service.StoreResolver`, which routes those tenants'
keys to their dedicated per-tenant Host instead of the system store.

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

### Target: the catalog record

Under OD-4 A, the replicated catalog record — the placement table extended to
every backend — carries the full database record: backend, raft options with
the resolved state storage, replicas, data directory, barrier settings,
protocols, the owning node for a local binding, and the generation. `meta.json`
becomes a node-local cache rebuilt from it at boot.

### Target: removing `engine` from persisted metadata

- `DatabaseMeta` and `DatabaseConfig` lose `Engine`; `database.EngineType`,
  its constants and `database.InvalidEngineError` are deleted.
- A `meta.json` written by an earlier version still carries `"engine"`. JSON
  decoding ignores it; the manager rewrites the file without it on the first
  open that finds it (atomic write-and-rename, as `writeMeta` does today), and
  logs the rewrite once per database. The rewrite is idempotent and needs no
  version marker: a file without the key is already migrated.
- A database created with `engine: timeseries` or `engine: vector` becomes an
  ordinary multi-model database. Its time-series or vector data is where the
  data migration below finds it; nothing about it depended on the engine field
  except which SQL virtual tables it exposed (OD-12).
- The protobuf fields numbered 2 in `DatabaseCreateRequest` and
  `DatabaseInfoResponse` become `reserved 2; reserved "engine";`.

## Target: migrating existing data

Pre-binding data lives in three places. What happens to each depends on OD-10
and OD-11; the mechanics below hold under the recommended rulings.

| Where it lives today | What it holds | Target |
|---|---|---|
| The shared system shard (raft mode) or the node's single local store (standalone), under `<tenant>/<db>/` and inside the `ts:` / `idx:metric:` / `retention:` prefixes | KV, MVCC, SQL catalog and rows, sequences and generation keys, collections, secondary index entries, time-series points and metric index and retention | relocated per database into its binding (OD-10) |
| A volume tenant's shared shard, under `<tenant>/<db>/` | the same | relocated per database into its binding (OD-10) |
| The node-local vector store at `{DataDir}/vector` | every tenant's vector namespaces, records and metadata | imported per `(tenant, database)` (OD-11) |

**Relocation of one database** (OD-10 A):

1. Resolve the database's target binding and materialize it empty.
2. Write the database's **relocation fence** in the source store. Writes to the
   database from this point are refused with a retryable unavailable error;
   reads continue from the source.
3. Copy through the unfiltered enumeration every key under `<tenant>/<db>/`,
   stripping the prefix, and every time-series key whose metric carries the
   `<tenant>/<db>/` namespace, stripping it from the metric. Copy in batches
   through the target binding's host, so a raft target replicates the copy.
4. **Rebuild derived state in the target** rather than copying it: the
   time-series tag index is rebuilt from the copied points, because the source
   tag index carries no metric and cannot be attributed; secondary indexes are
   rebuilt by the index backfill, because the source Pebble index entries live
   outside the keyspace; vector indexes are rebuilt from records.
5. Switch the catalog record to the new binding and bump nothing else — the
   generation is unchanged, because this is the same database.
6. Reclaim the source keyspace through the shipped reclamation walk.
7. Remove the relocation fence.

A crash before step 5 leaves the source authoritative and the target discarded
and rebuilt by the retried job; a crash after step 5 leaves only reclamation
work, which is idempotent. Collection records written before the collection
surface was tenant-scoped are not relocated, for the reason
[architecture.md](architecture.md) gives for not reclaiming them.

**Vector import** (OD-11 C): `qrdb admin migrate vectors --data-dir <dir>
--on-conflict fail|newest|skip`, run against a stopped node's store directory
or a running node's local store. For each physical namespace it decodes the
`<tenant>/<db>/` prefix and the tenant scope, resolves the target database, and
proposes namespace creates and batch upserts through that database's binding,
preserving IDs, metadata and timestamps. A namespace whose prefix names no
existing database is reported and skipped, never guessed. Conflicts are
records whose ID already exists in the target with different content.

## Target: phased implementation plan

Each lane has one seam it owns, touches one feature folder set, and lands with
its acceptance tests. Lanes in the same phase are independent. No lane starts
on a question that is still an open decision it depends on.

### Phase 1 — foundation (parallel)

**Lane 1A — storage-generic engine.** Seam: `storage.Storage` and the apply
`storage.Batch`. Owns `pkg/engine`, `pkg/timeseries`, `pkg/index`,
`pkg/storage/objstore`.
- Remove `*pebble.DB` from `MVCCEngine`: secondary indexes become the
  storage-backed index writing through the apply batch; time series reads
  through `storage.Reader` and writes through the apply batch.
- Give the objstore backend a commit journal with open-time replay, matching the
  file backend.
- Compose `MVCCEngine` for file and objstore; `KVEngine` is no longer composed
  for any database.
- Acceptance: (1) an engine conformance suite — KV, MVCC, transactions, CAS,
  leases, watches, secondary indexes, collections, a relational smoke test, time
  series — parametrized over memory, file, pebble and local-provider objstore;
  (2) a time-series write and a KV write in one entry whose batch commit is
  forced to fail leave neither; (3) an objstore commit interrupted after the
  journal is durable is replayed in full at open, and one interrupted before is
  absent; (4) a build-gate test that `pkg/engine` and `pkg/timeseries` do not
  import Pebble.

**Lane 1B — vector engine.** Seam: the engine apply-handler signature and the
batch. Owns `pkg/vector`, the vector parts of `pkg/service`, `pkg/protocol`
vector opcodes, `pkg/statemachine` vector code (deleted). Specified in
[../vectors/engineering.md](../vectors/engineering.md).
- Acceptance: listed there. It can develop against the pebble store while 1A
  generalizes the engine; it must not write outside the batch it is given.

**Lane 1C — remove `engine`.** Seam: the database types and every transport's
database handler. Owns the proto, `pkg/api/{rest,grpc,quic,mcp}`, `cmd/qrdb`
database verbs, `pkg/config` database section, `sdk/go` types and transports,
the SQL driver DSN, `pkg/sql/provider` engine tables (OD-12, OD-13).
- Acceptance: a create carrying `engine` behaves per OD-13 on all six
  transports; an old `meta.json` with `engine` opens and is rewritten without
  it; the database parity suite passes with no `engine` in any fixture; the
  proto field is reserved.

### Phase 2 — bindings (after 1A; 2A and 2C parallel, 2B after the OD-2/OD-4 rulings)

**Lane 2A — binding resolver.** Seam: `database.ResolveBinding` and the server's
per-binding bundle cache. Owns `pkg/database` (binding, defaults, new errors),
`pkg/server` resolution and materialization, `pkg/server/lifecycle` (the
node-local vector store and `NewLocalStorage`'s node-wide use are deleted).
- One bundle builder for every backend and isolation mode; the vector service
  in every bundle; a lease driver and watch hub per binding.
- Acceptance: (1) a table test over every isolation mode × backend cell,
  asserting the binding or the typed refusal; (2) for every allowed cell, a
  round trip of every engine whose written bytes are found **by exact key in
  that database's own store** — never by listing, and never through the
  service under test; (3) opening a tenant database's store with the node
  encryptor fails, with the tenant's succeeds; (4) sealing the tenant barrier,
  and separately the database barrier, fails every engine closed, vector and
  time series included; (5) a prefix-tenant `backend: raft` database with a
  barrier: sealing the barrier fails its data closed — the case that is open
  today.

**Lane 2B — catalog authority and placement for every backend** (OD-2, OD-4).
Seam: the placement table. Owns `pkg/placement`, the reconciler, request
forwarding.
- Acceptance: a database created on one node resolves to the same binding on
  every node; a local-binding database's request on a non-owner node is served
  by the owner; with the owner down it fails with the typed unavailable error.

**Lane 2C — drop under a binding.** Seam: `Manager.Drop` and the fence key.
Owns the drop path; depends on the guarded-write wrapper landing first.
- Acceptance: a writer racing a drop on every engine, vector and time series
  included, is refused and nothing it wrote survives; after the drop the store
  directory, object prefix, Raft group on every replica and vector index
  checkpoints are gone; recreating the name gives an empty database with a new
  generation.

### Phase 3 — node overrides and defaults (after 2A)

**Lane 3A.** Owns `cmd/qrdbd`, `sdk/go/node`, `sdk/go/transport/embedded`,
`pkg/sql/provider` create path, `pkg/config`, the capability report.
- `database_defaults`; delete the standalone `databases[0].backend` node-wide
  override; SQL `CREATE DATABASE` and `--sql-database` create through the
  defaults; `storage.backend` and `raft.sm_type` narrowed to the control plane.
- The capability report stops gating MVCC, transactions, leases, watches and
  locks on raft mode: every database serves them (ruling 1). Cluster, backup,
  rebalance and etcd remain raft-mode capabilities.
- Acceptance: a standalone node provisioned with four databases on memory,
  file, pebble and objstore puts each database's files where its binding says
  and nowhere else, and the control-plane store holds none of them; SQL
  `CREATE DATABASE` produces the default backend; changing `--sm` changes no
  database's backend.

### Phase 4 — migration (after 1B, 2A, 2B and the OD-10/OD-11 rulings)

**Lane 4A — relocation** of shared-shard data, as specified above.
- Acceptance: a fixture written by the previous version — KV, SQL tables with
  sequences and generation-scoped keys, collections, secondary indexes, time
  series with tags and retention — reads back identically after relocation;
  the source keyspace is empty by exact-key probe; a write during relocation is
  refused retryably and succeeds after; a crash injected at each step recovers.

**Lane 4B — vector import**, as specified above.
- Acceptance: a node-local fixture imports into the right databases with IDs
  and metadata preserved; each conflict policy behaves as documented; an
  unattributable namespace is reported and skipped.

**Lane 4C — time-series retention enforcement** (only if OD-16 is ruled A).
Specified in [../time-series/engineering.md](../time-series/engineering.md).

### Documentation each lane updates in the same change

multi-tenancy, run-modes, storage-engines, capabilities, configuration, sql,
collections, secondary-indexes, leases, watch, backup-restore,
etcd-compatibility — wherever the lane changes what that folder states.

## Target: surface impact across the six transports

Every row applies to REST, gRPC, Unix, QUIC, MCP and embedded alike, plus the
CLI and the SDK types. A row that is not on all six is a parity defect.

| Surface | Change | Breaking |
|---|---|---|
| Database create | `engine` removed; `backend` optional (defaults, OD-3); `raft.state_storage` added (OD-8) | yes |
| Database info / list | `engine` removed; read-only `binding` block added | yes |
| Update connection | removed (OD-9 A) | yes |
| Update raft | `state_storage` is creation-time; refused on update | no |
| Vector, all twenty capabilities | `shard_id` removed (OD-14 A) | yes |
| Vector, every capability | now served for every database on every backend, including volume and raft tenants, where it was absent | no |
| Time series, every capability | now served on every backend, where it required pebble | no |
| MVCC, transactions, leases, watches, locks | now served for every database on every node mode | no |
| Capabilities report | MVCC, transaction, lease, watch, lock no longer raft-mode gated | no |
| SQL `CREATE DATABASE` | backend from defaults | no |
| SQL engine tables | per OD-12 | yes under B |
| Configuration | `databases[].engine` removed; `database_defaults` added; `databases[0].backend` no longer node-wide | yes |
| SQL driver DSN | `engine=` parameter removed | yes |
| Admin | `qrdb admin migrate vectors` added (OD-11 C) | no |

Breaking rows land in one MINOR release, recorded in the CHANGELOG.

## Test strategy

- **Unit**: lifecycle (create/get/list/drop), **what a drop reclaims and in
  what order** (every key shape, the index-name shape, vector namespaces,
  idempotence, retry after a failed reclamation, and the refusal of an
  unscoped request), **what a drop must NOT reclaim** (two tenants with a
  database of the same name and unattributable pre-namespace collection
  records: one tenant's drop leaves the other's records and its own
  namespaced data is still reclaimed — both halves, because a fix that stops
  deleting everything by stopping the legitimate reclamation is not a fix),
  each update verb, the shared-store barrier refusal, scale bounds, the backend
  immutability rule, and `DatabaseScoped` producing an independent client.
- **The shared-store barrier refusal is a security test**, not a config test —
  it is what keeps a barrier from being decorative. In the target design the
  same role is played by acceptance test 2A(5).
- **The reclamation has a test against a real engine and a real key/value
  service**, not only against in-memory doubles, and it probes residue by
  exact key rather than by listing. Both properties are load bearing: a double
  that skipped the discovery guard, and an assertion that listed through it,
  each independently hide a teardown that deletes nothing. Doubles standing in
  for the key/value service apply the guard for the same reason.
- **Target: bindings are tested at the store, not at the service.** Every
  placement assertion opens the database's own store and probes by exact key.
  A test that writes through a service and reads back through the same service
  passes whether the data landed in the right store or the shared one, which is
  how the current state went unnoticed.
- **Integration**: database parity across transports in the standalone stage;
  `scale`, dedicated-raft creation and non-owner forwarding belong to the
  cluster stage.

## Observability

- `DatabaseInfo.Status` is the health signal per database; in the target design
  `DatabaseInfo.Binding` says where its data lives.
- `DatabaseBarrierStatus` reports seal state, mode and share counts.
- Create, drop, scale, seal, unseal, relocation start and finish, and vector
  import are audited through [../audit/](../audit/).
