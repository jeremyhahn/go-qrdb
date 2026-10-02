# Run Modes — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01 ·
> Ratified 2026-08-17

## Problem

The same engine must serve three genuinely different deployments: linked into
another process, a single standalone daemon, and a replicated Raft cluster.
Three products means three code paths and three sets of bugs. One product that
always requires consensus makes the embedded and single-node cases pay for
machinery they cannot use.

## Shape chosen

**Two run modes over one composition.** The service and transport stack above
the backend is identical in both; the mode selects what is composed underneath
and therefore which capabilities exist.

Terminology: [../GLOSSARY.md](../GLOSSARY.md).

### Standalone — no Raft

One qrdb process, no consensus layer. Services are composed over
`backend.LocalBackend` (`pkg/backend/local.go`) — a `service.Host`
implementation that applies commands directly to an engine on a local
storage backend — so the entire service/transport stack above it is
identical to raft mode.

Serves the **whole per-database data plane**, with the same API and the same
semantics as raft mode:

- KV (get/put/delete/range/scan), batch, secondary indexes
- Transactions / compare-and-swap
- **MVCC revisions and `at-revision` reads, watch (prefix, revision resume,
  cancellation), lease (grant, keep-alive, revoke, expiry) and lock**
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated)

Plus the **always-on security core** (below). What stays raft-only is
cluster-shaped — membership, rebalance, shard backup/restore, IsolationRaft
tenancy — and the etcd compatibility listener; those are refused with a typed
error whose text names the remedy: `requires mode: raft`.

**The engine, not the mode, bounds the data plane.** Pebble and memory compose
the MVCC engine and serve all of it. File and objstore compose the plain KV
engine (`pkg/engine/kv_engine.go`), which publishes changes to the watch hub but
keeps no revisions and no leases: it serves watch and value/version
transactions, and refuses MVCC, lease, lock, time series, vector, revision
compares and lease-carrying writes per call with a typed capability error —
the same answer a raft `sm_type` without those planes gives.

### Watch, lease and lock in standalone

*(This section replaces the earlier rule that "everything consensus-shaped —
MVCC revisions, watch, lease, lock — is refused". QuicBaaS and go-xkms depend on
them for client watch, secret-rotation events and scheduler leader election, and
worked around the refusal with in-process fallbacks and a one-member raft
cluster.)*

**Same code by construction.** The standalone engine is `engine.MVCCEngine` —
the engine raft `sm_type: mvcc` wraps in `store.MVCCStateMachine`. Standalone
applies the same commands through `backend.LocalBackend` in process instead of
through the log, so every family runs the code raft mode runs:

| Piece | Raft mode | Standalone |
|---|---|---|
| Engine | `MVCCEngine` inside `MVCCStateMachine` | `MVCCEngine` under `LocalBackend` |
| Watch hub | the node's `watch.Hub`, passed to the state machine | the node's `watch.Hub`, passed to `lifecycle.NewLocalEngine` |
| Lessor | the node's `lease.Lessor`, passed to the state machine | the node's `lease.Lessor`, passed to `lifecycle.NewLocalEngine` |
| Lease expiry | `Lessor.RunExpiry`, gated on Raft leadership | `lifecycle.StartLocalLeaseExpiry`: the same loop, always sweeping — a standalone node is the only replica |
| Revoke on expiry | `LeaseService.Revoke` → the log → apply | `LeaseService.Revoke` → `LocalBackend` → apply |
| Lock | `service.NewLockService` over the (tenant, db) lease, watch, MVCC and transaction views | identical |
| Tenancy | `restServiceRegistry` namespaced views | identical — the same registry |

**Determinism and restart.** Leases are durable in both modes: the engine
records each lease, its attached keys and the ID sequence in its own store in
the same batch as the change, and rebuilds them when it is constructed over that
store. A lease recovered after a restart expires one full granted TTL after
recovery, so a dead holder's lock is freed one TTL after the restart. See
[../leases/engineering.md](../leases/engineering.md#persistence). Expiry is
decided by one sweeper per node: in raft mode on the leader, in standalone on
the only replica — never on two clocks.

**Tenancy.** Watch, lease and lock resolve through the same per-(tenant, db)
service registry on both modes, so a standalone node enforces the same
confinement: a tenant's watch sees only its own `<tenant>/<database>/` keys, a
lease is owned by the namespace that granted it, and a lock lands in the
caller's namespace. There is no standalone-specific scoping code.

**What standalone does not get.** Replication: a standalone node's leases,
watches and locks are exactly as available as the process. Watch history
replay is bounded by the store's own revision index, as in raft mode, and a
watch does not survive a restart in either mode.

**Transactions are the deliberate exception** (owner ruling 2026-08-23). This
paragraph used to list them among the refusals; corrected here to match
`raftOnlyGatedRegistry`, which refuses MVCC, watch and lease and passes
`TransactionService` through. Consensus is what makes a swap linearizable
across *replicas*; a standalone node is engine-direct, single-process and has
none, so its compare-and-swap is already linearizable. The etcd-style `Txn`
endpoint is still refused, as part of etcd compatibility.

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
IsolationVolume tenancy. *(MVCC, watch, lease, lock and transactions are
served by standalone too; listed here because raft serves them on every
`sm_type` that carries them, below.)*

**Every `sm_type` starts a raft daemon.** Each one — `pebble` (the default),
`mvcc`, `memory`, `standard`, `concurrent`, `fast`, `disk`, `highperf` and
`regular` — serves KV, all-or-nothing batches, the guarded single-key verbs
and transactions, decided inside apply against the state machine's own
keyspace, so guarded batches and multi-key transactions behave the same on
all of them. What differs is what else a type carries:

| `sm_type` | Transactions and guarded batches | Revision compares, `--at-revision` | Lease, watch, lock, index, time series |
|---|---|---|---|
| `mvcc` | yes | yes | yes |
| `pebble` (default) | yes | refused (value and version compares only) | yes |
| `memory`, `standard`, `concurrent`, `fast`, `disk`, `highperf`, `regular` | yes | refused (value and version compares only) | no |

A node's capability report names exactly what its `sm_type` serves.

`raft.sm_storage` selects the storage backend the storage-generic state
machines (`sm_type: disk` or `fast`) compose: `pebble` (default), `file`,
or `memory`. The file backend earned the hot-apply role with real
durability: atomic temp-file+rename+fsync writes and an all-or-nothing
commit journal replayed at open (`pkg/storage/file/journal.go`), plus the
`storage.Syncer` seam. Objstore is a snapshot/backup tier and is refused
as a hot apply path with a pointed validation error. Every other state
machine carries its own storage and rejects a non-pebble `sm_storage`.


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


### Alternatives rejected

- **A `qrdb_raft` build tag.** Rejected explicitly: raft code is always compiled
  and the mode selects it at runtime. A tag would bifurcate the release matrix
  for no gain.
- **Silently degrading raft-only features in standalone.** Rejected: a watch
  that never fires or a lease that never expires is worse than an error. Every
  raft-only call in standalone returns a typed error whose text names the
  remedy — `requires mode: raft`.
- **Config enabling what the mode forbids.** Rejected: precedence is
  **build tag > run mode > config**, and config can only ever narrow. Asking for
  more is a validation error, not a silent no-op.
- **Three modes (embedded, standalone, cluster) as distinct configurations.**
  Rejected as a *mode* axis: embedded is a deployment shape of the same
  standalone composition reached through `sdk/go/node`, not a third mode with
  its own semantics.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Mode selection and validation | `pkg/config` | go-qrdb |
| Standalone composition (`LocalBackend`) | `pkg/backend/local.go` | go-qrdb |
| Canonical composition roots | `pkg/lifecycle` (`NewLocalStorage`, `NewLocalEngine`) | go-qrdb |
| Capability registry and sealing | `pkg/capabilities` | go-qrdb |
| Standalone mode option (etcd refusal) | `pkg/server/standalone_gate.go` | go-qrdb |
| Standalone lease-expiry sweeper | `pkg/server/lifecycle` (`StartLocalLeaseExpiry`) | go-qrdb |
| Consensus | — | **go-quicraft** |
| SDK entry points | `sdk/go/node` (`NewStandaloneNode`, `NewClusteredNode`) | go-qrdb |

## What this feature deliberately does not do

- **No third run mode.** Embedded is a deployment shape of standalone.
- **No partial consensus.** A feature that needs the log is composed only in
  raft mode; there is no half-implementation over `LocalBackend`.
- **No runtime mode switching.** The mode is fixed at composition; changing it
  is a restart.
