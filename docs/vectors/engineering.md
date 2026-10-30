# Vector Database — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-29
> Conflicts with this document are defects. Fix them here first.
>
> Sections headed **Target** are the vector half of the multi-model database
> program — see [architecture.md](architecture.md) and
> [../databases/engineering.md](../databases/engineering.md). They are approved and not implemented. OD-n refers to the ruled decisions in
> [../databases/architecture.md](../databases/architecture.md).

> **Build-tag gated: `qrdb_vector`** (`WITH_QRDB_VECTOR=1`, the default;
> `WITH_QRDB_VECTOR=0` removes it). GPU paths add
> `gpu`. Without the tag the CLI verb group and the transport handlers are
> compiled out and `pkg/capabilities` does not register `vector`.

## Exported surface

`sdk/go`:

```go
func (c *Client) Vector() transport.VectorClient
```

`sdk/go/transport`:

```go
type VectorClient interface {
	// Namespaces
	CreateNamespace(ctx context.Context, req *CreateNamespaceRequest) (*CreateNamespaceResponse, error)
	DeleteNamespace(ctx context.Context, req *DeleteNamespaceRequest) (*DeleteNamespaceResponse, error)
	ListNamespaces(ctx context.Context, req *ListNamespacesRequest) (*ListNamespacesResponse, error)
	GetNamespaceInfo(ctx context.Context, req *GetNamespaceInfoRequest) (*GetNamespaceInfoResponse, error)
	UpdateNamespace(ctx context.Context, req *UpdateNamespaceRequest) (*UpdateNamespaceResponse, error)

	// Vector CRUD
	UpsertVector(ctx context.Context, req *UpsertVectorRequest) (*UpsertVectorResponse, error)
	GetVector(ctx context.Context, req *GetVectorRequest) (*GetVectorResponse, error)
	DeleteVector(ctx context.Context, req *DeleteVectorRequest) (*DeleteVectorResponse, error)
	BatchUpsert(ctx context.Context, req *BatchUpsertRequest) (*BatchUpsertResponse, error)
	BatchDeleteVectors(ctx context.Context, req *BatchDeleteVectorsRequest) (*BatchDeleteVectorsResponse, error)
	ListVectors(ctx context.Context, req *ListVectorsRequest) (*ListVectorsResponse, error)
	UpdateMetadata(ctx context.Context, req *UpdateVectorMetadataRequest) (*UpdateVectorMetadataResponse, error)

	// Search
	SearchSimilar(ctx context.Context, req *SearchSimilarRequest) (*SearchSimilarResponse, error)
	SearchByMetadata(ctx context.Context, req *SearchByMetadataRequest) (*SearchByMetadataResponse, error)
	HybridSearch(ctx context.Context, req *HybridSearchRequest) (*HybridSearchResponse, error)
	SearchByID(ctx context.Context, req *SearchByIDRequest) (*SearchByIDResponse, error)

	// Index maintenance
	RebuildIndex(ctx context.Context, req *RebuildIndexRequest) (*RebuildIndexResponse, error)
	GetRebuildProgress(ctx context.Context, req *GetRebuildProgressRequest) (*GetRebuildProgressResponse, error)
	GetIndexStats(ctx context.Context, req *GetIndexStatsRequest) (*GetIndexStatsResponse, error)
	CompactNamespace(ctx context.Context, req *CompactNamespaceRequest) (*CompactNamespaceResponse, error)
	GetCompactionProgress(ctx context.Context, req *GetCompactionProgressRequest) (*GetCompactionProgressResponse, error)
}
```

Note the naming: the SDK methods drop the `Vector` prefix that the gRPC RPCs
carry (`SearchSimilar` here, `VectorSearchSimilar` on the wire). The mapping is
one-to-one and is tabulated in [api.md](api.md).

## Namespace geometry

Fixed at creation, immutable thereafter:

| Field | Values | Default |
|---|---|---|
| `dimension` | uint32 | required |
| `distance_metric` | `cosine`, `euclidean` (alias `l2`), `dot_product` | `euclidean` |
| `index_type` | `hnsw`, `flat`, `ivf_flat` | `hnsw` |

These are the wire values (`transport.DistanceMetric`,
`transport.VectorIndexType`). The CLI spells them `dot-product` and `ivf-flat`
and defaults `--distance-metric` to `cosine`.

## Namespace update

`UpdateNamespace` changes a namespace's index parameters (`index_config`) and
metadata in place and keeps every vector. It never changes the geometry.

- **Merge, not replace.** Each key of the request's `index_config` and
  `metadata` is written over the stored value; a key the request omits keeps
  its value. No key is removed. `storage/vector.MergeNamespaceUpdate` computes
  the merged record, and the service and the Raft apply both call it, so a
  direct write and a replicated one produce the same record.
- **Dimension is a precondition, not a field.** `dimension` zero means "not
  given". Any other value must equal the namespace's dimension, or the update
  is refused with `DimensionMismatchError` and nothing is written. The distance
  metric and index type are not in the request at all.
- **A missing namespace is refused** with `NamespaceNotFoundError`. The update
  never creates a namespace.
- **The rebuild flag.** `requires_rebuild` is true when at least one request
  key of `index_config` is absent from the stored parameters or has a
  different value there. A metadata-only update, or one that gives every
  parameter its stored value, reports false.
- **The update does not rebuild the index.** It rewrites the namespace record
  (and its cached copy) and leaves the in-memory ANN index as built: searches
  keep running on it. The caller that gets `requires_rebuild` runs
  `RebuildIndex`, which rebuilds from the stored vectors under the record's
  current parameters.
- **Replication.** On a node with a Raft node the service proposes
  `OpUpdateNamespace` to the vector consensus FSM, which re-reads the record,
  checks the namespace and the dimension on every replica alike, and applies
  the same merge. The service checks both before it proposes, so a refused
  update is normally never in the log; one that reaches apply anyway is
  refused there on its merits, identically on every replica, and does not
  halt the apply loop. Without Raft the service writes the merged record to
  storage itself.

Typed refusals travel as structured refusals with the reasons
`VECTOR_NAMESPACE_NOT_FOUND` and `VECTOR_DIMENSION_MISMATCH`: REST, gRPC, the
Unix socket, QUIC and MCP each rebuild the same `transport.NamespaceNotFoundError`
or `transport.DimensionMismatchError`, so a caller tests the type, not the
text.

## Invariants

- **Vector IDs are `uint64`.** The CLI takes them as strings to avoid shell and
  JSON precision loss, exactly as document IDs do in
  [../collections/](../collections/engineering.md).
- **Namespace geometry is immutable.** Changing dimension, metric or index type
  requires a new namespace and a re-upsert; there is no in-place migration.
  Index parameters and namespace metadata are not geometry: `UpdateNamespace`
  changes them in place (see "Namespace update").
- **Upsert is idempotent by ID** — inserting an existing ID replaces the vector
  and its metadata.
- **A vector's length must equal the namespace dimension.** A mismatch is
  refused, not padded or truncated.
- **Rebuild and compaction are asynchronous.** Both return a handle (job ID /
  operation ID) immediately; progress is polled through the corresponding
  progress call. A caller that does not poll has started an operation it cannot
  observe.
- **An asynchronous job never outlives the node.** An async rebuild or
  compaction runs on the vector service's own background context, not the
  request's. Closing the node closes the service before the vector Pebble DB:
  the service cancels every job in flight and returns only once each has
  stopped, so none reaches the closed DB, and it refuses every request made
  after that, a rebuild, a compaction or any read or write, with
  `VectorServiceClosedError` before it touches storage. Every Close call waits
  for the jobs to stop, a second one concurrent with the first included. A job
  Close cancelled records the cancellation as its error.
- **A namespace scan is ordered by vector ID.** `ScanNamespace` returns
  ascending IDs at or above `ScanOptions.FromID`, at most `Limit` of them, each
  at its latest version. On Pebble it seeks to the first key of `FromID`, keeps
  the last version of each ID as the iterator leaves it (keys sort by ID, then
  version, both big-endian) and stops at `Limit`. The memory store sorts by ID
  and applies the same bounds. `Offset`, still honoured for the replicated list
  command, counts in that same order after `FromID`.
- **`ListVectors` pages by an opaque keyset cursor.** `NextCursor` resumes after
  the last ID returned: today it is the decimal of that ID plus one, set only
  when a `limit+1` probe found another vector, and empty when none follows or
  the last ID is `MaxUint64`. A cursor that does not parse as one is refused
  with `InvalidVectorError`, never read as the first page. The index rebuild
  walks the namespace the same way, 1,000 IDs a page, so no vector repeats or
  is skipped when the namespace is larger than a page.
- **Search targets exactly one namespace.** There is no cross-namespace search.
- **`ef_search` applies to HNSW only** and trades recall against latency; it is
  ignored by `flat` and has no meaning for it.
- **Shard ID selects nothing** (measured). Namespaces live in one node-local
  store; see [architecture.md](architecture.md) "Current state". Removed in the
  target design (OD-14).
- **Vector payloads are encrypted at rest under the node's encryptor** and
  partitioned by a `<tenant>/<db>/` name prefix. A sealed node barrier fails
  vector operations closed; a sealed tenant or database barrier does not
  (measured). The target design puts them under the database binding's
  encryptor.

## Search modes

| Method | Input | Cost shape |
|---|---|---|
| `SearchSimilar` | query vector + `k` | index traversal |
| `SearchByID` | existing vector ID + `k` | one fetch, then index traversal — avoids shipping the vector twice |
| `SearchByMetadata` | filter JSON + `limit`/`offset` | metadata scan, no vector maths |
| `HybridSearch` | query vector + filter JSON + `k` | traversal with filtering |

`min_score` is a post-filter on similarity; it reduces results, it does not make
the search cheaper.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_vector` | capability absent; verbs and handlers not registered | **yes** |
| Namespace already exists | `service.NamespaceExistsError` | yes |
| Unknown namespace | not-found | **yes** — never auto-created |
| Vector length ≠ namespace dimension | invalid-argument | **yes** |
| Namespace update of an unknown namespace | `NamespaceNotFoundError` (not-found) | **yes** — never auto-created |
| Namespace update with a nonzero dimension ≠ the namespace's | `DimensionMismatchError` (invalid-argument); nothing written | **yes** |
| Unknown distance metric or index type at the CLI or MCP | invalid-argument | **yes** |
| Unknown vector ID on get/delete/search-by-id | not-found | yes |
| Malformed metadata filter JSON | invalid-argument | **yes** |
| Progress polled for an unknown job/operation ID | not-found | yes |
| Missing `vector:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |
| Target: namespace index still loading after open | `service.VectorIndexWarmingError`, retryable | **yes** — never a partial answer |
| Target: write after the database's drop fence | the guard refusal | **yes** |

Dimension mismatch fails rather than coercing: a silently padded vector produces
plausible-looking neighbours that are wrong, which is far worse than an error.

## Configuration

Namespace geometry is per namespace, not global. Build flags:
`WITH_QRDB_VECTOR=1` compiles the feature in; `WITH_QRDB_GPU=1` adds the `gpu`
tag and the GPU index paths (`pkg/vector/gpu`, with a remote index service
defined by `pkg/vector/gpu/remote/proto/vector_index.proto`).

### IVF clustering and `Config.Seed`

- **IVF clustering is randomly initialised.** k-means chooses its initial
  centroids at random, for both `kmeans++` and `random` initialisation, and
  `ivf.Config.Seed` zero, the default, seeds that choice from the clock.
  Target: a namespace in a database binding never uses the clock seed; the
  engine derives it from the namespace and its training generation (see
  "Target: determinism" below).
- **A nonzero `Config.Seed` makes clustering reproducible**: the same seed and
  the same vectors give the same centroids.
- **A test that asserts on the clustering must set a seed**: where the
  centroids land, which vectors share a cluster, which vector a search returns
  first. An assertion on an invariant that holds for every initialisation
  needs none.

The k-means internals behind this (initialisation cost, overflow clamping,
empty-cluster repair, dimension validation) are documented in the package
docs of `pkg/vector/ivf` and `pkg/vector/kmeans`.

## Persistence

Shipped: vectors, their metadata and namespace records are stored in the
node-local Pebble vector store, under the node's encryptor. ANN indexes are
in memory only, and are rebuilt from the stored vectors -- under the
namespace's own configured distance metric -- when the service opens: at
process restart, and over whatever the store already holds when a new
replica's vector directory is first opened (there is no Raft snapshot of
this node-local store; see architecture.md "Current state"). Compaction
reclaims space from deleted vectors; `RebuildIndex` reconstructs the ANN
index from the stored vectors on demand, the same way the automatic
construction-time rebuild does.

`CreateNamespace`, `loadNamespaces` and the lazy rebuild all derive the
index's metric from one function (`distanceMetricFromString`), and
`loadNamespaces` builds each namespace's index through the same rebuild
`ensureHNSWIndex` uses, so a restarted node ranks every namespace under its
configured metric. `TestVectorService_RestartPreservesConfiguredMetric` and
`TestVectorService_SnapshotInstallPreservesConfiguredMetric`
(`pkg/service/vector_restart_test.go`) pin both call paths against a real
PebbleDB-backed store, with a query vector chosen so that L2 and cosine
ranking disagree.

**Close and in-flight operations.** `PebbleVectorStorage` admits every
operation through the close gate `pkg/storage/pebble`'s store uses: an
operation holds a shared lock from its closed check to its last DB call, and
`Close` takes it exclusively. Close therefore waits for every in-flight read
and write to finish, then marks the store closed and closes the DB; an
operation arriving meanwhile queues behind it and gets `ErrStorageClosed`.
Close is idempotent and safe to call concurrently: exactly one call closes the
DB, the others return nil. No iterator, batch or snapshot is handed to a
caller -- each method closes what it opens before it leaves the gate -- so
there is no caller-held Pebble handle for Close to wait on. A method that
reuses another's body calls its unexported, ungated form (`scanNamespace`,
`scanByMetadata`, `getStats`), because a second shared acquisition would
deadlock behind a waiting Close. `pkg/storage/vector/close_gate_test.go` holds
a write (through the sync observer's clock) and a read (through the read
semaphore) inside the gate while Close runs.

## Target: the vector engine

### Status

This section is the five-phase program's target design, written ahead of the
code it specifies. Two of the five phases have shipped; the rest of this
section still describes work not yet wired to anything a caller can reach.

- **Phase 1 — engine foundation: shipped.** Raft opcodes 70-77
  (`protocol.OpVectorNamespaceCreate` and seven siblings), the `vector-engine`
  cluster feature gate, and apply handlers on both `MVCCEngine` and `KVEngine`
  (`pkg/engine/vector_handlers.go`) that read and write vector namespaces,
  vectors, and their metadata index inside the shared `storage.Batch`, on
  every local backend (memory, file, pebble, objstore). The reserved `__vec:`
  namespace is closed against every write verb and every compare, on both
  engines.
- **Phase 2 — index, checkpoint, determinism: shipped.** The in-memory ANN
  index (`pkg/engine.VectorIndexManager`), HNSW/IVF/flat `LiveIndex`
  adapters, deterministic layer/seed derivation (`pkg/vector.Deterministic*`),
  checkpoint read/write (`pkg/engine/vector_checkpoint.go`), and `Warm` /
  `RebuildNamespace` recovery. Wired into `MVCCEngine` and `KVEngine` via
  `WithMVCCVectorIndex` / `WithKVVectorIndex`, exercised by the acceptance
  tests in `pkg/engine/vector_acceptance_test.go`.
- **Phase 3 — Raft snapshot + multi-node parity: not started.** The vector
  keyspace is ordinary state-machine state and travels in today's snapshot by
  construction, but multi-node parity (join, catch-up, recovery) for it is
  unverified — no test in this tree stands up more than one replica.
- **Phase 4 — service-layer rewrite and transport parity: not started.**
  `pkg/service/vector.go` still proposes nothing: it is not wired to the
  opcodes above. The node-local Pebble vector store (`pkg/server/lifecycle`)
  is untouched and still serves production traffic. This phase also owns: the
  per-database storage-binding precondition noted under "Keys in the
  database's store"; count/byte totals and quota enforcement; the
  database-drop key-reclaim forward hazard (acceptance test 11); and OD-14
  shard_id removal across transports.
- **Phase 5 — backup/restore coverage: not started.**

Both shipped phases are additive and dormant: nothing proposes the new
opcodes yet (phase 4 does that). Their one observable effect is the `__vec:`
key-forgery refusal, which affects only keys under a prefix no existing caller
writes.

### Commands

Every vector write is a command in the extended protocol, proposed to the
database binding's host and applied by the engine. The opcodes, 70-77 in
`pkg/protocol/vector.go`, are wire format.

| Command | Applies |
|---|---|
| `OpVectorNamespaceCreate` | namespace record with geometry, index parameters, `seq = 0`, `next_id = 1`, training generation 0 |
| `OpVectorNamespaceDelete` | deletes the namespace record, every record, metadata index entry and mutation-log entry under it |
| `OpVectorUpsert`, `OpVectorBatchUpsert` | records, metadata index entries, `seq`, `next_id` for omitted IDs, mutation-log entries |
| `OpVectorDelete`, `OpVectorBatchDelete` | the same, removing |
| `OpVectorUpdateMetadata` | the record's metadata and its index entries; no ANN change |
| `OpVectorIndexReconfigure` | new index parameters and training generation; each replica rebuilds its index locally |

Each is wrapped by the guarded-write wrapper carrying the database's drop fence,
exactly like a KV write. The vector commands are new Raft commands, so they sit
behind the cluster feature gate: until every member of a database's group
supports them, vector writes on that database are refused with the typed
feature-gate error instead of being proposed. Reads — get, list, the four searches, namespace info,
index stats — are engine lookups through the host with the request's
consistency level.

`RebuildIndex` with unchanged parameters is a node-local rebuild on the serving
node; with changed parameters it proposes `OpVectorIndexReconfigure`. Rebuild and
compaction job handles are node-local: the progress call is routed to the node
that issued the handle, and a handle that node no longer knows (after a restart
or a leader change) is not-found, after which `GetIndexStats` reports the
index's state.

### Keys in the database's store

Reserved internal prefixes, covered by the internal-key guard so they never
appear in KV enumeration:

```
__vec:ns/<ns>                                           namespace record
__vec:v/<ns>/<id: 16 hex digits, big-endian>            vector record: embedding, metadata, version, timestamps
__vec:md/<ns>/<field, hex>/<value, hex>/<id: 16 hex digits, big-endian>  metadata equality index entry
__vec:ml/<ns>/<seq: 16 hex digits, big-endian>          mutation-log entry: operation and ID
```

Every integer component is hex-encoded (16 lowercase hex digits for the
8-byte value), which preserves the lexicographic ordering of a raw big-endian
value, one hex digit per nibble. Raw bytes would fail on `objstore`:
go-objstore's local provider refuses any key containing a NUL byte, and a raw
big-endian `uint64` is NUL for every leading byte up to the value itself --
the common case, since small, sequentially-allocated IDs (1, 2, 3, ...) are
the ones with the MOST leading zero bytes. The metadata field and value -- caller-supplied, unconstrained
strings, unlike a namespace name -- are hex-encoded too, both to remove any
NUL byte they might contain and to remove `/` as a possible segment-boundary
collision; because the metadata index is an EQUALITY index ("Shape chosen" in
architecture.md), losing the original strings' lexicographic order in their
encoded form costs nothing a lookup needs, since a lookup hex-encodes the same
field and value and matches the same segment.

Namespace names follow DNS-label rules, so they contain no `/` and no zero
byte, and are stored verbatim (not hex-encoded): unlike metadata, a namespace
name is validated at every write, so it cannot carry a NUL byte or a `/` for
the key layout to protect against. No tenant or database prefix is part of
any key: the store belongs to one database.

**Precondition (not yet enforced in code): the `storage.Batch` an apply
handler receives is the store of EXACTLY ONE database.** The key layout above
has no tenant or database prefix because it relies on this; it is an
assertion about the CALLER (whatever constructs the binding this engine is
wired into), not something `pkg/engine` can check from inside an apply
handler, which sees only a `storage.Batch` and has no way to ask "whose is
this?". Phases 1-2 wire nothing to a real binding, so the
precondition is latent and unenforced. Phase 4 (service-layer rewrite) is
where `pkg/service/vector.go` is wired to a real per-database storage
binding, and is where this precondition becomes checkable and must be
enforced -- e.g. by construction, if the binding type itself cannot be shared
across two databases -- rather than left as a comment.

**Forward hazard for phase 4: a database drop's key reclaim does not know
about `__vec:` keys.** `pkg/service.reclaimKeys` walks a fixed, explicit list
of key prefixes a database owns; `__vec:` is not on it, and adding it there
would not be correct anyway -- a raw prefix delete leaves the namespace
record, the vector records, the metadata index and the mutation log
consistent with EACH OTHER in the general case (they are deleted together),
but it is not how every other OpVectorNamespaceDelete removal happens (through
`deleteKeyRange` inside an apply batch, batched with the namespace record's own
delete), and a drop that bypasses that handler risks leaving one of those four
key families behind if the prefix list or the handler's own key set ever
drift apart. Phase 4, which wires a real per-database binding and therefore a
real drop path, must have teardown issue one `OpVectorNamespaceDelete` per
namespace the database holds -- through the same apply handler every other
vector delete goes through -- rather than teach `reclaimKeys` a fifth raw
prefix. This is a phase-4 acceptance test (see "Target: acceptance tests",
below), not phase-1's or phase-2's to fix: neither phase wires a real
database binding for `reclaimKeys` to run against.

### Apply and the index

1. The handler validates against the namespace record read through the apply
   batch (dimension, geometry), and writes every key it changes into the
   batch. A validation failure is an application-level refusal in the result,
   not an apply fault, so the log keeps advancing.
2. The batch commits with the entry.
3. After the commit, in log order, the engine applies the entry's index effect
   to the in-memory index: insert, update or delete by ID. An index error here
   marks the namespace for rebuild; it never fails the committed entry.

**Count and byte totals, and quota enforcement, are deferred, not shipped.**
Phase 1 (this document's "Target: the vector engine" as implemented) tracks
only `seq`, `next_id`, and `training_generation` on the namespace record. Per
namespace vector/byte counts and any quota check against them belong at the
service layer that proposes these commands -- phase 4 of the vector-replicated
program ("service-layer rewrite and transport parity", the same phase that
rewrites `pkg/service/vector.go` to propose through this engine instead of the
node-local Pebble store) -- and are not implemented in any phase before it.
`OpVectorUpsert` and `OpVectorBatchUpsert` do not apply them.

The mutation log keeps the last N entries per namespace, where N
(`defaultMutationLogWindow` in `pkg/engine/vector_handlers.go`, 50,000) is a
COMPILE-TIME CONSTANT, not a node setting: every replica trims at the
identical sequence only because every replica's binary carries the identical
value. A node setting would let two nodes running different configuration
trim at different points from the same log and diverge; if the window ever
needs to be configurable, it must be a REPLICATED value stored in the
namespace record itself and changed only through a command every replica
applies identically. Trimming itself happens in apply by sequence, so every replica
holds the same window regardless.

### Checkpoints

- Path: `<binding data root>/vector-index/<ns>/<seq>.idx`, written to a temporary
  name, synced, and renamed; older checkpoints for the namespace are removed
  after the rename.
- Content: index format version, namespace sequence, index parameters and
  training generation, the serialized index. Encrypted under the binding's
  encryptor, because a checkpoint holds every embedding in the namespace.
- Written on clean close, before a state-machine snapshot, and when the
  namespace's sequence has advanced by a configured amount since the last
  checkpoint — off the apply goroutine, under the namespace's index read lock.
- A `memory` binding writes none. A drop removes them with the store.

### Open

For each namespace record: load the newest checkpoint whose format version is
current and whose sequence is at least the oldest mutation-log sequence; replay
the log from the checkpoint's sequence; otherwise rebuild from the records.
Until a namespace is ready, searches on it return a retryable
`service.VectorIndexWarmingError`; get, list and metadata search work
from the store immediately.

### Snapshot

The state-machine snapshot of a raft binding appends one optional section per
namespace: format version, sequence, checkpoint bytes. On restore a section is
used only when its sequence equals the restored namespace record's; otherwise
the namespace is rebuilt. The section is optional in both directions, so a
snapshot without it restores correctly.

### Target: determinism

- Omitted IDs come from `next_id` in the namespace record, in apply.
- HNSW node level is `floor(-ln(u) * mL)` with `u` derived from a hash of the
  namespace and the ID, not from a random source.
- IVF k-means seeds from a hash of the namespace and its training generation.
- Timestamps in records come from the proposed command, not from the applying
  replica's clock.

### Target: what is deleted

`vectorstorage.NewPebbleVectorStorage` as the service's store, the vector store
in `pkg/server/lifecycle`, `shard.ShardVector` and `service.VectorShardID`,
`statemachine.VectorStateMachine`, `statemachine.VectorConsensusFSM` and their
factory, `service.WithRaftNode` for vectors, the vector service's
`SetClusterIsolationPolicyReader` and its tenant scoping of namespace names, and the namespaced vector
wrapper in `pkg/server` for any database with its own store. The vector
storage schema package is kept only if the engine's record codec reuses it.

### Target: acceptance tests

1. **Restart keeps search working.** Upsert, restart, search returns the same
   top-k. `TestVectorService_RestartPreservesConfiguredMetric`
   (`pkg/service/vector_restart_test.go`) pins this for the node-local service
   against a real PebbleDB store. Open for the engine once
   `pkg/service/vector.go` is rewired to it: `pkg/engine`'s own
   `Warm`/checkpoint recovery has not been run against this item end to end.
2. **The namespace's metric survives restart.** A cosine namespace searched
   after restart ranks by cosine. The query vector
   `TestVectorService_RestartPreservesConfiguredMetric` searches with is chosen
   so L2 and cosine ranking disagree.
3. **Replicas agree.** On a three-replica database, upsert through the leader
   and search with follower reads on each replica: identical top-k.
4. **Snapshot install.** A replica added after 10,000 upserts serves searches
   without a rebuild when the snapshot carries a matching checkpoint, and after
   one when it does not; both give the same results. Open: this item is about
   the engine's Raft snapshot, which is not wired.
   `TestVectorService_SnapshotInstallPreservesConfiguredMetric`
   (`pkg/service/vector_restart_test.go`) pins the analogous case for the
   node-local service -- which has no Raft snapshot of its store at all, so a
   "new replica" there is modeled as a fresh service opened over a filesystem
   copy of an already-populated store -- through the same `loadNamespaces`
   construction path; it is not a test of checkpoint-carrying Raft snapshots.
5. **Crash between commit and index update.** Inject the crash; after reopen the
   vector is searchable.
6. **The drop fence.** An upsert proposed before a database's drop fence and
   applied after it is refused, and the store holds no record of it.
7. **Atomicity.** A batch upsert whose apply batch is forced to fail leaves no
   record, no metadata entry and no sequence change.
8. **Every backend.** The vector conformance suite passes on memory, file,
   pebble, objstore and raft bindings.
9. **Tenant key.** Records and checkpoints of a barrier, volume or raft tenant's
   database cannot be read with the node's encryptor; sealing the tenant's
   barrier fails every vector capability closed.
10. **Deterministic IDs.** Omitted IDs are identical on every replica.
11. **A database drop reclaims every vector key.** (Phase 4.) After a
    database with vector namespaces is dropped, the store holds no
    `__vec:` key for it -- namespace records, vector records, the metadata
    index and the mutation log alike -- because teardown issued an
    `OpVectorNamespaceDelete` per namespace rather than a raw prefix delete
    that `reclaimKeys` does not know to run (see the forward hazard noted
    beside the single-database precondition, above).

## Test strategy

- **Unit**, under `qrdb_vector`: namespace lifecycle, each distance metric, each
  index type, dimension-mismatch refusal, upsert idempotence, each of the four
  search modes, metadata filter parsing, and progress reporting for rebuild and
  compaction.
- **GPU tests** run under the `gpu` tag via a dedicated Makefile target and are
  not part of the default suite.
- **No `time.Sleep`** when waiting on asynchronous rebuild/compaction — progress
  is driven through the job seam.
- **Integration**: vector parity across transports in the standalone stage.
- **Target**: the acceptance tests above; placement is asserted by opening the
  database's own store and probing `__vec:` keys by exact key, never through the
  vector service.

## Observability

- Namespace create/delete, rebuild and compaction are audited through
  [../audit/](../audit/).
- `GetIndexStats` is the primary operational signal: index size, vector count
  and structure health per namespace.
- Rebuild and compaction progress are the only way to observe a long-running
  maintenance operation; there is no log-only path.

## Wire-field bounds

The length-field contract for this plane — what each field can describe, that a
longer value is refused rather than truncated, that encoders return
`([]byte, error)` and validate before allocating, and that a refusal is a client
error mapping to `InvalidArgument`/400 — is one rule for the whole extended wire
protocol and is stated once, in
[../key-value/engineering.md](../key-value/engineering.md#wire-field-bounds--the-owning-contract-for-every-plane).

It is not restated here on purpose: four copies of a bound is how the four drift
apart.
