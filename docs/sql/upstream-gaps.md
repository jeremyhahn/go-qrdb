# Upstream gaps in go-mysql-server

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-21
> Register of the go-mysql-server and vitess defects QRDB still meets; each fix belongs upstream.

QRDB's SQL engine is [go-mysql-server](https://github.com/dolthub/go-mysql-server)
(GMS) with a vitess-derived parser. The entries below are defects or omissions
in that dependency that QRDB code does not fully work around. QRDB does not
fork GMS or vitess for them.

Each entry below states what was measured, where the defect is, and what the
fix looks like, so it can be filed and tracked without re-deriving any of it.

Pinned versions (`go.mod`):
`github.com/dolthub/go-mysql-server v0.20.1-0.20260812191550-bda99d66b4bf`
with `github.com/dolthub/vitess v0.0.0-20260728212736-0542037326d7`.

---

## 1. `UPDATE … RETURNING` and `DELETE … RETURNING` do not parse

**Severity: low — a clean, early error.**

**Worked around on the PostgreSQL wire, so the forms work there.**
The grammar gap is only about parsing: the `Update` and `Delete` AST nodes
carry a `Returning` field the planner and executor honor when it is set
programmatically. `pkg/sql/pgsql` splits the clause from the statement text
(`engine.SplitUpdateDeleteReturning`, called from `ParseHandler.HandleParse` in
`pkg/sql/pgsql/parse.go`), translates the head normally, parses the clause's
expression list via a throwaway SELECT, and reattaches it to the AST before
execution — see `(*ParseHandler).buildReturningStatement` in
`pkg/sql/pgsql/returning.go`. Every other entry path, the MySQL wire included,
still meets the gap.

GMS's plan and rowexec layers implement RETURNING for update and delete
(`plan/update.go`, `plan/delete.go`, `rowexec/update.go`, `rowexec/delete.go`
all carry `Returning` / `returnExprs`), but the vitess grammar only added the
clause to the `INSERT` production. Parsing either form fails with
`syntax error at position N near 'RETURNING'`, so the implemented plan nodes are
unreachable from SQL text.

**Fix.** Add RETURNING to the `UPDATE` and `DELETE` productions in the vitess
grammar and populate the existing plan fields.

QRDB turns this into a typed error up front rather than a bare syntax error —
see `updateOrDeleteLeadRe` in `pkg/sql/engine/translator.go`.

---

## 2. Server-side cursors race on `Conn.StatusFlags`

**Severity: medium — a known limitation of the MySQL wire. Present only in
builds with the `qrdb_mysql` tag (`WITH_QRDB_MYSQL`).**

A MySQL client opens a server-side cursor by sending `COM_STMT_EXECUTE` with a
cursor flag, then reads the rows with `COM_STMT_FETCH`. That path is served
entirely by the upstream stack: vitess runs the statement through
go-mysql-server's handler on one goroutine, which streams rows to the
connection goroutine, and the connection goroutine answers each
`COM_STMT_FETCH` itself. QRDB's statement gate is not consulted for a fetch.

go-mysql-server's handler reads and writes vitess `Conn.StatusFlags` (its
autocommit and in-transaction bits, and the cursor-exists check) on the
goroutine running the cursor's statement, while vitess sets and clears
`SERVER_STATUS_CURSOR_EXISTS` and `SERVER_STATUS_LAST_ROW_SENT` on the same
field from the connection goroutine. Nothing synchronizes the two; the race
detector reports it.

An idle cursor is bounded by the connection timeouts: `sql.wait_timeout` /
`sql.write_timeout` (`pkg/config.SQLConfig`) are wired to vitess's
`ListenerConfig.ConnReadTimeout` / `ConnWriteTimeout` in
`pkg/sql/mysql/server.go`'s `(*Server).Start`, so a connection issuing no
command for `wait_timeout` is closed, which discards its cursor.

**What clients should do.** A client that needs bounded resource use before
`sql.wait_timeout` closes the connection should not request server-side
cursors. MySQL Connector/J requests one only with `useCursorFetch=true` and a
positive fetch size: leave `useCursorFetch=false` (its default). With
libmysqlclient, do not set `STMT_ATTR_CURSOR_TYPE` to `CURSOR_TYPE_READ_ONLY`
through `mysql_stmt_attr_set`. go-sql-driver/mysql never requests one. Without
a cursor flag, an execute returns its whole result set and is bounded by
`sql.query_timeout` like any other statement.

**Fix.** Synchronize `Conn.StatusFlags` between the handler and the
connection goroutine in vitess and go-mysql-server.
