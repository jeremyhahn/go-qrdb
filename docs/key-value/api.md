# Key-Value Store — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-11
> Conflicts with this document are defects. Fix them here first.

Six transports, one capability set. The gRPC service is `qrdb.v1.QRDBService`;
the **Unix socket transport serves the same gRPC wire over a UDS**, so its
method names are identical to the gRPC column throughout. Embedded is a direct
Go call on the SDK client.

## Capability matrix

| Capability | REST | gRPC / Unix (`qrdb.v1.QRDBService/`) | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Put | `PUT /kv/{key}` (also `PUT /kv`) | `Put` | `kv.Put` | `qrdb_put` | `Client.KV().Put` | `kv:put` |
| Get | `GET /kv/{key}` | `Get` | `kv.Get` | `qrdb_get` | `Client.KV().Get` | `kv:get` |
| Delete | `DELETE /kv/{key}` (also `DELETE /kv`) | `Delete` | `kv.Delete` | `qrdb_delete` | `Client.KV().Delete` | `kv:delete` |
| List keys | `GET /kv?prefix=` | `List` | `kv.List` | `qrdb_list` | `Client.KV().List` | `kv:list` |
| Scan (keys + values) | `GET /kv/scan?prefix=` | `Scan` | `kv.Scan` | `qrdb_scan` | `Client.KV().Scan` | `kv:range` |
| Exists | derived — client-side `Get` | derived | derived | derived | `Client.KV().Exists` | `kv:exists` |
| Put if absent | `POST /kv/put-if-absent` | `PutIfAbsent` | `kv.PutIfAbsent` | `qrdb_put_if_absent` | `Client.KV().(GuardedKVClient).PutIfAbsent` | `kv:put` |
| Compare and delete | `POST /kv/compare-and-delete` | `CompareAndDelete` | `kv.CompareAndDelete` | `qrdb_compare_and_delete` | `Client.KV().(GuardedKVClient).CompareAndDelete` | `kv:delete` |
| Compare and swap | `POST /kv/compare-and-swap` | `CompareAndSwap` | `kv.CompareAndSwap` | `qrdb_compare_and_swap` | `Client.KV().(GuardedKVClient).CompareAndSwap` | `kv:put` |
| Put with lease | `PUT /kv/{key}` + lease field | `PutWithLease` | `mvcc.PutWithLease` | `qrdb_mvcc_put_with_lease` | `Client.MVCC().PutWithLease` | `mvcc:put_with_lease` |
| Get with revision metadata | `GET /kv/{key}?revision=true` | `GetRevision` | `mvcc.GetRevision` | `qrdb_mvcc_get_revision` | `Client.MVCC().GetRevision` | `mvcc:get_revision` |
| Get at revision | `GET /kv/{key}?at-revision=N` | `GetAtRevision` | `mvcc.GetAtRevision` | `qrdb_mvcc_get_at_revision` | `Client.MVCC().GetAtRevision` | `mvcc:get_at_revision` |
| Range | `GET /kv/range` | `Range` | `mvcc.Range` | `qrdb_mvcc_range` | `Client.MVCC().Range` | `kv:range` / `mvcc:range` |
| Current revision | `GET /kv/revision` | `CurrentRevision` | `mvcc.CurrentRevision` | `qrdb_mvcc_current_revision` | `Client.MVCC().CurrentRevision` | `mvcc:current_revision` |
| Compact (this database's history) | `POST /kv/compact` | `Compact` | `mvcc.Compact` | `qrdb_mvcc_compact` | `Client.MVCC().Compact` | `mvcc:compact` (`ScopeSelfStrict`) |
| Restore to revision | `POST /kv/restore` | `RestoreToRevision` | `mvcc.RestoreToRevision` | `qrdb_mvcc_restore_to_revision` | `Client.MVCC().RestoreToRevision` | `mvcc:restore` |
| Node-keyspace compaction policy (platform) | `GET` / `PUT /admin/node-compaction` | `GetNodeCompaction` / `SetNodeCompaction` | `admin.GetNodeCompaction` / `admin.SetNodeCompaction` | `qrdb_node_compaction` / `qrdb_update_node_compaction` | `Client.Admin().NodeCompaction` / `SetNodeCompaction` | `admin:node_compaction` (`ScopeCross`) |

**Each transport decides the permission, before the service.** The `scp`
permission in the matrix is checked by the transport's authorize step -- the
REST route table, the gRPC/Unix method option, the QUIC and MCP handler
registrations, the embedded transport's operation map -- and a caller that
lacks it is refused with the transport's typed denial (403 /
`PERMISSION_DENIED`, QUIC `StatusPermissionDenied`, MCP `-32003`, embedded
`auth.ErrUnauthorized`). The KV service is never reached, so a refused read is
never answered as an empty result. Listing returns keys only and needs
`kv:list`; every read that returns values -- `Get`, `Scan`, `Range` -- needs
its own permission. *(Corrected: `GET /kv` was documented as a `Get` alias
under `kv:get`, and `Scan` under `kv:list`; `GET /kv` lists keys under
`kv:list`, and `Scan` returns values under `kv:range`, on REST as on every
other transport.)*

**The node-keyspace policy is the platform's.** It governs the keys outside
every database namespace, so no tenant may read or set it: the permission is
`ScopeCross`, and a caller bound to any tenant but the system tenant is
refused (403 / `PERMISSION_DENIED`) on every transport, the embedded one
included. The body, both ways, is `{"compaction": {"mode": "revision",
"retainRevisions": 100000}}` -- the database policy's shape -- and an absent
`compaction` is no policy (in a request, it turns the policy off). A `PUT`
answers with the policy now in force.

**Put with lease on a state machine without revision history.** On the default
`pebble` state machine the lease is attached and the value stored, but there is
no revision to report: every transport answers with `create_revision`,
`mod_revision` and `version` all `0`. See
[../leases/api.md](../leases/api.md).

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
something that never arbitrated anything. **Every transport listed in the matrix
above now carries them natively**, MCP included: its three tools each make one
call to the atomic service method, with no tool call before and no fallback
after.

The capability contract also holds across a **version skew**. A client that has
the verbs talking to a server that does not gets
`transport.CapabilityUnsupportedError` — an older gRPC or unix server answers
`UNIMPLEMENTED`, an older QUIC server's dispatch answers its Unimplemented
status, an older REST server answers 404/405 on the absent route, and an older
MCP server has no such tool to call, all of which the SDK reconstitutes into the
same typed refusal. A consumer can therefore degrade instead of retrying a call
that can never succeed.

A refused guard is a **200 / `OK`** carrying `stored`, `deleted` or `swapped`
set to false — on MCP, a successful tool result whose text is the same
single-field JSON object, `{"stored": false}`. Losing the race is the normal
outcome of a claim, not an error: a caller told the call "failed" would retry
the one thing that can never succeed.

On MCP the three tools take the key as a plain string and every value —
`value`, `expected`, `next` — base64-encoded (RFC 4648), matching `qrdb_put`
and the REST bodies. An absent value argument is the empty value rather than an
error: a guard compares bytes, and the empty value must not be the one value a
claim can never be released from. Each tool requires the permission of the
write it performs — `kv:put` for `qrdb_put_if_absent` and
`qrdb_compare_and_swap`, `kv:delete` for `qrdb_compare_and_delete` — so no
separate grant is needed to reach the guarded plane over MCP.

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
| Key not found | 404 | `NOT_FOUND` | distinct from an empty value. QUIC answers `StatusNotFound`; the SDK reconstitutes `transport.KeyNotFoundError` from the status, never from the message |
| Bad parameter (`at-revision` unparseable, bad sort key) | 400 | `INVALID_ARGUMENT` | |
| Revision above current | 400 | `INVALID_ARGUMENT` | fails closed, never approximated |
| Revision at or below the database's compaction floor | 416 | `OUT_OF_RANGE` | `service.RevisionCompactedError`; fails closed, never approximated. QUIC answers `StatusOutOfRange` and MCP `OUT_OF_RANGE`; every SDK transport's `GetAtRevision` reconstitutes the refusal as `transport.RevisionCompactedError` carrying the revision asked for, from the status, never from the message, so a caller can tell it from a transient failure. *(Corrected: this row said 400 / `INVALID_ARGUMENT`; the classification has been `OUT_OF_RANGE` since it was introduced -- see [../api-server/api.md](../api-server/api.md). Only the embedded and REST clients used to type the refusal; the gRPC, Unix, QUIC and MCP clients returned a generic request error despite their documented contract, and now type it too.)* |
| `Compact` to a revision below 1, or above the current revision | 400 | `INVALID_ARGUMENT` | `service.CompactRevisionInvalidError`, `service.CompactRevisionFutureError` |
| `Compact` to a revision the database is already compacted to | 416 | `OUT_OF_RANGE` | `service.RevisionCompactedError`; nothing changes |
| `Compact` on a server with no database scope (raw service) | 422 | `FAILED_PRECONDITION` | `service.CompactScopeRequiredError`; there is no store-wide compaction |
| `Compact` before every replica of the group supports per-database compaction | 503 | `UNAVAILABLE` | `featuregate.NotActiveError` naming `prefix-compaction`; nothing proposed, and a retry succeeds once the feature is active. *(Corrected: this row said 422 / `FAILED_PRECONDITION`, `service.FeatureNotEnabledError`; the condition clears by itself, and every feature-gated refusal is now one type.)* |
| `SetNodeCompaction` with an invalid policy | 400 | `INVALID_ARGUMENT` | `compaction.InvalidPolicyError` |
| `SetNodeCompaction` before every replica of the shared store supports the compaction-state command | 503 | `UNAVAILABLE` | `featuregate.NotActiveError` naming `prefix-compaction` |
| `NodeCompaction` / `SetNodeCompaction` on a node whose shared store keeps no MVCC history | 422 | `FAILED_PRECONDITION` | `service.NodeCompactionUnavailableError` |
| `NodeCompaction` / `SetNodeCompaction` by a caller of any tenant but the system tenant | 403 | `PERMISSION_DENIED` | the `ScopeCross` refusal; in process, `tenant.CrossTenantDeniedError` |
| Non-MVCC state machine asked for an MVCC operation | 400 | `FAILED_PRECONDITION` | `service: requires raft.sm_type: mvcc` |
| Guarded request body over the endpoint's encoded byte bound | 413 | — | REST only; the coarse pre-decode guard |
| Guarded request key and values, SUMMED, over what one proposal can carry | 413 | — | REST only; the precise limit, derived from the Raft entry ceiling |
| Guarded request key over what the command encoding can frame (65535 bytes) | 413 | `INVALID_ARGUMENT` | the key length prefix is two bytes; REST reports it at the edge, the other transports at encode time |
| Guarded request body carrying data after the first JSON value | 400 | — | REST only; refused rather than silently truncated |
| State machine has no handler for the operation's opcode, or was built without the subsystem (no lessor, no time-series store, create/mod-revision compares on a non-MVCC backend) | 501 | `UNIMPLEMENTED` | `service.CapabilityUnsupportedError`. QUIC answers `StatusUnimplemented`; every SDK transport reconstitutes it as `transport.CapabilityUnsupportedError` so a caller can degrade instead of retrying |
| State machine refused the command on its merits (an invalid TTL, a malformed point) | 400 | `INVALID_ARGUMENT` | `service.CommandRejectedError` |
| State machine refused the command because a unique value is taken | 409 | `ALREADY_EXISTS` | `service.CommandConflictError` |
| The subsystem that would serve the command is closed on this replica | 503 | `UNAVAILABLE` | `service.SubsystemClosedError` |
| A committed command no writer of this version produced (undecodable) | 500 | `DATA_LOSS` | `service.CommandCorruptError` |
| The subsystem failed while applying the command | 500 | `INTERNAL` | `service.ApplyFaultError` |
| Restore targets a reserved prefix or key | 403 | `PERMISSION_DENIED` | unconditional, no override |
| Restore scope empty, ambiguous, or over `MaxRestoreKeys` | 400 | `INVALID_ARGUMENT` | refused whole |
| Missing permission | 403 | `PERMISSION_DENIED` | |
| Barrier sealed | 503 | `UNAVAILABLE` | fail closed |
| Not the leader, no resolver configured | 503 | `UNAVAILABLE` | otherwise routed to the leader. QUIC answers `StatusUnavailable`, not the generic `StatusError` it used to |
| Raft timed out the proposal or read: an election, or a leader still committing the first entry of its term | 503 | `UNAVAILABLE` | `quicraft.ErrTimeout`, whatever wrapper carried it. Retryable once a leader serves; a timed-out write may still have committed, so retry only a write made idempotent. *(Corrected: this answered 500 / `INTERNAL`, which reported an election as a server fault.)* |
| The leader stepped down with the write already in its log | 503 | `UNAVAILABLE` | `quicraft.ErrNotLeader` under `*quicraft.MaybeAppliedError`. The write may still commit under the next leader, so it is not a `NOT_LEADER` refusal and is never forwarded; retry only a write made idempotent. *(Corrected: this was held and re-proposed, then answered as a `NOT_LEADER` refusal.)* |

Every condition in this table has a QUIC status; the whole vocabulary is in
[the API server's status mapping](../api-server/api.md#common-status-mapping).

**How a state machine's refusal keeps its kind.** A state machine must not
fail an apply for a deterministic consequence of client input — that halts the
Raft group — so it encodes the refusal into its result instead. The encoding
carries a KIND (`protocol.AppErrorKind`: capability, invalid, conflict,
unavailable, fault, corrupt), and the service layer turns each kind into its
own typed error. Before, every refusal but the capability one shared a single
type, so an invalid TTL and a missing lease subsystem reached a caller as the
same condition: 501 over REST, `INTERNAL` over gRPC and QUIC.

## Versioning

REST paths in this feature are unversioned (`/kv/...`) rather than `/v1/...`;
the versioned prefix is used by the control-plane surfaces. No route in this
feature is deprecated.

## Public routes

`GET /health` and `GET /v1/config/initialized` are reachable without
authentication. `GET /metrics` requires an authenticated caller but no specific
permission. Nothing else in this feature is public.
