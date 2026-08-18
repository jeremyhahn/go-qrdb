# Key-Value Store — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Six transports, one capability set. The gRPC service is `qrdb.v1.QRDBService`;
the **Unix socket transport serves the same gRPC wire over a UDS**, so its
method names are identical to the gRPC column throughout. Embedded is a direct
Go call on the SDK client.

## Capability matrix

| Capability | REST | gRPC / Unix (`qrdb.v1.QRDBService/`) | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Put | `PUT /kv/{key}` (also `PUT /kv`) | `Put` | `kv.Put` | `qrdb_put` | `Client.KV().Put` | `kv:put` |
| Get | `GET /kv/{key}` (also `GET /kv`) | `Get` | `kv.Get` | `qrdb_get` | `Client.KV().Get` | `kv:get` |
| Delete | `DELETE /kv/{key}` (also `DELETE /kv`) | `Delete` | `kv.Delete` | `qrdb_delete` | `Client.KV().Delete` | `kv:delete` |
| List keys | `GET /kv?prefix=` | `List` | `kv.List` | `qrdb_list` | `Client.KV().List` | `kv:list` |
| Scan (keys + values) | `GET /kv/scan?prefix=` | `Scan` | `kv.Scan` | `qrdb_scan` | `Client.KV().Scan` | `kv:list` |
| Exists | derived — client-side `Get` | derived | derived | derived | `Client.KV().Exists` | `kv:exists` |
| Put with lease | `PUT /kv/{key}` + lease field | `PutWithLease` | `mvcc.PutWithLease` | `qrdb_mvcc_put_with_lease` | `Client.MVCC().PutWithLease` | `mvcc:put_with_lease` |
| Get with revision metadata | `GET /kv/{key}?revision=true` | `GetRevision` | `mvcc.GetRevision` | `qrdb_mvcc_get_revision` | `Client.MVCC().GetRevision` | `mvcc:get_revision` |
| Get at revision | `GET /kv/{key}?at-revision=N` | `GetAtRevision` | `mvcc.GetAtRevision` | `qrdb_mvcc_get_at_revision` | `Client.MVCC().GetAtRevision` | `mvcc:get_at_revision` |
| Range | `GET /kv/range` | `Range` | `mvcc.Range` | `qrdb_mvcc_range` | `Client.MVCC().Range` | `kv:range` / `mvcc:range` |
| Current revision | `GET /kv/revision` | `CurrentRevision` | `mvcc.CurrentRevision` | `qrdb_mvcc_current_revision` | `Client.MVCC().CurrentRevision` | `mvcc:current_revision` |
| Compact | `POST /kv/compact` | `Compact` | `mvcc.Compact` | `qrdb_mvcc_compact` | `Client.MVCC().Compact` | `mvcc:compact` |
| Restore to revision | `POST /kv/restore` | `RestoreToRevision` | `mvcc.RestoreToRevision` | `qrdb_mvcc_restore_to_revision` | `Client.MVCC().RestoreToRevision` | `mvcc:restore` |

**`Exists` has no wire operation on any network transport.** The REST, gRPC,
QUIC and MCP transports each derive it from `Get` client side. This is
deliberate — a boolean is exactly the information `Get` already carries — and
the `kv:exists` permission is still checked. It is recorded here rather than
omitted so the next reader does not go looking for the missing endpoint.

## Authentication and scoping

- **Authentication is mTLS.** The tenant is derived from the authenticated
  client identity; a client-supplied tenant is never honoured.
- **Database scope** travels in `X-QRDB-Database` on REST, the
  `x-qrdb-database` metadata value on gRPC and Unix, and the equivalent frame
  field on QUIC/MCP. Absent, the server's default database (`system`) is used.
- **Consistency** travels as the `consistency` query parameter on REST and as a
  request field elsewhere: `strong`, `linearizable`, `lease_read`, `eventual`.
- **Write mode** travels as a request field: `sync`, `wal_durable`, `batched`.

## Request and response schemas

`MVCCKeyValue` (the shape returned by every revision-aware read):

```json
{
  "key": "app/config/timeout",
  "value": "MzBz",
  "create_revision": 12,
  "mod_revision": 47,
  "version": 3,
  "lease": 0
}
```

`RangeOptions` fields, mirrored across transports: `limit`, `revision`,
`keys_only`, `count_only`, `sort_by` (`key|value|version|create|mod`),
`sort_order` (`asc|desc`). Range is the only unbounded result set in this
feature and `limit` is how it is paginated; a range without a limit returns
everything in `[start, end)`.

`RestoreRevisionRequest`: `revision`, and exactly one of `prefix` or `keys`,
plus `dry_run` and `force`. The response carries the plan entries
(`action`, `key`, `from_rev`, `to_rev`, `value_size`) and the conflict list.

## Status and error mapping

| Condition | REST | gRPC / Unix | Notes |
|---|---|---|---|
| Success | 200 | `OK` | |
| Key not found | 404 | `NOT_FOUND` | distinct from an empty value |
| Bad parameter (`at-revision` unparseable, bad sort key) | 400 | `INVALID_ARGUMENT` | |
| Revision above current, or below the compaction floor | 400 | `INVALID_ARGUMENT` | fails closed, never approximated |
| Non-MVCC state machine asked for an MVCC operation | 400 | `FAILED_PRECONDITION` | `service: requires raft.sm_type: mvcc` |
| Restore targets a reserved prefix or key | 403 | `PERMISSION_DENIED` | unconditional, no override |
| Restore scope empty, ambiguous, or over `MaxRestoreKeys` | 400 | `INVALID_ARGUMENT` | refused whole |
| Missing permission | 403 | `PERMISSION_DENIED` | |
| Barrier sealed | 503 | `UNAVAILABLE` | fail closed |
| Not the leader, no resolver configured | 503 | `UNAVAILABLE` | otherwise routed to the leader |

## Versioning

REST paths in this feature are unversioned (`/kv/...`) rather than `/v1/...`;
the versioned prefix is used by the control-plane surfaces. No route in this
feature is deprecated.

## Public routes

`GET /health` and `GET /v1/config/initialized` are reachable without
authentication. `GET /metrics` requires an authenticated caller but no specific
permission. Nothing else in this feature is public.
