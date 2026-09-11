# SQL — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Design and boundaries are in [architecture.md](architecture.md). This file is
the implementation contract: schemas, invariants, error codes, limits and the
test surface.

## Exported surface

```go
// sdk/go — ad-hoc query
func (c *Client) ...  // reached through the transport's SQLClient
```

`sdk/go/transport`:

```go
type SQLClient interface { /* ad-hoc SQL query */ }
type SQLProvider interface { /* transport capability probe */ }
```

In-process paths, both gated on `qrdb_sql`:

- `sdk/go/driver` — a `database/sql` driver. Its connection owns exactly one
  `provider.QRDBSession` for its lifetime, which is what makes transactions
  work on it.
- `sdk/go/gorm` — the GORM dialector. See [gorm.md](gorm.md) for the two
  in-process DSN schemes (`node://`, `embedded://`), which differ in exactly
  whether the relational catalog is reachable.

## Configuration

```yaml
server:
  mysql_port: 3306
  pgsql_port: 5432

protocols:
  mysql: true
  postgresql: true

sql:
  default_database: "qrdb"
  max_connections: 100
  query_timeout: "30s"

tls:
  enabled: true        # Required for SQL protocols
  ca_file: "ca.pem"
  cert_file: "server.pem"
  key_file: "server-key.pem"
  client_auth: "require_and_verify"

auth:
  enabled: true        # Required for SQL protocols
```

**Validation:** TLS and auth must be enabled when MySQL or PostgreSQL protocols are active.


## KV Table Schema

```sql
CREATE TABLE kv (
    key   VARCHAR(65535) PRIMARY KEY,
    value BLOB NOT NULL
);
```

## SQL Operations

| SQL | Service Call |
|-----|-------------|
| `SELECT * FROM kv` | `kvSvc.Scan(ctx, tenantPrefix)` |
| `SELECT * FROM kv WHERE key = ?` | `kvSvc.Get(ctx, prefixedKey)` |
| `INSERT INTO kv (key, value) VALUES (?, ?)` | `kvSvc.Put(ctx, prefixedKey, value)` |
| `UPDATE kv SET value = ? WHERE key = ?` | `kvSvc.Put(ctx, prefixedKey, value)` |
| `DELETE FROM kv WHERE key = ?` | `kvSvc.Delete(ctx, prefixedKey)` |


## Collection Table Schema

A document collection ([../collections/](../collections/)) is exposed as a
table whose **column 0 is `_id`** — `BIGINT UNSIGNED`, `PRIMARY KEY`,
`DEFAULT 0` — followed by one nullable column per field in the collection's
auto-discovered schema, sorted by name.

```sql
CREATE TABLE events (
    _id  BIGINT UNSIGNED PRIMARY KEY DEFAULT 0,
    kind VARCHAR(65535) NULL
);
```

`_id` is the document's identity, not one of its fields, so a field of that
name discovered in the collection's stored schema is **skipped** when the
columns are built (`pkg/sql/provider.buildCollectionSchema`): the identity is
declared exactly once. Collections written before the service stopped storing
`_id` as a field still carry the key in their metadata, which is why the
projection guards it rather than assuming.

| SQL | Service call |
|-----|--------------|
| `SELECT * FROM <collection>` | `collectionSvc.ListDocuments` |
| `INSERT INTO <collection> (_id, …) VALUES (N, …)` | `collectionSvc.InsertDocument` with `_id = N` — a **claim** |
| `INSERT INTO <collection> (…) VALUES (…)` | `collectionSvc.InsertDocument` with no `_id` — an **allocation** |
| `UPDATE <collection> SET … WHERE _id = N` | `collectionSvc.UpdateDocument` |
| `UPDATE <collection> SET _id = M WHERE _id = N` | `InsertDocument` claiming `M`, then `DeleteDocument(N)` — refused with `InvalidKeyError` when `M` is 0 |
| `DELETE FROM <collection> WHERE _id = N` | `collectionSvc.DeleteDocument` |
| `REPLACE INTO <collection> …` | `InsertDocument`; on a primary-key violation, `DeleteDocument` then `InsertDocument` |
| `INSERT … ON DUPLICATE KEY UPDATE` | `InsertDocument`; on a primary-key violation, `UpdateDocument` |

### `_id` on INSERT

An `_id` named in an `INSERT` is honoured, exactly as it is on every other
transport: it reserves an `increment` collection's counter and is **refused**
if another document already holds it. A zero or omitted `_id` is not a claim —
the collection allocates from its own ID strategy, which is what the column's
`DEFAULT 0` expresses (MySQL's convention for an `AUTO_INCREMENT` column, and
already how the service reads a zero `_id`).

The refusal is reported as `sql.NewUniqueKeyErr("PRIMARY", true, existingRow)`,
so it reaches a client as **MySQL 1062** / **PostgreSQL `SQLSTATE 23505`** /
`gorm.ErrDuplicatedKey`, the same as a relational table's primary key. That is
not only cosmetic: go-mysql-server implements `REPLACE INTO` and
`INSERT … ON DUPLICATE KEY UPDATE` by inserting, catching that specific error,
and feeding its `Existing` row into a delete or an update — which is why both
statements are in the table above, and why the row the writer supplies must
match the table's own column order (`documentToRow`, shared with the read
path).

**The key is enforced by a check, not by a compare-and-swap.**
`CollectionService.InsertDocument` reads the document key and then writes, so
the constraint closes the ordinary overwrite and not a race between two
concurrent claims. This is the one respect in which a collection table's
`PRIMARY KEY` is weaker than a relational table's, whose uniqueness rides on a
`TransactionServicer.Execute` compare in the same proposal as the row (see
*Relational Tables* below). `REPLACE` and `ON DUPLICATE KEY UPDATE` are built
on the refusal and inherit the same bound, and a collection table is
autocommit-only so neither can be widened into a transaction.

## Index-Accelerated WHERE Clauses

When a KV table has secondary indexes, the SQL analyzer automatically uses them to accelerate WHERE clauses.

```sql
-- If "email" is indexed:
SELECT * FROM kv WHERE key LIKE 'user:%' AND value LIKE '%alice%';
-- Uses email index for user: prefix, then post-filters value

-- Range queries:
SELECT * FROM kv WHERE key >= 'user:100' AND key <= 'user:200';
-- Uses key index (if available) for range scan
```

The query optimizer ([go-mysql-server](https://github.com/dolthub/go-mysql-server) Analyzer) examines the indexes a table reports through `GetIndexes` and selects the lowest-cost plan.

### Creating indexes

**On the `kv` table, and on entity/collection tables: index DDL is not supported.** `CREATE INDEX` and `DROP INDEX` fail during analysis with go-mysql-server's `ErrNotIndexable`, because these table implementations do not implement `sql.IndexAlterableTable`.

Indexes on these tables are created out of band, through the index API rather than through SQL:

```bash
qrdb index create email_idx --field email
qrdb index list
qrdb index drop email_idx
```

The same operations are available on every transport via `index.IndexServicer` (`CreateIndex`, `DropIndex`, `ListIndexes`). Once an index exists, the SQL layer discovers it automatically and the optimizer will use it — index *use* from SQL works; index *creation* from SQL does not.

**On user-defined relational tables (see "Relational Tables" below), index DDL is supported**: `CREATE INDEX`, `DROP INDEX`, and `ALTER TABLE ... RENAME INDEX` all work. `CREATE INDEX` on a non-empty table populates the index from existing rows (`sql.IndexBuildingTable`) rather than leaving it empty until the next write.


## Relational Tables

Beyond the fixed `kv` table, QRDB supports user-defined relational tables created with standard `CREATE TABLE` DDL, backed by `pkg/service.TableServicer` (the catalog), `pkg/service.SequenceServicer` (`AUTO_INCREMENT` and hidden row ids) and `pkg/sql/catalog` (schema, row, and key codecs). This is a distinct code path from the `kv`/entity/collection tables described above: `pkg/sql/provider.RelationalTable` (read path in `relational_table.go`, DDL in `relational_table_ddl.go`, DML in `relational_writer.go`, foreign keys in `relational_foreign_key.go`) and `pkg/sql/provider.Database`'s `CreateTable`/`CreateIndexedTable`/`DropTable`/`RenameTable`/`GetCollation`/`SetCollation`/`ValidateSchema` methods (`relational_ddl.go`).

Rows are stored with a **binary** row codec (`catalog.RowCodec`), not as JSON documents the way `entity_table.go` stores entities: JSON round-trips `int64` through `float64` (silent loss above 2^53) and cannot represent `DECIMAL` exactly. Primary keys are encoded with an **order-preserving** codec (`catalog.KeyCodec`), so a prefix scan of the table's data prefix returns rows in primary key order and a leading-prefix match on a composite key is a bounded scan rather than a full scan.

```sql
CREATE TABLE users (
    id    BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    email VARCHAR(255) NOT NULL,
    name  VARCHAR(255)
);

ALTER TABLE users ADD COLUMN active TINYINT(1) NOT NULL DEFAULT 1;
ALTER TABLE users DROP COLUMN name;
ALTER TABLE users MODIFY COLUMN email VARCHAR(320);
ALTER TABLE users RENAME COLUMN active TO is_active;

CREATE INDEX idx_email ON users (email);
ALTER TABLE users RENAME INDEX idx_email TO idx_user_email;
ALTER TABLE users DROP INDEX idx_user_email;

RENAME TABLE users TO accounts;
DROP TABLE accounts;
```

Key properties of this DDL:

- **Row and index keys are keyed by an immutable `TableID`, never by name.** `RENAME TABLE` is therefore a pure catalog compare-and-swap: no row moves.
- **`ADD COLUMN` is metadata-only.** `TableDef.Columns` is append-only, so a row encoded before the `ADD COLUMN` is a byte-for-byte prefix of the current column list; `catalog.RowCodec.Decode` fills the trailing column(s) from their declared default (or `NULL`) without rewriting any row.
- **`DROP COLUMN` and `MODIFY COLUMN` rewrite every row**, re-encoding it at a new schema version. The rewrite is journaled (`TableDDLJournal`) so a crash mid-rewrite is resumable: the very next query against the table detects the journal and finishes the rewrite before serving it, rather than leaving the table permanently stuck.
- **`DROP TABLE` reclaims data.** Row and secondary-index entries are deleted, chunked across proposals, before the catalog entry itself is removed -- so an interrupted `DROP TABLE` leaves reclaimable (still-catalogued) data rather than an unreclaimable orphan.
- **Spatial types (`GEOMETRY`, `POINT`, ...) and any other type `catalog.ColumnTypeOf` cannot map are rejected at `CREATE`/`ALTER TABLE` time**, not discovered later as a decode failure.

Relational tables shadow a same-named collection: if both a relational table and a legacy collection exist with the same name, `CREATE TABLE` deterministically wins.

### DML and AUTO_INCREMENT

`INSERT`, `UPDATE`, `DELETE`, `REPLACE` and `TRUNCATE` are implemented: `RelationalTable` implements `sql.InsertableTable`, `sql.UpdatableTable`, `sql.DeletableTable`, `sql.ReplaceableTable` and `sql.TruncateableTable` (`relational_writer.go`). Two properties of the write path are load-bearing.

**A row write and its secondary-index maintenance are one compare-and-swap.** Every `INSERT`/`UPDATE`/`DELETE` builds a single set of compares and ops covering the row key *and* every affected index entry, and proposes them through exactly one `service.TransactionServicer.Execute` call. Index entries live in the table's own keyspace (`service.TableIndexPrefix`), not in `index.IndexServicer`, precisely because `IndexServicer` writes cannot be composed into that CAS — and "check, then write" across two proposals has a race window in which a `UNIQUE` constraint cannot be enforced at all. `UNIQUE` rides on the same CAS: a `Compare` requiring the candidate entry's `Version` to be 0 (absent) is proposed alongside the row, so a colliding write commits *nothing*, not even the row. A lost compare is resolved into go-mysql-server's own `sql.NewUniqueKeyErr` (naming `PRIMARY` or the specific index), which reaches a client as MySQL error **1062**.

**`AUTO_INCREMENT` is Raft-committed and block-allocated.** Values come from `service.SequenceServicer` under `service.TableAutoIncrementSequenceName(TableID)`, handed out in blocks so a multi-row `INSERT` costs one proposal per block rather than one per row. Nothing about the allocation is process-local, so two nodes can never hand out the same value. **Gaps are permitted; duplicates are not** — which is exactly MySQL's own `innodb_autoinc_lock_mode=2` (interleaved) behaviour. The "consecutive values within a single multi-row `INSERT`" guarantee of modes 0/1 is *not* offered (`AcquireAutoIncrementLock` is a no-op, since block allocation already prevents collisions). An explicitly supplied value at or above the counter advances the sequence past it (`SequenceServicer.Advance`), so a later generated value can never collide with one a caller supplied. `ALTER TABLE ... AUTO_INCREMENT = N` is implemented, but — unlike InnoDB — it can only ever raise the counter, never lower it below a value already committed.

`REPLACE INTO` composes for free from the same writer: go-mysql-server's insert loop calls `Insert`, and on the duplicate-key error above calls `Delete` with the *existing* conflicting row before retrying — so the old row and its index entries are removed atomically, then the new row and its own entries are written atomically.

`TRUNCATE` is the one write that is deliberately *not* a single CAS: it reuses `DROP TABLE`'s chunked reclamation, so an interrupted `TRUNCATE` is safely retryable (rows are reclaimed before indexes, and an index entry whose row is already gone is the same benign case every read path already tolerates).

Per-statement ceilings are enforced at the statement, not deferred to an eventual proposal rejection: 65535 bytes for an encoded key, 4 MB for an encoded row, 8 MiB for one statement's estimated compares + ops (`RowKeyTooLargeError`, `RowValueTooLargeError`, `RowTransactionTooLargeError`).

### Tables with no PRIMARY KEY

A `CREATE TABLE` that declares no `PRIMARY KEY` is fully supported, and behaves exactly as it does in InnoDB: the table is physically keyed by a **hidden, monotonically increasing 64-bit row id** that no client can see.

```sql
CREATE TABLE hits (name VARCHAR(64), n BIGINT UNSIGNED);

INSERT INTO hits VALUES ('a', 7);
INSERT INTO hits VALUES ('a', 7);   -- a second, distinct row
SELECT COUNT(*) FROM hits;          -- 2
```

- **Identical rows are distinct rows.** Each `INSERT` allocates its own row id, so nothing collides. There is no cardinality counter and no content hash.
- **The row id is invisible.** It is a go-mysql-server hidden system column (`sql.Column.HiddenSystem`), so it is excluded from `SELECT *`, from an `INSERT`'s implicit column list, from `information_schema.COLUMNS` and `.STATISTICS`, and from `SHOW CREATE TABLE` / `DESCRIBE`. Any statement that names it outright is rejected. `SHOW CREATE TABLE` output stays replayable.
- **A table scan returns insertion order**, because the row id is encoded with the same order-preserving numeric encoding `catalog.KeyCodec` gives any other unsigned key column.
- **`UPDATE` rewrites the row in place.** No `SET` can name the hidden column, so the row id carries through from the old row to the new one; the row keeps its storage key and only genuinely-changed secondary-index entries are touched.
- **Row ids are allocated from a Raft-committed sequence** (`service.SequenceServicer`, under `TableRowIDSequenceName`), in growing blocks so a multi-row `INSERT` costs one proposal per block rather than one per row. Nothing about the allocation is process-local, so two nodes can never hand out the same row id. Gaps are acceptable; duplicates are not.
- **A keyless table may still declare an `AUTO_INCREMENT` column** (backed by a `UNIQUE` index, as MySQL requires). The two counters are separate sequences and never consume each other's values.
- **The row id is not stored in the row.** It *is* the storage key, so a keyless table's encoded row bytes are identical to the same values in a keyed table; the row id is injected back into the hidden slot on read.

One upstream restriction applies: go-mysql-server's `assign_update_join` analyzer rule refuses `UPDATE ... JOIN` when a target table's schema reports no primary key column, so that statement is rejected on a keyless table (`keyless tables unsupported for UPDATE JOIN`). `DELETE ... JOIN` is unaffected.

### Foreign keys

User-defined relational tables support `FOREIGN KEY` constraints, declared inline in `CREATE TABLE` or added later with `ALTER TABLE ... ADD CONSTRAINT`:

```sql
CREATE TABLE companies (id BIGINT PRIMARY KEY, name VARCHAR(64));

CREATE TABLE users (
    id         BIGINT PRIMARY KEY,
    company_id BIGINT,
    CONSTRAINT fk_users_company FOREIGN KEY (company_id)
        REFERENCES companies (id) ON DELETE CASCADE ON UPDATE RESTRICT
);

ALTER TABLE users DROP FOREIGN KEY fk_users_company;
```

**QRDB stores and reports constraint metadata; go-mysql-server enforces it.** `pkg/sql/provider.RelationalTable` implements `sql.ForeignKeyTable`, persisting each constraint on the declaring table's catalog record (`service.TableDef.ForeignKeys`) through the same compare-and-swap every other catalog mutation uses, and exposing a `sql.ForeignKeyEditor` that is the table's ordinary DML writer plus its index addressability. Everything else — parent-existence checks on `INSERT`/`UPDATE`, the `CASCADE`/`SET NULL`/`SET DEFAULT`/`RESTRICT` referential actions, cycle detection and depth limiting — is go-mysql-server's analyzer and plan layer acting on that metadata. There is no second enforcement engine in QRDB.

Properties worth knowing:

- **Violations surface as the standard MySQL errors**: 1452 (`ER_NO_REFERENCED_ROW_2`) for a child row with no parent, 1451 (`ER_ROW_IS_REFERENCED_2`) for a parent row that still has children. `sdk/go/gorm` maps both to `gorm.ErrForeignKeyViolated`.
- **Constraint names are database-scoped**, as in MySQL: adding a constraint whose name is already used by *any* table in the database is rejected with `duplicate foreign key constraint name`. (go-mysql-server itself only checks the declaring table, so this rule lives in `pkg/service.TableService`.)
- **The backing index is built, not just declared.** A constraint added to a non-empty table gets its index populated from the existing rows; an empty index would silently make the constraint reject every child row rather than error.
- **Self-referential constraints work**, and appear on both sides of the reverse lookup.
- **Cross-database and schema-qualified constraints are refused** with a typed error, never stored. Every catalog, row, and index key is scoped to one database, so there is no read path a parent-row check in another database could use.
- **`MATCH SIMPLE` (the default) exempts a row with any `NULL` key column** from the check; `MATCH FULL` is persisted and honoured by the engine.

### Secondary indexes on relational tables

Secondary indexes — including **multi-column (composite)** and `UNIQUE` ones — are declared inline in `CREATE TABLE` or added with `CREATE INDEX` / `ALTER TABLE ... ADD INDEX`, and are maintained by the write path described above.

- **A composite index is keyed by the whole tuple.** `('a','x')` and `('a','y')` occupy two distinct entries and coexist under a `UNIQUE` composite index, exactly as in MySQL. Because `catalog.KeyCodec` concatenates self-delimiting, individually order-preserving component encodings, the encoding of the first *k* columns is a byte-exact prefix of the encoding of all of them — which is what makes a **leading-prefix** lookup (`WHERE a = ? AND b = ?` over an index on `(a, b, c)`) a bounded prefix scan rather than a table scan.
- **A `NULL` in any indexed column contributes no index entry**, which is what lets a `UNIQUE` index over a nullable column hold any number of `NULL`-bearing rows, matching MySQL. It is also why a lookup that leaves a *nullable* index column unbound is not answered from the index.
- **A unique index's entry key carries no primary-key suffix** (at most one row may hold the value, so the value alone is the key — and is therefore directly guardable by a single `Compare`). A non-unique index's entry key is suffixed with the row's encoded primary key so rows sharing a value get distinct physical keys.
- **A prefix-length index (`KEY idx (name(10))`) is written and enforced over the truncated value**, as MySQL does, but is never used to *answer* a lookup: two distinct values sharing a truncated prefix produce the same entry, so an entry match is not a value match.
- **Any predicate the index cannot answer exactly falls back to a scan that re-evaluates the planner's own range bounds** through each column's `sql.Type.Compare` (`filteredScanRows`). The result is always exactly correct — which is what lets `PreciseMatch()` report true — but it is not accelerated.

### Index acceleration on the KV, entity, and collection tables

The KV, entity, and collection tables implement `sql.IndexAddressableTable` and `sql.IndexedTable`, so `WHERE` clauses on indexed fields are served by an index lookup or range scan instead of a full scan. Predicate-to-range translation lives in `pkg/sql/provider/range_translator.go`; both tables report `PreciseMatch() == true`, so go-mysql-server does not re-filter the returned rows.

### Optimizer statistics

`RelationalTable` implements `sql.StatisticsTable`: `RowCount` is an **exact** keys-only MVCC range count over the table's data prefix (`MVCCKVServicer.Range` with `WithCountOnly`), and `DataLength` estimates on-disk size as that count multiplied by the encoded size of an all-zero-value row. There is no maintained histogram or per-index cardinality.

The KV, entity, and collection tables do **not** implement `sql.StatisticsTable`, so the optimizer has no row-count estimate for them and falls back to its default heuristics. `information_schema.statistics` is served by go-mysql-server from the indexes each table reports; the `CARDINALITY` column is not populated from real statistics for any table kind.


## Transaction Control

`BEGIN` / `START TRANSACTION`, `COMMIT`, `ROLLBACK`, `SAVEPOINT`, `ROLLBACK TO SAVEPOINT` and `RELEASE SAVEPOINT` are **real** on user-defined relational tables. `provider.QRDBSession` implements `sql.TransactionSession`, so go-mysql-server's transaction plan nodes route through it instead of silently returning an empty iterator.

```sql
BEGIN;
INSERT INTO accounts (id, balance) VALUES (1, 100);
SAVEPOINT sp1;
UPDATE accounts SET balance = balance - 10 WHERE id = 1;
ROLLBACK TO SAVEPOINT sp1;
COMMIT;
```

### The isolation model

An open transaction buffers its writes in a `provider.WriteSet` and **commits the whole buffer as ONE `TransactionServicer.Execute` compare-and-swap.** QRDB's KV layer offers one-shot etcd-style CAS rather than a native interactive transaction primitive, and the write-set is the bridge.

- **Snapshot reads.** The MVCC revision is pinned the moment the first relational table participates. Every read inside the transaction is served from the buffer first (read-your-own-writes), then from that pinned revision.
- **First-committer-wins.** Each key the transaction *writes* carries one commit-time precondition — the `ModRevision` (or absence) it had at the pinned snapshot. A concurrent transaction that modified the same key after the snapshot makes this transaction's commit fail atomically.
- **Compares cover the write set, not the read set.** A read-set compare would grow without bound across a large `SELECT` and exceed the proposal's operation ceiling almost immediately. This is the deliberate trade: write-write conflicts are detected, read-write ones are not.
- **`ROLLBACK` is free** — nothing was ever proposed, so discarding the buffer *is* the rollback. `SAVEPOINT` marks a position in the write-set's undo log; `ROLLBACK TO SAVEPOINT` replays it in reverse and discards any savepoint marked after it, matching MySQL.
- **Limits.** A transaction may buffer at most 8 MiB and 100,000 distinct keys; exceeding either fails the *buffering statement* (`WriteSetLimitError`), naming it, rather than deferring the failure to `COMMIT`. If the pinned snapshot is compacted before commit, the transaction fails with `TransactionSnapshotExpiredError` rather than silently downgrading to a fresh read.
- **One database per transaction.** A single CAS commit targets one Raft shard, so a write to a second database inside an open transaction is refused with `CrossDatabaseTransactionError` rather than split into two non-atomic commits.
- **`START TRANSACTION READ ONLY`** is honoured: any buffered write returns `ReadOnlyTransactionError`.

### Tuning conflict detection

The scope of that write-set compare is selectable per session:

```sql
SET qrdb_isolation = 'none';          -- last-writer-wins: no preconditions
SET qrdb_isolation = 'written';       -- the default, described above
SET qrdb_isolation = 'written+reads'; -- also compare every point read
SELECT @@qrdb_isolation;
```

`written+reads` closes the write skew `written` misses — the classic case where
two transactions each read a row the other writes, and both commit — by
attaching a precondition to every key the transaction *read* as well. It pays
for that with a larger compare set, and it refuses a *range* read outright
(`IsolationRangeReadError`) rather than pretending to cover one: a range's
conflict set is the keys that do not exist yet, which a point compare cannot
express.

The variable is session-scoped only; `SET GLOBAL qrdb_isolation` is refused,
because the setting is a property of one client's workload and a global default
would silently change the isolation of every other connection.

Note what is *not* tunable. `none` does not mean "no atomicity": a row and its
index entries still commit in a single compare-and-swap at every level. And
there is deliberately no "retry automatically" setting — the write-set holds
*ops*, not the statements that produced them, so replaying `SET n = n + 1`
re-applies a stale value and loses the concurrent update. A correct retry has to
re-execute the statements, which only the client (or GORM's transaction
closure) can do.

MySQL's own `transaction_isolation` and `SET TRANSACTION ISOLATION LEVEL` are
left exactly as go-mysql-server already treats them — accepted, recorded, not
interpreted. They name the behaviour of a lock-based engine and QRDB's
transactions are optimistic; the two do not correspond, and aliasing them would
be a straightforward lie. InnoDB's `READ COMMITTED` still prevents a lost update
because the row is locked until commit, which `none` does not; InnoDB's
`REPEATABLE READ` *blocks* a second writer and then applies it, where QRDB
aborts it with 1213.

### Conflicts surface as MySQL 1213

A losing commit clears the write-set (matching InnoDB, which rolls back a transaction that hits `ER_LOCK_DEADLOCK`) and returns a `TransactionConflictError` wrapped in go-mysql-server's `sql.ErrLockDeadlock` — which `sql.CastSQLError` maps to **MySQL error 1213 / SQLSTATE 40001**. That is the code `database/sql` clients and ORM retry loops already recognise as "retry the transaction". A genuine infrastructure error (an unreachable leader, say) leaves the write-set in place instead, so `COMMIT` may safely be retried without losing buffered work.

### What is *not* transactional

- **The `kv`, entity, and collection tables are autocommit-only.** They have no write-set to buffer into, so a write to one of them while a transaction is open fails loudly with `NonTransactionalTableError` rather than silently escaping the transaction's atomicity boundary and surviving a later `ROLLBACK`. Outside a transaction they are entirely unaffected.
- **DDL implicitly commits**, exactly as in MySQL. `CREATE`/`DROP`/`RENAME TABLE`, `ADD COLUMN`, `CREATE`/`DROP`/`RENAME INDEX`, `ADD`/`DROP FOREIGN KEY`, `TRUNCATE` and `ALTER TABLE ... AUTO_INCREMENT = N` all flush any pending write-set before mutating the catalog, and that commit cannot be undone by a later `ROLLBACK`.
- **Isolation levels are accepted, not differentiated.** `sdk/go/driver` maps `database/sql`'s four levels onto `SET TRANSACTION ISOLATION LEVEL` statements the engine accepts, but what a relational table actually provides is the snapshot / first-committer-wins model above.

Both wire protocols and the in-process driver carry this, because transaction state lives on the **session**, not the query context — so every statement of a connection must share one `provider.QRDBSession`. `sdk/go/driver`'s connection owns exactly one session for its lifetime; the MySQL listener builds one per connection; on the PostgreSQL listener the session is created once per connection by a `wire.SessionMiddleware` handler (`pkg/sql/pgsql/parse.go`) and threaded through every statement, since `psql-wire` calls its parse function once per statement with no connection handle. PostgreSQL's `END` / `END WORK` / `END TRANSACTION` — spellings the vitess-derived parser does not accept — are rewritten to `COMMIT` before reaching the engine.

Single-statement atomicity is still provided by Raft for every table kind, and the KV transaction API (`TransactionServicer.Execute`) remains available directly.


## PostgreSQL Syntax Translation

`pkg/sql/engine.Translator` performs targeted, **literal-aware** string-level transformations — not full AST translation. Every transformation re-scans its input for string literals, dollar-quoted strings, and line/block comments (`scanRegions` in `pkg/sql/engine/lexer.go`) and skips them, so text that merely *looks* like a trigger (`ILIKE` inside a comment, `$1` inside a stored string, a reserved word inside a quoted value) passes through byte-for-byte.

| PostgreSQL | GMS Equivalent |
|------------|----------------|
| `$1, $2` | `?, ?` |
| `"identifier"` | `` `identifier` `` |
| bare `key`, `value`, `status`, `name`, `type`, `interval`, `function`, `condition`, `column`, `schema` | backtick-quoted (MySQL reserves them, PostgreSQL does not); compound keywords such as `PRIMARY KEY` are left alone |
| `ON CONFLICT (cols) DO UPDATE SET a = EXCLUDED.a` | `ON DUPLICATE KEY UPDATE a = VALUES(a)` |
| `ON CONFLICT [(cols)] DO NOTHING` | `INSERT IGNORE` |
| `E'escape'` | `'regular'` |
| `value::type` | `CAST(value AS type)` |
| `ILIKE` | `LIKE` |
| `LIMIT ALL` | *(removed)* |
| `LIMIT '5'`, `OFFSET '10'` | `LIMIT 5`, `OFFSET 10` (see below) |
| `'C:\temp'` | `'C:\\temp'` (see below) |
| `INSERT ... RETURNING` | *(unchanged — go-mysql-server parses and evaluates it natively)* |
| `ON CONFLICT ON CONSTRAINT <name> DO ...` | `ON DUPLICATE KEY UPDATE` / `INSERT IGNORE` (same widening caveat as the `(cols)` form) |
| `UPDATE t SET ... FROM others WHERE ...` | `UPDATE t, others SET t.col = ... WHERE ...` (assignments qualified with the target table) |
| `public.name` | `name` (the public schema and the current database are the same level here) |
| `text[]`, `integer[3][3]`, any array type | `LONGTEXT` (`VARCHAR(768)` when keyed) — array VALUES round-trip as their PostgreSQL text literal, which lib/pq/pgx array types write and parse; server-side array operators are not supported |
| `numeric` / `decimal` with no modifier | `DECIMAL(65,30)` (MySQL's bare DECIMAL is (10,0), which silently rounds fractions) |
| serial column outside every key | integer + `DEFAULT (NEXTVAL('<table>_<col>_seq'))` (MySQL allows one AUTO_INCREMENT, and only on a key; the sequence is PostgreSQL's own implicit one) |
| `CREATE TABLE t ()` | `CREATE TABLE t (qrdb_zero_column TINYINT NULL)` (MySQL's grammar refuses an empty column list) |
| `DEFAULT current_timestamp` after a `DATETIME(6)` translation | `DEFAULT CURRENT_TIMESTAMP(6)` (MySQL requires matching precision) |
| `DEFAULT nextval('s')` | `DEFAULT (nextval('s'))` (MySQL expression defaults must be parenthesized) |

Two of those rows are about semantics rather than spelling, and both are
load-bearing:

**Backslashes in string literals.** PostgreSQL, with
`standard_conforming_strings` on (its default since 9.1), treats a backslash
inside `'...'` as an ordinary character; MySQL treats it as an escape
introducer, and go-mysql-server does not honour the `NO_BACKSLASH_ESCAPES`
`sql_mode` that would turn that off. Left untranslated, `'C:\temp'` reached
storage as `C:<tab>emp`. The rewrite doubles each backslash inside plain
literals so the MySQL lexer reproduces PostgreSQL's bytes exactly. `E'...'`
literals are excluded — PostgreSQL's escape-string syntax already processes
backslashes the way MySQL does.

This is also what makes QRDB's `standard_conforming_strings=on`
ParameterStatus honest. Clients act on that parameter: having seen it, pgx
escapes an interpolated value by doubling quotes alone, so without the rewrite
a value ending in a backslash would escape its own closing quote and let the
remainder of the statement be parsed as SQL.

**Quoted row counts.** PostgreSQL types a bare `'5'` as `unknown` and coerces
it to `bigint` in a row-count position; MySQL has no such coercion. This is not
an exotic form — pgx renders every bind parameter as a quoted literal when it
interpolates a simple-protocol query, and GORM issues `LIMIT $1` on every
`First`/`Take`/`Last`. Only an all-digits body is unquoted; anything else is
left for the engine to reject, because PostgreSQL would reject it too.

`UPDATE ... RETURNING` and `DELETE ... RETURNING` are rejected by the
translator with a typed error (the embedded vitess grammar has no RETURNING on
those productions) — but the **PostgreSQL wire serves them anyway**:
`pkg/sql/pgsql` strips the clause, translates the head normally, and reattaches
the parsed expression list to the AST's `Returning` field, which the planner and
executor fully honor. See `pkg/sql/pgsql/returning.go` and
[upstream-gaps.md](upstream-gaps.md) §3.

Rejected with a typed `*engine.UnsupportedSyntaxError` rather than mistranslated:

| Construct | Why |
|---|---|
| `ON CONFLICT ... WHERE ...` | A partial-index conflict target, or a conditional `DO UPDATE`; MySQL has neither. |

One deliberate, documented divergence: because MySQL keys an upsert off *any* violated unique index, a statement relying on PostgreSQL erroring when the conflict is against some *other* unique index will succeed against QRDB where PostgreSQL would have failed. Only the exact-case spellings `ILIKE`/`ilike` are normalized; a mixed-case `IlIkE` is not rewritten.

## PostgreSQL Wire Semantics

Beyond text translation, `pkg/sql/pgsql` implements PostgreSQL semantics the
statement text alone cannot express:

- **SQLSTATEs.** Engine failures are reported under the SQLSTATE PostgreSQL
  raises for the same condition — `23505` unique violation, `23503` foreign
  key, `23514` check, `23502` not-null, `42P01`/`42703` undefined table/column
  — so client error translators (gorm's among them) work unmodified. See
  `pkg/sql/pgsql/wire_errors.go`.
- **`ON CONFLICT DO NOTHING` suppresses only duplicate keys.** The
  `INSERT IGNORE` translation would also swallow FK, CHECK, and NOT NULL
  violations (MySQL semantics); the wire re-raises any such suppressed
  violation as the statement failure PostgreSQL reports.
- **`UPDATE/DELETE ... RETURNING`** via AST reattachment (see above).
- **`DROP TABLE ... CASCADE`** drops the foreign keys other tables aim at the
  dropped table first, as PostgreSQL's CASCADE does.
- **Schema-scoped index DDL.** `DROP INDEX [CURRENT_SCHEMA().]name` and
  `ALTER INDEX name RENAME TO new` resolve the owning table through the
  catalog and become MySQL's table-scoped forms.
- **Sequences.** `nextval('name')` allocates from a Raft-linearizable
  per-database counter (`service.UserSequencer`); sequences materialize on
  first use and start at 1. `CREATE/ALTER SEQUENCE` are accepted (creation is
  implicit), and `DROP SEQUENCE` reclaims the counter — under
  materialize-on-first-use that is indistinguishable from a sequence that was
  never created, so a re-created name starts at 1, exactly the observable
  effect PostgreSQL's drop-and-recreate has. `ADD GENERATED ... AS
  IDENTITY` becomes a sequence default. `information_schema.columns` restates
  such defaults as `nextval('name'::regclass)`, the exact shape PostgreSQL
  clients pattern-match for auto-increment classification.
- **Unique constraint vs. unique index.** PostgreSQL lists only declared
  UNIQUE **constraints** in `information_schema.table_constraints`; a unique
  **index** never appears there. The wire records which one each statement
  created (`service.TableIndex.StatementIndex`, captured per statement) and
  the PostgreSQL dialect's `table_constraints` filters accordingly, while
  `pg_indexes` continues to list both. gorm's pg_index-based per-column index
  introspection is served through the `qrdb_index_columns` relation.
- **Multi-statement simple queries.** Several `;`-separated statements in one
  message execute in order; the last one's result is returned.
- **`uuid_generate_v4()` / `gen_random_uuid()`** are built in;
  `CREATE EXTENSION` is a no-op.


## Error code summary

Failures reach a client as the code its driver already classifies, never as a
QRDB-specific string:

| Condition | Code | Notes |
|---|---|---|
| Duplicate key / unique violation | MySQL **1062** | from `sql.NewUniqueKeyErr`, naming `PRIMARY` or the index |
| Transaction conflict (first-committer-wins loss) | MySQL **1213** / SQLSTATE **40001** | the code ORM retry loops already recognise |
| Database access denied | MySQL **1044** | `ER_DBACCESS_DENIED_ERROR` |
| Table access denied | MySQL **1142** | `ER_TABLEACCESS_DENIED_ERROR` |
| Column access denied | MySQL **1143** | `ER_COLUMNACCESS_DENIED_ERROR` |
| Cached plan result-type change | SQLSTATE **0A000** | `cached plan must not change result type` |
| Foreign key violations | MySQL **1451** / **1452** | see [gorm.md](gorm.md) |

Named Go error types on the QRDB side: `RowKeyTooLargeError`,
`RowValueTooLargeError`, `RowTransactionTooLargeError`, `WriteSetLimitError`,
`TransactionSnapshotExpiredError`, `CrossDatabaseTransactionError`,
`ReadOnlyTransactionError`, `NonTransactionalTableError`,
`TransactionConflictError`, `IsolationRangeReadError`.

**Fail-closed paths**, each stated in full above: an unmodelled statement kind
on an armed database is denied; a cached plan whose result type changed is
refused rather than mis-mapped; a type `catalog.ColumnTypeOf` cannot represent
is rejected at DDL time rather than at decode time; a write to a
non-transactional table inside an open transaction fails loudly; a range read
under `written+reads` is refused rather than pretending to be covered; and a
per-statement ceiling is enforced at the statement rather than deferred to an
eventual proposal rejection.

## Known authorization gap

The ad-hoc query endpoint is authorized with **`permission.KVRange`**
(`kv:range`) on every transport — there is no dedicated `sql:query` permission
constant. Since a single ad-hoc statement can reach relational tables and
collections, not only the `kv` table, `kv:range` is a weaker grant than the
capability it is guarding. Recorded here rather than silently accepted; the
right permission vocabulary is an owner decision.

## Makefile Targets

```bash
make test-sql-provider     # Provider unit tests
make test-sql-engine       # Engine + translator tests
make test-sql-mysql        # MySQL server tests
make test-sql-pgsql        # PgSQL server tests
make test-sql-privilege    # Table privilege tests (enforcement + GRANT/REVOKE)
make test-sql              # All SQL tests

make coverage-sql-provider # Provider coverage (91%+ required)
make coverage-sql-engine   # Engine coverage
make coverage-sql-mysql    # MySQL coverage
make coverage-sql-pgsql    # PgSQL coverage
make coverage-sql-privilege # Table privilege coverage
make coverage-sql          # All SQL coverage
```

Coverage floors are enforced by those targets, not by convention:
`coverage-sql-provider` requires 91%+.

## Observability

- SQL statements reach the existing audit system rather than a SQL-specific log
  — see [../audit/](../audit/).
- Privilege enforcement resolves each identity's privilege set once per query
  from a lock-free snapshot cache gated on the catalog generation. **An unarmed
  database costs no reads at all**, which is the signal that the latch has not
  been tripped.
- `SELECT @@qrdb_isolation` reports the session's conflict-detection scope.
