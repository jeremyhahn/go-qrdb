# Run Modes — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Exported surface

`sdk/go/node`:

```go
func NewStandaloneNode(...) (*Node, error)   // standalone composition
func NewClusteredNode(...)  (*Node, error)   // raft composition

func (n *Node) Server()      *server.Server   // the started multi-protocol server
func (n *Node) Client()      *sdk.Client      // in-process SDK client
func (n *Node) Services()    *lifecycle.Node  // aggregated service instances
func (n *Node) KVService()    service.KVServicer
func (n *Node) AdminService() service.AdminServicer
func (n *Node) Close()       error            // tears down in reverse order
```

`pkg/capabilities`:

```go
func Register(feature string)        // called from tag-gated init()
func Seal(profile ...)               // composition root seals the effective profile
func Get(feature string) bool        // pre-seal, falls back to compile-time
```

`pkg/server/lifecycle` (the one raft-free composition every root shares):

```go
func NewLocalStorage(backend database.BackendType, dataDir string, fs vfs.FS, obj config.ObjstoreConfig) (storage.Storage, *pebble.DB, error)
func NewLocalEngine(backend database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub, lr *lease.Lessor) (backend.SMAdapter, error)
func StartLocalLeaseExpiry(ctx context.Context, lr *lease.Lessor, leaseSvc service.LeaseServicer, logger *slog.Logger) (stop func())
```

`hub` and `lr` MUST be the instances the composer builds its watch service on
and runs the expiry sweeper over. `NewLocalEngine` gained `lr` on 2026-10-01
(it used to build a private lessor nothing swept); a caller passes `nil` only
for a composition that serves no leases. `stop` cancels the sweeper and
waits for it to exit; every composer calls it before closing the backend the
sweeper revokes through -- the daemon on shutdown, `node.Node` on `Close` and on
an encryption-migration teardown, `standalone.Server.Shutdown`, and the `qrdb`
GUI's `StopEmbedded`.

The daemon selects the mode with `mode: standalone | raft` in YAML or
`--mode standalone`; **absent means raft**, for full backward compatibility.

## Invariants

- **One canonical composition per mode.** SDK `node://`
  (`node.NewStandaloneNode`), the flat `embedded://` driver, and the daemon all
  share it (`lifecycle.NewLocalStorage` / `NewLocalEngine` →
  `backend.LocalBackend`). There is no second standalone assembly.
- **Precedence is build tag > run mode > config.** Config narrows only. Asking
  config to enable what the mode forbids or the binary lacks is a **validation
  error**, not a silent no-op.
- **`pkg/capabilities` is the single runtime truth.** At boot the composition
  root seals the effective profile (build tags ∩ mode ∩ config) via
  `capabilities.Seal(cfg.EffectiveCapabilities())`. `admin capabilities` reports
  it faithfully, including a `mode` field.
- **A `false` from the capability registry is three-way ambiguous** by design:
  not compiled, mode-gated, or config-disabled. The `mode` field plus the build
  is what disambiguates.
- **Before sealing, `Get` keeps the compile-time fallback** so a library
  embedding that never seals still gets correct answers.
- **There is no `qrdb_raft` build tag.** Raft is always compiled; the mode
  selects it.
- **Raft-only refusals are typed and name the remedy** — `requires mode: raft` —
  on every transport, not a generic error. The raft-only set is cluster-shaped:
  membership, rebalance, shard backup/restore, IsolationRaft, and the etcd
  listener.
- **The data plane is the same in both modes.** MVCC, watch, lease and lock are
  composed over the same engine, lessor and watch hub as raft mode's MVCC state
  machine, resolved through the same per-(tenant, db) registry, and exercised
  by one conformance suite against both modes on all six transports.
- **A standalone node always runs its lease-expiry sweeper**
  (`lifecycle.StartLocalLeaseExpiry`), in the daemon, `node.NewStandaloneNode`
  and the flat `standalone.NewServer` alike. A composition that grants leases
  without sweeping them is one-directional: it can fence a dead holder and
  never elect a replacement.
- **Standalone skips the raft identity requirements**; only `raft.data_dir`
  stays mandatory.
- **A raft node's start never waits on one unanswered request.** A restarted
  node can boot while its cluster elects or warms up a leader, and a settling
  leader drops a forwarded read or registration without answering it. Every
  leader wait, startup read and registration before the node serves `/health`
  is therefore bounded per attempt, retried while the cluster converges, and
  reported at WARN every 5s with what it waits for; a wait that cannot succeed
  fails the start with a typed error (`*server.StartupReadError`,
  `*server.ShardLeaderWaitError`, `*server.ShardReadWaitError`). Database
  provisioning is the deliberate exception: a cut-off write could still commit
  and race its retry, so it stays on the boot's context. Standalone has no
  peer to wait for. The contract, the budgets and the log lines are in
  [../clustering/engineering.md](../clustering/engineering.md) (Invariants).
- **Every composer can keep the compliance audit trail.** The flat
  `standalone.NewServer` keeps it when `ServerConfig.AuditTrail` names its file
  (relative to `DataDir`; `audit.DefaultTrailFile` is qrdbd's name; empty,
  the default, keeps none): `audit.OpenTrailIn`, confined to `DataDir` -- or
  an absolute path's own directory -- so a symbolic link cannot carry it
  outside, opened before anything else,
  failing the start with `*embedded.AuditTrailError` when it cannot be opened,
  closed last by `Shutdown`. Its barrier records on it
  (`seal.NewComplianceAuditLogger`, the system tenant) and the transport
  serves the audit query from it. It has no share-holder authority, so there
  are no holder events to wire. See [../audit/](../audit/).
- **Every no-argument `*Node` accessor performs a race-free read** from any
  goroutine at any point — a fully constructed value or nil, never a torn one.
  **`Close` blocks on an in-flight `Unseal` boot or system `EnableEncryption`**
  and then tears down what it built; a boot or migration that arrives after it
  is refused with `node.ErrNodeClosed`. See *Node accessor concurrency* below.

## Node accessor concurrency

Two guarantees, with different mechanisms: accessor reads are race-free, and
`Close` is ordered against the boot and migration that publish what they read.

### Guaranteed: accessor reads are race-free

Every **no-argument exported accessor** on `*Node` — `Client`, `Server`,
`Services`, `KVService`, `AdminService`, `RebalanceService`, `UserService`,
`AuthFactorService`, `UserAuthFactorService`, `ABACPolicyReloader`,
`BarrierRegistry`, `IsSealed` — performs a race-free read from any goroutine at
any point. It returns a fully constructed value or nil, never a torn or
unsynchronised one.

This matters because a node built with a barrier that needs operator input is
returned to the embedder **before its data plane exists**: `IsSealed()` is true
and the data-plane accessors return nil. `Services()` is the near-exception —
it returns the seal-only `lifecycle.Node` the constructor built, with `Barrier`,
`Ceremony` and `Credential` populated and the data-plane services nil — but the
same boot republishes it with the full graph, so it is inside this contract.
Those documented nils are an invitation to hold the node in one goroutine and
poll it while a second drives `Unseal` — the sealed-window shape every embedder
uses (`ServeSealedWindow` on one goroutine, the operator's unseal on another).

### Guaranteed: `Close` blocks on an in-flight boot or migration

The owner's ruling is that **`Close` blocks**. Every data-plane transition runs
under one lifecycle gate (`Node.lifeGate`, a one-slot semaphore plus a
closing signal that the first `Close` raises before it waits):

| Writer | Holds the gate for | After `Close` |
|---|---|---|
| `Unseal` | the barrier unseal and the whole boot it completes | returns `node.ErrNodeClosed` |
| system-scope `EnableEncryption` | tear down, re-key and rebuild | returns `node.ErrNodeClosed` |
| tenant-scope `EnableEncryption` | the tenant's recycle — held **shared**, alongside other tenant migrations and a system migration | returns `node.ErrNodeClosed` |
| `Close` | the whole teardown | returns nil (idempotent) |

So `Close` is serialised with `Unseal`, with a system-scope migration, and
with a tenant-scope migration: concurrent with any of them it either waits for
it and tears down what it built, or runs first and the late call is refused.
A tenant-scope migration is admitted through `server.TenantMigrationAdmission`
on the router every transport uses and on the direct `Node.EnableEncryption`
call; it keeps its per-tenant guard and still runs concurrently with other
tenants' migrations. The migration can no longer
reload a barrier `Close` retracted, and nothing is republished after `Close`
returns. `ErrNodeClosed` is a sentinel so a composer can tell "closed" apart
from a wrong credential (`*UnsealError`) or an unsupported migration
(`*MigrationNotSupportedError`). It is `service.ErrNodeClosed`, so a refusal
delivered over a transport is classified there as retryable against another
node: 503 on REST, Unavailable on gRPC, unix, QUIC and MCP, and the sentinel
itself on the embedded transport.

A boot or migration still **waiting** for the gate when `Close` begins is
refused, not queued: waiters select on the slot, the closing signal and their
own context. That is not a courtesy. `Close` stops the servers while it holds
the slot, a graceful stop waits for in-flight requests, and a transport-borne
`EnableEncryption` is one — a plain mutex let the two wait on each other.

`Close` can take as long as what it waits for — a KMS-backed unseal included.
Accessors never take the gate.

### Mechanism

Every such handle lives in an `atomic.Pointer` on `Node`, written by
`wireClientAndServer` (or, for the service graph, by `completeStandaloneBoot` /
`completeClusteredBoot`; the seal handles by the constructor) and retracted by
`Close` / `teardownDataPlane`. Interface-typed handles are boxed, so the
pointer holds `*Iface`.

`atomic.Pointer` rather than a mutex because each handle is an independent
single-word publication with no invariant spanning two of them, and reads
outnumber the single write by orders of magnitude — the same reasoning
`pkg/database`, `pkg/secure` and `pkg/storage/pool` apply to their swappable
handles. The lifecycle lock orders the WRITERS; a per-handle publication needs
no mutex.

### Retraction

`Close` retracts every handle it published before stopping what backs them, so
every accessor reads nil afterwards — including after a `Close` that overlapped
a boot or migration — rather than handing out a live-looking handle onto a
stopped service graph.

The in-process encryption-migration recycle (`EnableEncryption` →
`teardownDataPlane`) retracts the handles owned by the service graph it stops —
the custodian factor service, the login factor service, the ABAC reload seam —
and `wireClientAndServer` republishes them on the rebuild. The
Shamir-committed branch of `Reopen` never reaches that rebuild (it returns
sealed, pending a Security Officer quorum), and leaving handles published
through *that* window is precisely the stale-handle hazard this closes.

The scoped encryption router (`Node.encMigrator`, reached through
`EnableEncryption`) is the deliberate exception: it is **not** retracted by
`teardownDataPlane`. Its tenant leg reaches `TenantHostRegistry` and
`RaftBackendFactory`, which survive a system-scope recycle by design — tenant
migrations hold the lifecycle gate only shared, so they do not wait for a system
migration. Clearing
it mid-recycle would turn a concurrent tenant-scope migration, which today
proceeds independently, into a typed "not yet fully booted" refusal. `Close`
clears it directly instead, where no rebuild follows.

The router's tenant leg is wired with a `nodeTenantResolver`
(`sdk/go/node/migrate_encryption.go`), not a captured `svcNode.Tenant`: it
resolves `n.svcNode.Load().Tenant` on every call, so a concurrent tenant-scope
migration during the window always reaches whichever tenant service is
currently live — including the seal-window-only service `Reopen`'s
Shamir-committed branch builds while waiting on the Security Officer quorum —
rather than the one that existed when the router was constructed.

The same rule holds one layer over, for `TenantHostRegistry`
(`n.tenantHosts`). It is built once by `BuildTenantInfra` inside
`completeStandaloneBoot`, guarded by `n.tenantHosts == nil` so a later
system-scope recycle never rebuilds it, and it resolves each tenant's
`DataRoot` through the `server.TenantLookup` it was built with. The node
passes the same `nodeTenantResolver`, never `svcNode.Tenant`, so a
tenant-scope migration issued after a completed system-scope recycle reads the
live tenant service. It previously captured the first boot's
`*tenant.Service`, and every such migration failed its `DataRoot` lookup with
"storage is closed" against the stopped pre-recycle backend.

`server.TenantInfraConfig.TenantLookup` is **required** whenever the
`IsolationVolume` registry is built; `BuildTenantInfra` refuses a
configuration without one (`*TenantInfraConfigError`, field `TenantLookup`)
before any side effect, rather than defaulting to the one-shot `TenantSvc`.
`TenantSvc` is read only by the boot-time passes inside `BuildTenantInfra`.
The daemon, whose tenant service is never swapped, passes the service itself as
the lookup.

`RaftBackendFactory` has no counterpart: it resolves databases through the
database manager and the replicated placement table, and the clustered boot
path that builds it performs no in-process system recycle.

### What none of this promises

It governs **acquiring** a handle, never **using** one. Calling through a handle
you already hold, concurrently with `Close`, is a use-after-close; the node has
never promised to survive that and still does not. The nil-while-sealed
contract is likewise unchanged — this makes the transition safe, not different.

### Verification

The regression tests enumerate the accessors by **reflection over the type**,
not by hand. A hand-list stops covering the next accessor someone adds, which
is exactly how `IsSealed` and `BarrierRegistry` were missed on the first pass.
`Node.encMigrator` has no no-argument accessor and so cannot be reached
reflectively; it is asserted directly.

Measured: two independent `-race` sightings from a consumer
(`Read at Client()` vs `Previous write at wireClientAndServer()`), the second
reproduced at a clean base, which is what attributed the defect here rather
than to the consumer.

## Capability gates

errors, not silent no-ops).

| Gate | Question | Mechanism |
|---|---|---|
| Build tag | Compiled in? | Tag-gated `init()` → `capabilities.Register` (existing: `qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, `codec_*`) |
| Run mode | Structurally possible? | `mode: standalone \| raft` (implemented in config, daemon, and SDK); raft-only features are not composed in standalone |
| Config | Operator-enabled? | Existing sections plus `enabled` on `mvcc`/`watch`/`lease` to disable even in raft mode (implemented) |

Build tags currently registering capabilities: `qrdb_sql`, `qrdb_mysql`,
`qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, plus the
`codec_*` family.

## Standalone: what is and is not served

**Served** — the whole per-database data plane:

- KV (get/put/delete/range/scan), batch, secondary indexes
- Transactions / compare-and-swap (owner ruling 2026-08-23)
- MVCC revisions and `at-revision` reads, watch, lease, lock
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated)
- Plus the always-compiled core below

On the file and objstore backends the engine is the plain KV engine
(`pkg/engine/kv_engine.go`): watch and value/version transactions are served,
and MVCC, lease, lock, time series, vector, revision compares and
lease-carrying writes are refused per call with a typed capability error. A
transaction op naming a lease is refused before anything is staged, so it
writes nothing rather than storing a key that would never expire. The run-modes [API table](api.md#the-storage-engine-not-the-mode-bounds-the-coordination-families)
lists the outcome per family.

**Refused with `requires mode: raft`** — cluster membership and topology,
rebalance, shard backup/restore, IsolationRaft, and the etcd compatibility
listener (`server.WithStandaloneMode` refuses it in `startEtcd`).

*(Corrected twice. This section listed transactions among the refusals until
2026-08-23, and MVCC, watch, lease and lock until 2026-10-01. The refusal used
to be `raftOnlyGatedRegistry`, a wrapper around the transports' service
registry; it was deleted on 2026-10-01, and `embedded.WithStandaloneMode` with it.
`server.WithStandaloneMode` remains, now meaning only the etcd refusal.)*

### How standalone serves the coordination families

- **Composition.** `lifecycle.BuildServices` has always built MVCC, lease,
  watch and lock services over the local host; they were withheld from the
  transports. Each root now passes them through: the daemon
  (`server.WithMVCC/WithLease/WithWatch/WithLockService` in both modes),
  `node.NewStandaloneNode` (the same options, and the embedded transport's
  `WithMVCCService/WithLeaseService/WithWatchService/WithLockService`), and the
  flat `standalone.NewServer`, which also gained the watch hub it was built
  without (its watches registered on a hub nothing published to) and a lock
  service.
- **Lessor.** Each root builds the lessor and passes it to
  `lifecycle.NewLocalEngine`, so the expiry sweeper runs over the lessor the
  engine applies leases into. The engine restores durable leases into it when
  it is constructed — which is how a standalone node keeps its leases across a
  restart.
- **Sweeper lifetime.** `node.NewStandaloneNode` runs one sweeper per data
  plane: an in-process encryption migration's teardown stops it, and the
  rebuild starts one over the new engine's lessor. The daemon's runs for the
  process; `standalone.Server.Shutdown` stops its own before closing the backend.
- **Errors.** The embedded transport now translates the lease or MVCC refusal
  under a lock (`translateLockError`) and `service.MVCCBackendRequiredError` to
  `transport.CapabilityUnsupportedError`, matching the network transports.

## The always-compiled core

Excluded from build tags because removing any of these either violates the
security posture or breaks a dependency of everything else:

| Core component | Why it cannot be optional |
|---|---|
| `pkg/capabilities` | The registry every feature reports through |
| KV + batch + collections (`pkg/kv`, `pkg/kvstore`, engine layer) | Baseline data plane; every feature and the IAM stores depend on it |
| Database manager (`pkg/database`, `pkg/dao`) | Nothing opens without it |
| `pkg/config`, logger, `pkg/errors`, `pkg/firstboot` | Process cannot boot or bootstrap without them |
| Storage backends memory/file/pebble (`pkg/storage`) | No storage, no database; seal + IAM ride them |
| Auth: mTLS, RBAC, IAM stores (`pkg/auth`, `pkg/user`, `pkg/role`, `pkg/permission`) | An unauthenticated binary violates the security posture |
| Seal barrier + credentials (`pkg/seal`, `pkg/credentials`) | At-rest protection is non-negotiable; IAM depends on the barrier |
| Audit (`pkg/audit`) | Compliance posture; services emit unconditionally |
| REST + gRPC transports | A server with no transport is unusable; enabled per `protocols.*` |

This table describes the daemon. An embedder is held to a narrower graph,
described in the next section.

## The lean-standalone import gate

`make check-lean-standalone` runs the gate. It runs
`TestLeanStandaloneImportGraph` and `TestLeanStandaloneEdgeDetection` in
`test/buildgates/lean_standalone_test.go`, and the CI Lint job runs the same
target. The gate works as follows:

- **Fixture.** `sdk/go/examples/leanstandalone` is a standalone `file`-backend
  node built by `node.NewStandaloneNode` with REST and gRPC off. It is reached
  through `Node.Client()` (the embedded transport), runs as a service
  principal (`node.WithServicePrincipal` with a role grant of the `kv:*` and
  `index:*` permissions it uses), and saves and reads one entity through
  `sdk.NewDAO`.
- **Graph.** `go list -deps -json -tags codec_json` runs over the fixture from
  the `sdk/go` module. `codec_json` is the only tag in the set, because the
  DAO's codec needs it at run time. Anything a plain `go build` by an
  embedder would not enable stays out.
- **Forbidden set.** go-quicraft, PebbleDB, `pkg/api`, `pkg/server`,
  `pkg/cluster`, `pkg/vector`, `pkg/timeseries`, mcp-go, the Prometheus
  client, gin, chi and go-objstore. A path matches at a path-segment boundary,
  so `pkg/api` forbids `pkg/api/rest` but not `pkg/apimodule`.
- **What is compared.** The gate compares edges, not packages: every import
  from a package that is not forbidden to one that is. An edge inside the
  forbidden set disappears with the edge that reached it. Comparing edges
  points a failure at the import line to remove, rather than at a package
  reached through some unspecified chain.
- **Ratchet.** `leanStandaloneEdges` lists the remaining edges, grouped with
  the reason for each. A new edge fails the gate. A listed edge that no longer
  occurs also fails it. A change that removes a dependency therefore deletes
  its lines in the same commit, and the table can only shrink.
- **Discovery floors.** The graph must hold at least 200 packages. It must
  also contain the fixture, `sdk/go/node`, `sdk/go/transport/embedded` and
  `pkg/dao`, so an empty or wrong graph cannot pass. `TestLeanStandaloneEdgeDetection`
  runs the edge finder on a synthetic graph whose answer is known.
- **Splits, not tags.** Where a forbidden dependency is structural — a package
  that both a lean and a full composition import, holding code only the full
  one needs — the fix is to move that code to its own package, not to put it
  behind a build tag. The first such move took `NewServer` and its
  `ServerConfig`, `BarrierConfig` and constructor error types out of
  `sdk/go/transport/embedded` into `sdk/go/standalone`, and removed the
  transport's edges to PebbleDB and `pkg/server/lifecycle`.
  `BarrierSealedError` stayed in the transport, whose seal gate returns it.
  The second moved the index over a raw `*pebble.DB` out of `pkg/index` into
  `pkg/index/pebbleindex`, with the key layout shared through the internal
  `pkg/index/internal/indexkey`. `pkg/index` — which the DAO imports — no
  longer imports PebbleDB; the moved package is reached only through the Raft
  state machine in `pkg/store`.

## Storage selection in raft mode

`raft.sm_storage` selects the backend the storage-generic state machines
(`sm_type: disk` or `fast`) compose: `pebble` (default), `file`, or `memory`.

- **Storage is passed per resolution, never held in package state.**
  `statemachine.ResolveTypedWithStorage` takes a `StorageConfig` carrying
  `raft.data_dir` and `raft.sm_storage`; `qrdbd` and
  `sdk/go/node.NewClusteredNode` both pass it, so two nodes in one process each
  keep their storage under their own data directory. Which types are
  storage-generic is the registry's answer (`Descriptor.StorageGeneric`), and a
  non-pebble `sm_storage` for any other type is refused at resolution
  (`SMStorageNotApplicableError`) rather than ignored.
- **Under the seal barrier, storage-generic state machines are encrypted at
  rest or refused.** `StorageConfig.Encryptor` carries the node's at-rest
  encryptor. When it is enabled, `pebble` storage is opened on the barrier's
  encfs — the same wrapping the `pebble` and `mvcc` state machines get — and
  `file` storage, which has no encrypting seam, is refused at resolution
  (`SMStorageUnencryptedError`), at `qrdbd start` and in the SDK alike.
  `memory` storage persists nothing and is allowed. The factory opens storage
  when the replica starts, after the barrier is unsealed.
- **Every `sm_type` passes the start-time guarded-batch probe**
  (`requireGuardedBatch` in `cmd/qrdbd/start.go` over
  `kvstore.ProbeGuardedBatch`). The probe stays: it is what refuses a state
  machine registered without a transaction plane, and none in this tree is.

- **The file backend earned the hot-apply role with real durability**: atomic
  temp-file + rename + fsync writes and an all-or-nothing commit journal
  replayed at open (`pkg/storage/file/journal.go`), plus the `storage.Syncer`
  seam.
- **Objstore is a snapshot/backup tier** and is **refused as a hot apply path**
  with a pointed validation error.
- **Every state machine other than `disk` and `fast` carries its own
  storage** and rejects a non-pebble `sm_storage`.

## Configuration

```yaml
mode: raft   # "standalone" | "raft"; absent = raft (full back-compat)
```

- `mode: standalone` + `raft.join` or a multi-member `raft.peers` list →
  validation error: `requires mode: raft (a standalone node has no cluster to
  join)`. `mvcc.enabled`, `watch.enabled` and `lease.enabled` are valid in
  both modes and narrow either one *(corrected: enabling them under standalone
  was a validation error until 2026-10-01)*. Standalone skips the raft identity
  requirements (only `raft.data_dir` stays mandatory). Etcd is enabled
  per-database at runtime, so its mode gate lives at service composition.
- Enabling a wire protocol whose build tag is absent → boot-time error
  naming the tag (`Config.ValidateCapabilities`, called by composition
  roots — a deliberate separate gate so pure-config validation stays
  build-independent).
- The daemon serves `mode: standalone` natively: the data-plane families are
  wired exactly as in raft mode, and the cluster-shaped families answer
  501/Unimplemented with the `requires mode: raft` hint on every transport
  (their singletons are not passed in this mode).
- Refusals at runtime surface through the existing per-transport typed
  errors (REST 501 / gRPC & QUIC Unimplemented) with the
  `requires mode: raft` hint.


## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Raft-only call in standalone | typed error, `requires mode: raft`; REST 501, gRPC/QUIC `Unimplemented` | **yes** |
| `mode: standalone` with `raft.join` or multi-member `raft.peers` | validation error at boot | **yes** |
| Lease, lock, MVCC, a revision compare or a lease-carrying write on a standalone file/objstore backend | `CapabilityUnsupportedError` per call; nothing is written | **yes** |
| Wire protocol enabled whose build tag is absent | boot-time error **naming the tag** (`Config.ValidateCapabilities`) | **yes** |
| Objstore configured as `raft.sm_storage` | validation error | **yes** |
| Non-pebble `sm_storage` with a pebble-native state machine | rejected | **yes** |

`Config.ValidateCapabilities` is a deliberately separate gate from ordinary
config validation, so pure-config validation stays build-independent.

## Behavior change notice

**2026-10-13: the flat composer moved to `sdk/go/standalone`.**
`embedded.NewServer`, `embedded.Server`, `embedded.ServerConfig`,
`embedded.BarrierConfig`, `embedded.ErrShamirAutoUnseal` and the server's
constructor error types are now `standalone.NewServer`, `standalone.Server`,
and so on, with the same fields and behaviour. Nothing is left at the old
names. The embedded transport (`embedded.New`, its options and
`embedded.BarrierSealedError`) did not move.

**2026-10-01: standalone serves MVCC, watch, lease and lock.**
Code that relied on the refusal — a host's in-process fallback, a check for
`requires mode: raft` — sees the families served instead. Capability reports
on a standalone node now say `mvcc`, `watch`, `lease` and `lock` true. The
exported `embedded.WithStandaloneMode` is removed, and
`lifecycle.NewLocalEngine` takes the lessor.

### Earlier notice

SDK standalone (`NewStandaloneNode`) historically composed MVCC, lease,
and transaction services over `LocalBackend`. Under this requirement **MVCC and
lease** become raft-mode-only everywhere; standalone compositions stop building
them. Downstream consumers were notified on their own repositories.

**Transactions did not become raft-only** (owner ruling 2026-08-23), and this
notice said they did — corrected to match the shipped composition. Standalone
still serves the transaction / compare-and-swap family on every transport,
in-process and on the wire; `pkg/server/standalone_gate.go` passes
`TransactionService` through by name and says why.

## Test strategy

- **Unit**: mode validation for each rejected combination, capability sealing
  (build ∩ mode ∩ config), the pre-seal compile-time fallback, the etcd
  refusal, and `StartLocalLeaseExpiry` deleting an expired lease's key.
- **Parity, not refusal, is the important property now.**
  `sdk/go/conformance`'s `TestCoordinationFamilies_SameOnStandaloneAndRaft`
  runs one suite against a standalone memory, file and pebble node and a raft
  node on all six transports (and an objstore node embedded): lease lifecycle,
  lease expiry deleting its key (proves the sweeper is wired), prefix watch,
  revision resume, cancellation, lock lifecycle. Every backend composes the
  MVCC engine. Before this change every standalone cell failed with
  `requires mode: raft`.
- **Restart**: `sdk/go/node`'s `TestStandaloneNode_LeaseAndLockSurviveRestart`
  closes a standalone pebble node holding a lease and a lock and reopens it.
- **Accessor publication is verified under `-race` only.** An unsynchronised
  word read almost never observes a stale value on amd64/arm64, so
  `sdk/go/node`'s `TestNode_AccessorsRaceFreeDuringUnseal` and
  `TestNode_AccessorsRaceFreeDuringClose` are evidence only when the detector
  is on; `make test-sdk` runs them that way (`GOTEST` carries `-race`).
- **Retraction is verified with the optional handles populated.**
  `TestNode_CloseRetractsEveryHandle_Configured` boots with ABAC live reload so
  `ABACPolicyReloader` and the login factor service are non-nil *before*
  `Close`; asserting nil afterwards on a node that never populated them would
  be vacuous.
- **Integration**: the standalone stage runs the standalone composition; the
  cluster stage runs raft. CI runs the cluster stage's tests in
  the `integration-features` job (`.github/workflows/ci.yml`), one leg per feature (`make integration-test-<feature>`), required by
  `ci-gate`.

## Observability

`qrdb admin capabilities` is the authoritative runtime report: the effective
profile plus a `mode` field. It is the first thing to check when a feature is
"missing".
