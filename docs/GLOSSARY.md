<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# QRDB Glossary

Standard terminology for qrdb. Use these terms exactly — several look
interchangeable but name different layers, and most confusion in configs and
conversation comes from mixing them. The four load-bearing distinctions:

1. **Run mode** — HOW the qrdb process is deployed (embedded / standalone /
   clustered). A topology, not a storage choice.
2. **Storage backend** — WHERE bytes physically live on one node
   (`storage.backend`: memory / file / pebble / objstore).
3. **State machine (SM) type** — WHICH Raft state machine applies committed
   log entries on each replica (`raft.sm_type`: pebble / memory /
   concurrent / mvcc).
4. **Database engine** — WHAT data model a database speaks
   (`engine`: kv / timeseries / vector).

"Raft" is none of these four: it is the **replication layer**, selected by
the run mode. When a per-database `backend: raft` appears, it means
"replicate this database through consensus", not a fifth storage
technology.

**The intended run-mode split (ratified 2026-08-17):**

- **Standalone** = NO Raft. Standard database operations (KV, batch,
  collections, indexes, database management, SQL, timeseries, vector) over
  any non-raft storage backend, plus the always-on security core
  (mTLS+RBAC IAM, seal barrier, audit).
- **Raft** (single- or multi-node) = adds every consensus-dependent
  feature: MVCC revisions, watch, lease, lock, transactions (etcd-style
  Txn is revision-based), etcd compatibility, cluster
  membership/rebalance/shard operations.

Implementation status: the raft-free stack serves the SDK's in-process
paths AND the `qrdbd` daemon (`mode: standalone`). Where this glossary
marks something *(planned)*, the code has not landed yet.

---

## Run modes (deployment topology)

| Term | Definition |
|---|---|
| **Embedded** | qrdb linked as a Go library inside the host application's process. No daemon, no network transport required. Constructed via `sdk/go/node` (`NewStandaloneNode` / `NewClusteredNode`) or reached in-process through the SDK's embedded transport. |
| **`embedded://` (DSN)** | The SDK's fast in-process KV-only transport: engines wired directly, minimal stack. **No relational/SQL surface** — `CREATE TABLE` fails by design. Use for pure KV embedding. |
| **`node://` (DSN)** | The SDK's full in-process node: the complete service stack (SQL/relational catalog, tenants, seal, IAM) inside the caller's process — **raft-free**: `sdk/go/node.NewStandaloneNode` composes `backend.LocalBackend` (a `service.Host` over an engine, no consensus). `NewClusteredNode` is the raft counterpart. Required for relational/SQL work when embedding. |
| **Standalone** | One `qrdb` process with **no Raft**: services composed over `backend.LocalBackend` + an engine on a local storage backend. Serves standard database operations + the security core; refuses raft-only features with a typed "requires mode: raft" error. Implemented in the SDK (`node://`, `NewStandaloneNode`), the `embedded://` driver, AND the daemon (`mode: standalone` in YAML or `--mode standalone`). |
| **Raft single-node** | One `qrdbd` daemon running a **single-replica Raft group** (quorum of one — see *Single-node Raft* below). The full feature set including MVCC/watch/lease, with a clean scale-out path. This is what `configs/qrdb.yaml` describes, and what `qrdbd` runs today by default (`mode: raft`). |
| **Raft clustered** | Three or more `qrdbd` peers forming one Raft group per shard, replicating every write through consensus. Tolerates minority failures (`2f+1` members tolerate `f` failures). |

### Single-node Raft (why a 1-peer config is valid)

Raft commits an entry when a **majority of the replica set** acknowledges
it. For a replica set of one, the majority is one: the node elects itself
leader immediately and every proposal commits with its own vote. So

```yaml
raft:
  replica_id: 1
  peers: "1=127.0.0.1:63001"   # replica set = {1}; quorum = 1
  join: false                  # bootstrap a new group (don't join an existing one)
```

is a complete, valid Raft group. You get the identical write path, log,
snapshot, and recovery semantics as a cluster — which is exactly the point:
a standalone node **scales out later** by adding members (`join: true` on
the newcomers) without changing its storage or apply pipeline. What a
single node does NOT give you is fault tolerance: three peers are needed
only to *survive the loss of one node*, not to make Raft function.

---

## The storage stack (four distinct layers)

From the wire down:

```
Database engine   (kv | timeseries | vector)          ── data model
Store engine      (KVEngine | MVCCEngine, pkg/engine) ── operation dispatch
State machine     (raft.sm_type, pkg/store)           ── applies the Raft log
Storage backend   (storage.backend, pkg/storage)      ── bytes on disk/memory
```

### Storage backend — `storage.backend` (node-level)

The physical key-value persistence a state machine writes through.

| Value | Meaning |
|---|---|
| `memory` | In-process map. Nothing survives restart. Tests/ephemeral. |
| `file` | Simple file-per-namespace store. Low-dependency durability; used by downstream dev flows (e.g. xkey) as a diff-testing oracle. |
| `pebble` | CockroachDB's Pebble LSM. **The production backend.** Encrypted at rest via the seal barrier's encfs when the barrier is enabled. |
| `objstore` | go-objstore providers (local/memory/s3) for object-storage-backed data. |

### State machine type — `raft.sm_type` / `--sm` (per node)

The Raft **state machine** implementation every replica runs to apply
committed entries. Deterministic across replicas by contract.

| Value | Meaning |
|---|---|
| `pebble` | `PebbleStateMachine` — the production on-disk SM (default). |
| `mvcc` | MVCC-capable SM over `MVCCEngine`. **Required for every MVCC feature** (revisions, `--at-revision` reads, watch replay, `kv restore`/PITR-lite, transactions with revision semantics). |
| `memory` | In-memory SM. Fast, volatile; tests. |
| `concurrent` | Concurrent in-memory SM variant. |

`raft.sm_backend` further selects a variant when `sm_type` names a logical
engine: `standard`, `concurrent`, `fast`, `disk`.

### Store engine — `pkg/engine` (internal)

The protocol-dispatch/data-operation layer the state machines wrap:
**`KVEngine`** (plain KV) and **`MVCCEngine`** (revisioned KV with history,
watch, transactions). Not directly user-configurable; chosen by `sm_type`.

### Database engine — `engine` (per database)

The data model of a logical database created via `database create`:
**`kv`**, **`timeseries`**, **`vector`**. SQL/relational is a surface over
the kv/mvcc keyspace via the relational catalog, not a separate engine
value.

### Per-database backend (per database, distinct from `storage.backend`)

`database create --backend …` accepts `memory | file | pebble | objstore |
raft`. The first four choose node-local physical storage for that database.
**`raft` here selects the replicated data plane** — the database's writes go
through the (shared or dedicated) Raft group rather than a node-local
store. This is the one place "raft" appears in a backend position, and it
names replication, not a storage technology.

---

## Support matrix

**Run mode × feature set** (the requirement's matrix; *(planned)* items are
part of the approved run-modes plan):

| Feature | Standalone (no raft) | Raft (single or cluster) |
|---|---|---|
| KV, batch, collections, indexes, database mgmt | ✅ | ✅ |
| SQL, timeseries, vector (build-tag gated) | ✅ | ✅ |
| Auth (mTLS+RBAC IAM), seal barrier, audit | ✅ always-on core | ✅ always-on core |
| MVCC revisions, watch, lease, lock, transactions | ❌ requires raft | ✅ |
| etcd compatibility | ❌ requires raft | ✅ (qrdb_etcd tag) |
| Cluster membership, rebalance, shard backup | ❌ requires raft | ✅ |

**Raft mode × state machine (`raft.sm_type`)**:

| | `pebble` | `mvcc` | `memory` | `concurrent` |
|---|---|---|---|---|
| Raft single-node | ✅ (default) | ✅ | ✅ | ✅ |
| Raft clustered (3+ nodes) | ✅ | ✅ | ⚠ volatile¹ | ⚠ volatile¹ |

¹ Valid (used in tests), but a restarted replica has no local state and
must be re-fed by snapshot; never production. *(Planned: `raft.sm_storage`
selects the SM's storage backend — memory/file/pebble — with objstore
positioned as a snapshot/backup tier, never the hot apply path.)*

**State machine × capability:**

| Capability | `pebble` | `mvcc` | `memory`/`concurrent` |
|---|---|---|---|
| Durable across restart | ✅ | ✅ | ❌ |
| MVCC (revisions, `--at-revision`, watch replay, PITR-lite restore) | ❌ | ✅ | ❌ |
| SQL / relational (needs `node://` or daemon) | ✅ | ✅ | ✅ (volatile) |
| Encryption at rest (barrier encfs) | ✅ | ✅ | n/a |

**Run mode × storage backend (`storage.backend`):** all four backends load
in every run mode; production is `pebble` everywhere, `memory` for tests,
`file` for low-dependency dev flows, `objstore` where object storage is
explicitly wanted.

---

## Replication & cluster terms

| Term | Definition |
|---|---|
| **Raft group / shard** | One consensus group with its own log and leader. The **system shard** (`shard_id: 1`) carries cluster metadata, IAM, and shared data. IsolationRaft tenants get one dedicated shard per database on the shared host. |
| **Replica** | One member of a Raft group on one node (`replica_id`). |
| **Voter / Observer / Witness** | Voter: counts toward quorum. Observer (non-voting learner): receives the log, no vote — how scale-up admits new replicas before promotion. Witness: votes but keeps no state machine. Local role must agree with the replicated membership. |
| **Quorum** | Majority of the voting set. 1 of 1, 2 of 3, 3 of 5. |
| **`join`** | `false`: bootstrap a new group from the `peers` map. `true`: join an existing group as a fresh replica (snapshot-fed). Never restart an existing replica with `join: true`. |
| **Leader forwarding** | Followers transparently forward writes to the leader (REST and SDK transports). |
| **ReadIndex / lease read** | Linearizable read paths; `lease_read: true` serves reads from a valid leadership lease without a heartbeat round-trip (requires `check_quorum`). |
| **RF (replication factor)** | Replica count for a dedicated-raft database (`--replicas`). RF=1 is a valid single-replica group, scaled up later via observer → voter promotion. |

## Tenancy & isolation

| Term | Definition |
|---|---|
| **Isolation type** (per tenant) | `prefix`: logical key-prefix isolation in the shared store. `barrier`: dedicated per-tenant seal barrier (own DEK) over pooled storage. `volume`: dedicated on-disk DataRoot + barrier, single-node host. `raft` (**IsolationRaft**): dedicated Raft group per database on the shared host, tenant-DEK-encrypted. |
| **Tenant barrier** | A per-tenant seal barrier: own root key/DEK, sealed/unsealed independently of the system barrier. *Self-managed*: tenant admin initializes/unseals (starts sealed). *System-managed* (`barrier_auto_unseal`): DEK wrapped by the system barrier, auto-unseals. |
| **Sealed window** | The state where a barrier (system or tenant) is sealed: data fails closed; only the seal/unseal/status bootstrap surface is reachable. |
| **Two-plane authz** | Service RBAC (control plane) and SQL privileges (data plane) are separate by design and govern disjoint resources. |

## Seal / crypto (summary)

| Term | Definition |
|---|---|
| **Barrier** | The encryption boundary: wraps storage so all data at rest is encrypted under a DEK derived from a root key. |
| **Seal strategy** | How the root key is protected: `none`, `passphrase`, `shamir` (M-of-N officer shares), or hardware strategies injected by go-xkms. |
| **Epoch** | A DEK generation; rekey advances the epoch. |
| **DEK / root key** | Data-encryption key derived (HKDF) from the root key; the root key is what a strategy seals/unseals. |

---

*When a doc or config seems to contradict this glossary, the code wins —
then fix whichever document is wrong.*
