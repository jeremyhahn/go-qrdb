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
4. `DropCollection` deletes the collection and every document in it —
   irreversible.

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
