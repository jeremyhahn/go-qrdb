# Key-Value Store — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-02

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
| Put if absent | `POST /kv/put-if-absent` | `PutIfAbsent` | `kv.PutIfAbsent` | — | `Client.KV().(GuardedKVClient).PutIfAbsent` | `kv:put` |
| Compare and delete | `POST /kv/compare-and-delete` | `CompareAndDelete` | `kv.CompareAndDelete` | — | `Client.KV().(GuardedKVClient).CompareAndDelete` | `kv:delete` |
| Compare and swap | `POST /kv/compare-and-swap` | `CompareAndSwap` | `kv.CompareAndSwap` | — | `Client.KV().(GuardedKVClient).CompareAndSwap` | `kv:put` |
| Put with lease | `PUT /kv/{key}` + lease field | `PutWithLease` | `mvcc.PutWithLease` | `qrdb_mvcc_put_with_lease` | `Client.MVCC().PutWithLease` | `mvcc:put_with_lease` |
| Get with revision metadata | `GET /kv/{key}?revision=true` | `GetRevision` | `mvcc.GetRevision` | `qrdb_mvcc_get_revision` | `Client.MVCC().GetRevision` | `mvcc:get_revision` |
| Get at revision | `GET /kv/{key}?at-revision=N` | `GetAtRevision` | `mvcc.GetAtRevision` | `qrdb_mvcc_get_at_revision` | `Client.MVCC().GetAtRevision` | `mvcc:get_at_revision` |
| Range | `GET /kv/range` | `Range` | `mvcc.Range` | `qrdb_mvcc_range` | `Client.MVCC().Range` | `kv:range` / `mvcc:range` |
| Current revision | `GET /kv/revision` | `CurrentRevision` | `mvcc.CurrentRevision` | `qrdb_mvcc_current_revision` | `Client.MVCC().CurrentRevision` | `mvcc:current_revision` |
| Compact | `POST /kv/compact` | `Compact` | `mvcc.Compact` | `qrdb_mvcc_compact` | `Client.MVCC().Compact` | `mvcc:compact` |
| Restore to revision | `POST /kv/restore` | `RestoreToRevision` | `mvcc.RestoreToRevision` | `qrdb_mvcc_restore_to_revision` | `Client.MVCC().RestoreToRevision` | `mvcc:restore` |

## The guarded write verbs

`PutIfAbsent`, `CompareAndDelete` and `CompareAndSwap` are conditional writes
whose condition is evaluated **inside the state machine**, so they arbitrate a
cross-process race in one round trip rather than a read followed by a write a
second writer can slip between. `PutIfAbsent` is the acquire half of a claim,
`CompareAndDelete` its release half — an unguarded delete can un-decide what a
guarded create decided — and `CompareAndSwap` the edit between them. A swap
leaves an absent key absent: a swap is not a create.

They take **no write mode**. A conditional write must be linearizable to mean
anything, so it is always synchronous; a relaxed-durability guarded write would
be a guarantee it cannot keep.

They live on a **capability sub-interface**, `transport.GuardedKVClient`, not on
`KVClient`, and a consumer detects them by type assertion:

```go
if gv, ok := client.KV().(transport.GuardedKVClient); ok {
    stored, err := gv.PutIfAbsent(ctx, "claim", []byte("acme"))
}
```

The separation is deliberate. A transport can only offer these once its wire
contract carries them, and a transport that answered the assertion while
emulating a guarded write with a read and a write would be strictly worse than
one that declined it — the consumer would build a distributed lock on top of
something that never arbitrated anything. Every transport listed in the matrix
above now carries them natively; **MCP does not, and its assertion correctly
returns false** rather than promising a verb it cannot honour.

The capability contract also holds across a **version skew**. A client that has
the verbs talking to a server that does not gets
`transport.CapabilityUnsupportedError` — an older gRPC or unix server answers
`UNIMPLEMENTED`, an older QUIC server's dispatch answers its Unimplemented
status, and an older REST server answers 404/405 on the absent route, all of
which the SDK reconstitutes into the same typed refusal. A consumer can
therefore degrade instead of retrying a call that can never succeed.

A refused guard is a **200 / `OK`** carrying `stored`, `deleted` or `swapped`
set to false. Losing the race is the normal outcome of a claim, not an error: a
caller told the call "failed" would retry the one thing that can never succeed.

Request and response bodies on REST carry values as base64 JSON `[]byte`, not
as JSON strings. A guard compares bytes, and a value that did not survive a
UTF-8 round trip would make `CompareAndDelete` refuse a release that should
have succeeded — a lock that can be taken and never let go.

The REST bodies are **bounded twice**, and the split is deliberate: one number
cannot be both conservative enough never to reject a legal request and tight
enough to bound memory, so neither number is asked to be both.

- A coarse guard on the **encoded** body. It answers the memory question only —
  nothing unbounded is ever read — and models nothing about how any field
  encodes.
- The precise limit on the **decoded** content, applied after decoding where
  the sizes are known exactly: the key plus every value **summed**, never
  budgeted per field, against a ceiling computed from that verb's own command
  framing.

Both refusals are a 413 that names its limit.

Both derive from the only limit the system actually enforces on one of these
requests: a guarded verb becomes exactly **one Raft log entry**, which
go-quicraft caps at 8 MiB. A request larger than that cannot be proposed no
matter what a transport accepts, so admitting it buys nothing.

The decoded ceiling is **per verb**, computed from that verb's own command
framing — `PutIfAbsent` and `CompareAndDelete` frame their command in 3 bytes,
`CompareAndSwap` in 7 because it length-prefixes `expected` so `next` can
follow. One shared ceiling with a safety margin would refuse, on the verbs with
less framing, requests their encoders accept and every other transport carries.

Summing is the load-bearing part. `CompareAndSwap` carries two values, and
giving each its own budget multiplies the ceiling by the field count — a caller
could then claim several times the memory the limit appears to allow.

The key is bounded separately at **65535 bytes**, which is not a policy choice:
the command encoding frames a key with a two-byte length prefix, so a longer key
cannot be encoded on any transport. REST reports it at the edge as a 413 naming
the key; the other transports fail at encode time.

Two parse refusals, both 400, both closing a path where malformed input would
have performed a write and reported success:

- A body carrying **more than one top-level JSON value**. A JSON decoder stops
  at the end of the first value and leaves the rest unread, so `{...}{...}`
  would otherwise perform the first operation and report success.
- A body carrying an **unknown member**. A misspelled `value` would otherwise
  decode as absent, and `PutIfAbsent` would store an empty value and report
  success; a misspelled `expected` would make the guard compare against nothing.
  A typo must not become a mutation.

The other transports do not need an equivalent: gRPC and unix bound the
complete protobuf message far above the entry ceiling, so the entry ceiling is
the operative limit there too and the set of guarded requests that succeed is
the same on every transport. REST refuses an oversized one at the edge rather
than at the state machine, which is the one visible difference.

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
| Guarded request body over the endpoint's encoded byte bound | 413 | — | REST only; the coarse pre-decode guard |
| Guarded request key and values, SUMMED, over what one proposal can carry | 413 | — | REST only; the precise limit, derived from the Raft entry ceiling |
| Guarded request key over what the command encoding can frame (65535 bytes) | 413 | `INVALID_ARGUMENT` | the key length prefix is two bytes; REST reports it at the edge, the other transports at encode time |
| Guarded request body carrying data after the first JSON value | 400 | — | REST only; refused rather than silently truncated |
| State machine has no handler for the operation's opcode, or was built without the subsystem | 501 | `UNIMPLEMENTED` | QUIC answers `StatusUnimplemented`; every SDK transport reconstitutes it as `transport.CapabilityUnsupportedError` so a caller can degrade instead of retrying |
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
