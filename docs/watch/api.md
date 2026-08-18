# Watch — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its method matches the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Watch a key | `GET /watch?key=<k>` | `Watch` (server-streaming) | `watch.Watch` | `qrdb_watch` | `Client.Watch().Watch` | `watch:watch` |
| Watch a prefix | `GET /watch?key=<p>&prefix=true` | `Watch` (server-streaming, prefix field) | `watch.WatchPrefix` | `qrdb_watch` | `Client.Watch().WatchPrefix` | `watch:watch_prefix` |

REST query parameters: `key` (required), `prefix=true`, `start-revision=<n>`,
`prev-kv=true`.

Key and prefix watches are one gRPC method distinguished by a request field, and
one MCP tool distinguished by an argument; QUIC exposes them as two methods.
The permission checked differs in every case — a prefix watch is the broader
grant and is authorized as such.

## Request

`WatchOptions`, on every transport:

| Field | Type | Default | Meaning |
|---|---|---|---|
| `start_revision` | int64 | 0 | replay from this revision, then continue live |
| `prev_kv` | bool | false | include the previous value in each event |
| `progress_notify` | bool | false | request periodic progress responses on an idle stream |

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
  ],
  "canceled": false
}
```

`type`: `0` put · `1` delete.

A terminal response sets `"canceled": true` and carries `cancel_reason`. SDK
callers should not parse that string: `WatchResponse.Err()` classifies it into
`WatchCompactedError`, `WatchStartRevisionUnsupportedError`, or
`WatchCanceledError`.

## Status and error mapping

| Condition | REST | gRPC / Unix | Stream behavior |
|---|---|---|---|
| Stream opened | 200 | `OK` | events follow |
| Missing permission | 403 | `PERMISSION_DENIED` | refused at open |
| Barrier sealed | 503 | `UNAVAILABLE` | refused at open |
| Start revision above current | 400 | `INVALID_ARGUMENT` | refused at open |
| Start revision compacted away | — | — | in-band: `canceled: true`, reason classifies to `WatchCompactedError` |
| Store cannot replay (`raft.sm_type` is not `mvcc`) | — | — | in-band: reason classifies to `WatchStartRevisionUnsupportedError` |
| Server shutting down | — | — | in-band: `canceled: true` with reason |

Failures **after** the stream opens are reported in-band rather than as a status
code, because by then the status line is already sent. That is why every
abnormal termination carries a reason and no stream ever closes silently.

## Pagination

Not applicable — the result is an unbounded stream by construction, bounded by
the caller's context rather than a page size.

## Versioning

`GET /watch` is an unversioned data-plane path. Nothing here is deprecated.
