# Databases — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-06
>
> Sections headed **Target** are the requirements contract for the multi-model
> database program described in [architecture.md](architecture.md). They are
> approved by the owner on 2026-09-30 and are not implemented. Every other section describes
> the shipped code. The owner ruled every decision OD-1 to OD-16 on 2026-09-30;
> they are listed at the end of architecture.md.

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
	UpdateDatabaseSQL(ctx context.Context, req UpdateDatabaseSQLRequest) (*DatabaseInfo, error)
	UpdateDatabaseCompaction(ctx context.Context, req UpdateDatabaseCompactionRequest) (*DatabaseInfo, error)
	SealDatabase(ctx context.Context, name string) error
	UnsealDatabase(ctx context.Context, name, secret string) error
	DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierConfig, error)
}
```

`UpdateDatabaseConnection` is removed (OD-9, ruled A).

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
	SQL        *DatabaseSQLConfig        `json:"sql,omitempty"`
	Compaction *DatabaseCompactionConfig `json:"compaction,omitempty"` // nil: off
	Binding    *DatabaseBinding          `json:"binding,omitempty"`    // new, read-only
}

// DatabaseCompactionConfig is the database's scheduled-compaction policy,
// specified in ../key-value/engineering.md "Scheduled compaction".
type DatabaseCompactionConfig struct {
	Mode            string `json:"mode,omitempty"`            // off | revision | period
	RetainRevisions int64  `json:"retainRevisions,omitempty"` // revision mode, >= 1
	RetainPeriod    string `json:"retainPeriod,omitempty"`    // period mode, Go duration >= 1s
}

type UpdateDatabaseCompactionRequest struct {
	Name       string                    `json:"-"`
	Compaction *DatabaseCompactionConfig `json:"compaction"` // nil turns the policy off
}

// DatabaseSQLConfig is specified in ../row-locking/engineering.md.
type DatabaseSQLConfig struct {
	Concurrency string `json:"concurrency,omitempty"` // optimistic | pessimistic
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
	ReplicaID      uint64   `json:"replicaId,omitempty"` // no shard ID: issued by the cluster, see below
	RaftAddress    string   `json:"raftAddress,omitempty"`
	Peers          []string `json:"peers,omitempty"`
	LocalNodeCount int      `json:"localNodeCount,omitempty"`
	StateStorage   string   `json:"stateStorage,omitempty"` // new (OD-8 A): pebble | file | memory
}
```

`ProtocolConfig`, `EtcdProtocolConfig`, `DatabaseBarrierConfig`,
`DatabaseSQLConfig` and `ScaleDatabaseRequest` are unchanged. `DatabaseConnectionConfig`, the
`Connection` field and remote mode are removed (OD-9, ruled A). `Name` is
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

### Drop fence surface

`pkg/database` — the drop seam and its refusals:

```go
type KeyspaceReclaimer interface {
	ReclaimDatabaseKeyspace(ctx context.Context, tenantID, database string) error
	FenceDatabaseKeyspace(ctx context.Context, tenantID, database string) error
	UnfenceDatabaseKeyspace(ctx context.Context, tenantID, database, incarnation string) error
}

func (d *Database) Incarnation() string // the value every write to d is guarded by
func NewIncarnation() (string, error)   // 128 random bits, hex
func DropRollbackContext(ctx context.Context) (context.Context, context.CancelFunc)

func CheckDroppable(name string) error // *DefaultDatabaseDropError for the default database

type DroppingError struct{ Name, TenantID string }
type DefaultDatabaseDropError struct{ Name string }
type KeyspaceFenceError struct{ Name, TenantID string; Err error }
```

`pkg/service` — the guard a database's writes carry:

```go
const DropFencePrefix = InternalKeyPrefixSystem + "dropfence/"

func DropFenceKey(tenantID, db string) string
func DropFenceGuard(tenantID, db, incarnation string) (WriteGuard, error)

type WriteGuard struct{ /* encoded compares */ }
func NewWriteGuard(compares ...Compare) (WriteGuard, error)
func WithWriteGuard(ctx context.Context, g WriteGuard) context.Context
func WriteGuardFrom(ctx context.Context) WriteGuard

type StoreResolver interface {
	ReclaimTarget(ctx context.Context, tenantID string) (ReclaimTarget, bool, error)
	FenceTarget(ctx context.Context, tenantID, db string) (FenceStore, bool, error)
}

type FenceStore struct {
	KV          ReclaimKV // nil: the database's own store does not exist
	PerDatabase bool      // the store is the database's own (a dedicated-raft shard)
}
```

`pkg/protocol` — the command format (see "Guarded write commands"):

```go
const OpGuarded byte = 68

func EncodeGuarded(compares [][]byte, inner []byte) ([]byte, error)
func DecodeGuarded(payload []byte) ([]DecodedCompare, []byte, error)
func OpenGuarded(payload []byte, read GuardReader) (inner, refusal []byte, err error)
func EncodeGuardRefusal(key []byte) []byte
func DecodeGuardRefusal(data []byte) (key []byte, ok bool)
```

`sdk/go/transport/embedded`:

```go
func WithServiceRegistry(provider func() service.Registry) Option
```

## Guarded write commands

A guarded write is an ordinary write command wrapped with the compares that
must hold, at the entry's own log position, for it to apply:

```
[OpGuarded:1][compareCount:4 LE]([compareLen:4 LE][compare])*[inner command]
```

- **Each compare is the transaction compare encoding** (`EncodeCompare`,
  the one `TransactionServicer.Execute` uses). A guard accepts the two
  targets every state machine can evaluate: `version` — 0 when the key is
  absent, 1 when it is present, because several state machines keep no
  per-key version counter — and `value`. Revision targets are refused when
  the guard is built and fail closed if one is ever decoded.
- **The inner command is complete and unchanged**, opcode included. The
  guard is a wrapper rather than a field on each command because several
  payloads end in an unframed value (a KV put's value is the rest of the
  payload), so a trailing optional guard could not be told apart from it.
  A command with no guard is therefore byte-for-byte what it always was and
  applies unconditionally, including every command already in a Raft log.
- **Served by**: the four state machines in `pkg/statemachine`
  (`KVStateMachine`, `RegularStateMachine`, `FastStateMachine`,
  `ConcurrentStateMachine`), `pkg/store`'s `PebbleStateMachine` and
  `MVCCStateMachine`, and `pkg/engine`'s `KVEngine` and `MVCCEngine`.
  `pkg/backend`'s `SimpleStateMachine` does not serve it and refuses it in
  the result.
  Each reads the guard through the same read its own `CompareAndSwap` uses —
  within the apply batch where there is one, so a fence written by an
  earlier entry of the same batch is seen — and dispatches the inner command
  through its own handler table at the outer entry's revision.
- **A guard that does not hold is a successful application that writes
  nothing**, reported in the result payload as an application error carrying
  the refused key (`EncodeGuardRefusal`). Returning an apply error instead
  would halt the shard. The service layer turns a refused drop fence into
  `database.DroppingError`, marked `service.ErrNotStored`; any other refused
  guard is `service.WriteGuardRefusedError`. A malformed guard or a read
  fault during evaluation is an apply fault, like any undecodable committed
  entry — never a refusal, which would let replicas disagree.
- **`OpGuarded` is only sent to a group that can apply it.** A replica
  running a binary that predates the opcode would refuse the entry and
  write nothing while its peers applied the inner write, diverging the
  group. `service.guardCommand` -- the one place a guard is attached --
  asks the proposing Host, when it implements `service.FeatureGatedHost`,
  whether `featuregate.GuardedWrites` is enabled for the target shard, and
  proposes the command unguarded when it is not; the database's fence is
  then the resolve-time check. `raftbackend.HostAdapter` answers from the
  node's `featuregate.Gate` (see
  [../clustering/engineering.md](../clustering/engineering.md), "Cluster
  feature gate"); a Host with no transport, a `LocalBackend`, and a Host
  that does not implement the interface have every replica in this
  process and always send guarded writes.
- **`FastStateMachine` remembers an absent guard key.** A database with no
  incarnation of its own is live while its marker is absent, so every write
  to it would otherwise read storage for the marker; the first miss is
  cached as the tombstone the cache already uses for a deleted key.

## Invariants

### Shipped

- **Secrets are write-only, and one converter builds every response.**
  `pkg/api/dbwire` converts a `database.DatabaseInfo` to the JSON wire shape
  (`dbwire.Info`, `transport.DatabaseInfo`: REST, QUIC, MCP, the embedded
  transport and so the desktop bindings) and to the protobuf one
  (`dbwire.InfoProto`, `DatabaseInfoResponse`: gRPC and the Unix socket); the
  gRPC and Unix SDKs decode it back in one place
  (`sdk/go/transport/internal/dbconv`). No transport has a response conversion
  of its own. The response types are read views
  (`transport.DatabaseRaftInfo`, `transport.DatabaseBarrierInfo`, the proto's
  `DatabaseRaftInfo`/`DatabaseBarrierInfo` with the secret field numbers
  `reserved`): each discovery secret is reported as `SharedSecretSet` /
  `TokenSet`, true when set. The request types keep the secret fields.
  **Proof:** the `dbwiretest` fixture stores every discovery secret through
  the real `DatabaseService`; `TestDatabaseSecrets_NeverReadBack` (REST),
  `TestGRPC_DatabaseSecrets_NeverReadBack`,
  `TestHandleDatabaseSecrets_NeverReadBack` (QUIC),
  `TestDatabaseTools_SecretsNeverReadBack` (MCP),
  `TestUnixRT_DatabaseSecretsNeverReadBack` (Unix wire and SDK),
  `TestEmbeddedDatabase_SecretsNeverReadBack` and
  `TestDatabaseBinding_SecretsNeverReadBack` (desktop) read it back through
  get, list and an update, and fail on any secret byte or a missing set flag.
  `TestDatabaseInfoWire_*` holds the Go SDK, and `database-info-wire.test.ts`
  the TypeScript SDK, to the recorded REST response key for key.
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
- **etcd requires a node in mode `raft`, and TLS and auth enabled.** A
  standalone node's listener refuses to start (the etcd listener is raft-only,
  `server.WithStandaloneMode`). Any backend serves it.
  *(Corrected twice: `backend: raft` was documented as required and was
  neither required nor checked -- the listener serves any backend through the
  service registry; and this section previously also required `engine: kv`,
  refusing any other engine at create and on update with
  `database.EtcdEngineUnsupportedError` -- Lane 1C deleted the `engine`
  concept entirely (OD-12, OD-13), so that constraint and its typed error no
  longer exist. etcd now serves every database regardless of what it stores.)*
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
  - **The database is fenced before the reclamation runs.** `Manager.Get`
    refuses with `database.DroppingError` from the moment the drop begins
    until it either finishes or a failed reclamation clears the fence.
    Every read and write path resolves through `Manager.Get` (directly, or
    through `service.Registry.resolve`, which now checks it before any
    isolation-specific routing — see "Fenced but still cacheable" below), so
    this is what stops a write from landing in a keyspace that is already
    being reclaimed and surviving, orphaned, once the catalog record is
    gone. The fence is its own field (`Database.Dropping`), independent of
    `Database.Status` — Status keeps answering only "is there an open
    backend", so Close (a dependent of the fence, not the other way round)
    and the boot-time barrier re-registration cannot clear it as a side
    effect — and it is persisted in the database's own metadata, so a
    restart mid-drop reopens the database fenced (Status open or sealed as
    normal, Dropping true) rather than silently resolvable again.
  - **The store fence makes every write atomic with the drop.** Each
    database's fence marker (`service.DropFenceKey`), in the Raft group
    holding its data, holds the database's incarnation while it is live.
    Every write its views propose carries "the marker holds exactly my
    incarnation" in the same Raft command (see "Guarded write commands"
    above). Before it reclaims anything, `Manager.Drop` has the reclaimer
    set the marker to a tombstone no incarnation equals
    (`KeyspaceReclaimer.FenceDatabaseKeyspace`), so the state machine, not
    the caller, decides at the write's log position: a write resolved and
    proposed before the drop — a handle held across an interactive SQL
    transaction, a retried RPC, a slow goroutine — is refused with
    `database.DroppingError` if it applies after the fence, and removed by
    the reclamation if it applied before.
  - **Handles are incarnation-scoped.** `Manager.Create` chooses a new
    incarnation, records it in the database's metadata, and marks it live
    (`UnfenceDatabaseKeyspace`) as its LAST fallible step before the
    database becomes resolvable — a create that fails earlier leaves the
    store exactly as the drop left it. A handle to an earlier database of
    the name names the earlier incarnation and is refused. Creates of one
    name are serialized, so two can never mark different incarnations live.
    A database with an empty incarnation (created before incarnations, or
    with no store to mark at the time) is live while its marker is absent,
    which is why a drop's tombstone stays after the drop completes. A
    dedicated-raft database's marker is absent while it is live, because its
    shard ID identifies its incarnation: shard IDs are never issued twice
    in the cluster (they are claimed from the replicated high-water mark;
    see [Cluster-wide shard IDs](#cluster-wide-shard-ids)), so a shard
    cached for a dropped incarnation carries a different ID from the name's
    new placement and is evicted — stopped and its directory purged,
    outside the factory lock — never served or unfenced. A cold,
    never-cached dedicated-raft database stays droppable: the factory's
    fence-ignoring resolution reads the catalog through
    `Manager.GetIgnoringFence`.
  - **The marker is claimed, never overwritten.** `UnfenceDatabaseKeyspace`
    marks an incarnation live with `PutIfAbsent`, then
    `CompareAndSwap(tombstone -> incarnation)`, then a read: the incarnation
    it already holds is success (a retried create or rollback), any other
    live value is `*database.AlreadyExistsError` with `Elsewhere` set — the
    name was created on another node, since the catalog is node-local until
    it is replicated — and a marker that keeps changing under the claim is
    `*service.MarkerContendedError`. Clearing to absent is
    `CompareAndDelete(tombstone)`. `Manager.Create` returns the
    already-exists refusal unwrapped, so every transport maps it as a
    conflict.
  - **A dropped name is re-created only through the store.** The node
    records each drop under `<data-dir>/dropped/<name>`; a manager with no
    reclaimer refuses such a name with `*database.ReclaimerRequiredError`,
    and qrdbd provisions its configured databases (`Manager.Provision`,
    with the shared empty incarnation) only after `lifecycle.BuildServices`
    has attached the reclaimer and the system shard has a known leader —
    marking the name live is a Raft write. An invalid configured database
    is still rejected before the node starts.
  - **A failed drop is rolled back completely or not at all.** The rollback
    always restores the marker to the database's incarnation — a fence
    write that reported failure may have committed — under
    `database.DropRollbackContext`, which the failed request's cancellation
    cannot reach. Only when that succeeds is the local fence cleared;
    otherwise the database stays fenced until the drop is retried, rather
    than looking live while the store refuses its writes.
  - **The reclamation runs before the catalog record is removed**, so an
    interrupted drop leaves a fenced, empty database rather than an
    unreachable keyspace, and re-issuing the drop finishes it. The owning
    tenant can issue that retry themselves: `DropForTenant`'s ownership
    check resolves the catalog directly rather than through the fenced
    `Get`, which every other `*ForTenant` accessor uses deliberately — this
    one exception is what keeps the owner from being locked out of
    finishing their own interrupted drop.
  - **The reclamation is idempotent.** An already-empty keyspace is success,
    not a not-found error.
  - **A fencing or reclamation failure fails the drop.** Nothing is
    removed, both fences clear, and the drop is retried. A drop whose store cannot be resolved
    deletes nothing at all, because the resolution is the first thing it
    does.
  - **A dedicated-raft database is fenced twice, once per source of
    truth.** `database.Manager`'s local fence is complete for the shared
    system store and per-tenant volume hosts, which always resolve through
    the local catalog — but a dedicated-raft database's existence and
    ownership are authoritative in the *replicated* placement table
    instead, and its local catalog entry may legitimately be absent on a
    node that did not create it. `DropForTenant`/`Drop` therefore ALSO
    commit `placement.Placement.Dropping` (`Store.SetDropping`) to the
    database's placement entry before reclaiming anything, and
    `RaftBackendFactory.Services` — the resolution seam every node uses,
    cold-starting a shard or returning one already cached — checks it on
    every call and refuses with the same `database.DroppingError`. Because
    the placement table is replicated, this fence holds on every node,
    survives a leader change, and (like the rest of the entry) survives a
    restart, unlike the node-local one. It is removed together with the
    whole placement entry at the very end of a successful drop; a failed
    reclamation clears it the same way the local fence is cleared. The
    placement marker refuses NEW resolutions; the store fence, written into
    the database's OWN shard (resolved through
    `RaftBackendFactory.FenceServices`, the one resolution that ignores the
    marker, because the drop sets the marker first), is what refuses the
    writes of handles already resolved.
  - **A failed placement-entry removal is not swallowed.** Deleting the
    placement entry is what removes the replicated fence, so a failure
    there is surfaced rather than logged and ignored: the fence survives
    with the entry, keeping the database refused everywhere instead of
    simultaneously gone from the local catalog and unrefused in placement.
    A retry recognizes the local catalog entry already being gone
    (`database.NotFoundError` from the manager-level drop) as "finish the
    placement teardown", not as a fresh failure, so it completes rather
    than rolling back a fence a finished drop has no business rolling
    back.
  - **The guard rides the request, so no write verb can miss it.** The
    views `service.Registry` returns for a database attach the database's
    guard to the request context (`service.WithWriteGuard`), and every Raft
    proposal in `pkg/service` goes through one path (`propose`) that wraps
    the command with the context's guard. The namespaced KV, MVCC, batch,
    transaction, time-series and index views, the collection, table,
    sequence, privilege and lock services built over them, and the
    pass-through views of a dedicated-raft database all carry it; a
    conformance test drives every write verb and asserts each proposal is
    guarded. The default database cannot be dropped, so its views carry no
    guard and its writes pay no extra read. The registry resolves the
    catalog record ONCE per resolution and derives both the drop refusal and
    the guard's incarnation from it: a database being dropped is refused
    with `database.DroppingError`, a catalog failure other than "not found"
    is returned, and only a name the manager never held resolves without a
    guard. The collection services the registry caches per database are
    keyed by that guard too, so a name dropped and created again never
    reuses a collection service built for the earlier incarnation.
  - **Two writes are not Raft proposals.** Vector writes go to a node-local
    vector store and are refused by a check made before the write, which
    leaves a window between check and write. A database with a dedicated
    local store (an explicit data directory) is written directly; its drop
    closes and removes the whole store, so a late write has no keyspace to
    survive in.
  - **The embedded transport resolves named databases the way the network
    does.** A collection call or a SQL query naming a database resolves
    through the node's `service.Registry` when the composition configures
    one (`embedded.WithServiceRegistry`), so its writes carry the same guard
    and land in the same `<tenant>/<db>/` keyspace a drop reclaims. With no
    registry configured, the embedded transport serves the same single
    service set every network transport falls back to.
  - **A stale record can never overwrite a newer one, for every placement
    mutation — and a backend that cannot compare-and-swap is refused, never
    silently downgraded.** `Store.CompareAndSwapPlacement` is the primitive
    EVERY read-modify-write of an existing placement entry commits through —
    the fence (`SetDropping`/`TryMarkDropping`/`MarkDropCommitted`), a
    scale-driven membership change
    (`addObserverReplica`/`removeReplicaFromGroup`), and the reconciler's
    own observer-promotion writes. Each reads a snapshot, computes its
    update, and CASes against that exact snapshot; a write that lands in
    between is retried against the fresh value rather than discarding it or
    being discarded by it. A KV with no `AtomicKV` support returns
    `*placement.AtomicWriteUnsupportedError` (the inner error of a
    `*placement.StoreError`) rather than falling back to an unconditional
    `Put` — an earlier version of this fence fell back, which let two
    concurrent claims both observe `owned=true`. `ScaleForTenant` and the
    rebalance MOVE path both reject a snapshot that already shows
    `Dropping=true`, and the reconciler additionally refuses outright to
    promote a replica of a database that is mid-drop — though, like the
    write-time check above, these are still snapshot checks, not compares
    folded into the mutation itself.
  - **A request never claims the fence unless this node can actually
    service the drop, and a rollback undoes only a fence THIS call set.**
    `DropForTenant` resolves the local record through
    `Manager.GetForTenantIgnoringFence` (which, unlike `GetForTenant`, still
    returns the database while its OWN local fence is set — resuming its own
    claim is the one caller that fence must never refuse) BEFORE it ever
    touches the replicated fence; a request reaching a node with no local
    record at all is refused immediately unless `Placement.DropCommitted`
    (below) already proves an earlier attempt elsewhere finished the local
    half. This closes the cross-node half of two `Drop` attempts racing.
    On the SAME node, `Manager.Drop`'s own in-flight guard (`m.dropping`)
    already serializes concurrent attempts to one winner and one immediate
    `DropInProgressError`; `Store.TryMarkDropping` reports `owned` (did THIS
    call perform the not-dropping-to-dropping transition), and a rollback is
    gated on `owned` being true, so a `database.DropInProgressError` loser — which never
    claimed the fence — never clears it. The gate has a second half: the
    placement fence is rolled back only when `database.DropRolledBack(err)`
    reports the manager rolled its own fences back — a
    `*database.LocalFenceError`, or a `*database.KeyspaceFenceError` /
    `*database.KeyspaceReclaimError` with `RolledBack` set because the store
    marker was confirmed restored. A store fence that could not be cleared,
    and every other manager-level failure, leave both fences set. Persisting
    `Placement.DropCommitted` is held to the identical standard: a failure
    to set it now FAILS the whole `Drop`/`DropForTenant` call rather than
    being logged and discarded, so a retry is never stranded believing a
    marker committed that never actually did.
  - **A `NotFoundError` from the manager-level drop is ambiguous, and
    `Placement.DropCommitted` resolves it.** It means either "an earlier
    attempt on this node already finished the local drop" (finish the
    placement teardown) or "this node never had the local record at all"
    (now refused upfront, per above, before this branch is reached in
    practice). `Store.MarkDropCommitted` sets the marker once, right before
    the placement entry is removed on a successful drop; `DropForTenant`
    checks it before treating a `NotFoundError` as "already torn down", and
    returns the error as a genuine failure otherwise, leaving the fenced
    entry intact for a retry against the node that actually holds the
    record. The same `GetForTenantIgnoringFence` resolution also recovers
    Backend/Replicas/DedicatedWAL for a RESUMED drop (local fence survived a
    restart), so the tenant's quota is still released correctly even though
    `GetForTenant` itself would refuse.
  - **`Drop` resumes after a restart despite the local fence refusing the
    usual lookup.** `database.DroppingError` now carries the fenced
    database's tenant ID, recovered before the refusal, so `Drop`'s
    tenant-ID lookup succeeds even when `Manager.Get` itself refuses —
    otherwise a restart mid-drop could never identify which tenant's
    placement entry to finish tearing down.
  - **A cached shard cannot outlive the placement entry it was resolved
    through.** A completed drop's last step deletes the WHOLE placement
    entry, not just its `Dropping` flag, so there is no fence left to read
    at all — indistinguishable, from the resolver's placement lookup alone,
    from a Phase-4b database that never had a placement entry. Each cached
    `raftShard` records whether it was originally resolved through a real
    placement entry; if so, and that entry is now gone entirely,
    `RaftBackendFactory.Services`'s cache-hit path refuses rather than
    serving the stale bundle, closing the window before the
    `TenantShardReconciler`'s own (asynchronous) cache eviction runs.
  - **The local fence's in-memory flag commits before its durable one.**
    `Manager.Drop`'s step 0 sets `Database.Dropping` in memory FIRST, then
    persists it: `loadDatabase` refuses the instant the in-memory flag reads
    true, and `rewriteMeta` always reads it fresh (never a cached value), so
    a concurrent `Update*` call can never observe it still false and
    silently persist a stale `Dropping: false` over the durable write
    moments later. `meta.json` itself now writes through a temp-file/fsync/
    rename sequence rather than a direct write to the final path, so a crash
    mid-write can never leave it truncated, and concurrent writers settle on
    "the later rename wins" rather than an interleaved, invalid file.
- **A second concurrent drop of the same database is refused**, not merged.
  Exactly one caller may report success, because the tenant-scoped drop
  releases quota on success.
- **A sealed database fails reads and writes closed** until unsealed with its
  secret. The service registry refuses every accessor — KV, MVCC, watch, lease,
  index, time series, vector, transaction, batch, table, sequence, privilege,
  lock and collection — with `service.BarrierSealedError{Database: <name>}` for
  a database the caller owns whose status is `sealed`. Another tenant naming it
  gets the ordinary not-found answer. A watch or etcd stream already open ends
  with the same refusal when the database is sealed, and with the not-found
  when it is dropped (`database.Database.Unserved`). **The refusal is enforced on every node
  once the cluster has activated `cluster-seal`.** A database seal then writes
  the database's cluster seal record, and every node holding the database's
  barrier seals it there and ends its open streams; see
  [../barrier-seal/architecture.md](../barrier-seal/architecture.md#sealing-in-a-cluster).
  Before activation the refusal is per node: a database's barrier and its
  `sealed` status live on the node that sealed it, and a node on which it is
  unsealed keeps serving it. Only the tenant that owns a database may seal or
  unseal it, the `system` tenant included.
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
- **`Batch` is all-or-nothing after recovery on every backend** — memory,
  file, pebble and objstore alike. Objstore's commit journal
  (`pkg/storage/objstore/journal.go`, Lane 1A) gives it the same
  all-or-nothing-after-recovery guarantee the file backend already had: a
  commit publishes a single journal object before touching any key, applies
  the batch, then clears the journal, so an interrupted commit is replayed
  in full at the next `New` and one interrupted before the journal became
  durable leaves no trace. *(Corrected: objstore's batch was previously
  best effort, where a mid-commit failure could leave a partial result.)*

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
  that produced them, on every backend. *(Lane 1A, shipped: time-series
  points, metric and tag indexes, and secondary index entries. Still target:
  vector records and vector metadata indexes, Lane 1B.)*
- **New Raft commands are cluster-feature-gated.** The drop fence, the vector
  commands and the retention command are proposed only when the cluster feature
  gate reports every member of the group supports them; otherwise the
  capability is refused with a typed error. A mixed-version group never
  receives a command some member would reject.
- **Every write carries the drop fence.** A write is a guarded command whose
  guard is "this database's fence key is absent", evaluated at apply time on
  every replica. This holds for every engine and for local bindings as well as
  raft ones.
- **Every engine uses the binding's encryptor and data root.** An engine that
  obtains an encryptor or a directory from anywhere else — the node's
  `Encryptor`, the node's `DataDir` — is a tenant-isolation defect.
- ~~Every storage backend's `Batch` is all-or-nothing after recovery,
  objstore included (commit journal), because the apply batch depends on
  it.~~ **Shipped (Lane 1A)** — see "Shipped" above.
- **The composite `<tenant>/<db>/` prefix is not applied** to a database with
  its own store; its keys are at the root of that store. The prefix remains only
  on the control-plane store.
- **Resolution failures are typed and fail closed.** A binding that cannot be
  resolved or materialized is an error to the caller; it never falls back to the
  control-plane store or to another backend.

## Permission model

The two provisioning permissions are enforced where both the request's
backend and the tenant's isolation tier are known: the database service every
network transport (REST, gRPC, Unix, QUIC, MCP), both SQL wire listeners and
the embedded transport of an `sdk/go/node` node with request authentication
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
| `databases:officer_register` / `officer_list` / `officer_revoke` | a Shamir barrier's holder enrolment, listing and revocation |
| `databases:officer_unenrol` | removing a Shamir barrier's holder before its custody is initialized |
| `databases:initialize_shamir` | splitting a Shamir barrier's key to its enrolled holders |
| `databases:rekey` | opening a Shamir barrier's rekey round |

Unchanged by the target design. A create that takes `backend: raft` from the
node defaults (OD-3 A) is checked for `databases:create_raft` exactly as an
explicit one is: the check is on the resolved record, not on the request.


### Cluster-wide shard IDs

A shard ID names one Raft group on a host every tenant shares, so it must be
unique across the cluster, not only on the node that issued it. Every node
used to issue from its own counter, starting at `shard.UserMin` and moving
past the IDs that node had issued (persisted in a `shard-high-water` file in
its data directory), so two nodes creating raft databases each issued the same
ID -- two groups under one ID -- and a database dropped on one node and created
again through another could be handed the dropped incarnation's ID, which the
drop fence relies on never happening. The per-node counter and its file are
gone.

**The guarantee: a user-range shard ID is issued once in a cluster's lifetime,
and only by the cluster.** Its only state is one record in the replicated
store: nothing on a node's disk takes part, so standalone and raft mode behave
identically, and a node restored from a backup onto an empty data directory
continues past every ID the cluster issued. It rests on three things, each
enforced in code:

- **One issuer.** Every raft database create takes its ID from the
  `ShardIDClaimer` set with `Manager.SetShardIDClaimer`.
  `lifecycle.BuildServices` wires a `shard.Claimer` over the replicated system
  store, so every composition that builds the data plane (`qrdbd`,
  `sdk/go/node`, an embedding) claims. It refuses to build over a store
  without the atomic verbs (`lifecycle.ShardClaimerError`). Standalone and
  raft compositions wire the same claimer, each over its own system store. A
  Manager with no claimer refuses raft creates
  (`*database.ShardIssuerRequiredError`); there is no node-local issuer to
  fall back to.
- **No caller-chosen IDs.** No transport's create or Raft-update request
  carries a shard ID: the field is gone from REST, QUIC and MCP, gRPC reserves
  field 2 of `DatabaseRaftConfig`, and the Go and TypeScript SDKs and the CLI
  have no way to send one. As defence in depth, `Manager.create` refuses a
  non-zero `Raft.ShardID` with `*database.CallerShardIDError` (400 /
  `INVALID_ARGUMENT`), and `Manager.UpdateRaft` keeps the issued ID and refuses
  any other. A caller's ID was never checked against what other nodes hold, so
  it could name another database's group or a reserved shard such as the
  system shard.
- **No per-node IDs for provisioned databases.** `Manager.Provision` refuses a
  configuration with Raft settings (`*database.ProvisionRaftError`). Every
  node provisions its configured databases on its own, so a raft one could
  only take an ID from something on its own node, and nodes do not agree. No
  shipped path provisions one: `qrdbd` builds its configured databases with no
  Raft configuration.

How a claim works:

- **One record**, `__sys:shard/high-water`: the next ID no node has issued, in
  decimal. The first claim in a cluster creates it with `PutIfAbsent` and takes
  `shard.UserMin`. Every later claim reads it and swaps it to one past the ID it
  takes with `CompareAndSwap`, re-reading on a lost race
  (`*shard.ClaimContentionError` after 64 losses in a row, 503 /
  `UNAVAILABLE`).
- **A gap, never a reuse.** An ID is recorded as issued before it is returned,
  so a create that fails afterwards leaves a gap.
- **No fallback.** A create that cannot claim refuses with
  `*database.ShardIDClaimError` (503 / `UNAVAILABLE`, retryable) and leaves
  nothing behind. A mark that is not one this code wrote -- not a decimal, or
  at or below `shard.UserMin` -- is `*shard.HighWaterCorruptError` (500 /
  `INTERNAL`): a claim refuses rather than guess.
- **No claim before activation.** On a host whose groups can hold other
  binaries (one that answers `FeatureEnabled`, the raft adapter), the claimer
  is built with `shard.WithActivation`: every claim -- and so every raft
  database create -- is refused with `*featuregate.NotActiveError` (503 /
  `UNAVAILABLE`, retryable) until `featuregate.ClusterShardIDs` is active
  cluster-wide, and the store is not touched. Before activation an old node
  could still allocate per node, and a rolling upgrade would otherwise have a
  window in which two nodes issue one ID. A host with no transport holds only
  this binary and claims at once. **Proof:**
  `TestNewShardClaimer_WaitsForClusterShardIDs`,
  `TestShardIDs_RaftCreateWaitsForClusterShardIDs`,
  `TestClaimShardID_WaitsForActivation`.
- **Feature gate.** `featuregate.ClusterShardIDs` adds no Raft command. Its
  activation opens the claim, and applies the gate's downgrade rule: once every
  node claims, a binary that allocates per node or accepts a caller's shard ID
  can neither start nor be admitted as a replica.
- **No in-place upgrade of a cluster that holds raft databases.** Their shard
  IDs were issued per node and may collide, and nothing reconciles them with the
  high-water mark: the mark starts at `shard.UserMin` and could re-issue an ID a
  per-node database still holds. Such a cluster is rebuilt, not upgraded: create
  a new cluster on this release and move the data across. There is no
  migration and no seed -- the per-node allocation it would carry forward is
  the defect.
- **Proof.** `TestShardIDs_*` (unit: two managers over one replicated store; a
  caller-chosen ID, including the system shard, refused; raft provisioning
  refused; an update that names another shard refused; a restore of the store
  onto a fresh data directory issues no ID twice; a create writes nothing to
  the node's data directory but the database's own entry) and
  `TestIsolationRaft_ShardIDs_*` (three real nodes, in the per-push cluster
  tenancy job: distinct IDs for databases created through different nodes, and
  no reuse of a dropped database's ID when its name is created again through
  another node).

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
| Unseal with a wrong secret | refused; the database stays sealed | **yes** |
| Operating on a sealed database | `service.BarrierSealedError` naming the database, on every engine | **yes** |
| The store holding a database's keys is unresolvable | `service.ReclaimStoreUnavailableError`; the drop fails and nothing is removed | **yes** |
| The drop fence cannot be written, or cleared on create | `database.KeyspaceFenceError` wrapping the cause; a drop fails before anything is reclaimed and clears its local fence only once the marker is confirmed restored (`RolledBack`), a create creates nothing | **yes** |
| The local drop fence cannot be persisted | `database.LocalFenceError`; nothing else was touched, and a placement fence the call set is rolled back | **yes** |
| A create names a database live on another node | `database.AlreadyExistsError` with `Elsewhere` set; the other node's marker is untouched | **yes** |
| A create names a database this node dropped, with no keyspace reclaimer attached | `database.ReclaimerRequiredError`; nothing is created | **yes** |
| The placement table cannot be read while resolving a dedicated-raft fence | the read error surfaces; it is never reported as an unknown database | **yes** |
| A key, index or vector namespace cannot be reclaimed | `database.KeyspaceReclaimError` wrapping the cause; the drop fails and both fences clear (Status was never touched) | **yes** |
| A drop names the default database | `database.DefaultDatabaseDropError` (REST 400 / gRPC and QUIC InvalidArgument), before anything is touched | **yes** |
| A drop is already in flight for the database | `database.DropInProgressError` | **yes** |
| Any operation resolves a database whose drop is in flight | `database.DroppingError` (REST 409 / gRPC and QUIC Aborted) | **yes** |
| A write resolved before a drop is applied after its fence | refused by the state machine; `database.DroppingError`, matching `service.ErrNotStored` (REST 409 / gRPC and QUIC Aborted) | **yes** |
| A dedicated-raft database's placement entry cannot be marked or removed on drop | `*placement.StoreError`; a marking failure fails the drop before anything is reclaimed, a removal failure surfaces rather than being logged, and the database stays fenced either way | **yes** |
| Missing any `databases:*` permission | permission denied | **yes** |
| Target: `backend: raft` on a standalone node | `database.RaftBackendUnavailableError` | **yes** |
| Target: barrier on `backend: memory` | `database.BarrierRequiresPersistentBackendError` | **yes** |
| Target: etcd on a non-raft backend | `database.EtcdRequiresRaftBackendError` | **yes** |
| Target: `data_dir` outside a volume/raft tenant's root | `database.DataDirOutsideTenantRootError` | **yes** |
| Target: `file`/`objstore` for a volume/raft tenant (OD-7, ruled B), as backend or raft state storage | `database.BackendIsolationConflictError` | **yes** |
| Target: a tenant isolation mode the binding table does not know | `database.InvalidTenantIsolationError` | **yes** |
| Target: the store's directory cannot be placed (volume/raft tenant without a data root on this node, node without a data directory) | `database.BindingRootUnresolvedError` | **yes** |
| Target: a raft state storage other than `pebble`, `file`, `memory` (OD-8) | `database.InvalidRaftStateStorageError` | **yes** |
| Target: the owning node of a local-binding database is unreachable (OD-2, ruled A) | `database.BindingOwnerUnavailableError` naming the owner; 503 / `UNAVAILABLE` | **yes** |
| Target: a binding cannot be materialized (store will not open, shard will not start) | `database.BindingUnavailableError` wrapping the cause; 503 / `UNAVAILABLE` | **yes** |
| Target: a write lands after the drop fence | the guard refusal, surfaced to the writer as the drop-fence contract defines it | **yes** |
| Target: a gated command (drop fence, vector, retention) in a group whose members do not all support it | typed feature-gate refusal; nothing is proposed | **yes** |
| Target: a create request carrying `engine` (OD-13, ruled A) | unknown-field invalid-argument | **yes** |

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

`databases[].engine` is removed; a config file still carrying it is refused by
`pkg/config`'s existing strict-decode pass over the whole file (OD-13 ruled A:
a generic unknown-field rule, not an engine-specific check). The node's
control-plane store is configured where it is today —
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
through the discovery guard. A composer
serving volume- or raft-isolated tenants additionally attaches a
`service.StoreResolver`: it routes a volume-isolated tenant's keys, and its
databases' drop fences, to the tenant's dedicated Host, and a raft-isolated
database's drop fence into that database's own shard.

The drop fence is one key per database in the reserved `__sys:dropfence/`
namespace. It holds the database's incarnation while the database is live, a
tombstone after it is dropped, and the next incarnation once the name is
created again, so a dropped database leaves exactly one small record behind
in the store that held it. A database's incarnation is recorded in its
metadata (`meta.json`, `incarnation`); metadata written before incarnations
existed reopens with an empty one.

**The database catalog is node-local while the fence marker is replicated.**
A name live on one node cannot be created on another: the marker is claimed
only from absent or the tombstone, so the second create is refused with
`*database.AlreadyExistsError` (`Elsewhere` set) and the first database's
handles keep writing. Until the catalog is replicated, create a name through
one node. Boot-provisioned configured databases share the empty incarnation
on every node, so their stale handles are refused by the tombstone only
while the name stays dropped.

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

**A local binding is a degenerate, one-replica placement (shipped, Lane
2B).** Rather than a second record shape, a local binding is `Placement{RF:
1, Replicas: []Replica{owner}, LeaderReplicaID: owner.ReplicaID, Backend:
<non-raft>}`, carrying a real `ShardID` claimed from the same cluster-wide
space (`shard.Claimer`) dedicated Raft groups draw from. `Placement.Backend`
(zero value `BackendRaft`, decoding every pre-Lane-2B entry unchanged) and
`Placement.IsLocalBinding()` (`Backend != "" && Backend != BackendRaft`) are
the only new surface a caller needs: everything that resolves or forwards a
placement — the leader cache, `replicaforward.Locator`, the non-owner
refusal — is keyed on `ShardID`/`Replicas`/`LeaderReplicaID` alone and so
serves a local binding identically to a one-replica Raft group, unmodified.
`Placement.RefusalForNode` is the one place that builds the non-owner
refusal from either shape; `RaftBackendFactory`'s resolution path and the
reconciler both use it rather than each constructing `*NotAReplicaError`
themselves.

`Store.ClaimPlacement(ctx, p)` is the cluster-wide, race-free "first write
wins" create for a new entry (dedicated Raft group or local binding alike):
built on `kvstore.AtomicKVStore.PutIfAbsent`, it returns `(claimed, existing,
err)` and, on a lost race, returns the winner's own committed entry rather
than overwriting it — this is what makes a database created on one node
resolve to the same binding everywhere (the first of Lane 2B's acceptance
criteria). It is unsupported (`*StoreError` wrapping
`*AtomicWriteUnsupportedError`) against a KV that does not implement
`PutIfAbsent`.

The per-node reconciler (`TenantShardReconciler`) skips every placement
`IsLocalBinding()` reports true for, in both its poll (`ReconcileOnce`) and
watch (`handleEvent`) paths: a local binding has no Raft group for it to
attach, start or tear down — the owning node materializes it through
database resolution instead (Lane 2A), the same way `meta.json` does today.

### Removing `engine` from persisted metadata (shipped, Lane 1C)

- `DatabaseMeta` and `DatabaseConfig` lose `Engine`; `database.EngineType`,
  its constants and `database.InvalidEngineError` are deleted.
- **Corrected against the original plan above: no migrate-on-open.** The
  no-backward-compat ruling of 2026-10-02 superseded OD-11's "rewrite in
  place" premise for metadata the same way it superseded OD-11 itself (below):
  old systems are obsolete and ship no migration code. `readMeta` decodes
  `meta.json` strictly (`json.Decoder.DisallowUnknownFields`); a file still
  carrying `"engine"` (or any other field this version no longer understands)
  is refused with `*database.LegacyMetadataError` naming the path and the
  decode failure, fail-closed, rather than silently stripped and the file
  rewritten. The database must be recreated — the same remedy OD-11 settled on
  for the node-local vector stores. There is no version marker and no rewrite
  path: a meta.json this version wrote never carries the key, so the check
  costs nothing on the common path.
- A database created with `engine: timeseries` or `engine: vector` becomes an
  ordinary multi-model database. Its time-series or vector data is where the
  data migration below finds it; nothing about it depended on the engine field
  except which SQL virtual tables it exposed (OD-12).
- The protobuf fields numbered 2 in `DatabaseCreateRequest` and
  `DatabaseInfoResponse` become `reserved 2; reserved "engine";`.
- OD-13's strict-decoding rule is generic, not an engine-specific shim: REST,
  QUIC and MCP already refuse any request-body field they do not declare
  (`json.Decoder.DisallowUnknownFields` on REST and QUIC's whole-body decode;
  the MCP create tool's own declared-argument set, `wire.RejectUnknownArgs`,
  for its untyped argument map). A stale `engine` key is one instance this
  catches, not a dedicated check. gRPC and the Unix socket (which serves the
  same protobuf messages) need no runtime check at all: the field is reserved
  in the `.proto`, so the generated Go request type has no `Engine` member to
  set. The embedded transport and `sdk/go` likewise have no `Engine` field on
  `transport.CreateDatabaseRequest`/`DatabaseInfo` — removed at compile time.
  The CLI's `--engine` flag is deleted outright, so cobra reports it as an
  unknown flag rather than accepting and discarding it. The `databases:`
  configuration section is covered by `pkg/config`'s existing
  `validateKnownFields` strict-decode pass over the whole file, unchanged by
  this lane.

## Target: migrating existing data

Pre-binding data lives in three places. OD-10 (ruled A: automatic, online,
fenced relocation) and OD-11 (ruled C: an operator import command with a
conflict policy) decide what happens to each; the mechanics below implement
those rulings.

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
before the decisions it depends on are ruled; as of 2026-09-30 all are.

### Phase 1 — foundation (parallel)

**Lane 1A — storage-generic engine (shipped 2026-10-06).** Seam:
`storage.Storage` and the apply `storage.Batch`. Owns `pkg/engine`,
`pkg/timeseries`, `pkg/index`, `pkg/storage/objstore`.
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
  transports; an old `meta.json` with `engine` is refused as
  `*database.LegacyMetadataError` and the database must be recreated — the
  no-backward-compat ruling of 2026-10-02 supersedes OD-11's rewrite-on-open
  premise, so this replaces the "opens and is rewritten" criterion originally
  written here; the database parity suite passes with no `engine` in any
  fixture; the proto field is reserved.

### Phase 2 — bindings (after 1A; 2A, 2B and 2C in parallel)

**Lane 2A — binding resolver.** Seam: `database.ResolveBinding` and the server's
per-binding bundle cache. Owns `pkg/database` (binding, defaults, new errors),
`pkg/server` resolution and materialization, `pkg/server/lifecycle` (the
node-local vector store and `NewLocalStorage`'s node-wide use are deleted).
- One bundle builder for every backend and isolation mode; the vector service
  in every bundle; a lease driver and watch hub per binding.
- **Part 1, the resolver, shipped** (`pkg/database/binding.go`).
  `database.ResolveBinding(cfg, BindingTenant, BindingNode)` is the pure
  function of "Resolution model" in [architecture.md](architecture.md): no
  I/O, a `*database.Binding` (kind, backend, data root, encryptor scope,
  per-database barrier, and for `raft` the host, dedicated WAL, replicas,
  cluster mode and state storage) or one of the typed refusals of "Combinations
  that are refused". The isolation table and the backend-to-kind table are
  maps (`isolationPlacements`, `bindingKinds`), so a new mode or backend has
  exactly one row to add. `database.Defaults` (in `BindingNode`) fills only a
  field the record left empty: backend from the node default, else `raft` in
  raft mode and `pebble` standalone (OD-3); replicas (else 1), cluster mode and
  state storage (else `pebble`, OD-8) for a raft binding. Three refusals the
  table did not list follow from its rules and fail closed the same way: an
  unknown isolation mode (`InvalidTenantIsolationError`), a store directory
  that cannot be placed — a volume/raft tenant with no data root on this
  node, or a node with no data directory (`BindingRootUnresolvedError`) — and
  a raft state storage other than pebble, file or memory
  (`InvalidRaftStateStorageError`). OD-7's refusal of `file` covers a raft
  group's state storage as well as the backend, for the same reason. An
  explicit `data_dir` of a volume/raft tenant must lie strictly beneath the
  tenant root (the root itself holds the tenant's other state). Acceptance
  (1) is `TestResolveBinding_IsolationByBackend`. **Not yet called** by
  create or by request resolution: that is part 2 below.
- **Part 2, materialization (target).** Create resolves the binding and
  records it (defaults applied once); `pkg/server` materializes a binding once
  per node — store at the binding's data root under the binding's encryptor,
  host, watch hub, lease driver — and builds the one bundle over it, cached per
  (tenant, database, generation); `restServiceRegistry.resolve` serves every
  database from its binding; the node-local vector store and
  `NewLocalStorage`'s node-wide use are deleted. Acceptance (2)–(5) land here.
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

**Lane 2B — catalog authority and placement for every backend** (OD-2 and OD-4, both ruled A).
Seam: the placement table. Owns `pkg/placement`, the reconciler, request
forwarding.
- Acceptance: a database created on one node resolves to the same binding on
  every node; a local-binding database's request on a non-owner node is served
  by the owner; with the owner down it fails with the typed unavailable error.

**Lane 2C — drop under a binding.** Seam: `Manager.Drop` and the fence key.
Owns the drop path; depends on the guarded-write wrapper, which merges
behind a cluster feature gate, landing first.
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

### Phase 4 — migration (after 1B, 2A and 2B)

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

**Lane 4C — time-series retention enforcement** (OD-16, ruled A).
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
| Update connection | removed, with remote mode (OD-9, ruled A) | yes |
| Update raft | `state_storage` is creation-time; refused on update | no |
| Vector, all twenty capabilities | `shard_id` removed (OD-14 A) | yes |
| Vector, every capability | now served for every database on every backend, including volume and raft tenants, where it was absent | no |
| Time series, every capability | now served on every backend, where it required pebble | no |
| MVCC, transactions, leases, watches, locks | now served for every database on every node mode | no |
| Capabilities report | MVCC, transaction, lease, watch, lock no longer raft-mode gated | no |
| SQL `CREATE DATABASE` | backend from defaults | no |
| SQL engine tables | every database exposes `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`, `qrdb_vector_vectors` (OD-12, ruled B) | yes |
| Configuration | `databases[].engine` removed; `database_defaults` added; `databases[0].backend` no longer node-wide | yes |
| SQL driver DSN | `engine=` parameter removed | yes |
| Admin | `qrdb admin migrate vectors` added (OD-11 C) | no |

Breaking rows land in one MINOR release, recorded in the CHANGELOG.

## Shamir custody per database

- `pkg/ceremony`: `ScopedCeremonyService` is the one holder authority for a
  tenant barrier (`NewTenantCeremonyService`) and a database barrier
  (`NewDatabaseCeremonyService`, scope `ScopeDatabase(tenant, database)`,
  registry prefix `ceremony/tenants/<tenant>/databases/<db>/officers/`). It is
  installed on the database barrier as its distributor and holder verifier,
  and keeps its records under that barrier.
- `pkg/database`: `DatabaseBarrierManager` keeps its own `BarrierRegistry` over
  the system barrier, so a database name can never collide with a tenant id.
  It resolves a barrier mode through the `seal.StrategyRegistry` a composer
  supplies (`WithBarrierStrategyRegistry`) before the built-in
  `passphrase`/empty/`shamir` modes, so a hardware strategy registered once
  seals a database barrier as it seals a tenant barrier; a mode neither knows is
  `UnsupportedBarrierStrategyError`, on create (`EnsureBarrier`), on re-open
  (`RegisterBarrier`) and on `UpdateBarrier`.
  `DatabaseConfig.Validate` (and the database service, before any
  reservation) refuses a Shamir M-of-N outside 2 <= M <= N <=
  `MaxShamirShares` (255, the seal's GF(2^8) split limit) with
  `InvalidShamirSchemeError`, so an unsplittable scheme never reaches the
  catalog. `SetHolderDeps` (the CA and factor registry, wired by the server
  lifecycle from the ceremony service) enables Shamir; without it a `shamir` create is refused
  (`ShamirCustodyNotConfiguredError`). A create registers the barrier
  uninitialized and marks the database `sealed`; `InitializeShamir` splits
  through the holder authority; a reopen re-installs it; drop destroys the
  scope. `Holders(name)` answers `NotShamirBarrierError` for any database
  without one. `Manager.UpdateBarrier` refuses a strategy or M-of-N change, and
  `RecordShamirScheme` records the M-of-N a completed rekey produced. The
  rekey has already completed when it runs, so a failure there is logged and
  the completed outcome is still returned: the barrier's own record of the
  scheme is the new one, and only the catalog's copy is stale.
- `pkg/service`: `DatabaseHolderServicer` is additive to `DatabaseServicer`, as
  `DatabaseCredentialCreator` is; transports type-assert for it. Every method
  runs the tenant-ownership check before resolving the barrier. A completed
  seal or unseal quorum updates the database's status; initialization leaves
  it open.
- Transports: REST `handlers_database_holders.go` (map dispatch under
  `/v1/databases/{name}/barrier/`), gRPC/Unix `service_database_holders.go`
  (messages nest the system holder request), QUIC `db.Barrier*`, MCP
  `qrdb_database_barrier_*`, embedded (claims in process are bound by proof of
  possession). Errors classify through `pkg/api/errclass`.
- SDK: `transport.DatabaseClient` gains the ten methods (unenrolment
  included); the request types
  embed the system holder requests. `internal/holderconv.DatabaseHolders`
  carries the protobuf calls once for gRPC, Unix and QUIC (QUIC frames the same
  messages through a `DatabaseHolderRPC` of its own). A client refuses a
  request naming no database with `DatabaseNameRequiredError`.
- CLI and desktop: the holder verbs are written once against `holderTarget`;
  `holderKind` (system, tenant, database) selects the scope flag and target by
  map. The desktop `OfficerBinding` takes `HolderScope.Database` and adds
  `InitializeDatabaseShamir` and `UnenrolDatabaseHolder`; the CLI adds
  `barrier-officer unenrol` for the database scope.
- Web UI and desktop app: the database wizard offers Shamir, creates the
  database sealed, then a holders step enrols exactly N holders
  (`HolderEnrolmentForms` with an `enrol` callback that calls
  `registerDatabaseOfficer`) and initializes (`initializeDatabaseShamir`). It
  withholds Shamir only on the server's refusal at create
  (`ShamirCustodyNotConfiguredError` where no holder custody is wired); see
  [../desktop-gui/engineering.md](../desktop-gui/engineering.md). The admin
  database view shows `DatabaseHoldersPanel` (holders, `HolderRekeyPanel`)
  for a Shamir database, and for one whose custody is not yet initialized the
  enrolment of its remaining holders and **Initialize**.
- Tests: `pkg/service/servicetest.NewShamirDatabase` is a real database service
  with one Shamir database; every server transport drives a lifecycle through
  its handlers with it, and `sdk/go/transport/internal/dbholdertest` runs the
  same client-side suite through all six SDK transports.

## Test strategy

- **Unit**: lifecycle (create/get/list/drop), **what a drop reclaims and in
  what order** (every key shape, the index-name shape, vector namespaces,
  idempotence, retry after a failed reclamation, and the refusal of an
  unscoped request), **the fence a drop sets before it reclaims anything**
  (a concurrent `Get` is refused with `DroppingError` rather than resolving
  the database and, were the fence not refused, an actual write to the
  backend is proven rather than merely asserting the resolution failed; the
  fence survives a simulated crash because it is persisted in `meta.json`,
  and a restart followed by a retried `Drop` completes the job — closing
  and removing the backend the crash left open — rather than getting stuck
  because Status looks as though a drop already got that far; a failed
  reclamation clears the fence without disturbing Status, which was never
  touched; `DropForTenant` lets the owner retry a fenced database while
  still refusing a non-owner; every non-tenant `Update*` method refuses a
  dropping database, and `rewriteMeta` never clears a fence it did not set;
  boot-time barrier re-registration does not clear a restored fence
  either), **the same fence checked at the service-resolution layer**
  (`service.Registry.resolve` refuses a database pre-registered in the
  server-level registry — the one path that does not call
  `database.Manager.Get` on its own), **the store fence against the real
  apply path** (a write resolved and proposed before `Drop`, held until the
  drop has fenced and reclaimed, is refused at apply with `DroppingError`
  and leaves nothing behind, for a shared-store database and for a
  dedicated-raft database whose fence goes into its own shard and not the
  system store; a write applied before the fence is reclaimed; a handle
  from before the drop is refused after it and after the name is created
  again, while a fresh handle writes; a create that fails before marking
  the name live leaves it fenced; a failed drop — including one whose fence
  write reported failure after committing, and one whose request was
  cancelled — rolls the fence back, and one whose fence cannot be cleared
  stays fenced; a raft shard cached for a dropped incarnation is evicted;
  every method of every guarded servicer interface, enumerated by
  reflection, is either a classified read or a write verb whose proposals
  are all guarded, collection verbs included; every serving state machine
  and engine applies a
  guarded write when the guard holds, refuses it without halting when it
  does not — including against a fence written earlier in the same apply
  batch — and still applies a guard-less command unconditionally), **the
  default database** (every drop entry point refuses it before touching
  anything, and every transport maps the refusal), **the replicated
  placement fence a dedicated-raft drop also sets** (`Store.SetDropping`
  round-trips through `Get`, is a no-op when the value already matches, and
  fails on a placement entry that does not exist; `RaftBackendFactory.
  Services` refuses with `DroppingError` on both the cold-start path and a
  shard already cached from before the drop began, using two independent
  `RaftBackendFactory` instances sharing one `placement.Store` since a
  genuine multi-member Raft group cannot form across two transport-less
  test hosts in-process — the closest harness available for placement
  resolution across nodes; `DatabaseService.DropForTenant`/`Drop` commit the
  fence before reclaiming, leave it set and the entry intact when the final
  placement-entry removal fails rather than swallowing that failure, and a
  retry recognizes the local catalog already being gone as "finish the
  placement teardown" and completes it), **what a drop must
  NOT reclaim** (two tenants with a
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

## Compaction policy

A database's scheduled-compaction policy (`compaction.Policy` (`pkg/compaction`), the
`compaction` block of `DatabaseConfig`, its `meta.json` record and
`DatabaseInfo`) is **off unless its owning tenant set one**, and is set only by:

- `UpdateDatabaseCompaction` on any transport, which requires `mvcc:compact`
  (`ScopeSelfStrict`) and refuses -- as not found -- a database the caller's
  tenant does not own; the system tenant owns only its own databases
  (`database.Manager.UpdateCompactionForTenant` applies that rule to every
  caller); and
- the node's configuration file, `databases[].compaction`, for the databases
  that file declares (all system-owned). When present it is applied at every
  start; a database it is absent from keeps whatever was last set through the
  API.

Create never takes a policy, so `databases:create` cannot compact. The policy's
semantics, and the scheduler that runs it, are specified in
[../key-value/engineering.md](../key-value/engineering.md) "Scheduled
compaction".

```yaml
databases:
  - name: events
    backend: pebble
    compaction:
      mode: revision        # off | revision | period
      retain_revisions: 100000
  - name: audit
    backend: pebble
    compaction:
      mode: period
      retain_period: 720h
```
