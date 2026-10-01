# Transactions and Batch — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column exactly.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Execute transaction | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |
| Execute batch | `POST /batch` | `Batch` | `batch.Batch` | `qrdb_batch_execute` | `Client.Batch().ExecuteBatch` | `batch:execute` |
| Import file | `POST /batch/import` | `BatchImport` | `batch.Import` | `qrdb_batch_import` | `Client.Batch().ImportFile` | `batch:execute` |
| List import formats | derived — static list | derived | derived | derived | client-side | `batch:execute` |

`batch import-formats` is answered from a compiled-in list rather than a wire
call. Recorded here so the absence of an endpoint is not read as a gap.

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

`POST /batch` — `{"ops": [Op, ...]}`, response `{"responses": [OpResponse, ...]}`.

`POST /batch/import` — the file body plus `ImportOptions`
(`format`, `filename`, `key_field`, `value_mode`, `value_field`, `skip_header`,
`transactional`). Response `ImportResult`:

```json
{
  "total": 1000, "succeeded": 998, "failed": 2,
  "errors": [ { "row": 41, "key": "id-41", "message": "..." } ]
}
```

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success (including `succeeded: false`) | 200 | `OK` |
| Malformed request or unknown enum value | 400 | `INVALID_ARGUMENT` |
| Import format not inferable and not given | 400 | `INVALID_ARGUMENT` |
| Import body over the transport ceiling | 413 | `RESOURCE_EXHAUSTED` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |

**A failed compare is HTTP 200 with `succeeded: false`.** Mapping it to 4xx
would make an expected outcome indistinguishable from a malformed request.

## Payload limits

| Transport | Maximum import size |
|---|---|
| REST, gRPC, Unix, MCP | 100 MB |
| QUIC | ~63 MB (shared per-RPC payload cap) |

## Pagination

Neither capability returns an unbounded result set: a transaction's responses
are bounded by the ops submitted, and an import's errors are bounded by its
rows. No pagination applies.

## Versioning

`POST /tx`, `POST /batch` and `POST /batch/import` are unversioned data-plane
paths. Nothing here is deprecated.
