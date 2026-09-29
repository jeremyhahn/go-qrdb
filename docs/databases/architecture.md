# Databases — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11

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
- **Per-database barrier off the data path.** **Refused by the code**, not
  merely discouraged: a barrier is only meaningful when it wraps the store the
  database is served from. Today that is one shape — a non-raft database with
  an explicit `--data-dir`, which only the system tenant can set. A database
  sharing the default store, a `backend: raft` database (served from its Raft
  group) and every database of a volume- or raft-isolated tenant (served from
  the tenant's own store) are all refused: the barrier would wrap a store no
  request reads, and sealing it would leave the data readable and writable.
  Until per-database storage bindings land, a non-system tenant cannot create a
  per-database barrier at all; the bindings restore it with a barrier on the
  data path of every backend.
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

### What a drop reclaims

A database is a keyspace, not a directory, and its teardown has to reclaim the
keyspace. `DROP DATABASE` therefore reclaims, in this order:

1. **The data the database owns in the store it shares with other databases**
   — done first, while the database is still resolvable.
2. Its barrier, if it had one.
3. Its configuration directory, which for a database with a dedicated store
   also holds that store's files.
4. Its catalog entry.

**Dependents first, the record that makes the database resolvable last.** The
reverse order is what makes a residue permanent rather than merely present:
once the catalog record is gone, nothing in the system names the keyspace
again, so no later drop and no operator command can reach it. With this
order, the worst an interrupted drop leaves is a **live, empty database**,
and re-issuing the drop finishes it — the reclamation is idempotent, and an
already-empty keyspace is a success.

A reclamation that fails **fails the whole drop**. The database, its catalog
record and its data are left exactly as they were, because reporting a
successful drop over data that is still in the store is worse than refusing.

The data a database owns is addressed in two shapes, and a teardown that
reclaimed only the first would leave every document behind:

| Shape | Holds | Scoped by |
|---|---|---|
| `<tenant>/<db>/…` | plain KV and MVCC keys, the relational catalog, rows, index entries, every sequence counter (including the per-database table-id counter), the privilege catalog | tenant **and** database |
| `ts:<tenant>/<db>/…`, metric-index and retention records | time-series points, metric index, retention policies | tenant and database, inside the series prefixes, because it is the metric NAME that is namespaced |
| `<tenant>/<db>/__sys:collections/…`, `<tenant>/<db>/__col:…` | collection catalog entries and documents | tenant and database — the collection surface is resolved per (tenant, database) like every other engine |
| secondary index catalog | index names under `<tenant>/<db>/` | a namespace of its own inside the state machine, not reached by deleting keys |
| vector namespaces | namespaces under `<tenant>/<db>/` | their own per-node store, likewise not reached by deleting keys |

**Everything in that table is scoped by tenant**, and that is a property to
keep rather than an accident of how it was written. A drop is reachable by any
tenant that owns a database, and the store it runs against is shared, so a
prefix that does not carry the tenant is a prefix one tenant's drop uses to
delete another's data.

Three things are deliberately **not** reclaimed:

- the **time-series tag index**, whose keys are `tag key:value` and carry no
  metric, so no single database owns it;
- any record that is **database-agnostic** — roles, users, tenants;
- **collection records written before the collection surface was tenant-scoped**
  — `__sys:collections/<db>/…` and `__col:<db>:…` at the store root. These name
  a database and no tenant, so nothing can attribute them to the tenant issuing
  the drop, and a drop that deleted them by database alone would delete every
  other tenant's. They are left in place, inert: nothing reads them, because
  every transport's key surface is namespaced and no caller can compose the bare
  key, and nothing deletes them. An operator upgrading a deployment that has
  them exports them with the previous binary and re-imports them per tenant —
  which is the migration a reclamation would take away. See
  [collections/engineering.md](../collections/engineering.md) "Upgrading across
  the tenant-namespace change", which is the decision this follows.

**The reclamation reads through an unfiltered enumeration**, not the
user-facing one. Several of the shapes above are in the internal key
namespace, which the ordinary key listing hides from callers — a discovery
guard that is correct for a request handler and wrong for a teardown, which is
not a caller but the owner of the data. A reclamation built on the filtered
listing sees an empty result for the time-series prefixes and reports a
successful drop over data it never touched. The distinction is carried by a
separately named verb rather than a flag, so a filtered implementation cannot
satisfy the teardown's contract by accident.

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
  The refusal is made where every transport resolves a `(tenant, database)`
  pair, on every engine, whichever store serves the database — not by the
  store, because most stores are shared and cannot tell one sealed database
  from the rest. A database opened from disk with a barrier starts sealed.
  Sealing is a node's own state, not replicated: a database sealed on one node
  of a cluster is still served by any node on which it is unsealed.

## What this feature deliberately does not do

- **No cross-database transaction.** One commit targets one Raft shard — see
  [../sql/](../sql/engineering.md).
- **No barrier off the data path.** Refused, not warned about.
- **No engine or backend change after creation.** Those are creation-time
  choices; changing them means a new database and a data migration.
- **No implicit database creation.** A request naming an unknown database fails.
