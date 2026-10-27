# Time Series — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

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

**The `duration_ns` half is enforced; `resolution_ns` downsampling
is not.** See "Retention enforcement" below.

## Current state

- **Storage-generic.** `timeseries.Storage`
  wraps `storage.Storage`, not a raw `*pebble.DB`, and is available on every
  backend MVCCEngine composes over — memory, file, pebble and objstore —
  not only on a pebble-native state machine or a pebble standalone node.
  `pkg/timeseries` does not import Pebble, enforced by
  `test/buildgates/no_pebble_dependency_test.go`.
- **Inside the apply batch.** The engine's time-series
  handlers stage points, the metric index and the tag index into the
  caller's apply `storage.Batch` instead of writing straight to Pebble with
  their own synchronous writes. A time-series write is therefore atomic
  with the rest of its Raft entry: if the entry's batch fails, neither
  survives (`pkg/engine.TestMVCCEngine_TSAndKVWriteInOneApplyBatch_CommitFailureLeavesNeither`).
- **Per database by metric prefix.** In a shared store a database's series are
  distinguished by a `<tenant>/<db>/` prefix on the metric name. The tag index
  (`idx:tag:`) carries no metric, so no database owns it and a database drop
  cannot reclaim it.

## Time series in the database's storage

**Shipped.**

- **Over the storage interface.** `pkg/timeseries` reads through
  `storage.Reader` and writes into the apply `storage.Batch`. It does not
  import Pebble. Points, the metric index, the tag index and retention records
  (`SetRetentionPolicy`) commit with the entry that wrote them, or not at all.
- **Every backend MVCCEngine composes over**: memory, file, pebble and
  objstore. On objstore a range query is an ordered object listing, so it is
  correct and slower.
- **Retention deletion**, described in "Retention enforcement" below.

**Not yet built.**

- **The per-database raft binding.** Time series is not yet stored in a
  [storage binding](../databases/architecture.md#the-storage-binding) of the
  database's own, proposed to the database's Raft group under its drop fence.
  In such a store metric names would carry no `<tenant>/<db>/` prefix, the tag
  index would belong to the database, and a drop would destroy both; the key
  layout (`ts:`, `idx:metric:`, `idx:tag:`, `retention:`) and the `AggFunc`
  numbering stay as they are.
- **Downsampling** to `resolution_ns`.
- **A per-command, engine-level drop fence** on the retention command.

### Retention enforcement

A retention sweep changes stored data, so it must be the same on every replica.
It is therefore a command, not a background delete:

- A leader-driven scheduler (`pkg/server`'s `retentionScheduler`, the same
  per-tick, leader-gated pattern as the lease-expiry sweeper and the
  compaction scheduler) runs on every node and acts on the stores it leads
  or holds alone. Every `DefaultRetentionInterval` (one minute) it lists a
  store's written metrics and, for each with a policy, computes `cutoff_ns`
  from its own clock (`now - duration_ns`) and proposes one
  `protocol.OpTSApplyRetention` command (metric, cutoff) through the store's
  `TimeSeriesServicer` (`ApplyRetention`).
- The engine applies it deterministically from `cutoff_ns` alone: points
  older than `cutoff_ns` are deleted (`timeseries.Storage.ApplyRetention`,
  into the caller's `storage.Batch`, identically on every backend).
  Replicas never consult their own clocks. **Downsampling is not
  implemented** — a policy with a `resolution_ns` still has its expired
  points deleted outright, not replaced by one bucketed point; the policy's
  `aggregation` is read back by `TSGetRetention` but nothing computes it.
- The command is behind the cluster feature gate
  (`featuregate.TimeSeriesRetention`): the scheduler's `ApplyRetention` call
  is refused with `*featuregate.NotActiveError` until every member of the
  group supports the opcode.
- The command is bounded per entry (`timeseries.MaxRetentionDeletesPerApply`);
  a metric with more expired points than one command's bound is swept over
  several ticks. On a shared store a command is guarded by the owning
  database's drop fence, because it is proposed through that database's
  namespaced `TimeSeriesServicer` view like every other write of that
  database's; a dedicated per-database raft shard or a per-tenant volume
  host needs no such guard.

### Alternatives rejected

- **Keep time series on Pebble and give other backends a separate time-series
  store.** Rejected: two implementations of one engine, and the non-Pebble one
  would still be outside the apply batch.
- **A replica-local retention sweep.** Rejected: replicas would delete different
  points at different times, and a follower read would disagree with the
  leader.

## Trust boundaries

- Metrics are scoped per (tenant, database) like every other data-plane object.
- Ten capabilities, ten permissions (`timeseries:write`,
  `timeseries:query`, `timeseries:delete_series`, `timeseries:set_retention`, …).
  Setting retention is destructive over time and is a distinct grant from
  writing.
- Points are encrypted at rest under the store's encryptor and fail closed
  when that barrier is sealed. The store is the shared system store or a volume
  or raft tenant's store; the per-database binding is not yet built.

## What this feature deliberately does not do

- **No alerting, no rules engine.** This stores and queries; deciding something
  is wrong is the consumer's job.
- **No cross-metric arithmetic.** A query names one metric.
- **No automatic metric creation policy or cardinality limit** — tag cardinality
  is the caller's responsibility, and it is the usual way to make a time-series
  store unusable.
- **No CLI parity with the API.** Six of the ten capabilities have no CLI verb
  — see [cli.md](cli.md). That is a gap, not a scope fence.
