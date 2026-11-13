# QRDB Architecture Overview

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

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

## 1. Overview

QRDB (QuicRaft DB) is a distributed transactional key-value store in the
same class as etcd: it provides MVCC versioning, compare-and-swap
transactions, distributed leases, watch notifications, and linearizable
reads -- all replicated through multi-group Raft consensus with QUIC
transport.

QRDB is the cluster engine and database system of a layered stack
([SPEC.md](SPEC.md), Purpose):

- **go-quicraft** -- the consensus library (Raft protocol, WAL, QUIC
  transport, state machine interfaces, session deduplication), embedded
  by go-qrdb as a Go module dependency (`go.mod`)
- **go-qrdb** -- the distributed key-value store and the database services
  over it (MVCC, transactions, leases, watches, secondary indexes,
  time-series, vectors, SQL), served on REST, gRPC, QUIC, Unix socket, MCP
  and embedded transports
- **go-xkms** and **go-quicbaas** -- the layers above, which program against
  go-qrdb's `sdk/go` and service interfaces

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
|   | REST | gRPC | QUIC | Unix | MCP | etcd |                  |
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

The boundary between go-quicraft and go-qrdb is go-quicraft's `sm`
state machine interfaces. QRDB uses a three-layer store architecture:

1. **Storage backends** (`pkg/storage/`) implement the `storage.Storage`
   interface. Four backends are provided: `memory` (in-memory, data lost
   on restart), `file` (filesystem-backed, hashed file names), `pebble`
   (PebbleDB-backed) and `objstore` (any go-objstore provider). Each
   `objstore` database lives under its own object prefix, marked by a
   format-3 `qrdb-objstore-format` object that records the store's naming
   limit, so the same objects reopen intact under another spelling of their
   namespace (on `local`, whose journal is named after the root, such a
   reopen is refused instead); dropping the database removes only that
   engine's own objects, never those of a database nested beneath its prefix.
   A `local` namespace has one owner under an OS lock, so `local` and
   `memory` keep `storage.Exclusive`; other providers refuse it and guard
   in-process only.

2. **Store engines** (`pkg/engine/`) implement the `Engine` interface
   (extends `backend.SMAdapter`). They contain all handler logic for
   protocol dispatch:
   - `KVEngine` -- basic Get/Put/Delete/List/Scan, optional indexes and watches
   - `MVCCEngine` -- MVCC versioning, transactions, leases, indexes, watches,
     time-series, and extended protocol op codes

3. **State machines** (`pkg/store/`, `pkg/statemachine/`) are the Raft
   wrappers. `PebbleStateMachine` (`pkg/store/pebble.go`) and
   `MVCCStateMachine` (`pkg/store/mvcc.go`) implement `sm.DiskStateMachine`
   over PebbleDB; `MVCCStateMachine` delegates `Update`/`Lookup` to an
   `MVCCEngine` and adds persisting `lastApplied` and snapshot
   save/restore.

In **local (non-Raft) mode**, a `LocalBackend` (`pkg/backend/local.go`)
wraps an engine directly without a state machine, bypassing Raft consensus
entirely.

Users choose a **store engine** (`kv` or `mvcc`) and a **storage backend**
independently. Every backend defaults to the MVCC engine
(`pkg/server/lifecycle.NewLocalEngine`); `kv` is an explicit opt-in.

The `raft.sm_type` configuration field selects the Raft state machine for
distributed mode: `pebble` (the default), `mvcc`, `memory`, `standard`,
`concurrent`, `fast`, `disk`, `highperf` or `regular` (`validSMTypes`,
`pkg/config/config.go`). `raft.sm_backend` selects a variant, and
`raft.sm_storage` the storage backend that the storage-generic `disk` and
`fast` machines compose.

## 3. Package Structure

```
github.com/jeremyhahn/go-qrdb/
  cmd/qrdbd/                   Server daemon (cobra commands: start, status, peers, node-identity, version)
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
      service.go                 KVService, AdminService, Host interface
      mvcc_service.go            MVCCKVService
      transaction_service.go     TransactionService
      watch_service.go           WatchService
      lease_service.go           LeaseService
      batch_service.go           BatchService
      index_service.go           IndexService
      timeseries_service.go      TimeSeriesService
      vector_service.go          VectorService interface (namespace CRUD, k-NN search, metadata filtering)
      embedding_service.go       EmbeddingServicer interface (provider-proxy, batch processing, caching)
      rag_service.go             RAGServicer interface (document ingest, query, deletion)
      types.go                   Shared types (KeyValue, Event, Lease, Compare, Op, etc.)
      errors.go                  Typed service errors
    storage/                   Storage backend implementations
      storage.go                 storage.Storage interface
      memory/                    In-memory storage backend (data lost on restart)
      file/                      Filesystem-backed storage backend
      pebble/                    PebbleDB-backed storage backend
      objstore/                  go-objstore-backed storage backend
      vector/                    Vector embedding and metadata storage
    engine/                    Store engine implementations
      kv_engine.go               KVEngine (basic KV ops, optional indexes/watches)
      mvcc_engine.go             MVCCEngine (MVCC, transactions, leases, indexes, watches, time-series)
    store/                     Raft state machine wrappers
      mvcc.go                    MVCCStateMachine (sm.DiskStateMachine, delegates to engine)
      pebble.go                  PebbleStateMachine (sm.DiskStateMachine, default raft.sm_type)
      factory.go                 Functional options (Option, MVCCOption)
      registry.go                Registers the pebble and mvcc state machines
      errors.go                  Store-specific error types
    statemachine/              State machine registry and the storage-generic and in-memory machines
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
    audit/                   Audit trail: AuditLogger interface and its sinks (slog, hash-chained file)
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
    go/                        Go SDK; transports: embedded, gRPC, MCP, QUIC, REST, Unix
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

The package import graph is acyclic; the Go compiler enforces it. From
the bottom up (`go list -f '{{.Imports}}'`):

| Package | Imports from go-qrdb `pkg/` |
|---|---|
| `storage`, `consistency` | nothing |
| `lease` | `clock`, `storage` |
| `protocol` | `audit`, `lease`, `vlog` |
| `watch` | `kv`, `logger`, `messaging` |
| `mvcc` | `service`, `storage`, `tenantid` |
| `index` | `errors`, `logger`, `storage`, `util/metrics` |
| `timeseries` | `consistency`, `protocol`, `storage` |
| `engine` | the data packages (`mvcc`, `index`, `lease`, `timeseries`, `vector`, `watch`, `vlog`), `protocol`, `service`, `statemachine`, `storage` |
| `store` | `engine` and the same data packages, `storage/pebble` |
| `service` | the domain packages (auth, seal, tenant, cluster, data); not `engine` or `store` |
| `api/rest`, `api/grpc`, `api/etcd` | `service` and the domain packages |
| `server` | every `api/*` transport, `service`, `store`, `config` |
| `cmd/qrdbd`, `sdk/go/node` | `server`, `server/lifecycle`, `service`, `store`, `config` |

## 5.5. Daemon and Embedded Sibling Composition

Both the standalone daemon (`cmd/qrdbd`) and the embedded SDK node (`sdk/go/node`)
are sibling composition roots that call `server.New()` over the shared `pkg/server`
core. The daemon is **not** built on the node; rather, both are built on the same
foundation.

Both roots wire the per-tenant infrastructure through
`pkg/server.BuildTenantInfra(TenantInfraConfig)`: the tenant host registry
(`TenantHostRegistry`), the Raft backend factory (`RaftBackendFactory`) and
the placement reconciler (`TenantShardReconciler`). The embedded node
therefore has the daemon's per-tenant features, such as tenant-scoped
encryption via `node.WithTenantIsolation()` and `(*Node).EnableEncryption`.

## 6. Key Design Decisions

- **MVCC as a separate package over `storage.Storage`.** `mvcc.Store`
  manages revision-keyed data on any storage backend, independently from
  the base KV path, enabling distinct compaction policies and key encoding
  schemes without coupling the two storage paths.

- **Three-layer store architecture with independent engine and backend selection.**
  The store layer is split into storage backends (`pkg/storage/`), store
  engines (`pkg/engine/`), and thin Raft state machine wrappers (`pkg/store/`).
  Operators choose a store engine (`kv` for simple put/get/delete with base
  op codes 1-4, or `mvcc` for the full transactional feature set with
  extended op codes 10+) and a storage backend (`memory`, `file`, `pebble`,
  `objstore`)
  independently. State machines delegate all data operations to engines,
  adding only Raft lifecycle concerns (open, lastApplied, snapshots).
  In local (non-Raft) mode, a `LocalBackend` wraps an engine directly.

  Source: `pkg/engine/`, `pkg/storage/`, `pkg/store/`

- **The base KV opcode set is a contract every state machine must satisfy in
  full.** `kv.OpPut`, `kv.OpDelete` and `kv.OpPutIfAbsent` (op code 4) are
  handled by `pkg/store`, both engines in `pkg/engine`, and `KVStateMachine`,
  `FastStateMachine` and `RegularStateMachine` in `pkg/statemachine`. The set
  is discovered at run time: a state machine that omits one still compiles and
  starts, and fails only when an entry carrying that op code is applied.
  Adding an op code means adding it everywhere in the same change; the
  conformance test in `pkg/statemachine/putifabsent_test.go` enforces that.

  The **single-key guarded verbs**, `protocol.OpCompareAndDelete` (66) and
  `protocol.OpCompareAndSwap` (67), carry the same contract. They need no MVCC
  store: the guard is evaluated against the state machine's own store inside
  apply, so unlike the multi-key transaction plane they are not confined to
  `sm_type: mvcc`. They are available on **every `sm_type`**, `fast` included.
  `pkg/statemachine/guarded_ops_test.go` enforces their coverage the way
  `putifabsent_test.go` enforces the base set, and
  `sdk/go/conformance/matrix_test.go` walks them across every backend, mode and
  transport.

  Source: `pkg/statemachine/putifabsent.go`,
  `pkg/statemachine/guarded_ops.go`, `pkg/store/pebble.go`, `pkg/engine/`

- **The sharded state machine acknowledges a write before it is durable.**
  `FastStateMachine` writes to its in-memory shard cache, persists to storage
  on a background goroutine, and returns a successful result immediately. `KVStateMachine` and `RegularStateMachine`
  do NOT: they write through their backend synchronously and return its
  error.

  Two consequences follow for the sharded state machine, and neither is a durability
  guarantee:

  1. **A failed storage write is only logged, for the UNGUARDED verbs.** An
     ordinary `Put` or `Delete` can be reported as succeeding while the value
     is absent after a restart; its contract is "your write is queued".

     The **guarded** verbs are exempt. `PutIfAbsent`, `CompareAndSwap` and `CompareAndDelete`
     persist SYNCHRONOUSLY on the apply path and RETURN a storage failure
     instead of logging it, because "did I win this claim?" and "is my write
     queued?" are different promises and only the first is worth a disk wait. A
     write that does not reach storage is withdrawn from the shard cache,
     restoring exactly the prior state: an absence, a delete tombstone, or the
     previous value.

     `KVStateMachine` reaches storage only through the `Backend` seam, so
     **`Backend` carries the durable pair itself**: `PutDurable` and
     `DeleteDurable`, alongside the asynchronous `Put` and `Delete`. The three guarded verbs write through
     the durable pair; `Put`, `Delete` and the batch verbs keep the
     asynchronous one. Every implementation must answer: the in-memory
     `StandardBackend`, `ConcurrentBackend` and `HighPerfBackend`, and
     `DiskBackend`, satisfy it by delegating, since their writes already land
     before the call returns, and `FastBackend` implements it as a
     synchronous ordered write that returns its storage error and withdraws the
     cache entry when the write does not land.

     The methods are on the interface rather than in an optional one a backend
     may implement. An optional interface is the idiomatic Go shape and it is
     wrong here: a backend that skipped it would fall back to the asynchronous
     path SILENTLY, which is the degradation the guarded plane exists to
     refuse. An opt-in guarantee is not a guarantee.
  2. **Writes reach storage in apply order, per shard.** `persistOrder` draws a
     ticket under the shard's data lock, where apply order is already
     established, and each writer waits its turn. Without it,
     `Delete(k)` followed by `PutIfAbsent(k, v)` could leave storage with no
     key at all. Writes remain asynchronous, so apply never blocks on the disk;
     only their ORDER is constrained. A key belongs to exactly one shard, so
     per-shard ordering is per-key ordering. Every asynchronous storage writer
     in `pkg/statemachine`, `FastStateMachine` and `FastBackend` included,
     draws a ticket.

  Source: `pkg/statemachine/persist_order.go`, `pkg/statemachine/fast.go`,
  `pkg/statemachine/backend_fast.go`

- **One service per feature domain.** Each feature domain has its own
  service type (`KVService`, `MVCCKVService`, `TransactionService`,
  `WatchService`, `LeaseService`, `BatchService`, `IndexService`,
  `TimeSeriesService`, `AdminService`), all over the `Host` interface. API
  handlers depend only on the services they need, enabling independent
  testing.

  Source: `pkg/service/*.go`

- **Extended wire protocol defined in go-qrdb, not go-quicraft.** The base
  KV protocol (op codes 1-4: `OpPut`, `OpGet`, `OpDelete`, `OpPutIfAbsent`)
  lives in go-quicraft's `pkg/sm/kv` package. QRDB extends it with
  higher-numbered op codes (10-82) for MVCC, transactions, batches, leases,
  indexes, time-series, guarded writes, vectors, audit, range deletes and the
  verifiable log. This keeps the consensus library generic while
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
  API handler (`kvMethodHandlers`, a `map[string]kvMethodHandler` in
  `pkg/api/rest/handlers.go`) and the MVCC engine (`map[byte]mvccHandler`
  in `pkg/engine/mvcc_engine.go`) use dispatch tables instead of `switch`
  statements.

  Source: `pkg/api/rest/handlers.go` -- `kvMethodHandlers`

- **Service layer as the critical abstraction.** All protocol handlers
  (REST, gRPC) delegate to the service layer. The service layer
  translates between the API-facing types and the QuicRaft host
  operations, handling error mapping (`NotLeaderError` -> service-level
  `NotLeaderError`), protocol encoding, and business logic. No Raft
  operations leak into the API layer.

  Source: `pkg/service/service.go` -- `KVService`, `AdminService`

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

- **Typed errors only.** Errors are named struct types with an
  `Error() string` method rather than `fmt.Errorf` strings, enabling
  callers to use `errors.As` for precise error handling. Error types are
  co-located with their packages (`pkg/service/errors.go`,
  `pkg/store/errors.go`, `pkg/api/rest/errors.go`, etc.).

## 7. Capabilities API

The `pkg/capabilities/` package implements compile-time feature registration using
Go build tags. Each optional feature (time-series, vector, SQL, MySQL,
PostgreSQL, etcd, full-text search) has a `capabilities_<feature>.go` file,
compiled only under its build tag (`qrdb_ts`, `qrdb_vector`, `qrdb_sql`,
`qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`), whose `init()` calls
`Register`. A feature whose tag is absent is never registered. The core
features (KV, MVCC, transactions, batch, watch, lease, lock, index, cluster,
backup, rebalance) are always present.

```go
// pkg/capabilities/capabilities.go
func Register(feature string)   // called from a tagged file's init()
func Has(feature string) bool   // was the feature compiled in?
func Seal(c Capabilities)       // composition root: build tags ∩ run mode ∩ config
func Get() Capabilities         // the sealed profile, or the compiled-in one
```

The `GET /capabilities` REST endpoint and the `qrdb_capabilities` MCP tool
return `capabilities.Get()` through `AdminService.Capabilities`, giving
clients a runtime view of which features this node serves. This enables
conditional UI rendering in the GUI and feature-gated CLI commands.

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
