# Secondary Indexes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Create index | `POST /index/create` | `IndexCreate` | `index.Create` | `qrdb_index_create` | `Client.Index().IndexCreate` | `index:create` |
| Drop index | `POST /index/drop` | `IndexDrop` | `index.Drop` | `qrdb_index_drop` | `Client.Index().IndexDrop` | `index:drop` |
| Query (exact match) | `GET /index/query` | `IndexQuery` | `index.Query` | `qrdb_index_query` | `Client.Index().IndexQuery` | `index:query` |
| Scan (value range) | `GET /index/scan` | `IndexScan` | `index.Scan` | `qrdb_index_scan` | `Client.Index().IndexScan` | `index:scan` |
| List indexes | `GET /index/list` | `IndexList` | `index.List` | `qrdb_index_list` | `Client.Index().IndexList` | `index:list` |

Compound index **definition** has no wire capability: compound indexes are
declared in Go by implementing `index.CompoundIndexRegisterer` and are
registered when the DAO is constructed. They appear in `IndexList`. Recorded
here so the absence is not read as a missing endpoint.

## Schemas

`IndexCreate` request: index `name`, `field_name`, and `IndexConfig`:

```json
{ "name": "qrdb.User.Email", "field_name": "Email",
  "config": { "unique": true, "sparse": false, "numeric": false } }
```

Index names are `{database}.{entityType}.{FieldName}`.

`IndexQuery` request: `index_name`, `value`. Response:

```json
{ "keys": ["users/1", "users/7"] }
```

`IndexScan` request: `index_name`, `start`, `end` (half-open `[start, end)`).
Response:

```json
{ "entries": [ { "value": "shipped", "keys": ["orders/3", "orders/9"] } ] }
```

`IndexList` response: `{ "names": ["qrdb.User.Email", "qrdb.Order.Status"] }`.

`IndexCreate` and `IndexDrop` return `google.protobuf.Empty` on gRPC and an
empty body on REST.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Index already exists on create | 409 | `ALREADY_EXISTS` — `service.IndexExistsError` |
| Name held by another owner (a collection's index) on create or drop | 409 | `FAILED_PRECONDITION` — `index.OwnershipError`; drop a collection's index by dropping the collection |
| Unknown index on query, scan or drop | 404 | `NOT_FOUND` — never a silent full scan |
| Unique constraint violated (on the entity write) | 409 | `ALREADY_EXISTS` |
| Malformed request | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

## Pagination

`IndexQuery`, `IndexScan` and `IndexList` return complete result sets and are
not paginated. `IndexScan` is bounded by its `[start, end)` range, which is the
narrowing mechanism; a scan with both bounds unset returns the whole index and
should be treated as an unbounded read.

## Versioning

Index REST paths are unversioned (`/index/...`). Nothing here is deprecated.
