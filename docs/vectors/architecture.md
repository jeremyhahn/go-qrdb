# Vector Database — Architecture

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> "Target design" sections are the vector half of the multi-model database
> program in [../databases/architecture.md](../databases/architecture.md). They
> are approved by the owner on 2026-09-30 and not implemented. OD-n refers to that document's ruled decisions.

> **Build-tag gated.** Available only in builds carrying `qrdb_vector`
> (`WITH_QRDB_VECTOR=1`). GPU acceleration adds the `gpu` tag.
> `qrdb admin capabilities` reports `vector` when it is compiled in.

## Problem

Embedding-based retrieval needs approximate nearest-neighbour search over
high-dimensional vectors, with metadata filtering, at the same durability and
tenancy guarantees as the rest of the store. Bolting on a separate vector
database means a second system to seal, back up, authorize and keep consistent
with the primary data.

## Shape chosen

Vectors live in the same store, organised into **namespaces**. A namespace fixes
its dimensionality, distance metric and index type at creation:

| Property | Values | Default |
|---|---|---|
| dimension | uint32 | — (required) |
| distance metric | `cosine`, `euclidean`, `dot-product` | `cosine` |
| index type | `hnsw`, `flat`, `ivf-flat` | `hnsw` |

Four search modes, deliberately distinct rather than one overloaded call:

- **`SearchSimilar`** — k-NN from a supplied query vector.
- **`SearchByID`** — k-NN from an existing vector's ID, so the caller does not
  round-trip the vector out and back.
- **`SearchByMetadata`** — metadata predicates only, no vector maths.
- **`HybridSearch`** — vector similarity **and** metadata filters together.

Index maintenance is explicit and asynchronous: `RebuildIndex` and
`CompactNamespace` return immediately and expose progress endpoints
(`GetRebuildProgress`, `GetCompactionProgress`).

### Alternatives rejected

- **A separate vector store alongside qrdb.** Rejected: it would need its own
  barrier, its own tenancy, its own backup path and its own authorization
  vocabulary. Vector data is exactly as sensitive as the documents it was
  derived from.
- **One `search` call with optional filters.** Rejected: the four modes have
  genuinely different cost profiles and different permission grants
  (`vector:search_similar`, `vector:search_by_id`,
  `vector:search_by_metadata`, `vector:hybrid_search`). Collapsing them would
  make "may run metadata queries" and "may run k-NN" the same grant.
- **Synchronous index rebuild.** Rejected: a rebuild on a large namespace is a
  long operation; blocking a request on it turns an ordinary client timeout into
  an abandoned rebuild with no handle to check on.
- **Mutable namespace geometry.** Rejected: changing dimension, metric or index
  type invalidates the existing index. Namespaces are created with their
  geometry and it is fixed.

## Current state (measured)

Earlier revisions of this folder described per-shard namespaces and
barrier-encrypted, tenant-safe storage. The code does this instead:

- **One node-local store for everything.** The vector service is built once per
  node over a Pebble store at `{DataDir}/vector` (`pkg/server/lifecycle`),
  shared by every tenant and every database on the node.
- **Isolation is a name prefix.** A database's namespaces are distinguished by
  the `<tenant>/<db>/` prefix the request resolver adds to the namespace name,
  plus a tenant scope the service adds itself.
- **The system key, not the tenant's.** Values are encrypted under the node's
  at-rest encryptor. A tenant's barrier does not cover its vectors, and sealing
  it does not fail its vector operations closed.
- **Not replicated.** The service replicates only when given a Raft node, and no
  production path gives it one. The reserved vector shard and the two vector
  state machines in `pkg/statemachine` are never started. In a cluster, each
  node's store holds only the writes that node served. The `shard_id` request
  field selects nothing.
- **Absent where tenants have their own store.** The per-tenant volume host, the
  dedicated-raft database bundles and the dedicated-store bundle carry no vector
  service; vector requests for those databases are refused as not implemented.
- **ANN indexes are empty after a restart.** At startup the service creates an
  empty HNSW index for every stored namespace, with default parameters, and
  registers it. The lazy path that rebuilds an index from stored vectors only
  runs when no index is registered, so it never runs for a namespace that
  existed at startup: similarity search on it returns nothing until
  `RebuildIndex` is called. The namespace's own metric is also lost — the
  startup index is always L2. (Read in `vectorService.loadNamespaces` and
  `ensureHNSWIndex`; not executed.)

## Target design — vectors as an engine of the database binding

Vector is an engine of the database, like KV and time series. It has no store,
no Raft group and no encryptor of its own: it uses the database's
[storage binding](../databases/architecture.md#the-storage-binding).

```
database binding (memory | file | pebble | objstore | raft)
  └── engine apply batch
        ├── vector namespace records      ─┐
        ├── vector records                 ├─ source of truth, in the store
        ├── metadata equality index        │
        └── bounded mutation log          ─┘
  └── post-commit, in log order
        └── ANN index per namespace (HNSW | flat | IVF)  ─ derived, in memory
              └── checkpoints under the binding's data root
```

### What this gives, by construction

- **Every backend.** The records are ordinary keys in the database's store,
  read and written through the storage interface, so memory, file, pebble,
  objstore and raft all work.
- **Replication when the backend is `raft`.** Every vector write is a command
  proposed to the database's Raft group, so every replica applies the same
  writes in the same order. There is no separate vector shard.
- **Atomic apply.** A write's record, its metadata index entries, its namespace
  counters and its mutation-log entry are written into the apply batch together
  and commit with the entry, or not at all.
- **The drop fence.** Vector writes are guarded commands like every other write
  to the database, so a vector write that lands after the database's drop fence
  is refused on every replica.
- **Tenant isolation.** The records, the index checkpoints and every derived
  file are under the binding's data root and encryptor — the tenant's barrier,
  not the node's. A sealed tenant or database barrier fails vector operations
  closed. The namespace name needs no tenant or database prefix, because the
  store belongs to one database.

### The index is derived state

The records in the store are the truth. The ANN index is a cache of them that
makes search fast. That one decision settles the hard questions:

- **A crash between the batch commit and the index update loses nothing.** At
  open the index is brought up to date from the records.
- **Index structure does not have to be identical across replicas for results
  to be correct.** It is made identical where it can be (below), because
  identical indexes give identical answers on every replica, but correctness
  never depends on it.
- **Index checkpoints are an optimisation.** A missing, stale or unreadable
  checkpoint means a rebuild, never an error.

### Persistence and restart

Each namespace keeps a replicated **sequence number**, incremented by every
mutation in apply, and a **bounded mutation log** of the last N mutations
(namespace ID and operation), trimmed deterministically in apply.

- **Checkpoint.** The node serializes a namespace's index — tagged with the
  sequence number it reflects and the index format version — into the
  binding's data root, encrypted under the binding's encryptor. Checkpoints are
  written on clean close, before an SM snapshot is taken, and periodically off
  the apply path. A `memory` binding writes none.
- **Open.** For each namespace: if a checkpoint exists with the current format
  and its sequence is within the mutation log's window, load it and replay the
  log from that sequence; otherwise rebuild from the records. Search on a
  namespace that is still rebuilding is refused with a retryable unavailable
  error rather than answered from a partial index.
- **Snapshot and restore (raft).** A state-machine snapshot carries the store
  (records, namespace records and mutation logs) and, as an optional trailing
  section, the checkpoint of each namespace's index. A replica installing the
  snapshot accepts a checkpoint whose sequence matches the restored namespace
  record and rebuilds otherwise. The same holds for a backup restored into a
  local binding.

### Determinism

- **IDs** a caller omits are allocated in apply from the namespace record, so
  every replica assigns the same ID.
- **HNSW level assignment** is a function of the namespace and the vector ID,
  not of a random source, and inserts happen in log order on the single apply
  goroutine, so replicas on the same architecture build the same graph.
- **IVF training** seeds its k-means from the namespace and its training
  generation, not from the clock.
- Replicas on different CPU architectures may still differ in floating-point
  rounding of distances; the index remains a valid index over the same records,
  which is all correctness needs.

### Consistency

Reads resolve through the binding's host with the request's consistency level,
like every other read. A linearizable search waits until the serving replica has
applied the read index, and the index is updated in apply order, so a
linearizable search sees every acknowledged write. A follower or stale search
may lag.

### What is deleted

The node-local vector store and its wiring in `pkg/server/lifecycle`, the
reserved vector shard constant, the two unused vector state machines in
`pkg/statemachine`, the service's optional Raft-node path, and the service's
own tenant scoping of namespace names (the store is already one tenant's
database). Each capability keeps exactly one home: the engine.

### Alternatives rejected

- **Replicating the vector store on its own Raft group per node or per tenant.**
  Rejected: a document and its embedding could not commit atomically, and the
  database's drop, backup, barrier and replication settings would not apply to
  its vectors.
- **Persisting the ANN graph inside the replicated store, updated in the apply
  batch.** Rejected: one HNSW insert rewrites the adjacency lists of up to `M`
  neighbours on every layer, multiplying write amplification on every replica
  for data that can be recomputed from the records.
- **Rebuilding every index from the records on every open.** Rejected as the
  only mechanism: correct, but a large namespace would be unsearchable for
  minutes after every restart. It remains the fallback.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Namespaces, vector CRUD, search, index maintenance | `pkg/vector`, `pkg/service` | go-qrdb |
| Vector apply handlers and reads (target) | `pkg/engine` | go-qrdb |
| GPU acceleration (`gpu` tag), remote index service | `pkg/vector/gpu`, `pkg/vector/gpu/remote` | go-qrdb |
| Capability registration (`vector`) | `pkg/capabilities` | go-qrdb |
| Embedding generation | `pkg/embedding` — see [../embeddings/](../embeddings/) | go-qrdb |
| RAG pipeline built on top | `pkg/rag` — see [../rag/](../rag/) | go-qrdb |
| Underlying storage, the binding and the barrier | `pkg/storage`, `pkg/database`, `pkg/seal` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Vector()`) | go-qrdb |

## Sharding

Shipped: vector operations take an optional **shard ID** (`--shard-id`,
`shard_id`). It selects nothing — see "Current state".

Target: a namespace belongs to a database and lives wherever that database's
binding lives. Horizontal scale comes from databases on different Raft groups,
not from a vector-specific shard. The `shard_id` field is removed (OD-14).

## Trust boundaries

- **Target: vector storage is tenant-safe and barrier-encrypted.** Namespaces
  are part of one database's store, and vector payloads pass through that
  database's binding encryptor — see [../barrier-seal/](../barrier-seal/). A
  sealed barrier fails vector reads and writes closed. Shipped, this holds only
  for the node's own barrier (see "Current state").
- Each of the twenty vector capabilities carries its own `vector:*` permission,
  including the read-only ones (`vector:get_index_stats`,
  `vector:namespace_list`), so index topology is not readable by anyone with
  search access.
- Metadata filters are caller-supplied JSON; they are parsed, not interpolated.

## What this feature deliberately does not do

- **No embedding generation.** Turning text into vectors is
  [../embeddings/](../embeddings/); this feature stores and searches vectors that
  already exist.
- **No chunking or retrieval orchestration.** That is [../rag/](../rag/).
- **No namespace geometry migration.** Changing dimension, metric or index type
  means creating a new namespace and re-upserting.
- **No cross-namespace search.** A search targets exactly one namespace.
- **No cross-database search.** A namespace belongs to one database (target).
