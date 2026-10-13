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

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

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

go 1.27.2

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
|   |  RBAC  |  Audit  |  Certs  |  Users  |  Capabilities   | |
|   +-----------+-------+---------+---------+-----------------+ |
|               |          Service Layer         |              |
|               +--------------------------------+              |
|   | REST API | gRPC API | Unix API | MCP API | etcd API |     |
|               +--------------------------------+              |
+---------------------------------------------------------------+
                              |
                    sm.DiskStateMachine
                              |
                  +-------------------------+
                  | State Machines           |
                  | (Raft wrappers)          |
                  +-------------------------+
                              |
                  +-------------------------+
                  | Store Engines            |
                  | (MVCCEngine, KVEngine)   |
                  +-------------------------+
                              |
                  +-------------------------+
                  | Storage Backends         |
                  | (memory, file, pebble)   |
                  +-------------------------+
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
interface. QRDB uses a three-layer store architecture:

1. **Storage backends** (`pkg/storage/`) implement the `storage.Storage`
   interface. Three backends are provided: `memory` (in-memory, data lost
   on restart), `file` (filesystem-backed), and `pebble` (PebbleDB-backed,
   production default).

2. **Store engines** (`pkg/engine/`) implement the `Engine` interface
   (extends `backend.SMAdapter`). They contain all handler logic for
   protocol dispatch:
   - `KVEngine` -- basic Get/Put/Delete/List/Scan, optional indexes and watches
   - `MVCCEngine` -- MVCC versioning, transactions, leases, indexes, watches,
     time-series, and extended protocol op codes

3. **State machines** (`pkg/store/`) are thin Raft wrappers that implement
   `sm.DiskStateMachine` and delegate `Update`/`Lookup` calls to an engine.
   They add only: opening PebbleDB, persisting `lastApplied`, and snapshot
   save/restore. `MVCCStateMachine` (`pkg/store/mvcc.go`) is the primary
   Raft state machine.

In **local (non-Raft) mode**, a `LocalBackend` wraps an engine directly
without a state machine, bypassing Raft consensus entirely.

Users choose a **store engine** (`kv` or `mvcc`) and a **storage backend**
(`memory`, `file`, `pebble`) independently. The file backend defaults to
the KV engine because MVCC binary keys are not valid filenames.

The `raft.sm_type` configuration field (`"pebble"`, `"memory"`, or `"mvcc"`)
selects which Raft state machine wrapper to use for distributed mode.

## 3. Package Structure

```
github.com/jeremyhahn/go-qrdb/
  cmd/qrdbd/                   Server daemon (cobra commands: start, status, peers, tls)
  pkg/
    protocol/                  Extended wire protocol (MVCC, txn, lease, batch, index, TS, vector op codes)
    discovery/                 Peer discovery engine (static, multicast, dns, token methods + manager)
    cluster/                   Cluster bootstrap orchestration + opt-in dynamic membership manager
    mvcc/                      MVCC engine (revision tracking, key encoding, compaction)
    watch/                     Real-time change notifications (event fan-out, prefix matching)
    consistency/               Consistency level routing (strong, linearizable, eventual)
    lease/                     Distributed lease management (TTL, expiry, keep-alive)
    index/                     Secondary indexing (unique, sparse, PebbleDB + memory backends)
    timeseries/                Time-series storage (composite keys, aggregation, retention)
    embedding/                 Embedding service provider-proxy (OpenAI-compatible, LRU cache)
    vector/                    Vector database (HNSW indexes, k-NN search, tenant-scoped namespaces)
    rag/                        RAG ingest primitive (chunking, embedding, vector storage)
    service/                   Service layer interfaces + implementations
      service.go                 KVService, AdminService, Host interfaces
      mvcc_service.go            MVCCKVService interface
      transaction_service.go     TransactionService interface
      watch_service.go           WatchService interface
      lease_service.go           LeaseService interface
      batch_service.go           BatchService interface
      index_service.go           IndexService interface
      timeseries_service.go      TimeSeriesService interface
      vector_service.go          VectorService interface (namespace CRUD, k-NN search, metadata filtering)
      embedding_service.go       EmbeddingService interface (provider-proxy, batch processing, caching)
      rag_service.go             RAGService interface (document ingest, query, deletion)
      types.go                   Shared types (KeyValue, Event, Lease, Compare, Op, etc.)
      errors.go                  Typed service errors
    storage/                   Storage backend implementations
      storage.go                 storage.Storage interface
      memory/                    In-memory storage backend (data lost on restart)
      file/                      Filesystem-backed storage backend
      pebble/                    PebbleDB-backed storage backend (production default)
      namespace/                 Namespace-scoped storage wrapper
    engine/                    Store engine implementations
      kv_engine.go               KVEngine (basic KV ops, optional indexes/watches)
      mvcc_engine.go             MVCCEngine (MVCC, transactions, leases, indexes, watches, time-series)
    store/                     Raft state machine wrappers
      mvcc.go                    MVCCStateMachine (sm.DiskStateMachine, delegates to engine)
      pebble.go                  PebbleStateMachine (sm.DiskStateMachine, legacy wrapper)
      factory.go                 Store factory (functional options)
      errors.go                  Store-specific error types
    config/                    YAML configuration loading and validation
      config.go                  Config struct, DefaultConfig(), Load(), Validate()
      errors.go                  Config-specific error types
    capabilities/            Compile-time feature registration (build-tag gated)
    permission/              Fine-grained resource:action permission registry
    user/                    Passwordless mTLS user management (KVStore-backed)
    role/                    RBAC role management with built-in roles
    cert/                    Pluggable CertProvider (default: barrier-sealed ECDSA CA)
    auth/
      rbac/                  RBAC authorizer with permission cache
      middleware/            Auth chain: gRPC interceptors + REST middleware
    audit/                   Dual-backend audit logging (KVStore + slog JSON file)
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
      unix/                    gRPC over Unix domain socket
      mcp/                     MCP (Model Context Protocol) AI/LLM tool server
      etcd/                    etcd v3 gRPC compatibility (per-database endpoint)
    server/                    Multi-protocol server orchestrator
      server.go                  Coordinates REST + gRPC + Raft startup/shutdown
  cmd/qrdb/                    Client CLI + Desktop GUI (Wails v2 + Svelte 5)
    frontend/                  Svelte 5 frontend (Vite + TailwindCSS + DaisyUI)
  sdk/
    go/                        Go SDK with 5 transports
    typescript/                TypeScript SDK (@qrdb/sdk, WailsClient + RestClient)
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
State Machine (MVCCStateMachine.Update)
  |-- delegates to Engine.Update(cmd)
  v
Engine (KVEngine or MVCCEngine)
  |-- kv.DecodeCommand(cmd)  ->  {Op: OpPut, Key, Value}
  |-- storage.Put(key, value)
  v
Storage Backend (memory, file, or pebble)
  |-- persist data
  v
State Machine
  |-- persist lastApplied index
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
  |-- delegates to MVCCEngine.Update(cmd)
  v
MVCCEngine
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
  |-- storage.Put(mvccKey, mvccValue)
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
State Machine (MVCCStateMachine.Lookup)
  |-- delegates to Engine.Lookup(query)
  v
Engine -> Storage Backend
  |-- storage.Get(key)  ->  value bytes
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
| `storage`     | (none)                           | `pebble/v2`     |
| `engine`      | `protocol`, `storage`, `mvcc`    | stdlib only     |
| `store`       | `engine`, `storage`              | `pebble/v2`     |
| `service`     | (defines interfaces)             | `go-quicraft`   |
| `api/rest`    | `service`                        | `prometheus`    |
| `api/grpc`    | `service`                        | `grpc`, `protobuf` |
| `api/etcd`    | `service`                        | `grpc`, `etcd/api` |
| `server`      | `service`, `config`, `api/*`     | `prometheus`    |
| `config`      | (none)                           | `yaml.v3`       |
| `cmd/qrdbd`   | `server`, `config`, `store`      | `cobra`         |
| `sdk/go/node` | `server`, `config`, `store`      | (none)           |

## 5.5. Daemon and Embedded Sibling Composition

Both the standalone daemon (`cmd/qrdbd`) and the embedded SDK node (`sdk/go/node`)
are sibling composition roots that call `server.New()` over the shared `pkg/server`
core. The daemon is **not** built on the node; rather, both are built on the same
foundation.

A new compositor, `pkg/server.BuildTenantInfra(TenantInfraConfig)`, is used by
both roots to wire the per-tenant infrastructure (tenant host registry, Raft backend
factory, encryption migrator). This converges daemon and embedded paths on the same
code for per-tenant features, enabling the embedded node to offer daemon-parity
capabilities like tenant-scoped enable-encryption via `node.WithTenantIsolation()`
and `node.EnableEncryption()`.

## 6. Key Design Decisions

- **MVCC as a separate package with its own PebbleDB handle.** The MVCC
  engine manages revision-keyed data independently from the base KV
  store, enabling distinct compaction policies and key encoding schemes
  without coupling the two storage paths.

- **Three-layer store architecture with independent engine and backend selection.**
  The store layer is split into storage backends (`pkg/storage/`), store
  engines (`pkg/engine/`), and thin Raft state machine wrappers (`pkg/store/`).
  Operators choose a store engine (`kv` for simple put/get/delete with base
  op codes 1-3, or `mvcc` for the full transactional feature set with
  extended op codes 10+) and a storage backend (`memory`, `file`, `pebble`)
  independently. State machines delegate all data operations to engines,
  adding only Raft lifecycle concerns (open, lastApplied, snapshots).
  In local (non-Raft) mode, a `LocalBackend` wraps an engine directly.

  Source: `pkg/engine/`, `pkg/storage/`, `pkg/store/`

- **The base KV opcode set is a contract every state machine must satisfy in
  full.** `kv.OpPut`, `kv.OpDelete` and `kv.OpPutIfAbsent` (op code 4) are
  handled by `pkg/store`, both engines in `pkg/engine`, and all four state
  machines in `pkg/statemachine`. The set is discovered at RUN time — a state
  machine that omits one still compiles and still starts, and fails only when
  an entry carrying that op code is applied. `OpPutIfAbsent` was missing from
  all four `pkg/statemachine` implementations until go-qrdb#86, which meant
  the identity index could not claim an identity on `--sm-type memory` or
  `concurrent` and first boot could not complete. Adding an op code means
  adding it everywhere in the same change; the conformance test in
  `pkg/statemachine/putifabsent_test.go` is what enforces that.

  The **single-key guarded verbs** — `protocol.OpCompareAndDelete` (66) and
  `protocol.OpCompareAndSwap` (67) — carry the same contract, and repeated the
  same defect: they were dispatched by `pkg/store` and both `pkg/engine`
  engines and by none of the four state machines, so every `sm_type` those
  serve refused an ordinary authorized `CompareAndDelete` with
  `unknown opcode: 66` until go-qrdb#175. They need no MVCC store — the guard
  is evaluated against the state machine's own store inside apply — so unlike
  the multi-key transaction plane they are not confined to `sm_type: mvcc`.
  They are available on **every `sm_type`**, `fast` included. Between
  go-qrdb#175 and go-qrdb#216 `fast` refused them with a typed
  `GuardedWriteUnsupportedError`, because `KVStateMachine` reaches storage only
  through the `Backend` seam and `FastBackend` acknowledged a write before it
  was durable; `Backend` now requires a durable write pair of every
  implementation, so the refusal is gone along with the error type (below).
  `pkg/statemachine/guarded_ops_test.go` enforces their coverage the way
  `putifabsent_test.go` enforces the base set, and
  `sdk/go/conformance/matrix_test.go` walks them across every backend, mode and
  transport.

  Source: `pkg/statemachine/putifabsent.go`,
  `pkg/statemachine/guarded_ops.go`, `pkg/store/pebble.go`, `pkg/engine/`

- **The sharded state machines acknowledge a write before it is durable.**
  `FastStateMachine` and `ConcurrentStateMachine` write to their in-memory
  shard cache, persist to storage on a background goroutine, and return a
  successful result immediately. `KVStateMachine` and `RegularStateMachine`
  do NOT: they write through their backend synchronously and return its
  error.

  Two consequences follow for the sharded pair, and neither is a durability
  guarantee:

  1. **A failed storage write is only logged — for the UNGUARDED verbs.** An
     ordinary `Put` or `Delete` can be reported as succeeding while the value
     is absent after a restart; its contract is "your write is queued".

     The **guarded** verbs are exempt, by the owner ruling of 2026-08-23 on
     **go-qrdb#94**. `PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete`
     persist SYNCHRONOUSLY on the apply path and RETURN a storage failure
     instead of logging it, because "did I win this claim?" and "is my write
     queued?" are different promises and only the first is worth a disk wait. A
     write that does not reach storage is withdrawn from the shard cache,
     restoring exactly the prior state — an absence, a delete tombstone, or the
     previous value.

     That exemption reached `FastStateMachine` and `ConcurrentStateMachine`
     directly, because they own their storage. `KVStateMachine` reaches storage
     only through the `Backend` seam, so it had no way to ask for a synchronous
     write at all — and over `FastBackend` (`sm_type: fast`) it acknowledged a
     claim before it was durable and logged the storage failure rather than
     returning it.

     **`Backend` now carries the durable pair itself** — `PutDurable` and
     `DeleteDurable`, alongside the asynchronous `Put` and `Delete` (owner
     ruling 2026-09-03, **go-qrdb#216**). The three guarded verbs write through
     the durable pair; `Put`, `Delete` and the batch verbs keep the
     asynchronous one. Every implementation must answer: the four in-memory
     backends and `DiskBackend` satisfy it by delegating, since their writes
     already land before the call returns, and `FastBackend` implements it as a
     synchronous ordered write that returns its storage error and withdraws the
     cache entry when the write does not land.

     The methods are on the interface rather than in an optional one a backend
     may implement. An optional interface is the idiomatic Go shape and it is
     wrong here: a backend that skipped it would fall back to the asynchronous
     path SILENTLY, which is the degradation the guarded plane exists to
     refuse. An opt-in guarantee is not a guarantee. The interim opt-in
     `GuardedWriteBackend` and its `GuardedWriteUnsupportedError` are removed —
     a refusal path no backend can reach is an invitation to re-arm it.
  2. **Writes reach storage in apply order, per shard.** They did not
     originally: each write persisted on an independent goroutine, so
     `Delete(k)` followed by `PutIfAbsent(k, v)` could leave storage with no
     key at all. `persistOrder` draws a ticket under the shard's data lock —
     where apply order is already established — and each writer waits its turn.
     Writes remain asynchronous, so apply never blocks on the disk; only their
     ORDER is constrained. A key belongs to exactly one shard, so per-shard
     ordering is per-key ordering.

     **`FastBackend` was missing this until go-qrdb#224.** It was written after
     go-qrdb#86 and reproduced the pre-#86 write path, so `sm_type: fast` lost
     writes and resurrected deleted keys on the ordinary `Put`/`Delete` path,
     with no crash and no error. Every asynchronous storage writer in
     `pkg/statemachine` now draws a ticket: there are six, and all six are
     ordered. A fix applied writer-by-writer is a fix sized by whoever was in
     the room, so the count is stated here rather than left to be rediscovered.

  Source: `pkg/statemachine/persist_order.go`, `pkg/statemachine/fast.go`,
  `pkg/statemachine/concurrent.go`, `pkg/statemachine/backend_fast.go`

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

- **Every wire field that IS length-framed is bounded by the field that frames
  it, and the encoders refuse what they cannot describe.** A field framed by two
  bytes holds at most 65,535 bytes; one framed by four bytes at most
  4,294,967,295. A field that is NOT framed carries no such bound — an MVCC Put
  value is the remainder of the payload after its key, so it has no length field
  and is limited by the Raft entry size instead.
  A longer value is not encodable, and writing it with a narrowed cast is not
  a truncation of that field but a corruption of the NEXT one: a 65,537-byte
  key written into a two-byte field stores a length of 1, so the decoder reads
  a one-byte key and folds the remaining 65,536 bytes into whatever follows.
  On a write that stores under the wrong key; on a delete it removes a
  different record than the caller named — from input a caller can craft,
  inside a replicated state machine.

  Consequently **every encoder returns `([]byte, error)`**, and every encoder
  validates the whole request, including nested fields of a batch, BEFORE it
  allocates the output buffer, so an unencodable request costs no allocation
  proportional to itself. Refusals are typed:
  `*protocol.FieldTooLongError` names the offending field, and
  `*protocol.EncodedSizeOverflowError` reports a total size an `int` cannot
  describe.

  A refusal is a **client** error: no proposal is made, so the services return
  `*service.EncodeRequestError`, which maps to gRPC `InvalidArgument` and HTTP
  **400**. It is deliberately not `service.ProposeError` — that maps to
  `Internal`/500, which would report a healthy cluster as broken and invite a
  retry of an input that can never succeed.

  Source: `pkg/protocol/lengths.go`, `pkg/service/errors.go`

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

- **Vector storage and embedding as tenant-safe barrier-encrypted primitives.**
  QRDB provides a thin, tenant-scoped RAG ingest/retrieve primitive
  (`pkg/rag/`) backed by a tenant-partitioned vector database
  (`pkg/vector/`, `pkg/engine/vector_handlers.go`), and an
  embedding provider-proxy (`pkg/embedding/`) for generating vectors from
  text. Vector namespaces are TENANT-PARTITIONED—the namespace name is
  qualified by the request-context tenant, preventing collision between
  tenants with the same namespace name. Vector data is BARRIER-ENCRYPTED
  AT REST via the StorageBarrier (the same seal barrier that encrypts KV
  data). Under a `strict` cluster isolation-policy floor (FIPS build, or an
  operator-raised floor — see
  [multi-tenancy/architecture.md#the-isolation-policy](multi-tenancy/architecture.md#the-isolation-policy)),
  tenant-less namespace creation is rejected. RAG ORCHESTRATION
  (chunking strategies, BM25/hybrid/RRF ranking, query rewriting, context
  assembly, LLM text generation) lives at L4 (go-quicbaas); qrdb provides
  only the primitives.

  Source: `pkg/vector/`, `pkg/embedding/`, `pkg/rag/`
  Source: `pkg/engine/vector_handlers.go` -- applies the replicated vector operations

- **Functional options pattern for server configuration.** The REST
  server, gRPC server, multi-protocol orchestrator, and store
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

## 7. Capabilities API

The `pkg/capabilities/` package implements compile-time feature registration using
Go build tags. Each feature (MVCC, transactions, batch, watch, lease, index,
time-series, vector) registers itself via an `init()` function in a
`*_enabled.go` file that is only compiled when the corresponding build tag is
present. A companion `*_stub.go` file (compiled when the tag is absent) registers
the feature as disabled.

```go
// capabilities/registry.go
var registry = map[string]bool{}

func Register(name string, enabled bool) { registry[name] = enabled }
func IsEnabled(name string) bool         { return registry[name] }
func All() map[string]bool               { return registry }
```

The `GET /v1/capabilities` endpoint and `qrdb_capabilities` MCP tool return
`capabilities.All()`, giving clients a runtime view of which features the server
was compiled with. This enables conditional UI rendering in the GUI and
feature-gated CLI commands.

## 8. GUI Architecture

QRDB includes a universal GUI built with Wails v2 and Svelte 5 that runs in
two modes:

- **Desktop**: Native window via Wails, communicating with the embedded qrdbd
  server through Wails-generated Go bindings (IPC, no network)
- **Browser**: Standalone SPA served by the REST API, communicating via
  `fetch()` against the REST endpoints

The frontend lives in `cmd/qrdb/frontend/` and uses Vite, TailwindCSS, and
DaisyUI. A TypeScript SDK (`sdk/typescript/`, published as `@qrdb/sdk`) provides
two transport implementations (`WailsClient` and `RestClient`) that share the
same client interface.

See [GUI documentation](gui/README.md) for setup wizard, component architecture,
and build instructions.

## Cross-References

- [API Reference](api-server/api.md) -- REST and gRPC endpoint specifications
- [MVCC Engine](key-value/engineering.md) -- revision tracking internals
- [Transactions](transactions/engineering.md) -- compare-and-swap semantics
- [Configuration](12-configuration.md) -- complete YAML reference
- [Operations](clustering/cli.md) -- deployment and monitoring
- [Authentication and RBAC](auth/README.md) -- pluggable auth, RBAC authorizer, auth middleware
- [Seal / CA Key Lifecycle](seal/README.md) -- barrier-sealed CA private key protection
- [GUI](gui/README.md) -- desktop and browser GUI architecture
