# Run Modes & Feature Model (Requirements)

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

This document is the requirements statement for qrdb's run modes and feature
composition. Terminology: [GLOSSARY.md](GLOSSARY.md).

## The two run modes

### Standalone — no Raft

One qrdb process, no consensus layer. Services are composed over
`backend.LocalBackend` (`pkg/backend/local.go`) — a `service.Host`
implementation that applies commands directly to an engine on a local
storage backend — so the entire service/transport stack above it is
identical to raft mode.

Serves **standard database operations**:

- KV (get/put/delete/range/scan), batch, secondary indexes
- **Transactions / compare-and-swap** (`PutIfAbsent`, `CompareAndSwap`,
  `CompareAndDelete`, and the general transaction API)
- MVCC revisions, watch, lease and lock (MVCC-capable engine)
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated; require the MVCC-capable engine,
  so unavailable over the file/objstore backends — see the engine boundary
  in `pkg/engine/kv_engine.go`)

Plus the **always-on security core** (below). What needs a cluster — etcd
compatibility, cluster membership, rebalance, shard backup/restore — is
refused with a typed error whose text names the remedy: `requires mode: raft`.
[run-modes/architecture.md](run-modes/architecture.md) has the design.

Standalone is served by the SDK (`node://` /
`sdk/go/node.NewStandaloneNode`, the flat `embedded://` driver) and the
daemon (`mode: standalone` in YAML or `--mode standalone`). All three
roots share one canonical composition
(`lifecycle.NewLocalStorage`/`NewLocalEngine` → `backend.LocalBackend`).

### Raft — single- or multi-node

The QuicRaft consensus layer. A single-node deployment is a one-replica
group (quorum of one) with the identical write path as a cluster and a
clean scale-out path. Adds every cluster-shaped feature: etcd compatibility
(`qrdb_etcd`), cluster membership/topology, rebalance, shard backup/restore
and IsolationRaft tenancy.

`raft.sm_storage` selects the storage backend the storage-generic state
machines (`sm_type: disk` or `fast`) compose: `pebble` (default), `file`,
or `memory`. The file backend is durable: atomic temp-file+rename+fsync
writes and an all-or-nothing commit journal replayed at open (`pkg/storage/file/journal.go`), plus the
`storage.Syncer` seam. Objstore is a snapshot/backup tier and is refused
as a hot apply path with a pointed validation error. The pebble-native
state machines (`pebble`, `mvcc`) carry their own dedicated Pebble store
and reject a non-pebble `sm_storage`.

## The feature model: three gates, one truth

A feature is available iff all three gates pass. Precedence is
**build tag > run mode > config** — config can only narrow, never enable
something the mode forbids or the binary lacks (those are validation
errors, not silent no-ops).

| Gate | Question | Mechanism |
|---|---|---|
| Build tag | Compiled in? | Tag-gated `init()` → `capabilities.Register` (`qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`) |
| Run mode | Structurally possible? | `mode: standalone \| raft` (config, daemon and SDK); raft-only features are not composed in standalone |
| Config | Operator-enabled? | Feature sections plus `enabled` on `mvcc`/`watch`/`lease`, which disables each in either mode |

There is deliberately **no `qrdb_raft` build tag**: raft code is always
compiled and the mode selects it at runtime — a tag would bifurcate the
release matrix for no gain.

`pkg/capabilities` is the single runtime truth: at boot the composition root
seals the effective profile (build tags ∩ mode ∩ config) via
`capabilities.Seal(cfg.EffectiveCapabilities())`, and
`admin capabilities` reports it faithfully, including a `mode` field. A
`false` can therefore mean not-compiled, mode-gated, or config-disabled.
Before sealing, `Get` returns the compile-time profile (raft mode, every core
feature, tag-gated features as registered) for library embeddings.

## The always-compiled CORE

Excluded from build tags because removing any of these either violates the
security posture or breaks a dependency of everything else:

| Core component | Why it cannot be optional |
|---|---|
| `pkg/capabilities` | The registry every feature reports through |
| KV + batch + collections (`pkg/kv`, `pkg/kvstore`, engine layer) | Baseline data plane; every feature and the IAM stores depend on it |
| Database manager (`pkg/database`, `pkg/dao`) | Nothing opens without it |
| `pkg/config`, logger, `pkg/errors`, `pkg/firstboot` | Process cannot boot or bootstrap without them |
| Storage backends memory/file/pebble (`pkg/storage`) | No storage, no database; seal + IAM ride them |
| Auth: mTLS, RBAC, IAM stores (`pkg/auth`, `pkg/user`, `pkg/role`, `pkg/permission`) | An unauthenticated binary violates the security posture |
| Seal barrier + credentials (`pkg/seal`, `pkg/credentials`) | At-rest protection is non-negotiable; IAM depends on the barrier |
| Audit (`pkg/audit`) | Compliance posture; services emit unconditionally |
| REST + gRPC transports | A server with no transport is unusable; enabled per `protocols.*` |

## Configuration

```yaml
mode: raft   # "standalone" | "raft"; absent = raft
```

- `mode: standalone` + `raft.join` or a multi-member `raft.peers` list →
  validation error: `requires mode: raft (a standalone node has no cluster to
  join)`. `mvcc`/`watch`/`lease.enabled` are valid in both modes.
  Transactions have no config gate: they are a standard database operation in
  both modes. Standalone skips the raft identity requirements (only
  `raft.data_dir` stays mandatory). Etcd is enabled per-database at runtime,
  so its mode gate lives at service composition.
- Enabling a wire protocol whose build tag is absent → boot-time error
  naming the tag (`Config.ValidateCapabilities`, called by composition
  roots — a separate gate so pure-config validation stays
  build-independent).
- The daemon serves `mode: standalone` natively: the cluster-shaped endpoint
  families answer REST 501 / gRPC and QUIC Unimplemented with the
  `requires mode: raft` hint on every transport, and the data-plane
  families — transactions, MVCC, watch, lease, lock — are wired in **both**
  modes.

## Transactions in both modes

The transaction / compare-and-swap family is served in **every** run mode.
Consensus is what makes a compare-and-swap linearizable across *replicas*; a
standalone node is engine-direct and single-process and has none, so it
offers the guarantee outright. `sdk/go/node.buildEmbeddedOpts` wires
`embedded.WithTransactionService` unconditionally.

**The engine boundary.** Transactions require the MVCC-capable engine, which
`NewLocalEngine` composes for the memory and pebble backends. The **file**
and **objstore** backends compose the plain KV engine, which has no
transaction opcode, so a transaction there fails with a typed
`engine.UnsupportedOperationError`. This is an engine boundary, not a mode
gate.

**Consumer-side fallbacks are not permitted.** A consumer must not
substitute a process-local lock or any other consistency mechanism when a
compare-and-swap is unavailable; it fails closed and reports.

## Watch, lease, lock and MVCC in both modes

A standalone node serves the MVCC, watch, lease and lock families with the
same API, semantics and errors as raft mode, on every transport: the
semantics a single process can give are raft mode's with one replica. They
are composed over the same engine, lessor and watch hub as raft mode's MVCC
state machine, and the standalone node runs the lease-expiry sweeper itself.
Leases are durable in both modes, so a lock survives a restart. The same
engine boundary applies: the file and objstore backends refuse these
operations per call with a typed error. The normative contract is
[run-modes/](run-modes/).
