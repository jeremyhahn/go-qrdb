# Upstream gaps in go-mysql-server

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Register of go-mysql-server defects QRDB depends on; each is fixed upstream, not compensated here.

QRDB's SQL engine is [go-mysql-server](https://github.com/dolthub/go-mysql-server)
(GMS) with a vitess-derived parser. A small number of behaviours QRDB depends on
are defects or omissions in that dependency rather than in QRDB, and the
decision on record is to fix them **upstream** rather than to fork GMS or to
compensate for it inside QRDB — a compensation layer here would be exactly the
kind of shim [CLAUDE.md](../../CLAUDE.md) forbids, and for two of the three it
could not be complete anyway.

Each entry below states what was measured, where the defect is, and what the
fix looks like, so it can be filed and tracked without re-deriving any of it.

Pinned version at the time of measurement:
`github.com/dolthub/go-mysql-server v0.20.1-0.20260812191550-bda99d66b4bf`
with `github.com/dolthub/vitess v0.0.0-20260728212736-0542037326d7`.

---

## 1. `INSERT IGNORE … RETURNING` silently discards the RETURNING row

**Severity: high — silent wrong data, not an error.**

**Worked around in QRDB, so this one is no longer a live gap here.** The
engine installs a `PriorityBuilder` on the exec builder
(`pkg/sql/engine/dml_returning.go`) that intercepts exactly the two broken
statement shapes (an ignoring or upserting INSERT that carries RETURNING) and
repairs the row stream without touching any other statement. Upstream remains
the better home for the fix; nothing here is blocked on it.

`AddAccumulatorIter` (`sql/rowexec/dml_iters.go`) decides whether a DML
statement returns rows or a single `OkResult`. It inspects the iterator tree and
has a case for `*plan.TableEditorIter`, unwrapping it to check whether the inner
`insertIter`/`updateIter`/`deleteIter` carries RETURNING expressions:

```go
case *plan.TableEditorIter:
    switch innerIter := i.InnerIter().(type) {
    case *insertIter:
        if len(innerIter.returnExprs) > 0 { ... }
```

It has **no case for `plan.CheckpointingTableEditorIter`**, which is precisely
what an ignoring insert builds (`sql/rowexec/dml.go`):

```go
if ii.Ignore {
    return plan.NewCheckpointingTableEditorIter(insertIter, ed), nil
}
```

So the RETURNING iterator is dropped and the statement falls through to
`defaultAccumulatorIter`, producing an `OkResult`.

Measured against a stock in-memory GMS engine (no QRDB code in the path):

| statement | declared schema | executed schema | rows |
|---|---|---|---|
| `INSERT INTO people (name) VALUES ('a') RETURNING id` | `[id]` | `[id]` | `[[1]]` |
| `INSERT IGNORE INTO people (name) VALUES ('b') RETURNING id` | `[id]` | `[__ok_result__]` | none |

**Why it matters to QRDB.** `ON CONFLICT … DO NOTHING` translates to
`INSERT IGNORE` (see `pkg/sql/engine/translator.go`), and
`gorm.io/driver/postgres` reads every generated primary key back with
`INSERT … RETURNING "id"`. A GORM application doing an upsert therefore receives
the `OkResult`'s rows-affected count where it expects the row's primary key. The
widths happen to agree, so nothing downstream notices.

**Fix.** Add a `plan.CheckpointingTableEditorIter` case to `AddAccumulatorIter`
mirroring the `*plan.TableEditorIter` one. Note it has a **value** receiver
(`func (c CheckpointingTableEditorIter) InnerIter() sql.RowIter`), so the type
switch case is `plan.CheckpointingTableEditorIter`, not a pointer.

QRDB detects this rather than serving it: the PostgreSQL wire refuses the
statement with `cached plan must not change result type` (SQLSTATE 0A000) when
the executed schema disagrees with what was described, so the corruption
surfaces as an error instead of as data. See
`pkg/sql/pgsql/errors.go`'s `CachedPlanResultTypeError`.

---

## 2. `INSERT … ON DUPLICATE KEY UPDATE … RETURNING` emits a row wider than its schema

**Severity: medium on the PostgreSQL wire, where it is a protocol-level
failure, not silent; high on the MySQL wire, where it is silent wrong data —
see "Proof" below for how that second half was measured.**

**Worked around in QRDB, so this one is no longer a live gap here.** The same
`dmlReturningBuilder` that repairs item 1 (`pkg/sql/engine/dml_returning.go`)
also intercepts this shape: any `InsertInto` carrying `RETURNING` together
with `OnDupExprs`. Its `dmlReturningIter.Next` recognises the duplicate-update
path's old+new concatenation by width (exactly `2×` the destination schema's
column count) and re-projects the `RETURNING` expressions against the **new**
tuple, so the emitted row matches `returnSchema` on every wire. Upstream
remains the better home for the fix; nothing here is blocked on it.

The same probe:

```
INSERT INTO people (id, name) VALUES (1, 'd')
  ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id
```

reports a one-column schema (`[id]`) and then produces a **four-value row**
(`[1 a 1 d]` — the old and new tuples concatenated). Every consumer that trusts
the declared schema is wrong about the row.

On the PostgreSQL wire this is fatal rather than silent: psql-wire refuses to
write a row whose width disagrees with the RowDescription it already sent. The
MySQL wire fails differently, and worse: measured by deliberately reverting
the repair while writing `dml_returning_wire_test.go`, go-mysql-server's wire
writer encodes only the first `len(returnSchema)` values of whatever row the
iterator yields rather than rejecting the mismatch, so a client silently gets
the **old** tuple's values back — the pre-update row — under a RETURNING
clause that asked for the new one. That is the same class of defect as item 1
(silent wrong data), not a clean failure; the "protocol-level failure" framing
above held only for the PostgreSQL wire, which this repair also serves since
`pkg/sql/engine` is shared by both.

**Fix (upstream).** The RETURNING projection has to be applied to the correct
tuple in the on-duplicate path, so that the emitted row matches `returnSchema`.

**Proof.** Three tests pin the repaired behaviour, each at a different layer
so a regression anywhere in the chain is caught where it happens:

- `pkg/sql/engine/dml_returning_test.go`'s
  `TestOnDuplicateKeyReturning_RowMatchesDeclaredSchema` — against the engine
  directly, no transport in the path.
- `pkg/sql/mysql/dml_returning_wire_test.go`'s
  `TestMySQLWire_OnDuplicateKeyReturning_RowMatchesDeclaredSchema` — over a
  real MySQL wire connection (`go-sql-driver/mysql`), asserting the
  post-update value comes back rather than the pre-update one a naive
  width-truncation would leak.
- `sdk/go/driver/dml_returning_e2e_test.go`'s
  `TestEmbeddedRelational_OnDuplicateKeyReturning_RowMatchesDeclaredSchema` —
  through `database/sql` itself, the conformance surface a GORM application
  actually uses: an unrepaired row would surface here as the embedded
  driver's own `*driver.RowWidthError` (`sdk/go/driver/rows_embedded.go`),
  since `database/sql` sizes its scan destination from `Columns()`.

Each file also covers the companion shape, `...InsertPathUnaffected`: the
same statement when no conflict occurs, which upstream already projects
correctly and which the repair must leave alone rather than re-project.

---

## 3. `UPDATE … RETURNING` and `DELETE … RETURNING` do not parse

**Severity: low — a clean, early error.**

**Worked around in QRDB on the PostgreSQL wire, so the forms WORK there.**
The grammar gap is only about parsing: the `Update` and `Delete` AST nodes
carry a `Returning` field the planner and executor fully honor when it is set
programmatically. `pkg/sql/pgsql` strips the clause from the statement text,
translates the head normally, parses the clause's expression list via a
throwaway SELECT, and reattaches it to the AST before execution — see
`buildReturningStatement` in `pkg/sql/pgsql/returning.go`. The engine-level
typed error below still guards every OTHER entry path.

GMS's plan and rowexec layers implement RETURNING for update and delete
(`plan/update.go`, `plan/delete.go`, `rowexec/update.go`, `rowexec/delete.go`
all carry `Returning` / `returnExprs`), but the vitess grammar only added the
clause to the `INSERT` production. Parsing either form fails with
`syntax error at position N near 'RETURNING'`, so the implemented plan nodes are
unreachable from SQL text.

**Fix.** Add RETURNING to the `UPDATE` and `DELETE` productions in the vitess
grammar and populate the existing plan fields.

QRDB turns this into a typed error up front rather than a bare syntax error —
see `updateOrDeleteLeadRe` in `pkg/sql/engine/translator.go`, which is
documented to be deleted the day the grammar gains the token (at which point
the AST reattachment in `pkg/sql/pgsql/returning.go` also collapses into a
plain translation).

---

## 4. `information_schema.constraint_column_usage` is not implemented

**Severity: medium — blocks PostgreSQL schema introspection.**

`gorm.io/driver/postgres`'s Migrator resolves a table's primary-key and unique
constraints with a three-way join across `information_schema.table_constraints`,
`information_schema.constraint_column_usage`, and `information_schema.columns`.
GMS's `information_schema` does not serve `constraint_column_usage`, so the
query fails with `table not found: constraint_column_usage`.

This one is not reachable from QRDB at all: `information_schema` is GMS's own
built-in database, not something a `DatabaseProvider` contributes to. (QRDB does
serve PostgreSQL's *other* introspection namespace itself — see
`pkg/sql/pgcatalog` — because `pg_catalog` is a schema a provider can supply.)

**Fix.** Implement the view in GMS's `sql/information_schema`.

**Worked around in QRDB, so this one is no longer a live gap here.**
go-mysql-server exposes `NewInformationSchemaTablesToAdd`, documented as the
hook Doltgres injects PostgreSQL-specific tables and views through, and
`pkg/sql/infoschema` uses it to serve `constraint_column_usage` built from the
same catalog traversal go-mysql-server's own `key_column_usage` uses. Upstream
would still be the better home for a standard view, but nothing here is
blocked on it.

---

## 5. Server-side cursors race on `Conn.StatusFlags`; the idle-bound half is fixed in QRDB

**Severity: medium — a known limitation of the MySQL wire, the `StatusFlags`
race half of it. Present only in builds with the `qrdb_mysql` tag
(`WITH_QRDB_MYSQL`).**

A MySQL client opens a server-side cursor by sending `COM_STMT_EXECUTE` with a
cursor flag, then reads the rows with `COM_STMT_FETCH`. That path is served
entirely by the upstream stack: vitess runs the statement through
go-mysql-server's handler on one goroutine, which streams rows to the
connection goroutine, and the connection goroutine answers each
`COM_STMT_FETCH` itself. QRDB's statement gate is not consulted for a fetch.

- **A data race.** go-mysql-server's handler reads and writes vitess
  `Conn.StatusFlags` (its autocommit and in-transaction bits, and the
  cursor-exists check) on the goroutine running the cursor's statement, while
  vitess sets and clears `SERVER_STATUS_CURSOR_EXISTS` and
  `SERVER_STATUS_LAST_ROW_SENT` on the same field from the connection
  goroutine. Nothing synchronizes the two; the race detector reports it. **This
  half remains an upstream gap** — QRDB does not fork vitess or go-mysql-server
  to work around it, and tracks it for filing upstream if it is still present
  at release-candidate time.
- **An idle cursor is not bounded by `sql.query_timeout`.** It bounds a cursor
  while it produces rows, not while it waits: a cursor the client stops
  fetching from holds its statement, and what that statement holds, until the
  client fetches to the end, closes the statement or disconnects, or a seal
  closes the session. **Worked around in QRDB, so this half is no longer a
  live gap here.** `sql.wait_timeout` / `sql.write_timeout`
  (`pkg/config.SQLConfig`) are wired to vitess's `ListenerConfig.ConnReadTimeout`
  / `ConnWriteTimeout` in `pkg/sql/mysql/server.go`'s `(*Server).Start`, the
  single site that constructs the listener. Vitess re-arms a read deadline
  before every `Read` on the connection, so a connection issuing no command
  for `wait_timeout` — opening a cursor and never fetching again is exactly
  that — fails its next read and is closed, which discards the cursor and
  releases its statement's resources through the listener's existing
  per-connection cleanup (`Conn.discardCursor`, called from its deferred
  `ConnectionClosed`). This bounds the connection, not just the statement, so
  it reaches the gap `sql.query_timeout` cannot: a cursor is idle between
  fetches by construction, and the fix does not touch the handler/connection
  boundary the `StatusFlags` race lives on, so it does not mask or interact
  with that race.

**What clients should do.** A client that needs bounded resource use before
`sql.wait_timeout` closes the connection should still not request server-side
cursors. MySQL Connector/J requests one only with `useCursorFetch=true` and a
positive fetch size: leave `useCursorFetch=false` (its default). With
libmysqlclient, do not set `STMT_ATTR_CURSOR_TYPE` to `CURSOR_TYPE_READ_ONLY`
through `mysql_stmt_attr_set`. go-sql-driver/mysql never requests one. Without
a cursor flag, an execute returns its whole result set and is bounded by
`sql.query_timeout` like any other statement.

**Fix.** Synchronize `Conn.StatusFlags` between the handler and the
connection goroutine in vitess and go-mysql-server. QRDB does not fork either
dependency to work around it; this remains the one upstream-only piece of this
item.
