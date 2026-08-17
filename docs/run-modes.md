<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Run Modes & Feature Model (Requirements)

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
- Collections and entity CRUD
- Database management (create/list/delete; engines kv, timeseries, vector)
- SQL (build-tag gated, `qrdb_sql`)
- Timeseries and vector (build-tag gated; require the MVCC-capable engine,
  so unavailable over the file/objstore backends — see the engine boundary
  in `pkg/engine/kv_engine.go`)

Plus the **always-on security core** (below). Everything consensus-shaped —
MVCC revisions, watch, lease, lock, transactions, etcd compatibility,
cluster operations — is refused with a typed error whose text names the
remedy: `requires mode: raft`.

Status: implemented in the SDK today (`node://` /
`sdk/go/node.NewStandaloneNode`, and the flat `embedded://` driver);
*(planned)* in the daemon via `mode: standalone`.

### Raft — single- or multi-node

The QuicRaft consensus layer. A single-node deployment is a one-replica
group (quorum of one) with the identical write path as a cluster and a
clean scale-out path. Adds every consensus-dependent feature: MVCC
revisions and `--at-revision` reads, watch (incl. replay), lease, lock,
transactions, etcd compatibility (`qrdb_etcd`), cluster
membership/topology, rebalance, shard backup/restore, IsolationRaft and
IsolationVolume tenancy.

*(planned, final phase)* `raft.sm_storage` selects the state machine's
storage backend (memory/file/pebble). The file backend requires durability
work first (atomic batch commit, fsync); objstore is positioned as a
snapshot/backup tier and is refused as a hot apply path.

## The feature model: three gates, one truth

A feature is available iff all three gates pass. Precedence is
**build tag > run mode > config** — config can only narrow, never enable
something the mode forbids or the binary lacks (those are validation
errors, not silent no-ops).

| Gate | Question | Mechanism |
|---|---|---|
| Build tag | Compiled in? | Tag-gated `init()` → `capabilities.Register` (existing: `qrdb_sql`, `qrdb_mysql`, `qrdb_pgsql`, `qrdb_etcd`, `qrdb_fts`, `qrdb_vector`, `qrdb_ts`, `codec_*`) |
| Run mode | Structurally possible? | `mode: standalone \| raft` (config key implemented; daemon composition lands in phase 3); raft-only features are not composed in standalone |
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

## Configuration (config gate implemented; daemon branch in phase 3)

```yaml
mode: raft   # "standalone" | "raft"; absent = raft (full back-compat)
```

- `mode: standalone` + explicit `mvcc.enabled`/`watch.enabled`/
  `lease.enabled: true`, `raft.join`, or a multi-member `raft.peers` list →
  validation error: `requires mode: raft (standalone serves standard
  database operations only)`. Standalone skips the raft identity
  requirements (only `raft.data_dir` stays mandatory). Etcd is enabled
  per-database at runtime, so its mode gate lives at service composition.
- Enabling a wire protocol whose build tag is absent → boot-time error
  naming the tag (`Config.ValidateCapabilities`, called by composition
  roots — a deliberate separate gate so pure-config validation stays
  build-independent).
- The daemon currently refuses `mode: standalone` with a pointed error;
  its raft-free composition branch is phase 3.
- Refusals at runtime surface through the existing per-transport typed
  errors (REST 501 / gRPC & QUIC Unimplemented) with the
  `requires mode: raft` hint.

## Behavior change notice

SDK standalone (`NewStandaloneNode`) historically composed MVCC, lease,
and transaction services over `LocalBackend`. Under this requirement those
become raft-mode-only everywhere; standalone compositions stop building
them. Downstream consumers are notified via Gitea issues on their repos.
