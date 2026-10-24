# Time Series — API

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.
>
> "Target" sections describe the time-series half of the multi-model database
> program ([../databases/architecture.md](../databases/architecture.md)); they
> are approved and not implemented.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column. Every capability
requires a `qrdb_ts` build.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.TimeSeries()`) | `scp` permission | CLI |
|---|---|---|---|---|---|---|---|
| Write point | `POST /ts/write` | `TSWrite` | `ts.Write` | `qrdb_ts_write` | `TSWrite` | `timeseries:write` | `ts write` |
| Write batch | `POST /ts/write-batch` | `TSWriteBatch` | `ts.WriteBatch` | `qrdb_ts_write_batch` | `TSWriteBatch` | `timeseries:write_batch` | `ts write-batch` |
| Write point (narrow) | `POST /ts/write-point` | `TSWritePoint` | `ts.WritePoint` | `qrdb_ts_write_point` | `TSWritePoint` | `timeseries:write_point` | — |
| Query raw points | `GET /ts/query` | `TSQuery` | `ts.Query` | `qrdb_ts_query` | `TSQuery` | `timeseries:query` | `ts query` |
| Aggregate | `GET /ts/aggregate` | `TSAggregate` | `ts.Aggregate` | `qrdb_ts_aggregate` | `TSAggregate` | `timeseries:aggregate` | `ts aggregate` |
| Rich query | `POST /ts/query-rich` | `TSRichQuery` | `ts.RichQuery` | `qrdb_ts_rich_query` | `TSRichQuery` | `timeseries:rich_query` | — |
| Delete series | `DELETE /ts/series/{metric}` | `TSDeleteSeries` | `ts.DeleteSeries` | `qrdb_ts_delete_series` | `TSDeleteSeries` | `timeseries:delete_series` | — |
| Get retention | `GET /ts/series/{metric}/retention` | `TSGetRetention` | `ts.GetRetention` | `qrdb_ts_get_retention_policy` | `TSGetRetention` | `timeseries:get_retention` | — |
| Set retention | `PUT /ts/series/{metric}/retention` | `TSSetRetention` | `ts.SetRetention` | `qrdb_ts_set_retention_policy` | `TSSetRetention` | `timeseries:set_retention` | — |
| List metrics | `GET /ts/metrics` | `TSListMetrics` | `ts.ListMetrics` | `qrdb_ts_list_metrics` | `TSListMetrics` | `timeseries:list_metrics` | — |

All ten capabilities are present on all six transports. **Six have no CLI verb**
(marked `—`) — a CLI coverage gap recorded in [cli.md](cli.md), not a transport
gap.

## Schemas

The SDK types in `sdk/go/transport/timeseries_types.go` are the reference
shapes. `TSDataPoint`:

```json
{ "metric": "cpu.usage", "value": 0.42, "timestamp_ns": 1755561600000000000,
  "tags": [ { "key": "host", "value": "web-01" } ] }
```

`TSQueryRequest`:

```json
{ "metric": "cpu.usage",
  "start_ns": 1755561600000000000, "end_ns": 1755565200000000000,
  "tags": [ { "key": "host", "value": "web-01" } ],
  "limit": 1000, "descending": false, "consistency": "linearizable" }
```

`TSAggregateRequest` adds `function` (an `AggFunc`) and `interval_ns`.
Response `TSAggResult` carries `timestamp_ns`, `value` and `count` per bucket.

`TSRichQueryRequest` takes `aggregation` (an `AggFunc`), `interval_ns`,
`group_by` (tag keys), `limit` and `percentile`. The response carries
`points`, an optional `aggregated_value`, and `grouped_results` when
`group_by` is set.

**`AggFunc` values** (`sdk/go/transport`, `pkg/timeseries`):

| Value | Function |
|---|---|
| 0 | none |
| 1 | avg |
| 2 | sum |
| 3 | min |
| 4 | max |
| 5 | count |
| 6 | percentile (reads `percentile` on rich query) |
| 7 | first |
| 8 | last |

`TSRetentionPolicy`:

```json
{ "duration_ns": 2592000000000000, "resolution_ns": 3600000000000, "aggregation": 1 }
```

`duration_ns` is how long points are kept; `resolution_ns` and `aggregation`
are intended to make retention a downsampling policy rather than a delete
horizon. **`duration_ns` is enforced by a leader-driven sweep every
`DefaultRetentionInterval` (one minute); `resolution_ns`/`aggregation`
downsampling is stored and returned but not applied** — see
[engineering.md](engineering.md).

**Target:** no request or response changes. Every capability is served for
every database on every backend that `MVCCEngine` composes over — memory,
file, pebble and objstore, shipped — and answers as not configured only where
time-series support was not compiled in (`WITH_QRDB_TS`). A dedicated
per-database raft binding remains target work.

`TSDeleteRequest`: `metric`, optional `tags`, optional `start_ns`/`end_ns`.
**With no range, the whole series is deleted.**

`TSListMetricsRequest`: optional `prefix` and `limit`.

### Wire forms per transport

The field names and encodings differ by transport:

| Transport | Point timestamp | Tags | Range | Aggregation function |
|---|---|---|---|---|
| Embedded | `timestamp_ns` | list of `{key, value}` | `start_ns`, `end_ns` | `AggFunc` number, table above |
| gRPC / Unix | `timestamp_ns` | repeated `TSTag` | `start_ns`, `end_ns` | `qrdb.v1.AggFunc` enum: `AGG_AVG` 0, `AGG_SUM` 1, `AGG_MIN` 2, `AGG_MAX` 3, `AGG_COUNT` 4, `AGG_PERCENTILE` 5 |
| QUIC | `timestamp_ns` | `ts.Write`, `ts.WriteBatch`, `ts.Query`, `ts.Aggregate` carry the gRPC messages; the other six carry JSON with tags as an object | `start_ns`, `end_ns` | gRPC enum on `ts.Aggregate`; `AggFunc` number on the JSON methods |
| REST | `timestamp` | object in a body; `host=web-01,region=eu` in the `tags` query parameter | `start_ns`, `end_ns` | name: `avg`, `sum`, `min`, `max`, `count`, `percentile` |
| MCP | `timestamp` | JSON object in a string | `start`, `end` | name: `avg`, `sum`, `min`, `max`, `count`, `percentile`, `first`, `last` (`func` on `qrdb_ts_aggregate`, `aggregation` elsewhere) |

`GET /ts/query`, `GET /ts/aggregate` and `GET /ts/metrics` take their
fields as query parameters. `qrdb_ts_set_retention_policy` takes
`duration_seconds` and `resolution_seconds`, not nanoseconds.

## Ranges and units

Every timestamp field is **nanoseconds**, except where the MCP row above
says seconds. Every range is closed: `[start_ns, end_ns]`, both ends
included.

## Pagination

`TSQuery`, `TSRichQuery` and `TSListMetrics` bound their result with `limit`;
there is no cursor or page token. A query over a wide range with no limit
returns everything in the range and should be treated as an unbounded read.
Aggregate results are bounded by the bucket count.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| `end_ns < start_ns` | 400 | `INVALID_ARGUMENT` |
| Unknown aggregation function | 400 | `INVALID_ARGUMENT` |
| `percentile` function without a percentile value | 400 | `INVALID_ARGUMENT` |
| Retention with `duration_ns <= 0` | 400 | `INVALID_ARGUMENT` |
| Unknown metric on query | 200 with an empty result | `OK` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Built without `qrdb_ts` | 404 (route not registered) | `UNIMPLEMENTED` |
| Write reaching a state machine with no time-series store | 501 | `UNIMPLEMENTED` — `service.CapabilityUnsupportedError` |
| Point refused by the store on its merits (empty metric, zero timestamp, bad batch point) | 400 | `INVALID_ARGUMENT` — `service.CommandRejectedError` |
| Time-series store closed on this replica | 503 | `UNAVAILABLE` — `service.SubsystemClosedError` |
| Time-series store failed during apply | 500 | `INTERNAL` — `service.ApplyFaultError` |

Querying an unknown metric is not an error: metrics are created implicitly by
writing to them, so "no such metric" and "no points in range" are the same
answer.

## Versioning

Time-series REST paths are unversioned (`/ts/...`). Nothing here is deprecated.
