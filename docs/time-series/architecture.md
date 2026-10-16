# Time Series — Architecture

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-06
>
> "Target" sections describe the time-series half of the multi-model database
> program ([../databases/architecture.md](../databases/architecture.md)); they
> are approved and not implemented. OD-n refers to that document's ruled decisions.

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

**Measured: the `duration_ns` half is enforced; `resolution_ns` downsampling
is not.** `TSSetRetention` persists the policy and `TSGetRetention` returns
it. A leader-driven scheduler in `pkg/server` (`retention_scheduler.go`)
sweeps each store once a minute and proposes one `TSApplyRetention`
command per metric with a policy, deleting points older than
`duration_ns` deterministically on every replica
(`timeseries.Storage.ApplyRetention`). No code downsamples an expired
point into one `resolution_ns` bucket yet — that half, and the full
engine-over-storage-interface migration below, remain this program
(OD-16, ruled A); see "Retention enforcement" below.

## Current state (measured)

- **Storage-generic (Lane 1A, shipped 2026-10-06).** `timeseries.Storage`
  wraps `storage.Storage`, not a raw `*pebble.DB`, and is available on every
  backend MVCCEngine composes over — memory, file, pebble and objstore —
  not only on a pebble-native state machine or a pebble standalone node.
  `pkg/timeseries` imports no storage engine, enforced by `test/buildgates`.
- **Inside the apply batch (Lane 1A, shipped).** The engine's time-series
  handlers stage points, the metric index and the tag index into the
  caller's apply `storage.Batch` instead of writing straight to Pebble with
  their own synchronous writes. A time-series write is therefore atomic
  with the rest of its Raft entry: if the entry's batch fails, neither
  survives (`pkg/engine.TestMVCCEngine_TSAndKVWriteInOneApplyBatch_CommitFailureLeavesNeither`).
- **Per database by metric prefix.** In a shared store a database's series are
  distinguished by a `<tenant>/<db>/` prefix on the metric name. The tag index
  (`idx:tag:`) carries no metric, so no database owns it and a database drop
  cannot reclaim it.
- **Retention deletion is enforced; downsampling is not.** A leader-driven
  scheduler in `pkg/server` proposes a bounded `TSApplyRetention` command per
  metric per tick; see "Retention enforcement" below.

## Target design — time series as an engine of the database binding

Time series is an engine of the database, stored in the database's
[storage binding](../databases/architecture.md#the-storage-binding) like every
other engine.

- **Over the storage interface — shipped (Lane 1A).** `pkg/timeseries` reads
  through `storage.Reader` and writes into the apply `storage.Batch`. It
  imports no storage engine. Points, the metric index and the tag index
  commit with the entry that wrote them, or not at all. Retention records
  (`SetRetentionPolicy`) are likewise batch-staged; only the per-database
  *binding* below — a store of the database's own rather than the shared
  store — remains target work.
- **Every backend — shipped for memory, file, pebble and objstore (Lane
  1A); raft remains target**, pending the per-database binding below. On
  objstore a range query is an ordered object listing, so it is correct and
  slower; that is the cost of the backend the database chose.
- **Replicated with the database.** On a raft binding every write is proposed to
  the database's Raft group and carries the database's drop fence, like every
  other write.
- **Tenant scoped by construction.** The store belongs to one database of one
  tenant and is encrypted under the binding's encryptor, so metric names carry
  no database prefix and the tag index belongs to the database. A drop destroys
  the store and everything in it, the tag index included.
- **Key layout unchanged inside the store** — `ts:`, `idx:metric:`,
  `idx:tag:`, `retention:` — minus the `<tenant>/<db>/` metric prefix, which a
  store of one's own does not need. The encoding and the `AggFunc` numbering
  remain wire and disk format.

### Retention enforcement (OD-16 A)

> **Status: the deletion half is implemented against the storage-generic,
> in-apply-batch architecture shipped by Lane 1A** (`ApplyRetention` takes
> the caller's `storage.Batch` and works identically on every backend).
> Downsampling, and a universal per-database drop fence on the command
> itself — which depends on the per-database binding, not yet shipped —
> remain target work.

A retention sweep changes stored data, so it must be the same on every replica.
It is therefore a command, not a background delete:

- A leader-driven scheduler (`pkg/server`'s `retentionScheduler`, the same
  per-tick, leader-gated pattern as the lease-expiry sweeper and the
  compaction scheduler) runs on every node and acts on the stores it leads
  or holds alone. Each tick it lists a store's written metrics and, for each
  with a policy, computes `cutoff_ns` from its own clock
  (`now - duration_ns`) and proposes one `TSApplyRetention{metric,
  cutoff_ns}` command through the store's `TimeSeriesServicer`.
- The engine applies it deterministically from `cutoff_ns` alone: points
  older than `cutoff_ns` are deleted (`timeseries.Storage.ApplyRetention`).
  Replicas never consult their own clocks. **Measured: downsampling is not
  implemented** — a policy with a `resolution_ns` still has its expired
  points deleted outright, not replaced by one bucketed point; the policy's
  `aggregation` is read back by `TSGetRetention` but nothing computes it yet.
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
  host needs no such guard; the target's own per-command, engine-level
  fence remains unimplemented.

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
  when that barrier is sealed. Shipped, the store is the shared system store or
  a volume or raft tenant's store; target, it is the database's binding, under
  the tenant's key.

## What this feature deliberately does not do

- **No alerting, no rules engine.** This stores and queries; deciding something
  is wrong is the consumer's job.
- **No cross-metric arithmetic.** A query names one metric.
- **No automatic metric creation policy or cardinality limit** — tag cardinality
  is the caller's responsibility, and it is the usual way to make a time-series
  store unusable.
- **No CLI parity with the API.** Six of the ten capabilities have no CLI verb
  today — see [cli.md](cli.md). That is a gap, not a scope fence.
