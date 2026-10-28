# Watch — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its method matches the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Watch a key | `GET /watch?key=<k>` | `Watch` (server-streaming) | `watch.Watch` | `qrdb_watch` | `Client.Watch().Watch` | `watch:watch` |
| Watch a prefix | `GET /watch?key=<p>&prefix=true` | `Watch` (server-streaming, non-empty `range_end`) | `watch.WatchPrefix` | `qrdb_watch` (`prefix: true`) | `Client.Watch().WatchPrefix` | `watch:watch` |

REST query parameters: `key` (required), `prefix=true`, `start-revision=<n>`,
`prev-kv=true`.

Key and prefix watches are one gRPC method distinguished by `range_end`, and
one MCP tool distinguished by the `prefix` argument; QUIC exposes them as two
methods. Every transport checks `watch:watch` for both.

## Request

`WatchOptions`:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `start_revision` | int64 | 0 | replay from this revision, then continue live (REST query parameter `start-revision`) |
| `prev_kv` | bool | false | include the previous value in each event (REST query parameter `prev-kv`) |
| `progress_notify` | bool | false | emit one progress response after the replay completes, carrying a cursor safe to resume from; not accepted on REST |

REST selects the key with the `key` query parameter on `GET /watch`; adding
`prefix=true` makes `key` a prefix. It answers with Server-Sent Events.

## Response

`WatchResponse`, one per delivery:

```json
{
  "watch_id": 3,
  "revision": 4712,
  "events": [
    {
      "type": 0,
      "kv":      { "key": "app/config/timeout", "value": "MzBz", "mod_revision": 4712, "create_revision": 12, "version": 4 },
      "prev_kv": { "key": "app/config/timeout", "value": "MTVz", "mod_revision": 4711, "create_revision": 12, "version": 3 }
    }
  ]
}
```

`type`: `0` put · `1` delete on gRPC, QUIC and MCP; on REST it is the string
`"PUT"` or `"DELETE"`. A REST response also carries `compact_revision` when the
watch was canceled by compaction.

A terminal response sets `"canceled": true` and carries `cancel_reason`. SDK
callers should not parse that string: `WatchResponse.Err()` classifies it into
`WatchCompactedError`, `WatchStartRevisionUnsupportedError`,
`CapabilityUnsupportedError` (the serving node has no watch service), or
`WatchCanceledError`.

## Entity watch (service layer)

`WatchServicer.WatchEntity` and `WatchEntityType` project the same stream onto
entities. They are in-process only — no transport exposes them, which is why
they appear in no row of the capability matrix — and deliver
`WatchEntityEvent`s rather than `WatchResponse`s:

| Field | Type | Meaning |
|---|---|---|
| `EntityType` | string | the entity's type |
| `ID` | string | the entity's identifier within its type |
| `Data` | bytes | the payload after the change; nil for a delete |
| `Revision` | int64 | the MVCC revision of the change |
| `Type` | enum | `0` put · `1` delete |
| `Canceled` | bool | set on the single terminal event: the stream ended for a reason |
| `CancelReason` | string | that reason, the watcher error's message verbatim |

The terminal event names no entity — `ID`, `Data` and `Revision` are zero, and
only `EntityType` and `CancelReason` are set. **A watch that ended cleanly
delivers no terminal event at all**, so a consumer tells the two apart by what
arrived rather than by the channel's closure. As on the key-value stream, the
reason is carried verbatim so it classifies into the same typed error; a
consumer should not compare it against a literal.

## Status and error mapping

| Condition | REST | gRPC / Unix | Stream behavior |
|---|---|---|---|
| Stream opened | 200 | `OK` | events follow |
| Missing permission | 403 | `PERMISSION_DENIED` | refused at open |
| Barrier sealed | 503 | `UNAVAILABLE` | refused at open |
| Start revision above current | 200 | `OK` | accepted at open; a live watch that delivers once a change reaches that revision, matching etcd's semantics for a future start revision |
| Start revision compacted away | 416 | `OUT_OF_RANGE` | refused at open; the SDK error classifies to `WatchCompactedError` |
| Store cannot replay (`raft.sm_type` is not `mvcc`) | 501 | `UNIMPLEMENTED` | refused at open; the SDK error classifies to `WatchStartRevisionUnsupportedError` |
| Compaction outruns an in-progress replay | — | — | in-band: `canceled: true`, reason classifies to `WatchCompactedError` |
| Server shutting down | — | — | in-band: `canceled: true` with reason |

A start revision the store cannot serve is validated **before** the stream
opens, so it is answered with a status rather than in-band: the compacted-away
and cannot-replay rows above are refusals, not cancellations. A start revision
above current is not in this set — it names a revision the store has not
reached yet, not one it cannot serve, so it is accepted rather than refused.
QUIC and MCP are the exception by construction — both return their channel
before the server has answered, so their refusal arrives as the stream's first
and only terminal response.

Failures **after** the stream opens are reported in-band rather than as a status
code, because by then the status line is already sent. That is why every
abnormal termination carries a reason and no stream ever closes silently.

Either arrival yields the same typed SDK error, so a caller writes one
`errors.As` and it holds on every transport: the open error is classified by
`ClassifyWatchError` and the terminal response by `WatchResponse.Err()`. See
[engineering.md](engineering.md) § *Error semantics*.

## Pagination

Not applicable — the result is an unbounded stream by construction, bounded by
the caller's context rather than a page size.

## Versioning

`GET /watch` is an unversioned data-plane path. Nothing here is deprecated.
