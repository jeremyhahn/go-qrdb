# qrdb-sqlite

`qrdb-sqlite` is a SQLite virtual-table gateway for QRDB. It exposes a running
`qrdbd` instance (or an embedded in-process node) as a set of SQLite virtual
tables, letting any SQLite-aware tool — DBeaver, Python `sqlite3`, R, and more —
issue standard SQL against a distributed QRDB cluster with no schema changes and
no driver rewrites.

## Features

- **Two modes**: `embedded` (in-process Raft node) and `client` (remote via gRPC / REST / QUIC / Unix socket / MCP)
- **Seven virtual tables**: `qrdb_kv`, `qrdb_entity`, `qrdb_collection`, `qrdb_ts_datapoints`, `qrdb_ts_metrics`, `qrdb_vector_namespaces`, `qrdb_vectors`
- **Auto-discovery**: collections and entity schemas are introspected at connect time
- **Index pushdown**: point lookups, prefix scans, and range scans are pushed to the QRDB service layer — full-table scans are avoided whenever possible
- **Transaction batching**: `BEGIN` / `COMMIT` blocks are coalesced into a single batch proposal to the Raft leader
- **PRAGMA control**: per-session consistency and write-mode tuning without restarting the gateway
- **All backends**: memory, file, and PebbleDB storage backends
- **All cluster modes**: standalone, local multi-node, and distributed

## Prerequisites

- Go 1.26+ with CGo enabled (`gcc` or `clang` in `$PATH`)
- A running `qrdbd` daemon **or** a valid QRDB YAML config for embedded mode
- `make` (for the standard build targets)

## Documentation

| File | Description |
|---|---|
| [installation.md](installation.md) | Build from source, CGo setup, verification |
| [quickstart.md](quickstart.md) | Connect and run SQL in under five minutes |
| [configuration.md](configuration.md) | All flags, PRAGMAs, and environment variables |
| [architecture.md](architecture.md) | How the gateway works internally |
| [virtual-tables.md](virtual-tables.md) | Schema reference and SQL examples for every table |
| [performance-tuning.md](performance-tuning.md) | Latency guide, consistency tradeoffs, batching |
| [multi-protocol.md](multi-protocol.md) | Using qrdb-sqlite alongside gRPC, REST, and MySQL clients |
| [troubleshooting.md](troubleshooting.md) | Common errors and how to fix them |
