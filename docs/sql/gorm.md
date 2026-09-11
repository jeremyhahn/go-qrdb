# GORM

QRDB ships a first-class [GORM](https://gorm.io) dialector at `sdk/go/gorm`. It
plugs QRDB into GORM the same way `gorm.io/driver/mysql` plugs in a real MySQL
server: through `sdk/go/driver`, QRDB's `database/sql` driver (registered as
`qrdb`).

```
GORM ──▶ sdk/go/gorm (Dialector, Migrator) ──▶ sdk/go/driver ("qrdb")
                                                    │
                                    node:// ────────┴──────── embedded://
                                 full in-process stack      KV-only stack
                                 (Raft, registry,           (no relational
                                  relational tables,         catalog)
                                  transactions)
```

The SQL surface underneath is go-mysql-server, so everything in
[README.md](architecture.md) — the relational table catalog, DML, secondary indexes,
foreign keys, interactive transactions — is what GORM is driving. Read that
document for the engine's semantics; this one covers the dialector.

## Getting started

```go
package main

import (
	"log"

	"gorm.io/gorm"

	qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"
	_ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node" // registers node://
)

type User struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	Email string `gorm:"size:255;uniqueIndex"`
	Name  string `gorm:"size:255"`
}

func main() {
	db, err := gorm.Open(qrdbgorm.Open("node://memory?database=app"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	if err := db.AutoMigrate(&User{}); err != nil {
		log.Fatal(err)
	}

	if err := db.Create(&User{Email: "alice@example.com", Name: "Alice"}).Error; err != nil {
		log.Fatal(err)
	}

	var u User
	if err := db.Where("email = ?", "alice@example.com").First(&u).Error; err != nil {
		log.Fatal(err)
	}
	log.Printf("id=%d name=%s", u.ID, u.Name)
}
```

The import is aliased because this package's own name (`gorm`), like every other
GORM dialector's, collides with `gorm.io/gorm`.

The blank import of `sdk/go/driver/node` is what registers the `node://` scheme.
`sdk/go/gorm` already imports `sdk/go/driver` itself, so no separate blank
import is needed for `embedded://`, `rest://` or `grpc://`.

## DSN forms — and which one you want

`sdk/go/driver` deliberately exposes **two** in-process schemes. They are not the
same database, and choosing the wrong one is the single most common way to see
"this doesn't work with GORM".

| DSN | Backed by | `CREATE TABLE` / DML / transactions | Use it when |
|---|---|---|---|
| `node://memory?database=app` | A complete in-process QRDB node (`sdk/go/node.NewStandaloneNode`) with every protocol server disabled: Raft state machine, service registry, relational catalog, sequences | **Yes** | Almost always. This is the GORM path. |
| `node:///var/lib/qrdb?backend=pebble&database=app` | Same, persisted to disk | **Yes** | A durable single-process application. |
| `embedded://memory?database=app` | A storage backend plus the KV / entity / collection tables. No Raft, no `service.Registry` | **No** — `CREATE TABLE` fails with `*provider.RelationalCatalogUnavailableError` | You want only the KV surface and the fastest possible open. |
| `rest://host:port/db`, `grpc://host:port/db` | The network SQL API | **Refused at `Initialize`** | Never, for GORM. |

`embedded://` failing `CREATE TABLE` is **by design**, not a gap: it has no
`TransactionServicer`/`MVCCKVServicer`, which the catalog and sequences require,
and those only exist Raft-host-backed. A test pins that behaviour. If a
relational feature "doesn't work embedded", the answer is the wrong scheme, not
a missing feature. See `sdk/go/driver/node/node.go`'s package comment.

`node://` is a separate, side-effect-imported package because `sdk/go/node`
pulls in roughly 175 more packages than `sdk/go/driver` alone (915 against 741,
measured with `go list -deps` under the full build-tag set on 2026-08-13):
Raft, Pebble, the seal subsystem, the volume-pool subsystem, the whole service
graph. Making every `embedded://` and `rest://` user link all of that to reach a
scheme they will never open would be an unreasonable tax.

`rest://` and `grpc://` are rejected by `Dialector.Initialize` with
`*qrdbgorm.UnsupportedTransportError` *before* `sql.Open` is attempted. The
network SQL API accepts one statement per request with no field for bind
parameters, no session affinity for transactions, and no typed result metadata —
so the connection is refused rather than opened and left to fail on the first
parameterized query.

### DSN parameters

Both in-process schemes share one grammar (`driver.ParseEmbeddedDSN`):

| Parameter | Meaning |
|---|---|
| `database` | **Required.** Target database name. |
| `backend` | `memory`, `file`, `pebble`. Defaults to the host component, then to `memory`. |
| `engine` | `kv`, `timeseries`, `vector`. Defaults to `kv`. |
| `tenant` | Scopes every statement on the connection to a tenant namespace. Empty means the system tenant. |
| `user` | Subject recorded on the session identity (informational for in-process connections, which the driver does not authenticate). |
| `loc` | IANA time zone (or `Local`) used to render `time.Time` arguments as SQL datetime literals. Empty means UTC. |

The path component is the data directory: `node:///var/lib/qrdb?...`. A
`node://` DSN that names no data directory gets a temporary one, removed when
the connector is closed — a memory-backed node still needs a directory because
the vector store and the database catalog are file-backed regardless of the KV
backend.

One connector is one node. Two `*gorm.DB` values opened on the same memory DSN
are two independent databases; two opened on the same on-disk directory contend
for the same storage locks, exactly as two processes would.

### Over the wire instead

To reach a running `qrdbd` over its MySQL wire port, use the **stock**
`gorm.io/driver/mysql` with mTLS — not this dialector. QRDB's MySQL listener
speaks MySQL, and its authentication is mTLS-only (CN maps to the username, OU
to the tenant); see [README.md](architecture.md#authentication). The same applies to
`gorm.io/driver/postgres` against the PostgreSQL listener.

## Why `Dialector.Name()` reports `"mysql"`

`Name()` returns `"mysql"` by default, not `"qrdb"`. That is deliberate, and it
is the most load-bearing decision in the package:

1. **QRDB's SQL surface *is* go-mysql-server.** MySQL is not an approximation
   QRDB happens to resemble — it is QRDB's actual, only SQL dialect. Reporting
   anything else would be reporting the wrong dialect, not a neutral one.
2. **GORM's own test suite branches on `Dialector.Name()` in thirteen files**,
   almost all of the form "if not `mysql`, assume PostgreSQL-flavored defaults".
   There is no generic fallback path. Reporting `"qrdb"` would route every one
   of those tests into the Postgres branch, where they would keep passing while
   testing nothing.
3. **Third-party GORM plugins** (`gorm.io/plugin/dbresolver`, `gorm.io/gen`)
   switch on `Name()` for the same reason.

Set `Config.DialectName` if you need a different reported name — to
disambiguate multiple dialectors in logs, for instance. Be aware that doing so
changes which code paths GORM and its plugins take.

## Configuration

`qrdbgorm.Open(dsn)` is shorthand for `qrdbgorm.New(Config{DSN: dsn})`. Both
fill in the documented defaults for every field left unset.

```go
db, err := gorm.Open(qrdbgorm.New(qrdbgorm.Config{
	DSN:                      "node://memory?database=app",
	DefaultStringSize:        256,  // varchar width for an untagged string field
	DisableDatetimePrecision: false,
	SkipInitializeWithVersion: false,
}), &gorm.Config{})
```

| Field | Default | Notes |
|---|---|---|
| `DriverName` | `"qrdb"` | The `database/sql` driver name passed to `sql.Open`. |
| `DSN` | — | Ignored when `Conn` is set. |
| `Conn` | `nil` | A pre-opened `gorm.ConnPool`. When set, the DSN scheme check does not apply — choosing a usable transport becomes the caller's responsibility. |
| `DialectName` | `"mysql"` | See above. |
| `DefaultStringSize` | `256` | Width for a `string` field with no `gorm:"size:N"` tag. |
| `DefaultDatetimePrecision` | `3` | Also drives the `NowFunc` installed by `Apply`. |
| `DisableDatetimePrecision` | `false` | Omit fractional seconds from `DATETIME` columns entirely. |
| `DontSupportRenameIndex` | `false` | QRDB genuinely accepts `ALTER TABLE ... RENAME INDEX`; set true only to reproduce older-MySQL behaviour deliberately, which makes `Migrator.RenameIndex` return `*RenameIndexNotSupportedError`. |
| `DontSupportRenameColumn` | `false` | True issues MySQL's pre-8.0 `ALTER TABLE ... CHANGE`; both spellings are accepted by the engine. |
| `DontSupportNullAsDefaultValue` | `false` | Clears a reported default of the literal string `"NULL"`. |
| `SkipInitializeWithVersion` | `false` | Skips the `SELECT VERSION()` connectivity probe. The result is never used to branch dialect behaviour — QRDB's engine is uniformly go-mysql-server — so the probe exists only to fail `Initialize` fast on a bad DSN instead of deferring that to the first real query. |

`Apply` installs a `NowFunc` that rounds to `DefaultDatetimePrecision` unless
the caller supplied one. Without it, a freshly-set `time.Time` compares unequal
to the same value read back from a `datetime(3)` column, on sub-millisecond
remainders the column truncated away on write.

## Column types

`DataTypeOf` maps GORM's `schema.DataType` to MySQL types:

| Go / GORM type | SQL column |
|---|---|
| `bool` | `boolean` |
| `int*` / `uint*` | `tinyint` / `smallint` / `mediumint` / `int` / `bigint` by `size`, ` unsigned` for unsigned, ` AUTO_INCREMENT` when tagged |
| `float*` | `decimal(p,s)` when `precision` is tagged, else `float` (size ≤ 32) or `double` |
| `string` | `varchar(n)`, `mediumtext` (≥ 64 KiB), or `longtext` |
| `time.Time` | `datetime(p)`, plus ` NULL` unless the field is `NotNull` or a primary key |
| `[]byte` | `varbinary(n)`, `mediumblob`, or `longblob` |
| anything else | the literal `gorm:"type:..."` tag, or the empty string |

An untagged `string` field that is a primary key, unique, indexed, or has a
default is sized `varchar(191)` rather than left unbounded — `TEXT` columns
cannot be indexed or given a default in MySQL. (191 is 767 / 4, the utf8mb4
characters that fit MySQL's default index-prefix limit.)

A field with no recognized data type and no `type` tag yields an empty type,
which flows into the DDL and is rejected by the engine with a syntax error
naming the column. `DataTypeOf` deliberately does not panic: it runs inside your
process, and a mis-tagged struct field is not an unrecoverable condition.

## Migrations

`AutoMigrate` and the rest of `gorm.Migrator` work. The QRDB `Migrator` embeds
`gorm.io/gorm/migrator.Migrator` for everything whose default SQL the engine
already accepts, and overrides the `information_schema`-backed introspection
(`CurrentDatabase`, `HasTable`, `GetTables`, `TableType`, `HasColumn`,
`ColumnTypes`, `HasIndex`, `GetIndexes`, `HasConstraint`) plus the statements
whose MySQL shape differs (`AlterColumn` → `MODIFY COLUMN`, `RenameColumn`,
`DropIndex`, `DropConstraint`, `GetTypeAliases`, `DropTable`).

```go
type Company struct {
	ID   uint64 `gorm:"primaryKey;autoIncrement"`
	Name string `gorm:"size:64;index"`
}

type Employee struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	Email     string `gorm:"size:255;uniqueIndex"`
	CompanyID uint64
	Company   Company // AutoMigrate emits the FOREIGN KEY for this relationship
}

err := db.AutoMigrate(&Company{}, &Employee{})
```

Worth knowing:

- **Foreign keys need no special handling.** QRDB's relational tables implement
  `sql.ForeignKeyTable`, so `ADD`/`DROP FOREIGN KEY` is an ordinary statement
  the engine resolves and enforces. GORM's
  `gorm.Config.DisableForeignKeyConstraintWhenMigrating` may be left at its
  default (`false`), and relationship-derived constraints are created.
- **`DropTable` disables foreign key checking** on a *pinned* connection for the
  duration (`SET FOREIGN_KEY_CHECKS = 0`), because the engine refuses to drop a
  table another table still references, exactly as MySQL does — which makes a
  set containing a reference cycle undroppable one statement at a time in any
  order. The check is restored even when a drop fails. (`foreign_key_checks` is
  session-scoped; setting it on one pooled connection and dropping on another
  would leave it on, hence the pin.)
- **`DropConstraint` drops a `UNIQUE` constraint as an index.** go-mysql-server
  cannot resolve a bare `DROP CONSTRAINT` against these tables — it dispatches
  to the check-constraint path, which they do not implement — so the Migrator
  issues `ALTER TABLE ... DROP INDEX` against the unique index that *is* the
  constraint. A name matching no index is a no-op, not an error.
- **`RenameIndex` is real** (`ALTER TABLE ... RENAME INDEX`), unlike
  `gorm.io/driver/mysql`, which has to emulate it on older servers.
- **Type aliases prevent a re-`ALTER` loop.** `GetTypeAliases` treats
  `bool`/`tinyint`, `int`/`integer`, `bigint`/`int8`, `varchar`/`char`/`text`,
  `datetime`/`timestamp`, `blob`/`binary`/`varbinary` and `decimal`/`numeric` as
  the same type. Without it, `AutoMigrate` would re-`MODIFY` every `BOOLEAN`
  column (reported back as `tinyint`) on every single run.
- **`ColumnTypes` always returns a non-nil slice**, even on error, and its live
  `SELECT ... LIMIT 1` probe error is returned — `information_schema.columns`
  alone reports zero rows for a nonexistent table exactly as it does for one
  with no readable columns, so swallowing the probe error would make
  `ColumnTypes` report success for a table that was never created.

## Transactions

```go
err := db.Transaction(func(tx *gorm.DB) error {
	if err := tx.Create(&Company{Name: "Acme"}).Error; err != nil {
		return err // rolls back
	}
	return tx.Create(&Employee{Email: "bob@example.com"}).Error
})
```

The whole transaction is buffered and committed as **one** MVCC compare-and-swap
against the Raft group. Reads inside it are served from a pinned snapshot
overlaid with the transaction's own writes; conflicts are resolved
first-committer-wins. The full model, including savepoint semantics and the
write-set size ceilings, is in [README.md](architecture.md#transaction-control).

**Nested transactions use savepoints.** `Dialector` implements
`gorm.SavePointerDialectorInterface`, so GORM's nested `db.Transaction(...)`
calls issue `SAVEPOINT` / `ROLLBACK TO SAVEPOINT`. GORM generates names of the
form `sp1`; a caller (or plugin) supplying its own name must match
`^[A-Za-z_][A-Za-z0-9_]*$` or get `*InvalidSavePointNameError` — these
statements carry no bind parameter for the name, so an unvalidated name would be
an injection surface.

**GORM wraps every single write in a transaction by default.** That means each
`Create`/`Update`/`Delete` goes through the buffered write-set path rather than
the direct autocommit path. If you do not need that, set
`gorm.Config{SkipDefaultTransaction: true}` and single statements go straight to
the engine's autocommit path, which is one proposal with no snapshot read.

Do **not** issue DDL inside a transaction and expect it to roll back: DDL
implicitly commits any pending write-set first, exactly as in MySQL.

## Error handling

`Dialector` implements `gorm.ErrorTranslator`, so engine errors arrive as GORM
sentinels you can compare with `errors.Is`:

| MySQL error | GORM sentinel |
|---|---|
| 1062 duplicate entry | `gorm.ErrDuplicatedKey` |
| 1451 `ER_ROW_IS_REFERENCED_2` | `gorm.ErrForeignKeyViolated` |
| 1452 `ER_NO_REFERENCED_ROW_2` | `gorm.ErrForeignKeyViolated` |
| 3819 check constraint violated | `gorm.ErrCheckConstraintViolated` |

```go
if err := db.Create(&User{Email: "alice@example.com"}).Error; err != nil {
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		// UNIQUE or PRIMARY collision — nothing was written, not even the row.
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		// Parent row missing, or a parent still has children.
	default:
		return err
	}
}
```

These numbers are byte-identical to `go-gorm/mysql`'s own table, and identical
to what `go-sql-driver/mysql` surfaces as `*mysql.MySQLError` for the same
statement over QRDB's MySQL wire port — so one translation table serves the
in-process engine and the wire protocol, and a divergence between them is a bug
rather than a special case.

**1146 (unknown table) is deliberately *not* mapped.** GORM already produces
`gorm.ErrRecordNotFound` from a zero-row `First`; mapping a missing table onto
that sentinel would hide a real bug — a typo, a migration that never ran —
behind the error a caller expects for "no rows matched".

### Retrying a conflicted transaction (1213)

A transaction that loses its commit-time compare-and-swap surfaces as **MySQL
1213 / SQLSTATE 40001** (`ER_LOCK_DEADLOCK`). GORM has no sentinel for it, so it
passes through untranslated and you match on the code. In-process it arrives as
a `*driver.SQLStateError` (reachable with `errors.As` through the wrapping
`*driver.QueryExecError`); over the MySQL wire port it arrives as
`*mysql.MySQLError`. Both carry `Number == 1213`.

```go
import qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"

// retryable reports whether err is a serialization conflict worth retrying.
func retryable(err error) bool {
	var sqlState *qrdbdriver.SQLStateError
	return errors.As(err, &sqlState) && sqlState.Number == 1213
}

func transfer(db *gorm.DB, from, to uint64, amount int64) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = db.Transaction(func(tx *gorm.DB) error {
			if e := tx.Model(&Account{}).Where("id = ?", from).
				Update("balance", gorm.Expr("balance - ?", amount)).Error; e != nil {
				return e
			}
			return tx.Model(&Account{}).Where("id = ?", to).
				Update("balance", gorm.Expr("balance + ?", amount)).Error
		})
		if !retryable(err) {
			return err
		}
	}
	return err
}
```

The conflicted transaction is already rolled back by the time you see the error
— its write-set is cleared, matching InnoDB — so a retry starts clean.

## Known limitations

Every item here is a property of the engine or the dialector confirmed in code,
not a list of "not tested yet".

| Limitation | Detail |
|---|---|
| `embedded://` cannot create tables | By design; use `node://`. See above. |
| `rest://` / `grpc://` cannot back a GORM connection | Refused at `Initialize` with `*UnsupportedTransportError`. |
| `UPDATE ... JOIN` on a table with no primary key | Rejected by go-mysql-server itself (`sql/analyzer/assign_update_join.go`, gated on `sql.IsKeyless`), not by QRDB. `DELETE ... JOIN` is unaffected. Give the model a primary key if you need it. |
| CHECK constraints | QRDB's relational tables implement no `sql.CheckAlterableTable`, so the engine refuses `ADD CONSTRAINT ... CHECK`. `DropConstraint` still routes a CHECK name to `ALTER TABLE ... DROP CHECK`, whose acceptance is likewise the engine's call. |
| Schema-qualified and cross-database foreign keys | Refused at store time with a typed error, never persisted. Every catalog, row and index key is scoped to one database, so there is no read path a parent-row check in another database could use. |
| Cross-database transactions | A single CAS commit targets one Raft shard; a write to a second database inside an open transaction is refused (`CrossDatabaseTransactionError`). |
| Generated (virtual / stored-expression) columns | Rejected at DDL time — the persisted `TableDef` has no way to represent the generation expression, and silently dropping it would leave an unpopulatable column. |
| Spatial types (`GEOMETRY`, `POINT`, ...) | Rejected at `CREATE`/`ALTER TABLE` time rather than discovered as a decode failure later. |
| `ADD COLUMN` cannot honour `FIRST` / `AFTER` | Columns are always appended; that append-only invariant is what keeps `ADD COLUMN` metadata-only, with no row rewrite. |
| `ALTER TABLE ... AUTO_INCREMENT = N` cannot lower the counter | The underlying sequence only advances. Unlike InnoDB, a lower value is a silent no-op rather than a validated change. |
| `AUTO_INCREMENT` values can have gaps | Raft-committed block allocation, equivalent to `innodb_autoinc_lock_mode=2`. Duplicates are impossible; consecutive values within one multi-row `INSERT` are not guaranteed. |
| The `kv`, entity, and collection tables are autocommit-only | A write to one of them inside an open transaction fails with `NonTransactionalTableError` rather than silently escaping the transaction. Ordinary GORM models are relational tables and are unaffected. |
| A collection table's upsert is two statements, not one | Claiming a taken `_id` returns `gorm.ErrDuplicatedKey` like any other duplicate, and that claim **does** hold under concurrent writers — `CollectionService.InsertDocument` writes with a guarded write. What does not hold is `clause.OnConflict` and `Save` on a collection table: each is an insert, a refusal and a second write with no lock across the three, and a collection table is autocommit-only so neither can be wrapped in a transaction. Ordinary GORM models are relational tables and are unaffected. |
| Mixed-case `IlIkE` is not rewritten (PostgreSQL wire) | Only the exact-case `ILIKE`/`ilike` spellings are normalized. |
| `UPDATE ... RETURNING`, `DELETE ... RETURNING` (PostgreSQL wire) | Rejected with a typed error; the embedded vitess grammar carries `RETURNING` on the `INSERT` production only. `INSERT ... RETURNING` works. |

## Conformance

`sdk/go/gorm/conformance` runs **GORM's own upstream test suite** against QRDB
over three surfaces: the native dialector on `node://`, `gorm.io/driver/mysql`
over the MySQL wire port, and `gorm.io/driver/postgres` over the PostgreSQL wire
port. It is a separate Go module, so `go test ./...` at the repository root and
`make test` never touch it.

Its README documents the `make gorm-suite-sync` (fetch, verify, materialize)
and `make conformance-test-gorm-*` targets, one per surface.

The upstream suite is pinned by tag and verified by whole-tarball **and**
per-file SHA-256 against `GORM_SUITE.lock`. Expected failures live in a typed,
compiled allowlist that **ratchets**: a test in the allowlist that starts
passing fails the run, so no entry can silently keep excusing a test that was
actually fixed.

Two allowlist fields exist because a run has to be able to finish and to mean
something:

- **`AbortsRun`** — the test ends the `go test` process rather than failing
  (most nil-deref after a call they did not expect to fail; one hangs). The
  runner excludes these with `-skip`, because one of them truncates the suite
  and turns a partial measurement into a reported result. The runner also
  compares its results against `go test -list`, so a truncated run is reported
  as **RUN TRUNCATED** rather than as a pile of missing entries.
- **`Unstable`** — the outcome tracks what an earlier test left behind rather
  than anything about this test. The upstream suite shares one set of models,
  its migration tests drop and re-create them, and when a re-creation partially
  fails the shared tables stay dropped. These are exempt from the stale-pass
  check because a passing run says nothing about them either.

Current measurements (2026-08-14), each verified stable across repeated runs:

| target | pass | expected-fail | skip | excluded |
|---|---:|---:|---:|---:|
| node (native dialector) | 371 | 6 | 18 | 3 |
| mysql wire | 373 | 6 | 18 | 1 |
| pgsql wire | 255 | 119 | 17 | 7 |

Over half the PostgreSQL failures are two defects in go-mysql-server's
RETURNING support; see [upstream-gaps.md](upstream-gaps.md).

**This harness is infrastructure, not a gate**, and a tracked fraction of the
suite is expected to fail. For the current numbers, the tracked failure list,
and the qrdb-side bugs it has found, read
[`sdk/go/gorm/conformance/README.md`](../../sdk/go/gorm/conformance/README.md) —
that file is the source of truth and is updated with each measurement.

## Reference

| Topic | Source |
|---|---|
| Dialector, `Config`, savepoints | `sdk/go/gorm/qrdb.go` |
| Why `Name()` is `"mysql"` | `sdk/go/gorm/doc.go` |
| Error translation | `sdk/go/gorm/error_translator.go` |
| Migrator | `sdk/go/gorm/migrator.go` |
| Column type mapping | `sdk/go/gorm/datatype.go` |
| DSN grammar and schemes | `sdk/go/driver/dsn.go`, `sdk/go/driver/driver.go` |
| `node://` connector | `sdk/go/driver/node/node.go` |
| Engine semantics | [README.md](architecture.md) |
