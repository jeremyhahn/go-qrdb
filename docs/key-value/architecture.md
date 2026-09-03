# Key-Value Store — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-02

## Problem

Every project in the stack needs durable, consistent key storage that behaves
identically whether it is linked in-process, talked to over a socket, or spread
across a Raft cluster. Anything less means each consumer builds its own
persistence layer and the consistency story fragments.

## Shape chosen

A three-layer store, with each layer replaceable independently:

```
        service.KVServicer            request semantics, permission checks,
              |                       consistency selection, scope resolution
              v
        state machine                 pebble | memory | mvcc  (raft.sm_type)
              |                       replicated through Raft when clustered
              v
        storage backend               pebble | file | memory  (storage.backend)
```

The state machine decides *what history is kept*; the storage backend decides
*where bytes land*. They are configured separately because the useful
combinations are not a single axis — an MVCC state machine over a memory
backend is a legitimate test topology, and a Pebble state machine over a Pebble
backend is the production default.

### Alternatives rejected

- **One monolithic engine with a durability flag.** Rejected: revision history
  and byte placement have genuinely different lifetimes. Compaction policy
  belongs to the state machine; fsync policy belongs to the backend. Fusing
  them made every new backend re-implement MVCC.
- **MVCC always on.** Rejected: revision history costs storage and compaction
  work that the majority of consumers (config keys, service registries, lock
  metadata) never read. `raft.sm_type: pebble` is the default precisely because
  most callers never ask for an as-of read.
- **etcd's revision model wholesale.** Partially adopted — the revision,
  create-revision, mod-revision triple is deliberately etcd-shaped so the etcd
  compatibility surface (see [../etcd-compatibility/](../etcd-compatibility/))
  is a translation rather than an emulation. Not adopted: etcd's single global
  keyspace, because this store is multi-tenant and multi-database from the
  bottom up.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Request semantics, permissions, scope | `pkg/service` | go-qrdb |
| KV operations | `pkg/kv` | go-qrdb |
| MVCC engine, revision encoding, compaction | `pkg/mvcc`, `pkg/mvccadapter` | go-qrdb |
| Storage engines (file / memory / pebble / namespace) | `pkg/storage` | go-qrdb |
| Consistency levels and read routing | `pkg/consistency`, `pkg/routing` | go-qrdb |
| Write durability modes | `pkg/writemode` | go-qrdb (re-exports go-quicraft's) |
| Raft replication, log, snapshots | — | **go-quicraft**; qrdb implements its state-machine interface |
| Object-storage backends and the pool primitive | — | **go-objstore**; reached via `pkg/storage/pool` |
| SDK surface | `sdk/go` (`Client.KV()`, `Client.MVCC()`) | go-qrdb |

## Data flow

A write, clustered:

1. Client calls `KV().Put` on any of the six transports.
2. The transport handler resolves tenant (from the authenticated mTLS
   identity) and database (from the `X-QRDB-Database` header or its per-transport
   equivalent), then looks up the per-(tenant, database) servicer in
   `service.Registry`.
3. The RBAC PDP checks the `kv:put` permission.
4. The servicer encodes a proposal and submits it to go-quicraft.
5. Raft replicates it; on commit each replica applies it to its state machine.
6. The state machine writes through to the storage backend under the write mode
   in force (`sync`, `wal_durable`, `batched`).
7. When the barrier is engaged, values are encrypted with the tenant DEK before
   they reach the backend — see [../barrier-seal/](../barrier-seal/).

A read takes the same first three steps, then diverges on consistency level:
`Strong` goes through consensus, `Linearizable` (the default) uses the ReadIndex
protocol, `LeaseRead` serves from the leader's local state under a valid lease,
and `Eventual` reads any replica's local state.

Embedded mode collapses steps 1–2 into a Go call and, in single-node
configuration, steps 4–5 into a direct apply.

## Trust boundaries

- **Tenant partitioning is keyspace-level and enforced server side.** The
  tenant is taken from the authenticated identity, never from a client-supplied
  field. See [../multi-tenancy/](../multi-tenancy/).
- **Values may be barrier-encrypted at rest.** A sealed barrier fails reads and
  writes closed rather than serving ciphertext or plaintext.
- **The internal namespace is hidden from enumeration and from restore.**
  `service.IsInternalKey` is the single predicate; the reserved prefixes are
  `__sys:`, `__tbl:`, `__idx:`, the table-sequence prefix, `ts:`,
  `idx:metric:`, `idx:tag:` and `retention:`. A bulk `List`/`Scan` is filtered
  and a restore that names one is refused outright.
- **The guard is read-side only, deliberately.** There is no write refusal on
  reserved prefixes, because every internal subsystem — RBAC, tenants,
  collections, credentials, auth factors, the relational layer — writes its own
  records through the same `KVServicer` a write guard would have to refuse. The
  write path is governed by authorization, which is where a per-principal
  decision belongs. The reasoning is recorded in full at
  `pkg/service/internal_keys.go`; do not "tighten" it into a write guard
  without re-reading it.

## The guarded write plane

Three verbs — `PutIfAbsent`, `CompareAndDelete`, `CompareAndSwap` — evaluate
their condition **inside the state machine** rather than in the caller. They
exist because the naive shape a consumer reaches for, read → decide → write, is
not a conditional write: two callers read the same value, both decide, and both
write, and the second silently discards the first. Under a distributed lock
that is two holders.

Because the condition travels with the write in one proposal, replicas cannot
diverge on it and two proposers cannot both win. That property is the entire
product, and it is why the verbs may never be emulated at a transport boundary:
a client-side `Get` followed by a `Put` returns the same booleans and arbitrates
nothing, and it does so across a network round trip that makes the window
larger, not smaller.

The transport surface is therefore a **capability sub-interface**,
`transport.GuardedKVClient`, separate from `KVClient` and detected by type
assertion. A transport gets the assertion only once its wire contract carries
the verbs; until then `client.(GuardedKVClient)` correctly reports false and a
consumer knows to look elsewhere. Answering the assertion without the atomicity
would be worse than refusing it — the consumer would build a lock on top of
something that never decided anything. Embedded, gRPC, unix, QUIC and REST
carry them; MCP does not yet.

A state machine with no handler for a guarded opcode **refuses and keeps
serving**. The refusal is typed all the way to the caller
(`transport.CapabilityUnsupportedError`) so a consumer can fall back to a
different verb rather than retry something that can never succeed, and the
refusal must not cost the shard its circuit breaker — an unimplemented verb
that killed the group would take every later proposal with it, including plain
`Put`. Both properties are asserted per engine, per mode and per transport by
the conformance matrix in `sdk/go/conformance`.

## What this feature deliberately does not do

- **No cluster rewind.** `kv restore` is a logical undo that writes forward
  (see engineering.md); committed Raft history is never discarded. Restoring a
  whole cluster to a point in time is [../backup-restore/](../backup-restore/)'s job.
- **No secondary indexing.** That is [../secondary-indexes/](../secondary-indexes/).
- **No cross-key atomicity beyond a single request.** Multi-key atomicity is
  [../transactions/](../transactions/).
- **No hardware key custody.** The barrier defines the seam; hardware arrives by
  composition from go-xkms through go-quicbaas.
