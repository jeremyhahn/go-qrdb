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
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

## Versioning

Collection REST paths are under `/v1/`. Nothing here is deprecated.
