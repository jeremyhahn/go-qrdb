# GORM conformance suite

This directory runs [go-gorm/gorm](https://github.com/go-gorm/gorm)'s own
upstream test suite against qrdb, over three surfaces:

- **node** — qrdb's native GORM dialector (`sdk/go/gorm`) over a complete
  in-process qrdb node (`node://`, registered by `sdk/go/driver/node`):
  Raft state machine, service registry, relational table catalog, interactive
  transactions, every protocol server disabled. No `qrdbd` subprocess.
  It is deliberately **not** `embedded://`, which is qrdb's KV-only
  in-process path: that scheme has no `service.Registry`, so `CREATE TABLE`
  fails with `pkg/sql/provider.RelationalCatalogUnavailableError` and this
  suite has no tables to run against at all.
- **mysql** — [`gorm.io/driver/mysql`](https://gorm.io/driver/mysql) over
  qrdb's MySQL wire protocol server, mTLS-authenticated.
- **pgsql** — [`gorm.io/driver/postgres`](https://gorm.io/driver/postgres)
  over qrdb's PostgreSQL wire protocol server, mTLS-authenticated.

**This is infrastructure, not a gate.** A tracked fraction of the upstream
suite fails on every target and is expected to — see
[Observed results](#observed-results) below, where the **pgsql** target in
particular is very red for one identifiable reason. The point of this harness
is that the RED is *precise, tracked, and ratcheted*, not that it is green.

## Why a separate module

`sdk/go/gorm/conformance` is its own Go module
(`github.com/jeremyhahn/go-qrdb/sdk/go/gorm/conformance`), nested under but
independent from `sdk/go`. It needs `gorm.io/driver/{mysql,postgres,gaussdb}`,
`github.com/lib/pq`, `github.com/go-sql-driver/mysql`, and the upstream GORM
test fixtures purely to run GORM's suite — none of that belongs in `sdk/go`'s
own dependency graph, which every SDK consumer inherits. Being a separate
module also means:

- `go test ./...` at the repository root never touches it.
- `make test-sdk` never touches it.
- `make test`, `make integration-test`, and `make all` never touch it (see
  the Makefile's "GORM Conformance Suite" section) — a red run here can
  never fail a build that isn't explicitly asking for it.

### go.work

Like every other nested module in this workspace (`sdk/go`,
`cmd/qrdb-sqlite`, ...), local development resolution against `sdk/go` and
the root `go-qrdb` module is provided entirely by the top-level Go
workspace at `/home/jhahn/sources/go.work`, **not** by local `replace`
directives in this module's `go.mod` (see its header comment). That means
`./sdk/go/gorm/conformance` **must** be listed in `go.work`'s `use` block —
without it, Go refuses to build this module at all
(`directory prefix . does not contain modules listed in go.work or their
selected dependencies`), and outside workspace mode entirely there is no
publishable version of `github.com/jeremyhahn/go-qrdb/sdk/go` to resolve
against. This entry was added as part of building this harness, immediately
next to the existing `./go-qrdb/sdk/go` line.

## Layout

```
sdk/go/gorm/conformance/
  go.mod                  # nested module — see "Why a separate module"
  .gitignore              # excludes materialized upstream *_test.go
  GORM_SUITE.lock         # pinned tag, hashes, exclusions, budgets
  sync.go                 # go run sync.go [-clean] — fetch/verify/materialize
  runner.go               # go run runner.go -target=... — ratchet reconciler
  qrdb_tests_test.go      # substitute for upstream tests_test.go
  qrdb_harness_test.go    # qrdbd subprocess lifecycle for mysql/pgsql targets
  qrdb_allowlist_test.go  # typed, compiled ratchet table
  README.md               # this file
  *_test.go               # materialized by `make gorm-suite-sync`, gitignored
```

## Running it

```sh
make gorm-suite-sync             # fetch + verify + materialize (idempotent, offline after first run)
make conformance-test-gorm-node
make conformance-test-gorm-mysql   # spawns qrdbd, needs `make build-server`'s tags
make conformance-test-gorm-pgsql
make conformance-test-gorm         # all three
make test-gorm                     # compile-check only: go vet -tags gormconformance ./...
make coverage-gorm                 # coverage for the node target (not COVER_MIN-gated)
make bench-gorm                    # upstream's own Benchmark* functions, node target
```

Each `conformance-test-gorm-*` target prints a per-target summary:

```
gorm-conformance[mysql]: ran=398 passed=369 expected-fail=0 unexpected-fail=11 skipped=18 stale=0 budget=0/12

gorm-conformance: RATCHET VIOLATION — see above
```

and writes a machine-readable copy to
`coverage/gorm-conformance-<target>.json`. Exit code is non-zero if the
ratchet finds a violation — see [The ratchet](#the-ratchet). It is non-zero
today on all three targets: there are real failures outside the allowlist on
every one of them (see [Observed results](#observed-results)).

`GORM_QRDB_TARGET` has **no default**. Running `go test -tags
gormconformance ./...` directly without setting it is a fatal error by
design, so a bare invocation can never be silently mistaken for having
exercised a specific target.

## The pinned tag

`GORM_SUITE.lock` pins `v1.31.2` — the latest `gorm.io/gorm` tag at the time
this harness was built, and deliberately the *exact* version `sdk/go/go.mod`
already requires for the dialector itself. Testing against any other gorm
version would exercise a combination no qrdb consumer actually runs.

`gorm.io/gorm/tests` is not a resolvable Go module path (it nests its own
`go.mod`, so `go get`/`go list -m` 404 on it) and the `gorm.io/gorm` module
zip excludes the `tests/` directory entirely (same reason). The only way to
obtain it is a pinned-tag GitHub tarball — `sync.go` fetches
`https://codeload.github.com/go-gorm/gorm/tar.gz/refs/tags/v1.31.2`, verifies
its whole-tarball sha256 against `GORM_SUITE.lock`, then verifies every
individual file it writes against its own per-file sha256 in the same lock
file. Any mismatch is a hard failure. The tarball is cached under
`$(go env GOCACHE)/qrdb-gormsuite/`, so repeat syncs are fully offline.

## Substitutes and exclusions

| File | Disposition | Reason |
|---|---|---|
| `tests_test.go` | **substituted** by `qrdb_tests_test.go` | Upstream hard-codes `switch os.Getenv("GORM_DIALECT")` over mysql/postgres/gaussdb/sqlserver/tidb/sqlite with no extension point, and connects in `init()` rather than `TestMain`. The substitute supplies the same symbols (`DB`, `OpenTestConnection`, `postgresDSN`, `gaussdbDSN`) dispatched over `GORM_QRDB_TARGET` instead, and moves the connect+migrate sequence into `TestMain`. |
| `lru_test.go` | **excluded** | Imports `gorm.io/gorm/internal/lru`, only importable from within `gorm.io/gorm`'s own module tree. Tests gorm's own LRU cache — zero qrdb surface. |

`postgresDSN`/`gaussdbDSN` exist in the substitute purely so `table_test.go`
and `migrate_test.go` compile: both files open a **throwaway** connection
with these literal upstream DSNs inside a block guarded by
`DB.Dialector.Name() != "postgres"`/`"gaussdb"`. That guard runs true for
qrdb's `pgsql` target (its `Dialector.Name()` genuinely is `"postgres"` —
it's `gorm.io/driver/postgres`, used unmodified for the wire test) but the
code path never issues a query, only `schema.Parse` — see
`qrdb_tests_test.go`'s doc comment on those two variables.

## Deviations from upstream in `RunMigrations`

`qrdb_tests_test.go`'s `RunMigrations` is upstream's function with two
intentional changes, documented in full at its declaration:

1. **Deterministic model order.** Upstream `rand.Shuffle`s the model list
   before migrating. qrdb's DDL coverage is still uneven across targets, so
   shuffling only makes the failure set non-reproducible between runs for no
   benefit.
2. **Non-fatal on migration failure.** Upstream calls `os.Exit(1)` the
   instant `AutoMigrate` errors, which would abort the *entire* suite before
   a single `Test*` function ran. That is exactly what the **pgsql** target
   does today — `gorm.io/driver/postgres` emits `bigserial` for an
   auto-increment key and qrdb's PostgreSQL surface cannot parse it, so not
   one model table gets created — and exiting fatally there would produce
   zero per-test signal, the opposite of what this harness exists to
   surface. `RunMigrations` logs the failure and lets `m.Run()` proceed, so
   every upstream test reports its own real pass or fail.

## The ratchet

`qrdb_allowlist_test.go` holds a **typed, compiled** table of
`expectedFailure` entries (`Test`, `Targets`, `Reason`, `Issue`, `Retire`) —
ordinary Go, so it cannot rot into unparseable JSON the way a hand-maintained
skip list can. `runner.go` is a separate process (`go run runner.go
-target=...`) that reruns the suite under `go test -json`, reads the
compiled table out of the same test binary via a `GORM_QRDB_DUMP_ALLOWLIST=1`
JSON dump, and reconciles the two. It exits non-zero on:

1. **An unexpected failure** — a test not in the allowlist for this target
   failed.
2. **A stale pass** — a test *in* the allowlist for this target passed. The
   entry is now a lie and must be deleted from `qrdb_allowlist_test.go`.
   This is what makes the table a ratchet instead of a skip list: nothing in
   it can silently keep excusing a test that was actually fixed.
3. **An expired entry** — any allowlist entry for this target whose
   `Retire` date has passed, regardless of whether the test still fails. An
   entry is a promise to come back and look, not a permanent exemption.
4. **A budget overrun** — the target's entry count exceeds
   `GORM_SUITE.lock`'s `budgets.<target>.maxexpected`. The budget is edited
   downward only; a raised ceiling is a code-review-visible red flag, never
   a routine change.

A closely related fifth check is also enforced: an allowlist entry whose
`Test` name never appeared in the run at all (renamed or removed upstream,
or a typo) is reported and treated the same as a stale pass.

`runner.go -target=node`'s stale-pass and unexpected-failure checks were
verified by hand during development: temporarily allowlisting a real passing
test (`TestOpen`) and temporarily removing a real failing entry
(`TestAssociationEmptyPrimaryKey`) both produced the expected violation and
non-zero exit before being reverted.

## Observed results

Measured **2026-08-13**, all three targets, against the pinned `v1.31.2`
suite. Every number below came from a run that was allowed to reach process
exit — a count read from a still-running log is a partial count, not a
result — and `-v` is required or there are no `--- PASS` lines to count at
all. Nothing was allowlisted to improve any of them: all three targets have
real failures outside the allowlist and all three exit non-zero.

| target | passed | failed | skipped | panics | observed | of 398 |
|---|---|---|---|---|---|---|
| **node** | 361 | 17 | 18 | 0 | 396 | 2 excluded, see below |
| **mysql** | 369 | 11 | 18 | 0 | 398 | whole suite |
| **pgsql** | 61 | 304 | 17 | 0 | 382 | 16 excluded, see below |

mysql was run three times; two runs gave 369/11/18 and one gave 368/12/18
(`TestSelectWithUpdate` is order-dependent). `GORM_SUITE.lock`'s ceiling for
it is the **union**, 12, so the budget is not quietly below what a real run
can produce.

The exact commands, run from `sdk/go/gorm/conformance`:

```sh
# node — the only exclusions are the two structurally-unpassable generics tests
GORM_QRDB_TARGET=node go test -tags gormconformance -v -count=1 -timeout 45m \
  -skip 'TestGenericsPreloads|TestGenericsNestedPreloads' ./...

# mysql — no -skip at all; the whole suite runs to completion
GORM_QRDB_TARGET=mysql go test -tags gormconformance -v -count=1 -timeout 40m ./...

# pgsql — 14 -skip patterns, 11 of them upstream tests that PANIC here
GORM_QRDB_TARGET=pgsql go test -tags gormconformance -v -count=1 -timeout 40m \
  -skip 'TestGenericsPreloads|TestGenericsNestedPreloads|TestPreparedStmtDeadlock|TestDifferentTypeWithoutDeclaredLength|TestMigrateArrayTypeModel|TestMigrateWithDefaultValue|TestManyToManyPreloadForNestedPointer|TestNestedPreloadWithNestedJoin|TestSelectWithVariables|TestScanRows|TestRows|TestRaw|TestUpdateReturning|TestSaveWithHooks' ./...
```

### node — 361 / 17 / 18

`TestGenericsPreloads` and `TestGenericsNestedPreloads` are unpassable for
any third-party dialector and are the only exclusions: both do
`DB.Dialector.(*mysql.Dialector).ServerVersion` guarded only by
`DB.Dialector.Name() == "mysql"`, and qrdb's dialector deliberately reports
`"mysql"` (see `sdk/go/gorm/doc.go`) without being that type, so the
assertion panics and takes the whole binary with it. Resolving that needs a
decision not made here: either substitute `generics_test.go` through
`GORM_SUITE.lock`'s `substitutes[]` mechanism (owning a large upstream file),
or stop reporting `"mysql"` (an SDK-wide behaviour change).

Everything else now runs, including `TestPreparedStmtDeadlock`, which used to
hang the suite forever and needed no product change at all — see
[The `openNode` connection-pool fix](#the-opennode-connection-pool-fix).

`runner.go` passes no `-skip`, so on **node** it hits that panic and observes
only the first 139 of 398 tests. Its own output is therefore
`ran=139 passed=126 expected-fail=5 unexpected-fail=0 stale=4`, where all four
"stale" entries are simply allowlisted tests that lie past the abort point.
That is the sole remaining node ratchet violation, and it is the panic above,
not a wrong allowlist. The **mysql** target needs no `-skip` at all and
`runner.go` reconciles its full 398.

Failing: `TestHasManyAssociation`, `TestSingleTableMany2ManyAssociation`,
`TestConcurrentMany2ManyAssociation`, `TestCreateFromMapWithTable`,
`TestMigrateColumns`, `TestMigrateConstraint`,
`TestDifferentTypeWithoutDeclaredLength`, `TestManyToManyWithMultiPrimaryKeys`,
`TestManyToManyWithCustomizedForeignKeys`,
`TestNonStdPrimaryKeyAndDefaultValues`, `TestNestedManyToManyPreload3`,
`TestCustomizedTypePrimaryKey`, `TestSubQueryWithHaving`, `TestScannerValuer`,
`TestScannerValuerWithFirstOrCreate`, `TestInvalidValuer`, `TestUpdates`.
The last few are order- or data-dependent and vary between runs.

### mysql — 369 / 11 / 18, the whole suite, no `-skip`

The MySQL wire target is the **best** of the three, which is the expected
shape rather than a surprise: it drives the stock, unmodified
`gorm.io/driver/mysql` against qrdb's MySQL wire protocol server, so gorm
emits exactly the SQL it emits for a real MySQL server and every dialector
quirk qrdb's native dialector has to approximate simply is not in the
picture. Two consequences worth naming:

- `TestGenericsPreloads` and `TestGenericsNestedPreloads` **pass** here
  (verified separately), because `DB.Dialector` genuinely *is*
  `*mysql.Dialector`. They are not excluded from the number above.
- `TestPreparedStmtDeadlock` **passes** here too (2.9s): the hang it used to
  cause was specific to the in-process node path.

Failing: `TestHasManyAssociation`, `TestSingleTableMany2ManyAssociation`,
`TestConcurrentMany2ManyAssociation`, `TestMigrateColumns`, `TestMigrateView`,
`TestMigrateWithUniqueIndexAndUnique`, `TestManyToManyWithMultiPrimaryKeys`,
`TestManyToManyWithCustomizedForeignKeys`, `TestNestedManyToManyPreload3`,
`TestSubQueryWithHaving`, `TestToSQL`, plus `TestSelectWithUpdate` in one run
of three. Nine of the eleven stable failures also fail on node, so they are
relational-surface gaps rather than wire-protocol ones.

### pgsql — 61 / 304 / 17, and why it is so much worse

**Not one model table exists on this target.** `gorm.io/driver/postgres`
emits `bigserial` for an auto-increment primary key, and qrdb's PostgreSQL
surface cannot parse it:

```
CREATE TABLE "companies" ("id" bigserial,"name" text,PRIMARY KEY ("id"))
  -> syntax error at position 41 near 'bigserial'
```

`RunMigrations` therefore creates nothing, and the overwhelming majority of
the 304 failures are the single downstream symptom `table not found: users`
(and friends). Two further PostgreSQL-dialect gaps are visible behind it and
would be the next blockers even if `serial` were handled: `current_schema()`
is not implemented (73 occurrences, from gorm's own `HasTable` probe), and
the `pg_catalog`/`pg_type` system catalogs do not exist (gorm's
`ColumnTypes`). These are described in
[What is still blocking the suite](#what-is-still-blocking-the-suite); they
are a dialect-coverage project, not a bug fix, and nothing was changed here
to paper over them.

Run with only the standard three-test skip list, this target does **not**
reach process exit: `TestDifferentTypeWithoutDeclaredLength` panics on a nil
`*ColumnType` (upstream uses `t.Errorf`, not `t.Fatalf`, and dereferences
anyway) and kills the binary at 211 of 398 observed, reporting 27 / 171 / 13.
Skipping panicking tests one at a time until the run converges took eleven
more names — every one an upstream test that dereferences a nil `*sql.Rows`
or nil `*ColumnType` that qrdb left nil — and produced the 61 / 304 / 17 in
the table. Both figures are reported because both are true; the converged one
is the useful baseline.

### The `openNode` connection-pool fix

`openNode` used to call `sql.Open("qrdb", "node://memory?database=gorm")` on
every `OpenTestConnection`. Per `sdk/go/driver/node`'s documented contract —
*one connector is one node* — that gave every caller a brand-new, **empty**
node while `RunMigrations` had created the tables on the node behind the
package-level `DB`. Those connections reported `HasTable(&User{}) == false`
and failed every statement with 1146; and because gorm's `PreparedStmtDB`
evicts a cache entry whenever `Prepare` errors, their statement cache could
never warm, so every call took the slow `Mux.Lock()` path and made gorm's own
four-party lock inversion deterministic — `TestPreparedStmtDeadlock` hung
forever.

The fix is one shared **node**, one `*sql.DB` **per connection**
(`sql.OpenDB` over a shared, deliberately non-`io.Closer` connector wrapper).
Both halves matter: sharing a single `*sql.DB` instead is equally wrong,
because upstream tests call `SetMaxOpenConns` on the connection they were
handed (`TestPreparedStmtDeadlock` pins it to 1) and never restore it, which
starves every later test — measured as a hang in `TestOmit`, blocked in
`database/sql.(*DB).conn`. This was a harness bug; no product code changed
for it.

### qrdb product bugs found by this harness

Found and fixed while getting the **wire** targets to run at all:

1. **The PostgreSQL wire server ignored the client's database.**
   `pkg/sql/pgsql`'s `buildSQLContext` set the session's current database to
   the server-wide `--sql-database` default on every statement, discarding the
   `database` parameter the client sent in its PostgreSQL startup message. A
   client connecting with `dbname=gorm` was served the default instead, so
   every statement failed with `service: database not found: qrdb (tenant:
   gorm)` — and, for a client whose tenant *did* own the default, would have
   silently read and written the wrong keyspace. The MySQL wire server has
   always carried its own per-connection database; this was the PostgreSQL
   side of the same contract. Fixed in `pkg/sql/pgsql/parse.go`, with
   regression tests in `pkg/sql/pgsql/session_database_test.go`.

Found earlier, on the node target, each of which had been blocking the entire
suite:

2. **`kv.OpPutIfAbsent` / `protocol.OpMVCCPutIfAbsent` had no handler in
   `pkg/engine.MVCCEngine`** (only `pkg/store.PebbleStateMachine` had them),
   so every conditional write against an MVCC-engine-backed store failed the
   whole proposal with `protocol.UnknownOpError` — including the metadata
   write `service.TableService.CreateTable` performs, and therefore every
   `CREATE TABLE`.
3. **Interactive transactions failed on the first read of a not-yet-existing
   key.** `pkg/sql/provider.WriteSet` treated only `*service.KeyNotFoundError`
   as snapshot absence, but a real MVCC store reports a key with no version at
   or before the pinned revision as `*service.RevisionNotFoundError`. The
   package's own test doubles returned the former for both cases, which is why
   it had never surfaced.
4. **`information_schema.TABLES` was empty for every database.**
   go-mysql-server calls `PeekNextAutoIncrementValue` on every
   `AutoIncrementTable` while building that view and tolerates exactly one
   error — `sql.ErrNoAutoIncrementCol`. qrdb returned its own typed error for
   a table with no AUTO_INCREMENT column, which aborted the whole iteration.
   `Migrator.HasTable` therefore reported `false` for tables that plainly
   existed, `AutoMigrate` re-issued `CREATE TABLE`, and everything downstream
   failed with `table already exists`.

### What FOREIGN KEY support changed

`pkg/sql/provider.RelationalTable` implements `sql.ForeignKeyTable`:
constraint metadata is persisted in the replicated table catalog and reported
back, and go-mysql-server does the enforcing from it (parent-existence checks,
`CASCADE`/`SET NULL`/`SET DEFAULT`/`RESTRICT`, cycle detection). `AutoMigrate`
on the shared `User` model no longer aborts on
`CONSTRAINT fk_users_company FOREIGN KEY ...`, so `RunMigrations` completes and
the models every downstream test depends on actually exist.

That took the node allowlist from 138 entries to 21: **117 entries were
deleted because their tests now pass** (each verified passing in three
consecutive runs before removal). `GORM_SUITE.lock`'s ceiling of 172 was not
touched.

The `openNode` fix above then deleted **12 more**, taking the table from 21
entries to 9, each again verified passing in three consecutive whole-suite
runs first. Two of the nine that remain (`TestGenericsPreloads`,
`TestGenericsNestedPreloads`) are not qrdb gaps at all and are now labelled
`upstream:dialector-type-assertion` rather than `qrdb:relational-gap`.
Neither wire target has any allowlist entry: both were first measured on
2026-08-13, so every failure on them is reported as unexpected rather than
pre-excused.

### What is still blocking the suite

- **PostgreSQL dialect coverage (pgsql only, and it is the whole story
  there).** `serial`/`bigserial`/`smallserial` are not translated to an
  auto-increment column by `pkg/sql/engine`'s PG→GMS translator, which is a
  regex rewriter with no DDL type mapping at all; `current_schema()` is not
  implemented; the `pg_catalog`/`pg_type` system catalogs do not exist. Each
  is a deliberate design question (how much of PostgreSQL's dialect and system
  catalogs qrdb intends to present), not a defect to patch in passing.
- **Association loading gaps** — `TestNestedManyToManyPreload3`,
  `TestManyToManyWithMultiPrimaryKeys`,
  `TestManyToManyWithCustomizedForeignKeys`, `TestHasManyAssociation`,
  `TestSingleTableMany2ManyAssociation`, `TestConcurrentMany2ManyAssociation`
  — all failing on **both** node and mysql, so they are relational-surface
  gaps rather than transport ones.
- **Migrator/DDL gaps** — `TestMigrateColumns`, `TestMigrateView`,
  `TestMigrateWithUniqueIndexAndUnique` (mysql); `TestMigrateConstraint`,
  `TestDifferentTypeWithoutDeclaredLength`,
  `TestNonStdPrimaryKeyAndDefaultValues` (node). Column defaults and
  unique-constraint metadata are the recurring themes.
- **Two upstream generics tests on node**, described above.

### Harness limitations worth knowing

- **A panicking test leaks the `qrdbd` subprocess.** `TestMain`'s teardown
  runs after `m.Run()`, and a panic in a test goroutine terminates the process
  before that, so the daemon survives. It only bites the pgsql target (which
  panics) and only until the panicking upstream tests are dealt with, but a
  leaked daemon holds its ports and can make a *later* run fail to start.
  `pkill -f qrdb-gorm-conformance-data` clears them.
- **`qrdbdharness.ReserveTCPPorts` bind-verifies on `127.0.0.1` while `qrdbd`
  binds `0.0.0.0`.** With `SO_REUSEADDR` (which Go sets), a `127.0.0.1:P`
  probe succeeds while something else is already listening on `0.0.0.0:P`, so
  a leaked daemon from a previous run can be handed the same port and the new
  daemon then dies with `bind: address already in use`. Observed exactly once,
  after a leak from the bullet above.
