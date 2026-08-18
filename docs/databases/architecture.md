# Databases — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

One store serves several workloads with genuinely different needs: one wants
Raft replication and its own encryption key, another wants a cheap local Pebble
store, a third needs to look like etcd on port 2379. Forcing them into one
configuration means the strictest requirement is paid for by everyone.

## Shape chosen

A **database** is a first-class, independently configured container inside a
tenant. Each carries its own engine, storage backend, wire protocols, Raft
topology, encryption barrier and — optionally — its own data directory.

```
tenant
  └── database  (engine, backend, protocol, raft, barrier, connection)
        ├── kv / entity / collection / relational data
        ├── optional MySQL + PostgreSQL wire protocols
        └── optional etcd v3 listener  (its own etcd cluster)
```

Data-plane requests select a database with `--database` / `X-QRDB-Database`;
absent, the server's default (`system`) is used.

### Axes

| Axis | Values | Notes |
|---|---|---|
| `engine` | `kv`, `timeseries` | what the database stores |
| `backend` | `pebble` (default), `memory`, `file`, `raft` | where and how |
| `mode` (raft only) | `local`, `distributed` | Raft cluster topology |
| `replicas` (raft only) | ≥ 1 | replication factor, changeable with `scale` |
| `protocol` | MySQL, PostgreSQL, etcd | wire compatibility, per database |
| `barrier` | enabled, strategy, shares | per-database encryption, needs a dedicated store |
| `connection` | transport, address, TLS files | for a database backed by a remote node |

### Alternatives rejected

- **One global configuration.** Rejected on the problem above.
- **Per-database barrier on a shared store.** **Refused by the code**, not
  merely discouraged: a barrier is only meaningful when the database has a store
  of its own (an explicit `--data-dir`, a dedicated-raft database, or an
  IsolationVolume/IsolationRaft tenant). Enabling one on a database sharing the
  default store would encrypt nothing that another database could not read.
- **Recreating a database to change its replication factor.** Rejected:
  `database scale` adds and removes voting replicas in place.
- **A global etcd facade.** Rejected — see
  [../etcd-compatibility/](../etcd-compatibility/architecture.md). etcd is
  per database precisely because its revision counter is cluster-global.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Database manager, lifecycle, catalog | `pkg/database` | go-qrdb |
| Per-(tenant, database) service resolution | `pkg/service` (`service.Registry`) | go-qrdb |
| Engines and storage backends | `pkg/engine`, `pkg/storage` — see [../storage-engines/](../storage-engines/) | go-qrdb |
| Per-database barrier | `pkg/seal` — see [../barrier-seal/](../barrier-seal/) | go-qrdb |
| Raft groups and membership | — | **go-quicraft**, driven by `pkg/cluster` |
| Wire protocols | `pkg/sql`, `pkg/api/etcd` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Database()`, `Client.DatabaseScoped()`) | go-qrdb |

## Data flow

Every data-plane request resolves `(tenant, database)` server side and looks the
pair up in `service.Registry`, which returns the servicer bound to that
database's engine, backend, shard and barrier. This is the single point where a
database becomes a concrete set of services, and it is why a database's
configuration cannot leak into another's request path.

## Trust boundaries

- **Databases live inside a tenant.** The tenant comes from the authenticated
  identity; the database name travels in the request. A database name cannot
  reach across tenants.
- **Privileged operations are separately gated.** Creating a Raft-backed
  database (`databases:create_raft`) and a volume-isolated one
  (`databases:create_volume`) are checked *in addition to* `databases:create`,
  because each provisions dedicated infrastructure. `databases:scale` is
  distinct from `databases:update` because it consumes quota and mutates cluster
  placement and Raft membership. Sealing and unsealing
  (`databases:seal`, `databases:unseal`) are custody operations, gated apart
  from configuration.
- **A sealed database fails closed.** Its data is unreadable until unsealed.

## What this feature deliberately does not do

- **No cross-database transaction.** One commit targets one Raft shard — see
  [../sql/](../sql/engineering.md).
- **No barrier on a shared store.** Refused, not warned about.
- **No engine or backend change after creation.** Those are creation-time
  choices; changing them means a new database and a data migration.
- **No implicit database creation.** A request naming an unknown database fails.
