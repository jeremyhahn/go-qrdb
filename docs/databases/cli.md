# Databases — CLI

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.
>
> "Target" sections describe the multi-model database program in
> [architecture.md](architecture.md); they are approved and not
> implemented. OD-n refers to that document's ruled decisions.

Command group: `qrdb database` (alias `qrdb db`) — create, inspect, list, drop
and reconfigure databases. All verbs accept the root persistent flags documented
in [../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb database create <name>`

There is no `--engine` flag; passing one is an unknown-flag error. Every database serves every compiled-in engine.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--backend` | string | `pebble` | `pebble \| memory \| file \| raft \| objstore` — see below |
| `--data-dir` | string | — | dedicated data directory; **required for a per-database barrier**. Honoured for the system tenant only |
| `--cluster-mode` | string | unset (server applies `local`) | `local \| distributed` — **raft backend only** |
| `--replicas` | int | unset (server applies `1`) | replication factor — **raft backend only** |
| `--barrier-enabled` | bool | false | enable a per-database encryption barrier (**non-raft `--data-dir` database, system tenant only**) |
| `--barrier-strategy` | string | `passphrase` | `passphrase \| shamir`; `shamir` splits the root key to the database's enrolled share holders (see [Share-holder verbs](#share-holder-verbs-shamir-database-barrier)) instead of taking a passphrase |
| `--barrier-passphrase` | string | — | passphrase, when strategy is `passphrase` |
| `--barrier-shamir-threshold` | int | — | quorum threshold, when strategy is `shamir` |
| `--barrier-shamir-total` | int | — | total share count, when strategy is `shamir` |
| `--etcd` | bool | false | enable the etcd wire protocol |
| `--etcd-listen-addr` | string | `localhost:2379` | etcd listen address |
| `--etcd-advertise-urls` | strings | — | advertised etcd client URLs |
| `--sql-concurrency` | string | unset (server applies `optimistic`) | `optimistic \| pessimistic` — the database's SQL concurrency mode |

```bash
qrdb database create app
qrdb database create billing --backend raft --replicas 3 --cluster-mode distributed
qrdb database create secure --data-dir <install-dir>/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
qrdb database create k8s --backend raft --etcd --etcd-listen-addr :2379
```

**A barrier must be on the data path.** `--barrier-enabled` is accepted only on
a non-raft database with an explicit `--data-dir`, which only the system tenant
can set. It is **rejected**, not warned about, on a database sharing the default
store (`SharedStoreBarrierUnsupportedError`), on a `--backend raft` database and
on any database of a volume- or raft-isolated tenant
(`BarrierOffDataPathError`): each is served from a store the barrier would not
wrap. Both refusals are `422` / `FAILED_PRECONDITION`.

**Reserved names.** `wal`, `nodehost` and `__home` (in any case) name node
state and are refused for every database.

`--backend` is recorded and, for most databases, not where the data
lives, and `--backend objstore` fails when the store is opened. See
[architecture.md](architecture.md) "Current state".

### Target: `qrdb database create <name>` (remaining, unshipped)

These rows are not yet implemented.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--backend` | string | `pebble` | `memory \| file \| pebble \| raft \| objstore` — where **every** engine of the database stores its data |
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
- **A dropped database is not found.** Once the drop has finished, every node
  answers a command naming it with "database not found", whichever node ran
  the drop.
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
| `--secret` | string | no — unseal secret for the database barrier; the CLI does not enforce it and sends an empty secret to the server as is |

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
qrdb database update-etcd k8s --enabled --listen-addr :2379
```

### `qrdb database update-raft <name>`

| Flag | Type | Meaning |
|---|---|---|
| `--cluster-mode` | string | `none \| local \| distributed` |
| `--replica-id` | uint64 | raft replica ID |
| `--raft-address` | string | raft address, `host:port` |

### `qrdb database update-connection <name>`

For a database backed by a remote node. The connection is recorded and never
used — no remote proxy is constructed. The target design removes it, with
remote mode (OD-9).

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

## Share-holder verbs (Shamir database barrier)

The same verbs and flags as the tenant forms in
[../shamir-custody/cli.md](../shamir-custody/cli.md), taking `--database <name>`
in place of `--tenant-id`; rekey and revocation are authorized by RBAC and take
no `--so-pin`. The database is resolved in the caller's tenant.

```bash
qrdb database create vault --backend file --data-dir <install-dir>/vault \
     --barrier-enabled --barrier-strategy shamir \
     --barrier-shamir-threshold 2 --barrier-shamir-total 3
qrdb database barrier-officer register --database vault --username alice \
     --role so --csr-file alice.csr
qrdb database barrier-officer list --database vault
qrdb database barrier-officer unenrol --database vault --username carol   # before init only
qrdb database barrier-init-shamir --database vault --policy designated
qrdb database barrier-officer claim --database vault --username alice \
     --key alice.key --out alice.envelope
qrdb database barrier-quorum submit --database vault --op unseal \
     --username alice --key alice.key --envelope alice.envelope
qrdb database barrier-rekey --database vault --threshold 2
qrdb database barrier-officer revoke --database vault --username bob --threshold 2
```

| Verb | Does |
|---|---|
| `barrier-officer register\|list\|claim\|claim-begin\|claim-complete\|revoke` | Enrol, list, claim (whole handshake or split), revoke |
| `barrier-officer unenrol` | Remove a holder before the custody is initialized: it holds no share, so no quorum is needed. Refused once initialized; revoke instead |
| `barrier-init-shamir` | Split the key at the M-of-N the create fixed (`--barrier-shamir-threshold` of `--barrier-shamir-total`); exactly that many holders must be enrolled |
| `barrier-quorum begin\|submit` | A holder's factor challenge and share submission |
| `barrier-rekey` | Open a rekey round |

`register` prints the CA-issued certificate (`--output json` for
`cert_pem`). `claim` needs the connection to present the holder's certificate;
in process (the embedded profile) the claim is bound to the enrolled
certificate by the holder's signature over the share-claim message for the
server's nonce instead.
`qrdb database unseal` with `--secret` and `qrdb database seal` are refused for
a Shamir database barrier; its holders unseal and seal it with
`barrier-quorum submit --op unseal|seal`.

## Integration tests

`make integration-test-databases` drives these verbs through the `qrdb` CLI
(`test/integration/database_lifecycle_cli_test.go`,
`database_barrier_cli_test.go`, `database_barrier_holders_cli_test.go`); the
`_PerProtocol` and `_AllTransports` tests run each verb on every transport.
`test/integration/COVERAGE.md` records what is not covered.
