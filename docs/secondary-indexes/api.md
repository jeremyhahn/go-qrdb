# Secondary Indexes — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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

The Embedded column needs an `IndexServicer` on the transport. A node's own
`Node.Client()` has one already. An embedder that builds its own transport with
`embedded.New` supplies it from the SDK alone, over the same host and shard it
gives `sdk.NewKVService`:
`embedded.WithIndexService(sdk.NewIndexService(host, shardID))`
(`sdk.IndexServicer` is the interface type). Without it, every embedded index
call returns `embedded.NilServiceError` naming `IndexServicer`.

## Schemas

`IndexCreate` request: index `name`, `field_name`, and the flags `unique`,
`sparse`, `numeric` and `case_insensitive`, all at the top level:

```json
{ "name": "qrdb.User.Email", "field_name": "Email",
  "unique": true, "sparse": false, "numeric": false, "case_insensitive": false }
```

Index names are `{database}.{entityType}.{FieldName}`.

`numeric: true` builds the index with order-preserving numeric terms: values
compare as numbers, and a value that is not a decimal number, or is `NaN`, is
not indexed. Every transport carries the flag (the REST and MCP `numeric`
field, the gRPC / Unix `IndexCreateRequest.numeric`, the QUIC and embedded
`transport.IndexConfig.Numeric`). The order is specified in
[architecture.md](architecture.md#numeric-order).

`IndexQuery` request: `index_name`, `value`. Response:

```json
{ "keys": ["users/1", "users/7"] }
```

`IndexScan` request: `index_name`, `start`, `end` (half-open `[start, end)`).
Either bound may be omitted for an unbounded side. REST response, a map from
index value to keys:

```json
{ "entries": { "shipped": ["orders/3", "orders/9"] } }
```

gRPC / Unix returns `IndexScanResponse.entries`, a list of `IndexScanEntry { value, keys }`.

On a numeric index each bound must be a decimal number (`2`, `-1.5`, `6.02e23`,
`Inf`); `NaN` or any other text is a malformed request. A bound ending in one
`0x00` byte names the value immediately after the number before it, which is
how a caller asks for an exclusive lower or an inclusive upper bound. Each
entry's `value` is the number's canonical decimal text (`1.50` → `1.5`).

`IndexList` response: `{ "names": ["qrdb.User.Email", "qrdb.Order.Status"] }`.

The MCP query, scan and list tools return the same JSON without the
`keys`, `entries` or `names` wrapper.

`IndexCreate` and `IndexDrop` return `google.protobuf.Empty` on gRPC and an
empty body on REST.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Index already exists on create, same owner | 200 | `OK` — idempotent; a create asking for a different value type (`numeric`) rebuilds the index from the stored records |
| Name held by another owner (a collection's index) on create or drop | 409 | `FAILED_PRECONDITION` — `index.OwnershipError`; drop a collection's index by dropping the collection |
| Unknown or dropped index on query or scan | 404 | `NOT_FOUND` — never a silent full scan; every SDK transport (REST, gRPC, Unix, QUIC, MCP, embedded) returns `transport.IndexNotFoundError` naming the index |
| Unknown or already-dropped index on drop | 200 | `OK` — idempotent |
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
