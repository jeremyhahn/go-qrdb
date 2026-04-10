# Architecture

## Component Overview

```
┌─────────────────────────────────────────────────────────────────┐
│  SQLite engine (mattn/go-sqlite3)                               │
│                                                                  │
│  SELECT * FROM qrdb_kv WHERE key LIKE 'user:%'                  │
│         │                                                        │
│         ▼                                                        │
│  ┌─────────────┐   BestIndex()   ┌──────────────────────────┐  │
│  │ virtual tab │ ──────────────► │ index cost estimator     │  │
│  │  module     │                 │ (point / prefix / scan)  │  │
│  │  (vtab/)    │ ◄────────────── │                          │  │
│  └──────┬──────┘   idxNum        └──────────────────────────┘  │
│         │                                                        │
│         │ xFilter(idxNum, args)                                  │
│         ▼                                                        │
│  ┌──────────────┐  prefetch   ┌────────────────────────────┐   │
│  │  cursor      │ ──────────► │  QRDB SDK client           │   │
│  │  (Scan/Next) │             │  (sdk/go/client.go)        │   │
│  └──────────────┘             └──────────┬─────────────────┘   │
└─────────────────────────────────────────┼───────────────────────┘
                                           │ transport
                    ┌──────────────────────┼──────────────────────┐
                    │  Transport layer      │                       │
                    │                       ▼                       │
                    │  gRPC / REST / QUIC / Unix / MCP             │
                    └──────────────────────┬───────────────────────┘
                                           │
                    ┌──────────────────────┼──────────────────────┐
                    │  qrdbd (or embedded) │                       │
                    │         ▼            ▼                       │
                    │  ┌─────────────────────────┐                │
                    │  │  Service layer           │                │
                    │  │  (pkg/service/)          │                │
                    │  └──────────┬──────────────┘                │
                    │             │                                 │
                    │  ┌──────────▼──────────────┐                │
                    │  │  Raft state machine      │                │
                    │  │  (pkg/store/)            │                │
                    │  └──────────┬──────────────┘                │
                    │             │                                 │
                    │  ┌──────────▼──────────────┐                │
                    │  │  Storage engine          │                │
                    │  │  memory / file / pebble  │                │
                    │  └─────────────────────────┘                │
                    └─────────────────────────────────────────────┘
```

## Read Data Flow

1. SQLite calls `BestIndex` on the virtual table module; the module inspects
   constraints and returns an `idxNum` (full scan, point lookup, prefix/range scan)
   along with an estimated row cost.
2. SQLite calls `xFilter` with the chosen `idxNum` and constraint argument values.
3. The cursor issues a prefetch request to the QRDB SDK using the appropriate
   service call (`Get`, `Scan`, `Range`) with the active consistency level from
   `PragmaStore`.
4. Rows arrive in chunks of `--chunk-size` and are buffered in the cursor. SQLite
   advances the cursor via repeated `xNext` / `xColumn` calls.

## Write Data Flow

1. `INSERT`, `UPDATE`, and `DELETE` invoke `xUpdate` on the virtual table module.
2. Outside a transaction, each mutation is forwarded immediately as a single SDK
   call to the service layer, which proposes it to the Raft leader.
3. Inside a `BEGIN` / `COMMIT` block, mutations accumulate in a `txBuffer` held by
   the transaction coordinator (`vtab/transaction.go`). On `COMMIT`, the buffer is
   flushed as a single batch proposal, reducing round-trips and leader log entries.

## Virtual Table to Service Mapping

| Virtual Table | QRDB Service | Index Types |
|---|---|---|
| `qrdb_kv` | KV service | full scan, point lookup (`key=`), prefix scan (`LIKE 'p%'`) |
| `qrdb_entity` | Entity / MVCC service | full scan, `_id=`, equality, range |
| `qrdb_collection` | Collection service | full scan, `_id=` point lookup |
| `qrdb_ts_datapoints` | Time-series service | full scan, metric+range, metric-only |
| `qrdb_ts_metrics` | Time-series service | full scan |
| `qrdb_vector_namespaces` | Vector service | full scan |
| `qrdb_vectors` | Vector service | full scan, id+namespace composite, namespace scan |

## Transaction Semantics

SQLite's `BEGIN` / `COMMIT` maps to qrdb's batch service. The gateway intercepts
`xBegin` / `xCommit` on the virtual table connection and hands a `txBuffer` to
every `xUpdate` call within that transaction. `ROLLBACK` discards the buffer
without sending any proposals to the Raft leader.

Nested `SAVEPOINT` is not currently supported.

## Embedded vs Client Mode

In **client mode** the gateway process holds only the SQLite engine and SDK
client; all data lives in the remote `qrdbd` cluster.

In **embedded mode** the gateway calls `server.New(cfg)` during `PersistentPreRunE`
to start a full Raft node in-process. The SDK client then communicates with that
node over a loopback Unix socket, keeping the service layer code path identical
between modes.
