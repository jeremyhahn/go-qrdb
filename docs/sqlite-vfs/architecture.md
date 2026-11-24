# SQLite VFS Backend — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-23
> Conflicts with this document are defects. Fix them here first. This is a
> future feature: not scheduled, not implemented.

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

This document is the design for making qrdb a **drop-in storage backend
for applications that already use SQLite**: the application keeps running the
stock SQLite engine, with its own schema, DDL, triggers, and query plans, and
qrdb replaces the local file underneath it with a replicated, encrypted,
multi-tenant page store.

## Problem

The requirement is to run qrdb **under** existing SQLite *applications*: a
drop-in replacement for their database file.

A virtual-table gateway, exposing qrdb's KV, entity, collection, timeseries and
vector planes as SQLite virtual tables behind an interactive shell, is rejected:
it lets a person run SQLite *syntax* **over** qrdb data, the inverse of the
requirement. Virtual tables cannot deliver a drop-in: an application's own
`CREATE TABLE` lands in the local SQLite database, not in qrdb, and
virtual-table modules only exist inside the process that registers them.
Applications migrating their SQL layer to qrdb are served by the
MySQL/PostgreSQL wire protocols and GORM (on the stock MySQL dialector); this
design is for applications that must keep the SQLite engine itself.

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
sqlite/<db-name>/meta          page size, page count, format version, generation, change counter
sqlite/<db-name>/page/<pgno>   one page per key, versioned by MVCC revision
sqlite/<db-name>/stage/<commit-id>/page/<pgno>   staged page of an oversized commit, never read as a page
```

### Read path

At transaction start (`xLock(SHARED)`), the VFS pins the current MVCC
revision. Every `xRead` serves `page@revision`, giving the whole SQLite
transaction a consistent snapshot without holding any server-side state.
A connection that stays at SHARED after its own commit (an open read
statement) reads on at that commit's revision, not the newest one.
A local page cache (invalidated by a watch on the namespace, or by
revision comparison at snapshot pin) keeps warm reads off the network.

### Write path

`xWrite` calls buffer dirty pages locally. The VFS advertises
`SQLITE_IOCAP_BATCH_ATOMIC`, so SQLite skips its rollback journal and
delegates atomicity to the VFS: at commit (`xFileControl(COMMIT_PHASETWO)` /
`xSync`), the buffered pages plus the meta record are submitted as **one
atomic batch proposal**. Rollback discards the buffer; a rollback SQLite
plays back from its journal over pages it already wrote (a cache spill)
restores the snapshot's bytes, so that commit writes no revision. A commit above the
stage threshold stages its pages and publishes them with the server-side
prefix move (`OpMVCCMovePrefix`, gated by the `prefix-move` cluster feature),
the meta record and truncated-page deletes at the same one revision.

### Concurrency — two selectable modes

1. **Serialized writer** (default, dqlite-style): `xLock(RESERVED)` (not
   EXCLUSIVE, as decided in Q3 A) acquires a qrdb distributed lock with
   lease without blocking and returns `SQLITE_BUSY` at once when it is held;
   a lost lease fails the commit with `SQLITE_BUSY`. Simple, matches
   SQLite's native single-writer model across *machines*, not just
   processes.
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
| 1 | Go applications | Import the VFS package, open `file:app.db?vfs=qrdb`. Pure-Go via `modernc.org/sqlite` (no CGo): package `pkg/sqlitevfs/moderncvfs`, registered with `moderncvfs.Register`; or registered on `mattn/go-sqlite3`. |
| 2 | Any language | A loadable extension (`.so`/`.dylib`) registering the VFS; apps change only the connection string (`?vfs=qrdb`) or `SQLITE_VFS` default. |
| 3 | Unmodified binaries | `LD_PRELOAD` shim or a LiteFS-style FUSE mount. Zero application changes. |

Tier 1 is the natural first milestone; tiers 2–3 reuse its core.

## Performance expectations

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

- A new package implementing the page store against the service layer,
  plus a thin engine binding per tier. The page store core exists
  in `pkg/sqlitevfs` (keyspace, meta record, snapshot reads, one-batch
  commit on the local MVCC store), with the staged commit and publish for
  oversized transactions (Q2), the meta generation check (Q4), and the
  serialized writer lock with the meta change counter and the sweep of
  stages a crashed writer left (Q3), and the server-side prefix-move publish
  op (`OpMVCCMovePrefix` behind the `prefix-move` cluster feature), with a
  service-backed Backend (`ServiceBackend`) that commits through the
  transaction service and publishes through that op (an optimistic
  commit's read set travels as the commit's compares, or as the prefix
  move's compares that the engine checks at apply, atomically with the
  move), and the writer lock
  over the server's lock service (`NewLockServiceWriter`), and a commit past
  the tenant's storage quota failing with `ErrFull`, a `Backend` that takes
  the caller's context on every call, a staged publish refused at apply when
  its database was dropped meanwhile (the drop-fence guard), and the
  creation gate `sqlitevfs.Admit`: a new database needs the `sqlite-vfs`
  entitlement and a free slot under the tenant's SQLite-database count cap
  (a soft bound), and an existing one opens with neither. The gate and its
  admin surface (the entitlement and the cap on every transport, the CLI and
  config defaults) are built. The tier 1 engine binding
  (`pkg/sqlitevfs/moderncvfs` on `modernc.org/sqlite`, see
  [usage.md](usage.md)) is built, and so is the Q4 restore hook: every
  scope's MVCC service in `pkg/server` is wrapped so a restore to a
  revision that changed a key under `sqlite/<db>/` restores that database
  through its page store (`Store.RestoreToRevision`) in one commit that
  carries the generation bump. The server calls the gate on its write path:
  every scope's transaction service and prefix move admit the write that
  puts a database's meta record while none is live, and count each create
  and drop in the tenant's `sqlite_databases` usage. The Store stays in the
  application; the client half of owner item o1 (a Store over an SDK
  client, in [../plans/926.md](../plans/926.md)) is not built.
- If any tier requires CGo (`mattn`, the loadable extension), that binding
  lives in its **own Go module**: CGo must not enter the root module's
  dependency graph. The pure-Go tier via `modernc.org/sqlite` avoids CGo
  entirely and should be evaluated first.
- The SDK transports and the batch and write-mode plumbing the VFS needs are
  existing SDK surfaces.

## Decided questions

Each question is answered with options, a recommendation and its reason in
the decision record
[../decisions/sqlite-vfs-build.md](../decisions/sqlite-vfs-build.md),
where the questions are decided by convention under ruling 109, then
adopted by Tenant Agent ruling 1115 (2026-11-22); owner ruling 1234
approved the SQLite engine dependency (`modernc.org/sqlite`).

1. **WAL-mode support** — advertise batch-atomic and force rollback-journal
   semantics (simpler), or replicate WAL frames dqlite-style (harder,
   better concurrency)? See
   [Q1](../decisions/sqlite-vfs-build.md#q1-wal-mode-support).
2. **Checkpoint/vacuum behaviour** — `VACUUM` rewrites every page; needs a
   chunked-proposal strategy to avoid one giant Raft entry. See
   [Q2](../decisions/sqlite-vfs-build.md#q2-checkpointvacuum-chunking).
3. **Multi-writer default** — serialized-writer vs optimistic as the
   shipped default, and how `SQLITE_BUSY` surfaces through each tier. See
   [Q3](../decisions/sqlite-vfs-build.md#q3-multi-writer-default-and-how-sqlite_busy-surfaces).
4. **Backup/restore interplay** — the page namespace must participate in
   qrdb's existing backup, PITR and tenant-barrier machinery; encryption
   comes free via the barrier. Decided A: a restore to a revision bumps the
   generation of each database it touched, in the same commit that restores
   the database's pages, built as the restore hook in `pkg/server` (see
   [usage.md](usage.md#restore)). See
   [Q4](../decisions/sqlite-vfs-build.md#q4-backup-restore-and-pitr-interplay).
5. **Quota/entitlement accounting** — pages are opaque to the entity/SQL
   planes; per-tenant quotas need to count the namespace. Creation is gated
   by `sqlitevfs.Admit` (entitlement and count cap), not yet called by the
   server. An operator sets both on any transport with
   `qrdb tenant set-entitlement --allow-sqlite-vfs` and
   `qrdb tenant update-quotas --max-sqlite-databases`. See
   [Q5](../decisions/sqlite-vfs-build.md#q5-quota-and-entitlement-accounting).

## Non-goals

- A virtual-table gateway, in any form.
- SQL-dialect compatibility work — that is the MySQL/PG wire protocols'
  charter, which GORM reaches through the stock MySQL dialector.
- Emulating SQLite file-format compatibility on disk; the page store is
  authoritative and only SQLite (through this VFS) interprets it.
