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

# SQLite VFS Backend (Future Enhancement)

> **Status: PROPOSED — not scheduled, not implemented.** · Last verified: 2026-10-13

**Status: PROPOSED — not scheduled, not implemented.**

This document records the design for making qrdb a **drop-in storage backend
for applications that already use SQLite**: the application keeps running the
stock SQLite engine, with its own schema, DDL, triggers, and query plans, and
qrdb replaces the local file underneath it with a replicated, encrypted,
multi-tenant page store.

## History and motivation

qrdb previously shipped `cmd/qrdb-sqlite`, a virtual-table gateway that
exposed qrdb's KV, entity, collection, timeseries and vector planes as seven
SQLite virtual tables behind an interactive shell. It was removed on
2026-08-17 because it solved the inverse of the intended problem:

- The gateway let a person run SQLite *syntax* **over** qrdb data.
- The requirement was to run qrdb **under** existing SQLite *applications* —
  a drop-in replacement for their database file.

Virtual tables cannot deliver that: an application's own `CREATE TABLE`
lands in the local SQLite database, not in qrdb, and virtual-table modules
only exist inside the process that registers them. No amount of gateway
work closes that gap. Applications migrating their SQL layer to qrdb are
served today by the MySQL/PostgreSQL wire protocols and GORM (on the stock
MySQL dialector);
this proposal is for applications that must keep the SQLite engine itself.

## The mechanism: SQLite's VFS layer

SQLite performs all storage I/O through a pluggable OS-interface called the
**VFS** (`sqlite3_vfs` + `sqlite3_io_methods`). The engine reads and writes
its database as fixed-size pages (4096 bytes by default) through `xRead` /
`xWrite` / `xSync` / `xLock` calls. A custom VFS that maps those calls onto
qrdb gives the application an unmodified SQLite engine whose "file" is a
Raft-replicated cluster.

Prior art demonstrates every load-bearing piece of this design:

| Project | Substrate | Relevance |
|---|---|---|
| **mvsqlite** | FoundationDB | Stock SQLite on a distributed MVCC KV via a page-store VFS; optimistic page-conflict commits. The closest structural precedent. |
| **dqlite** | Raft (C-raft) | SQLite WAL frames replicated through Raft via a custom VFS. Proves the consensus-commit boundary. |
| **LiteFS** | FUSE + Litestream | Drop-in for *unmodified binaries* by interposing at the filesystem layer. |
| **rqlite** | Raft | Server model (not drop-in); useful for operational comparisons only. |

qrdb already owns the three primitives the design needs: an **MVCC engine**
(snapshot reads at a revision), **atomic batch proposals** (a transaction's
dirty pages commit as one Raft entry), and **distributed locking** (writer
serialization across processes).

## Architecture

```
┌────────────────────────────────────────────────────────┐
│ Application (unmodified)                               │
│   stock SQLite engine — parser, planner, B-tree        │
│        │  xRead / xWrite / xSync / xLock               │
│        ▼                                               │
│   qrdb VFS                                             │
│     page cache ── read path:  page@snapshot revision   │
│     txn buffer ── write path: dirty pages → one batch  │
│     lock shim  ── xLock → distributed lock / conflict  │
└────────┬───────────────────────────────────────────────┘
         │ SDK transport (gRPC / QUIC / Unix / embedded)
         ▼
   qrdbd — service layer → Raft → storage backend
```

### Keyspace mapping

Each SQLite "database file" becomes a reserved namespace in the tenant's
keyspace (disjoint from both the KV plane and the relational plane, per the
two-plane authz model):

```
sqlite/<db-name>/meta          page size, page count, format version
sqlite/<db-name>/page/<pgno>   one page per key, versioned by MVCC revision
```

### Read path

At transaction start (`xLock(SHARED)`), the VFS pins the current MVCC
revision. Every `xRead` serves `page@revision`, giving the whole SQLite
transaction a consistent snapshot without holding any server-side state.
A local page cache (invalidated by a watch on the namespace, or by
revision comparison at snapshot pin) keeps warm reads off the network.

### Write path

`xWrite` calls buffer dirty pages locally. The VFS advertises
`SQLITE_IOCAP_BATCH_ATOMIC`, so SQLite skips its rollback journal and
delegates atomicity to the VFS: at commit (`xFileControl(COMMIT_PHASETWO)` /
`xSync`), the buffered pages plus the meta record are submitted as **one
atomic batch proposal**. Rollback discards the buffer. This is the same
coalescing the removed gateway used for virtual-table transactions, moved
down to the page layer where it belongs.

### Concurrency — two selectable modes

1. **Serialized writer** (default, dqlite-style): `xLock(EXCLUSIVE)` acquires
   a qrdb distributed lock with lease. Simple, matches SQLite's native
   single-writer model across *machines*, not just processes.
2. **Optimistic** (mvsqlite-style): no lock; the commit batch carries the
   snapshot revision and the read-page set, and the state machine rejects
   the batch if any read page changed since the snapshot (the CAS/conflict
   machinery the MVCC engine already has). SQLite sees `SQLITE_BUSY` and
   the application's normal busy-retry path applies.

### Durability and consistency mapping

qrdb's existing vocabulary applies directly: write modes (`sync`,
`wal_durable`, `batched`) govern the commit proposal; read consistency
levels govern snapshot pinning (linearizable pin via ReadIndex vs. stale
local pin). Defaults must be `sync` + linearizable — SQLite applications
assume durable commits.

## Delivery tiers (how "drop-in" each consumer gets)

| Tier | Consumer | Change required |
|---|---|---|
| 1 | Go applications | Import the VFS package, open `file:app.db?vfs=qrdb`. Pure-Go via `modernc.org/sqlite` (no CGo) or registered on `mattn/go-sqlite3`. |
| 2 | Any language | A loadable extension (`.so`/`.dylib`) registering the VFS; apps change only the connection string (`?vfs=qrdb`) or `SQLITE_VFS` default. |
| 3 | Unmodified binaries | `LD_PRELOAD` shim or a LiteFS-style FUSE mount. Zero application changes. |

Tier 1 is the natural first milestone; tiers 2–3 reuse its core.

## Performance expectations (honest)

- **Reads**: page-cache-warm reads are local; cold reads are one round-trip
  per page miss. Read-heavy workloads are the sweet spot (mvsqlite's
  published results support this).
- **Writes**: every commit is a Raft proposal carrying its dirty pages.
  Write-amplification is real (a one-row update dirties at least one 4KB
  page plus B-tree ancestors). Hot single-row write loops will be an order
  of magnitude slower than local SQLite; batch-friendly workloads fare far
  better. This must be stated plainly in user docs if built.
- Page size is tunable per database at creation (`PRAGMA page_size`);
  larger pages trade read amplification for fewer proposal entries.

## Component placement

- A new package (e.g. `pkg/sqlitevfs`) implementing the page store against
  the service layer, plus a thin engine binding per tier.
- If any tier requires CGo (`mattn`, the loadable extension), that binding
  lives in its **own Go module**, exactly as the removed gateway did — CGo
  must not enter the root module's dependency graph. The pure-Go tier via
  `modernc.org/sqlite` avoids CGo entirely and should be evaluated first.
- No code from the removed `cmd/qrdb-sqlite` is a dependency of this
  design; the SDK transports and the batch/write-mode plumbing it exercised
  are already first-class SDK surfaces.

## Open questions (blocking a build decision)

1. **WAL-mode support** — advertise batch-atomic and force rollback-journal
   semantics (simpler), or replicate WAL frames dqlite-style (harder,
   better concurrency)?
2. **Checkpoint/vacuum behaviour** — `VACUUM` rewrites every page; needs a
   chunked-proposal strategy to avoid one giant Raft entry.
3. **Multi-writer default** — serialized-writer vs optimistic as the
   shipped default, and how `SQLITE_BUSY` surfaces through each tier.
4. **Backup/restore interplay** — the page namespace must participate in
   qrdb's existing backup, PITR and tenant-barrier machinery; encryption
   comes free via the barrier, but restore semantics need definition.
5. **Quota/entitlement accounting** — pages are opaque to the entity/SQL
   planes; per-tenant quotas need to count the namespace.

## Non-goals

- Reintroducing the virtual-table gateway in any form.
- SQL-dialect compatibility work — that is the MySQL/PG wire protocols'
  charter, which GORM reaches through the stock MySQL dialector.
- Emulating SQLite file-format compatibility on disk; the page store is
  authoritative and only SQLite (through this VFS) interprets it.
