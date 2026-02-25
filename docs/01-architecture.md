<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# QRDB Architecture Overview

## 1. Overview

QRDB (QuicRaft DB) is a distributed transactional key-value store in the
same class as etcd: it provides MVCC versioning, compare-and-swap
transactions, distributed leases, watch notifications, and linearizable
reads -- all replicated through multi-group Raft consensus with QUIC
transport.

QRDB sits in the middle tier of a three-project stack:

- **go-quicraft** -- the consensus engine (Raft protocol, WAL, QUIC
  transport, state machine interfaces, session deduplication)
- **go-qrdb** -- the transactional KV layer (MVCC, transactions, leases,
  watches, secondary indexes, time-series, REST + gRPC APIs)
- **go-dragondb** -- the database layer (SQL, schemas, query planning --
  future consumer of go-qrdb as its storage engine)

QRDB depends on go-quicraft via a local `replace` directive and exposes
its own service interfaces that higher-level consumers (go-dragondb, CLI
tools, application code) program against.

Source: `go.mod`

```
module github.com/jeremyhahn/go-qrdb

go 1.26

require github.com/jeremyhahn/go-quicraft v0.0.0
replace github.com/jeremyhahn/go-quicraft => ../go-quicraft
```

## 2. Three-Tier Architecture

```
+---------------------------------------------------------------+
|                          go-qrdb                               |
|                                                               |
|   +----------+  +--------+  +-------+  +-------+  +--------+ |
|   |   MVCC   |  | Watch  |  | Lease |  | Index |  |   TS   | |
|   +----------+  +--------+  +-------+  +-------+  +--------+ |
|   |  Transactions  |  Batch  |  Consistency  |  Protocol    | |
|   +-----------+----+---------+---------+-----+--------------+ |
|               |          Service Layer         |              |
|               +--------------------------------+              |
|               |  REST API  |   gRPC API  |                    |
|               +--------------------------------+              |
+---------------------------------------------------------------+
                              |
                    sm.DiskStateMachine
                              |
+---------------------------------------------------------------+
|                        go-quicraft                             |
|                                                               |
|   +--------+  +-----------+  +--------+  +----------+        |
|   |  Raft  |  | QUIC Txp  |  |  WAL   |  | Sessions |        |
|   +--------+  +-----------+  +--------+  +----------+        |
|   |  Engine Pipeline  |  Crypto (Barrier)  |  Discovery  |    |
|   +-------------------+-------------------+--------------+    |
+---------------------------------------------------------------+
```

The boundary between go-quicraft and go-qrdb is the `sm.DiskStateMachine`
interface. QRDB implements this interface in two forms:

- `PebbleStore` (`pkg/store/pebble.go`) -- a basic KV state machine for
  simple put/get/delete/list operations
- `MVCCStateMachine` (planned in `pkg/store/`) -- an MVCC-aware state
  machine that dispatches extended protocol op codes

Both are selectable at startup via the `raft.sm_type` configuration field
(`"pebble"`, `"memory"`, or `"mvcc"`).

## 3. Package Structure

```
github.com/jeremyhahn/go-qrdb/
  cmd/qrdbd/                   Server daemon (cobra commands: start, status, peers, tls)
  pkg/
    protocol/                  Extended wire protocol (MVCC, txn, lease, batch, index, TS op codes)
    mvcc/                      MVCC engine (revision tracking, key encoding, compaction)
    watch/                     Real-time change notifications (event fan-out, prefix matching)
    consistency/               Consistency level routing (strong, linearizable, eventual)
    lease/                     Distributed lease management (TTL, expiry, keep-alive)
    index/                     Secondary indexing (unique, sparse, PebbleDB + memory backends)
    timeseries/                Time-series storage (composite keys, aggregation, retention)
    service/                   Service layer interfaces + implementations
      service.go                 KVService, AdminService, Host interfaces
      mvcc_service.go            MVCCKVService interface
      transaction_service.go     TransactionService interface
      watch_service.go           WatchService interface
      lease_service.go           LeaseService interface
      batch_service.go           BatchService interface
      index_service.go           IndexService interface
      timeseries_service.go      TimeSeriesService interface
      types.go                   Shared types (KeyValue, Event, Lease, Compare, Op, etc.)
      errors.go                  Typed service errors
    store/                     Storage backends
      pebble.go                  PebbleStore (sm.DiskStateMachine, PebbleDB-backed)
      factory.go                 Store factory (NewPebbleCreateFunc with functional options)
      errors.go                  Store-specific error types
    config/                    YAML configuration loading and validation
      config.go                  Config struct, DefaultConfig(), Load(), Validate()
      errors.go                  Config-specific error types
    api/
      rest/                    REST API (HTTP/2 + HTTP/3, leader forwarding, Prometheus)
        server.go                Server struct, route registration, TLS setup
        handlers.go              HTTP handler functions, map-based KV dispatch
        errors.go                REST-specific error types
      grpc/                    gRPC API (protobuf service, unary interceptors)
        server.go                gRPC server setup
        service.go               gRPC service implementation
        errors.go                gRPC-specific error types
        proto/qrdbv1/            Protobuf definitions and generated code
    server/                    Multi-protocol server orchestrator
      server.go                  Coordinates REST + gRPC + Raft startup/shutdown
  configs/                     YAML configuration files (qrdb.yaml, qrdb-dev.yaml)
  docs/                        Documentation
  test/
    integration/               Integration tests
    performance/               Benchmark tests and performance results
  build/                       Build artifacts, Docker files
```

## 4. Data Flow

### Write Path (Proposal)

```
Client
  |
  |  PUT /kv/mykey  or  gRPC Put(key, value)
  v
REST Server / gRPC Server
  |-- route dispatch (map-based)
  |-- extract key, value from request
  v
Service Layer (KVService.Put)
  |-- encode: kv.EncodePut(key, value)  ->  [op:1][keyLen:2][key][value]
  |-- host.SyncPropose(ctx, shardID, cmd)
  v
go-quicraft Host
  |-- Raft consensus (propose -> replicate -> commit)
  |-- Engine pipeline: step -> commit (WAL) -> apply
  v
State Machine (PebbleStore.Update)
  |-- kv.DecodeCommand(cmd)  ->  {Op: OpPut, Key, Value}
  |-- pebble.Batch.Set(key, value)
  |-- persist lastApplied index
  |-- batch.Commit(pebble.Sync)
  v
Client <- 204 No Content  or  gRPC OK
```

### MVCC Write Path (Extended Protocol)

```
Client
  |
  |  MVCCKVService.PutWithLease(key, value, leaseID)
  v
Service Layer
  |-- encode: protocol.EncodeMVCCPut(key, value, leaseID)
  |--   -> [op:10][keyLen:2][key][leaseID:8][value]
  |-- host.SyncPropose(ctx, shardID, cmd)
  v
go-quicraft Host
  |-- Raft consensus
  v
MVCCStateMachine.Update
  |-- protocol.DecodeCommand(cmd)  ->  {Op: OpMVCCPut, Payload}
  |-- dispatch via op-code map:
  |     10 -> handleMVCCPut
  |     11 -> handleMVCCDelete
  |     12 -> handleMVCCRange
  |     20 -> handleTransaction
  |     30 -> handleBatch
  |     40 -> handleLeaseGrant
  |     ...
  |-- revision.Add(1)  (atomic.Int64)
  |-- encode MVCC key: [userKey | revisionBytes]
  |-- pebble.Set(mvccKey, mvccValue)
  |-- emit watch events
  v
Client <- KeyValue{Key, Value, CreateRevision, ModRevision, Version}
```

### Read Path (Linearizable)

```
Client
  |
  |  GET /kv/mykey  or  gRPC Get(key)
  v
REST Server / gRPC Server
  |-- route dispatch
  v
Service Layer (KVService.Get)
  |-- host.SyncRead(ctx, shardID, []byte(key))
  v
go-quicraft Host
  |-- ReadIndex protocol:
  |     leader broadcasts heartbeat with readKey
  |     quorum confirms leadership
  |     wait for lastApplied >= commitIndex
  |-- sm.Lookup(ctx, query)
  v
State Machine (PebbleStore.Lookup)
  |-- pebble.Get(key)  ->  value bytes
  |-- return defensive copy
  v
Client <- 200 OK + value bytes  or  gRPC GetResponse{value}
```

### Transaction Path

```
Client
  |
  |  TransactionService.Execute(compares, successOps, failureOps)
  v
Service Layer
  |-- encode compares:  protocol.EncodeCompare(key, target, result, ...)
  |-- encode ops:       protocol.EncodeOp(opType, key, value, rangeEnd, leaseID)
  |-- encode txn:       protocol.EncodeTransaction(compares, successOps, failureOps)
  |--   -> [op:20][compareCount:2][compares...][successCount:2][...][failureCount:2][...]
  |-- host.SyncPropose(ctx, shardID, cmd)
  v
MVCCStateMachine.Update
  |-- decode transaction payload
  |-- evaluate all compares against current MVCC state
  |-- if ALL pass:  apply successOps atomically
  |-- if ANY fail:  apply failureOps atomically
  |-- single revision bump for entire transaction
  v
Client <- TransactionResponse{Succeeded, Revision, Responses}
```

## 5. Internal Dependency Graph

The package dependency graph is strictly acyclic. Arrows represent
import relationships.

```
                              +----------+
                              | protocol |  (stdlib only)
                              +----------+
                              /    |     \
                             /     |      \
                            v      v       v
                      +------+  +-------+  +-------+
                      | mvcc |  | store |  | lease |
                      +------+  +-------+  +-------+
                         |         |           |
                         |    pebble/v2        |
                         |         |           |
          +---------+    |         |     +-----------+
          |  watch  |    |         |     | consistency|  (stdlib only)
          +---------+    |         |     +-----------+
               |         |         |           |
               v         v         v           v
          +-----------------------------------------------+
          |              service (types.go)                |
          |                                               |
          |  KVService        MVCCKVService                |
          |  AdminService     TransactionService           |
          |  WatchService     LeaseService                 |
          |  BatchService     IndexService                 |
          |  TimeSeriesService                             |
          +-----------------------------------------------+
                    |                       |
                    v                       v
          +----------------+      +------------------+
          | api/rest       |      | api/grpc         |
          | (handlers.go)  |      | (service.go)     |
          +----------------+      +------------------+
                    \                      /
                     \                    /
                      v                  v
                  +------------------------+
                  |        server          |
                  | (multi-protocol       |
                  |  orchestrator)        |
                  +------------------------+
                            |
                            v
                  +------------------------+
                  |       cmd/qrdbd        |
                  | (server daemon)        |
                  +------------------------+
```

Key invariant: **no import cycles exist**. Each layer depends only on
layers below it. The `protocol` package depends only on the standard
library. The `service` package defines interfaces and types that bridge
the gap between transport-specific API handlers and storage backends.

Package dependency summary:

| Package       | Imports from go-qrdb             | External        |
|---------------|----------------------------------|-----------------|
| `protocol`    | (none)                           | stdlib only     |
| `mvcc`        | `protocol`                       | `pebble/v2`     |
| `watch`       | `service` (types only)           | stdlib only     |
| `consistency` | (none)                           | stdlib only     |
| `lease`       | `service` (types only)           | stdlib only     |
| `index`       | `service` (types only)           | `pebble/v2`     |
| `timeseries`  | `service` (types only)           | stdlib only     |
| `store`       | `protocol` (via kv)              | `pebble/v2`, `xxhash` |
| `service`     | (defines interfaces)             | `go-quicraft`   |
| `api/rest`    | `service`                        | `prometheus`    |
| `api/grpc`    | `service`                        | `grpc`, `protobuf` |
| `server`      | `service`, `config`, `api/*`     | `prometheus`    |
| `config`      | (none)                           | `yaml.v3`       |
| `cmd/qrdbd`   | `server`, `config`, `store`      | `cobra`         |

## 6. Key Design Decisions

- **MVCC as a separate package with its own PebbleDB handle.** The MVCC
  engine manages revision-keyed data independently from the base KV
  store, enabling distinct compaction policies and key encoding schemes
  without coupling the two storage paths.

- **MVCCStateMachine coexists with PebbleStore (selectable via `sm_type`).**
  Operators choose `"pebble"` for a simple KV workload (base op codes 1-3)
  or `"mvcc"` for the full transactional feature set (extended op codes
  10+). Both implement `sm.DiskStateMachine` and are registered via the
  same factory pattern.

  Source: `pkg/config/config.go` -- `validSMTypes`

- **Interface segregation across service boundaries.** Each feature domain
  has its own service interface (`KVService`, `MVCCKVService`,
  `TransactionService`, `WatchService`, `LeaseService`, `BatchService`,
  `IndexService`, `TimeSeriesService`, `AdminService`). API handlers
  depend only on the interfaces they need, enabling independent testing
  and clean compile-time contracts.

  Source: `pkg/service/*.go`

- **Extended wire protocol defined in go-qrdb, not go-quicraft.** The base
  KV protocol (op codes 1-3: `OpPut`, `OpGet`, `OpDelete`) lives in
  go-quicraft's `pkg/sm/kv` package. QRDB extends it with higher-numbered
  op codes (10-69) for MVCC, transactions, leases, batches, indexes, and
  time-series operations. This keeps the consensus library generic while
  allowing QRDB to define its own domain-specific wire format.

  Source: `pkg/protocol/protocol.go` -- op code constants and encoders

- **Watch events generated inside state machine `Update()`.** Events are
  emitted synchronously during `Update` processing, guaranteeing they
  reflect the exact committed state at the revision they describe. This
  avoids race conditions between event emission and state visibility.

- **Lock-free revision counter (`atomic.Int64`).** The global MVCC
  revision is incremented atomically without mutex contention. Since
  `Update()` is called serially by the engine apply worker (one shard at
  a time), the atomic provides ordering guarantees without the overhead
  of a full lock.

- **Map-based dispatch in state machine and API routing.** Both the REST
  API handler (`kvMethodHandlers` map in `pkg/api/rest/handlers.go`) and
  the MVCC state machine use `map[byte]handler` dispatch tables instead
  of `switch` statements. This follows the project convention of
  preferring map-based dispatch for extensibility and readability.

  Source: `pkg/api/rest/handlers.go` -- `kvMethodHandlers`

- **Service layer as the critical abstraction.** All protocol handlers
  (REST, gRPC) delegate to service interfaces. The service layer
  translates between the API-facing types and the QuicRaft host
  operations, handling error mapping (`NotLeaderError` -> service-level
  `NotLeaderError`), protocol encoding, and business logic. No Raft
  operations leak into the API layer.

  Source: `pkg/service/service.go` -- `kvService`, `adminService`

- **Functional options pattern for server configuration.** The REST
  server, gRPC server, multi-protocol orchestrator, and PebbleStore
  factory all use the functional options pattern (`WithTLS`,
  `WithGatherer`, `WithCacheSize`, etc.) for optional dependency
  injection.

  Source: `pkg/server/server.go` -- `ServerOption`
  Source: `pkg/store/factory.go` -- `Option`

- **Typed errors only.** The codebase never uses `fmt.Errorf`. Every error
  is a named struct type with an `Error() string` method, enabling
  callers to use `errors.As` for precise error handling. Error types are
  co-located with their packages (`pkg/service/errors.go`,
  `pkg/store/errors.go`, `pkg/api/rest/errors.go`, etc.).

## Cross-References

- [API Reference](api.md) -- REST and gRPC endpoint specifications
- [MVCC Engine](02-mvcc.md) -- revision tracking internals
- [Transactions](03-transactions.md) -- compare-and-swap semantics
- [Configuration](12-configuration.md) -- complete YAML reference
- [Operations](13-operations.md) -- deployment and monitoring
