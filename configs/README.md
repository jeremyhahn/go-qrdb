<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Example Configurations

One exemplar per supported cell of the run-mode × state-machine matrix.
Terminology is defined in [docs/GLOSSARY.md](../docs/GLOSSARY.md) — read it
first; "storage backend", "state machine type", and "run mode" are three
different knobs.

## The matrix: implemented vs. gaps

| Run mode | Replication | SM type | Storage backend | Status | Exemplar |
|---|---|---|---|---|---|
| Embedded, `embedded://` (SQL driver DSN) | **none — no Raft at all** | n/a (plain `database.Backend`) | memory \| file \| pebble | ✅ implemented | DSNs below (not YAML) |
| Embedded, `node://` / `sdk/go/node` | single-replica Raft, in-process | pebble (default) \| mvcc \| memory \| concurrent | pebble \| memory \| file | ✅ implemented | programmatic (`qrdbconfig.Config`), same keys as the YAMLs here |
| Standalone daemon | single-replica Raft (quorum of 1) | pebble (default) | pebble | ✅ implemented | [`qrdb.yaml`](qrdb.yaml) |
| Standalone daemon | single-replica Raft | **mvcc** (required for MVCC features) | pebble | ✅ implemented | [`qrdb-standalone-mvcc.yaml`](qrdb-standalone-mvcc.yaml) |
| Standalone daemon | single-replica Raft | memory (volatile, dev/test) | memory | ✅ implemented | [`qrdb-dev.yaml`](qrdb-dev.yaml) |
| Clustered daemon (3 nodes) | multi-replica Raft | pebble or mvcc | pebble | ✅ implemented | [`qrdb-cluster-node1.yaml`](qrdb-cluster-node1.yaml) /  [`node2`](qrdb-cluster-node2.yaml) / [`node3`](qrdb-cluster-node3.yaml) |
| **Standalone daemon WITHOUT Raft** (memory/file/pebble only, full non-Raft feature set) | none | n/a | memory \| file \| pebble | ❌ **GAP — not implemented.** `qrdbd` unconditionally builds the QuicRaft host; every daemon data-plane service rides the system shard. Tracked on the production-readiness backlog. | — |

Additional truths behind the matrix:

- **Per-database backends inside a daemon** (`database create --backend
  memory|file|pebble`) open a node-local `StandaloneBackend` at the manager
  level, but the daemon's SERVED data plane currently routes all databases
  through the system shard's services (see the registry construction in
  `cmd/qrdbd/start.go`); dedicated-Raft (IsolationRaft) databases are the
  exception with their own shards. A non-Raft SERVING path in the daemon is
  part of the same gap noted above.
- The only fully Raft-free path today is the embedded `embedded://` driver:
  a plain storage backend + engine in your process — KV and the GMS SQL
  query engine, but no Raft, no daemon services (IAM/tenants/watch/lease/
  seal), and no relational-table catalog unless a registry is supplied.

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
