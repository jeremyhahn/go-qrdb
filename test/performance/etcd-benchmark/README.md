# QRDB etcd API vs etcd Performance Benchmarks

Apples-to-apples comparison of QRDB's etcd v3 compatibility layer against a
standalone etcd server. Both targets are exercised using the etcd v3 client
library over gRPC with mTLS:

| Parameter | Value |
|---|---|
| Payload Size | 128 bytes |
| Concurrency | 8 goroutines |
| TLS | mTLS (ECDSA P-256, TLS 1.3) |
| Seed Pool | 10,000 keys |
| etcd Version | v3.5.17 |
| QRDB Backend | PebbleDB (MVCC) |

Each benchmark is run across the following scenarios:

- **PUT** -- Sequential / Concurrent/8
- **GET** -- Sequential / Concurrent/8
- **DELETE** -- Sequential / Concurrent/8
- **TXN** -- Sequential (compare-and-swap)
- **Watch Latency** -- put-to-event delivery

## Running

```bash
# Full comparison (QRDB etcd API vs real etcd)
make bench-etcd-compare

# QRDB etcd API only
make bench-etcd-qrdb

# Real etcd server only
make bench-etcd-server

# With custom benchtime
make bench-etcd-compare KV_BENCHTIME=30s
```

Or via kvcompare directly:

```bash
cd test/performance/cmd/kvcompare
go run . --benchtime=5s etcd-compare
```

## Architecture Notes

**QRDB mode** -- the benchmark binary starts QRDB internally, creates a
database with the etcd protocol enabled, and connects to it via gRPC. No
external process is required.

**External mode** -- the benchmark binary connects to a pre-started etcd
server (via docker-compose). The etcd container must be running before
invoking the target.

Both modes use the same `go.etcd.io/etcd/client/v3` library so the client
path is identical. Results are emitted as `perfresult` JSON and aggregated
for side-by-side comparison.
