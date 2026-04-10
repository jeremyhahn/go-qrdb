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

Each database can expose additional wire protocol endpoints via the
`protocol` field. Supported protocols are MySQL, PostgreSQL, and etcd.
Protocol configuration replaced the former top-level `SQLConfig`.

```yaml
databases:
  - name: production
    engine: kv
    backend: raft
    protocol:
      etcd:
        enabled: true
        listen_addr: ":2379"
        advertise_urls:
          - "https://node1:2379"
  - name: analytics
    engine: kv
    backend: raft
    protocol:
      mysql:
        enabled: true
        port: 3306
      postgresql:
        enabled: true
        port: 5432
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

QRDB provides 9 database management operations available across all transport
protocols.

### Operations

| Operation | Description |
|-----------|-------------|
| CreateDatabase | Create a new database with engine, backend, and optional protocol/barrier config |
| GetDatabase | Retrieve metadata for a specific database |
| ListDatabases | List all databases (optionally filtered by tenant) |
| DropDatabase | Close and permanently delete a database and its data |
| UpdateDatabaseProtocol | Update MySQL/PostgreSQL protocol settings |
| UpdateDatabaseEtcd | Enable/disable etcd protocol and update listen/advertise config |
| UpdateDatabaseRaft | Update Raft consensus configuration |
| UpdateDatabaseBarrier | Update barrier encryption settings |
| UpdateDatabaseConnection | Update connection pool and timeout settings |

### Tenant-Aware Operations

The Manager provides tenant-aware variants of all operations:

| Method | Description |
|--------|-------------|
| `CreateForTenant` | Create a database scoped to a tenant |
| `GetForTenant` | Get database info within a tenant's scope |
| `ListForTenant` | List only databases belonging to a tenant |
| `DropForTenant` | Drop a tenant's database |

Tenant-aware operations verify ownership before executing and use the
naming convention `{tenantID}_{dbName}` for database names.

---

## CLI Commands

The `qrdb database` command group manages databases from the command line.

### Create a database

```bash
qrdb database create <name> --engine kv --backend pebble
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
- [API Reference](../api.md)
