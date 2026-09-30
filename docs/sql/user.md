# SQL — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

You can talk to the store with `mysql`, `psql`, a `database/sql` program, GORM,
or the `qrdb sql query` verb. All of them run through the same engine.

## Prerequisites

- A daemon built with SQL support. `WITH_QRDB_SQL=1` gives you the engine and
  the ad-hoc endpoint; `WITH_QRDB_MYSQL=1` and `WITH_QRDB_PGSQL=1` add the wire
  servers. Check with `qrdb admin capabilities` — `sql`, `mysql`, `pgsql`.
- **Client certificates.** Both wire protocols are mTLS-only; there is no
  password to set. Your certificate's CN becomes your username and its OU
  becomes your tenant.
- The server started with TLS and auth enabled. It refuses to start a SQL
  listener without them.

## First run

The quickest path is the CLI verb, which needs no SQL client installed:

```bash
qrdb sql query "SELECT * FROM kv"
qrdb sql query --database mydb "SELECT * FROM kv WHERE key = 'foo'"
```

With a real client:

```bash
mysql --ssl-ca=ca.pem --ssl-cert=client.pem --ssl-key=client-key.pem \
      -h db.example.com -P 3306 qrdb

psql "host=db.example.com port=5432 dbname=qrdb sslmode=verify-full \
      sslrootcert=ca.pem sslcert=client.pem sslkey=client-key.pem"
```

## Common tasks

**Query the built-in key-value table.** Every database has a `kv` table with a
`key` and a `value`.

```sql
SELECT * FROM kv WHERE key = 'config/db';
INSERT INTO kv (key, value) VALUES ('config/db', x'0102');
```

Your tenant prefix is added and stripped for you — you see and write the key
you asked for.

**Create real tables.**

```sql
CREATE TABLE users (
    id    BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    name  VARCHAR(255)
);
CREATE INDEX idx_email ON users (email);
```

`AUTO_INCREMENT` values are cluster-wide and handed out in blocks. **Gaps are
normal; duplicates are impossible.** If you need consecutive ids within one
multi-row `INSERT`, this is not the store for that guarantee.

`ALTER TABLE users AUTO_INCREMENT = 0` restarts the counter at 1, and every
session — on any node — stops using ids it had reserved before the restart:
its next generated id comes from the restarted counter, so two concurrent
inserts never both receive the same new id. Rows already in the table are
not renumbered and the counter is not checked against them: an
`AUTO_INCREMENT = 0` on a table that still holds id 1 means the next insert
fails with a duplicate-key error (MySQL would instead move the counter past
the largest id).

**Use transactions.**

```sql
BEGIN;
INSERT INTO accounts (id, balance) VALUES (1, 100);
SAVEPOINT sp1;
UPDATE accounts SET balance = balance - 10 WHERE id = 1;
ROLLBACK TO SAVEPOINT sp1;
COMMIT;
```

`ROLLBACK` is free — nothing was sent until `COMMIT`.

**Re-create tables from v0.20.0 through v0.21.0.** A relational table created
by one of those releases is refused with **MySQL error 1459** (SQLSTATE 55000
on the PostgreSQL wire): its data was written under a key layout this version
does not read, and there is no migration. `DROP TABLE` it, `CREATE TABLE` it
again, and reload its rows from your own source of truth.

**Retry a conflicted transaction.** Concurrent writers to the same row make one
of them lose at `COMMIT` with **MySQL error 1213**, which is exactly the code
your driver or ORM already treats as "retry". A statement whose table was
truncated, dropped or altered while it ran fails the same way, as do the REST
(409), gRPC (`ABORTED`) and QUIC (`StatusAborted`) endpoints. Retry by **re-running the
statements**, not by re-issuing `COMMIT`: the server buffered operations, not
your SQL, so replaying `SET n = n + 1` would re-apply a stale value.

**Tighten conflict detection for one session.**

```sql
SET qrdb_isolation = 'written+reads';
SELECT @@qrdb_isolation;
```

This catches write skew that the default misses, at the cost of a bigger
compare set — and it refuses range reads outright rather than pretending to
cover them. It is session-scoped; there is no global form.

**Grant table access.**

```sql
GRANT SELECT, INSERT ON shop.orders TO 'alice'@'%';
GRANT SELECT (id, total) ON shop.orders TO 'bob'@'%';
SHOW GRANTS FOR 'alice'@'%';
REVOKE INSERT ON shop.orders FROM 'alice'@'%';
```

**Understand the latch before you do that.** A database with no privilege
catalog is unrestricted. **The first privilege statement arms it, and from that
moment it is default-deny for every session.** `REVOKE` does not disarm it and
nothing else does — the posture only ever tightens. Grant yourself what you
need *before* you arm it, or arm it from inside the system tenant, which is
permanently exempt.

**Use GORM or `database/sql` in process.** See [gorm.md](gorm.md); the short
version is that `node://` and `embedded://` DSNs differ in whether the
relational catalog is reachable.

**Write to a document collection.** A collection
([../collections/user.md](../collections/user.md)) appears as a table whose
first column is `_id`, and that column means what it says: name it and you are
claiming that document ID, omit it (or write `0`) and the collection allocates
one.

```sql
INSERT INTO events (_id, kind) VALUES (42, 'deploy');   -- claim 42
INSERT INTO events (kind)      VALUES ('deploy');       -- let it allocate
INSERT INTO archive (_id, kind) SELECT _id, kind FROM events;  -- ids preserved
```

A claim on an ID another document already holds is refused as a duplicate key
(1062, or `SQLSTATE 23505`), not silently replaced; `REPLACE INTO` and
`INSERT … ON DUPLICATE KEY UPDATE` do the replacing. `UPDATE … SET _id = N`
moves a document, and is refused with nothing destroyed if `N` is taken.

## Troubleshooting

**"Access denied" (1044 / 1142 / 1143) right after your first `GRANT`.** You
armed the database. It was unrestricted until that statement and is
default-deny now. Grant the rest of what you need from an identity that already
has `GRANT OPTION` at the database tier, or from the system tenant.

**Your grants seem to have no effect.** Privileges are enforced for non-system
tenants only. If everything is running as the system tenant, the deployment is
unrestricted by design.

**`GRANT ... WITH GRANT OPTION` on one table does not let you re-grant it.**
Administering privileges requires `GRANT OPTION` at the **database** tier. The
parser attaches no target to `GRANT`, so no finer tier can be checked. This is
stricter than MySQL, deliberately.

**A `GRANT` was refused by name.** Server-wide privileges, routine and trigger
privileges, `ON FUNCTION`/`ON PROCEDURE`, `ON *.*`, and host-qualified grantees
other than `'user'@'%'` cannot be represented, so they are refused rather than
stored and never enforced. QRDB identities are certificate subjects and have no
host.

**pgx or GORM fails with "cached plan must not change result type"
(SQLSTATE 0A000).** Something ran DDL that changed a prepared statement's
result shape while a client was still decoding against the old descriptor. The
refusal is deliberate — writing those rows would silently land values in the
wrong fields. Clients recover on their own: pgx drops the statement from its
cache and a retry re-parses.

**A `CREATE TABLE` was rejected for its column type.** Spatial types and
anything else the type mapper cannot represent are refused at DDL time rather
than surfacing later as a decode failure.

**An `INSERT` into a collection table now fails with a duplicate key where it
used to succeed.** It was previously discarding the `_id` you supplied and
storing the document under a generated one, which is why it never collided —
so nothing that statement inserted landed where you asked. Re-check the IDs
before assuming the new error is wrong.

**A collection table's `PRIMARY KEY` under two concurrent writers.** It holds:
the insert behind it is a guarded write, so one writer is told it stored and
the other gets the duplicate-key error. `REPLACE`, `ON DUPLICATE KEY UPDATE` and
`UPDATE` are guarded writes too: two of them racing on one `_id` never lose a
write silently — the loser fails with **1213** (serialization failure) and
should retry. A read-modify-write spread over several statements is still not
held together, because a collection table has no transactions; use a relational
table inside a transaction for that.

**A write inside `BEGIN` failed with "non-transactional table".** The `kv`,
entity and collection tables are autocommit-only — they have no write buffer.
The failure is loud on purpose: silently escaping the transaction's atomicity
boundary and surviving a later `ROLLBACK` would be worse.

**Your DDL committed a transaction you did not mean to end.** DDL implicitly
commits, exactly as in MySQL.

**"cross-database transaction" error.** One commit targets one Raft shard, so a
transaction cannot span two databases. Split it.

**A long-running transaction failed with "snapshot expired".** Its pinned MVCC
revision was compacted before it committed. It fails rather than silently
re-reading at a newer revision. Shorten the transaction.

**A big statement failed on a size limit.** Ceilings are per statement and are
checked up front: 65535 bytes per encoded key, 4 MB per encoded row, 8 MiB for
one statement's compares plus operations. A transaction may buffer at most
8 MiB and 100,000 distinct keys, and exceeding either fails the *buffering
statement*, naming it, rather than surprising you at `COMMIT`.

**`SET TRANSACTION ISOLATION LEVEL` seems to do nothing.** It is accepted and
recorded, not interpreted. Those levels describe a lock-based engine; QRDB's
transactions are optimistic. Use `qrdb_isolation` instead.

**Your PostgreSQL client cannot introspect the schema.** The `pg_catalog`
subset covers `pg_type`, `pg_class`, `pg_namespace`, `pg_description` and
`pg_indexes`. `pg_indexes` reports a primary key as `<table>_pkey`, which is
what clients compare against. Anything outside that subset is not served, and
`pg_catalog` is never served to MySQL clients.

**Something works on MySQL and not on PostgreSQL.** PostgreSQL is the newer
path and translation is targeted rather than exhaustive. Check
[upstream-gaps.md](upstream-gaps.md) for known engine defects before assuming
it is your query.
