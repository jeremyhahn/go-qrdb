# SQL — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Two surfaces: the `qrdb sql` client verb group, and the `qrdbd` flags that turn
the wire protocols on. Both require a `qrdb_sql` build; the wire servers
additionally require `qrdb_mysql` / `qrdb_pgsql`.

**`qrdb sql`, `database update-protocol`, `database update-sql`, and
`database create`'s `--sql-concurrency` flag all exist only on a `qrdb`
built with `WITH_QRDB_SQL=1` (the default).** A `qrdb` built with
`WITH_QRDB_SQL=0` has no `sql` command group, no `database
update-protocol` subcommand (the one that sets `--mysql` / `--postgresql`
on an existing database), no `database update-sql` subcommand, and no
`--sql-concurrency` flag on `database create` — there is no SQL engine on
that binary for a concurrency mode to apply to. `database update-protocol`
itself further splits its two flags: `--mysql` exists only with
`WITH_QRDB_MYSQL=1`, `--postgresql` only with `WITH_QRDB_PGSQL=1` (both
imply `WITH_QRDB_SQL=1`), so a build with the SQL engine but only one wire
protocol compiled in has `update-protocol` with just that one flag.
`qrdbd start`'s `--sql-database` flag follows the same rule as `qrdb sql`:
present only under `WITH_QRDB_SQL=1`.

## `qrdb sql query [sql]`

Execute an ad-hoc SQL query against a database's KV, entity and collection
surface through the go-mysql-server engine.

The query is supplied **either** as a positional argument **or** from a file
(`--file -` reads stdin). Exactly one of the two forms is accepted.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--database` | string | `system` | target database |
| `--file` | string | — | read the query from a file, `-` for stdin |

Output is governed by the root persistent `--output` flag
(`table \| json \| yaml`). All other root persistent flags apply — see
[../key-value/cli.md](../key-value/cli.md#global-flags).

```bash
qrdb sql query "SELECT * FROM kv"
qrdb sql query --database mydb "SELECT * FROM kv WHERE key = 'foo'"
qrdb sql query --file query.sql
echo "SELECT * FROM kv" | qrdb sql query --file -
```

The verb runs over every `--transport`. With `--transport embedded` it opens
the data directory in-process and needs no running server.

**Exit codes.** `0` on success; `1` on any error, with the message on stderr.

`qrdb sql` has exactly one verb. Full DDL, DML and transaction control are
available over the wire protocols and the in-process driver, not through this
verb.

A collection is one of the tables this reaches, and its first column is `_id` —
the document's ID, honoured on write:

```bash
qrdb sql query --database app "INSERT INTO events (_id, kind) VALUES (42, 'deploy')"
qrdb collection doc get events 42 --database app   # the same document
```

Claiming an `_id` another document holds exits non-zero with a duplicate-key
error rather than replacing it. See
[../collections/user.md](../collections/user.md#doing-the-same-thing-over-sql).

## Server flags — `qrdbd start`

```bash
qrdbd start \
  --enable-mysql --mysql-port 3306 \
  --enable-pgsql --pgsql-port 5432 \
  --sql-database qrdb \
  --tls-ca-file ca.pem --tls-cert-file server.pem --tls-key-file server-key.pem
```

| Flag | Default | Description |
|------|---------|-------------|
| `--enable-mysql` | `false` | Enable MySQL wire protocol server |
| `--mysql-port` | `3306` | MySQL wire protocol server port |
| `--enable-pgsql` | `false` | Enable PostgreSQL wire protocol server |
| `--pgsql-port` | `5432` | PostgreSQL wire protocol server port |
| `--sql-database` | `qrdb` | Default SQL database name |


TLS and auth must both be enabled when either protocol is active; the daemon
refuses to start otherwise.

**`--enable-mysql` / `--mysql-port` and `--enable-pgsql` / `--pgsql-port` each
exist only on a `qrdbd` built with the matching tag** (`WITH_QRDB_MYSQL=1` /
`WITH_QRDB_PGSQL=1`, both the default). A build with `WITH_QRDB_MYSQL=0` has
no `--enable-mysql` or `--mysql-port` flag at all — `qrdbd start --help`
does not list them, and passing either is cobra's `unknown flag`, not a
silently accepted no-op; the same applies to `--enable-pgsql` /
`--pgsql-port` and `WITH_QRDB_PGSQL=0`. The config-file path is refused
instead of rejected at parse time, since YAML has no notion of "unknown
flag": starting the daemon with `protocols.mysql: true` on a binary built with
`WITH_QRDB_MYSQL=0`
is refused at startup, before any listener is attempted, with a typed
configuration error naming the missing build tag (`protocols.mysql: not
compiled in: requires build tag qrdb_mysql`); the same applies to
`protocols.postgresql` and `WITH_QRDB_PGSQL=0`. Neither path is ever
silently ignored.

## Connecting with a SQL client

### MySQL

```bash
mysql --ssl-cert=client.pem --ssl-key=client-key.pem --ssl-ca=ca.pem \
  -h 127.0.0.1 -P 3306 -e "SELECT * FROM kv"
```

### PostgreSQL

```bash
psql "sslmode=verify-full sslcert=client.pem sslkey=client-key.pem sslrootcert=ca.pem \
  host=127.0.0.1 port=5432 dbname=qrdb" -c "SELECT * FROM kv"
```


Both protocols are mTLS-only: the certificate CN becomes the username and the
OU becomes the tenant ID. There is no password authentication to configure.

## Parity

`qrdb sql query` is driven on every network transport by `sql_query_cli_test.go`
and `sql_query_tenant_scope_test.go` (`make integration-test-sql`).
Per-transport accounting: the `sql` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md). The wire
protocol servers are exercised by the same target's wire-protocol suites and, as
unit tests, by `make test-sql-mysql` and `make test-sql-pgsql`, rather than by
the CLI suite, since they have no `qrdb` verb.
