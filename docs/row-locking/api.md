# Row Locking — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-30

Row locking is reached two ways:

1. **The concurrency-mode capability** — reading and changing a database's
   `sql.concurrency`, present on all six transports.
2. **SQL itself** — lock clauses, session variables and error codes on the
   MySQL and PostgreSQL wire protocols, the in-process driver, and the ad-hoc
   query capability ([../sql/api.md](../sql/api.md)).

## Capability matrix — concurrency mode

gRPC service `qrdb.v1.QRDBService`; the Unix socket transport serves the same
gRPC wire over a UDS.

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Database()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Set at creation | `POST /v1/databases` (`sql` field) | `DatabaseCreate` (`sql`) | `db.Create` (`sql`) | `qrdb_database_create` (`sql_concurrency`) | `CreateDatabase` (`SQL`) | `databases:create` |
| Update | `PATCH /v1/databases/{name}/sql` | `DatabaseUpdateSQL` | `db.UpdateSQL` | `qrdb_database_update_sql` | `UpdateDatabaseSQL` | `databases:update` |
| Read | `GET /v1/databases/{name}` (`sql` field) | `DatabaseGet` | `db.Get` | `qrdb_database_get` | `GetDatabase` | `databases:read` |

### Request and response

```json
PATCH /v1/databases/shop/sql
{ "sql": { "concurrency": "pessimistic" } }
```

```json
{
  "name": "shop",
  "engine": "kv",
  "backend": "pebble",
  "status": "open",
  "sql": { "concurrency": "pessimistic" }
}
```

An absent `sql` object, or an empty `concurrency`, means `optimistic`.

### Status and error mapping

| Condition | REST | gRPC / Unix | QUIC | MCP |
|---|---|---|---|---|
| Updated | 200 | `OK` | `StatusOK` | result |
| Unknown concurrency mode | 400 | `INVALID_ARGUMENT` | `StatusInvalidArgument` | tool error |
| Unknown database | 404 | `NOT_FOUND` | `StatusNotFound` | tool error |
| Database owned by another tenant | 404 | `NOT_FOUND` | `StatusNotFound` | tool error |
| Missing `databases:update` | 403 | `PERMISSION_DENIED` | `StatusPermissionDenied` | tool error |

## SQL surface

### Lock clauses

| Clause | MySQL wire | PostgreSQL wire | Lock |
|---|---|---|---|
| `FOR UPDATE` | yes | yes | `X` |
| `FOR NO KEY UPDATE` | — | yes | `X` |
| `FOR SHARE` | yes | yes | `S` |
| `FOR KEY SHARE` | — | yes | `S` |
| `LOCK IN SHARE MODE` | yes | — | `S` |
| `FOR UPDATE OF t [, ...]` | yes | yes | `X` on the named tables only |
| `... NOWAIT` | yes | yes | zero-length wait |
| `... SKIP LOCKED` | refused | refused | — |

### Session variables

| Variable | Values | Scope |
|---|---|---|
| `qrdb_concurrency` | `database` (default), `optimistic`, `pessimistic` | session |
| `innodb_lock_wait_timeout` | 1–1073741824 seconds, default 50 | session and global |
| `lock_timeout` (PostgreSQL wire) | PostgreSQL duration; translated to `innodb_lock_wait_timeout` | session |

### Error codes

| Condition | MySQL | PostgreSQL | Ad-hoc query: REST / gRPC / QUIC |
|---|---|---|---|
| Lock wait timeout | 1205 | 55P03 | 409 / `ABORTED` / `StatusAborted` |
| `NOWAIT`, row locked | 3572 | 55P03 | 409 / `ABORTED` / `StatusAborted` |
| Deadlock victim | 1213 (40001) | 40P01 | 409 / `ABORTED` / `StatusAborted` |
| Lock request lost | 1213 (40001) | 40001 | 409 / `ABORTED` / `StatusAborted` |
| Statement in an aborted transaction | 1213 (40001) | 25P02 | 409 / `ABORTED` / `StatusAborted` |
| `SKIP LOCKED` | 1235 (42000) | 0A000 | 501 / `UNIMPLEMENTED` / `StatusUnimplemented` |

The in-process driver reports the MySQL numbers through
`*driver.SQLStateError` (`Number`), exactly as it reports 1213 today.

## Versioning

`PATCH /v1/databases/{name}/sql` is new in this release. The `sql` field on
the database object is additive: a client that does not know it ignores it, and
a server that does not know it treats the database as `optimistic`.
