# Row Locking — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

QRDB's SQL transactions are optimistic by default: two transactions that write
the same row both run, and the second to commit fails with error 1213 and must
be retried. Row locking lets you trade that for **waiting**: the second
transaction blocks until the first finishes, then carries on with the committed
data.

## When you want it

- Your workload has hot rows — counters, balances, a parent row every child
  insert updates — and you are seeing a lot of 1213s.
- Your application uses `SELECT ... FOR UPDATE` and expects it to serialise
  access, the way it does on MySQL or PostgreSQL.
- Your ORM cannot retry a conflicted transaction for you (GORM's many-to-many
  association `Append` is one).

If none of that applies, leave the default alone: optimistic transactions cost
fewer round trips.

## Two switches

**`FOR UPDATE` and `FOR SHARE` always lock**, in either mode. Nothing to turn on.

**Pessimistic mode** additionally makes every `UPDATE`, `DELETE` and `INSERT`
lock the rows it touches. Turn it on for a whole database:

```bash
qrdb database update-sql shop --concurrency pessimistic
```

or for one session:

```sql
SET qrdb_concurrency = 'pessimistic';
```

`SET qrdb_concurrency = 'database'` goes back to whatever the database is set to.

With the in-process driver or GORM, put it in the DSN so every pooled connection
gets it:

```go
db, err := gorm.Open(qrdbgorm.Open("node://memory?database=shop&concurrency=pessimistic"), &gorm.Config{})
```

## Common tasks

**Serialise a read-modify-write.**

```sql
BEGIN;
SELECT balance FROM accounts WHERE id = 1 FOR UPDATE;   -- a second session blocks here
UPDATE accounts SET balance = balance - 10 WHERE id = 1;
COMMIT;                                                  -- ...and continues here
```

The second session's `SELECT` returns the balance the first one committed, not
the balance it would have seen when its own transaction started.

**Share a row with other readers but keep writers out.**

```sql
SELECT * FROM products WHERE id = 7 FOR SHARE;           -- PostgreSQL, MySQL 8 spelling
SELECT * FROM products WHERE id = 7 LOCK IN SHARE MODE;  -- MySQL 5.7 spelling
```

**Fail fast instead of waiting.**

```sql
SELECT * FROM jobs WHERE id = 3 FOR UPDATE NOWAIT;       -- error 3572 / 55P03 if someone holds it
```

**Wait less (or more).**

```sql
SET innodb_lock_wait_timeout = 5;   -- MySQL: seconds
SET lock_timeout = '5s';            -- PostgreSQL
```

The default is 50 seconds on both wires. A timeout fails only the statement
that waited; your transaction is still open and still holds the locks it had.

Inside a transaction, a statement that changed several rows before it timed
out is undone whole — none of its changes stay — and your earlier statements
are kept. Outside a transaction it is not: each row it wrote before the timeout
is already committed. Put a multi-row statement inside a transaction when it
must be all-or-nothing.

## Troubleshooting

**Error 1213 (or 40P01 on PostgreSQL) "deadlock".** Two transactions each held
a row the other wanted. QRDB rolled yours back and released its locks. Every
further statement in that transaction fails until you `ROLLBACK` — then run the
whole transaction again. To avoid deadlocks, lock rows in a consistent order
(for example, by primary key).

**Error 1213 without a deadlock.** The commit-time check still runs in both
modes. Something wrote the row without taking a lock — a session in optimistic
mode, a non-SQL writer, or a lock holder whose lease expired during a long
pause — and your commit lost. Retry the transaction.

**Error 1205 / 55P03 "lock wait timeout".** Another transaction held the row for
longer than your timeout. Find long-running transactions; raise the timeout only
if the wait is expected.

**`SKIP LOCKED` is refused.** It is not supported. Use `NOWAIT` and retry
another row.

**`FOR UPDATE` over a whole table is slow.** Without a usable index, a locking
read locks every row it reads, exactly as InnoDB does. Add an index on the
predicate so the read — and the lock — touches only the rows you mean.

**A new row appeared in a range you had locked.** Locks cover rows, not gaps.
If phantoms matter, use `SET qrdb_isolation = 'written+reads'`, which refuses
range reads rather than silently missing them.

**`FOR UPDATE` on the `kv` table or a collection does nothing.** Those tables
are autocommit-only; there is no transaction to hold a lock. Use a relational
table.

**A crashed client's locks.** They are bound to a lease and go away about ten
seconds after the client stops renewing it. You never have to clean them up.
