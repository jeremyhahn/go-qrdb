# Document Collections — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Create collection | `POST /v1/collections` | `CreateCollection` | `collection.Create` | `qrdb_collection_create` | `Client.Collections().CreateCollection` | `collections:create` |
| Collection info | `GET /v1/collections/{database}/{name}` | `GetCollectionInfo` | `collection.GetInfo` | `qrdb_collection_info` | `GetCollectionInfo` | `collections:read` |
| List collections | `GET /v1/collections/list` | `ListCollections` | `collection.List` | `qrdb_collection_list` | `ListCollections` | `collections:list` |
| Drop collection | `DELETE /v1/collections/{database}/{name}` | `DropCollection` | `collection.Drop` | `qrdb_collection_drop` | `DropCollection` | `collections:drop` |
| Insert document | `POST /v1/collections/{database}/{name}/documents` | `InsertDocument` | `collection.InsertDocument` | `qrdb_collection_insert` | `InsertDocument` | `collections:insert` |
| Get document | `GET /v1/collections/{database}/{name}/documents/{id}` | `GetDocument` | `collection.GetDocument` | `qrdb_collection_get` | `GetDocument` | `collections:read` |
| Update document | `PUT /v1/collections/{database}/{name}/documents/{id}` | `UpdateDocument` | `collection.UpdateDocument` | `qrdb_collection_update` | `UpdateDocument` | `collections:update` |
| Delete document | `DELETE /v1/collections/{database}/{name}/documents/{id}` | `DeleteDocument` | `collection.DeleteDocument` | `qrdb_collection_delete` | `DeleteDocument` | `collections:delete` |
| List documents | `GET /v1/collections/{database}/{name}/documents` | `ListDocuments` | `collection.ListDocuments` | `qrdb_collection_list_documents` | `ListDocuments` | `collections:list` |

REST additionally registers wildcard routes (`GET`, `POST`, `PUT`, `DELETE` on
`/v1/collections/*`) carrying the same permissions, for paths the explicit
patterns do not cover.

The MySQL and PostgreSQL wire protocols expose a collection as a table in the
connected database, alongside relational tables. They are not a tenth row above
because they carry no collection DDL — only reads and DML against a collection
that already exists — but they read and write the same documents, through the
same per-`(tenant, database)` resolution.

## Which database a request addresses

Every capability names its database EXPLICITLY — in the URL path, the request
body, the protobuf message, or the tool argument — and that is the database
whose keyspace the operation resolves into. The transport-level database
selector (`X-QRDB-Database` and its per-transport equivalents) does **not**
override it: a request to `/v1/collections/app/products` addresses `app`
whatever the header says. The tenant, as everywhere else, comes from the
authenticated identity and never from the request.

One consequence is visible to callers: a collection request naming a database
that does not exist for the caller's tenant is refused with the standard
not-found, rather than silently operating in a database nothing created.

## Schemas

`CreateCollectionRequest`:

```json
{ "database": "app", "name": "products",
  "id_strategy": "field_hash", "field_hash": "sku",
  "storage_codec": "json" }
```

`id_strategy` is `unix_nano` (default), `increment`, or `field_hash`.
`field_hash` is required when the strategy is `field_hash` and ignored
otherwise. The strategy is immutable after creation.

`CollectionInfo`:

```json
{ "name": "products", "database": "app",
  "id_strategy": "field_hash", "field_hash": "sku",
  "schema": {}, "document_count": 421,
  "storage_codec": "json", "created_at": "2026-08-19T10:00:00Z" }
```

`schema` is descriptive metadata. **It is not enforced on write** — documents
are free-form.

`DocumentResponse`:

```json
{ "id": "1755561600000000000", "document": { "email": "alice@example.com" } }
```

**`id` is a `uint64` carried as a JSON string** (`json:"id,string"`), so values
above 2^53 survive JavaScript clients intact. `InsertDocumentResponse` uses the
same encoding.

### Supplying `_id` on an insert

An insert body's `document` may carry its own `_id` (a positive number), which
is used instead of a generated ID:

```json
{ "document": { "_id": 42, "kind": "deploy" } }
```

The supplied value is **reserved** against the collection's ID strategy, and an
`_id` that already names a stored document is **refused with `409 Conflict`**
(`AlreadyExists` on gRPC) rather than overwritten. A zero, negative or
non-numeric `_id` is read as "generate one for me". See
[architecture.md](architecture.md#explicitly-supplied-ids) for the full rule
and [user.md](user.md#supplying-your-own-document-id) for the workflow.

**`_id` is not echoed back as a field.** It names the document; the document's
`document` object holds its data and its ID is the `id` beside it. An insert of
`{"_id": 42, "kind": "deploy"}` reads back as:

```json
{ "id": "42", "document": { "kind": "deploy" } }
```

which is the same shape a document inserted without an `_id` has. The same
applies to `UpdateDocument`: its ID comes from the request path, and an `_id`
in the body is consumed rather than stored, so a read-modify-write that sends
the document back unchanged does not reintroduce the key.

`ListDocumentsResponse`:

```json
{ "documents": [ ... ], "next_page_token": "...", "total_count": 421 }
```

## Pagination

`ListDocuments` is the only paginated capability in this feature and the only
unbounded result set. Request `page_size` and `page_token`; follow
`next_page_token` until it is empty. `total_count` is the full size, not the
page size.

`ListCollections` is not paginated — the number of collections in a database is
bounded by an operator action.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Collection already exists | 409 | `ALREADY_EXISTS` — `service.CollectionAlreadyExistsError` |
| Unknown collection | 404 | `NOT_FOUND` — never auto-created |
| Unknown document ID | 404 | `NOT_FOUND` |
| `field_hash` strategy without `field_hash`, or unknown strategy | 400 | `INVALID_ARGUMENT` |
| Malformed document JSON | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Unknown tenant | 404 | `NOT_FOUND` — `service.TenantNotFoundError` |
| Unknown database for this tenant | 404 | `NOT_FOUND` — `service.DatabaseNotFoundError` |
| Collection surface not configured | 501 | `UNIMPLEMENTED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Isolation backend unavailable | 503 | `UNAVAILABLE` — `service.IsolationBackendUnavailableError` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

The tenant, database, sealed-barrier and isolation-backend rows are the
resolution outcomes every other data-plane surface already reports; the
collection surface reports them because it resolves through the same
`service.Registry` accessors. A refused resolution is never downgraded to a
servicer the caller was refused.

## Versioning

Collection REST paths are under `/v1/`. Nothing here is deprecated.
