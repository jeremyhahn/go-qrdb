# Databases — Architecture

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> This document has two layers, and each section says which layer it is in.
>
> - **Current state** sections describe the code as shipped and measured. Where
>   the previous revision of this document claimed more than the code does, the
>   claim is corrected here (see "Corrections to earlier revisions").
> - **Target design** sections are the requirements contract for the
>   multi-model database program: every database supports every engine on the
>   storage backend it chose, for every tenant isolation mode. They were
>   approved by the owner on 2026-09-30. Nothing in them is implemented yet.
>   Every design question the rulings did not settle is listed, with its
>   options and the ruling, in "Decisions ruled by the owner" at the end.

## Problem

One store serves several workloads with genuinely different needs: one wants
Raft replication and its own encryption key, another wants a cheap local Pebble
store, a third needs to look like etcd on port 2379, a fourth is an embedding
index that should live next to the documents it was derived from. Forcing them
into one configuration means the strictest requirement is paid for by everyone.

A database is the unit at which those choices are made. For that to mean
anything, the choice a database records has to be the choice its data actually
lives under, for every kind of data it holds.

## Rulings this design implements

These are owner rulings. They are not reopened here.

1. **Databases are multi-model.** Every database supports every engine — plain
   KV, MVCC and transactions, SQL/relational, collections, secondary indexes,
   vector, time series, leases and watches — on its chosen backend.
2. **The storage backend is a database-level option, and nothing hardcodes
   it.** memory, file, pebble, raft and objstore are all per-database choices,
   honoured by every engine. No node flag, no SQL statement and no boot path
   substitutes its own backend for the one the database recorded.
3. **The `engine` field is removed** from the database configuration, the
   persisted metadata and every API.
4. **Vector and time series work the same way as every other engine** and are
   tenant scoped: they live in the database's storage binding, under the
   tenant's encryptor and data root.
5. Custodian and secret-material rulings are unaffected by this design.
6. **The database drop fence ships behind a cluster feature gate**, and so does
   every new Raft command this program adds (the vector commands and the
   time-series retention command). A node proposes a gated command only once the
   gate reports that every member of the group applies it; until then the
   capability that needs it is refused with a typed error rather than proposed
   into a group that would reject it.
7. **Every open decision is ruled.** On 2026-09-30 the owner accepted every
   recommendation in "Decisions ruled by the owner" below (OD-1 to OD-16).

## Current state (measured)

### What decides where a database's data lives today

The per-database `backend` is validated and persisted, and for most databases
it is not what decides where the data goes. The tenant's isolation mode and
node-level settings decide instead.

| Path | What serves the data plane | Engines served | Honours the database's `backend`? |
|---|---|---|---|
| Prefix or barrier tenant, no per-database barrier | the shared system shard, under a `<tenant>/<db>/` key prefix | all except vector (vector goes to the node-local store below) | **no** — the node's `--sm` / `raft.sm_type` in raft mode, or the node's single local store in standalone mode |
| Prefix or barrier tenant, database with its own barrier and an explicit data directory | the database's own store, opened by `database.Manager` | **KV and collections only**; MVCC, batch, transactions, leases, watches, indexes, time series and vector are absent, so the SQL surface and sequences are too | partly — memory, file and pebble are opened; nothing else is |
| Volume tenant | one Raft shard on a per-tenant host, shared by all of the tenant's databases under the `<tenant>/<db>/` prefix | all except vector | **no** |
| Raft tenant | a dedicated Raft group per database on the system host | all except vector | forced to `raft` |

Supporting facts, each located in the code by reading it:

- `database.Manager` opens a per-database store through `openStorage`
  (`pkg/database/factory.go`). That store is read by exactly one resolution
  path — `buildDedicatedStoreServices` in `pkg/server`, used only for a
  barrier-enabled database with an explicit data directory — and even there it
  backs KV and collections only.
- `backend: raft` opens an **in-memory placeholder** store. The comment says
  so: the data plane for a dedicated-raft database is the tenant's Raft shard,
  reached through the request resolver, not through the store the manager
  opened.
- `backend: objstore` is accepted by validation and **refused by
  `openStorage`** with `database.InvalidBackendError`.
- `backend: raft` on a **prefix or barrier** tenant is metered as a Raft group
  and gets a replicated placement record, yet `resolve` routes on the tenant's
  isolation mode, so the database's data is served from the shared system shard.
  A per-database barrier on such a database would therefore not be on its
  data path, so creation refuses one with `database.BarrierOffDataPathError`
  (as it does for every database of a volume- or raft-isolated tenant). A
  barrier-enabled record written before that refusal opens sealed, and the
  service registry refuses every engine of a sealed database with
  `service.BarrierSealedError`, so it fails closed rather than serving the
  shared shard. *(Corrected: an earlier revision of this design, written
  before the refusal shipped, recorded that creation accepted such a barrier
  and that sealing it did not fail the data closed.)*
- `restServiceRegistry.resolve` (`pkg/server/rest_registry.go`) routes every
  other database to the shared default service set under the composite
  `<tenant>/<db>/` prefix, regardless of the database's recorded backend.
- In standalone mode the whole node's data plane is one local store whose
  backend is taken from **`databases[0].backend`** in the configuration file —
  the first database's setting is applied to every database on the node
  (`cmd/qrdbd` start path, `sdk/go/node.resolveBackend`).
- SQL `CREATE DATABASE` hardcodes `engine: kv, backend: pebble`
  (`pkg/sql/provider`), and the daemon's `--sql-database` auto-provisioning
  derives the backend from `--sm`.
- **Vector** is one node-local Pebble store at `{DataDir}/vector`, shared by
  every tenant and every database on the node, encrypted under the system
  encryptor, and isolated only by a key prefix on the namespace name
  (`pkg/server/lifecycle`). It is not replicated: the vector state machines in
  `pkg/statemachine` and the reserved vector shard are never started, and no
  production path gives the vector service a Raft node. The per-tenant volume
  host, the dedicated-raft bundles and the dedicated-store bundle carry no
  vector service at all.
- **Time series** requires a raw `*pebble.DB` (`pkg/timeseries`). It is
  therefore available only on the Pebble-native state machines or a pebble
  standalone node, and its apply handlers write straight to Pebble, outside the state
  machine's apply batch (`pkg/engine/mvcc_engine.go`, `handleTSWrite` and
  siblings). Per-database separation is a prefix on the metric name.
- **Secondary indexes** on the MVCC engine likewise write through a
  `*pebble.DB` handle directly when one is present, outside the apply batch;
  without one they fall back to an in-memory index.
- The **file and objstore** backends compose the plain KV engine, which has no
  MVCC, no transactions, no leases and no time series. The objstore batch is
  best effort: a failure mid-commit can leave a partial result.
- **Remote-mode databases** (`mode: remote` with a `connection`) are validated
  and persisted, and the factory never constructs the remote proxy: such a
  database is opened on a local store like any other.

### Corrections to earlier revisions

Earlier revisions of this folder were wrong, or silent, on these points:

- they said each database's backend is where its data lives — true only for the
  paths marked "partly" and "forced" above;
- they listed the backends as `pebble`, `memory`, `file`, `raft` — `objstore` is
  also accepted by validation, and then refused when the store is opened;
- they listed the engines as `kv`, `timeseries` — `vector` is also accepted;
- they said nothing about where vector data lives, which is the node-local store above
  rather than any database's store;
- they said enabling etcd on a database that is not `kv` on `raft` is rejected at
  configuration time — no such check existed in the database, configuration or
  etcd server code. The engine half is now enforced: etcd on a non-`kv`
  database is refused at create and on update with
  `database.EtcdEngineUnsupportedError`. The backend half is not, and the
  listener serves any backend; the target design adds that check (see
  "Combinations that are refused").

## Target design — shape chosen

A **database** is a first-class, independently configured, multi-model
container inside a tenant. It records one storage choice, and every engine it
serves is built over that choice.

```
tenant  (isolation mode: prefix | barrier | volume | raft)
  └── database  (backend, raft options, data dir, barrier, protocols)
        └── storage binding  — one per database, resolved from the above
              ├── KV · MVCC · transactions · batch · sequences
              ├── SQL / relational catalog and rows
              ├── collections · secondary indexes
              ├── vector namespaces and their ANN indexes
              ├── time series points, metric index, retention
              ├── leases · watches · locks (derived)
              └── optional MySQL / PostgreSQL / etcd wire protocols
```

### The storage binding

A **storage binding** is the resolved, concrete placement of one
`(tenant, database)`. It is computed from three inputs and nothing else:

1. the database's own record — `backend`, `raft` options, `replicas`,
   `data_dir`, `barrier`;
2. the owning tenant's record — isolation mode, data root, barrier;
3. the node's **defaults**, consulted only for a field the database record
   left empty at creation, and then written into the record, so the record is
   complete and the default is never consulted again for that database.

A binding has exactly two kinds:

| Kind | Backends | Host that applies writes | Replicated |
|---|---|---|---|
| **local** | `memory`, `file`, `pebble`, `objstore` | an engine-direct local host over the database's own store — the same host type standalone nodes use today | no |
| **raft** | `raft` | the database's Raft group; its state machine is the same engine over its own state storage | yes |

**Both kinds run the same engine over the same storage interface through the
same apply path.** A write is a command proposed to the binding's host; the
host applies it through the engine's apply batch, whether that host is a Raft
group or the local engine-direct host. This is what makes "every engine on
every backend" one implementation rather than five: an engine that is correct
over `storage.Storage` inside the apply batch is correct on every backend, and
replication is the host's property, not the engine's.

The service bundle — KV, MVCC, batch, transactions, leases, watches, indexes,
time series, vector, collections, and the SQL services derived from them — is
built by **one** builder over the binding's host. There is no second, partial
bundle for any kind of database.

### Resolution model

```
ResolveBinding(database record, tenant record, node defaults) → Binding | typed refusal
Materialize(Binding) → open store + host + watch hub + lease driver
Build(host) → the full service bundle, cached per (tenant, database, generation)
```

`ResolveBinding` is a pure function: no I/O, table-testable over every cell of
the matrix below. `Materialize` opens or attaches the store and host exactly
once per binding per node. A request resolves `(tenant, database)` to its
binding and is served by that binding's bundle. The composite
`<tenant>/<db>/` key prefix is no longer part of the data plane of any
database that has its own store.

### Tenant isolation × backend composition

The tenant's isolation mode decides **where** a binding lives and **whose key**
encrypts it. The database's backend decides **what kind of store** it is. The
two compose; neither overrides the other.

| Tenant isolation | Data root of the database's store | Encryptor | Host for `backend: raft` |
|---|---|---|---|
| `prefix` | node data root, `{DataDir}/tenants/<tenant>/databases/<db>` | the node at-rest encryptor — prefix isolation is, by definition, a tenant with no key of its own | system host, shared log |
| `barrier` | node data root, as above | **the tenant's barrier** — the barrier now covers the tenant's database data, not only its IAM data | system host, shared log |
| `volume` | the tenant's `DataRoot`, `{DataRoot}/databases/<db>` | the tenant's barrier | the tenant's own host, log under its `DataRoot` |
| `raft` | the tenant's `DataRoot`, `{DataRoot}/databases/<db>` | the tenant's barrier | system host, shared log or the tenant's dedicated WAL |

Rules that hold in every cell:

- **Every engine of a database uses the binding's encryptor and data root.**
  No engine captures the node's encryptor or the node's data directory at boot
  and applies it to tenant data. The vector store, the time-series store, the
  index store and the vector index checkpoints are all under the binding.
- **An explicit `data_dir` must lie under the tenant's data root** for volume
  and raft tenants, as today.
- **A per-database barrier layers on the binding**, it does not replace the
  tenant's (OD-6, ruled A). Every database now has a store of its
  own, so the old refusal of a barrier on a shared store no longer has a shared
  store to refuse; what remains is the refusal on `memory`, below.
- **A sealed barrier — the tenant's or the database's — fails every engine of
  the database closed**, vector and time series included.

The group granularity of `backend: raft` — one group per database for every
isolation mode (OD-1, ruled A) — is what makes the right-hand column
per-database for every isolation mode.

### Combinations that are refused

Refusals happen at resolution, before anything is created, with typed errors.
None of them degrades to another backend. Every type in this table is **new**
except `service.BarrierSealedError`; `database.BarrierRequiresPersistentBackendError`
replaces `database.SharedStoreBarrierUnsupportedError`, whose condition — a
database without a store of its own — no longer exists. For the same reason
the shipped `database.BarrierOffDataPathError` has no condition left to refuse:
every database's barrier layers on its own binding, which is its data path.

| Combination | Refused with | Why |
|---|---|---|
| `backend: raft` on a standalone node | `database.RaftBackendUnavailableError` | there is no Raft host to place the group on |
| `replicas`, `cluster_mode` or any `raft` option on a non-raft backend | `database.RaftOptionsRequireRaftBackendError` | replication is a property of the raft binding only |
| a per-database barrier on `backend: memory` | `database.BarrierRequiresPersistentBackendError` | nothing is at rest to encrypt; the barrier would be decorative |
| etcd protocol on a non-raft backend | `database.EtcdRequiresRaftBackendError` | etcd's revision counter needs a replicated log |
| `data_dir` outside the tenant's data root (volume, raft tenants) | `database.DataDirOutsideTenantRootError` | the tenant's contract is that its data lives under its root |
| `file` or `objstore` for a volume or raft tenant | `database.BackendIsolationConflictError` (OD-7, ruled B) | those backends expose key names outside the tenant's encryption |
| any backend while the tenant's or the database's barrier is sealed | `service.BarrierSealedError` at request time | fail closed |

A non-replicated backend in a multi-node cluster is **not** refused (OD-2, ruled
A): the catalog records its owning node and every other node forwards its
requests there. When the owner cannot be reached the request fails with
`database.BindingOwnerUnavailableError`, which names the owner.

### What stays node-level

Node configuration keeps only what is genuinely a property of the node:

| Setting | Meaning after this change |
|---|---|
| run mode (`raft` / `standalone`) | whether the node hosts Raft groups at all |
| control-plane store — `raft.sm_type` / `raft.sm_storage` (raft mode), `storage.backend` (standalone) | the store that holds the system tenant's catalog, users, tenants, placement and the system database; **never** another database's data |
| `database_defaults` — `backend`, `replicas`, `raft.cluster_mode`, `raft.state_storage` | filled into a database record at creation when the caller left the field empty; never consulted again for that database |
| `storage.objstore` provider profiles and credentials | how the node reaches an object store; a database chooses `objstore`, the node supplies the credentials |
| `storage.volume_pools` | where volume and raft tenants' data roots are allocated |
| Raft tuning, data directory root, listeners | unchanged |

Removed as node-level overrides: `databases[0].backend` applied to the whole
node, the hardcoded backend in SQL `CREATE DATABASE`, and `--sql-database`
deriving its backend from `--sm`. The `databases:` boot list stays; each entry
is provisioned with its own backend like any API create.

### Drop under a binding

The shipped drop reclaims a keyspace inside a shared store, dependents first
and the catalog record last (below, "What a drop reclaims"). With a store per
database, a drop is:

1. **Fence** — write the database's fence key through the binding's host.
   Every write to the database is a guarded command carrying "fence key absent",
   so a write proposed before the fence and applied after it is refused on every
   replica, from the same log. The guard applies to every engine's writes,
   vector and time series included, and to local bindings exactly as to raft
   ones, because both apply through the same engine. The fence is behind the
   cluster feature gate (ruling 6): a drop in a group whose members do not all
   support it is refused with a typed error, never run unfenced.
2. **Destroy the store** — stop and purge the Raft group on every replica, or
   close and remove the local store's directory or object prefix, and remove
   the vector index checkpoints under the data root.
3. **Remove the barrier**, if the database had one.
4. **Remove the catalog record.**

Dependents first and the resolvable record last, for the same reason as
before. The keyspace walk below remains the reclamation for the control-plane
store only.

## Shipped design — alternatives rejected

- **One global configuration.** Rejected on the problem above.
- **Per-database barrier off the data path.** **Refused by the code**, not
  merely discouraged: a barrier is only meaningful when it wraps the store the
  database is served from. Today that is one shape — a non-raft database with
  an explicit `--data-dir`, which only the system tenant can set. A database
  sharing the default store, a `backend: raft` database (served from its Raft
  group) and every database of a volume- or raft-isolated tenant (served from
  the tenant's own store) are all refused: the barrier would wrap a store no
  request reads, and sealing it would leave the data readable and writable.
  Until per-database storage bindings land, a non-system tenant cannot create a
  per-database barrier at all; the bindings restore it with a barrier on the
  data path of every backend.
- **Recreating a database to change its replication factor.** Rejected:
  `database scale` adds and removes voting replicas in place.
- **A global etcd facade.** Rejected — see
  [../etcd-compatibility/](../etcd-compatibility/architecture.md). An etcd
  listener serves exactly one database's keys, watches and leases, resolved
  through the service registry like every other transport.

## Target design — alternatives rejected

- **Keep the tenant isolation mode as the thing that decides the store, and
  treat the database backend as advisory.** Rejected by ruling 2, and by the
  measured result: a recorded setting that does not decide anything is a
  setting operators rely on and cannot see is ignored.
- **Per-engine stores inside one database** (a vector store beside a KV store
  beside a time-series store). Rejected: a write that touches two engines —
  a document and its embedding, a row and its secondary index entry — could no
  longer be one atomic apply, and each extra store needs its own snapshot,
  backup, barrier and drop.
- **A node-local vector store, replicated later.** Rejected: that is the
  current state, and it is the reason vector data today has neither
  replication nor tenant encryption. Vector is an engine of the binding from
  the start.
- **An engine-type field that turns engines on per database.** Rejected by
  ruling 1 and ruling 3. Build tags still decide which engines a binary
  carries; a database never decides it.
- **Emulating atomic batches on object storage by ordering writes.** Rejected:
  the storage interface promises an all-or-nothing batch, and every engine
  depends on it. The objstore backend gets a commit journal, as the file
  backend has.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Database manager, lifecycle, catalog, defaults, `ResolveBinding` | `pkg/database` | go-qrdb |
| Binding materialization, per-binding bundle cache, request resolution | `pkg/server` | go-qrdb |
| One engine over `storage.Storage` for every engine, in the apply batch | `pkg/engine` | go-qrdb |
| Storage backends — memory, file, pebble, objstore | `pkg/storage` — see [../storage-engines/](../storage-engines/) | go-qrdb |
| Vector engine | `pkg/vector`, `pkg/service` — see [../vectors/](../vectors/) | go-qrdb |
| Time-series engine | `pkg/timeseries` — see [../time-series/](../time-series/) | go-qrdb |
| Per-database and per-tenant barriers | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Raft groups and membership | — | **go-quicraft**, driven by `pkg/cluster` and the placement reconciler |
| Wire protocols | `pkg/sql`, `pkg/api/etcd` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Database()`, `Client.DatabaseScoped()`) | go-qrdb |

## Data flow

Every data-plane request resolves `(tenant, database)` server side and looks the
pair up in `service.Registry`, which returns the servicer bound to that
database's engine, backend, shard and barrier. This is the single point where a
database becomes a concrete set of services, and it is why a database's
configuration cannot leak into another's request path. In the target design it
is also the single point where the binding is chosen, and every servicer it
returns is built over that binding. The in-process (embedded) transport
resolves every operation that names a database — a collection call, a SQL
query — through the same registry whenever the embedding composition
configures one; with none configured, it serves the same single service set
the network transports fall back to.

### What a drop reclaims (current state)

A database in a shared store is a keyspace, not a directory, and its teardown
has to reclaim the keyspace. `DROP DATABASE` therefore reclaims, in this order:

0. **A fence**, so no new operation can resolve the database once reclamation
   starts, and no write already in flight can land after it.
1. **The data the database owns in the store it shares with other databases**
   — done next, against a database no caller can reach and no write can
   enter anymore.
2. Its barrier, if it had one.
3. Its configuration directory, which for a database with a dedicated store
   also holds that store's files.
4. Its catalog entry.

**Fence first, dependents next, the record that makes the database
resolvable last.** The dependents-before-record half is what makes a residue
permanent rather than merely present: once the catalog record is gone,
nothing in the system names the keyspace again, so no later drop and no
operator command can reach it. The fence closes the window that order alone
leaves open: without it, a write that resolved the database between the
keyspace scan and the catalog removal survived the reclamation that ran
concurrently with it, orphaned under a name the catalog no longer named.
The fence has two halves, and they answer different questions:

- **The local fence** (`Database.Dropping`, persisted in the database's
  metadata) answers "may a caller resolve this database now?".
  `Manager.Get` — the seam every read and write path resolves a database
  through — refuses with `database.DroppingError` for the whole of steps
  1-3, even though the catalog entry itself does not go until step 4.
- **The store fence** answers "may this write apply?", for writes that were
  resolved, checked and proposed before the drop began. Each database has a
  fence marker — `service.DropFenceKey(tenant, db)`, under the reserved
  `__sys:dropfence/` prefix — in the **same Raft group as its data**: the
  system shard for a shared-store database, the tenant's own Host shard
  under volume isolation, and the database's own shard for a dedicated-raft
  database. While the database is live the marker holds its
  **incarnation**, a random value chosen when the database is created and
  recorded with it; every write the database's views propose carries a
  guard, "the marker holds exactly my incarnation", **inside the same Raft
  command as the write** (`protocol.OpGuarded`). A drop sets the marker to a
  tombstone no incarnation equals. The state machine evaluates the guard at
  the write's own log position, on every replica, so the log decides every
  race: a write applied before the fence landed before it and is removed by
  the reclamation that follows; a write applied after it is refused and its
  caller receives `database.DroppingError`. There is no check-then-write
  window, because there is no separate check.

**Handles are scoped to an incarnation.** Creating a database marks its new
incarnation live as the last step before it becomes resolvable. A handle
resolved before a drop names the old incarnation, so it is refused after the
drop and stays refused after the name is created again: it can never write
into the new database. A database with no incarnation of its own — one
created before incarnations existed, or with no store to mark at the time —
is live while its marker is absent; the tombstone a drop leaves is what
refuses its stale handles, which is why the tombstone stays after the drop
completes rather than being removed. A dedicated-raft database's marker is
absent while it is live, because its shard identifies its incarnation: no
shard ID is ever issued twice in the cluster -- every raft database create
claims its ID from one replicated high-water mark, and no caller can name one
(see [engineering.md](engineering.md#cluster-wide-shard-ids)) -- so a database
created again under the name always gets a new shard, and a shard cached under the dropped one's ID is
evicted (and its directory purged) rather than served.

**Marking an incarnation live never overwrites another one.** The marker is
claimed only from absent or from the tombstone, by compare-and-swap. The
database catalog is node-local while the marker is replicated, so a marker
already holding a different live incarnation means the name was created on
another node; the create is refused with `*database.AlreadyExistsError`
(`Elsewhere` set) rather than taking the name over and refusing every write
to the other node's database. Until the catalog is replicated, a name is
created through the API on one node only.

**A dropped name needs the store to be re-created.** A drop's tombstone
outlives the drop, and only the keyspace reclaimer can replace it. A manager
with no reclaimer attached refuses to create a name this node dropped —
`*database.ReclaimerRequiredError` — instead of creating a database every
write to which the tombstone would refuse; the node records its drops beside
the catalog (`dropped/<name>`) so it can tell. For the same reason the
daemon provisions its configured databases only after the data plane has
attached the reclaimer. Boot provisioning (`Manager.Provision`) uses the
empty incarnation, the one every node provisioning the same configured name
agrees on without a replicated catalog; it clears a tombstone to absent and
refuses a name live elsewhere, like a create.

A failed drop is rolled back: the marker is restored to the database's own
incarnation — always, because a fence write that reported failure may still
have committed — under a context the failed request's cancellation cannot
reach. Only once that succeeds is the local fence cleared, and only then is
a dedicated-raft database's replicated placement fence cleared; if the store
cannot be reached, the database stays fenced (refused, honestly) until the
drop is retried, rather than looking live while the store refuses its
writes. A failure to read the placement table while checking the fence is
reported as that failure, never mistaken for "no such database".

**Mixed binary versions: the cluster feature gate.** A replica running a
binary that predates guarded writes would refuse one as an unknown opcode
and write nothing while its up-to-date peers applied it, leaving the
replicas diverged. A replica that rejoins a group later is caught up by
REPLAYING that group's committed log, so it is not enough for a group's
CURRENT members to all support the opcode today — a later membership change
could still introduce an old binary that chokes on an entry already in the
log. So a guarded write is only ever proposed once the `guarded-writes`
feature has been activated cluster-wide: every node in the cluster, not just
the group's current members, has advertised support (see
[../clustering/architecture.md](../clustering/architecture.md), "Cluster
feature gate"). Until then every group's writes are proposed unguarded and
the drop fence is the check made when the database is resolved, before the
write — the protection the fence gave before guarded writes existed, with
its check-then-write window. Activation is permanent once it happens, and
from then on a node whose binary lacks an activated feature refuses to
start. A standalone node, and a single-node or transportless group, has one
binary and uses guarded writes from the start.

Activation is consulted at PROPOSAL time, fresh on every write, never cached
on a handle or re-checked against when a handle was resolved. A handle
already in hand when activation happens is not special and needs no
separate re-check: its next write is guarded exactly like one from a handle
resolved after activation, because the decision was never attached to the
handle to begin with — only to the write. Before activation, this cuts the
other way too: a handle already in hand — a SQL transaction mid-flight, or
the cached composite collection service a database's catalog record is
reused across — gets no write-time check at all, the same as any other
handle resolved before activation; there is nothing on it to upgrade when
activation later happens, only its next write.

The fence (`Database.Dropping`) is its own field, deliberately independent
of `Database.Status`. Status keeps answering the question it always has —
was there an open backend for step 2 to close — undisturbed by the fence;
Close, and the boot-time barrier re-registration that also writes Status,
would otherwise be able to silently clear the fence as a side effect of
work that has nothing to do with a drop. It also means a database a crash
left mid-drop reopens on restart as an ordinary `StatusOpen` (or
`StatusSealed`) database with `Dropping` true: the retried `Drop`'s own
step 2 still finds a backend to close, and the OWNING tenant's
`DropForTenant` — which checks ownership directly against the catalog
rather than through the fenced `Get`, precisely so this retry is possible —
can still finish the job.

With this order, the worst an interrupted drop leaves is a **fenced, empty
database**: unresolvable to ordinary operations, but still in the catalog, so
re-issuing the drop finishes it — the reclamation is idempotent, and an
already-empty keyspace is a success. A crash between the fence and the
catalog removal is indistinguishable from an interrupted drop still in
progress: the fence is persisted in the database's own metadata, so a
restart reopens it fenced rather than silently resolvable again.

A reclamation that fails **fails the whole drop**. The database, its catalog
record and its data are left exactly as they were — the fence clears, and
Status was never touched in the first place — because reporting a
successful drop over data that is still in the store is worse than
refusing, and a permanently fenced database from a failed attempt would
turn one failure into an outage.

**A dedicated-raft (`IsolationRaft`) database needs a second, REPLICATED
fence, and has one.** Its existence and ownership are authoritative in the
replicated placement table, not the local catalog — a follower's local
`database.Manager` entry may legitimately be absent even though the
follower hosts a live replica (see `RaftBackendFactory`) — so a fence that
only lived in `database.Manager` would close the race solely on the node
that issued the `Drop`. `DropForTenant` (and `Drop`, which shares the same
logic) therefore commits a second fence, `placement.Placement.Dropping`,
to the database's replicated placement entry, in the same step-0 position
relative to reclamation as the local one. `RaftBackendFactory.Services` —
the resolution seam every node's data-plane request reaches for a
dedicated-raft database, cold-starting a shard or returning one already
cached — checks it on every call and refuses with the same
`database.DroppingError` when it is set, so every replica answers
identically regardless of which node issued the `Drop` or which one
currently leads the shard. That marker refuses NEW handles; the store fence,
written into the database's own shard as for every database, is what
refuses the writes of handles already in hand. The placement entry, and the
fence with it, is
removed at the very end of a successful drop, once the manager-level drop
and the tenant's quota release have both completed; a failed reclamation
clears the fence the same way the local one is cleared. Unlike the
best-effort placement cleanup this replaced, a failure removing the
placement entry itself is no longer swallowed: the fence survives with the
entry, so the database stays refused rather than becoming simultaneously
unreachable (the local catalog record is already gone) and unrefused (no
replica has any signal that it is still being torn down) — an operator's
retry (or a resumed `DropForTenant`) finishes the removal.

**Every write the fence can refuse carries it, on every transport, because
the guard rides the request, not the verb.** A database's views attach the
guard to the request context (`service.WithWriteGuard`), and every Raft
proposal `pkg/service` makes goes through one path that wraps the command
with the context's guard. A write verb cannot forget the guard, because no
verb proposes any other way — which is what a per-verb re-check kept
getting wrong, one method at a time. The views that carry it are the
namespaced KV, MVCC, batch, transaction, time-series and index views behind
every transport, the collection and SQL services built over them, and the
pass-through views of a dedicated-raft database. The default database
carries no guard: it cannot be dropped.

Two writes are not Raft proposals and so cannot carry the guard. **Vector
writes** go to a node-local vector store: they are refused by a check made
before the write, which leaves a window between the check and the write.
**A database with a dedicated local store** (an explicit data directory)
writes to that store directly; its drop closes and removes the whole store,
so a late write has no keyspace to survive in, and the check before the
write only turns a closed-store failure into `database.DroppingError`.
Leases are global, not scoped to a database; attaching a lease to a key is
a KV or MVCC write and is guarded.

**The default database cannot be dropped.** Every tenant's unscoped requests
resolve to it and the bootstrap records live in it, so every drop entry
point refuses it with `database.DefaultDatabaseDropError` before touching
anything, on every transport.

**A stale record can never overwrite a newer one, for every placement
mutation, not only the fence — and a backend that cannot compare-and-swap
is refused outright, never silently downgraded.** `Store.
CompareAndSwapPlacement` is the one primitive every placement write commits
through when it is updating an EXISTING entry — the fence
(`SetDropping`/`TryMarkDropping`/`MarkDropCommitted`), a scale-driven
membership change (`addObserverReplica`/`removeReplicaFromGroup`), and the
reconciler's own observer-promotion writes all read-then-CAS against the
exact snapshot they read, so a write that lands between that read and the
CAS is retried against the fresh value instead of silently discarding it or
being silently discarded itself. A KV backend with no `AtomicKV` support
is refused with `*placement.AtomicWriteUnsupportedError` (inside the
`*placement.StoreError` naming the entry) rather than falling back to an
unconditional `Put`: an early version of this fence degraded that way,
which reintroduced the exact lost-update race the CAS exists to close (two
concurrent claims could both observe `owned=true`). `ScaleForTenant` and
the rebalance MOVE path both reject a placement snapshot that already shows
`Dropping=true` before reserving quota or mutating membership, and the
reconciler additionally refuses outright to promote a replica of a database
that is mid-drop, rather than relying on the CAS alone to notice — though
this is still a check against a snapshot read moments earlier, not a
compare folded into the same mutation. It guards control-plane membership,
not data: the data plane's writes are refused by the store fence itself.

**A request never claims the fence at all unless this node can actually
service the drop.** `DropForTenant` resolves the local record through
`Manager.GetForTenantIgnoringFence` -- which, unlike `GetForTenant`, still
returns the database while its own local fence is set, since resuming this
very drop's claim is exactly the one caller that fence must never refuse --
BEFORE it ever touches the replicated placement fence. A request that
reaches a node with no local record at all (an ordinary, load-balanced
client landing on a non-authoritative replica -- a dedicated-raft
database's local `database.Manager` record exists only on the node that
created it) is refused immediately, unless `Placement.DropCommitted` (see
below) already proves an earlier attempt, on whichever node held the
record, finished the local half. This closes the cross-node half of two-
`Drop`-attempts-racing outright: only the one node that can legitimately
progress a drop is ever allowed to claim its fence in the first place.

**On that SAME node, a rollback still undoes only a fence THIS call set,
and never on `database.DropInProgressError`.** `Manager.Drop`'s own in-flight guard
(`m.dropping`) already serializes two concurrent same-node attempts to one
winner and one immediate `DropInProgressError`, but the loser must still
never be the one to clear the replicated fence on that error: it never set
it, and clearing it out from under the winner would unrefuse a database
still genuinely mid-drop. `TryMarkDropping` reports whether THIS call
performed the not-dropping-to-dropping transition (`owned`); a caller's
rollback is gated on `owned` being true, so a call that merely observed the
fence already set (a resumed retry finding its OWN earlier, still-recorded
claim, or a `database.DropInProgressError` loser) never touches it. The gate has a second
half: the replicated fence is rolled back only when the manager-level drop
reports that it rolled back ITS fences — `database.DropRolledBack(err)`,
true for a `*database.LocalFenceError` (the local fence could not be
persisted, so nothing else was touched) and for a
`*database.KeyspaceFenceError` or `*database.KeyspaceReclaimError` whose
`RolledBack` is set because the store marker was confirmed restored. A drop
whose store fence could not be cleared, and a backend-close or
directory-removal failure, leave both fences set, so the database stays
refused everywhere rather than becoming un-fenced on the replicated table
while the store still refuses its writes. Persisting `Placement.
DropCommitted` (below) is held to the identical standard: a failure to set
it fails the whole `Drop`/`DropForTenant` call rather than being logged and
discarded, so a later retry is never stranded believing a marker was set
that never actually committed.

**`Placement.DropCommitted` tells a legitimate retry-after-teardown apart
from a request that reached a node with no local record.**
`manager.DropForTenant` returning `NotFoundError` is otherwise ambiguous:
it means either "an earlier attempt on this node already finished the local
drop" (a legitimate retry that only needs to finish the placement teardown)
or "this node never had the local record at all" (now refused upfront, per
above, before this point is ever reached in practice). `DropCommitted`, set
once — via `Store.MarkDropCommitted` — right before the placement entry is
removed at the end of a successful drop, is the signal that tells them
apart: only when it is already true does `DropForTenant` finish the
placement teardown on a `NotFoundError`; otherwise the error is returned as
the genuine failure it is, and the fenced placement entry survives for a
retry against the node that actually holds the record. The same
`GetForTenantIgnoringFence` resolution also recovers Backend/Replicas/
DedicatedWAL for a RESUMED drop (one whose local fence survived a restart),
so the tenant's quota is still released correctly even though `GetForTenant`
itself would refuse with `DroppingError`.

**A drop resumes after a restart even though the local fence refuses the
usual lookup.** `Manager.Get` keeps answering `*database.DroppingError` for
a database whose local fence survived the restart, which is exactly the
resolution path `Drop`'s tenant-ID lookup used to depend on. The error
itself now carries the tenant ID, recovered from the fenced record before
the refusal, so `Drop` can still identify which tenant's placement entry to
finish tearing down instead of leaving it permanently stranded.

**A cached shard cannot outlive the placement entry it was resolved
through.** `RaftBackendFactory.Services`'s cache-hit path re-checks the
fence on every call, but a completed drop's last step removes the WHOLE
placement entry, not merely its `Dropping` flag — at that point there is no
fence left to read at all, and the resolver's own placement lookup reports
"no entry," identically to a Phase-4b (pre-placement) database that never
had one. The factory tells the two apart by recording, on the cached shard
itself, whether it was originally resolved through a real placement entry;
if it was, and that entry is now gone entirely, the shard is refused rather
than served stale — closing the asynchronous window between the placement
delete and the `TenantShardReconciler`'s own (later) cache eviction.

**The local fence's in-memory flag commits before its durable one, not
after.** `Manager.Drop`'s step 0 sets `Database.Dropping` in memory FIRST,
then persists it to `meta.json`: `loadDatabase` (the seam every
`Update*`/`Get` resolution goes through) refuses the instant the in-memory
flag reads true, and `rewriteMeta` always reads it fresh at call time, never
a cached value, so a concurrent `Update*` call can never observe the flag
still false and silently persist a stale `Dropping: false` over the durable
write moments later. `meta.json` itself is written through a temp-file/
fsync/rename sequence rather than a direct write to the final path, so a
crash mid-write can never leave it truncated or otherwise corrupt, and two
writers racing the same path settle on "the later rename wins" rather than
an interleaved, invalid file.

The data a database owns is addressed in these shapes, and a teardown that
reclaimed only the first would leave every document behind:

| Shape | Holds | Scoped by |
|---|---|---|
| `<tenant>/<db>/…` | plain KV and MVCC keys, the relational catalog, rows, index entries, every sequence counter (including the per-database table-id counter), the privilege catalog | tenant **and** database |
| `ts:<tenant>/<db>/…`, metric-index and retention records | time-series points, metric index, retention policies | tenant and database, inside the series prefixes, because it is the metric NAME that is namespaced |
| `<tenant>/<db>/__sys:collections/…`, `<tenant>/<db>/__col:…` | collection catalog entries and documents | tenant and database — the collection surface is resolved per (tenant, database) like every other engine |
| secondary index catalog | index names under `<tenant>/<db>/` | a namespace of its own inside the state machine, not reached by deleting keys |
| vector namespaces | namespaces under `<tenant>/<db>/` | the node-local vector store, likewise not reached by deleting keys |

**Everything in that table is scoped by tenant**, and that is a property to
keep rather than an accident of how it was written. A drop is reachable by any
tenant that owns a database, and the store it runs against is shared, so a
prefix that does not carry the tenant is a prefix one tenant's drop uses to
delete another's data.

Three things are deliberately **not** reclaimed:

- the **time-series tag index**, whose keys are `tag key:value` and carry no
  metric, so no single database owns it;
- any record that is **database-agnostic** — roles, users, tenants;
- **collection records written before the collection surface was tenant-scoped**
  — `__sys:collections/<db>/…` and `__col:<db>:…` at the store root. These name
  a database and no tenant, so nothing can attribute them to the tenant issuing
  the drop, and a drop that deleted them by database alone would delete every
  other tenant's. They are left in place, inert: nothing reads them, because
  every transport's key surface is namespaced and no caller can compose the bare
  key, and nothing deletes them. An operator upgrading a deployment that has
  them exports them with the previous binary and re-imports them per tenant —
  which is the migration a reclamation would take away. See
  [collections/engineering.md](../collections/engineering.md) "Upgrading across
  the tenant-namespace change", which is the decision this follows.

**The reclamation reads through an unfiltered enumeration**, not the
user-facing one. Several of the shapes above are in the internal key
namespace, which the ordinary key listing hides from callers — a discovery
guard that is correct for a request handler and wrong for a teardown, which is
not a caller but the owner of the data. A reclamation built on the filtered
listing sees an empty result for the time-series prefixes and reports a
successful drop over data it never touched. The distinction is carried by a
separately named verb rather than a flag, so a filtered implementation cannot
satisfy the teardown's contract by accident.

In the target design every database has its own store, the tag index and every
other shape above belongs to exactly one database, and a drop destroys the
store instead of walking it (see "Drop under a binding").

## Trust boundaries

- **Databases live inside a tenant.** The tenant comes from the authenticated
  identity; the database name travels in the request. A database name cannot
  reach across tenants.
- **Another tenant's database is answered as missing.** Database names are
  still one node-wide namespace (keying the catalog by tenant and name is the
  catalog lane's work), so every tenant-scoped operation, every data-plane
  resolution and SQL `HasDatabase` / `USE` answer a database of another tenant
  (other than the configured default SQL database, `sql.default_database`,
  which every tenant has in its own namespace)
  exactly as they answer a name nobody holds — `database.NotFoundError`, never
  an access denial that would tell the caller the name is taken. The ad-hoc
  SQL query (`POST /sql/query`, `SQLQuery`) asks the service registry before
  the node-wide catalog, so another tenant's name and an unused name get the
  registry's one not-found rather than two different errors. The one
  remaining signal is creation: a name another tenant holds cannot be created
  (`database.AlreadyExistsError`) until names are scoped per tenant.
- **Privileged operations are separately gated.** Creating a Raft-backed
  database (`databases:create_raft`) and a volume-isolated one
  (`databases:create_volume`) are checked *in addition to* `databases:create`,
  because each provisions dedicated infrastructure. `databases:scale` is
  distinct from `databases:update` because it consumes quota and mutates cluster
  placement and Raft membership. Sealing and unsealing
  (`databases:seal`, `databases:unseal`) are custody operations, gated apart
  from configuration.
- **A sealed database fails closed.** Its data is unreadable until unsealed.
  The refusal is made where every transport resolves a `(tenant, database)`
  pair, on every engine, whichever store serves the database — not by the
  store, because most stores are shared and cannot tell one sealed database
  from the rest. A database opened from disk with a barrier starts sealed.
  Sealing is a node's own state, not replicated: a database sealed on one node
  of a cluster is still served by any node on which it is unsealed.
- **Secrets set on a database are write-only.** A Raft discovery secret
  (static or multicast shared secret, bootstrap token) goes in with a request
  and never comes out: every transport reports only that it is set. The
  response types have no field that could carry one, and every transport
  builds them in one place (`pkg/api/dbwire`), so a transport cannot reopen
  the leak on its own. Reading a database's configuration is not the same
  privilege as holding the secret that admits a node to its group.
- **Target design: a tenant's data is encrypted under the tenant's key on every
  engine.** The node-local vector store under the system encryptor is the case
  this closes.

## Shamir custody of a database barrier

A database barrier may be split M-of-N to **share holders** instead of sealed
by a passphrase. There is no database-specific custody system: a database is
one more **holder scope** of the one share-holder model in
[../shamir-custody/](../shamir-custody/architecture.md) — the same registry,
enrolment, envelope format, claim protocol, quorum accumulator, factor
policies, rekey and revocation that serve the system and tenant barriers.

- **Scope.** The scope is the pair (tenant, database). A database barrier has
  the **unseal domain only**, as a tenant barrier does: no recovery key and no
  root token. One person holds one share, in one role, per scope; the same
  person may also hold a share of the system, tenant or another database's
  barrier, each under its own enrolment.
- **Holder identity.** Enrolment has the ceremony CA issue the holder's
  certificate in the holder namespace of the database's tenant,
  `spiffe://<trust-domain>/tenant/<tenant>/holder/<username>`; the CSR
  contributes only its public key. The authentication layer refuses a holder
  identity as a login identity, as for every other scope.
- **Lifecycle.** A database is created with `barrier: {enabled: true, mode:
  "shamir", sharesRequired: M, sharesTotal: N}` and no secret. It then has a
  registered, **uninitialized** barrier, reports itself `sealed`, and fails
  closed until custody is initialized. Exactly N holders are enrolled against
  the database; initialization (a factor `policy` only — the M-of-N is the
  create's, and is not restated) generates the root key, splits it to the
  holders and seals each share to its holder in an envelope held only in
  protected memory, and leaves the database unsealed. Each
  holder claims its envelope once. From then on unseal and seal are quorum
  rounds of the database's holders; a direct `unseal` with a secret and a
  single-caller `seal` are refused (`ErrShamirUnsealRequiresQuorum`,
  `ErrShamirBarrierRequiresQuorum`).
- **Rekey and revocation.** Starting a rekey (new threshold or policy) is
  RBAC-gated; revoking a holder is a rekey that removes them, refused when the
  remaining holders cannot meet the threshold. Either completes only with a
  fresh M-of-N quorum of the current holders, and the new M-of-N is recorded
  on the database's configuration. A configuration update never changes a
  Shamir barrier's strategy or M-of-N: that is a rekey.
- **Authorization.** The `databases:officer_register`, `officer_list`,
  `initialize_shamir`, `rekey` and `officer_revoke` permissions are
  self-scoped like the rest of `databases:*`; the database service confines
  each call to a database in the caller's tenant before it learns whether the
  database or its barrier exists. Claim and quorum are identity-gated: the
  holder's certificate and factor proof are the credential. The system
  tenant's administrator may target any database. A share holder whose
  identity is in the system tenant is not that administrator: it is confined
  to the system tenant's own databases. Another tenant's database answers it
  `NotFound`, exactly as a missing one does, so the holder routes reveal no
  other tenant's database names.
- **Tenants.** A Shamir barrier is held to the passphrase barrier's rule: it
  is accepted only on the database's data path, a non-raft database with its
  own data directory, which only the system tenant can set. Until per-database
  storage bindings land, a non-system tenant cannot create one; the bindings
  restore it, as they do the passphrase barrier.
- **Where it lives.** The database barrier's wrapped key and holder roster
  live with the barrier's metadata in the database's dedicated store, beside
  the passphrase barrier's: a Shamir database barrier needs a dedicated store
  exactly as a passphrase one does. Holder records live under the database's
  barrier (encrypted at rest once it is initialized). After a restart the
  database opens sealed and its holders' authority is re-installed; a quorum
  unseals it.
- **Drop.** Dropping the database destroys its holder records, every envelope
  still held for its holders and every pending challenge.

Rejected: a per-database custodian system or registry (the owner's ruling is
one share-holder model); reusing the tenant's holders for its databases
(different keys need different custody, and a holder's share is bound to one
roster); recovery keys per database (a database barrier, like a tenant
barrier, has the unseal domain only).

## What this feature deliberately does not do

- **No cross-database transaction.** One commit targets one binding — see
  [../sql/](../sql/engineering.md).
- **No barrier off the data path.** Refused, not warned about. In the target
  design every barrier layers on its database's binding, so none can be.
- **No backend change after creation.** The backend is a creation-time choice;
  changing it means a new database and a data migration. Replicas, cluster mode,
  protocols, barrier state and connection settings remain updatable.
- **No implicit database creation.** A request naming an unknown database fails.
- **No per-database engine selection.** Every compiled-in engine is available
  in every database (target design; ruling 1).

## Decisions ruled by the owner

Each item was a question the rulings above did not answer. **All sixteen were
ruled on 2026-09-30: the owner accepted every recommendation.** The options are
kept so the reasoning behind each ruling stays on record.

**OD-1 — RULED A — Raft group granularity for `backend: raft`.**
- A: one Raft group per database, for every tenant isolation mode. The tenant's
  isolation decides the host, data root and key; the database always gets its
  own group.
- B: shared groups — prefix and barrier tenants' raft databases share the system
  group under the `<tenant>/<db>/` prefix, a volume tenant's share its tenant
  group, raft tenants get one per database.
- C: a per-database option `raft.group: dedicated | shared`.
- **RULED: A — the recommendation, accepted.** It is the only option under which replicas, scale,
  etcd, the drop fence, a per-database barrier and the tenant encryptor mean the
  same thing for every raft database. B keeps the prefix machinery on the data
  plane and cannot put a barrier tenant's data under the tenant's key without
  per-value envelopes in a shared log. C doubles the matrix for little gain. The
  cost of A is one group per raft database, which is what multi-group Raft is
  for.

**OD-2 — RULED A — Non-replicated backends in a multi-node raft-mode cluster.** A memory,
file, pebble or objstore database exists on one node.
- A: the replicated catalog records the owning node; every other node forwards
  the database's requests to it, as writes are forwarded to a leader today. If
  the owner is down the database is unavailable, which is what that backend
  choice means.
- B: refused in a multi-node cluster with `database.LocalBackendInClusterError`.
- C: allowed, reachable only on the owning node; elsewhere a typed error names
  the owner.
- **RULED: A — the recommendation, accepted**, with C's typed error as what a request gets when
  forwarding cannot reach the owner.

**OD-3 — RULED A — The default backend when a create leaves it empty.**
- A: run-mode dependent — `raft` in raft mode, `pebble` in standalone mode —
  overridable by `database_defaults.backend`.
- B: `pebble` always, today's literal default. In a cluster that would make
  every default database node-local and unreplicated, where today its data is in
  the replicated system shard.
- C: no default; `backend` is required.
- **RULED: A — the recommendation, accepted.**

**OD-4 — RULED A — Catalog authority.** Today a database's `meta.json` is node-local; only
dedicated-raft databases also have a replicated placement record.
- A: the replicated catalog (the placement table, extended to every backend) is
  authoritative for every database; `meta.json` becomes a node-local cache
  rebuilt from it.
- B: keep `meta.json` authoritative for non-raft backends and placement for raft.
- **RULED: A — the recommendation, accepted.** OD-2 A requires every node to resolve every database,
  and B cannot give it that.

**OD-5 — RULED A — Implicit default databases of non-system tenants.** Every tenant can
address the default database (`system`) and the SQL default database without
creating them; today both are prefixes in the shared system shard.
- A: provision both as real databases at tenant creation, with the node defaults.
- B: keep them on the control-plane store under the `<tenant>/<db>/` prefix, as
  the one remaining shared binding.
- C: remove them; a non-system tenant's request without a database is refused.
- **RULED: A — the recommendation, accepted.** B keeps a second resolution path alive for two names;
  C breaks every client that relies on the default today.

**OD-6 — RULED A — Encryption when a database has its own barrier.**
- A: nested — the store is encrypted at rest under the tenant's key (or the
  node's, for prefix tenants), and values are additionally encrypted under the
  database's barrier. Sealing either one fails the database closed.
- B: the database's barrier replaces the tenant's for that database's store.
- **RULED: A — the recommendation, accepted.** B would let a database owner take a tenant's data out
  from under the tenant's custody.

**OD-7 — RULED B — Key-name confidentiality on `file` and `objstore`.** Values are
encrypted on every backend. Pebble encrypts whole files, keys included. The file
backend's keys are file names and objstore's are object names, so key names are
visible to anyone who can list the directory or bucket. A remote object-store
provider also places data outside the tenant's data root.
- A: accept and document it, for every isolation mode.
- B: refuse `file` and `objstore` for volume and raft tenants, whose contract is
  that their data lives under their root and their key; allow them, documented,
  for prefix and barrier tenants.
- C: encrypt key names deterministically. This destroys key ordering, so range
  scans — and therefore MVCC, SQL and time series — stop working on those
  backends. Not viable under ruling 1.
- **RULED: B — the recommendation, accepted.**

**OD-8 — RULED A — State machine of a database's Raft group.**
- A: one canonical database state machine — the MVCC engine over
  `storage.Storage` — for every database group, with its state storage chosen
  per database (`raft.state_storage: pebble | file | memory`, defaulted from
  the node). The node's `sm_type` applies to the control-plane group only.
- B: a per-database `sm_type`, which requires every state-machine family to
  implement every engine, vector and time series included.
- **RULED: A — the recommendation, accepted.** The in-flight work giving every state-machine type an
  MVCC store remains useful for the control-plane group and is not a
  prerequisite here.

**OD-9 — RULED A — Remote-mode databases.** Measured: accepted, validated, persisted, and
never constructed; the data goes to a local store.
- A: remove remote mode, the `connection` block and the update-connection verb
  on all six transports.
- B: implement a full multi-model proxy over the SDK client, every engine.
- C: keep a KV-only proxy and refuse other engines — contradicts ruling 1.
- **RULED: A — the recommendation, accepted.** A database that proxies to another node is a client
  configuration, which the SDK already provides.

**OD-10 — RULED A — Relocating existing shared-shard data.** Databases whose data is in
the shared system shard (or a volume tenant's shared shard) today move to their
own bindings.
- A: automatic, online, per database, run by the control-plane leader on first
  boot of the new version. Each database is fenced for the duration of its copy
  (writes are refused with a retryable unavailable error), copied with its
  prefix stripped into its new binding, switched in the catalog, and its old
  keyspace reclaimed. The legacy resolution path exists only while relocation
  jobs are pending and is deleted in the following minor release.
- B: an operator-run `qrdb database relocate <name>` verb; unrelocated databases
  keep resolving to the shared shard until it is run.
- C: refuse; the new version does not open pre-binding data, and operators
  export with the previous version and import into the new one.
- **RULED: A — the recommendation, accepted.**

**OD-11 — RULED C — Migrating the node-local vector stores.** In a multi-node cluster
each node's vector store holds only the writes that node served, so the stores
disagree.
- A: import every node's store; last writer wins by vector version and update
  time; deletes are not propagated.
- B: import from one designated node only.
- C: an operator import command, run per node, with
  `--on-conflict fail | newest | skip` defaulting to `fail`, reporting every
  conflict.
- **RULED: C — the recommendation, accepted.** A single-node deployment, the common case, has no
  conflicts and imports cleanly; a cluster's operator sees every disagreement
  instead of having one resolved silently.

**OD-12 — RULED B — SQL virtual tables for the engines.** Today only a database created
with engine `timeseries` or `vector` exposes `datapoints`/`metrics` or
`namespaces`/`vectors` as SQL tables.
- A: every database exposes every compiled-in engine table under those names,
  and the names become reserved (`CREATE TABLE metrics` is refused).
- B: every database exposes them under reserved, prefixed names —
  `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`,
  `qrdb_vector_vectors` — so user tables named `metrics` or `vectors` keep
  working.
- C: stop exposing engine tables through SQL.
- **RULED: B — the recommendation, accepted.**

**OD-13 — RULED A — How callers learn `engine` is gone.** The CLI flag fails as an unknown
flag and the protobuf field number is reserved either way.
- A: strict decoding on database-create requests (REST, QUIC, MCP) and on the
  `databases:` configuration section, so `engine` is refused as an unknown field
  — a generic rule, not an engine-specific shim.
- B: ignore it silently, the JSON and YAML default.
- **RULED: A — the recommendation, accepted.** A script creating an `engine: timeseries` database
  should learn that the field no longer means anything.

**OD-14 — RULED A — The vector `shard_id` request field.** The binding decides the shard.
- A: remove it from all twenty vector requests on all six transports.
- B: keep it and refuse any non-zero value with a typed error.
- **RULED: A — the recommendation, accepted** — a breaking change, carried by a MINOR bump.

**OD-15 — RULED A — Choosing a backend from SQL `CREATE DATABASE`.**
- A: node defaults only; non-default backends are created through the API or CLI.
- B: an executable-comment extension on MySQL
  (`CREATE DATABASE x /*!qrdb backend=raft replicas=3 */`) and a `WITH` option
  on PostgreSQL.
- **RULED: A — the recommendation, accepted** for this program.

**OD-16 — RULED A — Time-series retention enforcement.** Measured: retention policies are
stored and returned, and nothing ever expires or downsamples a point.
- A: include it in this program — a leader-driven retention operation, proposed
  through the binding and applied deterministically in the apply batch.
- B: a separate program; this program only corrects the documentation.
- **RULED: A — the recommendation, accepted**, as its own lane after the time-series phase. The
  documentation is corrected now either way.
