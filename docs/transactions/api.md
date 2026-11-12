# Transactions and Batch — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column exactly.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Execute transaction | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |
| Execute batch | `POST /batch` | `Batch` | `batch.Batch` | `qrdb_batch_execute` | `Client.Batch().ExecuteBatch` | `batch:execute` |
| List import formats and codecs | `GET /batch/import/formats` | — | — | — | `sdk.AvailableCodecs` (codecs only) | `batch:execute` |
| Begin an import session | `POST /batch/import/begin` | `ImportBegin` | `batch.ImportBegin` | `qrdb_batch_import_begin` | `Client.Batch().ImportBegin` | `batch:execute` |
| Append a chunk | `POST /batch/import/{session}/append` | `ImportAppend` | `batch.ImportAppend` | `qrdb_batch_import_append` | `Client.Batch().ImportAppend` | `batch:execute` |
| Commit a session | `POST /batch/import/{session}/commit` | `ImportCommit` | `batch.ImportCommit` | `qrdb_batch_import_commit` | `Client.Batch().ImportCommit` | `batch:execute` |
| Abort a session | `POST /batch/import/{session}/abort` | `ImportAbort` | `batch.ImportAbort` | `qrdb_batch_import_abort` | `Client.Batch().ImportAbort` | `batch:execute` |
| Import a whole file (SDK/CLI convenience) | — | — | — | — | `Client.Batch().ImportFile` | `batch:execute` |
| List import formats | derived — static list | derived | derived | derived | client-side | `batch:execute` |
| Guarded multi-key commit (SDK convenience) | — | — | — | — | `Client.KVStore().(sdk.MultiGuardedBatchKVStore).PutAllIfAllUnchanged` | `transaction:execute` |

`batch import-formats` is answered from a compiled-in list, not a wire call.
`ImportFile` is not its own RPC on any transport: it is the Go SDK driving
`ImportBegin`/`ImportAppend`*/`ImportCommit` for a caller that would rather
hand over an `io.Reader` than manage sequence numbers itself. `qrdb batch
import` is built on it.

`PutAllIfAllUnchanged` is not its own RPC either: the store `Client.KVStore()`
returns turns its guards (`sdk.KVGuard`: a nil value means absent, any other
value means present and equal) into `Compare`s and its writes (`sdk.KVWrite`)
into the success branch of ONE `ExecuteTransaction`. Every write commits, or,
when any guard does not hold, none does and it returns `applied=false` with a
nil error; a non-nil error means the outcome is unknown. It is served on every
run mode, a standalone node's included, so an embedder writes an entry and its
index entries together, fenced against a concurrent writer, from `sdk/go`
alone.

There is no single-message import RPC on any transport: a whole file in one
message would exceed the pre-auth cap every other RPC is held to. See
[architecture.md](architecture.md)'s "Alternatives rejected".

## Request schemas

`POST /tx` — `TransactionRequest`:

```json
{
  "compares": [
    { "key": "k1", "target": "value", "result": "equal", "value": "v0" }
  ],
  "success_ops": [ { "type": "put", "key": "k1", "value": "v1", "lease_id": 0 } ],
  "failure_ops": [ { "type": "delete", "key": "k2" } ]
}
```

Keys and values are JSON strings, sent as their bytes.
`target`: `version` · `create_revision` · `mod_revision` · `value`; the
compare reads the field of the same name (`version`, `create_revision`,
`mod_revision` or `value`).
`result`: `equal` · `greater` · `less` · `not_equal`.
op `type`: `put` · `delete` · `get` · `range`. A `get` reads `key`; a `range`
reads `[key, range_end)`. A `get` that carries a `range_end` is refused whole
as an invalid argument (`TransactionGetRangeEndError`) and nothing is read. A read in a transaction branch is answered inside
the transaction, after the compares and the writes staged before it in the
same branch, and needs an MVCC state machine (`--state-machine mvcc`) and the
cluster feature `transaction-reads`; before that feature is active the
transaction is refused whole (`feature.NotActiveError`: REST 503,
gRPC/Unix/QUIC/MCP `UNAVAILABLE`), and nothing is applied. An unrecognised
`target` falls back to `version` and `result` to `equal`; an unrecognised op
`type`, in a transaction or a batch, is refused before anything is proposed
rather than read as a write: REST 400 (`UnknownOpTypeError`), gRPC/Unix/QUIC
`INVALID_ARGUMENT` for an unknown `OpType` enum value
(`UnsupportedTransactionOpError`), MCP a tool error (`UnknownOpTypeError`).

Response — `TransactionResponse`:

```json
{
  "succeeded": true,
  "revision": 4711,
  "responses": [
    { "kvs": [], "count": 0, "succeeded": true },
    { "kvs": [ { "key": "k1", "value": "v1", "create_revision": 4702,
                 "mod_revision": 4711, "version": 3, "lease": 0 } ],
      "count": 1, "succeeded": true }
  ]
}
```

`responses` holds one entry per op of the branch that ran, in order. A write's
entry carries no `kvs`; a read's carries the keys it found, with their MVCC
metadata, and `count`. An absent key answers `count: 0`.

`POST /batch` — `{"ops": [Op, ...]}`, response
`{"revision": 4712, "responses": [OpResponse, ...]}`. `revision` is the single
MVCC revision every op in the batch committed at, `0` for a batch with no put
or delete; gRPC, Unix and QUIC carry it as `int64 revision = 2` of
`BatchResponse`, the embedded client as `transport.BatchResponse.Revision`,
and MCP `qrdb_batch_execute` answers `{"Revision": N, "Responses": [...]}`.

`GET /batch/import/formats` — no body. Lists the import formats a session
accepts and the compiled-in value codecs, the same lists the desktop app's
`AvailableImportFormats` and `AvailableCodecs` bindings return:

```json
{ "formats": ["auto", "csv", "json", "ndjson", "yaml", "archive"], "codecs": ["json", "..."] }
```

The TypeScript SDK's `batch.importContent(data, opts)` (REST) runs one
session over the routes below for content the caller holds in memory, such
as a file picked in a browser: begin, append in 1 MiB chunks with `seq`
from 0, commit, and abort on any failure. The browser UI's Import tab uses
it; the desktop app imports from a path with `importFile`.

`POST /batch/import/begin` — `ImportOptions` (`format`, `filename`,
`key_field`, `value_mode`, `value_field`, `skip_header`, `transactional`,
`collection`). Batch import writes the raw KV keyspace only, so a non-empty
`collection` is refused with `422` (`importing into a collection ... is not
supported`), the same refusal the desktop binding's `ImportFile` makes; the
rows are never written to KV in its place. `format` is one of the list
`GET /batch/import/formats` reports (`codec.ImportFormats`, shared with the
desktop binding's `AvailableImportFormats`). Response:

```json
{ "session_id": "a1b2c3d4e5f6a7b8" }
```

`POST /batch/import/{session}/append?seq=N` — the chunk's raw bytes as the
request body (`Content-Type: application/octet-stream`); `seq` is a query
parameter rather than a JSON field so the body stays raw bytes end to end,
with no base64 or envelope overhead counted against the pre-auth cap.
Response is empty on success (`204`).

`POST /batch/import/{session}/commit` — no body. Response `ImportResult`:

```json
{
  "total": 1000, "succeeded": 998, "failed": 2,
  "errors": [ { "row": 41, "key": "id-41", "message": "..." } ]
}
```

`POST /batch/import/{session}/abort` — no body. Empty response on success
(`204`).

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success (including `succeeded: false`) | 200 | `OK` |
| `ImportAppend`/`ImportCommit`/`ImportAbort` success with no body | 204 | `OK` |
| Malformed request | 400 | `INVALID_ARGUMENT` |
| `ImportBegin`'s format not inferable and not given | 400 | `INVALID_ARGUMENT` |
| A call's body (a chunk, or any other) exceeds the transport's pre-auth cap | 413 | `RESOURCE_EXHAUSTED` |
| `ImportAppend`'s `seq` is a gap, a regression, or a replay with different bytes | 400 | `INVALID_ARGUMENT` |
| Unknown or expired session | 404 | `NOT_FOUND` |
| Caller is not the session's (tenant, identity) | 403 | `PERMISSION_DENIED` |
| A session's byte, chunk-count, or per-tenant concurrency quota is exceeded | 429 | `RESOURCE_EXHAUSTED` |
| A `--transactional` import commit fails (`ImportTransactionAbortedError`) | 409 | `ABORTED` |
| An import proposal or an expanded archive exceeds its bound (`ImportProposalTooLargeError`, `ImportArchiveExpandedTooLargeError`) | 413 | `RESOURCE_EXHAUSTED` |
| A transaction branch's reads answer more than 4 MiB (`TxnReadResultTooLargeError`) | 413 | `RESOURCE_EXHAUSTED` |
| An import session's stored state is corrupt (`ImportSessionCorruptError`) | 500 | `INTERNAL` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

**A failed compare is HTTP 200 with `succeeded: false`.** Mapping it to 4xx
would make an expected outcome indistinguishable from a malformed request.

## Payload limits

Every transport applies the same pre-auth message cap,
`transport.max_message_bytes` (default 4 MiB), to EVERY call in the import
session lifecycle exactly as it does to every other unary request: REST's
request body, gRPC's and Unix's `MaxRecvMsgSize`, QUIC's wire frame
payload, and MCP's JSON-RPC request body. There is no larger-message
opt-in — a chunk over the cap is refused before authentication, on every
transport, the same way any other oversized request is; a dataset bigger
than one chunk is more `ImportAppend` calls, never a bigger one.

Independently of the transport cap, a session's total staged bytes and
chunk count are bounded by `import_session.max_session_bytes` /
`max_chunks_per_session` (see [engineering.md](engineering.md)), and a
tenant's concurrent sessions by `import_session.max_sessions_per_tenant`.

A transaction's reply is bounded too. The whole encoded reply of the
chosen branch may not exceed 4 MiB (`service.MaxReplyBytes`), the largest
reply one node relays to another: a transaction forwarded from a node that
holds no replica comes back through the gRPC forwarder, whose receive limit
is the same 4 MiB. The reply is charged its framing -- 13 bytes, plus 19 for
every op of the branch, reads and writes alike -- and, for every entry a get
or range answers, 62 bytes plus the entry's key and value. These are the
largest the protobuf `TransactionResponse` fields can encode to, so the
charge is never less than the encoded reply. The bound is fixed, not
configurable: every replica applies the transaction and must decide it the
same way. It is checked before any write of the branch is applied, and each
read is charged its entries plus every earlier put of the branch that falls
in its range -- an upper bound, so a branch that overwrites a key it reads
can be refused slightly short of 4 MiB. A range stops scanning at the entry
that crosses the bound. A branch over it is refused whole with
`TxnReadResultTooLargeError`, which names the limit and the reply bytes
charged when it was crossed: none of its writes is applied, the revision
does not move, and the group keeps applying the entries after it. Read
fewer or smaller keys, or use fewer ops, per transaction, or page a large
range outside one.

## Pagination

No capability on this page returns an unbounded result set: a
transaction's reply is bounded at 4 MiB (see "Payload limits"), and an
import's errors are bounded by its rows. No pagination applies.

## Versioning

Every path on this page is an unversioned data-plane path. Nothing here is
deprecated.
