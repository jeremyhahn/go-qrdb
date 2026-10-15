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
| Table truncated or its definition changed under the statement (retry it) | 409 | `ABORTED` | MySQL **1213** / SQLSTATE **40001** |
| Table created by v0.20.0–v0.21.0 (drop and re-create it) | 412 | `FAILED_PRECONDITION` | MySQL **1459** / SQLSTATE **55000** |
| Database / table / column access denied | 403 | `PERMISSION_DENIED` | MySQL **1044** / **1142** / **1143** |
| `CREATE DATABASE` without `databases:create`, `DROP DATABASE` without `databases:delete` or of another tenant's database, or either with no tenant-scoped lifecycle configured | — | — | statement error; nothing is created or dropped |
| Cached plan result-type change | — | — | SQLSTATE **0A000** (PostgreSQL only) |
| Missing `kv:range` | 403 | `PERMISSION_DENIED` | connection refused at auth |
| Barrier sealed | 503 | `UNAVAILABLE` | connection refused |
| Seal state unconfirmed (the node cannot read the cluster seal records; the barrier is not sealed — retry) | 503 `SEAL_STATE_UNCONFIRMED` | `UNAVAILABLE`, reason `SEAL_STATE_UNCONFIRMED` | MySQL **1105** `sql/mysql: seal state unconfirmed: …` / SQLSTATE **57P03** `sql/pgsql: seal state unconfirmed: …` |
| Built without `qrdb_sql` | 501 | `UNIMPLEMENTED` | listener not started |

Statement-level ceilings surface as their own errors before any proposal is
made: 65535 bytes for an encoded key, 4 MB for an encoded row, 8 MiB for one
statement's estimated compares plus ops.

## Table surfaces

Four kinds of table are reachable through both the ad-hoc endpoint and the
wire protocols, and they are not equivalent:

| Kind | Where its rows live | Primary key enforced by | Transactional |
|---|---|---|---|
| `kv` | the raw KV keyspace | the key itself | no — autocommit-only |
| entity (`pkg/dao`-registered) | the entity keyspace | the key itself | no — autocommit-only |
| collection ([../collections/](../collections/)) | `__col:` documents | `CollectionService.InsertDocument`'s **guarded write** (`PutIfAbsent`) | no — autocommit-only |
| relational (`CREATE TABLE`) | the table's own keyspace | a **compare in the same proposal** as the row | yes |

A collection table's first column is `_id`; naming it in an `INSERT` claims
that document ID and is refused with **1062** if it is taken, while omitting it
(or writing `0`) lets the collection allocate. `REPLACE INTO` and
`INSERT … ON DUPLICATE KEY UPDATE` are guarded writes: `REPLACE` is one
guarded upsert per row, and the update half of `ON DUPLICATE KEY UPDATE` is
written only against the revision of the row it was computed from — a lost race
is **1213** / `SQLSTATE 40001`, never a silent overwrite. The row it produces is the same document a REST, gRPC, QUIC or MCP insert
of the same `_id` would produce. See
[engineering.md](engineering.md#collection-table-schema).

**The `1062` in the status table above therefore means two different strengths**
depending on which kind of table raised it: on a relational table it is a
constraint that holds under concurrent writers, on a collection table it is a
check that does not.

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
