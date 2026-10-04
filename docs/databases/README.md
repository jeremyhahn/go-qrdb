<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Multi-Database Architecture

> **Companion reference, partly superseded.** This document predates the
> feature folder. Its description of an engine type per database, and of each
> database's backend deciding where its data lives, does not match the shipped
> code; [architecture.md](architecture.md) "Current state" records what the code
> does, and its target design removes the engine type entirely. Where the two
> disagree, architecture.md is authoritative.

## Overview

A QRDB **database** is a named, isolated data store. Each database has an
**engine type** that determines its data model and a **backend type** that
determines how data is persisted. Every database receives its own directory
under `{dataDir}/databases/{name}/`, providing physical isolation at the
filesystem level.

```
{dataDir}/
  databases/
    default/
      meta.json       # {"name":"default","engine":"kv","backend":"pebble"}
      data/            # storage files (PebbleDB SSTables, WAL, etc.)
    metrics/
      meta.json
      data/
    embeddings/
      meta.json
      data/
```

Database names must match `^[a-zA-Z0-9_-]+$` (alphanumeric characters,
hyphens, and underscores only).

A request selects its database with the `X-QRDB-Database` header (REST), the
`x-qrdb-database` gRPC/Unix metadata key, the QUIC envelope `database` field, or
the MCP `database` tool argument; the Go/TypeScript SDKs expose
`client.DatabaseScoped(name)` / `client.databaseScoped(name)`. When no scope is
supplied, the request resolves to the implicit bootstrap database `system`
(`tenant.DefaultDatabase`). The **tenant**, by contrast, is never client-supplied
— it comes from the authenticated identity. See [scoping.md](../scoping.md) for
the request-time scoping model and [multi-tenancy.md](../multi-tenancy/architecture.md) for the
per-tenant isolation ladder, quotas, and entitlements.

---

## Engine Types

Each database is configured with one of the following engine types. The
engine determines which service-layer operations are available.

| Engine       | Value        | Description                                                        |
|--------------|--------------|--------------------------------------------------------------------|
| Key-Value    | `kv`         | General-purpose key-value store with MVCC, transactions, and leases |
| Time-Series  | `timeseries` | Time-series optimized storage with composite keys, aggregation, and retention policies |
| Vector       | `vector`     | Vector-search engine for similarity queries over high-dimensional embeddings |

---

## Backend Types

The backend controls how data is physically stored. All backends expose the
same `Backend` interface, so the service layer works identically regardless
of the underlying storage mechanism.

| Backend  | Value    | Persistent | Distributed | Description                                    |
|----------|----------|:----------:|:-----------:|------------------------------------------------|
| Memory   | `memory` | No         | No          | In-process storage; data lost on restart       |
| File     | `file`   | Yes        | No          | Flat-file storage on the local filesystem      |
| PebbleDB | `pebble` | Yes        | No          | PebbleDB LSM-tree; production-grade local storage |
| Raft     | `raft`   | Yes        | Yes         | Raft consensus for replicated, distributed storage |

The `Backend` interface that all backends implement:

```go
type Backend interface {
    Get(ctx context.Context, key []byte) ([]byte, error)
    Put(ctx context.Context, key, value []byte) error
    Delete(ctx context.Context, key []byte) error
    List(ctx context.Context, prefix string) ([]string, error)
    Range(ctx context.Context, start, end []byte, limit int) ([]KeyValue, error)
    Batch(ctx context.Context, ops []BatchOp) error
    Close() error
}
```

Standalone backends (`memory`, `file`, `pebble`) delegate directly to the
`storage.Storage` interface via `StandaloneBackend`. The `raft` backend
routes operations through the Raft consensus layer, making reads and writes
linearizable across the cluster.

---

## Configuration

Databases are declared in the `databases:` section of the YAML configuration
file.

### Single database

```yaml
databases:
  - name: default
    engine: kv
    backend: pebble
```

### Multiple databases

```yaml
databases:
  - name: default
    engine: kv
    backend: pebble
  - name: metrics
    engine: timeseries
    backend: pebble
  - name: embeddings
    engine: vector
    backend: pebble
```

### Mixed backends

```yaml
databases:
  - name: production
    engine: kv
    backend: raft
  - name: cache
    engine: kv
    backend: memory
  - name: logs
    engine: timeseries
    backend: file
```

### Per-database protocol configuration

Each database can expose additional wire protocol endpoints. Supported
protocols are MySQL, PostgreSQL, and etcd. `mysql` and `postgresql` are
plain booleans directly on the database entry; `etcd` is a block with its
own `enabled`/`listen_addr`/`advertise_client_urls` fields, also directly
on the entry. None of the three is nested under a `protocol:` key:
`config.DatabaseConfig` has no such field, and a nested block is silently
dropped by the YAML decoder rather than applied. There is also no
per-database `port` — the MySQL and PostgreSQL ports are server-wide
(`server.mysql_port` / `server.pgsql_port`, or `--mysql-port` /
`--pgsql-port`), not configured per database. (Corrected 2026-10-03; an
earlier version of this example showed a nested `protocol:` shape with a
per-database `port` that never matched what the loader reads.)

```yaml
databases:
  - name: production
    engine: kv
    backend: raft
    etcd:
      enabled: true
      listen_addr: ":2379"
      advertise_client_urls:
        - "https://node1:2379"
  - name: analytics
    engine: kv
    backend: raft
    mysql: true
    postgresql: true
```

### Tenant-aware databases

Databases can be associated with a tenant for multi-tenant BaaS deployments.
The `tenant_id` field is set by the BaaS layer when creating databases on
behalf of tenants.

```yaml
databases:
  - name: acme_system
    engine: kv
    backend: raft
    tenant_id: acme
  - name: acme_analytics
    engine: kv
    backend: raft
    tenant_id: acme
```

### Barrier encryption

Databases can be encrypted at rest with barrier encryption. The barrier
configuration controls seal/unseal behavior for the database.

```yaml
databases:
  - name: production
    engine: kv
    backend: raft
    barrier:
      enabled: true
      key_id: "dek:tenant:acme:db:production"
```

### Backward compatibility

When no `databases:` section exists in the configuration, QRDB operates in
legacy single-database mode. The existing `storage.backend` and
`raft.data_dir` settings control the single implicit database. This ensures
that upgrading QRDB does not break existing deployments.

---

## Database Management API

QRDB exposes database management operations across every transport protocol
(REST, gRPC, QUIC, MCP, and Unix). The lifecycle/config operations plus three
encryption-barrier operations are available on each.

### Operations

| Operation | Permission | Description |
|-----------|-----------|-------------|
| CreateDatabase | `databases:create` | Create a new database with engine, backend, and optional protocol/barrier config |
| GetDatabase | `databases:read` | Retrieve metadata for a specific database |
| ListDatabases | `databases:list` | List databases (scoped to the caller's tenant; see below) |
| DropDatabase | `databases:delete` | Close and permanently delete a database and its data |
| UpdateDatabaseProtocol | `databases:update` | Update MySQL/PostgreSQL protocol settings |
| UpdateDatabaseEtcd | `databases:update` | Enable/disable etcd protocol and update listen/advertise config |
| UpdateDatabaseRaft | `databases:update` | Update Raft consensus configuration |
| UpdateDatabaseBarrier | `databases:update` | Update barrier encryption settings |
| UpdateDatabaseConnection | `databases:update` | Update connection pool and timeout settings |
| SealDatabase | `databases:seal` | Seal the database's at-rest encryption barrier |
| UnsealDatabase | `databases:unseal` | Unseal the database's encryption barrier with a secret |
| DatabaseBarrierStatus | `databases:barrier_status` | Report sealed state and active strategy |

REST seal/unseal/status map to `POST /v1/databases/{name}/seal`,
`POST /v1/databases/{name}/unseal`, and `GET /v1/databases/{name}/barrier/status`.
gRPC RPCs are `DatabaseSeal`/`DatabaseUnseal`/`DatabaseBarrierStatus`; QUIC
methods are `db.Seal`/`db.Unseal`/`db.BarrierStatus`; MCP tools are
`qrdb_database_seal`/`qrdb_database_unseal`/`qrdb_database_barrier_status`.

### Automatic tenant scoping

Database operations are **tenant-scope-aware**: the transport handlers pass the
authenticated caller's context (the auth middleware stamps the caller's tenant
on every request) to the service, which routes each call based on that tenant:

- **System/admin caller** (the reserved `system` tenant) → the cross-tenant
  admin path, with no ownership restriction.
- **Tenant principal** → the ownership-enforced path: create injects the
  caller's tenant, and get/list/drop/update verify the database belongs to that
  tenant (cross-tenant access is denied). `ListDatabases` returns only the
  caller's own databases.

This routing is centralized in `DatabaseService` (one place), so there is a
single database API surface — there are no separate "ForTenant" wire endpoints.
The Go SDK exposes the same operations via `client.Database()`, and a
`transport.WithTenantDatabase` decorator can pin client-side calls to a tenant.

---

## CLI Commands

The `qrdb database` command group manages databases from the command line.

### Create a database

```bash
qrdb database create <name> --engine kv --backend pebble
```

Create a dedicated-Raft database with a replication factor. `--replicas` applies
to the `raft` backend only and defaults to `1`; a higher value is validated
against the tenant's `MaxReplicasPerGroup` / `MaxTotalReplicas` quota and clamped
to the live cluster size (multi-node replication of tenant raft groups is in
development — see [multi-tenancy.md](../multi-tenancy/architecture.md)):

```bash
qrdb database create payments --engine kv --backend raft --replicas 3
```

Create with etcd protocol enabled:

```bash
qrdb database create --name k8s --engine kv --backend raft \
  --etcd --etcd-listen-addr :2379 --etcd-advertise-urls https://node1:2379
```

Create with barrier encryption:

```bash
qrdb database create --name secure --engine kv --backend raft \
  --barrier --barrier-key-id "dek:tenant:acme:db:secure"
```

### Get database info

```bash
qrdb database info <name>
```

Example output:

```json
{
  "name": "metrics",
  "engine": "timeseries",
  "backend": "pebble",
  "status": "open",
  "tenant_id": "",
  "data_dir": "/var/lib/qrdb/databases/metrics/data"
}
```

### List all databases

```bash
qrdb database list
```

Example output:

```
NAME            ENGINE      BACKEND    TENANT    STATUS
default         kv          pebble               open
acme_system     kv          raft       acme      open
acme_analytics  kv          raft       acme      open
```

### Drop a database

```bash
qrdb database drop <name>
```

Dropping a database closes it and removes its entire directory tree
(`{dataDir}/databases/{name}/`), including all data files and metadata.

### Update etcd protocol

Enable or disable the etcd protocol on an existing database:

```bash
qrdb database update-etcd <name> --enabled --listen-addr :2379 --advertise-urls https://node1:2379
qrdb database update-etcd <name> --disabled
```

| Flag | Default | Description |
|------|---------|-------------|
| `--enabled` | | Enable etcd protocol |
| `--disabled` | | Disable etcd protocol |
| `--listen-addr` | `:2379` | etcd gRPC listen address |
| `--advertise-urls` | | Advertise URLs for etcd client discovery |

### Update protocol settings

Update MySQL or PostgreSQL protocol configuration:

```bash
qrdb database update-protocol <name> --mysql --mysql-port 3306
qrdb database update-protocol <name> --postgresql --postgresql-port 5432
```

### Update Raft configuration

Update Raft consensus settings for a database:

```bash
qrdb database update-raft <name> --election-rtt 10 --heartbeat-rtt 1
```

### Update barrier encryption

Update barrier encryption configuration:

```bash
qrdb database update-barrier <name> --enabled --key-id "dek:tenant:acme:db:mydb"
qrdb database update-barrier <name> --disabled
```

### Update connection settings

Update connection pool and timeout configuration:

```bash
qrdb database update-connection <name> --max-connections 100 --idle-timeout 30s
```

### Seal / unseal the encryption barrier

```bash
qrdb database seal <name>
qrdb database unseal <name> --secret "<unseal-secret>"
qrdb database barrier-status <name>
```

---

## API Endpoints

### REST

| Method   | Path                                      | Description                        |
|----------|-------------------------------------------|------------------------------------|
| `POST`   | `/v1/databases`                           | Create a new database              |
| `GET`    | `/v1/databases`                           | List all databases                 |
| `GET`    | `/v1/databases/{name}`                    | Get info for a specific database   |
| `DELETE` | `/v1/databases/{name}`                    | Drop a database                    |
| `PATCH`  | `/v1/databases/{name}/protocol`           | Update protocol configuration      |
| `PATCH`  | `/v1/databases/{name}/etcd`               | Update etcd protocol configuration |
| `PATCH`  | `/v1/databases/{name}/raft`               | Update Raft configuration          |
| `PATCH`  | `/v1/databases/{name}/barrier`            | Update barrier encryption settings |
| `PATCH`  | `/v1/databases/{name}/connection`         | Update connection settings         |
| `POST`   | `/v1/databases/{name}/seal`               | Seal the encryption barrier        |
| `POST`   | `/v1/databases/{name}/unseal`             | Unseal the encryption barrier      |
| `GET`    | `/v1/databases/{name}/barrier/status`     | Encryption-barrier status          |

### gRPC RPCs

| RPC | Description |
|-----|-------------|
| `DatabaseCreate` | Create a new database |
| `DatabaseGet` | Get database metadata |
| `DatabaseList` | List all databases |
| `DatabaseDrop` | Drop a database |
| `DatabaseUpdateProtocol` | Update protocol settings |
| `DatabaseUpdateEtcd` | Update etcd protocol |
| `DatabaseUpdateRaft` | Update Raft configuration |
| `DatabaseUpdateBarrier` | Update barrier encryption |
| `DatabaseUpdateConnection` | Update connection settings |
| `DatabaseSeal` | Seal the encryption barrier |
| `DatabaseUnseal` | Unseal the encryption barrier |
| `DatabaseBarrierStatus` | Encryption-barrier status |

### QUIC Methods

| Method | Description |
|--------|-------------|
| `db.Create` | Create a new database |
| `db.Get` | Get database metadata |
| `db.List` | List all databases |
| `db.Drop` | Drop a database |
| `db.UpdateProtocol` | Update protocol settings |
| `db.UpdateEtcd` | Update etcd protocol |
| `db.UpdateRaft` | Update Raft configuration |
| `db.UpdateBarrier` | Update barrier encryption |
| `db.UpdateConnection` | Update connection settings |
| `db.Seal` | Seal the encryption barrier |
| `db.Unseal` | Unseal the encryption barrier |
| `db.BarrierStatus` | Encryption-barrier status |

### MCP Tools

| Tool                                    | Description                          |
|-----------------------------------------|--------------------------------------|
| `qrdb_database_create`                  | Create a new database                |
| `qrdb_database_list`                    | List all databases                   |
| `qrdb_database_get`                     | Get database info                    |
| `qrdb_database_drop`                    | Drop a database                      |
| `qrdb_database_update_protocol`         | Update protocol configuration        |
| `qrdb_database_update_etcd`             | Update etcd protocol configuration   |
| `qrdb_database_update_raft`             | Update Raft configuration            |
| `qrdb_database_update_barrier`          | Update barrier encryption settings   |
| `qrdb_database_update_connection`       | Update connection settings           |
| `qrdb_database_seal`                    | Seal the encryption barrier          |
| `qrdb_database_unseal`                  | Unseal the encryption barrier        |
| `qrdb_database_barrier_status`          | Encryption-barrier status            |

### SQL

```sql
CREATE DATABASE metrics ENGINE timeseries BACKEND pebble;
CREATE DATABASE k8s ENGINE kv BACKEND raft PROTOCOL ETCD LISTEN ':2379';
SHOW DATABASES;
DROP DATABASE metrics;
USE metrics;
```

`USE` sets the active database context for subsequent queries within the
session.

---

## SDK

### Go SDK - DatabaseClient

The Go SDK provides a `DatabaseClient` with methods for all 9 operations:

```go
client := sdk.NewDatabaseClient(conn)

// Create
err := client.CreateDatabase(ctx, &CreateDatabaseRequest{
    Name:    "analytics",
    Engine:  "kv",
    Backend: "raft",
})

// List
databases, err := client.ListDatabases(ctx)

// Get
info, err := client.GetDatabase(ctx, "analytics")

// Drop
err := client.DropDatabase(ctx, "analytics")

// Update operations
err := client.UpdateDatabaseProtocol(ctx, "analytics", &ProtocolConfig{...})
err := client.UpdateDatabaseEtcd(ctx, "analytics", &EtcdConfig{...})
err := client.UpdateDatabaseRaft(ctx, "analytics", &RaftConfig{...})
err := client.UpdateDatabaseBarrier(ctx, "analytics", &BarrierConfig{...})
err := client.UpdateDatabaseConnection(ctx, "analytics", &ConnectionConfig{...})
```

### TenantDatabaseClient

The `TenantDatabaseClient` wraps `DatabaseClient` and automatically scopes
all operations to a specific tenant:

```go
tenantClient := sdk.NewTenantDatabaseClient(conn, "acme")

// Operations are scoped to tenant "acme"
err := tenantClient.CreateDatabase(ctx, &CreateDatabaseRequest{
    Name:    "analytics",   // stored as "acme_analytics"
    Engine:  "kv",
    Backend: "raft",
})
```

---

## Barrier Encryption Lifecycle

Databases with barrier encryption enabled follow a seal/unseal lifecycle:

1. **Create** -- Database is created with barrier config. A DEK is
   provisioned (via the BaaS layer's `EnsureDatabaseDEK`). The database
   starts in **sealed** state.

2. **Unseal** -- The barrier is unsealed by providing the master key or
   through the automatic unseal process. The DEK is unwrapped and the
   database becomes readable/writable.

3. **Seal** -- The barrier can be explicitly sealed, wiping the unwrapped
   DEK from memory. The database becomes inaccessible until unsealed again.

4. **Drop** -- When a database is dropped, its DEK is deleted
   (via `DeleteDatabaseDEK`) and all encrypted data is removed.

### Per-tenant barrier key management

For multi-tenant deployments, a tenant that uses a **dedicated** isolation mode
(`barrier` or `volume`) chooses how its encryption barrier's key is managed:

- **Self-managed** (default) -- the tenant admin initializes and unseals the
  barrier; it comes up sealed after every restart, and neither the operator nor
  the system holds the tenant key.
- **System-managed auto-unseal** (opt-in, entitlement-gated) -- the tenant root
  key is wrapped under the system barrier so the barrier auto-unseals at boot
  with no credential.

The mode is selected at tenant creation (`barrier_auto_unseal`) and the opt-in
mode is gated by the `allow_system_managed_barrier` entitlement. See
[seal/multi-tenant.md → Tenant barrier key management](../seal/multi-tenant.md#tenant-barrier-key-management)
for the full trade-offs, lifecycle, and CLI/SDK/REST examples.

---

## Migration

QRDB performs an automatic one-time migration from the legacy flat directory
layout to the per-database layout. Migration is triggered at startup when
**both** conditions are true:

1. `{dataDir}/pebble/` exists (old layout)
2. `{dataDir}/databases/` does not exist (new layout absent)

### Migration steps

1. Create `{dataDir}/databases/`.
2. Move `{dataDir}/pebble/` to `{dataDir}/databases/default/data/`.
3. Write `meta.json` for the `default` database with engine `kv` and
   backend `pebble`.
4. If `{dataDir}/vectors/` exists, move it to
   `{dataDir}/databases/vectors/data/` and write `meta.json` with engine
   `vector` and backend `pebble`.

### Before migration

```
{dataDir}/
  pebble/           # flat PebbleDB storage
  vectors/           # optional vector data
```

### After migration

```
{dataDir}/
  databases/
    default/
      meta.json      # {"name":"default","engine":"kv","backend":"pebble"}
      data/           # moved from {dataDir}/pebble/
    vectors/
      meta.json      # {"name":"vectors","engine":"vector","backend":"pebble"}
      data/           # moved from {dataDir}/vectors/
```

The migration is idempotent. If `{dataDir}/databases/` already exists,
no migration occurs. If the migration fails at any step, a
`*MigrationError` is returned with the step name and underlying cause.

---

## Architecture

### Backend abstraction

The `Backend` interface is the central abstraction that decouples the
service layer from storage details. Services call `Backend.Get`,
`Backend.Put`, and other methods without knowing whether the operation
executes against local PebbleDB or goes through Raft consensus.

```
                     +------------------+
                     |   Service Layer  |
                     +--------+---------+
                              |
                        Backend interface
                              |
              +---------------+---------------+
              |                               |
    +---------+---------+          +----------+----------+
    | StandaloneBackend |          |    Raft Backend     |
    | (memory/file/     |          | (proposals + reads  |
    |  pebble)          |          |  through consensus) |
    +---------+---------+          +----------+----------+
              |                               |
       storage.Storage                  Raft Host
```

`StandaloneBackend` wraps a `storage.Storage` implementation and delegates
operations directly. The Raft backend routes writes through
`host.SyncPropose` and reads through `host.SyncRead`, making every
operation linearizable.

### Manager lifecycle

The `Manager` manages the lifecycle of all databases:

- **Create** -- validates config, creates the directory structure, writes
  `meta.json`, opens the storage backend, optionally provisions barrier
  encryption, and registers the database in a lock-free `sync.Map`.
- **Open** -- reads `meta.json` from an existing directory, opens the
  storage backend, and registers the database.
- **OpenAll** -- scans `{dataDir}/databases/` and opens every database that
  has a valid `meta.json`. Called at server startup.
- **Get** -- returns an already-open database by name.
- **Drop** -- closes the database, removes it from the registry, deletes
  barrier encryption keys if present, and deletes the entire directory tree.
- **Close** -- shuts down all open databases during server shutdown.

Concurrent access is safe. The `sync.Map` provides lock-free reads, and
`LoadOrStore` prevents duplicate creation races. Database status is tracked
with `atomic.Value` (values: `open`, `closed`, `error`).

---

## Cross-References

- [Architecture Overview](../01-architecture.md)
- [Configuration Reference](../12-configuration.md)
- [API Reference](../api-server/api.md)
- [Tenant & Database Scoping](../scoping.md)
- [Multi-Tenancy: Isolation, Quotas & Entitlements](../multi-tenancy/architecture.md)
