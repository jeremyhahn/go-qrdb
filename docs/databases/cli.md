# Databases — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb database` (alias `qrdb db`) — create, inspect, list, drop
and reconfigure databases. All verbs accept the root persistent flags documented
in [../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb database create <name>`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--engine` | string | `kv` | `kv \| timeseries` |
| `--backend` | string | `pebble` | `pebble \| memory \| file \| raft` |
| `--data-dir` | string | — | dedicated data directory; **required for a real per-database barrier** |
| `--cluster-mode` | string | `local` | `local \| distributed` — **raft backend only** |
| `--replicas` | int | 1 | replication factor — **raft backend only** |
| `--barrier-enabled` | bool | false | enable a per-database encryption barrier (**requires a dedicated store**) |
| `--barrier-strategy` | string | `passphrase` | `passphrase \| shamir` |
| `--barrier-passphrase` | string | — | passphrase, when strategy is `passphrase` |
| `--barrier-shamir-threshold` | int | — | quorum threshold, when strategy is `shamir` |
| `--barrier-shamir-total` | int | — | total share count, when strategy is `shamir` |
| `--etcd` | bool | false | enable the etcd wire protocol |
| `--etcd-listen-addr` | string | `localhost:2379` | etcd listen address |
| `--etcd-advertise-urls` | strings | — | advertised etcd client URLs |

```bash
qrdb database create app
qrdb database create billing --engine kv --backend raft --replicas 3 --cluster-mode distributed
qrdb database create secure --data-dir /var/lib/qrdb/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
qrdb database create k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379
```

**A barrier needs a dedicated store.** `--barrier-enabled` is accepted only with
an explicit `--data-dir`, a `--backend raft` database, or a database created for
an IsolationVolume / IsolationRaft tenant. On a database sharing the default
store it is **rejected**, not warned about.

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

- **A drop that fails has removed nothing.** Re-issue it.
- **A drop interrupted part way leaves the database in place and emptier.**
  Re-issue it; the second drop finishes the first.
- **A name reused after a successful drop starts empty.** It inherits nothing
  from the database that held it.

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

For a database backed by a remote node.

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
