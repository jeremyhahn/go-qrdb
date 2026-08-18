# Databases — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

A database is a container inside your tenant with its own engine, storage,
replication, wire protocols and — if you want one — its own encryption key.

## Prerequisites

A reachable server, client certificates, and the `databases:*` permissions.
Creating a Raft-backed or volume-isolated database needs an extra grant
(`databases:create_raft`, `databases:create_volume`) on top of
`databases:create`.

## First run

```bash
qrdb database create app
qrdb database list
qrdb --database app kv put hello world
```

The root `--database` flag scopes data-plane commands. Without it you are
talking to the server's default database, `system`.

## Choosing engine and backend

You pick these at creation and cannot change them afterwards.

| Backend | What you get | Pick it when |
|---|---|---|
| `pebble` (default) | local durable store | single-node workloads, development |
| `memory` | nothing survives a restart | tests, caches |
| `file` | filesystem-mapped store | small metadata sets |
| `raft` | its own Raft group, replicated | you need replication, watches, leases, transactions or etcd |

Engines are `kv` (the default) and `timeseries`.

## Common tasks

**Create a replicated database.**

```bash
qrdb database create billing --backend raft --replicas 3 --cluster-mode distributed
```

**Change the replication factor later** — no recreation, no data move on your
part.

```bash
qrdb database scale billing --replicas 5
```

**Give a database its own encryption key.** This needs a dedicated store: an
explicit `--data-dir`, a `--backend raft` database, or a volume/raft-isolated
tenant.

```bash
qrdb database create secure --data-dir /var/lib/qrdb/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
```

Or with a Shamir quorum instead of a passphrase:

```bash
qrdb database create vault --backend raft \
     --barrier-enabled --barrier-strategy shamir \
     --barrier-shamir-threshold 3 --barrier-shamir-total 5
```

**Seal and unseal it.**

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

**Turn on SQL wire protocols for one database.**

```bash
qrdb database update-protocol --name app --postgresql
```

**Make a database look like etcd.** It must be `kv` on `raft`.

```bash
qrdb database create k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379
```

**Inspect what you have.**

```bash
qrdb database info billing --output json
qrdb db list
```

**Delete one.** Everything in it goes, permanently.

```bash
qrdb database drop scratch
```

## Troubleshooting

**`--barrier-enabled` was rejected.** The database shares the default store, so
a barrier would encrypt nothing another database could not already read. Give it
`--data-dir`, or make it a `--backend raft` database, or create it for a
volume/raft-isolated tenant.

**`--replicas` or `--cluster-mode` was rejected.** Those apply to
`--backend raft` only.

**`qrdb database scale` says the database is not eligible.** Scaling works on
dedicated-raft databases. A pebble-backed database has no replicas to change.

**Watch, lease, lock or transaction operations fail with "requires mode: raft".**
Those need consensus. See [../run-modes/user.md](../run-modes/user.md) — this is
the daemon's run mode, not just the database backend.

**"database not found" on a data-plane call.** Databases are never created
implicitly, so a typo in `--database` is an error rather than a new, empty
database you would not notice until later.

**You want to change engine or backend.** Not possible in place. Create a new
database with the shape you want and migrate the data — backup and restore
([../backup-restore/user.md](../backup-restore/user.md)) is the usual route.

**Everything in one database returns "unavailable" while others work.** That
database's barrier is sealed. `qrdb database barrier-status <name>` confirms it;
`qrdb database unseal <name> --secret ...` opens it.

**Creating a raft database failed with a permission error even though you can
create databases.** Raft-backed creation checks `databases:create_raft` on top
of `databases:create` — a dedicated Raft group is privileged provisioning.
