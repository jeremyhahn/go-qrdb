<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Example Configurations

One exemplar per supported cell of the run-mode × state-machine matrix.
Terminology is defined in [docs/GLOSSARY.md](../docs/GLOSSARY.md) — read it
first; "storage backend", "state machine type", and "run mode" are three
different knobs.

## The matrix: implemented vs. planned

Run modes follow the ratified split (see the glossary): **standalone = no
Raft**, standard database operations + the security core; **raft** (single
or clustered) adds MVCC/watch/lease/lock/transactions/etcd/cluster ops.

| Run mode | Replication | Status | Exemplar |
|---|---|---|---|
| Embedded, `embedded://` (SQL driver DSN) | none — raft-free | ✅ implemented (KV + GMS SQL subset; no daemon services) | DSNs below (not YAML) |
| Embedded, `node://` / `sdk/go/node.NewStandaloneNode` | none — raft-free (`backend.LocalBackend`) | ✅ implemented (full in-process service stack) | programmatic (`qrdbconfig.Config`) |
| Embedded, `sdk/go/node.NewClusteredNode` | multi-replica Raft, in-process | ✅ implemented | programmatic |
| **Standalone daemon** (`mode: standalone`) | **none — raft-free** | ✅ implemented: `qrdbd` composes the same LocalBackend stack the SDK uses; raft-only features refused with "requires mode: raft" | [`qrdb-standalone-pebble.yaml`](qrdb-standalone-pebble.yaml) / [`memory`](qrdb-standalone-memory.yaml) / [`file`](qrdb-standalone-file.yaml) |
| Raft single-node daemon | single-replica Raft (quorum of 1) | ✅ implemented (today's default; full feature set incl. MVCC) | [`qrdb.yaml`](qrdb.yaml) (pebble SM), [`qrdb-raft-single-mvcc.yaml`](qrdb-raft-single-mvcc.yaml) (mvcc SM), [`qrdb-dev.yaml`](qrdb-dev.yaml) (pebble SM, dev; memory until the refusal below is lifted) |
| Raft clustered daemon (3 nodes) | multi-replica Raft | ✅ implemented | [`qrdb-cluster-node1.yaml`](qrdb-cluster-node1.yaml) / [`node2`](qrdb-cluster-node2.yaml) / [`node3`](qrdb-cluster-node3.yaml) |
| Raft SM over file/memory storage (`raft.sm_storage`, with `sm_type: disk`/`fast`) | Raft | ⚠️ implemented, but **refused at start** (see below): file backend upgraded to durable atomic writes + all-or-nothing commit journal; objstore remains a snapshot/backup tier and is refused as the hot apply path | flags: `--sm disk --sm-storage file` |

**Only `sm_type` `pebble`, `mvcc` and `mvcc-fast` start a raft daemon.** Every
other type (`memory`, `standard`, `concurrent`, `fast`, `disk`, `highperf`,
`regular`) has no transaction subsystem, and `qrdbd start` refuses it on
**every** start, because role and user creation commits atomically through
one. That includes the `disk`/`fast` storage row above; `qrdb-dev.yaml` runs
pebble for that reason until every state machine supports guarded batches. An
existing deployment on one of those types cannot start on this release. See
[../docs/firstboot/cli.md](../docs/firstboot/cli.md#daemon-side-mode-selection).

Per-backend nuances for standalone: pebble/memory serve the full standard
set; file/objstore compose the plain KV engine, so timeseries is
additionally unavailable there (documented engine boundary,
`pkg/engine/kv_engine.go`).

## Embedded DSN examples (`database/sql` driver)

```go
// Raft-free, in-process, volatile:
db, _ := sql.Open("qrdb", "embedded://memory?database=mydb")

// Raft-free, in-process, durable pebble:
db, _ := sql.Open("qrdb", "embedded:///var/lib/myapp/qrdb?backend=pebble&database=mydb&engine=kv")

// Full in-process node (single-replica Raft, full stack incl. SQL catalog):
db, _ := sql.Open("qrdb", "node:///var/lib/myapp/qrdb?database=mydb")
```

## Choosing `sm_type`

- `pebble` — durable production default. No MVCC.
- `mvcc` — required for **every** MVCC feature: revisions,
  `kv get --at-revision`, watch replay, transactions with revision
  semantics, `kv restore` (PITR-lite).
- `memory` / `concurrent` — volatile; dev and tests only.

## Cluster notes

- Each node's YAML differs only in `replica_id`, its own addresses, and
  (optionally) per-node paths; `peers` lists the SAME full founding member
  set on every node, and every founding node uses `join: false`.
- A node added AFTER formation uses `join: true` and is snapshot-fed;
  never restart an existing member with `join: true`.
- Production clusters use mTLS with SPIFFE SANs
  (`spiffe://{trust_domain}/node/{raftAddr}`); see
  [docs/deployment-multi-node-raft.md](../docs/deployment-multi-node-raft.md).
