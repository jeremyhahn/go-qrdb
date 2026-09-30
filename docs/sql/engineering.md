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
  work on it. Its embedded `Rows.Next` refuses a row whose width differs from
  the result's column count with `*driver.RowWidthError`: `database/sql`
  sizes the destination from `Columns()`, so a wider row would index past it
  and a narrower one would leave the previous row's values in the unfilled
  slots.
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
| `UPDATE <collection> SET … WHERE _id = N` | `GetDocument(N)` compared with the row read, then `UpsertDocument` guarded by its revision |
| `UPDATE <collection> SET _id = M WHERE _id = N` | `InsertDocument` claiming `M`, then `DeleteDocument(N)` guarded by the revision read — refused with `InvalidKeyError` when `M` is 0. A release refused because `N` changed or is gone withdraws `M` (MySQL 1213); a release that fails with an unknown outcome leaves both and returns `provider.DocumentMoveOutcomeUnknownError` |
| `DELETE FROM <collection> WHERE _id = N` | `collectionSvc.DeleteDocument` |
| `REPLACE INTO <collection> …` | `UpsertDocument` — one guarded write per row |
| `INSERT … ON DUPLICATE KEY UPDATE` | `InsertDocument`; on a primary-key violation, the guarded `UPDATE` above |

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

**The key is enforced by the write, not by a check before it.**
`CollectionService.InsertDocument` writes a non-overwriting document with
`KVServicer.PutIfAbsent`, so the constraint closes a race between two
concurrent claims as well as the ordinary overwrite. It is as strong as a
relational table's uniqueness, which rides on a `TransactionServicer.Execute`
compare in the same proposal as the row (see *Relational Tables* below),
reached by a different mechanism.

It was a read followed by an unguarded write until the backend behind a
database with its own store gained the guarded verbs; every other engine
already had them. A backend that still lacks them — the remote proxy, whose
wire protocol does not carry the verbs — refuses the insert rather than
degrading to the weaker shape.

**`REPLACE` and `ON DUPLICATE KEY UPDATE` are guarded writes too.** Each used
to be an insert, a refusal and a second statement-level write with no lock
across the three, on a table that is autocommit-only. `REPLACE INTO` is now one
`CollectionService.UpsertDocument` per row — insert or replace decided and
written in the same guarded write — and still reports the primary-key
violation, carrying exactly the document it displaced, so go-mysql-server
counts two affected rows for a replacement; the delete and re-insert it issues
next for that row write nothing. The update half of `ON DUPLICATE KEY UPDATE`,
like every `UPDATE`, reads the stored document back, compares it with the row
its expressions were evaluated over, and replaces it only while it carries the
revision read. A lost race writes nothing and is reported as
`sql.ErrLockDeadlock` — **MySQL 1213** / `SQLSTATE 40001`, the code clients
retry on — exactly as a relational table's lost optimistic commit is.

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
- **`DROP COLUMN` and `MODIFY COLUMN` rewrite every row**, re-encoding it at a new schema version. The rewrite is journaled (`TableDDLJournal`) so a crash mid-rewrite is resumable: the very next query against the table detects the journal and finishes the rewrite before serving it, rather than leaving the table permanently stuck. The journal records the metadata record's ModRevision its target definition was derived from (`TableDDLJournal.MetadataRevision`), and `CompleteRewrite` writes the target back only while the record is still at that revision — refusing with `*service.TableDefinitionStaleError` otherwise, never overwriting. While a journal exists every other catalog change of the table refuses with `*service.TableRewriteInProgressError`: `ALTER`, `RENAME TABLE`, `TRUNCATE`, `CREATE`/`DROP`/`RENAME INDEX` and `ADD`/`DROP`/`UPDATE FOREIGN KEY` each check up front and also compare the journal key's absence in their own commit (`requireNoRewrite`), so none can land between the rewrite's start and its write-back and be undone by it. `DROP TABLE` alone proceeds, removing the journal with the table.
- **A rewrite rebuilds every index it changes under a new incarnation.** `DROP COLUMN` narrows each index on the column, and a layout-changing `MODIFY COLUMN` changes the encoding each index on it was built with; either way the index's existing entries no longer match its definition. `AlterTable` gives each such index a fresh incarnation in the journal's target (a rename re-encodes nothing and keeps every incarnation), and the rewrite builds the new incarnation's entries from the re-encoded rows before completing — every row converted and every entry built before any is written, so a `UNIQUE` index narrowed onto repeating values fails the `ALTER` (go-mysql-server's unique-key error) with the table untouched. Rows and entries are written in chunks fenced on the generation and on the metadata record the journal was derived from. `CompleteRewrite`, in the same fenced transaction that makes the target the definition, publishes a `ReclaimJob` for every incarnation the definition stops listing — the rebuilt indexes' old ones and any index the drop left with no columns. `AbortRewrite` publishes one for the target's fresh incarnations, in the transaction that discards the journal.
- **`DROP TABLE` publishes its data for reclamation.** One transaction removes the catalog entry, the rewrite journal, the generation counter and both sequences, and publishes a `ReclaimJob` for the current generation's rows and whole index keyspace; the sweeper deletes them after reader quiescence (see "Reclaiming orphaned keyspace" below). A crash at any point leaves either the whole table or the job that reclaims it, and no reader ever sees a partially-deleted table.
- **Spatial types (`GEOMETRY`, `POINT`, ...) and any other type `catalog.ColumnTypeOf` cannot map are rejected at `CREATE`/`ALTER TABLE` time**, not discovered later as a decode failure.

Relational tables shadow a same-named collection: if both a relational table and a legacy collection exist with the same name, `CREATE TABLE` deterministically wins.

### DML and AUTO_INCREMENT

`INSERT`, `UPDATE`, `DELETE`, `REPLACE` and `TRUNCATE` are implemented: `RelationalTable` implements `sql.InsertableTable`, `sql.UpdatableTable`, `sql.DeletableTable`, `sql.ReplaceableTable` and `sql.TruncateableTable` (`relational_writer.go`). Two properties of the write path are load-bearing.

**A row write and its secondary-index maintenance are one compare-and-swap.** Every `INSERT`/`UPDATE`/`DELETE` builds a single set of compares and ops covering the row key *and* every affected index entry, and proposes them through exactly one `service.TransactionServicer.Execute` call. Index entries live in the table's own keyspace (`service.TableIndexPrefix`), not in `index.IndexServicer`, precisely because `IndexServicer` writes cannot be composed into that CAS — and "check, then write" across two proposals has a race window in which a `UNIQUE` constraint cannot be enforced at all. `UNIQUE` rides on the same CAS: a `Compare` requiring the candidate entry's `Version` to be 0 (absent) is proposed alongside the row, so a colliding write commits *nothing*, not even the row. A lost compare is resolved into go-mysql-server's own `sql.NewUniqueKeyErr` (naming `PRIMARY` or the specific index), which reaches a client as MySQL error **1062**.

**`AUTO_INCREMENT` is Raft-committed and block-allocated.** Values come from `service.SequenceServicer` under `service.TableAutoIncrementSequenceName(TableID)`, handed out in blocks so a multi-row `INSERT` costs one proposal per block rather than one per row. Nothing about the allocation is process-local, so two nodes can never hand out the same value. **Gaps are permitted; duplicates are not** — which is exactly MySQL's own `innodb_autoinc_lock_mode=2` (interleaved) behaviour. The "consecutive values within a single multi-row `INSERT`" guarantee of modes 0/1 is *not* offered (`AcquireAutoIncrementLock` is a no-op, since block allocation already prevents collisions). An explicitly supplied value at or above the counter advances the sequence past it (`SequenceServicer.Advance`), so a later generated value can never collide with one a caller supplied. `ALTER TABLE ... AUTO_INCREMENT = N` is implemented, but — unlike InnoDB — it can only ever raise the counter, never lower it below a value already committed.

`REPLACE INTO` composes for free from the same writer: go-mysql-server's insert loop calls `Insert`, and on the duplicate-key error above calls `Delete` with the *existing* conflicting row before retrying — so the old row and its index entries are removed atomically, then the new row and its own entries are written atomically.

### `TRUNCATE TABLE`: generation-scoped keys, O(1) regardless of table size

Every row and index-entry key carries a **generation** number in its prefix: `service.TableDataPrefix(database, tableID, generation)` and `service.TableIndexPrefix(database, tableID, generation, incarnation)`. `TRUNCATE TABLE` (`TableService.TruncateTable`) does not scan or delete a single row. It is ONE `TransactionServicer.Execute` call that:

1. Bumps a dedicated generation counter key (`service.TableGenerationKey`) with a `CompareModRevision` CAS — absent decodes as generation 0, the same "absent means zero" contract every counter in this package uses.
2. Optionally resets the table's `AUTO_INCREMENT` sequence to 0 in the SAME proposal — **MySQL semantics**: `TRUNCATE TABLE` resets the counter, matching real MySQL's behavior. **PostgreSQL keeps its sequence**: plain `TRUNCATE` (without `RESTART IDENTITY`, which this engine does not parse as a distinct form) leaves a sequence the table owns untouched, matching real PostgreSQL. `RelationalTable.postgres` (set via `WithPostgreSQLDialect`, itself derived from the session's wire protocol) decides which behavior a given `TRUNCATE` gets — the same generation bump runs either way; only the sequence-reset flag differs.
3. Publishes a `service.ReclaimJob` naming the OLD generation's data prefix and that generation's WHOLE index keyspace (`service.TableIndexGenerationPrefix`, every index incarnation at once — so entries of an index the catalog no longer lists go too) as orphaned, in the SAME transaction as the bump, in the table's own store — so a crash immediately after commit still leaves a durable record of what to reclaim.

The table's `TableDef.Generation` (populated fresh on every `GetTable`/`AlterTable`/`CompleteRewrite`/`RewriteJournal`, never persisted in the metadata record itself) is read ONCE when a statement resolves the table and never re-read on the successful path. `GetTable` also reports the revisions it read — `TableDef.MetadataRevision` and `TableDef.GenerationRevision` — and `TableDef.GenerationReadAt`, when the generation read began on the local monotonic clock: the start of the statement's **generation pin** (see "Reader quiescence" below). A handle publishes its generation, the row prefix derived from it and its pin as one immutable value behind one atomic pointer, so a `TRUNCATE` on the handle is never observed half-applied.

**Index entries are keyed by incarnation, not name.** Every secondary index carries a `TableIndex.Incarnation`, allocated from the table's own `IndexIncarnations` counter in the same compare-and-swap that adds the index. The counter lives in the metadata record and only ever moves forward through a compare-and-swap on that record; the one write that replaces the record wholesale from an earlier copy — a rewrite's `CompleteRewrite` — is refused if the record moved since that copy was taken, and every index change is refused while a rewrite is open, so an incarnation is never handed out twice within a table. A name can be renamed, dropped and created again; an incarnation cannot — so the keyspace a reclaim job deletes can never become a live index's, and the numeric segment, terminated by `/`, can never be a prefix of another index's (`a` against `a/b` cannot arise). `RENAME INDEX` is therefore a single catalog compare-and-swap (`TableService.RenameIndex`) that moves, copies and reclaims nothing: the renamed index keeps its incarnation, and DML running concurrently keeps maintaining the same keyspace under either name. `DROP INDEX` removes the index from the catalog and, in the same transaction and fenced on the generation, publishes a `ReclaimJob` for its incarnation's entries (`TableService.DropIndex`) — they are not deleted inline, because a statement that resolved the index may still be reading them.

**Tables created by v0.20.0 through v0.21.0 must be dropped and re-created; there is no migration.** Those releases shipped the previous layout, whose row and index-entry keys carried neither a generation nor an index incarnation, and whose catalog records carry no incarnations for their indexes. Every catalog record now states the layout its keys were written under (`TableDef.Layout`, `service.TableLayoutCurrent`), and a record without one is refused with `*service.TableLayoutUnsupportedError` by every operation that would address its keys (`GetTable`, `TruncateTable`, index DDL, `AlterTable`, the rewrite journal). Opening such a table anyway would read keys that were never written — an empty table — and would make all of its indexes share incarnation 0's keyspace. The SQL layer resolves the table to a stand-in (`legacyLayoutTable`) whose every read, write and `TRUNCATE` fails with MySQL error **1459** (`ER_TABLE_NEEDS_UPGRADE`, SQLSTATE HY000; PostgreSQL SQLSTATE **55000**, `object_not_in_prerequisite_state`), with a message naming the table and the remedy. `DROP TABLE` is the one operation it supports: the same one-transaction drop as any table, whose reclaim job also names the old layout's prefixes (`service.LegacyTableKeyPrefixes`), after which the name can be re-created. The layout is checked before the rewrite journal, so a legacy table whose v0.20.x rewrite was interrupted still resolves as a legacy table and can be dropped. `information_schema` lists it with the refusal as its table comment; `ALTER`, `CREATE INDEX` and `ADD FOREIGN KEY` refuse with 1459. The stand-in lists no indexes and declares no constraints — go-mysql-server treats `GetIndexes` as infallible and panics on its error on `DROP TABLE`'s own path — so `SHOW INDEX` shows none and `DROP`/`RENAME INDEX` and `DROP FOREIGN KEY` are refused by the engine before they reach the table. Every non-SQL transport classifies the refusal the same way (`service.IsTableLayoutUnsupported`): gRPC, the Unix socket and MCP as `FAILED_PRECONDITION`, QUIC as `StatusFailedPrecondition`, REST as 412.

**Every DML commit is fenced on what its statement resolved, in the SAME Raft transaction as its row and index writes — autocommit and buffered (interactive) transactions alike.** Two Compares ride in that proposal (`RelationalTable.resolutionCompares`):

- **The generation.** `generationCompare` builds a `CompareValue` precondition against the generation key: generation 0 (never truncated — the common case) compares against an *empty* value, matching `CompareValue`'s absent-key semantics (`pkg/mvcc.Evaluate`, `pebbleEvaluateCompare`), rather than a literal encoded zero, which the generation key does not hold until its first write.
- **The definition.** `metadataCompare` compares the metadata record's ModRevision against `TableDef.MetadataRevision`. Every DDL rewrites that record — `CREATE`/`DROP`/`RENAME INDEX`, `ALTER`, `RENAME TABLE`, `DROP TABLE` — so a statement planned against an index set the table no longer has fails instead of committing index maintenance for the wrong one: an index created meanwhile would otherwise permanently lack the row's entry, and a dropped one would gain entries after its reclamation. This is also what makes `CREATE INDEX`'s build complete: DML resolved before the index existed cannot commit after it.

Both ride in the proposal the row/index write already makes, so they add no read on the successful path — resolving the table once per **statement** (`TableService.GetTable`) is the only read, and a multi-row `INSERT` resolves the table once and calls `Insert` once per row on that SAME handle. Only on a CAS **failure** does `checkResolutionStale` read the generation (and, if needed, the metadata revision) to tell "the table moved" apart from "a genuine duplicate-key collision," which is the one case a bulk insert's hot path never takes.

An interactive (`BEGIN`...`COMMIT`) transaction registers the same fence once per relational table it touches (`WriteSet.RequireResolution`, called from `RelationalTable.activeWriteSet`), independent of the session's `qrdb_isolation` level: `IsolationNone` otherwise attaches no preconditions at all, but these fences are a distinct correctness concern from the user-selectable conflict-detection `IsolationLevel` controls. `WriteSet.commit` folds every registered fence into its proposal regardless of level, and on commit failure `WriteSet.staleResolution` reports which one moved. `activeWriteSet` also refuses, at resolution:

- a later statement whose fresh generation or definition differs from what an earlier statement in the same transaction pinned, and
- a table's FIRST touch when its generation or definition was written after the transaction's snapshot revision (`TableDef.GenerationRevision` / `MetadataRevision` newer than the snapshot bound by another table) — the snapshot holds neither that generation's rows nor that definition's index entries, and reading it would return them as absent — or when the generation the snapshot holds (`WriteSet.generationAt`) is not the one the handle resolved: a handle resolved before a `TRUNCATE` its snapshot already includes has an older revision, not a newer one.

A stale generation surfaces as a **typed, retryable** `*service.TableGenerationStaleError`, and a changed definition as `*service.TableDefinitionStaleError`, both wrapped in go-mysql-server's own `sql.ErrLockDeadlock` — the identical mechanism an ordinary buffered-transaction write conflict is already wrapped in (`QRDBSession.CommitTransaction`) — which becomes MySQL error **1213** (`ER_LOCK_DEADLOCK`) automatically via go-mysql-server's own wire translation, and PostgreSQL **SQLSTATE 40001** (`serialization_failure`) via `pkg/sql/pgsql`'s `sql.ErrLockDeadlock.Is` check in `wire_errors.go`. Both wire protocols therefore tell the client to retry the statement. There is deliberately **no auto-retry inside the provider** — the client/driver decides. Every non-SQL transport classifies the same two errors as retryable (`service.IsTableResolutionStale`, which follows go-mysql-server's `Cause()` links as well as `Unwrap`): gRPC, the Unix socket and MCP as `ABORTED`, QUIC as `StatusAborted`, REST as 409. The SQL service sheds go-mysql-server's `sql.WrappedInsertError` — which exposes its cause through neither — before handing an `INSERT`'s failure to a transport.

`ALTER TABLE DROP/MODIFY COLUMN`'s background rewrite (`relational_rewrite.go`) and `CREATE INDEX`'s build (`indexBuildInserter`) write their (potentially large) key sets in CHUNKS, each chunk proposed as its own compare-and-swap carrying the SAME generation Compare the DML path uses, so a `TRUNCATE` that commits between chunks fails the next chunk immediately. The build resolves the new index's incarnation from the catalog (`catalogIncarnation`), which allocated it after the build's handle resolved the table, together with the ModRevision of the definition listing it, and every chunk also compares the metadata record against that revision (`writeChunkedWithResolutionFence`): a `DROP INDEX` landing mid-build has published a reclaim of the incarnation, and a later chunk fails with `*service.TableDefinitionStaleError` instead of writing entries behind the reclaim.

**`DROP TABLE` is one transaction** (`TableService.DropTable`): the definition, the rewrite journal, the generation counter and both sequence counters are deleted, the catalog epoch is bumped, and a `ReclaimJob` names the current generation's data and whole index keyspace (older generations already have their `TRUNCATE` jobs). It compares the generation key as well as the metadata key: a `TRUNCATE` landing between the drop's reads and its commit fails the attempt, which re-reads. Deleted separately, a crash between the counters and the definition would either strand the counters forever (TableID is never reused) or — counter first — leave a resolvable table reading "generation 0" again, writing into a keyspace an earlier `TRUNCATE`'s job may still be deleting. The SQL layer passes the definition it resolved (`DropTableRequest.Resolution`), so a `TRUNCATE` or DDL landing between the statement's resolution and the drop refuses it with the retryable stale error rather than dropping a state the statement never saw. The rows are deleted by the sweeper, after quiescence, never inline: a statement that resolved the table before the drop may still be reading them.

#### Reclaiming orphaned keyspace

`service.ReclaimSweeper`, constructed once at `pkg/server/lifecycle.BuildServices` alongside the pre-existing `KeyspaceReclaimService`, drains pending `ReclaimJob` records. The job shape (an ID, the (tenant, database) whose store it lives in, and a list of key prefixes — nothing table-specific) is deliberately generic, so a future "cannot delete inline" reclamation can publish into the same machinery.

**Discovery.** A job lives in its table's own store — composite-namespaced `<tenant>/<db>/` in the shared store, or a dedicated store — which the sweeper cannot list cheaply. Every job therefore has a `ReclaimIndexEntry` in the SYSTEM store, un-namespaced, under `__reclaim:index/` (an internal-key prefix, hidden from user `List`/`Scan` and MVCC restore like `__reclaim:job/`). The entry is always written FIRST, as its own transaction through the system store's own `TransactionServicer` (`WithReclaimIndex`), strictly before the operation's own transaction publishes the job: the two stores cannot share one compare-and-swap, and this order means a crash between them leaves at worst an entry with nothing behind it — never a job nothing points to. Every publishing ATTEMPT gets a fresh job ID (`<tenant>:<label>:<128 random bits>`), so two tenants' dedicated stores that allocate the same TableID never collide on one index key, and an attempt that definitely lost its compare-and-swap withdraws its own entry without any chance of withdrawing another's.

**Entries are settled by fences, read before the job.** "Entry, no job" is what an in-flight publish looks like between its two transactions, what a lost or crashed publish leaves, and what a finished job leaves if the sweeper crashed between deleting the job record and deleting its entry. Each entry records `Fences`: keys the publishing transaction compared by ModRevision (the table's metadata and generation keys), chosen so that once any of them reads a different ModRevision in the job's store that transaction can never commit, and so that committing it moves at least one. The sweeper reads the fences FIRST: while every fence holds, the publish has not committed and the entry is left alone, however long a slow proposal takes. Only once a fence has moved does it read the job — present, it is drained; absent, there is nothing left to find and the entry is deleted. The reverse order would be check-then-act: a job read as absent could be committed by its own publish an instant later, which a fence read that followed would then report as settled.

**Ownership.** The index entry records the job store's `StorePlacement`, set by `restServiceRegistry.resolve` for the (tenant, database) the operation ran against, and the publishing node's `Origin` (its system-shard replica ID). The sweep loop runs on EVERY node; per entry, `service.ReclaimStoreResolver.Owns` decides who acts:

| Placement | Store | Swept by |
|---|---|---|
| `shared` | the replicated shared store, composite-namespaced | the system shard's leader |
| `node` | a volume-isolated tenant's single-node Host, or a database with its own encrypting store | the node that published it — no other node can reach it |
| `shard` | the database's own Raft group | that group's current leader (`RaftBackendFactory.LeadsDatabase`, which never attaches a shard) |

A placement the resolver does not recognize has no owner. The job's store is then resolved through the same `restServiceRegistry` the transports use — namespace, dedicated store and quota accounting included — so deletes are credited to the tenant's storage quota exactly as the rows' writes were charged. A shared-store job is resolved without the registry's existence check (that check reads the resolving node's local catalog, which the system leader need not hold for a database another node created). A resolved placement that disagrees with the recorded one is refused (`ReclaimPlacementMismatchError`) and retried rather than looked for in the wrong store. A node-local database its publishing node no longer has is reported gone (`ReclaimStoreGoneError`), finishing the entry. A shared database that was dropped needs no such signal: its namespace was reclaimed, job record included, and the fences settle the entry.

**Bounded work.** Neither the index nor a job's keyspace is ever listed whole. A sweep reads one page of `reclaimIndexPageSize` index entries after the last key the previous sweep read, wrapping at the end, so a job sorted after any number of failing entries is still reached. Keys are listed with a bounded MVCC `Range` (`reclaimKeyPageSize`) and each page is deleted in ONE transaction through the database's own `TransactionServicer` (`service.NewReclaimStore` pairs it with the MVCC view), one Raft proposal per page rather than per key, within a per-sweep budget (`reclaimMaxKeysPerSweep`). The sweeper keeps a resume key per job — the prefix it was working through and the last key it deleted — so each sweep lists from just after where the last one stopped, never re-walking the deleted keys (or their tombstones) before it. A job's record is deleted only once every prefix has been listed to its end, so a large generation drains over several sweeps and a crash at any point leaves it resumable; a new owner starts from the beginning of each prefix. Two sweepers acting on one job (a leadership change mid-sweep) is harmless: deletes are idempotent.

#### Reader quiescence

A statement reads the generation and index incarnations it resolved for as long as it runs. The sweeper must not delete those keys while such a reader is live, or the reader sees a short or empty result instead of an error. The guarantee has two halves that meet at one constant, `service.TableGenerationPinTTL` (10s):

- **Readers re-prove old pins.** `RelationalTable.confirmGenerationPin` runs after every read (`txGet`, `txScan`) and after the exact count `RowCount` reports, before the result is used. A pin younger than `TableGenerationPinTTL` costs nothing. An older one re-proves the whole resolution with one `GetTable` (`reproveResolution`): the same TableID, the same generation, and every index incarnation the statement may read still catalogued. Unchanged, the pin is renewed from the instant before that read; changed, the statement fails with the typed, retryable `*service.TableGenerationStaleError` — or `*service.TableDefinitionStaleError` when an index was dropped or the table was dropped or replaced — both reported as MySQL 1213 / SQLSTATE 40001. A renamed index keeps its incarnation and keeps reading. While the catalog is mid-rewrite, when `GetTable` reports the rewrite instead of a definition, the committed definition is read through the rewrite journal (`RewriteJournal`) and proven the same way — TableID, generation and every index incarnation. A missed point read is reported as a miss only after the same check, and any other read error on an indexed lookup is returned rather than treated as "no such row". A definition with no recorded read time is treated as already expired. `Truncate` renews its own handle's pin on the generation it committed.
- **The sweeper waits longer than any pin.** It deletes a job's keys only `service.ReclaimQuiescence` (`TableGenerationPinTTL` + 5s) after IT FIRST OBSERVED the committed job record. It cannot observe the job before the orphaning transaction commits, and every reader of the orphaned keys pinned them before that commit, so every result read under such a pin was complete before deletion began. Observation is in-memory and per sweeper — a new owner (leader change, restart) starts its own wait, which is only more conservative — and compares only each node's own monotonic elapsed time, never wall clocks across nodes.

Every composer that serves relational DDL starts the sweep loop on every node: the daemon (`cmd/qrdbd`'s `startReclaimSweeper`, standalone included — a standalone daemon owns every shared-store job itself) and the SDK node (`startReclaimSweep`, `startReclaimSweepStandalone`). The server installs the resolver when it is built with a `DatabaseServiceRegistry`; a server without one serves no relational DDL and publishes no jobs. `TableService` refuses `TRUNCATE`/`DROP INDEX`/`DROP TABLE` outright (`ReclaimIndexUnconfiguredError`) when constructed without `WithReclaimIndex`, rather than publish a job nothing could find.

**Residuals.** An entry whose job store is gone for good but cannot be proven gone — a dedicated Raft database that was dropped, or a node-local store on a node that was decommissioned — has no owner and stays in the index: a few hundred bytes, costing only its share of a page listing. A publish attempt that crashed before its transaction, on a table nothing ever modifies again, keeps its fenced entry for the same reason.

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
