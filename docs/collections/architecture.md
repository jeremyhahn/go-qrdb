# Document Collections — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

Storing JSON documents in a raw key-value store means every application invents
its own key scheme, its own ID allocation, and its own encoding — and then
cannot list "all documents of this kind" without knowing that private scheme.

## Shape chosen

A **collection** is a named document container inside a database, with a
declared ID strategy and an optional storage codec. Documents are
`map[string]interface{}` with a `uint64` ID.

```
database  →  collection  →  document (uint64 ID)
```

Collection metadata lives under `__sys:collections/{database}/{name}`; documents
live under `__col:{database}:{collection}/{id}`. Both are inside the reserved
internal namespace, so a collection's storage layout is not something a KV
caller trips over or can enumerate.

Neither key carries a tenant segment. The tenant dimension comes from the
namespace the transport registry applies underneath — see
[Trust boundaries](#trust-boundaries).

### ID strategies

Chosen at creation, fixed for the collection's life:

| Strategy | Behaviour | Use when |
|---|---|---|
| `unix_nano` (default) | ID from the insert timestamp in nanoseconds | you want roughly time-ordered IDs and no coordination |
| `increment` | monotonic counter, Raft-committed per `(database, collection)` | you want dense, small, predictable IDs |
| `field_hash` | ID hashed from a named document field (`--field-hash`) | the document has a natural key and re-inserting it must be idempotent |

`increment`'s counter is a replicated sequence owned by the collection, and it
is **torn down with the collection**: a name that is dropped and created again
starts over at 1. The alternative — carrying the old high-water mark into the
new collection — was rejected because it delivers neither property the table
above promises, and because there is nothing for a restarted counter to collide
with: a drop deletes every document in the collection before it reclaims the
counter (see [Data flow](#data-flow)).

### Explicitly supplied IDs

A document may carry its own `_id`. That is what makes re-import work — export
a collection, recreate it, put the documents back under the IDs they had — and
it is the second way an ID reaches the key space, alongside the strategy. Two
allocators over one keyspace need a rule, and there is one:

**An explicit `_id` is a claim on an identity; a generated ID is an
allocation.** The claim wins, permanently:

- **It reserves the counter.** On an `increment` collection the sequence is
  raised past the supplied value *before* the document is stored, so no ID the
  collection generates afterwards can reach it. This is the rule the relational
  layer already applies to an explicit value in an `AUTO_INCREMENT` column (see
  [../sql/engineering.md](../sql/engineering.md)), and the rule MySQL applies.
  `unix_nano` and `field_hash` have no counter, so there is nothing to reserve
  and nothing is written.
- **It never overwrites.** An insert whose `_id` already names a stored
  document is refused with a document-already-exists error. Insert requires the
  document to be absent exactly as update requires it to be present, and that
  pair is what keeps the two verbs distinguishable.

The cost is stated rather than hidden: the counter only moves up, so an
explicit `_id` far above the collection's watermark permanently strands the IDs
beneath it. That is the cheaper of the two failures — a stranded range wastes
IDs in a 64-bit space, an unreserved one destroys documents.

#### What a generated ID may overwrite

Exactly one case: a `field_hash` ID. That strategy derives the ID from a named
document field, so re-inserting a document with the same natural key resolves
to the same ID **on purpose** — the idempotent re-insert the table above
promises. Everything else is refused:

| ID origin | `unix_nano` | `increment` | `field_hash` |
|---|---|---|---|
| explicit `_id` | refuse | refuse | refuse |
| generated | refuse | refuse | **overwrite** (idempotent re-insert) |

Once explicit IDs reserve the counter, `increment` cannot reach an occupied ID
at all, so a refusal there means the allocator is wrong and the caller is told
rather than losing a document. `unix_nano` reaches one only when two inserts
read the same nanosecond, which is rare rather than impossible.

#### What this does not close

The existence check is a read followed by the document write, so it closes the
overwrite a single caller reaches with no concurrency at all — which is the
defect — and not a race between two concurrent inserts claiming the same
explicit `_id`. Closing that as well means writing the document with the
guarded `PutIfAbsent`, the only write whose outcome is defined under
concurrency. It is not used yet because the dedicated-store backend cannot
perform a guarded write, so a guarded insert would fail every collection insert
on that path. The guard lives in the service layer either way, so tightening
the mechanism later changes no caller.

### Alternatives rejected

- **A single global ID strategy.** Rejected: the three cases above genuinely
  conflict. `increment` needs a coordinated counter; `unix_nano` needs none;
  `field_hash` needs to be deterministic. Forcing one makes two of the three
  wrong.
- **Schema enforcement on write.** Not taken. `CollectionInfo` carries a
  `schema` map, but documents are free-form; the schema is descriptive, not a
  validator. Enforcing a schema here would duplicate what the relational layer
  ([../sql/](../sql/)) already does properly, with DDL and types.
- **String document IDs.** Rejected: `uint64` keeps IDs cheap to encode and
  order-preserving in key space. The JSON representation is a string
  (`json:"id,string"`) so that large values survive JavaScript clients.
- **Bulk document import.** Deliberately absent — see the scope fence below.
- **Refusing an explicit `_id` on an `increment` collection outright.**
  Rejected, though it is the cleanest contract on paper: the strategy owns ID
  assignment or the caller does, never both, and a collision becomes impossible
  rather than merely handled. The cost is that it deletes the re-import
  workflow — recreate a collection, put the documents back under the IDs they
  had — from the one strategy whose IDs are small and stable enough for a
  caller to have recorded them elsewhere. Reserving the counter makes that
  workflow *safe* instead of *forbidden* at the price of one read per explicit
  insert, and it keeps `increment` consistent with the relational layer's
  `AUTO_INCREMENT`, which takes the same view for the same reason.
- **Refusing the collision alone, without reserving the counter.** Rejected as
  a half-fix. It converts the silent overwrite into an error, which is the
  minimum bar, but it leaves the collection's own allocator walking into IDs a
  caller already used — so the caller who did nothing wrong, the one inserting
  without an `_id`, is the one who gets the error. Reserving the counter is
  what makes that unreachable; refusing the collision is what makes the
  remaining cases loud. Both, not either.
- **Treating an insert with an explicit `_id` as an upsert.** This is what the
  code did before, and it was not a decision — it was the absence of one. An
  insert that destroys a stored document and reports success is
  indistinguishable from one that stored a new document, and `UpdateDocument`
  already exists for callers that mean "replace whatever is there".

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Collection metadata, document CRUD, ID strategies | `pkg/service` (`collection_service.go`, `collection_types.go`) | go-qrdb |
| Document encoding (storage codec) | `pkg/codec`, backed by **go-codec** | go-qrdb / go-codec |
| Underlying storage and MVCC | `pkg/storage`, `pkg/mvcc` | go-qrdb |
| Secondary indexes on documents | `pkg/index` — see [../secondary-indexes/](../secondary-indexes/) | go-qrdb |
| SDK surface | `sdk/go` (`Client.Collections()`) | go-qrdb |

## Data flow

1. `CreateCollection` writes metadata under `__sys:collections/{db}/{name}`,
   fixing the ID strategy and codec.
2. `InsertDocument` allocates an ID per the strategy, encodes the document with
   the collection's codec, and writes it through the same service and Raft path
   as any other write.
3. `ListDocuments` is **paginated** (`page_size` / `page_token`), because a
   collection is an unbounded set by construction.
4. `DropCollection` reclaims everything the collection owns — irreversible.
   The order is fixed: **documents, then every secondary index the collection
   declared, then the `increment` counter, then the metadata**. The metadata is
   what makes the collection resolvable, so it goes last and an interrupted
   drop is completed by re-issuing it. Documents go first because each delete
   retracts that document's index entries, so the indexes are already empty
   when they are dropped; the counter goes after the documents, because a
   counter restarted while documents still existed could reissue an ID a live
   document already held. A step that fails fails the whole drop rather than
   being skipped — reporting success over an unreclaimed index or counter is
   what leaves an orphan behind forever.

### Drop order, and why the metadata key goes last

`DropCollection` tears down dependent structures **first** and deletes the
metadata key **last** — the same ordering, for the same reason, as relational
`DROP TABLE` in `pkg/sql/provider`, which reclaims a table's row and index data
before removing its catalog entry.

The metadata key is what makes a collection resolvable, so while it is present
the drop is re-issuable and finishes: a drop interrupted mid-documents, or
between the documents and the indexes, or between the indexes and the metadata,
is completed by simply running it again. The index drop is idempotent, so the
retry costs nothing where it has already succeeded.

**Every ordering has a bad interruption state; this one is the smallest.**

| Order | If it is interrupted before the last step |
|---|---|
| indexes, documents, metadata | The collection is live and **fully populated** with its declared indexes gone, for as long as the document deletion takes — the longest step. Reads fail loudly (a query naming a dropped index returns "index not found", not an empty result), but **writes do not**: inserts and updates keep succeeding with `unique` unenforced, and nothing anywhere reports it. |
| documents, metadata, indexes | The metadata is gone, so nothing in the store names the surviving indexes. That state is **permanent**: no retry can find them, because the record that listed them was deleted. This is the defect being fixed. |
| **documents, indexes, metadata** (chosen) | The collection is live and its indexes are gone, but it is **empty**, and the window is one metadata delete wide. A retry closes it. |

The chosen order does not make the live-without-indexes state impossible — it
makes it short, and it makes it recoverable. Stated plainly, and in both of the
sizes it comes in:

- **When every index drop succeeds**, the window is one metadata delete wide:
  between the last drop and that delete, the collection still resolves and
  still accepts writes with its declared indexes unmaintained, so an insert
  landing there is written without index maintenance and without `unique`
  enforcement.
- **When a drop fails partway through a collection's index list**, the drop
  returns and the window stays open until someone retries. The indexes before
  the failing one are already gone, so the collection is live and *partially*
  indexed — its metadata still declares indexes that no longer exist. This is
  wider than the first case and is bounded by operator response time, not by a
  single write.

Reads in either state fail rather than mislead: a query naming an index that
was dropped returns "index not found". Writes are the unguarded side, which is
why this is a window and not merely an inconvenience.

Closing it requires a persisted "dropping" state that the other collection
verbs refuse — a metadata-format change and a semantics decision for every
verb, not an implementation detail — and is tracked separately.

An index that cannot be dropped **fails the whole operation** rather than being
skipped. Index creation is idempotent by name and the *existing* definition
wins, so an index left behind silently overrides the definition a later
`CreateCollection` asks for on the same `(database, collection, field)` — a
`unique` constraint the new collection requested then goes unenforced. Reporting
success over that is worse than reporting the failure, and because the metadata
survives the operator can retry. An index reported as *already absent* is not a
failure: a teardown asked to remove something that is not there has got what it
asked for.

**Index ownership is derived from the name, not tracked.** A collection's
indexes are `{database}.{collection}.{field}`, and the drop removes every name
the metadata declares. If an index under that exact name already existed when
the collection was created — through the explicit index API, or built by a DAO —
`CreateCollection` adopted it silently, because index creation is a
name-idempotent no-op, and the drop then destroys it. This is a real collision
and it is chosen rather than overlooked: the metadata declares the index as the
collection's, and refusing to drop what the collection believes it owns
re-opens the orphan. Telling the two apart needs persisted ownership, which is
a separate capability.

## Trust boundaries

- Collections are addressed as `(database, name)` inside the authenticated
  tenant. The database travels in the request; the tenant never does.
- Permissions are split by action, not just by object: `collections:create`,
  `collections:read`, `collections:list`, `collections:drop`,
  `collections:insert`, `collections:update`, `collections:delete`. Dropping a
  collection is a distinct grant from deleting a document in it.
- Documents and metadata are stored under reserved key prefixes (`__col:`,
  `__sys:collections/`), so they are invisible to user KV enumeration.

### Tenant isolation is a property of the SERVICE, not of the key

A collection key names the database and never the tenant, so the isolation of a
document is entirely the isolation of the engines the collection service was
built over. Every transport therefore resolves its collection servicer per
request, through `service.Registry.CollectionService(tenant, database)`, exactly
as it resolves KV and MVCC. What that accessor returns is a collection service
built over the `<tenant>/<database>/`-namespaced engines, so the physical key
is `<tenant>/<database>/__col:{database}:{collection}/{id}`.

**This was not always true, and the failure mode is worth recording.** Until the
accessor existed, every transport held ONE process-wide collection servicer
built over the raw, pre-namespace engines. Under `prefix` isolation — where
tenants share one state machine and are separated only by that prefix — two
tenants naming the same database and the same collection addressed the same
documents: one could read another's, one's insert could overwrite another's at a
`field_hash`-derived ID, and one's collection name denied the other's. The
registry wrapper that isolates every other engine was applied to the bundle's
KV and index servicers, and the collection service was copied past it.

The lesson generalises beyond collections: **a service inherits isolation from
the engines it holds, never from the engines its neighbours hold.** A composite
service that is copied rather than rebuilt over the wrapped engines is
unisolated no matter how well isolated the rest of the bundle is.

The database appears twice in the physical key — once in the namespace and once
inside `__col:` — which is redundant and deliberately left alone: removing it
would change the key format and require moving existing documents, and this
design moves no keys (see the upgrade note in
[engineering.md](engineering.md#upgrading-across-the-tenant-namespace-change)).

Two compositions legitimately keep a single collection servicer, and both are
recorded rather than treated as exceptions to explain away:

- **A registry-less composition** — the embedded transport and single-database
  servers — where the whole store and the caller's own keyspace are the same
  set.
- **A resolution that already reached a dedicated store** — a `raft`-isolated
  tenant's per-database shard, a `volume`-isolated tenant's own Host, or a
  database carrying its own barrier over its own store. Those bundles are built
  over a store belonging to exactly one `(tenant, database)` pair already, and
  the accessor returns their collection service unchanged.

## What this feature deliberately does not do

- **No schema validation on write.** Documents are free-form.
- **No bulk import.** `qrdb batch import` targets the raw KV keyspace only;
  documents go in one at a time. This is stated in the CLI help and is a real
  constraint, not an oversight to route around.
- **No query language over documents.** Filtering is by secondary index or by
  the SQL layer, not by a document query DSL.
- **No partial update.** `UpdateDocument` replaces the document.
- **No index declaration over the wire.** A collection's index definitions are
  carried on the in-process service request only; no transport type, CLI flag or
  protobuf field exposes them yet. Collections created through the SDK or an API
  therefore have no secondary indexes to drop, and only an in-process composer
  can declare them.
