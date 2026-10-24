# Databases — Architecture

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.
>
> This document has two layers, and each section says which layer it is in.
>
> - **Current state** sections describe the code as shipped.
> - **Target design** sections are the requirements contract for the
>   multi-model database program: every database supports every engine on the
>   storage backend it chose, for every tenant isolation mode. Of the design,
>   rule 3 below (the `engine` field's removal) has shipped, and
>   `database.ResolveBinding` (`pkg/database/binding.go`) with its typed
>   refusals is written but has no production caller; the rest is not
>   implemented. Every design question, with its options and the decision, is
>   in "Decisions" at the end.

## Problem

One store serves several workloads with genuinely different needs: one wants
Raft replication and its own encryption key, another wants a cheap local Pebble
store, a third needs to look like etcd on port 2379, a fourth is an embedding
index that should live next to the documents it was derived from. Forcing them
into one configuration means the strictest requirement is paid for by everyone.

A database is the unit at which those choices are made. For that to mean
anything, the choice a database records has to be the choice its data actually
lives under, for every kind of data it holds.

## Rules this design implements

1. **Databases are multi-model.** Every database supports every engine — plain
   KV, MVCC and transactions, SQL/relational, collections, secondary indexes,
   vector, time series, leases and watches — on its chosen backend.
2. **The storage backend is a database-level option, and nothing hardcodes
   it.** memory, file, pebble, raft and objstore are all per-database choices,
   honoured by every engine. No node flag, no SQL statement and no boot path
   substitutes its own backend for the one the database recorded.
3. **The `engine` field is removed** from the database configuration, the
   persisted metadata and every API. **Shipped.**
4. **Vector and time series work the same way as every other engine** and are
   tenant scoped: they live in the database's storage binding, under the
   tenant's encryptor and data root.
5. Custodian and secret-material handling is unaffected by this design.
6. **The database drop fence ships behind a cluster feature gate**, and so does
   every new Raft command this program adds (the vector commands and the
   time-series retention command). A node proposes a gated command only once the
   gate reports that every member of the group applies it; until then the
   capability that needs it is refused with a typed error rather than proposed
   into a group that would reject it.
7. **Every design question is decided**, in "Decisions" below (OD-1 to
   OD-16).

## Current state

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

Supporting facts:

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
  (as it does for every database of a volume- or raft-isolated tenant). An
  existing barrier-enabled record of such a database opens sealed, and the
  service registry refuses every engine of a sealed database with
  `service.BarrierSealedError`, so it fails closed rather than serving the
  shared shard.
- `restServiceRegistry.resolve` (`pkg/server/rest_registry.go`) routes every
  other database to the shared default service set under the composite
  `<tenant>/<db>/` prefix, regardless of the database's recorded backend.
- In standalone mode the whole node's data plane is one local store whose
  backend is taken from **`databases[0].backend`** in the configuration file —
  the first database's setting is applied to every database on the node
  (`cmd/qrdbd` start path, `sdk/go/node.resolveBackend`).
- SQL `CREATE DATABASE` hardcodes `backend: pebble` (`pkg/sql/provider`), and the daemon's `--sql-database` auto-provisioning derives the backend from `--sm`.
- **Vector** is one node-local Pebble store at `{DataDir}/vector`, shared by
  every tenant and every database on the node, encrypted under the system
  encryptor, and isolated only by a key prefix on the namespace name
  (`pkg/server/lifecycle`). It is not replicated: the vector state machines in
  `pkg/statemachine` and the reserved vector shard are never started, and no
  production path gives the vector service a Raft node. The per-tenant volume
  host, the dedicated-raft bundles and the dedicated-store bundle carry no
  vector service at all.
- **Time series** is storage-backed (`pkg/timeseries.Storage` wraps
  `storage.Storage`, not a raw `*pebble.DB`) and available on every backend.
  Every write (`pkg/engine/mvcc_engine.go`, `handleTSWrite` and siblings)
  stages into the caller's apply batch instead of committing one of its own,
  so it is atomic with the rest of the entry that produced it. Per-database
  separation is a prefix on the metric name.
- **Secondary indexes** on the MVCC engine are likewise storage-backed
  (`index.StoragePebbleIndex`, used unconditionally) and stage into the
  apply batch; there is no separate in-memory fallback.
- Every backend — memory, file, pebble and objstore — composes the
  MVCC-capable engine (`pkg/server/lifecycle.NewLocalEngine`). The plain KV
  engine (`pkg/engine/kv_engine.go`) is not composed by any production
  composition root. The objstore batch is atomic (all-or-nothing after
  recovery, via a commit journal), matching the file backend.
- **Remote-mode databases** (`mode: remote` with a `connection`) are validated
  and persisted, and the factory never constructs the remote proxy: such a
  database is opened on a local store like any other.
- The etcd protocol serves a database of any backend on a raft-mode node;
  nothing on the data path checks the backend (the target design adds that
  check, "Combinations that are refused").

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
`<tenant>/<db>/` key prefix is not part of the data plane of any
database that has its own store.

### Tenant isolation × backend composition

The tenant's isolation mode decides **where** a binding lives and **whose key**
encrypts it. The database's backend decides **what kind of store** it is. The
two compose; neither overrides the other.

| Tenant isolation | Data root of the database's store | Encryptor | Host for `backend: raft` |
|---|---|---|---|
| `prefix` | node data root, `{DataDir}/tenants/<tenant>/databases/<db>` | the node at-rest encryptor — prefix isolation is, by definition, a tenant with no key of its own | system host, shared log |
| `barrier` | node data root, as above | **the tenant's barrier** — the barrier covers the tenant's database data, not only its IAM data | system host, shared log |
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
  tenant's (OD-6). Every database has a store of its own, so the only
  barrier refusal is the one on `memory`, below.
- **A sealed barrier — the tenant's or the database's — fails every engine of
  the database closed**, vector and time series included.

The group granularity of `backend: raft` — one group per database for every
isolation mode (OD-1) — is what makes the right-hand column
per-database for every isolation mode.

### Combinations that are refused

Refusals happen at resolution, before anything is created, with typed errors.
None of them degrades to another backend. `database.ResolveBinding` returns
each `database.*` refusal below except `database.BindingOwnerUnavailableError`,
which does not exist yet. Under the binding,
`database.SharedStoreBarrierUnsupportedError` and
`database.BarrierOffDataPathError` have no condition left to refuse: every
database's barrier layers on its own binding, which is its data path.

| Combination | Refused with | Why |
|---|---|---|
| `backend: raft` on a standalone node | `database.RaftBackendUnavailableError` | there is no Raft host to place the group on |
| `replicas`, `dedicated_wal` or any `raft` option on a non-raft backend | `database.RaftOptionsRequireRaftBackendError` | replication is a property of the raft binding only |
| a per-database barrier on `backend: memory` | `database.BarrierRequiresPersistentBackendError` | nothing is at rest to encrypt; the barrier would be decorative |
| etcd protocol on a non-raft backend | `database.EtcdRequiresRaftBackendError` | etcd's revision counter needs a replicated log |
| `data_dir` outside the tenant's data root (volume, raft tenants) | `database.DataDirOutsideTenantRootError` | the tenant's contract is that its data lives under its root |
| `file` or `objstore` for a volume or raft tenant | `database.BackendIsolationConflictError` (OD-7) | those backends expose key names outside the tenant's encryption |
| any backend while the tenant's or the database's barrier is sealed | `service.BarrierSealedError` at request time | fail closed |

A non-replicated backend in a multi-node cluster is **not** refused (OD-2): the catalog records its owning node and every other node forwards its
requests there. When the owner cannot be reached the request fails with
`database.BindingOwnerUnavailableError`, which names the owner.

### What stays node-level

Node configuration keeps only what is genuinely a property of the node:

| Setting | Meaning under the binding |
|---|---|
| run mode (`raft` / `standalone`) | whether the node hosts Raft groups at all |
| control-plane store — `raft.sm_type` / `raft.sm_storage` (raft mode), `storage.backend` (standalone) | the store that holds the system tenant's catalog, users, tenants, placement and the system database; **never** another database's data |
| `database_defaults` — `backend`, `replicas`, `raft.clusterMode`, `raft.state_storage` (not yet configuration keys; `database.Defaults` holds the four fields) | filled into a database record at creation when the caller left the field empty; never consulted again for that database |
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
   cluster feature gate (rule 6): a drop in a group whose members do not all
   support it is refused with a typed error, never run unfenced.
2. **Destroy the store** — stop and purge the Raft group on every replica, or
   close and remove the local store's directory or object prefix, and remove
   the vector index checkpoints under the data root.
3. **Remove the barrier**, if the database had one.
4. **Remove the catalog record.**

Dependents first and the resolvable record last, for the same reason as
the shipped drop. The keyspace walk below remains the reclamation for the control-plane
store only.

## Shipped design — alternatives rejected

- **One global configuration.** Rejected on the problem above.
- **Per-database barrier off the data path.** **Refused by the code**, not
  merely discouraged: a barrier is only meaningful when it wraps the store the
  database is served from. In the shipped code that is one shape — a non-raft database with
  an explicit `--data-dir`, which only the system tenant can set. A database
  sharing the default store, a `backend: raft` database (served from its Raft
  group) and every database of a volume- or raft-isolated tenant (served from
  the tenant's own store) are all refused: the barrier would wrap a store no
  request reads, and sealing it would leave the data readable and writable.
  A non-system tenant therefore cannot create a per-database barrier; the
  storage binding (target design) puts the barrier on the data path of every
  backend.
- **Recreating a database to change its replication factor.** Rejected:
  `database scale` adds and removes voting replicas in place.
- **A global etcd facade.** Rejected — see
  [../etcd-compatibility/](../etcd-compatibility/architecture.md). An etcd
  listener serves exactly one database's keys, watches and leases, resolved
  through the service registry like every other transport.

## Target design — alternatives rejected

- **Keep the tenant isolation mode as the thing that decides the store, and
  treat the database backend as advisory.** Rejected by rule 2: a recorded setting that does not decide anything is a
  setting operators rely on and cannot see is ignored.
- **Per-engine stores inside one database** (a vector store beside a KV store
  beside a time-series store). Rejected: a write that touches two engines —
  a document and its embedding, a row and its secondary index entry — could no
  longer be one atomic apply, and each extra store needs its own snapshot,
  backup, barrier and drop.
- **A node-local vector store, replicated later.** Rejected: that is the
  current state, and it is why vector data has neither
  replication nor tenant encryption. Vector is an engine of the binding from
  the start.
- **An engine-type field that turns engines on per database.** Rejected by
  rules 1 and 3. Build tags still decide which engines a binary
  carries; a database never decides it.
- **Emulating atomic batches on object storage by ordering writes.** Rejected:
  the storage interface promises an all-or-nothing batch, and every engine
  depends on it. The objstore backend has a commit journal, as the file
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
database's backend, shard and barrier. This is the single point where a
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
   enter anymore. Keys go in range-delete proposals of at most 1,024 listed
   keys each (`protocol.OpDeleteRange`, which every engine and every `sm_type`
   applies as a batch of deletes over the live keys in the range: indexes,
   history, lease bindings and per-key watch events match one-at-a-time
   deletes, committed at one revision rather than one each — see
   [engineering.md](engineering.md), "Range delete command"). On a shard that has not activated the
   `range-delete` cluster feature, the rest of the prefix is deleted one key
   per proposal instead.
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
keyspace scan and the catalog removal would survive the reclamation that ran
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
whose record carries none, or that had no store to mark when it was created —
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
write, with its check-then-write window. Activation is permanent once it happens, and
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
of `Database.Status`. Status answers its own question —
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
clears the fence the same way the local one is cleared. A failure removing
the placement entry itself is not swallowed: the fence survives with the
entry, so the database stays refused rather than becoming simultaneously
unreachable (the local catalog record is already gone) and unrefused (no
replica has any signal that it is still being torn down) — an operator's
retry (or a resumed `DropForTenant`) finishes the removal.

**Every write the fence can refuse carries it, on every transport, because
the guard rides the request, not the verb.** A database's views attach the
guard to the request context (`service.WithWriteGuard`), and every Raft
proposal `pkg/service` makes goes through one path that wraps the command
with the context's guard. A write verb cannot forget the guard, because no
verb proposes any other way. The views that carry it are the
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
being silently discarded itself. A replica removal confirms its snapshot
with a CAS before evicting the replica from Raft, and records the eviction
afterwards with `Store.RemoveReplica`, which applies to the entry as it then
stands. A KV backend with no `AtomicKV` support
is refused with `*placement.AtomicWriteUnsupportedError` (inside the
`*placement.StoreError` naming the entry) rather than falling back to an
unconditional `Put`, which would reopen the lost-update race the CAS exists to close (two
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
while the store still refuses its writes. Persisting
`Placement.DropCommitted` (below) is held to the identical standard: a failure to set
it fails the whole `Drop`/`DropForTenant` call rather than being logged and
discarded, so a later retry is never stranded believing a marker was set
that never actually committed.

**`Placement.DropCommitted` tells a legitimate retry-after-teardown apart
from a request that reached a node with no local record.**
`manager.DropForTenant` returning `NotFoundError` is otherwise ambiguous:
it means either "an earlier attempt on this node already finished the local
drop" (a legitimate retry that only needs to finish the placement teardown)
or "this node never had the local record at all" (refused upfront, per
above). `DropCommitted`, set
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
a database whose local fence survived the restart, the resolution path
`Drop`'s tenant-ID lookup would otherwise use. The error carries the tenant
ID (`DroppingError.TenantID`), recovered from the fenced record before
the refusal, so `Drop` can still identify which tenant's placement entry to
finish tearing down instead of leaving it permanently stranded.

**A cached shard cannot outlive the placement entry it was resolved
through.** `RaftBackendFactory.Services`'s cache-hit path re-checks the
fence on every call, but a completed drop's last step removes the WHOLE
placement entry, not merely its `Dropping` flag — at that point there is no
fence left to read at all, and the resolver's own placement lookup reports
"no entry," identically to a database that never had one. The factory tells the two apart by recording, on the cached shard
itself, whether it was originally resolved through a real placement entry;
if it was, and that entry is now gone entirely, the shard is refused rather
than served stale — closing the asynchronous window between the placement
delete and the `TenantShardReconciler`'s own (later) cache eviction.

**The local fence's in-memory flag commits before its durable one, not
after.** `Manager.Drop`'s step 0 sets the in-memory flag
(`Database.SetDropping`) FIRST, then persists `DatabaseMeta.Dropping` to
`meta.json`: `loadDatabase` (the seam every
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
- **collection records at the store root** — `__sys:collections/<db>/…` and
  `__col:<db>:…`. These name a database and no tenant, so nothing can attribute them to the tenant issuing
  the drop, and a drop that deleted them by database alone would delete every
  other tenant's. They are left in place, inert: nothing reads them, because
  every transport's key surface is namespaced and no caller can compose the bare
  key, and nothing deletes them. A deployment that has them exports and re-imports
  them per tenant; see
  [collections/engineering.md](../collections/engineering.md) "Upgrading across
  the tenant-namespace change".

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
  one node-wide namespace, so every tenant-scoped operation, every data-plane
  resolution and SQL `HasDatabase` / `USE` answer a database of another tenant
  (other than the configured default SQL database, `sql.default_database`,
  which every tenant has in its own namespace)
  exactly as they answer a name nobody holds — `database.NotFoundError`, never
  an access denial that would tell the caller the name is taken. The ad-hoc
  SQL query (`POST /sql/query`, `SQLQuery`) asks the service registry before
  the node-wide catalog, so another tenant's name and an unused name get the
  registry's one not-found rather than two different errors. The one
  remaining signal is creation: a name another tenant holds cannot be created
  (`database.AlreadyExistsError`).
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
  certificate as `CN=<username>`, `OU=[<database's tenant>, <profile>]`, with
  no SAN; the CSR contributes only its public key. A holder certificate
  authenticates with no roles, as for every other scope.
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
  `officer_unenrol`, `initialize_shamir`, `rekey` and `officer_revoke`
  permissions are
  self-scoped like the rest of `databases:*`; the database service confines
  each call to a database in the caller's tenant before it learns whether the
  database or its barrier exists. Claim and quorum are identity-gated: the
  holder's certificate and factor proof are the credential. There is no
  cross-tenant database access: every caller, the system tenant's Admin,
  Security Officer, Auditor and share holders included, is confined to its
  own tenant's databases by the same ownership check. That check is one
  function, `database.OwnedBy` (a database recorded with no owner is the
  system tenant's), applied through `database.Manager.GetOwned` by the
  manager's and the database service's `*ForTenant` operations, the service
  registry and catalog, the dedicated-raft backend, the etcd listener and the
  SQL provider alike; none restates it and none forks on the system tenant.
  Another tenant's database answers `NotFound`, exactly as a missing one does,
  so no route reveals another tenant's database names, and a create request
  naming another tenant makes a database the caller owns.
- **Tenants.** A Shamir barrier is held to the passphrase barrier's rule: it
  is accepted only on the database's data path, a non-raft database with its
  own data directory, which only the system tenant can set. A non-system tenant
  therefore cannot create one; the storage binding (target design) allows it,
  as it does the passphrase barrier.
- **Where it lives.** The database barrier's wrapped key and holder roster
  live with the barrier's metadata in the database's dedicated store, beside
  the passphrase barrier's: a Shamir database barrier needs a dedicated store
  exactly as a passphrase one does. Holder records live under the database's
  barrier (encrypted at rest once it is initialized). After a restart the
  database opens sealed and its holders' authority is re-installed; a quorum
  unseals it.
- **Drop.** Dropping the database destroys its holder records, every envelope
  still held for its holders and every pending challenge.

Rejected: a per-database custodian system or registry (there is one
share-holder model); reusing the tenant's holders for its databases
(different keys need different custody, and a holder's share is bound to one
roster); recovery keys per database (a database barrier, like a tenant
barrier, has the unseal domain only).

## Verifiable log

> **Status: engine implemented; service and surface not.** In the code:
> `pkg/vlog`, `protocol.OpVerifiableLog` (`pkg/protocol/vlog.go`),
> `featuregate.VerifiableLog`, `statemachine.ApplyVerifiableLog` dispatched by
> both state-machine families, and the reads behind `vlog.Request`. Not in the
> code: the service, transports, SDK, CLI, `vlog:*` permissions, the sign-head
> seam and the audit switch; the names this section gives them are proposed.
> [Decided](#decided) states what the design does not reopen; [Design](#design) implements it, and its
> [§9](#9-design-choices) records its choices with the alternatives not taken.
> One choice is still open, with go-xkms: [Open questions](#open-questions).

### Decided

Each item is stated as a fact the design does not reopen.

- **One database type.** go-qrdb's whole part in the trusted service plane is a
  *verifiable log*, created per tenant like key-value, time-series and vector:
  append-only (no update, no delete), sequence-numbered entries, a Merkle tree
  over them, inclusion proofs, consistency proofs between tree sizes,
  checkpoints, and tree heads signed by the log owner's key (the CA key for
  go-xkms's transparency log, a tenant key for a tenant's own log). It is served
  on every transport and in the SDK with the tenant's isolation, DEK, barrier
  and replication.
- **No PKI semantics.** What is logged, the signed-head policy and any CA or
  service meaning belong to go-xkms (its transparency log is a CA service) and
  to go-quicbaas (which appends its own records through go-xkms). go-qrdb owns
  the primitive.
- **Engine surface:** create, append, get, proof-inclusion, proof-consistency,
  head, and a sign-head hook — a seam go-xkms implements with its KMS or
  hardware signer. go-qrdb ships only the software signer over a tenant key.
- **No new state machine.** The log is `vlog.Chain`, which the audit chain
  wraps (`audit.ChainOn`). There is one apply-time opcode,
  `protocol.OpVerifiableLog`, framed like `protocol.OpAuditAppend`, behind a cluster feature gate
  for mixed-version groups, dispatched by both state-machine families, attached
  unconditionally in the Raft factory and in `lifecycle.NewLocalEngine`, and
  restored on open and on snapshot. The sequence is assigned at apply.
- **Three generalisations:** a Merkle tree with inclusion and consistency
  proofs; named per-tenant log instances; owner-signed heads. A head is signed
  outside apply and appended as a record. The audit trail becomes one log
  instance.
- **Move, never delete and rewrite.** Code the log takes over from
  `pkg/audit` moves, with its history and tests, to `pkg/vlog`.
- **Authorization:** `vlog:sign_head` is an Operator permission, never Admin,
  as `keys:sign` is, and Admin creates and expires logs
  ([Authorization](#authorization)).

### What already exists

The engine half of the log and the replicated audit chain share one mechanism
([../audit/architecture.md](../audit/architecture.md#cluster-wide-replicated-trail)):

| Piece | Where | What it does |
|---|---|---|
| `vlog.Chain` | `pkg/vlog/log.go`, `pkg/vlog/named.go` | The per-state-machine registry: an in-memory head per audit tenant (`headState{seq, lo, digest}`) and per named log (`vlog.LogID{TenantID, Database, Log}`), restored from storage, updated only in Apply. `Create`, `AppendEntries`, `Checkpoint` and `ExpireBodies` are the named-log mutations; the Merkle tree comes from `github.com/transparency-dev/merkle` (`compact`, `rfc6962`, `proof`). |
| `audit.Chain` | `pkg/audit/chain.go` | The audit trail over a `vlog.Chain` (`audit.ChainOn`). `Append` assigns the next sequence and a linear SHA-256 digest (`digest = SHA-256(prev ‖ event JSON)`, `PrevHash` embedded in the event); `Prune` drops a retired prefix; `Query` and `Verify` read. |
| The tables | `pkg/vlog/table.go` | Two reserved raw-keyspace tables: `TablePrefix = "\x00qrdb.audit/"`, with `h/<tenant>` head records and `e/<tenant>/<seq>` event records, and `LogTablePrefix = "\x00qrdb.vlog/"` for named logs. `vlog.IsTableKey` keeps both out of KV `Range` and watch. |
| The opcodes | `pkg/protocol/audit.go`, `pkg/protocol/vlog.go` | `OpAuditAppend` (79) with sub-types `AuditSubtypeAppend` and `AuditSubtypePrune`; `EncodeAuditAppend` refuses a zero timestamp or a tenant mismatch before proposal. `OpVerifiableLog` (82) with sub-types `VerifiableLogSubtypeCreate`, `…Append`, `…Checkpoint` and `…ExpireBodies`, built by the `EncodeVerifiableLog*` functions. |
| The gates | `pkg/cluster/featuregate/featuregate.go` | `featuregate.AuditAppend` (`"audit-append"`) and `featuregate.VerifiableLog` (`"verifiable-log"`). |
| Dispatch | `pkg/engine/mvcc_engine.go` (`handleAuditAppend`, `handleVerifiableLog`), `pkg/store/pebble.go` (`pebbleHandleAuditAppend`, `pebbleHandleVerifiableLog`), `pkg/statemachine/vlog_apply.go` (`ApplyVerifiableLog`) | Both families apply both opcodes, answer `audit.QueryRequest`, `audit.VerifyRequest` and `vlog.Request` (`HeadRequest`, `GetRequest`, `InclusionRequest`, `ConsistencyRequest`) in their read path, restore the registry on open and snapshot recovery, and resync it after a failed apply. |
| Attachment | `pkg/store/factory.go` (`WithLogs`), `pkg/store/mvcc.go` and `pkg/store/pebble.go` (`SetLogs`), `pkg/engine/mvcc_engine.go` (`WithMVCCLogs`), `cmd/qrdbd/start.go` (`buildSMCreateFunc`), `pkg/server/tenant_host.go` (`newDataPlaneCreateFunc`), `pkg/server/lifecycle/local.go` (`NewLocalEngine`) | Every create function builds a fresh `vlog.Chain` for each state machine it creates, unconditionally: the system shard, every tenant and database group, and every local engine. No two state machines share heads. |
| Proposer and reader | `pkg/service/audit_replicated_logger.go` (`ReplicatedLogger`), `audit_optin_logger.go`, `audit_shard_resolver.go`, `audit_errors.go` | Routes every audit append to the system shard (`SystemShardAuditResolver`; a dedicated-isolation tenant is refused with `DedicatedTenantAuditNotSupportedError`), checks the gate, sets the timestamp, proposes; reads back through `SyncRead`. Nothing proposes `OpVerifiableLog` yet. |
| Node-local trail | `pkg/audit/chain_file_logger.go`, `chain_verify.go`, `trail.go`, `trail_dir.go`, `verify.go` | The hash-chained NDJSON file a node writes, its rotation-set verifier and the trail composition. A separate mechanism from the replicated chain. |

### Design

**Constraints the design holds to.** Both modes, standalone and Raft (§1);
nothing node-local (§1); strict tenant isolation (§8); every cryptographic
primitive from an audited library (§4, §6, §9). These are the platform's
standing rules.

#### 1. Modes

The log lives in the state machine's own storage, under a reserved raw-keyspace
table, so the same code serves both modes with no branch on mode:

- **Raft.** An append is a proposal through the tenant's resolved shard — the
  system shard for the system tenant, the tenant's own group for an isolated
  tenant — and every replica applies it identically. Reads that must be current
  (head, proofs at the latest size) are linearizable reads through the group;
  any node serves them, forwarding as replica forwarding already does.
- **Standalone.** `lifecycle.NewLocalEngine` attaches a fresh `vlog.Chain`
  (`engine.WithMVCCLogs`), and a proposal applies locally through the
  same handler. A standalone node is one replica; nothing about the handler
  knows the difference.

Nothing is node-local: entries, tree nodes, heads, checkpoints, log descriptors
and the software signer's sealed key are all records in the tenant's resolved
store, so a backup of the tenant is a backup of its logs.

#### 2. The opcode and its sub-types

One opcode, `OpVerifiableLog`, in `pkg/protocol/vlog.go` beside `audit.go`,
framed the same way: `[subtype:1][tenantIDLen:2][tenantID][body]`.
The body names the log (database and log name) and carries the sub-type's
fields.

| Sub-type | Body | Apply does |
|---|---|---|
| `Create` | log name, `vlog.Descriptor` (`Origin`, `Hash`, `VerifierKeys`, `AllowExpireBodies`) | Refuses a name that exists; stages the descriptor record and an empty head. |
| `Append` | one entry, or a bounded batch of entries | Assigns each entry the next index, stages the entry, its leaf hash and every tree node the append completes, and the new head (§4). Returns the first assigned index. |
| `Checkpoint` | tree size, root hash, signed checkpoint bytes | Checks that the size is no larger than the log's size and that the root is the tree's root at that size (§5); stages the checkpoint record. |
| `ExpireBodies` | cutoff index | Only on a log whose descriptor allows it, a flag set at create — the audit instance's does, for its retention (§9, decision 8). Proposed by an Admin (`vlog:expire`) or by the audit retention scheduler. Deletes entry bodies below the cutoff; leaf and node hashes stay, so the tree and every proof remain valid. |

There is no sub-type that updates or deletes an entry, and no per-log delete:
a log dies with its database, through the drop and keyspace reclaim every
database type already uses
([What a drop reclaims](#what-a-drop-reclaims-current-state)).

Refusals follow the audit handler's rule exactly: a malformed body, a missing
log or an out-of-range checkpoint is a deterministic consequence of the
committed bytes, answered with `statemachine.RefusalResult`; a table-write
fault is local to the replica and halts apply.

#### 3. The feature gate, and a mixed-version group

The gate, `featuregate.VerifiableLog` (`"verifiable-log"`), in
`pkg/cluster/featuregate/featuregate.go`. A binary that carries the log
advertises it.

- **Before the gate is enabled for a group** — some replica runs a binary
  without the log — the service refuses every create, append and checkpoint
  for a log on that group with a typed `LogFeatureNotActiveError` before
  proposing, as `ReplicatedLogger.Emit` refuses with
  `AuditFeatureNotActiveError`. Reads of a log that does not yet exist
  answer not-found. Nothing is proposed, so nothing diverges.
- **The audit instance keeps working unchanged while the gate is closed.** The
  service goes on proposing `OpAuditAppend`, and every binary — old and new —
  applies it to the linear chain only. No new binary writes a Merkle
  node for an `OpAuditAppend`, so old and new replicas stay identical.
- **Once the gate is enabled** for the group, the service proposes audit events
  as `OpVerifiableLog`/`Append` to the audit instance (§7). The switch is a
  property of the committed log, not of any node's view: every entry before it
  is an `OpAuditAppend`, every entry after it an `OpVerifiableLog`, and every
  replica applies both identically.
- After activation (every node advertises it), the gate is permanent, as for
  every gate: a binary without the log refuses to start.

`OpAuditAppend`'s apply stays in both dispatch tables. Its entries are in
existing Raft logs and snapshots, and a replica replaying them must apply them
as they were applied. Proposers stop emitting it once the gate is enabled.

#### 4. Apply-time sequencing and the tree

Everything in apply follows `vlog.Chain`'s rules, which exist because Raft may
pack any number of committed entries into one `Update` call:

- **The index is assigned at apply** from the log's in-memory head, never from
  the proposal.
- **Apply never depends on whether an earlier entry of the same `Update` call is
  visible in storage.** The log keeps, per state
  machine, the in-memory head of each log and the tree nodes it staged in the
  current `Update` call, and reads through them.
- **Hashing happens at apply** (§9, decision 3). The log keeps each log's
  *compact range* — the root hashes of the perfect subtrees covering
  `[0, size)`, at most one per bit of `size` — in its head. Appending a leaf
  hashes it, merges it into the compact range, and reports every perfect
  subtree node the merge completes; apply stages each completed node. That is
  one leaf hash and on average one node hash per entry, and on average two node
  records per entry. The Merkle algorithms (leaf and node hashing, the compact
  range, proof construction and verification) come from `github.com/transparency-dev/merkle` (§9, decision 1); go-qrdb writes no hashing of its own.
- **The head record** holds the size, the low-watermark below which
  `ExpireBodies` has removed entry bodies, and the compact range; the audit
  instance keeps its linear digest as well (§7).

Records, all under `LogTablePrefix`, which `vlog.IsTableKey` keeps out of KV
`Range` and watch; indexes and sizes in keys are 8-byte big-endian:

| Record | Key (under the log's prefix) | Value |
|---|---|---|
| Descriptor | `d` | the `vlog.Descriptor` set at create |
| Head | `h` | size, low-watermark, compact range |
| Entry | `e/<index>` | the entry bytes as appended |
| Tree node | `n/<level>/<index>` | the hash of a completed perfect subtree |
| Checkpoint | `c/<size>` | the signed checkpoint for that size |

The log's prefix is `<LogTablePrefix><tenant>/<database>/<log>/`, and
`LogID.Validate` refuses a `/` in any of the three names (and an empty
database or log name), so no name can carve a segment inside another's range.

**Proofs are reads.** An inclusion proof for index *i* at size *n*, and a
consistency proof between sizes *m* and *n*, are each a bounded set of node
identifiers the library computes; the read fetches those node hashes (every one
is a stored perfect subtree, or a short rehash of stored ones the library
describes) — O(log n) point reads, no tree walk. Proofs at any size up to the
current one are served, because every perfect node ever completed is stored.

#### 5. Signed heads, outside apply

A signature is never computed in apply: the signer is a key the owner holds,
possibly in a device, and apply must be a pure function of committed bytes.

1. A caller with the sign-head permission asks the service to sign the head of
   a log (on demand — §9, decision 4).
2. The service reads the head through the group (size *n*, root *r*), builds
   the checkpoint body for (origin, *n*, *r*) as a C2SP checkpoint (§9, decision 2), and asks the sign-head seam (§6) for the owner's signature.
3. The service verifies the returned signature against the log's registered
   verifier key, then proposes `OpVerifiableLog`/`Checkpoint` with *n*, *r* and
   the signed bytes.
4. Apply checks *n* ≤ size and that *r* is the tree's root at *n*, computed from
   the stored perfect nodes covering `[0, n)` read through the current call's
   staged nodes. It does **not** verify the signature: signature verification
   is done by the proposing service and by every client, and keeping it out of
   apply keeps apply independent of a verifier library's version in a
   mixed-version group. A checkpoint whose root does not match is refused.
5. The checkpoint is appended as a record (`c/<size>`), append-only. Reading the
   latest checkpoint, or the one at a size, is the `head` operation.

A checkpoint is not a leaf of the tree it describes: a head that entered its own
tree would describe a tree it had already made stale. The record is beside the
tree, append-only, in the same table and the same snapshot.

**Key rotation** is a descriptor change through the same opcode: the
descriptor's verifier keys are an append-only list, and a checkpoint names the
key that signed it. Which key signs, and when keys rotate, is the owner's
policy. Rotating the software provider's key, not built yet, appends the
`k/<version+1>` record and the descriptor's new `KeyRef` in one proposal.

#### 6. The sign-head seam

In `pkg/vlog/signing.go` (§9, decision 7). `HeadSigner` has the shape of
`golang.org/x/mod/sumdb/note`'s `Signer` (`Name`, `KeyHash`, `Sign`, in that
module's `sumdb/note/note.go`) with a context added, plus the key version, the
signature type and the public key a signed head is checked against. The
signature types are the two RFC 9162 registers (section 10.2.2): Ed25519, the
default, and ECDSA P-256 with SHA-256 (ASN.1 DER). A signed head carries its
signature type and key version; a key of any other type is refused with
`UnsupportedKeyTypeError`.

```go
// HeadSigner signs a log's checkpoints with the log owner's key.
type HeadSigner interface {
	// Name is the key name the checkpoint's signature line carries.
	Name() string
	// KeyHash is the key hash the signature line carries.
	KeyHash() uint32
	// KeyVersion is the version of the key that signs.
	KeyVersion() uint32
	// SignatureType is the RFC 9162 signature type Sign produces.
	SignatureType() SignatureType
	// Public is the key a signed head verifies against.
	Public() crypto.PublicKey
	// Sign returns the signature over msg, the checkpoint body, in the
	// signer's signature type.
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// HeadSignerProvider returns the signer for one log's owner key.
type HeadSignerProvider interface {
	HeadSigner(ctx context.Context, req HeadSignerRequest) (HeadSigner, error)
}

// HeadSignerRequest names the log and the owner's key for it.
type HeadSignerRequest struct {
	TenantID string
	Database string
	Log      string
	// KeyRef is the owner's reference to its key, recorded in the
	// descriptor at create. The software provider reads it as the decimal
	// version of the log's k/<version> record; another provider's
	// reference is its own.
	KeyRef string
}
```

- **Injection:** `WithHeadSignerProvider(p HeadSignerProvider) ServerOption` in
  `pkg/server/seam_options.go`, beside `WithDEKProvider`, composed through
  `sdk/go/node` like the other seams. Nil keeps the software provider.
- **The software provider go-qrdb ships** (`SoftwareHeadSignerProvider`) signs
  with a key generated at create (`crypto/ed25519` or `crypto/ecdsa` P-256
  over `crypto/rand`, the standard library's validated module). The key is a
  PKCS #8 record, `k/<version>` under the log's prefix, written by Create in
  the same Raft proposal as the descriptor and head, so a create either lands
  with its key or not at all; the descriptor's `KeyRef` names its version. The
  record is sealed at rest by the same barrier as the log's other records
  (§8), travels in the snapshot, and is outside KV `Range` like the rest of
  the table. Its public key is the descriptor's verifier key. Any node can
  sign; the key never leaves the server.
- **What go-xkms implements:** a `HeadSignerProvider` that resolves `KeyRef` to
  its KMS or hardware key, and for its transparency log returns a signer over
  the CA's key (the same `crypto.Signer` source `pkg/cakey.Provider` exposes),
  as an ECDSA P-256 signer. go-quicbaas composes go-xkms's provider; it does not
  implement one.

#### 7. The audit trail as one log instance

The replicated audit chain becomes the log named `audit` in each tenant's
reserved system database, with a descriptor that allows `ExpireBodies` and keeps
the linear digest. Its existing records keep their keys under
`"\x00qrdb.audit/"`: the audit instance's prefix **is** the existing table, so
the upgrade moves no data.

- **Existing data reads unchanged.** Every record `OpAuditAppend` wrote stays
  where it is, `Query` reads it as it does, and `Verify`'s linear fold covers it
  as it does.
- **The tree starts at the first `OpVerifiableLog` append** to the instance, at
  sequence *S₀*, as leaf 0; the descriptor records *S₀*. That first leaf's bytes
  are the marshaled event with its `PrevHash`, the linear digest of everything
  before it, so the tree commits transitively to the whole pre-upgrade chain.
  No backfill command, no migration of stored records.
- **The linear chain continues** for the audit instance: each append still sets
  `PrevHash` and advances the linear digest, so `ChainContinuityReport` and
  every existing verifier keep their meaning. Named logs do not keep a linear
  digest; their Merkle root is their head.
- **Retention** stays a replicated, deterministic, bounded command: on the
  audit instance `Prune` becomes `ExpireBodies`, which removes event bodies and
  keeps the tree (§9, decision 8).
- **The node-local trail is unchanged.** `ChainFileLogger` is a different
  mechanism — one node's file, written before the store is unsealed, and the
  sink an audit-write failure falls back on — and this design neither moves nor
  generalises it.

#### 8. Isolation, barrier, DEK and replication

- **Isolation.** A log is addressed by (tenant, database, log). The service
  confines the tenant exactly as audit confinement does (`tenant.Confine`); the
  resolver routes to the tenant's own store; the key prefix contains the tenant,
  so a read never leaves the tenant's range even inside a shared table. In
  strict mode every record is in the tenant's own store.
- **Barrier.** The tenant's (or the database's) barrier encrypts the records at
  rest, below apply, as it does for every other record. Apply therefore hashes
  the entry as appended.
- **The signing key.** The software signer's `k/<version>` record is one of
  the log's records, so the same barrier seals it at rest; there is no second
  key layer. A caller that wants entries
  the platform cannot read encrypts them with `DEKService` before appending; the
  leaf then commits to the ciphertext, which is the caller's choice and the
  standard shape for a confidential transparency log.
- **Replication.** Raft, through the tenant's group, like everything else.
- **Limits.** One entry is bounded by the pre-authentication request cap every
  transport applies, and an `Append` batch by a per-command maximum, as
  `MaxPrunePerCommand` bounds a prune.

#### 9. Design choices

Each choice is kept with the alternatives not taken and the reason each lost.

1. **Merkle library and hash: `github.com/transparency-dev/merkle`,** RFC 9162 /
   RFC 6962 tree hashing over SHA-256 (`0x00` leaf and `0x01` node prefixes),
   through its `rfc6962` hasher and its `compact` and `proof` packages. The
   compact range gives exactly the apply-time node visitor §4 needs, and it is
   the hash every CT log and verifier uses. Not taken:
   `golang.org/x/mod/sumdb/tlog` — the same tree hashing and already an indirect
   dependency, but shaped around tiles and its own hash reader, a less direct fit
   for per-append node staging; it stays compatible because the tree is the same.
   Trillian as a service — a second datastore, refused by the single-datastore
   rule.
2. **Checkpoint format: C2SP `tlog-checkpoint` in a C2SP `signed-note`**:
   origin, size and root as text lines, signatures as note lines, witnessable by
   the C2SP cosignature ecosystem. `golang.org/x/mod/sumdb/note` signs and
   verifies its Ed25519 type, the only type that module defines (`algEd25519`, in
   its `sumdb/note/note.go`); that is the software provider's type. Not taken: an
   RFC 6962 `SignedTreeHead` structure (binary, not witnessable by the note
   ecosystem, superseded by RFC 9162) and a go-qrdb format (nothing would verify
   it). A non-Ed25519 owner key signs ECDSA P-256 with SHA-256, the other
   RFC 9162 type and signed-note type `0x02`
   ([Open questions](#open-questions), ruled (b)).
3. **Hashing at apply** (§4): the tree is always current and atomic with the
   entry, with one proposal per append and about two SHA-256 and two record
   writes per entry. Not taken: an integrator proposing tree hashes after commit
   (proofs lag appends, two proposals, a second moving part, replicas re-checking
   its hashes), and per-node computation from committed entries (node-local
   derived state).
4. **Heads signed on demand** (§5): `SignHead` signs the current head when a
   caller holding `vlog:sign_head` asks; the cadence is the owner's policy. Not
   taken: a per-log interval acted on by a group-leader scheduler, which would put
   signing policy in go-qrdb; it stays additive if a consumer asks for it.
5. **A build tag on the service and the transports:** `WITH_QRDB_VLOG ?= 1`,
   tag `qrdb_vlog`, like `WITH_QRDB_TS` and `WITH_QRDB_VECTOR`. Off removes the
   named-log service, its transport handlers, the SDK surface and the CLI, and
   `admin capabilities` omits `vlog`. The software HeadSigner provider
   (`pkg/vlog/signing_software.go`) is behind the tag too; without it the stub
   refuses every signer with `vlog.NotCompiledError`, an absent capability. The engine core, the opcode's apply and the
   audit instance are never tagged: the audit trail depends on them, and a
   replica must apply what its group commits whatever its build, so a binary
   built without the tag still advertises the gate.
6. **Tiles later.** Proofs and checkpoints go through the service now; C2SP
   `tlog-tiles` is added later as a read-side rendering of the stored node
   records (§4 stores every perfect node, so a tile is a read of 256 of them),
   with no change to storage.
7. **The package is `pkg/vlog`.** It holds the generic half of the chain
   (`vlog.Chain`, `pkg/vlog/log.go`) and both tables (`pkg/vlog/table.go`);
   `pkg/audit` keeps the audit instance (event validation, `PrevHash`, `Query`,
   `Verify`) and the node-local trail, and `audit.Chain` is the audit instance's
   adapter over a `vlog.Chain` (`audit.ChainOn`). Not taken: generalising `Chain` in place in
   `pkg/audit`, which would make the audit package own a database type; and the
   name `pkg/tlog`, which collides with `golang.org/x/mod/sumdb/tlog`.
8. **Retention by `ExpireBodies`** (§2, §7): allowed only on a log whose
   descriptor allows it; bodies below the cutoff are removed, leaf and node hashes
   stay, so the tree and every proof stay valid and an expired entry reads as
   expired. Not taken: no retention on the audit instance (a compliance trail
   that must age out would grow without bound), and keeping the audit chain
   outside the log type (contradicts [Decided](#decided): the audit trail becomes
   one log instance).

### Remaining work

What [What already exists](#what-already-exists) lists is in the code. Each
remaining step leaves the tree green:

1. The log service (`pkg/service/vlog_service.go`), composed in
   `pkg/server/lifecycle/lifecycle.go`; the sign-head seam and its software
   provider (§6); the transports, SDK and CLI ([Surface](#surface)); the
   `vlog:*` permissions ([Authorization](#authorization)).
2. The audit switch (§7): `ReplicatedLogger.Emit` proposes to the audit
   instance through the log service once `featuregate.VerifiableLog` is enabled
   for the group, and `OpAuditAppend` before; the log service shares the audit
   resolver. The audit transports, SDK and CLI keep every capability and its
   shape; the audit instance is also reachable through the log surface by a
   caller holding the log permissions on its own tenant.

### Surface

#### Engine

`pkg/vlog` addresses every log by a `vlog.LogID` (tenant, database, log). The
mutations are `vlog.Chain` methods called in apply: `Create`, `AppendEntries`,
`Checkpoint` (the apply half of sign-head) and `ExpireBodies`. The reads take a
`storage.Reader`: `ReadHead` (a `vlog.Head`: the current size and root, the
lowest unexpired index and the latest checkpoint), `Get`, `Inclusion`,
`Consistency` and `ReadCheckpoint`; the state machines answer them as
`vlog.Request` values.

#### Service layer

`pkg/service/vlog_service.go`, following `VectorService`:

```go
type LogService interface {
	CreateLog(ctx context.Context, req *CreateLogRequest) (*CreateLogResponse, error)
	ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error)
	Append(ctx context.Context, req *AppendRequest) (*AppendResponse, error)
	GetEntries(ctx context.Context, req *GetEntriesRequest) (*GetEntriesResponse, error)
	ProveInclusion(ctx context.Context, req *InclusionRequest) (*InclusionResponse, error)
	ProveConsistency(ctx context.Context, req *ConsistencyRequest) (*ConsistencyResponse, error)
	Head(ctx context.Context, req *HeadRequest) (*HeadResponse, error)
	SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error)
	ExpireBodies(ctx context.Context, req *ExpireBodiesRequest) (*ExpireBodiesResponse, error)
}
```

Every protocol handler delegates to it. A proof response carries the hashes, the
leaf index and both sizes, so a client verifies it with the same library
without trusting the server.

#### Transports

The six client transports, in one capability matrix in the feature folder's
`api.md`:

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | Permission |
|---|---|---|---|---|---|---|
| Create log | `POST /vlog/log` | `VlogCreate` | `vlog.Create` | `qrdb_vlog_create` | `CreateLog` | `vlog:create` |
| List logs | `GET /vlog/logs` | `VlogList` | `vlog.List` | `qrdb_vlog_list` | `ListLogs` | `vlog:list` |
| Append | `POST /vlog/append` | `VlogAppend` | `vlog.Append` | `qrdb_vlog_append` | `Append` | `vlog:append` |
| Get entries | `GET /vlog/entries` | `VlogGet` | `vlog.Get` | `qrdb_vlog_get` | `GetEntries` | `vlog:get` |
| Inclusion proof | `GET /vlog/proof/inclusion` | `VlogProveInclusion` | `vlog.ProveInclusion` | `qrdb_vlog_prove_inclusion` | `ProveInclusion` | `vlog:proof` |
| Consistency proof | `GET /vlog/proof/consistency` | `VlogProveConsistency` | `vlog.ProveConsistency` | `qrdb_vlog_prove_consistency` | `ProveConsistency` | `vlog:proof` |
| Head | `GET /vlog/head` | `VlogHead` | `vlog.Head` | `qrdb_vlog_head` | `Head` | `vlog:head` |
| Sign head | `POST /vlog/head/sign` | `VlogSignHead` | `vlog.SignHead` | `qrdb_vlog_sign_head` | `SignHead` | `vlog:sign_head` |
| Expire bodies | `POST /vlog/expire` | `VlogExpireBodies` | `vlog.ExpireBodies` | `qrdb_vlog_expire_bodies` | `ExpireBodies` | `vlog:expire` |

REST is `pkg/api/rest`, gRPC adds the methods to
`pkg/api/grpc/proto/qrdbv1/qrdb.proto` (the Unix socket serves the same gRPC
wire), QUIC is `pkg/api/quic`, MCP is `pkg/api/mcp`, embedded is
`sdk/go/transport/embedded`. Every request carries the database the way the
vector and time-series requests do. The compatibility protocols (etcd, MySQL,
PostgreSQL) carry no log: they have no shape for one, as they have none for a
vector namespace.

#### SDK

`sdk/go/transport` gains the request and response types and a `LogProvider`
interface each transport implements; `sdk/go` gains `Client.Vlog()` (nil when
the transport does not implement it, as `Client.Audit()` is) and a verification
helper that checks an inclusion or consistency proof and a checkpoint's
signature against a verifier key, with the same library the server uses.

#### CLI

`qrdb vlog create|list|append|get|prove-inclusion|prove-consistency|head|sign-head|expire`,
on every transport, with `--database` like the vector commands; `prove-*` and
`head` take `--verify` with a verifier key to check the answer client-side.

#### Authorization

New permissions in `pkg/permission/constants.go`: `vlog:create`, `vlog:list`,
`vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head`, `vlog:expire` and
`vlog:sign_head`. Each is tenant-scoped, like the vector permissions, and is
listed in `pkg/permission/self_scope.go` as `keys:sign` is. The built-in roles
are the ones `pkg/permission/role_grant.go` names (`RoleAdmin`, `RoleOperator`,
`RoleUser`, `RoleAuditor`), and the grants go in
`pkg/permission/builtin_role_grants.go`:

| Role | Granted |
|---|---|
| Admin (`registerAdminGrants`) | `vlog:create`, `vlog:expire`, `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` — Admin creates logs and expires entry bodies on the logs that allow it. **Never `vlog:sign_head`.** |
| Operator (`registerOperatorGrants`) | `vlog:sign_head`, beside `keys:sign` in the same grant list, and `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` |
| User (`registerUserGrants`) | `vlog:list`, `vlog:append`, `vlog:get`, `vlog:proof`, `vlog:head` |
| Auditor (`registerAuditorGrants`) | `vlog:list`, `vlog:get`, `vlog:proof`, `vlog:head` — read-only, as the role is |

**Signing a head is a use of the owner's key, so it follows `keys:sign`:** an
Operator permission, never an Admin one.
`vlog:sign_head` is declared in `DutyCryptoUse` by
`registerCryptoUseDuties` in `pkg/permission/builtin_duties.go`, beside
`keys:sign`, so the separation-of-duties guard keeps it off the Security
Officer as well. Creating a log and expiring its bodies are configuration of
the tenant's data, which is Admin's. A cross-tenant request is refused by
`tenant.Confine` before the store is reached, as for audit.

### Open questions

[§9](#9-design-choices) settles the other choices. The one below is ruled and
kept for its record.

1. **The signed-note signature type for a non-Ed25519 owner key** — ECDSA
   P-256 at least, the CA key go-xkms's transparency log signs with.
   *Ruled (b), restricted to RFC 9162's types:* a head is signed Ed25519 (the
   default) or ECDSA P-256 with SHA-256, and carries its signature type and
   key version; any other key type is refused (§6). Rotation appends
   `k/<version+1>` in one proposal (§5). c2sp.org/signed-note, at the
   revision [docs/reference](../reference/README.md) pins, assigns ECDSA
   signature type `0x02` and defines its key ID as the first four bytes of the
   SHA-256 of the DER SPKI public key, without the key name; an Ed25519 key ID
   is type `0x01`'s hash over the name, a newline, `0x01` and the key.
   *Context:* the checkpoint is a C2SP signed-note (§9, decision 2), and
   `golang.org/x/mod/sumdb/note` defines only the Ed25519 type. Because the seam
   leaves the signature type and its encoding to the signer (§6), go-qrdb's
   storage, apply and service do not change with this choice; the SDK's
   verification helper and every client verifier do. The candidate forms:
   - (a) **The signed-note signature type for RFC 6962 tree-head signatures**
     (identifier per c2sp.org/signed-note, to confirm): the CA key signs an
     RFC 6962 `TreeHeadSignature` over the checkpoint's size and root with a
     timestamp the signer sets, carried as a note signature line. *Standard
     verifier support:* stock `golang.org/x/mod/sumdb/note` verifiers do not
     accept it; a verifier must implement the type. *CT tooling:* the signed
     bytes are the ones a CT log's signed tree head carries, so tooling built
     for RFC 6962 logs can check go-xkms's transparency log heads. *Custom
     verifiers:* only for the note wrapping, not for the signature itself.
   - (b) **A plain ECDSA signature over the note text,** if c2sp.org/signed-note
     registers such a type (its existence and identifier to confirm). *Standard
     verifier support:* only where a note library implements it. *CT tooling:*
     none, the signed bytes are not a tree-head structure. *Custom verifiers:*
     needed by most consumers. Simplest for the signer; if the spec registers no
     such type it would be a private type, which the format decision rules out.
   - (c) **Co-signing:** every checkpoint carries an Ed25519 signature line from
     a log key, which stock note verifiers and C2SP witnesses accept, and the CA
     key's signature line in form (a). *Standard verifier support:* full, on the
     Ed25519 line. *CT tooling:* on the CA line. *Custom verifiers:* none needed
     for the Ed25519 line. The cost is a second key per log, whose custody is
     go-xkms's to decide, two signatures per head, and the question of which key
     a consumer must trust, which is go-xkms's policy.
   - *Recommendation: (a)* for go-xkms's CA key: its transparency log is
     CT-shaped and its consumers have CT tooling, and the CA key stays the log
     owner's one key, as [Decided](#decided) states. (c) remains additive without a change in go-qrdb,
     since a signed note carries any number of signature lines. A tenant's own
     log keeps the software provider's Ed25519 type.

### Test plan

- **Unit.** In `pkg/vlog`: determinism across replicas for the same committed
  entries packed into one `Update` call versus many (the test the audit chain
  already has, over the tree); restore on open and after a failed apply; proofs
  verified by the library's own verifier at every size up to a few thousand
  entries; checkpoint refusal for a wrong root or a size past the head;
  `ExpireBodies` leaving every proof valid; name validation. In both state
  machine families: dispatch, refusal versus halt classification, and the read
  requests. In `pkg/service`: confinement, the gate refusal and the
  `OpAuditAppend` → `OpVerifiableLog` switch. In `pkg/permission`: the grant
  table above, row by row — `vlog:sign_head` refused for an Admin and the
  Security Officer and granted to an Operator, `vlog:create` and `vlog:expire`
  granted to an Admin. The per-state-machine registry: two groups from one
  create function keep separate heads across open and recovery. The seam: the software provider's
  sealed key round trip and a fault-injecting fake provider.
- **The mixed-version gate test.** A three-replica group with one replica on a
  binary that does not advertise `verifiable-log`: create and append are refused
  before proposal, audit events keep flowing as `OpAuditAppend`, and after the
  replica upgrades the gate opens and every replica's table is byte-identical.
- **Snapshot and restore.** A replica that recovers from a snapshot taken
  mid-log serves the same head, proofs and checkpoints as its peers, and its
  next append produces the same nodes; a tenant backup and restore preserves
  every log.
- **The upgrade.** A chain written by `OpAuditAppend`, then extended through the
  audit instance: `Verify` still verifies, and leaf 0 carries the pre-upgrade
  `PrevHash`.
- **Integration, per feature.** A new `INTTEST_verifiable_log` entry in
  `test/integration/features.mk` (and the feature folder the coverage gate
  requires), for `make integration-test-verifiable-log`; the existing
  `make integration-test-audit` re-run for the audit instance.
- **CLI end to end on every transport.** Each `qrdb vlog` command, table-driven
  over REST, gRPC, QUIC, MCP, Unix socket and embedded, asserting the output —
  entries, proof hashes, a checkpoint that `--verify` accepts — in standalone and
  on a three-node Raft cluster, including a tenant admin creating a log and a
  second tenant refused.

### What this backs

**The SPEC row** is `verifiable-log` in [../SPEC.md](../SPEC.md).

**The seam rows on the other side**, named here and written in those projects'
own SPECs:

- **go-xkms:** the transparency log as a CA service over a go-qrdb verifiable
  log, and the `HeadSignerProvider` implementation over its KMS and hardware
  keys and its CA key.
- **go-quicbaas:** appending its own records to the transparency log through
  go-xkms, composing go-xkms's provider.

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
  in every database (target design; rule 1).

## Decisions

Each item is a question the rules above do not answer, with its options and the
option chosen; the options not chosen are kept with the reason each lost.

**OD-1 — Raft group granularity for `backend: raft`.**
- A: one Raft group per database, for every tenant isolation mode. The tenant's
  isolation decides the host, data root and key; the database always gets its
  own group.
- B: shared groups — prefix and barrier tenants' raft databases share the system
  group under the `<tenant>/<db>/` prefix, a volume tenant's share its tenant
  group, raft tenants get one per database.
- C: a per-database option `raft.group: dedicated | shared`.
- **Chosen: A.** It is the only option under which replicas, scale,
  etcd, the drop fence, a per-database barrier and the tenant encryptor mean the
  same thing for every raft database. B keeps the prefix machinery on the data
  plane and cannot put a barrier tenant's data under the tenant's key without
  per-value envelopes in a shared log. C doubles the matrix for little gain. The
  cost of A is one group per raft database, which is what multi-group Raft is
  for.

**OD-2 — Non-replicated backends in a multi-node raft-mode cluster.** A memory,
file, pebble or objstore database exists on one node.
- A: the replicated catalog records the owning node; every other node forwards
  the database's requests to it, as writes are forwarded to a leader. If
  the owner is down the database is unavailable, which is what that backend
  choice means.
- B: refused in a multi-node cluster with a typed error.
- C: allowed, reachable only on the owning node; elsewhere a typed error names
  the owner.
- **Chosen: A**, with C's typed error as what a request gets when
  forwarding cannot reach the owner.

**OD-3 — The default backend when a create leaves it empty.**
- A: run-mode dependent — `raft` in raft mode, `pebble` in standalone mode.
- B: `pebble` always. In a cluster that makes every default database
  node-local and unreplicated.
- C: no default; `backend` is required.
- **Chosen: A.**

**OD-4 — Catalog authority.** A database's `meta.json` is node-local; only
dedicated-raft databases also have a replicated placement record.
- A: the replicated catalog (the placement table, extended to every backend) is
  authoritative for every database; `meta.json` becomes a node-local cache
  rebuilt from it.
- B: keep `meta.json` authoritative for non-raft backends and placement for raft.
- **Chosen: A.** OD-2 A requires every node to resolve every database,
  and B cannot give it that.

**OD-5 — Implicit default databases of non-system tenants.** Every tenant can
address the default database (`system`) and the SQL default database without
creating them; both are prefixes in the shared system shard.
- A: provision both as real databases at tenant creation, with the node defaults.
- B: keep them on the control-plane store under the `<tenant>/<db>/` prefix, as
  the one remaining shared binding.
- C: remove them; a non-system tenant's request without a database is refused.
- **Chosen: A.** B keeps a second resolution path alive for two names;
  C breaks every client that relies on the default.

**OD-6 — Encryption when a database has its own barrier.**
- A: nested — the store is encrypted at rest under the tenant's key (or the
  node's, for prefix tenants), and values are additionally encrypted under the
  database's barrier. Sealing either one fails the database closed.
- B: the database's barrier replaces the tenant's for that database's store.
- **Chosen: A.** B would let a database owner take a tenant's data out
  from under the tenant's custody.

**OD-7 — Key-name confidentiality on `file` and `objstore`.** Values are
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
  backends. Not viable under rule 1.
- **Chosen: B.**

**OD-8 — State machine of a database's Raft group.**
- A: one canonical database state machine — the MVCC engine over
  `storage.Storage` — for every database group, with its state storage chosen
  per database (`raft.state_storage: pebble | file | memory`, defaulted from
  the node). The node's `sm_type` applies to the control-plane group only.
- B: a per-database `sm_type`, which requires every state-machine family to
  implement every engine, vector and time series included.
- **Chosen: A.**

**OD-9 — Remote-mode databases.** `database.ModeRemote` is accepted, validated and
persisted, and never constructed; the data goes to a local store.
- A: remove remote mode, the `connection` block and the update-connection verb
  on all six transports.
- B: implement a full multi-model proxy over the SDK client, every engine.
- C: keep a KV-only proxy and refuse other engines — contradicts rule 1.
- **Chosen: A.** A database that proxies to another node is a client
  configuration, which the SDK already provides.

**OD-10 — Relocating existing shared-shard data.** Databases whose data is in
the shared system shard (or a volume tenant's shared shard) move to their own
bindings.
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
- **Chosen: A.**

**OD-11 — Migrating the node-local vector stores.** In a multi-node cluster each node's vector store
holds only the writes that node served, so the stores disagree.
- A: import every node's store; last writer wins by vector version and update
  time; deletes are not propagated.
- B: import from one designated node only.
- C: an operator import command, run per node, with
  `--on-conflict fail | newest | skip` defaulting to `fail`, reporting every
  conflict.
- **Chosen: none of them.** No release ships migration code for an earlier
  layout: the node-local vector stores are deleted outright, and an operator who
  needs the data re-ingests it through the vector API, as from any other data
  source. No import command is built.

**OD-12 — SQL virtual tables for the engines.** The time-series engine's
tables are `datapoints` and `metrics`, the vector engine's `namespaces` and
`vectors` (`pkg/sql/provider`).
- A: every database exposes every compiled-in engine table under those names,
  and the names become reserved (`CREATE TABLE metrics` is refused).
- B: every database exposes them under reserved, prefixed names —
  `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`,
  `qrdb_vector_vectors` — so user tables named `metrics` or `vectors` keep
  working.
- C: stop exposing engine tables through SQL.
- **Chosen: B.**

**OD-13 — How callers learn `engine` is gone.** The CLI flag fails as an unknown
flag and the protobuf field number is reserved either way.
- A: strict decoding on database-create requests (REST, QUIC, MCP) and on the
  `databases:` configuration section, so `engine` is refused as an unknown field
  — a generic rule, not an engine-specific shim.
- B: ignore it silently, the JSON and YAML default.
- **Chosen: A.** A script creating an `engine: timeseries` database
  should learn that the field means nothing.

**OD-14 — The vector `shard_id` request field.** The binding decides the shard.
- A: remove it from all twenty vector requests on all six transports.
- B: keep it and refuse any non-zero value with a typed error.
- **Chosen: A** — a breaking change, carried by a MINOR bump.

**OD-15 — Choosing a backend from SQL `CREATE DATABASE`.**
- A: node defaults only; non-default backends are created through the API or CLI.
- B: an executable-comment extension on MySQL
  (`CREATE DATABASE x /*!qrdb backend=raft replicas=3 */`) and a `WITH` option
  on PostgreSQL.
- **Chosen: A.**

**OD-16 — Time-series retention enforcement.**
- A: include it in this program — a leader-driven retention operation, proposed
  through the binding and applied deterministically in the apply batch.
- B: a separate program; this program only corrects the documentation.
- **Chosen: A.** Its deletion half is in the code: a `pkg/server` scheduler
  proposes a feature-gated `protocol.OpTSApplyRetention` command per metric per
  tick; see
  [../time-series/architecture.md#retention-enforcement-od-16-a](../time-series/architecture.md#retention-enforcement-od-16-a).
  Downsampling, and applying it through the database's binding, wait on the
  storage binding.
