# SQL — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

SQL reaches callers two ways, and they are genuinely different surfaces:

1. **The ad-hoc query capability** — one call, present on all six transports.
2. **The wire protocol servers** — MySQL and PostgreSQL listeners on their own
   ports, speaking their own protocols. These are not "a transport" in the
   six-transport sense; they are separate servers over the same engine.

## Capability matrix — ad-hoc query

gRPC service `qrdb.v1.QRDBService`; the Unix socket transport serves the same
gRPC wire over a UDS.

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Ad-hoc SQL query | `POST /sql/query` and `POST /v1/sql/query` | `SQLQuery` | `sql.Query` | `qrdb_sql_query` | in-process `sdk/go/driver` | **`kv:range`** |

Both REST paths are registered and behave identically; `/v1/sql/query` is the
versioned spelling.

**The permission is `kv:range`, not a SQL-specific grant.** There is no
`sql:query` permission constant. An ad-hoc statement can reach relational
tables and collections as well as the `kv` table, so this grant is weaker than
the capability it guards. Recorded as a gap rather than presented as a design —
the correct vocabulary is an owner decision. See
[engineering.md](engineering.md#known-authorization-gap).

Without a `qrdb_sql` build, **every transport answers `/sql/query` with its
nil-service error** — the route exists, the service behind it does not.

## Wire protocol endpoints

| Protocol | Default port | Config key | Flag | Build tag |
|---|---|---|---|---|
| MySQL | 3306 | `server.mysql_port`, `protocols.mysql` | `--enable-mysql`, `--mysql-port` | `qrdb_mysql` |
| PostgreSQL | 5432 | `server.pgsql_port`, `protocols.postgresql` | `--enable-pgsql`, `--pgsql-port` | `qrdb_pgsql` |

Both are **mTLS-only**. Certificate CN maps to the username; OU maps to the
tenant ID. There is no password authentication, and TLS plus auth must both be
enabled or the daemon refuses to start.

## Request and response — ad-hoc query

Request: the statement text plus the target database. Response: column
metadata and rows, rendered by the client per `--output`.

## Status and error mapping

The ad-hoc endpoint maps engine errors to transport status; the wire protocols
return the protocol's own codes directly.

| Condition | REST | gRPC / Unix | Wire protocol |
|---|---|---|---|
| Success | 200 | `OK` | normal result set |
| Syntax or analysis error | 400 | `INVALID_ARGUMENT` | MySQL parse error |
| Duplicate key | 409 | `ALREADY_EXISTS` | MySQL **1062** |
| Transaction conflict | 409 | `ABORTED` | MySQL **1213** / SQLSTATE **40001** |
| Database / table / column access denied | 403 | `PERMISSION_DENIED` | MySQL **1044** / **1142** / **1143** |
| Cached plan result-type change | — | — | SQLSTATE **0A000** (PostgreSQL only) |
| Missing `kv:range` | 403 | `PERMISSION_DENIED` | connection refused at auth |
| Barrier sealed | 503 | `UNAVAILABLE` | connection refused |
| Built without `qrdb_sql` | 501 | `UNIMPLEMENTED` | listener not started |

Statement-level ceilings surface as their own errors before any proposal is
made: 65535 bytes for an encoded key, 4 MB for an encoded row, 8 MiB for one
statement's estimated compares plus ops.

## Catalog surfaces

- **`pg_catalog`** — `pg_type`, `pg_class`, `pg_namespace`, `pg_description`,
  `pg_indexes`, served from `pkg/sql/pgcatalog`, reachable qualified and bare,
  read-only, tenant-scoped, **PostgreSQL clients only**.
- **`information_schema.constraint_column_usage`** — added through
  go-mysql-server's `NewInformationSchemaTablesToAdd` hook by
  `pkg/sql/infoschema`. Reports PRIMARY KEY and UNIQUE constraints only; a
  FOREIGN KEY's referenced columns cannot be named from an
  `sql.ForeignKeyConstraint` alone, and a plausible wrong answer would be worse
  than a true partial one.
- **`mysql`** — served as an empty stand-in, purely because go-mysql-server's
  plan builder resolves it while building `GRANT`/`REVOKE`. QRDB does not
  implement MySQL's grant tables.

## Pagination

The ad-hoc endpoint returns the full result set; bound it with SQL's own
`LIMIT`. The wire protocols stream result sets as their protocols define.

## Versioning

`POST /v1/sql/query` is the versioned REST path; `POST /sql/query` is
registered alongside it. Neither is deprecated.
