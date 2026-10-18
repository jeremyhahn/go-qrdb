# Transactions and Batch — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column exactly.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Execute transaction | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |
| Execute batch | `POST /batch` | `Batch` | `batch.Batch` | `qrdb_batch_execute` | `Client.Batch().ExecuteBatch` | `batch:execute` |
| Begin an import session | `POST /batch/import/begin` | `ImportBegin` | `batch.ImportBegin` | `qrdb_batch_import_begin` | `Client.Batch().ImportBegin` | `batch:execute` |
| Append a chunk | `POST /batch/import/{session}/append` | `ImportAppend` | `batch.ImportAppend` | `qrdb_batch_import_append` | `Client.Batch().ImportAppend` | `batch:execute` |
| Commit a session | `POST /batch/import/{session}/commit` | `ImportCommit` | `batch.ImportCommit` | `qrdb_batch_import_commit` | `Client.Batch().ImportCommit` | `batch:execute` |
| Abort a session | `POST /batch/import/{session}/abort` | `ImportAbort` | `batch.ImportAbort` | `qrdb_batch_import_abort` | `Client.Batch().ImportAbort` | `batch:execute` |
| Import a whole file (SDK/CLI convenience) | — | — | — | — | `Client.Batch().ImportFile` | `batch:execute` |
| List import formats | derived — static list | derived | derived | derived | client-side | `batch:execute` |

`batch import-formats` is answered from a compiled-in list rather than a wire
call. Recorded here so the absence of an endpoint is not read as a gap.
`ImportFile` is not its own RPC on any transport — it is the Go SDK driving
`ImportBegin`/`ImportAppend`*/`ImportCommit` for a caller that would rather
hand over an `io.Reader` than manage sequence numbers itself; `qrdb batch
import` is built on it, so the CLI surface is unchanged even though the wire
underneath is now four small calls instead of one large one.

There is no single-message import RPC on any transport. One existed
(`BatchImport`) and was removed outright, no compat, because it required
accepting a message far larger than the universal pre-auth cap any other
RPC is held to — see [architecture.md](architecture.md)'s "Alternatives
rejected".

## Request schemas

`POST /tx` — `TransactionRequest`:

```json
{
  "compares": [
    { "key": "<base64>", "target": 3, "result": 0, "value": "<base64>" }
  ],
  "success_ops": [ { "type": 0, "key": "<base64>", "value": "<base64>", "lease_id": 0 } ],
  "failure_ops": [ { "type": 2, "key": "<base64>" } ]
}
```

`target`: `0` version · `1` create_revision · `2` mod_revision · `3` value.
`result`: `0` equal · `1` greater · `2` less · `3` not_equal.
op `type`: `0` put · `2` delete. `1` get and `3` range are values of the
shared `OpType` enum but are refused in a transaction or batch branch
(`UnsupportedTransactionOpError`: REST 400, gRPC/Unix/QUIC/MCP
`INVALID_ARGUMENT`); read the keys after the transaction commits.

Response — `TransactionResponse`:

```json
{
  "succeeded": true,
  "revision": 4711,
  "responses": [ { "kvs": [], "count": 0, "succeeded": true } ]
}
```

`POST /batch` — `{"ops": [Op, ...]}`, response
`{"revision": 4712, "responses": [OpResponse, ...]}`. `revision` is the single
MVCC revision every op in the batch committed at, `0` for a batch with no put
or delete; gRPC, Unix and QUIC carry it as `int64 revision = 2` of
`BatchResponse`, the embedded client as `transport.BatchResponse.Revision`,
and MCP `qrdb_batch_execute` answers `{"Revision": N, "Responses": [...]}`.

`POST /batch/import/begin` — `ImportOptions` (`format`, `filename`,
`key_field`, `value_mode`, `value_field`, `skip_header`, `transactional`).
Response:

```json
{ "session_id": "a1b2c3d4e5f6a7b8" }
```

`POST /batch/import/{session}/append?seq=N` — the chunk's raw bytes as the
request body (`Content-Type: application/octet-stream`); `seq` is a query
parameter rather than a JSON field so the body stays raw bytes end to end,
with no base64 or envelope overhead counted against the pre-auth cap.
Response is empty on success (`204`).

`POST /batch/import/{session}/commit` — no body. Response `ImportResult`,
identical in shape to the old single-message import's response:

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
| Malformed request or unknown enum value | 400 | `INVALID_ARGUMENT` |
| `ImportBegin`'s format not inferable and not given | 400 | `INVALID_ARGUMENT` |
| A call's body (a chunk, or any other) exceeds the transport's pre-auth cap | 413 | `RESOURCE_EXHAUSTED` |
| `ImportAppend`'s `seq` is a gap, a regression, or a replay with different bytes | 400 | `INVALID_ARGUMENT` |
| Unknown or expired session | 404 | `NOT_FOUND` |
| Caller is not the session's (tenant, identity) | 403 | `PERMISSION_DENIED` |
| A session's byte, chunk-count, or per-tenant concurrency quota is exceeded | 429 | `RESOURCE_EXHAUSTED` |
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

## Pagination

No capability on this page returns an unbounded result set: a
transaction's responses are bounded by the ops submitted, and an import's
errors are bounded by its rows. No pagination applies.

## Versioning

Every path on this page is an unversioned data-plane path. Nothing here is
deprecated.
