<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Run Modes & Feature Model (Requirements)

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13
> Conflicts with this document are defects. Fix them here first.

Ratified 2026-08-17. This document is the requirements statement for qrdb's
run modes and feature composition. Terminology: [GLOSSARY.md](GLOSSARY.md).
Items marked *(planned)* are approved but not yet landed; everything else
describes shipping behavior.

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
  `CompareAndDelete`, and the general transaction API) — see the amendment
  of 2026-08-23 below
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated; require the MVCC-capable engine,
  so unavailable over the file/objstore backends — see the engine boundary
  in `pkg/engine/kv_engine.go`)

Plus the **always-on security core** (below). What needs a cluster — etcd
compatibility, cluster operations — is refused with a typed error whose text
names the remedy: `requires mode: raft`. *(Amended 2026-10-01, go-qrdb#582:
MVCC revisions, watch, lease and lock were in this list; standalone serves
them now — see the amendment at the end and
[run-modes/architecture.md](run-modes/architecture.md).)*

Transactions are **not** in that list, as of the amendment of 2026-08-23.
Consensus is what makes a compare-and-swap linearizable across *replicas*; a
standalone node is engine-direct and single-process and has none, so it can
offer the guarantee outright.

Status: implemented everywhere — the SDK (`node://` /
`sdk/go/node.NewStandaloneNode`, the flat `embedded://` driver) and the
daemon (`mode: standalone` in YAML or `--mode standalone`). All three
roots share one canonical composition
(`lifecycle.NewLocalStorage`/`NewLocalEngine` → `backend.LocalBackend`).

### Raft — single- or multi-node

The QuicRaft consensus layer. A single-node deployment is a one-replica
group (quorum of one) with the identical write path as a cluster and a
clean scale-out path. Adds every consensus-dependent feature: MVCC
revisions and `--at-revision` reads, watch (incl. replay), lease, lock,
transactions, etcd compatibility (`qrdb_etcd`), cluster
membership/topology, rebalance, shard backup/restore, IsolationRaft and
IsolationVolume tenancy.

`raft.sm_storage` selects the storage backend the storage-generic state
machines (`sm_type: disk` or `fast`) compose: `pebble` (default), `file`,
or `memory`. The file backend earned the hot-apply role with real
durability: atomic temp-file+rename+fsync writes and an all-or-nothing
commit journal replayed at open (`pkg/storage/file/journal.go`), plus the
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
| Build tag | Compiled in? | Tag-gated `init()` → `capabilities.Register` (existing: `qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, `codec_*`) |
| Run mode | Structurally possible? | `mode: standalone \| raft` (implemented in config, daemon, and SDK); raft-only features are not composed in standalone |
| Config | Operator-enabled? | Existing sections plus `enabled` on `mvcc`/`watch`/`lease` to disable even in raft mode (implemented) |

There is deliberately **no `qrdb_raft` build tag**: raft code is always
compiled and the mode selects it at runtime — a tag would bifurcate the
release matrix for no gain.

`pkg/capabilities` is the single runtime truth (implemented): at boot the
composition root seals the effective profile (build tags ∩ mode ∩ config)
via `capabilities.Seal(cfg.EffectiveCapabilities())`, and
`admin capabilities` reports it faithfully, including a `mode` field. A
`false` can therefore mean not-compiled, mode-gated, or config-disabled.
Before sealing, `Get` keeps the historical compile-time fallback for
library embeddings.

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
mode: raft   # "standalone" | "raft"; absent = raft (full back-compat)
```

- `mode: standalone` + `raft.join` or a multi-member `raft.peers` list →
  validation error: `requires mode: raft (a standalone node has no cluster to
  join)`. `mvcc`/`watch`/`lease.enabled` are valid in both modes since
  go-qrdb#582. Transactions have no such gate: they are a
  standard database operation in both modes (amendment of 2026-08-23). Standalone skips the raft identity
  requirements (only `raft.data_dir` stays mandatory). Etcd is enabled
  per-database at runtime, so its mode gate lives at service composition.
- Enabling a wire protocol whose build tag is absent → boot-time error
  naming the tag (`Config.ValidateCapabilities`, called by composition
  roots — a deliberate separate gate so pure-config validation stays
  build-independent).
- The daemon serves `mode: standalone` natively: the cluster-shaped endpoint
  families answer 501/Unimplemented with the `requires mode: raft` hint
  on every transport, and the data-plane families — transactions, MVCC,
  watch, lease, lock — are wired in **both** modes.
- Refusals at runtime surface through the existing per-transport typed
  errors (REST 501 / gRPC & QUIC Unimplemented) with the
  `requires mode: raft` hint.

## Behavior change notice

SDK standalone (`NewStandaloneNode`) historically composed MVCC, lease,
and transaction services over `LocalBackend`. Under this requirement MVCC,
watch and lease become raft-mode-only everywhere; standalone compositions
stop building them. Downstream consumers are notified via Gitea issues on
their repos.

## Amendment 2026-08-23 — transactions are served in both modes

**Owner ruling, from go-qrdb issue 62.** The transaction /
compare-and-swap family is served in **every** run mode. The ruling reverses
this document's original inclusion of transactions among the raft-only
families, and settles a conflict between two current normative statements:
this document made transactions raft-only, while the project's agent spec
states that *the SDK surface is identical in all three modes*. The agent spec
governs.

**Why transactions are not like the others.** Consensus makes a
compare-and-swap linearizable across *replicas*. A standalone node is
engine-direct and single-process and has no replicas, so the guarantee holds
without it. MVCC, watch and lease are different in kind — they are about
observing and coordinating state across replicas over time — and stay
raft-only.

**What it fixed.** Withholding it left a standalone consumer with no
compare-and-swap primitive on any surface. `go-xkms` hosts qrdb in-process
and its CA key store reaches `PutIfAbsent` on the first write of a fresh
node, so the daemon failed to start on every `storage.backend: qrdb`
configuration with `embedded: TransactionServicer must not be nil`. Both
sites now serve it: `sdk/go/node.buildEmbeddedOpts` wires
`embedded.WithTransactionService` unconditionally, and
`pkg/server/standalone_gate.go` passes `TransactionService` through.

**One limit, stated because it is a capability boundary rather than a bug.**
Transactions require the MVCC-capable engine, which `NewLocalEngine`
composes for the memory and pebble backends. The **file** and **objstore**
backends compose the plain KV engine, which has no transaction opcode, so a
transaction there fails with a typed `engine.UnsupportedOperationError`. That
is the same engine boundary already documented for MVCC-shaped features
above, not a mode gate.

**Consumer-side fallbacks are not permitted.** A consumer must not
substitute a process-local lock or any other consistency mechanism when a
compare-and-swap is unavailable; it fails closed and reports.

## Amendment 2026-10-01 — watch, lease, lock and MVCC are served in both modes

**go-qrdb#582.** A standalone node serves the MVCC, watch, lease and lock
families with the same API, semantics and errors as raft mode, on every
transport. The amendment of 2026-08-23 kept them raft-only on the ground that
they "coordinate state across replicas over time"; the hosts that depend on
them (client watch, secret-rotation events, scheduler leader election) need
them on one process just as much, and the semantics a single process can give
are exactly raft mode's with one replica. They are composed over the same
engine, lessor and watch hub as raft mode's MVCC state machine, and the
standalone node runs the lease-expiry sweeper itself. Leases are durable in both
modes, so a lock survives a restart. The normative contract is
[run-modes/](run-modes/).
