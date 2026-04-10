# Performance Tuning

## Latency Reference

Approximate p50 latency per operation type in a single-node local setup:

| Operation | Embedded | Client (gRPC) | Client (REST) |
|---|---|---|---|
| KV point lookup — eventual | ~50 µs | ~200 µs | ~500 µs |
| KV point lookup — linearizable | ~200 µs | ~400 µs | ~800 µs |
| KV prefix scan (100 rows) | ~500 µs | ~1 ms | ~2 ms |
| Batch write (100 ops) | ~2 ms | ~5 ms | ~10 ms |
| Single write — sync | ~1 ms | ~3 ms | ~6 ms |
| Time-series range (1 000 pts) | ~2 ms | ~6 ms | ~12 ms |

Numbers scale with Raft cluster size, network RTT, and storage backend.

## Consistency Guide

| Level | When to Use | Trade-off |
|---|---|---|
| `linearizable` (default) | Correctness-critical reads; leader-local | Highest latency; reads go to leader |
| `serializable` | Reads within a known transaction window | Slightly lower latency than linearizable |
| `bounded-staleness` | Analytics, dashboards — stale data acceptable | Low latency; reads from any node |
| `eventual` | Bulk exports, background processing | Lowest latency; may return stale data |

Set per-session:

```sql
PRAGMA qrdb_consistency = 'eventual';
SELECT * FROM qrdb_kv WHERE key LIKE 'report:%';
PRAGMA qrdb_consistency = 'linearizable';
```

Or globally via flag: `--consistency eventual`.

## Write Mode Guide

| Mode | When to Use | Durability |
|---|---|---|
| `leader` (default) | Standard writes; all writes go to leader | Full Raft commit before ack |
| `local` | Low-latency writes; forwarded internally | Forwarded — same durability, lower RTT on followers |

> Note: `wal_durable` and `batched` are session-level write modes set via
> `PRAGMA qrdb_write_mode` and map to SDK transport options.

## Transaction Batching

The single biggest latency lever for bulk writes is using `BEGIN` / `COMMIT`.
Without a transaction, each `INSERT` / `UPDATE` / `DELETE` is an independent
Raft proposal — one network round-trip per mutation.

```sql
-- Slow: 1 000 individual Raft proposals
INSERT INTO qrdb_kv (key, value) VALUES ('k1', 'v1');
INSERT INTO qrdb_kv (key, value) VALUES ('k2', 'v2');
-- ... 998 more

-- Fast: 1 batch proposal for all 1 000 mutations
BEGIN;
INSERT INTO qrdb_kv (key, value) VALUES ('k1', 'v1');
INSERT INTO qrdb_kv (key, value) VALUES ('k2', 'v2');
-- ... 998 more
COMMIT;
```

Observed throughput improvement: **10-50x** depending on individual operation
latency and cluster size.

## Prefetch Chunk Size

`--chunk-size` (default 256) controls how many rows are fetched from the service
layer per cursor page. Increase for large scans; decrease to reduce memory
pressure on small queries.

```sh
# Large export — reduce round-trips
bin/qrdb-sqlite shell --chunk-size 1024 ...

# Tight memory budget
bin/qrdb-sqlite shell --chunk-size 64 ...
```

## Coalescing Transport

`--coalescing` wraps the SDK transport with a coalescing layer that batches
concurrent small requests from multiple goroutines into single network calls.
Effective when the gateway is shared by multiple concurrent SQLite connections.

```sh
bin/qrdb-sqlite shell --coalescing ...
```

## Backend Performance Comparison

| Backend | Throughput | Latency | Durability | Best For |
|---|---|---|---|---|
| `memory` | Highest | Lowest | None (lost on restart) | Tests, ephemeral caches |
| `file` | Medium | Medium | Fsync on commit | Development, small datasets |
| `pebble` | High | Low | LSM-tree WAL | Production, large datasets |

## Anti-Patterns

- **Full scans on large tables**: always filter by indexed columns (`key=`, `key LIKE 'p%'`, `metric=`).
- **Single-row inserts in a loop without a transaction**: wrap in `BEGIN`/`COMMIT`.
- **Linearizable consistency for analytics**: switch to `eventual` for read-heavy reporting workloads.
- **Large chunk sizes on memory-constrained hosts**: keep `--chunk-size` proportional to available heap.
- **REST transport for high-throughput workloads**: prefer gRPC or QUIC for lower serialization overhead.
