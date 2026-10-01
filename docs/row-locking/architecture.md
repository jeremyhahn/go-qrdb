# Row Locking — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

> **Build-tag gated** with the rest of the SQL layer: `qrdb_sql` compiles it,
> `qrdb_mysql` / `qrdb_pgsql` carry it onto the wire protocols. See
> [../sql/architecture.md](../sql/architecture.md).

## Problem

QRDB's SQL transactions are optimistic. A transaction buffers its writes and
commits them as one compare-and-swap; a concurrent writer of the same row is
not blocked, it is **aborted at `COMMIT`** with MySQL 1213 / SQLSTATE 40001.
That is correct and cheap when conflicts are rare. Under contention it is the
wrong trade:

- A hot row — a counter, a parent row every child insert touches, the
  `updated_at` GORM writes on every association append — turns N concurrent
  transactions into one success and N-1 retries, each of which re-does all of
  its work.
- Applications written for a lock-based engine expect `SELECT ... FOR UPDATE`
  to **serialise** the second reader behind the first, not to be accepted and
  ignored. go-mysql-server, the engine underneath, discards the clause at plan
  build time.
- Some ORMs cannot retry a 1213 at all, because the conflicting work happened
  inside a framework-owned transaction (GORM's many-to-many `Append`).

## Shape chosen

Two concurrency modes, selectable per database and per session, over one
replicated lock table.

| Mode | Plain DML | `FOR UPDATE` / `FOR SHARE` | Commit |
|---|---|---|---|
| `optimistic` (default) | takes no locks | takes real locks | CAS on the write set |
| `pessimistic` | takes exclusive row locks implicitly, with **current reads** | takes real locks | CAS on the write set |

The default is `optimistic`, so a database nobody configures behaves exactly as
it did before this feature existed.

**The commit-time compare-and-swap stays in both modes.** Locks reduce how
often a commit loses; they never replace commit validation. A writer the lock
table does not see — an optimistic-mode session, a non-SQL KV write, a holder
whose lease expired during a long pause — is still caught by the CAS, and the
loser still gets 1213.

### Lock modes

| Mode | Taken by | Compatible with |
|---|---|---|
| shared (`S`) | `SELECT ... FOR SHARE`, `LOCK IN SHARE MODE`, PostgreSQL `FOR KEY SHARE` | `S` |
| exclusive (`X`) | `SELECT ... FOR UPDATE`, PostgreSQL `FOR NO KEY UPDATE`; in pessimistic mode every `UPDATE`, `DELETE`, `INSERT`, `REPLACE` | nothing |

A transaction never conflicts with itself: re-requesting a lock it holds is
free, and requesting `X` on a row it holds `S` on is an **upgrade** that queues
behind every other transaction ahead of it on that row.

### Key granularity

The lock unit is **one physical key**:

- a relational table's **row key** — the row's storage key under the table's
  current generation (see [../sql/engineering.md](../sql/engineering.md#truncate-table-generation-scoped-keys-o1-regardless-of-table-size));
- in pessimistic mode, an `INSERT` or key-changing `UPDATE` also locks each
  **UNIQUE index entry key** it is about to occupy, so two sessions inserting
  the same unique value serialise instead of racing.

**Gap locks and next-key locks are out of scope.** A locking read locks the
rows it reads; it does not lock the absence of rows. A row inserted into a
locked range after the read is a phantom the lock does not prevent. A
transaction that must exclude phantoms uses `SET qrdb_isolation =
'written+reads'`, which refuses range reads rather than pretending to cover
them (see [../sql/engineering.md](../sql/engineering.md#tuning-conflict-detection)).

Locking applies to **relational tables** — the tables `CREATE TABLE` produces.
The `kv`, entity and collection tables are autocommit-only and have no
transaction for a lock to outlive; a lock clause on them takes no lock.

### Which rows a locking read locks

go-mysql-server builds the same plan with or without a lock clause, so QRDB
reads the clause from the statement itself: the statement text on the query
context is parsed once (and cached), and every query block that carries a lock
clause contributes the tables in its own `FROM` list. `FOR UPDATE OF t1`
narrows that to the named tables or aliases. A subquery without its own
clause locks nothing, exactly as in MySQL.

Within such a table, the rows locked are the rows the table **returns to the
engine**: the looked-up keys for a primary-key or unique point lookup, the
rows matching the pushed-down range for an index lookup, and **every row** for
a full scan (the engine filters above the table). That is InnoDB's behaviour
for an unindexed predicate too: lock what you examine.

### Current reads

A locked row is read at the **latest committed version after the lock is
granted**, not at the transaction's snapshot. That is what makes a lock
useful: the second of two `UPDATE ... SET n = n + 1` waits for the first to
commit and then increments the committed value instead of failing its CAS.
The revision observed at that read becomes the row's commit precondition, and
every later read of the row in the same transaction is served from it (or from
the transaction's own buffered write). Rows the transaction reads without a
lock keep coming from its snapshot.

## The lock table

The lock table is **replicated through Raft as ordinary records in the
database's own store**, under the internal prefix `__sys:rowlock/`. There is no
node-local lock map and no new Raft command: every lock operation is an
existing transaction, lease or watch operation, which is why a leader change
loses nothing — the records and the leases they are bound to are replicated
state.

```
Lock table = lease (holder liveness)  +  CAS transaction (enqueue)  +  revision order (FIFO)  +  watch (wake-up)
```

This is the composition [../distributed-locks/](../distributed-locks/) already
uses, extended with a queue and a lock mode.

### Records and FIFO order

Every lock request — granted or waiting — is one record keyed by the row key,
the requesting transaction, and the mode. A request is enqueued by writing its
record; its position in the row's queue is the record's **create revision**,
which Raft assigns in commit order. The queue is therefore FIFO and identical
on every replica.

**A request is granted when every earlier record on the row that belongs to a
different transaction is compatible with it.** Records are only ever appended
at the tail or deleted, so a granted request can never become ungranted — a
new request always sorts after it. A shared request behind a waiting exclusive
one waits too: FIFO, no starvation of writers.

### Lease binding

A transaction's first lock grants a lease (reusing the [lease](../leases/)
subsystem) and attaches every lock record it ever writes to that lease. The
session keeps the lease alive while the transaction is open.

- `COMMIT` and `ROLLBACK` **revoke the lease**, which deletes every one of the
  transaction's lock records in one replicated operation.
- A session that crashes, or whose node is lost, stops renewing; the leader's
  expiry sweep deletes its records when the lease runs out, and its waiters
  wake.
- A lost lease never silently grants the transaction anything: the CAS at
  commit is still there (see above).

### Wait queue and wake-up

A request that is not granted waits on a **watch of its row's queue prefix**,
started at the revision right after its own record. Only deletions can change
whether it is granted, so every delete event re-evaluates the grant rule; a
grant, a timeout, a cancellation or a deadlock ends the wait. On a timeout or
a cancelled statement the request deletes its own **waiting** records, so it
stops blocking the requests queued behind it; locks the transaction already
holds stay held.

### Deadlock detection

A waiting request's edges in the wait-for graph are the earlier, incompatible
records of other transactions on its row. Edges are only ever created by an
enqueue, so a cycle is always closed by the **newest** request in it. The
requesting node therefore checks for a cycle right after it enqueues and before
it waits, from a linearizable read of the database's lock table; the request
that closes the cycle is the **victim**. Two requests that close the same cycle
at the same instant can both see it and both abort — detection errs toward an
extra abort, never toward a missed deadlock.

The victim's **whole transaction is rolled back** and its lease revoked, as in
InnoDB, and the client receives MySQL **1213** / PostgreSQL **40P01**.

Detection reads at most a bounded number of lock records. A database whose
lock table is larger than that bound treats the requester as the victim rather
than wait on a graph it did not fully read — InnoDB applies the same rule to a
wait-for graph that is too large to search.

#### Cross-shard limits

The detector sees one database's lock table, and that is complete: **a SQL
transaction is confined to one database** (`CrossDatabaseTransactionError`),
and a locking read in a second database is refused the same way, so every
edge of every possible cycle lives in one store. No coordinator is needed and
none exists. A cycle that passes through the **application** — one client
holding a lock on connection A while blocking on connection B — is invisible
to any database engine; the lock-wait timeout ends it.

### Timeouts

Every wait is bounded by the session's lock-wait timeout — MySQL's
`innodb_lock_wait_timeout`, PostgreSQL's `lock_timeout` — defaulting to 50
seconds on both wires. A timeout fails **only the statement** (MySQL
**1205**, PostgreSQL **55P03**); the transaction stays open and keeps the locks
it already holds, matching InnoDB's default. `NOWAIT` is a wait of zero
(MySQL **3572**, PostgreSQL **55P03**). `SKIP LOCKED` is refused by name.

The statement is failed, not undone. The SQL layer has no implicit
statement-level savepoint, so a multi-row statement that times out after
changing some rows — including a foreign-key cascade that blocks on a child row
after its parent was deleted — leaves those changes in the transaction's
write-set, exactly as any statement that fails part way through (a duplicate
key on its third row, say) already does. Outside a transaction those earlier
rows are already committed, because each autocommit row write is its own
commit. Pessimistic mode adds no new kind of partial effect, but it does add a
new way for a statement to stop part way: a wait.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Lock table: records, grant rule, wait, deadlock detection, lease binding, metrics | `pkg/rowlock` | go-qrdb |
| Lock clause analysis, current reads, implicit DML locks, session variables | `pkg/sql/provider` | go-qrdb |
| `FOR SHARE` rewrite (MySQL wire) | `pkg/sql/engine` | go-qrdb |
| PostgreSQL lock-clause, `SET` and `lock_timeout` translation | `pkg/sql/engine`, `pkg/sql/pgsql` | go-qrdb |
| Per-database `sql.concurrency` | `pkg/database`, `pkg/service`, every transport | go-qrdb |
| Error classification on non-SQL transports | `pkg/api/errclass` | go-qrdb |
| Lease expiry, watch delivery, CAS, revision order | `pkg/lease`, `pkg/watch`, `pkg/service`, `pkg/mvcc` | go-qrdb |
| Consensus underneath every lock record | — | **go-quicraft** |

## Interaction with other features

- **Generation-scoped `TRUNCATE`.** A lock key carries the table's generation,
  so after a `TRUNCATE` new rows live under keys nobody holds a lock on. DDL
  takes no row locks and does not wait for them — there are no metadata locks
  — and the transaction that held locks on the old generation fails its
  commit on the existing generation fence with 1213. Its lock records go with
  its lease, not with the truncated keyspace.
- **The drop fence.** `DROP TABLE` behaves like `TRUNCATE` for lock holders.
  `DROP DATABASE` reclaims the whole namespace, lock records included; a
  waiter whose own record disappears is told its lock request was lost and its
  transaction is rolled back (1213), and a holder's next lock operation or
  commit fails against a database that no longer exists.
- **Isolation levels (`qrdb_isolation`)** tune which keys carry commit
  preconditions; they are orthogonal to the concurrency mode and compose with
  it.
- **Tenancy.** Lock records live in the `<tenant>/<database>/`-namespaced
  store the registry resolves, exactly like rows; two tenants can never see or
  wait on each other's locks.

## Alternatives rejected

- **A state-machine lock subsystem with new Raft commands.** Grant and
  deadlock decisions would be made deterministically inside the apply loop,
  but every new command must sit behind a cluster feature gate that does not
  exist, every state-machine type would need the subsystem, and the lock table
  would need its own snapshot format and a hook into lease expiry. The records
  above need none of that and reuse primitives that are already proven.
- **A node-local lock map.** A leader change, or simply two sessions on two
  nodes, would see two different lock tables.
- **Replacing the commit CAS with locks in pessimistic mode.** A lease can
  expire under a paused holder, and not every writer takes locks. The CAS is
  what keeps those cases correct.
- **Gap and next-key locks.** They would make range locks a property of index
  key encodings and multiply the lock records a scan writes; `written+reads`
  already gives phantom-sensitive transactions an honest refusal instead.
- **Transparent retry of a lost commit.** The write set holds operations, not
  statements; replaying `SET n = n + 1` would apply a stale value.

## What this feature deliberately does not do

- No gap, next-key, predicate, table or metadata locks.
- No lock escalation: a full-scan `FOR UPDATE` locks every row it returns.
- No `SKIP LOCKED`.
- No lock on the `kv`, entity or collection tables.
- No cross-database lock or deadlock detection — a transaction cannot span
  databases.
- Optimistic-mode DML does not wait for locks; a race between it and a lock
  holder is settled by the commit CAS, so the holder can still receive 1213.
