# Databases — Architecture

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> This document has two layers, and each section says which layer it is in.
>
> - **Current state** sections describe the code as shipped and measured. Where
>   the previous revision of this document claimed more than the code does, the
>   claim is corrected here (see "Corrections to earlier revisions").
> - **Target design** sections are the requirements contract for the
>   multi-model database program: every database supports every engine on the
>   storage backend it chose, for every tenant isolation mode. They are pending
>   owner review. Nothing in them is implemented yet. The open questions the
>   owner has not ruled on are listed in "Open decisions for the owner" at the
>   end, each with options and a recommendation; implementation lanes do not
>   start on a question listed there.

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
  tenant's (see open decision OD-6). Every database now has a store of its
  own, so the old refusal of a barrier on a shared store no longer has a shared
  store to refuse; what remains is the refusal on `memory`, below.
- **A sealed barrier — the tenant's or the database's — fails every engine of
  the database closed**, vector and time series included.

Where the table depends on an open decision it says so in that decision:
the group granularity of `backend: raft` (OD-1) is what makes the right-hand
column per-database for every isolation mode.

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
| `file` or `objstore` for a volume or raft tenant | `database.BackendIsolationConflictError` — **only if OD-7 is ruled B** | those backends expose key names outside the tenant's encryption |
| a non-replicated backend in a multi-node cluster | `database.LocalBackendInClusterError` — **only if OD-2 is ruled B** | see OD-2 |
| any backend while the tenant's or the database's barrier is sealed | `service.BarrierSealedError` at request time | fail closed |

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
   ones, because both apply through the same engine.
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

Every data-plane request resolves `(tenant, database)` server side and looks
the pair up in `service.Registry`, which returns the servicer bound to that
database. This is the single point where a database becomes a concrete set of
services, and it is why a database's configuration cannot leak into another's
request path. In the target design it is also the single point where the
binding is chosen, and every servicer it returns is built over that binding.

### What a drop reclaims (current state)

A database in a shared store is a keyspace, not a directory, and its teardown
has to reclaim the keyspace. `DROP DATABASE` therefore reclaims, in this order:

1. **The data the database owns in the store it shares with other databases**
   — done first, while the database is still resolvable.
2. Its barrier, if it had one.
3. Its configuration directory, which for a database with a dedicated store
   also holds that store's files.
4. Its catalog entry.

**Dependents first, the record that makes the database resolvable last.** The
reverse order is what makes a residue permanent rather than merely present:
once the catalog record is gone, nothing in the system names the keyspace
again, so no later drop and no operator command can reach it. With this
order, the worst an interrupted drop leaves is a **live, empty database**,
and re-issuing the drop finishes it — the reclamation is idempotent, and an
already-empty keyspace is a success.

A reclamation that fails **fails the whole drop**. The database, its catalog
record and its data are left exactly as they were, because reporting a
successful drop over data that is still in the store is worse than refusing.

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
- **Target design: a tenant's data is encrypted under the tenant's key on every
  engine.** The node-local vector store under the system encryptor is the case
  this closes.

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

## Open decisions for the owner

Each item is a question the rulings above do not answer. The recommendation is
this design's; the owner decides. An implementation lane that depends on an
item does not start until it is ruled.

**OD-1 — Raft group granularity for `backend: raft`.**
- A: one Raft group per database, for every tenant isolation mode. The tenant's
  isolation decides the host, data root and key; the database always gets its
  own group.
- B: shared groups — prefix and barrier tenants' raft databases share the system
  group under the `<tenant>/<db>/` prefix, a volume tenant's share its tenant
  group, raft tenants get one per database.
- C: a per-database option `raft.group: dedicated | shared`.
- **Recommendation: A.** It is the only option under which replicas, scale,
  etcd, the drop fence, a per-database barrier and the tenant encryptor mean the
  same thing for every raft database. B keeps the prefix machinery on the data
  plane and cannot put a barrier tenant's data under the tenant's key without
  per-value envelopes in a shared log. C doubles the matrix for little gain. The
  cost of A is one group per raft database, which is what multi-group Raft is
  for.

**OD-2 — Non-replicated backends in a multi-node raft-mode cluster.** A memory,
file, pebble or objstore database exists on one node.
- A: the replicated catalog records the owning node; every other node forwards
  the database's requests to it, as writes are forwarded to a leader today. If
  the owner is down the database is unavailable, which is what that backend
  choice means.
- B: refused in a multi-node cluster with `database.LocalBackendInClusterError`.
- C: allowed, reachable only on the owning node; elsewhere a typed error names
  the owner.
- **Recommendation: A**, with C's typed error as what a request gets when
  forwarding cannot reach the owner.

**OD-3 — The default backend when a create leaves it empty.**
- A: run-mode dependent — `raft` in raft mode, `pebble` in standalone mode —
  overridable by `database_defaults.backend`.
- B: `pebble` always, today's literal default. In a cluster that would make
  every default database node-local and unreplicated, where today its data is in
  the replicated system shard.
- C: no default; `backend` is required.
- **Recommendation: A.**

**OD-4 — Catalog authority.** Today a database's `meta.json` is node-local; only
dedicated-raft databases also have a replicated placement record.
- A: the replicated catalog (the placement table, extended to every backend) is
  authoritative for every database; `meta.json` becomes a node-local cache
  rebuilt from it.
- B: keep `meta.json` authoritative for non-raft backends and placement for raft.
- **Recommendation: A.** OD-2 A requires every node to resolve every database,
  and B cannot give it that.

**OD-5 — Implicit default databases of non-system tenants.** Every tenant can
address the default database (`system`) and the SQL default database without
creating them; today both are prefixes in the shared system shard.
- A: provision both as real databases at tenant creation, with the node defaults.
- B: keep them on the control-plane store under the `<tenant>/<db>/` prefix, as
  the one remaining shared binding.
- C: remove them; a non-system tenant's request without a database is refused.
- **Recommendation: A.** B keeps a second resolution path alive for two names;
  C breaks every client that relies on the default today.

**OD-6 — Encryption when a database has its own barrier.**
- A: nested — the store is encrypted at rest under the tenant's key (or the
  node's, for prefix tenants), and values are additionally encrypted under the
  database's barrier. Sealing either one fails the database closed.
- B: the database's barrier replaces the tenant's for that database's store.
- **Recommendation: A.** B would let a database owner take a tenant's data out
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
  backends. Not viable under ruling 1.
- **Recommendation: B.**

**OD-8 — State machine of a database's Raft group.**
- A: one canonical database state machine — the MVCC engine over
  `storage.Storage` — for every database group, with its state storage chosen
  per database (`raft.state_storage: pebble | file | memory`, defaulted from
  the node). The node's `sm_type` applies to the control-plane group only.
- B: a per-database `sm_type`, which requires every state-machine family to
  implement every engine, vector and time series included.
- **Recommendation: A.** The in-flight work giving every state-machine type an
  MVCC store remains useful for the control-plane group and is not a
  prerequisite here.

**OD-9 — Remote-mode databases.** Measured: accepted, validated, persisted, and
never constructed; the data goes to a local store.
- A: remove remote mode, the `connection` block and the update-connection verb
  on all six transports.
- B: implement a full multi-model proxy over the SDK client, every engine.
- C: keep a KV-only proxy and refuse other engines — contradicts ruling 1.
- **Recommendation: A.** A database that proxies to another node is a client
  configuration, which the SDK already provides.

**OD-10 — Relocating existing shared-shard data.** Databases whose data is in
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
- **Recommendation: A.**

**OD-11 — Migrating the node-local vector stores.** In a multi-node cluster
each node's vector store holds only the writes that node served, so the stores
disagree.
- A: import every node's store; last writer wins by vector version and update
  time; deletes are not propagated.
- B: import from one designated node only.
- C: an operator import command, run per node, with
  `--on-conflict fail | newest | skip` defaulting to `fail`, reporting every
  conflict.
- **Recommendation: C.** A single-node deployment, the common case, has no
  conflicts and imports cleanly; a cluster's operator sees every disagreement
  instead of having one resolved silently.

**OD-12 — SQL virtual tables for the engines.** Today only a database created
with engine `timeseries` or `vector` exposes `datapoints`/`metrics` or
`namespaces`/`vectors` as SQL tables.
- A: every database exposes every compiled-in engine table under those names,
  and the names become reserved (`CREATE TABLE metrics` is refused).
- B: every database exposes them under reserved, prefixed names —
  `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`,
  `qrdb_vector_vectors` — so user tables named `metrics` or `vectors` keep
  working.
- C: stop exposing engine tables through SQL.
- **Recommendation: B.**

**OD-13 — How callers learn `engine` is gone.** The CLI flag fails as an unknown
flag and the protobuf field number is reserved either way.
- A: strict decoding on database-create requests (REST, QUIC, MCP) and on the
  `databases:` configuration section, so `engine` is refused as an unknown field
  — a generic rule, not an engine-specific shim.
- B: ignore it silently, the JSON and YAML default.
- **Recommendation: A.** A script creating an `engine: timeseries` database
  should learn that the field no longer means anything.

**OD-14 — The vector `shard_id` request field.** The binding decides the shard.
- A: remove it from all twenty vector requests on all six transports.
- B: keep it and refuse any non-zero value with a typed error.
- **Recommendation: A** — a breaking change, carried by a MINOR bump.

**OD-15 — Choosing a backend from SQL `CREATE DATABASE`.**
- A: node defaults only; non-default backends are created through the API or CLI.
- B: an executable-comment extension on MySQL
  (`CREATE DATABASE x /*!qrdb backend=raft replicas=3 */`) and a `WITH` option
  on PostgreSQL.
- **Recommendation: A** for this program.

**OD-16 — Time-series retention enforcement.** Measured: retention policies are
stored and returned, and nothing ever expires or downsamples a point.
- A: include it in this program — a leader-driven retention operation, proposed
  through the binding and applied deterministically in the apply batch.
- B: a separate program; this program only corrects the documentation.
- **Recommendation: A**, as its own lane after the time-series phase. The
  documentation is corrected now either way.
