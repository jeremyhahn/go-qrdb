# Databases — User Guide

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> "Coming change" sections describe the multi-model database design in
> [architecture.md](architecture.md). It is pending review and not implemented.

A database is a container inside your tenant with its own storage,
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

**Know what the backend does today.** For most databases the backend you choose
is recorded but is not where the data goes: the tenant's isolation mode and the
server's own settings decide that. Vector data from every database on a node
goes to one node-local store. `objstore` is accepted and then fails when the
database is opened. The design below fixes all three.

### Coming change: every database is multi-model

- **There is no engine to choose.** Every database stores keys, SQL tables,
  collections, secondary indexes, vectors, time series, leases and watches,
  as far as your server build includes them. `--engine` goes away.
- **The backend you choose is where all of it lives** — vectors and time series
  included, and on every tenant isolation level.

| Backend | What you get | Pick it when |
|---|---|---|
| `raft` | its own Raft group, replicated across the cluster | anything you cannot afford to lose to one node |
| `pebble` | a durable store on one node | single-node deployments, or data you can rebuild |
| `file` | a store of plain files on one node | small data sets you want to inspect with ordinary tools |
| `objstore` | an object store (local directory, S3, GCS, Azure, MinIO…) | large, cold data where object-store durability is what you want |
| `memory` | nothing survives a restart | tests and caches |

If you leave `--backend` out, the server's default is used; the proposal is
replicated `raft` on a cluster and `pebble` on a standalone node, and your
operator can set a different one. `qrdb database info <name>` shows where a
database actually lives.

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

**Give a database its own encryption key.** A per-database barrier is accepted
only where it wraps the store the database is actually served from: a non-raft
database with an explicit `--data-dir`, created by the system tenant (an
operator). Anywhere else it is refused, because the data would live somewhere
the barrier does not reach and sealing it would protect nothing:

- a `--backend raft` database is served from its Raft group;
- every database of a volume- or raft-isolated tenant is served from that
  tenant's own store, which the tenant's barrier already encrypts;
- a non-system tenant's `--data-dir` is ignored, so its databases share the
  default store.

Until per-database storage bindings land, a non-system tenant therefore cannot
create a per-database barrier at all; the bindings restore it with a barrier
that is on the data path for every backend.

```bash
qrdb database create secure --data-dir <install-dir>/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
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

**Delete one.** Everything in it goes, permanently — keys, collections,
documents, tables, rows, indexes and vector namespaces alike.

```bash
qrdb database drop scratch
```

Creating a database under a name you dropped gives you an empty one. It does
not pick up anything the old database left.

## Troubleshooting

**`--barrier-enabled` was rejected.** The database shares the default store, so
a barrier would encrypt nothing another database could not already read. Give it
`--data-dir`, or make it a `--backend raft` database, or create it for a
volume/raft-isolated tenant.

**`--replicas` or `--cluster-mode` was rejected.** Those apply to
`--backend raft` only.

**`qrdb database drop` failed and the database is still there.** That is the
intended behaviour: a drop that cannot reclaim the database's data does not
report success and does not remove anything. Fix what it reported — most often
a store or a per-tenant volume that is unreachable, or a sealed barrier — and
run the same command again.

**`qrdb database drop` says a drop is already in progress.** Another drop of
the same database has not finished. Wait for it rather than re-running; only
one of them can succeed.

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
After the coming change there is no engine to change: every database already
has every engine.

**Coming change: `--backend raft` is refused.** You are on a standalone node,
which has no Raft groups. Use `pebble`, or run a cluster.

**Coming change: `--replicas` or `--cluster-mode` is refused.** Those apply to
`--backend raft` only; they are no longer silently ignored on other backends.

**Coming change: a barrier on a `memory` database is refused.** Nothing is
stored at rest, so there is nothing for it to protect.

**Everything in one database returns "unavailable" while others work.** That
database is sealed; every engine on every transport refuses it until it is
unsealed. `qrdb database barrier-status <name>` confirms it;
`qrdb database unseal <name> --secret ...` opens it.

**Creating a raft database failed with a permission error even though you can
create databases.** Raft-backed creation checks `databases:create_raft` on top
of `databases:create` — a dedicated Raft group is privileged provisioning.
