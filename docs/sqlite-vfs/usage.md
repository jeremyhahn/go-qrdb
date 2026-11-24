# SQLite VFS — Using Tier 1 as a Go Library

Tier 1 of the SQLite VFS backend ([architecture](architecture.md)) runs a
stock SQLite application on qrdb's MVCC store: package
`pkg/sqlitevfs/moderncvfs` registers VFS `qrdb` on the pure-Go
`modernc.org/sqlite` driver (no CGo), and package `pkg/sqlitevfs` keeps each
database file as pages under `sqlite/<db>/` in an MVCC keyspace. This page
covers using it from Go. A runnable example is `ExampleRegister` in
`pkg/sqlitevfs/moderncvfs/example_test.go`.

## Setup

1. Build a `sqlitevfs.Backend`:
   - `sqlitevfs.NewMVCCBackend(store)` over a local `pkg/mvcc` store, or
   - `sqlitevfs.NewServiceBackend(kv, txn)` over a scope's MVCC and
     transaction services (an embedded server's).
2. Build one `sqlitevfs.Store` per database file with
   `sqlitevfs.NewStore(backend, "app.db", opts...)`. Options:
   - default: one writer commits at a time against the whole database; a
     commit that finds the database moved since its snapshot is refused;
   - `sqlitevfs.WithWriterLock(w)`: serialized writers through a lock
     (`sqlitevfs.NewLockServiceWriter` over the server's lock service), so
     a second writer gets `SQLITE_BUSY` at `BEGIN IMMEDIATE` or its first
     write;
   - `sqlitevfs.WithOptimistic()`: no lock; a commit is refused only when a
     page it read changed, so writers on disjoint pages both commit;
   - `sqlitevfs.WithStageThreshold(n)` / `WithStageChunkBytes(n)`: the size
     above which a commit stages its pages and publishes them in one move.
3. Register an `moderncvfs.Opener` with `moderncvfs.Register(open)`. The
   binding calls it with the URI's file name and every URI parameter, and
   uses the `*sqlitevfs.Store` it returns. Registration is once per
   process; `moderncvfs.Unregister()` removes it.
4. Open the database with `sql.Open(moderncvfs.DriverName, "file:app.db?vfs=qrdb")`.

## The URI

`file:<name>?vfs=qrdb[&param=value...]`. The binding interprets no
parameter itself; it hands them all to the Opener. By convention
`qrdb_concurrency` (`moderncvfs.ConcurrencyParam`) chooses the Store's
concurrency mode, for example `qrdb_concurrency=optimistic` for
`sqlitevfs.WithOptimistic`. What values an Opener accepts is up to it.

## Journal mode

The VFS advertises batch-atomic writes, so SQLite keeps its rollback
journal out of the store and each commit is one atomic batch of pages plus
the meta record. WAL mode is not supported (decision
[Q1](../decisions/sqlite-vfs-build.md#q1-wal-mode-support)); leave
`journal_mode` at its default and do not set `PRAGMA journal_mode=WAL`.

## Result codes

| Code | Meaning | What the application does |
|---|---|---|
| `SQLITE_BUSY` | Another writer holds the writer lock (`WithWriterLock`), or the lock's lease was lost at commit. | The normal busy-retry path: retry the transaction. |
| `SQLITE_BUSY_SNAPSHOT` | The database moved under the transaction: another writer committed since its snapshot, or a restore replaced the database (its generation changed). Nothing was written. | Roll back and run the transaction again; the next one reads the current pages. |
| `SQLITE_FULL` | The commit would pass the tenant's storage quota. Nothing was written. | Free space or raise the quota; retrying does not help. |

The extended code `SQLITE_BUSY_SNAPSHOT` is 517; `code & 0xff` is
`SQLITE_BUSY`, so a busy handler that checks the primary code covers both.

## Restore

A restore to a revision through a scope's MVCC service
(`RestoreToRevision`, the CLI's `qrdb kv restore`) that changes any key
under `sqlite/<db>/` restores that database through its page store
(`Store.RestoreToRevision`) in one commit that writes the target revision's pages and meta record and bumps the
generation past every generation it has had. This holds on every isolation
type (prefix, volume and raft). A transaction or connection open across the
restore gets `SQLITE_BUSY_SNAPSHOT`; the next one reads the restored pages.
A database that did not exist at the target revision is removed. A dry
run, a restore that touched no `sqlite/` key and a failed restore change
nothing. Stage keys left by abandoned commits are deleted by the restore.
Keys outside `sqlite/` are restored by the MVCC service as before. A
database whose writer lock is held, or that a writer changes meanwhile, is
reported as a conflict and left as it is; a forced restore returns the
error instead.

## Limits

- **Reads**: page-cache-warm reads are local; a cold read is one round trip
  per page miss. Read-heavy workloads suit it best.
- **Writes**: every commit is one proposal carrying its dirty pages. A
  one-row update writes at least one whole page plus its B-tree ancestors,
  so a hot single-row write loop is an order of magnitude slower than local
  SQLite. Batch writes into fewer, larger transactions.
- Page size is set per database at creation (`PRAGMA page_size`); larger
  pages mean fewer proposal entries and more read amplification.

## Entitlement and count cap

Creating a new database is gated by `sqlitevfs.Admit`: the tenant needs the
`sqlite-vfs` entitlement and a free slot under its SQLite-database count
cap; an existing database opens with neither. An operator sets them with
`qrdb tenant set-entitlement --allow-sqlite-vfs` and
`qrdb tenant update-quotas --max-sqlite-databases`. A library user calling
`NewStore` over an embedded Backend is not gated. The server gates the
write that creates a database on its services of every isolation type: a
commit or staged publish that puts a database's meta record while none is
live is refused with the entitlement or quota error and writes nothing, and
each create and drop moves the tenant's `sqlite_databases` usage by one.

Opening a Store over a remote qrdb server through an SDK client is the next
step of plan item o1 in [docs/plans/926.md](../plans/926.md).
