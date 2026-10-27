# Databases — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.
>
> Sections headed **Target** are the requirements contract for the multi-model
> database program described in [architecture.md](architecture.md). They are
> approved and are not implemented. Every other section describes
> the shipped code.

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
	DatabaseBarrierStatus(ctx context.Context, name string) (*DatabaseBarrierStatus, error)
	DatabaseBarrierOfficerRegister(ctx context.Context, req *DatabaseOfficerRegisterRequest) (*OfficerRegisterResponse, error)
	DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*OfficerStatus, error)
	DatabaseBarrierShamirInitialize(ctx context.Context, req *DatabaseShamirInitializeRequest) (*BarrierInitializeShamirResponse, error)
	DatabaseBarrierShareClaimBegin(ctx context.Context, req *DatabaseShareClaimBeginRequest) (*ShareClaimBeginResponse, error)
	DatabaseBarrierShareClaimComplete(ctx context.Context, req *DatabaseShareClaimCompleteRequest) (*ShareClaimCompleteResponse, error)
	DatabaseBarrierQuorumBegin(ctx context.Context, req *DatabaseQuorumBeginRequest) (*QuorumChallenge, error)
	DatabaseBarrierQuorumSubmit(ctx context.Context, req *DatabaseQuorumSubmitRequest) (*QuorumOutcome, error)
	DatabaseBarrierRekeyStart(ctx context.Context, req *DatabaseRekeyStartRequest) (*QuorumRound, error)
	DatabaseBarrierOfficerRevoke(ctx context.Context, req *DatabaseOfficerRevokeRequest) (*QuorumRound, error)
	DatabaseBarrierOfficerUnenrol(ctx context.Context, req *DatabaseOfficerUnenrolRequest) error
}
```

### Types

```go
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
```

`DatabaseInfo.CompactionUnavailable` reports that the server could not read the
policy from the store: `Compaction` is then unknown, not off. `Name` is
`json:"-"` on the update and scale requests: it travels in the path, not the
body.

### Types (target)

`DatabaseInfo` gains `Binding *DatabaseBinding` (`json:"binding,omitempty"`),
read-only: it reports what the resolver decided, so an operator can see where a
database's data lives without reading the server's configuration.
`DatabaseRaftConfig` gains `StateStorage string`
(`json:"stateStorage,omitempty"`: `pebble | file | memory`) and carries no shard
ID, which the cluster issues. `UpdateDatabaseConnection`,
`UpdateDatabaseConnectionRequest`, `DatabaseConnectionConfig`, the `Connection`
field and remote mode are removed.

```go
// DatabaseBinding is the resolved placement of the database's storage.
type DatabaseBinding struct {
	Kind       string   `json:"kind"`            // local | raft
	Nodes      []string `json:"nodes,omitempty"` // owning node (local) or replica nodes (raft)
	Encryption string   `json:"encryption"`      // node | tenant | tenant+database
	Generation uint64   `json:"generation"`      // increments on every create of this name
}
```

### Server-side binding

`pkg/database/binding.go`:

```go
type BindingKind string

const (
	BindingLocal BindingKind = "local" // engine-direct host over the database's own store
	BindingRaft  BindingKind = "raft"  // the database's Raft group
)

type EncryptorScope string // whose key encrypts the store at rest

const (
	EncryptorNode   EncryptorScope = "node"   // prefix tenants: the node at-rest encryptor
	EncryptorTenant EncryptorScope = "tenant" // barrier, volume and raft tenants: the tenant barrier
)

type RaftHostScope string

const (
	RaftHostSystem RaftHostScope = "system" // the node's system host and shared log
	RaftHostTenant RaftHostScope = "tenant" // a volume-isolated tenant's own host
)

const DefaultRaftStateStorage = BackendPebble

// Defaults fill fields a create left empty.
type Defaults struct {
	Backend          BackendType // empty: raft in raft mode, pebble standalone
	Replicas         int         // zero: one
	RaftClusterMode  RaftClusterMode
	RaftStateStorage BackendType // empty: DefaultRaftStateStorage
}

type BindingNode struct {
	DataDir  string // root of prefix- and barrier-isolated tenants' stores
	RaftMode bool   // the node hosts Raft groups
	Defaults Defaults
}

type BindingTenant struct {
	ID        string
	Isolation tenant.IsolationType // empty is prefix
	DataRoot  string               // volume and raft tenants only
}

type RaftBinding struct {
	Host         RaftHostScope
	DedicatedWAL bool // the group's log is the raft tenant's own WAL
	Replicas     int
	ClusterMode  RaftClusterMode
	StateStorage BackendType // pebble | file | memory
}

// Binding is the resolved, concrete placement of one (tenant, database).
type Binding struct {
	TenantID        string
	Database        string
	Kind            BindingKind
	Backend         BackendType
	DataRoot        string // empty for a memory store
	Encryptor       EncryptorScope
	DatabaseBarrier bool         // a per-database barrier is layered on the store
	Raft            *RaftBinding // BindingRaft only
}

func (b *Binding) TenantBarrierBound() bool

// ResolveBinding is pure: no I/O. Every refusal is a typed error, returned
// before anything is created.
func ResolveBinding(cfg *DatabaseConfig, t BindingTenant, node BindingNode) (*Binding, error)
```

**Target.** `pkg/server` materializes a `Binding` into an open store, a
`service.Host` (`backend.LocalBackend` for `BindingLocal`, the Raft shard for
`BindingRaft`), a watch hub and a lease driver, then calls the **one** bundle
builder, `buildDataPlaneServices` extended with the vector service, over that
host. The bundle is cached per `(tenant, database, generation)`.
`buildDedicatedStoreServices`, `service.NewNamedBackendKVService`'s data-plane
use, the in-memory placeholder in `openStorage` and the node-local vector store
in `pkg/server/lifecycle` are deleted, not kept beside the new path.

### Drop fence surface

`pkg/database` — the drop seam and its refusals:

```go
type KeyspaceReclaimer interface {
	ReclaimDatabaseKeyspace(ctx context.Context, tenantID, database, incarnation string) error
	FenceDatabaseKeyspace(ctx context.Context, tenantID, database string) error
	UnfenceDatabaseKeyspace(ctx context.Context, tenantID, database, incarnation string) error
}

func (d *Database) Incarnation() string // the value every write to d is guarded by
func NewIncarnation() (string, error)   // 128 random bits, hex
func DropRollbackContext(ctx context.Context) (context.Context, context.CancelFunc)

func CheckDroppable(name string) error // *DefaultDatabaseDropError for the default database

type DroppingError struct{ Name, TenantID string }
type DefaultDatabaseDropError struct{ Name string }
type KeyspaceFenceError struct {
	Name, TenantID string
	Err            error
	RolledBack     bool // the store fence and then the local one were cleared
}
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
func WithServiceRegistry(reg service.Registry) Option
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
- **Served by**: the three state machines in `pkg/statemachine`
  (`KVStateMachine`, `RegularStateMachine`, `FastStateMachine`),
  `pkg/store`'s `PebbleStateMachine` and
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
  then the resolve-time check. `pkg/backend/raft`'s `HostAdapter` answers from the
  node's `featuregate.Gate` (see
  [../clustering/engineering.md](../clustering/engineering.md), "Cluster
  feature gate"); a Host with no transport, a `LocalBackend`, and a Host
  that does not implement the interface have every replica in this
  process and always send guarded writes.
- **`FastStateMachine` remembers an absent guard key.** A database with no
  incarnation of its own is live while its marker is absent, so every write
  to it would otherwise read storage for the marker; the first miss is
  cached as the tombstone the cache already uses for a deleted key.

## Range delete command

A range delete removes every key in `[start, end)` in one Raft entry:

```
[OpDeleteRange:1] DecodeOp payload: Key = start, RangeEnd = end
```

- **Encoding**: `protocol.EncodeDeleteRange(start, end)`; an empty `end` is
  unbounded above (through the end of the keyspace). Every handler decodes
  it with `statemachine.DecodeDeleteRange`. Bounds that do not describe a
  range — an empty start, an end at or below start — are a
  `*statemachine.InvalidRangeError`, a refusal in the result; a start in the
  reserved `0x00` namespace is a `*statemachine.ReservedKeyError` refusal.
  Neither halts apply, and neither deletes anything. Any other start sorts
  above the whole reserved namespace, so no state machine record is ever in
  a range.
- **It is the batch of single deletes over the live keys in the range when
  the entry applies** — not a storage range tombstone, which would leave
  index entries and lease bindings behind and tell no watcher. Each handler
  lists the keys in the range (seeing earlier entries of the same apply),
  then applies them through the path its own `OpBatch` uses.
- **Listing the range costs the range, on every backend.** The listing reads
  through the apply batch. Pebble's batch is natively indexed; the memory,
  file and objstore backends share `storage.BufferedBatch`, whose iterator
  merges the batch's pending writes, sorted, with a live iterator over the
  committed store, as an LSM merges a memtable with the levels below it. A
  pending range delete is skipped with one seek, never walked, and nothing
  outside the iterator's bounds is read. Inside one batch a range delete
  hides the committed keys it covers and the keys written before it, not
  those written after it; `pkg/storage/batch_range_parity_test.go` holds
  every `Batch` implementation to that against a model, in both directions
  (forward only on the partition manager, whose merge iterator does not
  move backward). The memory backend commits the batch under one write lock
  with a B-tree path hint per leading key byte, so the runs of tombstones,
  metadata deletes and revision-index records an MVCC range delete writes
  each resume their search where the previous write of their family ended.
- **Served by**: `KVStateMachine` on every backend an `sm_type` selects
  (`memory`/`standard`, `concurrent`, `highperf`, `fast`, and `disk` over
  every `sm_storage`), `RegularStateMachine`, `FastStateMachine`,
  `pkg/store`'s `PebbleStateMachine` and
  `MVCCStateMachine`, and `pkg/engine`'s `KVEngine` and `MVCCEngine` on every
  storage backend. `pkg/statemachine/delete_range_test.go` drives the same
  contract through every state machine and backend, so an `sm_type` that
  does not dispatch the opcode fails there rather than in a drop;
  `pkg/engine/delete_range_backends_test.go` does the same for both engines
  over memory, file, pebble and objstore. `pkg/backend`'s
  `SimpleStateMachine`, which no `sm_type` selects, does not serve it and
  refuses it in the result, as it refuses `OpGuarded`.
- **What a reader or watcher observes is what the same single deletes would
  leave, applied together**: each deleted key is gone, and loses whatever
  that state machine's single delete takes away — its MVCC tombstone is
  written, its lease binding released, its secondary index entries removed;
  every watcher of a deleted key receives one `DELETE`
  event, with the previous value where the state machine keeps one, in key
  order. An empty range succeeds, notifies nobody and, on `MVCCEngine`, does
  not advance the revision.
- **The whole range commits at one revision.** This is the one observable
  difference from issuing the single deletes one at a time, and it is
  deliberate: a range delete is one entry, applied as an `OpBatch` or a
  transaction is, so every key's tombstone and every `DELETE` event carries
  the same revision, and no reader at any revision sees some keys of the
  range deleted and others not. It is the semantics etcd gives a
  `DeleteRange`. On a state machine that keeps revisions, a read at a
  revision before the range delete still returns every key in it, and one
  at or after it returns none of them. A caller
  that needs a revision per key issues the single deletes itself.
- **Vector engine records are not client data.** They sort among client
  keys under the `__vec:` prefix, and a range that spans them leaves them —
  and the index built from them — in place on `KVEngine` and
  `PebbleStateMachine` alike; the result counts only the client keys it
  deleted. `MVCCEngine` keeps them outside the revisioned keyspace, so no
  range reaches them. The vector engine drops its own namespaces.
- **Who uses it.** The keyspace reclaim of a dropped database does: range
  deletes of at most 1,024 listed keys each over every prefix it owns,
  through `KVService.DeleteRange`. Two
  other bulk deletes deliberately do not:
  - **TRUNCATE TABLE, DROP TABLE and DROP INDEX** orphan the old keyspace
    in O(1) — a new table generation or index incarnation committed in one
    transaction — and leave its deletion to the `ReclaimSweeper`. The
    sweeper already deletes a page of keys per transaction, one proposal
    per page, and it paces itself: each pass has a key budget, waits out
    the reclaim quiescence period, and resumes from the last key it
    deleted. An unbounded range delete would trade that pacing for one
    entry whose size grows with the table.
  - **DropCollection** deletes each document with a `CompareAndDelete` of
    the value it scanned, because that is the only way to know that this
    drop, and not a racing `DeleteDocument`, removed the document — and so
    to release each document's bytes against the storage quota exactly
    once. A range delete reports how many keys it removed, not which ones
    or their sizes, so it cannot meter the drop.
- **`OpDeleteRange` is only sent to a group that can apply it.**
  `KVService.DeleteRange` asks a `service.FeatureGatedHost` whether
  `featuregate.RangeDelete` is enabled for the shard and returns
  `*featuregate.NotActiveError` without proposing when it is not; a
  `LocalBackend` has every replica in this process and always proposes. The
  keyspace reclaim falls back to one delete per proposal for the rest of the
  prefix on that error (see [architecture.md](architecture.md), "What a drop
  reclaims").

## Verifiable-log head-signing seam

A verifiable log's head-signing keys come from a composer-supplied
`vlog.HeadSignerProvider` ([architecture.md](architecture.md), "The sign-head
seam"); the service never generates a key itself.

| Surface | Contract |
| --- | --- |
| `vlog.HeadSignerProvider` | `CreateKey(ctx, CreateKeyRequest) (CreatedKey, error)` creates one key version for one log; `HeadSigner(ctx, HeadSignerRequest) (HeadSigner, error)` returns the signer for one recorded version. |
| `vlog.CreatedKey` | `Ref`, the provider's reference, recorded in the descriptor; `Public`, the public key, recorded as PKIX DER; `Record`, the sealed `k/<version>` record, set by the software provider only. |
| `vlog.HeadKey` | One descriptor entry: `Version`, `Ref`, `PublicKey`. `Descriptor.Keys` ascends from version 1; the last is current. |
| `server.WithHeadSignerProvider` / `node.WithHeadSignerProvider` | Inject the provider. `ResolveCryptoOptions` surfaces it as `CryptoOptions.HeadSignerProvider`, forwarded into `lifecycle.Infra.HeadSignerProvider`, like `WithDEKProvider`. `sdk/go` re-exports the types (`sdk.HeadSignerProvider`, `sdk.HeadKey`, ...) and `sdk/go/server` the option (`sdk/go/server.WithHeadSignerProvider`). |
| Default | Nil keeps `vlog.NewSoftwareHeadSignerProvider`, which returns the key as the log's sealed record, written in the same proposal as the descriptor (create) or the new key (rotate). |

Invariants:

- A create or rotation lands with its key or not at all: the descriptor entry
  and, for the software provider, the `k/<version>` record are one proposal.
- A provider that keeps its key outside go-qrdb returns no record and none is
  written.
- The provider's answer is checked before propose (`CreatedKey.HeadKey`: a
  supported type, the requested version, a reference, a record that is the
  same key), and every signed head is checked with `Descriptor.VerifyHead`
  before it is recorded.
- A rotation names the next version; any other is refused at apply with
  `KeyVersionConflictError`, a conflict.

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
- **Backend is creation-time and immutable.** Protocol, etcd, raft cluster
  mode, barrier state, connection and replica count are all updatable; the
  backend is not.
- **`--cluster-mode` and `--replicas` apply to `backend: raft` only.**
  `cluster-mode` defaults to `local`, `replicas` to 1.
- **`ScaleDatabase` requires `TargetReplicas >= 1`** and works on
  dedicated-raft databases only. It adds or removes voting replicas in place —
  the database is not recreated.
- **etcd requires a node in mode `raft`, and TLS and auth enabled.** A
  standalone node's listener refuses to start (the etcd listener is raft-only,
  `server.WithStandaloneMode`). Any backend serves it.
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
    through `pkg/server`'s `restServiceRegistry.resolve`, which checks it
    before any isolation-specific routing), so
    this is what stops a write from landing in a keyspace that is already
    being reclaimed and surviving, orphaned, once the catalog record is
    gone. The fence is its own state (`Database.Dropping()`, persisted as
    `DatabaseMeta.Dropping`), independent of `Database.Status()` — Status keeps answering only "is there an open
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
    A database with an empty incarnation (one with no store to mark at
    create) is live while its marker is absent,
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
  - **A claim whose outcome is unknown is released.** When the
    `PutIfAbsent` or `CompareAndSwap` that claims the marker fails — a
    propose that timed out may still be applied after the create has
    rolled back — `UnfenceDatabaseKeyspace` proposes
    `CompareAndSwap(incarnation -> tombstone)` under a context detached
    from the expired one (`database.DropRollbackContext`) before it
    returns. The release is ordered after the claim in the log, so a claim
    that lands is swapped back to the fenced tombstone and one that never
    lands leaves the release a no-op; either way the marker and the
    catalog agree that no database exists, and the retried create claims
    the tombstone. A release that fails too is
    `*service.MarkerReleaseError`, which unwraps to the claim's failure
    (so a timeout stays a retryable 503) and names both errors; the marker
    may then hold the orphaned incarnation until a drop of the name fences
    it.
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
    being discarded by it. `removeReplicaFromGroup` CASes its snapshot onto
    itself before `RemoveFollowerNode`, so a stale snapshot never reaches
    Raft. It then drops the evicted replica with `Store.RemoveReplica`. Raft
    goes first because a replica that leaves the placement is stopped on its
    node, and the eviction needs that replica's vote to commit in a group of
    two voters. A KV with no `AtomicKV` support returns
    `*placement.AtomicWriteUnsupportedError` (the inner error of a
    `*placement.StoreError`) rather than falling back to an unconditional
    `Put`, which would let two concurrent claims both observe `owned=true`.
    `ScaleForTenant` and the
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
    to set it FAILS the whole `Drop`/`DropForTenant` call rather than
    being logged and discarded, so a retry is never stranded believing a
    marker committed that never actually did.
  - **A `NotFoundError` from the manager-level drop is ambiguous, and
    `Placement.DropCommitted` resolves it.** It means either "an earlier
    attempt on this node already finished the local drop" (finish the
    placement teardown) or "this node never had the local record at all"
    (refused upfront, per above, before this branch is reached in
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
    usual lookup.** `database.DroppingError` carries the fenced
    database's tenant ID, recovered before the refusal, so `Drop`'s
    tenant-ID lookup succeeds even when `Manager.Get` itself refuses —
    otherwise a restart mid-drop could never identify which tenant's
    placement entry to finish tearing down.
  - **A cached shard cannot outlive the placement entry it was resolved
    through.** A completed drop's last step deletes the WHOLE placement
    entry, not just its `Dropping` flag, so there is no fence left to read
    at all — indistinguishable, from the resolver's placement lookup alone,
    from a database that never had a placement entry. Each cached
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
    moments later. `meta.json` itself writes through a temp-file/fsync/
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
  (`pkg/storage/objstore/journal.go`) gives it the file backend's
  all-or-nothing-after-recovery guarantee: a commit publishes a single journal object before touching any key, applies
  the batch, then clears the journal, so an interrupted commit is replayed
  in full at the next `New` and one interrupted before the journal became
  durable leaves no trace.

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
  that produced them, on every backend. Time-series points, metric and tag
  indexes and secondary index entries already are; vector records and vector
  metadata indexes are not.
- **New Raft commands are cluster-feature-gated.** The drop fence, the vector
  commands and the retention command are proposed only when the cluster feature
  gate reports every member of the group supports them; otherwise the
  capability is refused with a typed error. A mixed-version group never
  receives a command some member would reject.
- **Every write carries the drop fence.** A write is a guarded command whose
  guard is "this database's fence marker holds its incarnation", evaluated at apply time on
  every replica. This holds for every engine and for local bindings as well as
  raft ones.
- **Every engine uses the binding's encryptor and data root.** An engine that
  obtains an encryptor or a directory from anywhere else — the node's
  `Encryptor`, the node's `DataDir` — is a tenant-isolation defect.
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
cannot be read is refused.

Deliberately finer than one grant per verb:

| Permission | Gates |
|---|---|
| `databases:create` | creating any database — including SQL `CREATE DATABASE`, which creates a database owned by the session's tenant, inside its quota |
| `databases:create_raft` | **in addition** for a raft-backed database — `backend: raft`, or any database of a raft-isolated tenant — because a dedicated Raft group is privileged provisioning |
| `databases:create_volume` | **in addition** for a volume-isolated database — any database of a volume-isolated tenant |
| `databases:read` | reading one database's record |
| `databases:list` | listing databases |
| `databases:update` | all configuration updates: barrier, connection, etcd, protocol, raft |
| `databases:scale` | changing the replication factor — distinct from `update` because it consumes quota and mutates cluster placement and Raft membership |
| `databases:delete` | dropping a database — including SQL `DROP DATABASE`, which may drop only a database the session's tenant owns |
| `databases:seal` / `databases:unseal` | barrier custody, gated apart from configuration |
| `databases:barrier_status` | reading barrier state |
| `databases:officer_register` / `officer_list` / `officer_revoke` | a Shamir barrier's holder enrolment, listing and revocation |
| `databases:officer_unenrol` | removing a Shamir barrier's holder before its custody is initialized |
| `databases:initialize_shamir` | splitting a Shamir barrier's key to its enrolled holders |
| `databases:rekey` | opening a Shamir barrier's rekey round |

**Target.** A create that takes `backend: raft` from the node defaults is
checked for `databases:create_raft` exactly as an explicit one is: the check is
on the resolved record, not on the request.

### Cluster-wide shard IDs

A shard ID names one Raft group on a host every tenant shares, so it must be
unique across the cluster, not only on the node that issued it: two groups
under one ID would collide, and the drop fence relies on a re-created database
never receiving its dropped incarnation's ID.

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
  carries a shard ID: REST, QUIC and MCP have no such field, gRPC reserves
  field 2 of `DatabaseRaftConfig`, and the Go and TypeScript SDKs and the CLI
  have no way to send one. As defence in depth, `Manager.create` refuses a
  non-zero `Raft.ShardID` with `*database.CallerShardIDError` (400 /
  `INVALID_ARGUMENT`), and `Manager.UpdateRaft` keeps the issued ID and refuses
  any other. A caller's ID cannot be checked against what other nodes hold, so
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
  cluster-wide, and the store is not touched. Until then a node of an earlier
  release may allocate per node, and a rolling upgrade would otherwise have a
  window in which two nodes issue one ID. A host with no transport holds only
  this binary and claims at once. **Proof:**
  `TestNewShardClaimer_WaitsForClusterShardIDs`,
  `TestShardIDs_RaftCreateWaitsForClusterShardIDs`,
  `TestClaimShardID_WaitsForActivation`.
- **Feature gate.** `featuregate.ClusterShardIDs` adds no Raft command. Its
  activation opens the claim, and applies the gate's downgrade rule: once every
  node claims, a binary that allocates per node or accepts a caller's shard ID
  can neither start nor be admitted as a replica.
- **No in-place upgrade of a cluster whose raft databases hold per-node shard
  IDs.** Those IDs may collide, and nothing reconciles them with the
  high-water mark: the mark starts at `shard.UserMin` and could re-issue an ID a
  per-node database holds. Such a cluster is rebuilt, not upgraded: create a
  new cluster on this release and move the data across. There is no migration
  and no seed: carrying the per-node allocation forward would carry the defect.
- **Proof.** `TestShardIDs_*` (unit: two managers over one replicated store; a
  caller-chosen ID, including the system shard, refused; raft provisioning
  refused; an update that names another shard refused; a restore of the store
  onto a fresh data directory issues no ID twice; a create writes nothing to
  the node's data directory but the database's own entry) and
  `TestIsolationRaft_ShardIDs_*` (three real nodes, in the full-tier
  `multi-tenancy` leg of the `integration-features` job: distinct IDs for databases created through different nodes, and
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
| A create does not finish within the service's create deadline (`service.DefaultDatabaseCreateTimeout`, 10 s, bounding `CreateWithCredentials` and `CreateForTenantWithCredentials` and so every transport) because a consensus step has no leader or quorum | `service.DatabaseCreateTimeoutError` wrapping the step's error, 503 / `UNAVAILABLE`; the quota reservations, the half-created database and its placement are rolled back under a context detached from the expired one (`database.DropRollbackContext`), so the create is retryable. A caller's own deadline or cancellation that ends the create first is returned as it is. A dedicated shard started on the way waits for its leader under the same context, so the deadline bounds that wait too | **yes** |
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
| Target: `file`/`objstore` for a volume/raft tenant, as backend or raft state storage | `database.BackendIsolationConflictError` | **yes** |
| Target: a tenant isolation mode the binding table does not know | `database.InvalidTenantIsolationError` | **yes** |
| Target: the store's directory cannot be placed (volume/raft tenant without a data root on this node, node without a data directory) | `database.BindingRootUnresolvedError` | **yes** |
| Target: a raft state storage other than `pebble`, `file`, `memory` | `database.InvalidRaftStateStorageError` | **yes** |
| Target: the owning node of a local-binding database is unreachable | a typed error naming the owner; 503 / `UNAVAILABLE` | **yes** |
| Target: a binding cannot be materialized (store will not open, shard will not start) | a typed error wrapping the cause; 503 / `UNAVAILABLE` | **yes** |
| A range delete or time-series retention command in a group where `featuregate.RangeDelete` or `featuregate.TimeSeriesRetention` is not active cluster-wide | `*featuregate.NotActiveError`; nothing is proposed | **yes** |
| Target: a vector command in a group where `featuregate.VectorEngine` is not active cluster-wide | `*featuregate.NotActiveError`; nothing is proposed | **yes** |
| A REST create body carrying `engine` or any other unknown member | `rest.ReadBodyError`, 400 | **yes** |

Each `database.*Error` above is a struct type in `pkg/database/errors.go` with
the fields that identify the database and the conflicting setting. Its status
on every transport comes from one table, `pkg/api/errclass/table.go`.

## Configuration

Databases are created and updated through the API; the daemon's `databases:`
section provisions a list at boot, each entry with its own settings.

### Target: node defaults

```yaml
database_defaults:
  backend: raft            # when unset: raft in raft mode, pebble in standalone
  replicas: 3              # raft only
  raft:
    cluster_mode: distributed
    state_storage: pebble

databases:                 # provisioned at boot; each entry is a full create
  - name: app
    backend: pebble
  - name: metrics
    backend: raft
    replicas: 3
```

A `databases[]` entry has no `engine` key: a config file carrying it, or any
other key the configuration does not declare, is refused by `pkg/config`'s
strict decode over the whole file (`validateKnownFields`). The node's
control-plane store is configured by `raft.sm_type` / `raft.sm_storage` in raft
mode and `storage.backend` in standalone mode, and holds no database other than
the system tenant's `system` database.

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
metadata (`meta.json`, `incarnation`); metadata with no incarnation reopens
with an empty one.

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

The replicated catalog record — the placement table extended to
every backend — carries the full database record: backend, raft options with
the resolved state storage, replicas, data directory, barrier settings,
protocols, the owning node for a local binding, and the generation. `meta.json`
becomes a node-local cache rebuilt from it at boot.

**A local binding is a degenerate, one-replica placement (shipped).** Rather
than a second record shape, a local binding is `Placement{RF:
1, Replicas: []Replica{owner}, LeaderReplicaID: owner.ReplicaID, Backend:
<non-raft>}`, carrying a real `ShardID` claimed from the same cluster-wide
space (`shard.Claimer`) dedicated Raft groups draw from. `Placement.Backend`
(empty means `BackendRaft`) and
`Placement.IsLocalBinding()` (`Backend != "" && Backend != BackendRaft`) are
all a caller needs: everything that resolves or forwards a
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
than overwriting it, so a database created on one node resolves to the same
binding everywhere. It is unsupported (`*StoreError` wrapping
`*AtomicWriteUnsupportedError`) against a KV that does not implement
`PutIfAbsent`.

The per-node reconciler (`TenantShardReconciler`) skips every placement
`IsLocalBinding()` reports true for, in both its poll (`ReconcileOnce`) and
watch (`handleEvent`) paths: a local binding has no Raft group for it to
attach, start or tear down. The owning node opens it from `meta.json`;
**Target:** it materializes it through database resolution.

### No `engine` field

Every database serves every engine, so nothing records one.

- `DatabaseMeta` and `DatabaseConfig` have no `Engine` field, and there is no
  engine type.
- `readMeta` decodes `meta.json` strictly (`json.Decoder.DisallowUnknownFields`):
  a file carrying `"engine"`, or any other field this version does not
  understand, is refused with `*database.LegacyMetadataError` naming the path
  and the decode failure. Nothing is stripped or rewritten and there is no
  migration; the database is recreated. A `meta.json` this version wrote never
  carries the key, so the check costs nothing on the common path.
- The protobuf field 2 of `DatabaseCreateRequest` and `DatabaseInfoResponse` is
  `reserved 2; reserved "engine";`.
- Every request is decoded strictly, by a generic rule rather than an
  engine-specific check: REST and QUIC decode the whole body with
  `json.Decoder.DisallowUnknownFields`, and the MCP create tool checks its
  untyped argument map against its declared arguments
  (`wire.RejectUnknownArgs`). gRPC and the Unix socket, which serves the same
  protobuf messages, need no runtime check: the generated request type has no
  `Engine` member. The embedded transport and `sdk/go` have no `Engine` field on
  `transport.CreateDatabaseRequest` or `DatabaseInfo`. The CLI has no
  `--engine` flag, so cobra reports it as unknown. The `databases:`
  configuration section is covered by `pkg/config`'s strict decode
  (`validateKnownFields`).

## Target: migrating existing data

Data written before bindings lives in three places. Shared-shard data is
relocated automatically, online and fenced; node-local vector data is imported
by an operator command with a conflict policy.

| Where it lives | What it holds | Target |
|---|---|---|
| The shared system shard (raft mode) or the node's single local store (standalone), under `<tenant>/<db>/` and inside the `ts:` / `idx:metric:` / `retention:` prefixes | KV, MVCC, SQL catalog and rows, sequences and generation keys, collections, secondary index entries, time-series points and metric index and retention | relocated per database into its binding |
| A volume tenant's shared shard, under `<tenant>/<db>/` | the same | relocated per database into its binding |
| The node-local vector store at `{DataDir}/vector` | every tenant's vector namespaces, records and metadata | imported per `(tenant, database)` |

**Relocation of one database**:

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
work, which is idempotent. Collection records with no tenant scope are not
relocated, for the reason
[architecture.md](architecture.md) gives for not reclaiming them.

**Vector import**: `qrdb admin migrate vectors --data-dir <dir>
--on-conflict fail|newest|skip`, run against a stopped node's store directory
or a running node's local store. For each physical namespace it decodes the
`<tenant>/<db>/` prefix and the tenant scope, resolves the target database, and
proposes namespace creates and batch upserts through that database's binding,
preserving IDs, metadata and timestamps. A namespace whose prefix names no
existing database is reported and skipped, never guessed. Conflicts are
records whose ID already exists in the target with different content.

## Target: acceptance

Each requirement lands with the tests named here.

- **Binding resolution (shipped).** `database.ResolveBinding(cfg,
  BindingTenant, BindingNode)` (`pkg/database/binding.go`) is the pure function
  of "Resolution model" in [architecture.md](architecture.md): no I/O, a
  `*database.Binding` or one of the typed refusals of "Combinations that are
  refused". The isolation table and the backend-to-kind table are maps
  (`isolationPlacements`, `bindingKinds`), so a new mode or backend is one row.
  `database.Defaults` (in `BindingNode`) fills only a field the record left
  empty: the backend from the node default, else `raft` in raft mode and
  `pebble` standalone; for a raft binding the replicas (else 1), cluster mode
  and state storage (else `pebble`). The refusal of `file` covers a raft
  group's state storage as well as the backend. An explicit `data_dir` of a
  volume/raft tenant lies strictly beneath the tenant root, which itself holds
  the tenant's other state. `TestResolveBinding_IsolationByBackend` covers every
  isolation mode × backend cell, asserting the binding or the typed refusal.
- **Binding materialization.** Create resolves the binding and records it,
  applying defaults once. `pkg/server` materializes a binding once per node --
  the store at the binding's data root under the binding's encryptor, host,
  watch hub, lease driver -- and builds one bundle over it, with the vector
  service in every bundle, cached per (tenant, database, generation).
  `restServiceRegistry.resolve` serves every database from its binding. The
  node-local vector store and `NewLocalStorage`'s node-wide use are deleted.
  Tests: (1) for every allowed cell, a round trip of every engine whose written
  bytes are found **by exact key in that database's own store**, never by
  listing and never through the service under test; (2) opening a tenant
  database's store with the node encryptor fails, with the tenant's succeeds;
  (3) sealing the tenant barrier, and separately the database barrier, fails
  every engine closed, vector and time series included; (4) a prefix-tenant
  `backend: raft` database with a barrier: sealing the barrier fails its data
  closed.
- **Catalog authority for every backend.** A database created on one node
  resolves to the same binding on every node; a local-binding database's
  request on a non-owner node is served by the owner; with the owner down it
  fails with the typed unavailable error.
- **Drop under a binding.** A writer racing a drop on every engine, vector and
  time series included, is refused and nothing it wrote survives; after the
  drop the store directory, object prefix, Raft group on every replica and
  vector index checkpoints are gone; creating the name again gives an empty
  database with a new generation.
- **Node defaults.** `database_defaults` replaces the standalone
  `databases[0].backend` node-wide override; SQL `CREATE DATABASE` and
  `--sql-database` create through the defaults; `storage.backend` and
  `raft.sm_type` configure the control plane only. The capability report does
  not gate MVCC, transactions, leases, watches or locks on raft mode, since
  every database serves them; cluster, backup, rebalance and etcd remain
  raft-mode capabilities. Test: a standalone node provisioned with four
  databases on memory, file, pebble and objstore puts each database's files
  where its binding says and nowhere else, and the control-plane store holds
  none of them; SQL `CREATE DATABASE` produces the default backend; changing
  `--sm` changes no database's backend.
- **Relocation.** Data written before bindings -- KV, SQL tables with sequences
  and generation-scoped keys, collections, secondary indexes, time series with
  tags and retention -- reads back identically after relocation; the source
  keyspace is empty by exact-key probe; a write during relocation is refused
  retryably and succeeds after; a crash injected at each step recovers.
- **Vector import.** A node-local fixture imports into the right databases with
  IDs and metadata preserved; each conflict policy behaves as specified; an
  unattributable namespace is reported and skipped.

## Target: surface impact across the six transports

Every row applies to REST, gRPC, Unix, QUIC, MCP and embedded alike, plus the
CLI and the SDK types. A row that is not on all six is a parity defect.

| Surface | Change | Breaking |
|---|---|---|
| Database create | `backend` optional (node defaults); `raft.state_storage` added | no |
| Database info / list | read-only `binding` block added | no |
| Update connection | removed, with remote mode | yes |
| Update raft | `state_storage` is creation-time; refused on update | no |
| Vector, every request that carries `shard_id` | `shard_id` removed | yes |
| Vector, every capability | served for every database on every backend, including volume and raft tenants | no |
| MVCC, transactions, leases, watches, locks | served for every database on every node mode | no |
| Capabilities report | MVCC, transaction, lease, watch, lock not raft-mode gated | no |
| SQL `CREATE DATABASE` | backend from defaults | no |
| SQL engine tables | every database exposes `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`, `qrdb_vector_vectors` | yes |
| Configuration | `database_defaults` added; `databases[0].backend` not node-wide | yes |
| Admin | `qrdb admin migrate vectors` added | no |

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
- SDK: `transport.DatabaseClient` carries the ten holder methods (unenrolment
  included); the request types
  embed the system holder requests. `internal/holderconv.DatabaseHolders`
  carries the protobuf calls once for gRPC, Unix and QUIC (QUIC frames the same
  messages through a `DatabaseHolderRPC` of its own). A client refuses a
  request naming no database with `DatabaseNameRequiredError`.
- CLI and desktop: the holder verbs are written once against `holderTarget`;
  `holderKind` (system, tenant, database) selects the scope flag and target by
  map. The desktop `OfficerBinding` takes `HolderScope.Database` and has
  `InitializeDatabaseShamir` and `UnenrolDatabaseHolder`; the CLI has
  `qrdb database barrier-officer unenrol` for the database scope.
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
  (`pkg/server`'s `restServiceRegistry.resolve` refuses a database
  pre-registered in the server-level registry — the one path that does not call
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
  fails on a placement entry that does not exist;
  `RaftBackendFactory.Services` refuses with `DroppingError` on both the cold-start path and a
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
  same role is played by binding-materialization test (4).
- **The reclamation has a test against a real engine and a real key/value
  service**, not only against in-memory doubles, and it probes residue by
  exact key rather than by listing. Both properties are load bearing: a double
  that skipped the discovery guard, and an assertion that listed through it,
  each independently hide a teardown that deletes nothing. Doubles standing in
  for the key/value service apply the guard for the same reason.
- **Target: bindings are tested at the store, not at the service.** Every
  placement assertion opens the database's own store and probes by exact key.
  A test that writes through a service and reads back through the same service
  passes whether the data landed in the right store or the shared one.
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
`DatabaseInfo`) is **off unless its owning tenant set one**, and is set only by
`UpdateDatabaseCompaction` on any transport, which requires `mvcc:compact`
(`ScopeSelfStrict`) and refuses -- as not found -- a database the caller's
tenant does not own; the system tenant owns only its own databases
(`service.DatabaseService.UpdateCompactionForTenant` applies that rule to
every caller). The node's configuration file has no compaction key.

Create never takes a policy, so `databases:create` cannot compact. The policy's
semantics, and the scheduler that runs it, are specified in
[../key-value/engineering.md](../key-value/engineering.md) "Scheduled
compaction".
