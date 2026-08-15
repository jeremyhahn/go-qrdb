# Upstream gaps in go-mysql-server

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

**Severity: medium — protocol-level failure, not silent.**

The same probe:

```
INSERT INTO people (id, name) VALUES (1, 'd')
  ON DUPLICATE KEY UPDATE name = VALUES(name) RETURNING id
```

reports a one-column schema (`[id]`) and then produces a **four-value row**
(`[1 a 1 d]` — the old and new tuples concatenated). Every consumer that trusts
the declared schema is wrong about the row.

On the PostgreSQL wire this is fatal rather than silent: psql-wire refuses to
write a row whose width disagrees with the RowDescription it already sent.

**Fix.** The RETURNING projection has to be applied to the correct tuple in the
on-duplicate path, so that the emitted row matches `returnSchema`.

---

## 3. `UPDATE … RETURNING` and `DELETE … RETURNING` do not parse

**Severity: low — a clean, early error.**

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
documented to be deleted the day the grammar gains the token.

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

**Served by QRDB, so this one is no longer a live gap here.** The PostgreSQL
engine swaps in `pgcatalog.InfoSchema`, a wrapper that adds
`constraint_column_usage` (built from the same catalog traversal
go-mysql-server's own `key_column_usage` uses) and delegates everything else.
It is scoped to the PostgreSQL dialect, so the MySQL endpoint's
information_schema is untouched. Upstream would still be the better home for a
SQL-standard view, but nothing here is blocked on it.

---

## 5. A scalar subquery in HAVING is evaluated against the wrong row set

**Severity: medium — a correct query is rejected.**

```sql
SELECT AVG(age) AS avgage FROM users
GROUP BY name
HAVING AVG(age) > (SELECT AVG(age) FROM users)
```

fails with `the subquery returned more than 1 row`, even though the subquery is
an ungrouped aggregate that returns exactly one row — `SELECT AVG(age) FROM
users` on its own returns `[[20]]`.

Reproduced on a **stock in-memory go-mysql-server engine with no QRDB code in
the path**, so this is not an artifact of QRDB's tables or provider:

| query | result |
|---|---|
| `SELECT AVG(age) FROM users` | 1 row, `20` |
| `… GROUP BY name HAVING AVG(age) > 15` | 2 rows |
| `… GROUP BY name HAVING AVG(age) > (SELECT AVG(age) FROM users)` | **error** |

HAVING itself works and the subquery itself works; only the combination fails,
which points at the subquery being evaluated against the pre-aggregation row
set rather than being executed once as the scalar it is.

**Impact.** It is the only unexpected failure the `node` target has, and it
fails identically on `mysql`. Upstream test: `TestSubQueryWithHaving`.

