# Databases — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Database()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /v1/databases` | `DatabaseCreate` | `db.Create` | `qrdb_database_create` | `CreateDatabase` | `databases:create` (+ `create_raft` / `create_volume`) |
| Get | `GET /v1/databases/{name}` | `DatabaseGet` | `db.Get` | `qrdb_database_get` | `GetDatabase` | `databases:read` |
| List | `GET /v1/databases/list` | `DatabaseList` | `db.List` | `qrdb_database_list` | `ListDatabases` | `databases:list` |
| Drop | `DELETE /v1/databases/{name}` | `DatabaseDrop` | `db.Drop` | `qrdb_database_drop` | `DropDatabase` | `databases:delete` |
| Update protocol | `PATCH /v1/databases/{name}/protocol` | `DatabaseUpdateProtocol` | `db.UpdateProtocol` | `qrdb_database_update_protocol` | `UpdateDatabaseProtocol` | `databases:update` |
| Update etcd | `PATCH /v1/databases/{name}/etcd` | `DatabaseUpdateEtcd` | `db.UpdateEtcd` | `qrdb_database_update_etcd` | `UpdateDatabaseEtcd` | `databases:update` |
| Update raft | `PATCH /v1/databases/{name}/raft` | `DatabaseUpdateRaft` | `db.UpdateRaft` | `qrdb_database_update_raft` | `UpdateDatabaseRaft` | `databases:update` |
| Update barrier | `PATCH /v1/databases/{name}/barrier` | `DatabaseUpdateBarrier` | `db.UpdateBarrier` | `qrdb_database_update_barrier` | `UpdateDatabaseBarrier` | `databases:update` |
| Update connection | `PATCH /v1/databases/{name}/connection` | `DatabaseUpdateConnection` | `db.UpdateConnection` | `qrdb_database_update_connection` | `UpdateDatabaseConnection` | `databases:update` |
| Scale | `PATCH /v1/databases/{name}/scale` | `DatabaseScale` | `db.Scale` | `qrdb_database_scale` | `ScaleDatabase` | `databases:scale` |
| Seal | `POST /v1/databases/{name}/seal` | `DatabaseSeal` | `db.Seal` | `qrdb_database_seal` | `SealDatabase` | `databases:seal` |
| Unseal | `POST /v1/databases/{name}/unseal` | `DatabaseUnseal` | `db.Unseal` | `qrdb_database_unseal` | `UnsealDatabase` | `databases:unseal` |
| Barrier status | `GET /v1/databases/{name}/barrier/status` | `DatabaseBarrierStatus` | `db.BarrierStatus` | `qrdb_database_barrier_status` | `DatabaseBarrierStatus` | `databases:barrier_status` |

Thirteen capabilities, all present on all six transports.

**Note the additive permissions.** Creating a raft-backed database checks
`databases:create_raft` *in addition to* `databases:create`; a volume-isolated
database checks `databases:create_volume`. Neither replaces the base grant.

## Database scope on data-plane requests

Every data-plane capability in every other feature carries a database scope:

| Transport | Mechanism |
|---|---|
| REST | `X-QRDB-Database` header |
| gRPC / Unix | `x-qrdb-database` metadata value |
| QUIC / MCP | equivalent request field |
| Embedded | `Client.DatabaseScoped(name)` |

Absent, the server's default database (`system`) is used.

## Schemas

`POST /v1/databases` — `CreateDatabaseRequest`:

```json
{ "name": "billing", "engine": "kv", "backend": "raft",
  "dataDir": "/var/lib/qrdb/billing",
  "mode": "distributed", "replicas": 3,
  "protocol": { "mysql": false, "postgresql": true,
                "etcd": { "enabled": false, "listenAddr": ":2379" } },
  "barrier": { "enabled": true, "mode": "shamir",
               "sharesRequired": 3, "sharesTotal": 5 },
  "connection": { "transport": "tcp", "address": "db2.example.com:63003" } }
```

Response — `DatabaseInfo`, which adds `tenantId`, `status` and `shardId`.

`PATCH /v1/databases/{name}/scale` — `{"target_replicas": 5}`. The database name
travels in the **path**, not the body (`json:"-"` on the request struct).

`POST /v1/databases/{name}/unseal` — carries the unseal `secret`.

`DatabaseRaftConfig` fields: `clusterMode` (`none|local|distributed`),
`shardId`, `replicaId`, `raftAddress`, `peers`, `localNodeCount`.

`DatabaseBarrierConfig` fields: `enabled`, `mode` (`passphrase|shamir`),
`sharesRequired`, `sharesTotal`, `secret`.

`DatabaseConnectionConfig` fields: `transport` (`tcp|quic`), `address`,
`caFile`, `certFile`, `keyFile`.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Database already exists | 409 | `ALREADY_EXISTS` — `database.AlreadyExistsError` |
| Name reserved for node state (`wal`, `nodehost`) or otherwise invalid | 400 | `INVALID_ARGUMENT` — `database.ReservedNameError` / `database.InvalidNameError` |
| Unknown database, or another tenant's database | 404 | `NOT_FOUND` — never implicitly created; another tenant's database is indistinguishable from a missing one |
| Barrier enabled on a shared store | 409 | `FAILED_PRECONDITION` — `database.SharedStoreBarrierUnsupportedError` *(corrected: documented as 400; every failed precondition is 409 on REST)* |
| Barrier enabled off the data path (raft backend, volume/raft-isolated tenant) | 409 | `FAILED_PRECONDITION` — `database.BarrierOffDataPathError` |
| `cluster_mode` / `replicas` on a non-raft backend | 400 | `INVALID_ARGUMENT` |
| `target_replicas < 1` | 400 | `INVALID_ARGUMENT` |
| etcd enabled on a non-`kv` / non-`raft` database | 400 | `FAILED_PRECONDITION` |
| Wrong unseal secret | 403 | `PERMISSION_DENIED` — the database stays sealed |
| Operating on a sealed database | 503 | `UNAVAILABLE` |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Pagination

`ListDatabases` returns every database in the tenant and is not paginated; the
count is bounded by operator action.

## Versioning

Database REST paths are under `/v1/`. Nothing here is deprecated.
