# Configuration

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--mode` | `client` | Operating mode: `embedded` or `client` |
| `--server` | — | qrdbd address (required for client mode). See address formats below. |
| `--database` | — | QRDB logical database name to expose as virtual tables |
| `--config` | — | Path to qrdb YAML config file (required for embedded mode) |
| `--transport` | auto | Force a specific transport: `grpc`, `rest`, `unix`, `mcp`, `quic`. Overrides scheme auto-detection from `--server`. |
| `--consistency` | `linearizable` | Default read consistency: `linearizable`, `serializable`, `bounded-staleness`, `eventual` |
| `--write-mode` | `leader` | Write routing: `leader` (all writes go to Raft leader) or `local` (forward internally) |
| `--coalescing` | `false` | Wrap transport with coalescing layer to batch concurrent small requests |
| `--chunk-size` | `256` | Number of rows prefetched per virtual-table scan page |
| `--debug` | `false` | Enable verbose debug logging to stderr |

## PRAGMA Reference

PRAGMAs change session-level settings without restarting the gateway. They take
effect immediately for subsequent queries.

```sql
PRAGMA qrdb_consistency = 'eventual';
PRAGMA qrdb_write_mode  = 'batched';
PRAGMA qrdb_status;
```

| PRAGMA | Values | Description |
|---|---|---|
| `qrdb_consistency` | `linearizable`, `linearizable`, `strong`, `eventual` | Read consistency level for this session |
| `qrdb_write_mode` | `sync`, `wal_durable`, `batched` | Write durability mode for this session |
| `qrdb_status` | (read-only) | Returns current mode, server address, database, consistency, and write mode |

## Environment Variables

Environment variables are read before flag defaults. Explicit flags override them.

| Variable | Corresponding Flag |
|---|---|
| `QRDB_SERVER` | `--server` |
| `QRDB_DATABASE` | `--database` |
| `QRDB_MODE` | `--mode` |
| `QRDB_TRANSPORT` | `--transport` |
| `QRDB_CONSISTENCY` | `--consistency` |
| `QRDB_WRITE_MODE` | `--write-mode` |
| `QRDB_CONFIG` | `--config` |

## Server Address Formats

The `--server` flag accepts a bare `host:port` (defaults to gRPC) or a scheme:

```
localhost:7070               # gRPC (default)
grpc://localhost:7070
rest://localhost:8080
unix:///var/run/qrdbd.sock
quic://cluster.example.com:7070
mcp://localhost:9090
```

When a scheme is present, `--transport` is inferred from it and does not need to
be set explicitly.

## Backend Selection for Embedded Mode

The storage backend is controlled by the YAML config file passed via `--config`.
Set the `storage.engine` field to `memory`, `file`, or `pebble`. Example:

```yaml
# configs/qrdb-dev.yaml
storage:
  engine: pebble
  path: /var/lib/qrdb/data
```

See [docs/12-configuration.md](../../docs/12-configuration.md) for the full
qrdb server configuration reference.

## TLS Configuration

TLS is configured in the qrdb server YAML under `tls`. The gateway inherits the
TLS settings from the config file in embedded mode, or from the server's
certificate trust chain in client mode.

For client mode with mutual TLS:

```sh
bin/qrdb-sqlite shell \
  --server grpc://cluster.example.com:7070 \
  --config configs/qrdb-tls-client.yaml
```
