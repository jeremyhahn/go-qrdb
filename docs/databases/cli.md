# Databases — CLI

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-01
>
> "Target" sections describe the multi-model database program in
> [architecture.md](architecture.md); they are approved by the owner on 2026-09-30 and not
> implemented. OD-n refers to that document's ruled decisions.

Command group: `qrdb database` (alias `qrdb db`) — create, inspect, list, drop
and reconfigure databases. All verbs accept the root persistent flags documented
in [../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb database create <name>`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--engine` | string | `kv` | `kv \| timeseries` — **removed in the target design** |
| `--backend` | string | `pebble` | `pebble \| memory \| file \| raft \| objstore` — see below |
| `--data-dir` | string | — | dedicated data directory; **required for a per-database barrier**. Honoured for the system tenant only |
| `--cluster-mode` | string | `local` | `local \| distributed` — **raft backend only** |
| `--replicas` | int | 1 | replication factor — **raft backend only** |
| `--barrier-enabled` | bool | false | enable a per-database encryption barrier (**non-raft `--data-dir` database, system tenant only**) |
| `--barrier-strategy` | string | `passphrase` | `passphrase \| shamir`; `shamir` is refused (`database.UnsupportedBarrierStrategyError`) until per-database Shamir share holders ship |
| `--barrier-passphrase` | string | — | passphrase, when strategy is `passphrase` |
| `--barrier-shamir-threshold` | int | — | quorum threshold, when strategy is `shamir` |
| `--barrier-shamir-total` | int | — | total share count, when strategy is `shamir` |
| `--etcd` | bool | false | enable the etcd wire protocol |
| `--etcd-listen-addr` | string | `localhost:2379` | etcd listen address |
| `--etcd-advertise-urls` | strings | — | advertised etcd client URLs |

```bash
qrdb database create app
qrdb database create billing --engine kv --backend raft --replicas 3 --cluster-mode distributed
qrdb database create secure --data-dir <install-dir>/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
qrdb database create k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379
```

**A barrier must be on the data path.** `--barrier-enabled` is accepted only on
a non-raft database with an explicit `--data-dir`, which only the system tenant
can set. It is **rejected**, not warned about, on a database sharing the default
store (`SharedStoreBarrierUnsupportedError`), on a `--backend raft` database and
on any database of a volume- or raft-isolated tenant
(`BarrierOffDataPathError`): each is served from a store the barrier would not
wrap. Both refusals are `422` / `FAILED_PRECONDITION`.

**Reserved names.** `wal` and `nodehost` (in any case) name node state and
are refused for every database.

Measured: `--backend` is recorded and, for most databases, not where the data
lives, and `--backend objstore` fails when the store is opened. See
[architecture.md](architecture.md) "Current state".

### Target: `qrdb database create <name>`

`--engine` is removed; passing it is an unknown-flag error. Every database
serves every compiled-in engine.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--backend` | string | node `database_defaults.backend` (OD-3) | `memory \| file \| pebble \| raft \| objstore` — where **every** engine of the database stores its data |
| `--data-dir` | string | under the tenant's data root | the database's store directory; for volume and raft tenants it must lie under the tenant's root |
| `--cluster-mode` | string | node default | `local \| distributed` — **raft backend only**, refused otherwise |
| `--replicas` | int | node default | replication factor — **raft backend only**, refused otherwise |
| `--raft-state-storage` | string | node default | `pebble \| file \| memory` — the raft state machine's local store (OD-8) |
| `--barrier-*`, `--etcd*` | | | as above; a barrier is refused on `memory`, etcd is refused on anything but `raft` |

```bash
qrdb database create app                                   # node default backend
qrdb database create cache --backend memory
qrdb database create archive --backend objstore
qrdb database create billing --backend raft --replicas 3 --cluster-mode distributed
```

`qrdb database info <name> --output json` reports the resolved `binding` —
local or raft, owning or replica nodes, whose key encrypts it, and the
generation.

## `qrdb database info <name>` / `list`

Show details, or list every database. No flags beyond the persistent set.

```bash
qrdb database info billing --output json
qrdb db list
```

## `qrdb database drop <name>`

Drop a database and delete all of its data. **Irreversible.** No flags.

```bash
qrdb database drop scratch
```

"All of its data" means every key, collection, document, table, row,
secondary index, sequence and vector namespace the database owns — not only
the directory that holds its configuration. The name is released last, so:

- **A drop that fails before its data is reclaimed has removed nothing.**
  Re-issue it.
- **A drop that fails AFTER its data is reclaimed — closing its backend or
  removing its directory — leaves the database fenced, not gone: emptied of
  its data, still unresolvable to ordinary operations, still occupying its
  name.** This is the only case in which a failed drop has changed
  anything; re-issuing it finishes the remaining, already-idempotent steps.
- **A drop interrupted part way leaves the database in place and emptier.**
  Re-issue it; the second drop finishes the first.
- **A name reused after a successful drop starts empty.** It inherits nothing
  from the database that held it.
- **A write already on its way when the drop starts is refused, not lost into
  the dropped database.** A key, document, row, index or time-series write
  delayed past the drop gets "database is being dropped" and stores nothing,
  however long it was delayed. Vector writes are the exception: they are
  checked just before they are written, not refused by the store.
- **The default database (`system`) cannot be dropped.** The command is
  refused before anything is touched.

## `qrdb database scale <name>`

Change the replication factor of an existing **dedicated-raft** database, adding
or removing voting replicas without recreating it.

| Flag | Type | Required |
|---|---|---|
| `--replicas` | int | yes — target factor, must be ≥ 1 |

```bash
qrdb database scale billing --replicas 5
```

## Barrier verbs

### `qrdb database barrier-status <name>`

Show the database's encryption-barrier status. No flags.

### `qrdb database seal <name>`

Seal the database's barrier. Data becomes unreadable until unsealed. No flags.

### `qrdb database unseal <name>`

| Flag | Type | Required |
|---|---|---|
| `--secret` | string | yes — unseal secret for the database barrier |

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

### `qrdb database update-barrier <name>`

| Flag | Type | Meaning |
|---|---|---|
| `--enabled` | bool | enable the encryption barrier |
| `--disabled` | bool | disable it |

## Configuration verbs

### `qrdb database update-protocol <name>`

| Flag | Type | Meaning |
|---|---|---|
| `--mysql` | bool | enable the MySQL wire protocol |
| `--postgresql` | bool | enable the PostgreSQL wire protocol |

### `qrdb database update-sql <name>`

| Flag | Type | Meaning |
|---|---|---|
| `--concurrency` | string | `optimistic \| pessimistic` — the database's default SQL concurrency mode |

`qrdb database create` takes the same setting as `--sql-concurrency`. See
[../row-locking/cli.md](../row-locking/cli.md).

### `qrdb database update-compaction <name>`

Set the database's scheduled-compaction policy. Off unless set; requires
`mvcc:compact`, and only the tenant that owns the database may set it.

| Flag | Type | Meaning |
|---|---|---|
| `--mode` | string | `off \| revision \| period` (required) |
| `--retain-revisions` | int | `revision` mode: keep the last N revisions |
| `--retain-period` | duration | `period` mode: keep history newer than this (>= `1s`) |

```bash
qrdb database update-compaction events --mode revision --retain-revisions 100000
qrdb database update-compaction audit --mode period --retain-period 720h
qrdb database update-compaction events --mode off
```

`qrdb database create` does not take a policy: setting one is compacting, and
create is a different permission. The policy is stored in the database's own
replicated store, so it applies on every node and survives a leader change, a
restart and a backup; `qrdb database info` shows it.

### `qrdb database update-etcd <name>`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--enabled` / `--disabled` | bool | — | turn the etcd protocol on or off |
| `--listen-addr` | string | — | etcd listen address |
| `--advertise-urls` | strings | — | advertised client URLs |

```bash
qrdb database update-etcd --name k8s --enabled --listen-addr :2379
```

### `qrdb database update-raft <name>`

| Flag | Type | Meaning |
|---|---|---|
| `--cluster-mode` | string | `none \| local \| distributed` |

### `qrdb database update-connection <name>`

For a database backed by a remote node. Measured: the connection is recorded and
never used — no remote proxy is constructed. Removed in the target design, with remote mode (OD-9, ruled A).

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--address` | string | — | remote database address, `host:port` |
| `--conn-transport` | string | `tcp` | `tcp \| quic` |
| `--conn-ca-file` | string | — | connection TLS CA certificate |
| `--conn-cert-file` | string | — | connection TLS client certificate |
| `--conn-key-file` | string | — | connection TLS client key |

## Scoping data-plane commands

The root `--database` flag scopes every data-plane verb:

```bash
qrdb --database billing kv list invoices/
qrdb --database billing sql query "SELECT * FROM kv"
```

Absent, the server's default database (`system`) is used.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
