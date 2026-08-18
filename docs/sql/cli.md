# SQL — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Two surfaces: the `qrdb sql` client verb group, and the `qrdbd` flags that turn
the wire protocols on. Both require a `qrdb_sql` build; the wire servers
additionally require `qrdb_mysql` / `qrdb_pgsql`.

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

`qrdb sql` has exactly one verb. Full DDL, DML and transaction control are
available over the wire protocols and the in-process driver, not through this
verb.

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

`qrdb sql query` has a `CommandDefinition` in the integration parity suite. The
wire protocol servers are exercised by `make test-sql-mysql` and
`make test-sql-pgsql` rather than by the CLI parity suite, since they have no
`qrdb` verb.
