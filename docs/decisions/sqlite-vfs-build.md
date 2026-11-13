# SQLite VFS backend: answers to the open questions that block a build decision

> **Status: PROPOSED — awaiting owner ruling** · Scope: go-qrdb
> Last verified: 2026-11-12
> SPEC row: `sqlite-vfs` (future). This record answers the five open questions
> of [../sqlite-vfs/architecture.md](../sqlite-vfs/architecture.md)
> ("Open questions (blocking a build decision)"). Each question lists its
> options, the dependency each option needs, a recommendation and the reason.
> The architecture calls are the owner's; nothing here is built.

## Problem

The SQLite VFS design maps SQLite's page I/O onto a reserved
`sqlite/<db-name>/` namespace in the tenant's keyspace, commits a transaction's
dirty pages as one atomic batch proposal, and serializes writers through a
distributed lock ([../sqlite-vfs/architecture.md](../sqlite-vfs/architecture.md)).
Five questions remain open, and until they are answered the first build slice
cannot be scoped: which journal modes the VFS accepts, how page-rewriting
operations (`VACUUM`, checkpoints) fit Raft's entry size, which concurrency mode
ships as the default and how `SQLITE_BUSY` reaches the application, how the
page namespace takes part in backup, PITR and the tenant barrier, and how
pages count against tenant quotas.

### Dependency baseline

Every option below is assessed against the tagged lower releases already in
`go.mod`: go-quicraft v0.24.3-alpha (Raft, batch proposals), go-enclave
v0.11.1-alpha (key custody under the tenant barrier), go-objstore v0.9.1-alpha
(backup object storage) and go-codec v1.5.1-alpha (wire encoding). "Lower
project: none" means the option builds on those tags and on go-qrdb's own
service layer with no new API from a lower project. No option in this record
needs an API a lower project's main lacks.

A SQLite engine binding is a third-party dependency, not a lower project:
`modernc.org/sqlite` (pure Go) for tier 1, or `mattn/go-sqlite3` (CGo) in a
separate Go module. Adding it is a `go.mod` change made by the build lane,
not by this record.

## Q1. WAL-mode support

SQLite applications commonly set `PRAGMA journal_mode=WAL` for reader/writer
concurrency on a local file.

### Options

- **A. Batch-atomic only; WAL is refused or downgraded.** The VFS advertises
  `SQLITE_IOCAP_BATCH_ATOMIC`, so SQLite skips its rollback journal and the
  VFS commits the dirty pages as one batch proposal. The VFS returns no
  `xShmMap` support, so `journal_mode=WAL` fails to engage and SQLite stays in
  rollback mode (`PRAGMA journal_mode` reports the mode actually in force).
  Reader concurrency comes from MVCC snapshots: every reader pins a revision,
  so readers never block the writer or each other, which is what WAL buys on
  a local file.
  Lower project: none.
- **B. Replicate WAL frames (dqlite-style).** The VFS implements the WAL file
  and the shared-memory index (`xShmMap`, `xShmLock`) and replicates committed
  frames through Raft; checkpoints fold frames into pages.
  Lower project: none for the replication itself, but it needs a
  cluster-wide shared-memory index emulation and a checkpoint protocol that
  qrdb does not have, and it duplicates the MVCC versioning the page store
  already provides.
- **C. Accept WAL syntactically, run batch-atomic underneath.** Report `wal`
  to the application while committing as in A.
  Lower project: none. It misreports the mode in force, and applications that
  rely on WAL-specific behaviour (`wal_checkpoint`, `-wal` file presence)
  would see behaviour that does not match what was reported.

### Recommendation: A

The page store's MVCC already gives each reader a consistent snapshot that
does not block the writer, which is the reason applications enable WAL. Option
A keeps one commit path (one batch proposal per transaction), matches the
mechanism the architecture already specifies, and reports the true journal
mode. B doubles the versioning machinery and adds a distributed shared-memory
protocol for no concurrency gain over MVCC snapshots. C hides a mode change
from the application. User docs state that `journal_mode=WAL` is not engaged
and why.

## Q2. Checkpoint/vacuum chunking

`VACUUM` (and, under A, any very large transaction) rewrites every page of the
database. One batch proposal per transaction would make a single Raft entry as
large as the database, past the Raft entry size limit
([../01-architecture.md](../01-architecture.md)) and past the in-memory log
limits ([../12-configuration.md](../12-configuration.md),
`max_in_mem_log_size`). Under Q1 option A there is no WAL checkpoint, so this
question reduces to large transactions and `VACUUM`.

### Options

- **A. Staged commit: chunked staging proposals + one small publish
  proposal.** When a transaction's dirty set exceeds a size threshold, the VFS
  writes its pages in bounded chunks to a staging area keyed by a commit id
  (`sqlite/<db-name>/stage/<commit-id>/page/<pgno>`), invisible to readers;
  a final small proposal atomically publishes the staged pages (moves them
  into `page/` at one revision and updates `meta`) after checking the
  snapshot (serialized lock lease or optimistic read set). An abandoned stage
  is garbage-collected by commit id and lease expiry. Readers keep their
  snapshot semantics: the publish is one revision.
  Lower project: none (bounded batch proposals on go-quicraft v0.24.3-alpha
  plus a server-side publish operation in go-qrdb's service layer).
- **B. Refuse oversized transactions.** Return `SQLITE_FULL` / `SQLITE_TOOBIG`
  above the entry limit; `VACUUM` works only on databases below it.
  Lower project: none. Simple, but `VACUUM` silently stops working as a
  database grows.
- **C. Disable `VACUUM`; rely on `auto_vacuum=INCREMENTAL`.** Force
  incremental vacuum at database creation so free pages are reclaimed in
  small steps, and reject full `VACUUM`.
  Lower project: none. Does not handle large ordinary transactions (bulk
  imports), and changes a SQLite behaviour applications use.
- **D. Raise the Raft entry size limit.**
  Lower project: go-quicraft (entry and snapshot transport limits are its
  contract); it moves the problem rather than removing it and makes every
  large commit a large replication stall.

### Recommendation: A

Staging keeps every Raft entry bounded while preserving the one property the
design depends on: a transaction becomes visible at a single revision or not
at all. It handles `VACUUM` and bulk imports by one mechanism, needs nothing
from go-quicraft beyond the batch proposals it already has, and the
threshold makes small transactions take the existing one-proposal path
unchanged. B and C remove SQLite behaviour; D pushes the cost to every
replica and to a lower project.

## Q3. Multi-writer default and how `SQLITE_BUSY` surfaces

### Options

- **A. Serialized writer default; optimistic opt-in.** `xLock(RESERVED)`
  (SQLite's intent-to-write) acquires the database's distributed lock with a
  lease ([../distributed-locks/architecture.md](../distributed-locks/architecture.md));
  if the lock is held, the VFS returns `SQLITE_BUSY` at once and SQLite's
  busy handler (`sqlite3_busy_timeout`, the `_busy_timeout` connection
  parameter) retries, exactly as on a local file. A lease lost mid-transaction
  fails the commit with `SQLITE_BUSY` (snapshot-checked at publish, so a stale
  holder cannot commit). Optimistic mode is selected per connection by a
  VFS URI parameter (`?vfs=qrdb&qrdb_concurrency=optimistic`).
  Lower project: none (distributed locks are go-qrdb's own).
- **B. Optimistic default (mvsqlite-style).** No lock; the commit carries the
  snapshot revision and read-page set and is rejected on conflict. SQLite
  returns `SQLITE_BUSY` (or `SQLITE_BUSY_SNAPSHOT`) from the commit, which
  many applications do not retry because on a local file a commit after the
  write lock was taken does not fail.
  Lower project: none (go-qrdb's MVCC conflict check).
- **C. Serialized only.** No optimistic mode.
  Lower project: none. Loses the write-scalability path for disjoint-page
  workloads that the architecture already specifies.

### Surfacing per tier

`SQLITE_BUSY` is a VFS return code, so every tier sees it through the stock
engine's own path: tier 1 Go drivers return it as the driver's busy error,
tier 2 loadable-extension users see it as `SQLITE_BUSY` from `sqlite3_step`,
tier 3 unmodified binaries see it exactly as for a contended local file. No
tier adds a qrdb-specific error; the VFS logs the cause (lock held, lease
lost, conflict) for operators.

### Recommendation: A

SQLite's own model is a single writer, and applications are written to
handle `SQLITE_BUSY` at lock acquisition, not at commit. Serialized-writer
default keeps that contract across machines; optimistic stays available for
workloads that measure a benefit, behind an explicit per-connection choice.

## Q4. Backup, restore and PITR interplay

The page namespace lives in the tenant's keyspace, so the tenant barrier
encrypts it like any other key
([../barrier-seal/architecture.md](../barrier-seal/architecture.md),
[../tenant-dek/architecture.md](../tenant-dek/architecture.md)). The open
part is restore semantics: a restore to revision R must yield a SQLite
database that opens, with no half-published transaction.

### Options

- **A. The namespace is ordinary keyspace; restore at a published
  revision.** Backups and PITR
  ([../backup-restore/architecture.md](../backup-restore/architecture.md))
  include `sqlite/<db-name>/` with the tenant's other keys. Because every
  transaction publishes at one revision (Q2 A), any restored revision holds a
  consistent page set and `meta`; staged-but-unpublished chunks
  (`stage/<commit-id>/`) are dropped on restore and by GC. A restore while
  connections are open bumps the database's `meta` generation, so open
  connections see a changed generation at their next snapshot pin, drop their
  page cache and fail the open transaction with `SQLITE_BUSY_SNAPSHOT`
  (reported to the application as a busy error) before re-reading.
  Lower project: none (go-objstore v0.9.1-alpha as the existing backup
  target).
- **B. Per-database SQLite-format export/import.** A separate
  `qrdb sqlite export|import` writes or reads a standard SQLite file,
  outside the backup machinery.
  Lower project: none. Useful for migration in and out, but a second backup
  path that PITR does not cover and the tenant barrier does not protect once
  the file leaves the cluster.
- **C. Exclude the namespace from PITR; whole-database snapshots only.**
  Lower project: none. Loses PITR for SQLite users, the main thing the
  replicated store gives them over a local file.

### Recommendation: A, with B as a later migration tool

The page store is keyspace, and treating it as keyspace makes backup, PITR
and the barrier apply with no new path. The single-revision publish of Q2
makes every revision a valid database, which is the restore semantic the
architecture asked for. Export/import (B) is worth building later as a
migration aid, not as the backup mechanism.

## Q5. Quota and entitlement accounting

Pages are opaque to the entity and SQL planes, so per-tenant quotas
([../multi-tenancy/architecture.md](../multi-tenancy/architecture.md)) must
count the namespace by bytes and keys.

### Options

- **A. Count the namespace in the tenant's existing storage quota, plus a
  per-tenant SQLite-database count limit and an entitlement gate.** Page
  keys count toward the tenant's storage bytes and key count like any other
  key (staged pages included, so a stage cannot exceed quota). Exceeding the
  quota fails the commit with `SQLITE_FULL`, which SQLite surfaces as
  "database or disk is full". Creating a database needs an entitlement
  (`sqlite-vfs`), and the count of databases per tenant is capped.
  Lower project: none.
- **B. A separate SQLite quota.** Its own bytes limit per tenant, not shared
  with the KV/entity planes.
  Lower project: none. Adds a second storage dimension to manage and lets a
  tenant exceed its intended total footprint.
- **C. Per-database `max_page_count`.** Rely on SQLite's own
  `PRAGMA max_page_count`, set by the VFS from a tenant setting.
  Lower project: none. The application can lower it but the VFS must stop it
  raising it; it limits one database, not the tenant.

### Recommendation: A

One storage quota per tenant keeps accounting where operators already look,
and `SQLITE_FULL` is the error SQLite applications already handle for a full
disk. The entitlement keeps the feature off for tenants that have not been
granted it. `max_page_count` (C) can be set as a per-database guard in
addition, not as the quota.

## First build slice the recommendations imply

Placement follows the architecture's "Component placement" section: a new
package for the page store against the service layer, and a thin engine
binding per tier; any CGo binding in its own Go module.

1. **Page store package** (go-qrdb root module, pure Go): the
   `sqlite/<db-name>/meta` and `page/<pgno>` keyspace, snapshot-pinned reads,
   one-proposal commit, the staged commit and publish for oversized
   transactions (Q2 A), serialized-writer locking through distributed locks
   with `SQLITE_BUSY` on contention (Q3 A), quota accounting and
   `SQLITE_FULL` (Q5 A), and the `sqlite-vfs` entitlement. Restore is free
   through ordinary keyspace backup (Q4 A); the slice adds the `meta`
   generation check at snapshot pin.
2. **Tier 1 binding** on `modernc.org/sqlite` (pure Go, no CGo), registering
   VFS `qrdb` with `SQLITE_IOCAP_BATCH_ATOMIC` and no `xShmMap` (Q1 A). This
   is the one new third-party dependency (a `go.mod` change in the build
   lane).
3. **Tests**: unit tests of the page store; CLI end-to-end tests per the
   test standard driving a stock SQLite application on every transport
   (gRPC, QUIC, Unix, embedded): commit, contention and busy-retry, a
   `VACUUM` above the staging threshold, restore to a revision, and quota
   exhaustion.

Deferred to later slices: optimistic mode (Q3), export/import (Q4 B), tiers
2 and 3.

No lower-project API is needed for this slice: it builds on go-quicraft
v0.24.3-alpha, go-enclave v0.11.1-alpha, go-objstore v0.9.1-alpha and
go-codec v1.5.1-alpha as tagged.

## Ruling

None yet. The owner rules on Q1-Q5; this section then records the options
chosen and what was built.
