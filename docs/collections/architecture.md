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
are stored beneath the collection. Both are inside the reserved internal
namespace, so a collection's storage layout is not something a KV caller trips
over or can enumerate.

### ID strategies

Chosen at creation, fixed for the collection's life:

| Strategy | Behaviour | Use when |
|---|---|---|
| `unix_nano` (default) | ID from the insert timestamp in nanoseconds | you want roughly time-ordered IDs and no coordination |
| `increment` | monotonic counter | you want dense, small, predictable IDs |
| `field_hash` | ID hashed from a named document field (`--field-hash`) | the document has a natural key and re-inserting it must be idempotent |

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
4. `DropCollection` deletes every document in the collection, then drops every
   secondary index the collection declared, then deletes the metadata —
   irreversible.

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
- Documents are stored under the reserved `__sys:` namespace, so they are
  invisible to user KV enumeration.

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
