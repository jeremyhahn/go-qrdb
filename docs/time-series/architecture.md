# Time Series — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

> **Build-tag gated.** Available only in builds carrying `qrdb_ts`
> (`WITH_QRDB_TS=1`). `qrdb admin capabilities` reports `timeSeries` when it is
> compiled in.

## Problem

Metric data has a shape general key-value storage handles badly: append-heavy,
queried by time range rather than by key, filtered by dimensions (tags), read
mostly as aggregates rather than raw points, and worth discarding after a while.
Storing it as ordinary keys means the caller invents a key encoding, does its
own range maths, and never gets retention.

## Shape chosen

A metric-and-tags model over composite keys, with server-side aggregation and
per-metric retention:

```
metric name  +  tag set  +  timestamp (ns)  ->  float64 value
```

Keys are composite so a time range for a metric is a contiguous seek rather than
a scan-and-filter. Tags are indexed separately (`idx:metric:`, `idx:tag:`) so
tag-filtered queries do not degrade to a full metric scan.

Three query surfaces, in increasing power:

| Call | Answers |
|---|---|
| `TSQuery` | raw points in a range, optionally tag-filtered, limited, ordered |
| `TSAggregate` | one aggregate function bucketed by interval |
| `TSRichQuery` | aggregation **plus** `group_by` over tags, plus percentile |

Timestamps are **nanoseconds** throughout, on every field and every flag.

### Alternatives rejected

- **Storing points as ordinary KV keys.** Rejected: the caller then owns the key
  encoding, and retention becomes an application cron job.
- **A single query call with every option.** Partially rejected — `TSRichQuery`
  *is* the everything call, but `TSQuery` and `TSAggregate` remain because they
  are the common cases, they are cheaper, and they carry narrower permissions
  (`timeseries:query`, `timeseries:aggregate`, `timeseries:rich_query`).
- **Client-side aggregation.** Rejected: shipping every raw point to compute an
  average is exactly the traffic this feature exists to avoid.
- **Millisecond or second timestamps.** Rejected: nanoseconds are the native
  Go resolution and downsampling is lossless in one direction only.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Time-series storage, composite keys, tag indexes, retention | `pkg/timeseries` | go-qrdb |
| Aggregation functions | `pkg/timeseries` | go-qrdb |
| Capability registration (`timeSeries`) | `pkg/capabilities` | go-qrdb |
| Underlying storage and the barrier | `pkg/storage`, `pkg/seal` | go-qrdb |
| SDK surface | `sdk/go` (`Client.TimeSeries()`) | go-qrdb |

## Storage and the internal namespace

Time-series data lives under reserved prefixes — `ts:` for points,
`idx:metric:` and `idx:tag:` for the tag indexes, `retention:` for policies.
All four are inside the internal-key guard, so they never appear in user KV
enumeration and cannot be restored by `kv restore`. The time-series subsystem
reads them through its own engine, not through KV enumeration, which is why they
are granted no enumeration escape.

## Retention

Retention is **per metric**, not global, and carries three fields:

- `duration_ns` — how long points are kept.
- `resolution_ns` — the downsampled resolution, optional.
- `aggregation` — which function produces the downsampled value.

That triple is what makes retention a downsampling policy rather than a delete
policy: a metric can keep a year at hourly resolution instead of keeping nothing.

## Trust boundaries

- Metrics are scoped per (tenant, database) like every other data-plane object.
- Ten capabilities, ten permissions (`timeseries:write`,
  `timeseries:query`, `timeseries:delete_series`, `timeseries:set_retention`, …).
  Setting retention is destructive over time and is a distinct grant from
  writing.
- Points are barrier-encrypted at rest and fail closed when the barrier is
  sealed.

## What this feature deliberately does not do

- **No alerting, no rules engine.** This stores and queries; deciding something
  is wrong is the consumer's job.
- **No cross-metric arithmetic.** A query names one metric.
- **No automatic metric creation policy or cardinality limit** — tag cardinality
  is the caller's responsibility, and it is the usual way to make a time-series
  store unusable.
- **No CLI parity with the API.** Six of the ten capabilities have no CLI verb
  today — see [cli.md](cli.md). That is a gap, not a scope fence.
