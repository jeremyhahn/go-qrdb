# Databases — API

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> "Target" sections describe the multi-model database program in
> [architecture.md](architecture.md); they are approved by the owner on 2026-09-30 and not
> implemented. OD-n refers to that document's ruled decisions.

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
`databases:create_raft` *in addition to* `databases:create`, as does every
database of a raft-isolated tenant; every database of a volume-isolated tenant
checks `databases:create_volume`. Neither replaces the base grant. They apply to
every create path -- REST, gRPC, Unix, QUIC, MCP and SQL `CREATE DATABASE` --
and a refusal answers `403` / `PERMISSION_DENIED`.

## Database scope on data-plane requests

Every data-plane capability in every other feature carries a database scope:

| Transport | Mechanism |
|---|---|
| REST | `X-QRDB-Database` header |
| gRPC / Unix | `x-qrdb-database` metadata value |
| QUIC / MCP | equivalent request field |
| Embedded | `Client.DatabaseScoped(name)`, resolved through the node's service registry; refused with `transport.DatabaseScopeUnsupportedError` when the embedded transport has none |

Absent, the server's default database (`system`) is used.

## Schemas

`POST /v1/databases` — `CreateDatabaseRequest`:

Shipped:

```json
{ "name": "billing", "engine": "kv", "backend": "raft",
  "dataDir": "<install-dir>/data/billing",
  "mode": "distributed", "replicas": 3,
  "protocol": { "mysql": false, "postgresql": true,
                "etcd": { "enabled": false, "listenAddr": ":2379" } },
  "barrier": { "enabled": true, "mode": "shamir",
               "sharesRequired": 3, "sharesTotal": 5 },
  "connection": { "transport": "tcp", "address": "db2.example.com:63003" } }
```

Response — `DatabaseInfo`, which adds `tenantId`, `status` and `shardId`.

`engine` is accepted as `kv`, `timeseries` or `vector`; `backend` as `memory`,
`file`, `pebble`, `raft` or `objstore`. Measured: the backend does not decide
where most databases' data lives, and `objstore` is refused when the store is
opened — see [architecture.md](architecture.md) "Current state".

### Target: create and info

`engine` is removed from the request and the response. `backend` is optional;
when absent the node's `database_defaults.backend` is written into the record
(OD-3). `raft.stateStorage` selects a raft database's state-machine store
(OD-8).

```json
{ "name": "billing", "backend": "raft", "replicas": 3,
  "raft": { "clusterMode": "distributed", "stateStorage": "pebble" },
  "protocol": { "mysql": false, "postgresql": true },
  "barrier": { "enabled": true, "mode": "shamir",
               "sharesRequired": 3, "sharesTotal": 5 } }
```

`DatabaseInfo` gains a read-only `binding` block reporting what the resolver
decided:

```json
{ "name": "billing", "tenantId": "acme", "backend": "raft", "status": "open",
  "shardId": 4107, "replicas": 3,
  "binding": { "kind": "raft", "nodes": ["node-1", "node-2", "node-3"],
               "encryption": "tenant+database", "generation": 1 } }
```

`kind` is `local` or `raft`; `nodes` is the owning node of a local binding or
the replica nodes of a raft one; `encryption` is `node`, `tenant` or
`tenant+database`; `generation` increments each time the name is created, so a
dropped-and-recreated database is distinguishable from its predecessor.

On gRPC the field numbered 2 in `DatabaseCreateRequest` and
`DatabaseInfoResponse` becomes `reserved 2; reserved "engine";`, and
`DatabaseInfoResponse` gains a `DatabaseBindingInfo binding` message field. A
request carrying `engine` on REST, QUIC or MCP is refused as an unknown field
(OD-13, ruled A). The `connection` block and the update-connection capability are
removed on every transport (OD-9, ruled A).

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
| etcd enabled on a non-`kv` database | 400 | `INVALID_ARGUMENT` — `database.EtcdEngineUnsupportedError` *(corrected: documented as non-`kv` / non-`raft`, `FAILED_PRECONDITION`)* |
| etcd enabled on a non-`raft` database (target — no check exists today) | 400 | `FAILED_PRECONDITION` — `database.EtcdRequiresRaftBackendError` |
| Wrong unseal secret | 403 | `PERMISSION_DENIED` — the database stays sealed |
| Operating on a sealed database | 503 | `UNAVAILABLE` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Target: `backend: raft` on a standalone node | 400 | `FAILED_PRECONDITION` — `database.RaftBackendUnavailableError` |
| Target: raft options on a non-raft backend | 400 | `INVALID_ARGUMENT` — `database.RaftOptionsRequireRaftBackendError` |
| Target: barrier on `backend: memory` | 400 | `FAILED_PRECONDITION` — `database.BarrierRequiresPersistentBackendError` |
| Target: `data_dir` outside a volume/raft tenant's root | 400 | `INVALID_ARGUMENT` — `database.DataDirOutsideTenantRootError` |
| Target: backend conflicts with the tenant's isolation (OD-7, ruled B) | 400 | `FAILED_PRECONDITION` — `database.BackendIsolationConflictError` |
| Target: a database's binding cannot be opened | 503 | `UNAVAILABLE` — `database.BindingUnavailableError` |
| Target: a local-binding database's owning node is unreachable (OD-2, ruled A) | 503 | `UNAVAILABLE` — `database.BindingOwnerUnavailableError` |
| Target: the cluster feature gate for a command is not yet open on every member | 503 | `FAILED_PRECONDITION` — typed feature-gate refusal |
| Target: create carrying `engine` (OD-13, ruled A) | 400 | `INVALID_ARGUMENT` |

## Pagination

`ListDatabases` returns every database in the tenant and is not paginated; the
count is bounded by operator action.

## Versioning

Database REST paths are under `/v1/`. The target design's removals (`engine`,
and `connection` under OD-9 A) are breaking and ship together in one MINOR
release; there is no deprecation period in the pre-1.0 series.
