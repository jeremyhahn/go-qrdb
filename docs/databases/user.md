# Databases — User Guide

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.
>
> "Coming change" sections describe the multi-model database design in
> [architecture.md](architecture.md). It is approved and not yet implemented.

Test reference: `make integration-test-databases`; coverage row: `test/integration/COVERAGE.md` `[databases](../../docs/databases/)`.

A database is a container inside your tenant with its own storage,
replication, wire protocols and — if you want one — its own encryption key.

## Prerequisites

A reachable server, client certificates, and the `databases:*` permissions.
Creating a Raft-backed or volume-isolated database needs an extra grant
(`databases:create_raft`, `databases:create_volume`) on top of
`databases:create`. A database barrier's secret, unseal key or share goes only
over TLS: on a connection without it, the Unix socket included, the call is
refused with `BarrierSecretRequiresTLSError` before anything is sent.

## First run

```bash
qrdb database create app
qrdb database list
qrdb --database app kv put hello world
```

The root `--database` flag scopes data-plane commands. Without it you are
talking to the server's default database, `system`.

## Choosing a backend

**There is no engine to choose.** There is no `--engine` flag; passing one is
an unknown-flag error. Every database stores keys, SQL tables,
collections, secondary indexes, vectors, time series, leases and watches, as
far as your server build includes them.

You pick the backend at creation and cannot change it afterwards.

| Backend | What you get | Pick it when |
|---|---|---|
| `pebble` (default) | local durable store | single-node workloads, development |
| `memory` | nothing survives a restart | tests, caches |
| `file` | filesystem-mapped store | small metadata sets |
| `raft` | its own Raft group, replicated | you need replication or etcd |

**Know what the backend does today.** For most databases the backend you choose
is recorded but is not where the data goes: the tenant's isolation mode and the
server's own settings decide that. Vector data from every database on a node
goes to one node-local store. `objstore` is accepted and then fails when the
database is opened. The design below fixes all three.

### Coming change: every database is multi-model

- **The backend you choose is where all of it lives** — vectors and time series
  included, and on every tenant isolation level.

| Backend | What you get | Pick it when |
|---|---|---|
| `raft` | its own Raft group, replicated across the cluster | anything you cannot afford to lose to one node |
| `pebble` | a durable store on one node | single-node deployments, or data you can rebuild |
| `file` | a store of plain files on one node | small data sets you want to inspect with ordinary tools |
| `objstore` | an object store (local directory, S3, GCS, Azure…) | large, cold data where object-store durability is what you want |
| `memory` | nothing survives a restart | tests and caches |

If you leave `--backend` out, the server's default is used: replicated `raft`
on a cluster and `pebble` on a standalone node, unless your operator sets a
different one. `qrdb database info <name>` shows where a
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

A non-system tenant therefore cannot create a per-database barrier at all.

```bash
qrdb database create secure --data-dir <install-dir>/secure \
     --barrier-enabled --barrier-strategy passphrase --barrier-passphrase "$PASSPHRASE"
```

**Split a database's key between people.** A database barrier can be held
M-of-N by share holders instead of a passphrase — the same share holders as
the server's barrier, enrolled for this database alone. Create the database
with a Shamir barrier and a data directory; it stays sealed until custody is
initialized. Enrol exactly as many holders as there will be shares (each from
a CSR they generated themselves; the server refuses one more until custody is
initialized), initialize, and have every holder claim
their envelope with the CLI or the desktop app, never in the browser. From
then on M holders unseal it with `barrier-quorum submit`; rekey and
revocation work as they do for a tenant barrier. The web UI's and the desktop
app's database wizard does the create, the enrolment and the initialization;
the claims and the unseal stay with the holders.

**Seal and unseal it.**

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

**Turn on SQL wire protocols for one database.**

```bash
qrdb database update-protocol app --postgresql
```

**Make a database look like etcd.** The node must run in raft mode; a
standalone node refuses the etcd protocol.

```bash
qrdb database create k8s --backend raft --etcd --etcd-listen-addr :2379
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
not pick up anything the old database left — including writes that were still
in flight when the drop began: those are refused with "database is being
dropped" rather than stored.

The default database, `system`, cannot be dropped.

**Keep a database's revision history bounded.** A database keeps every
revision until you compact it, and nothing compacts it unless you say so. Set
a policy and the node compacts it for you:

```bash
qrdb database update-compaction events --mode revision --retain-revisions 100000
qrdb database update-compaction audit --mode period --retain-period 720h
qrdb database info events --output json   # "compaction" shows the policy
qrdb database update-compaction events --mode off
```

Setting a policy needs `mvcc:compact`, and only the tenant that owns the
database may set one. As-of reads, watch replays and restores below what the
policy keeps fail afterwards -- that history is gone.

**A policy belongs to the database, not to a node.** It is stored in the
database's own replicated store, so every replica holds it and whichever node
currently leads the database's group runs it. If that node goes down, the new
leader carries on with the same policy and the samples already taken -- a
`period` policy's clock does not start over -- and a backup of the database
carries the policy with the data. A standalone node keeps it across restarts
the same way.

**A `period` policy needs one full period first.** After you set a period
policy, nothing is compacted until the database has been watched for one whole
period. Leader changes and restarts do not reset that wait.

**Dropping a database removes its policy.** A database created again under the
same name starts with no policy.

## Upgrading a cluster that holds raft databases

A cluster whose raft-backed databases were created with per-node shard IDs
cannot be upgraded in place to a release with cluster-wide shard IDs: two
databases may share one ID, and the cluster-wide issuer cannot tell which IDs
are taken. Build a new cluster on the new release and move the data across. A
cluster with no raft-backed databases upgrades as usual.

During a rolling upgrade, and on a new cluster until every node has joined
with the new release, creating a raft-backed database is refused with
`503 Service Unavailable` (`UNAVAILABLE`): the cluster feature
`cluster-shard-ids` is not active yet. Retry once every node runs the new
release.

## Troubleshooting

**`--barrier-enabled` was rejected.** The database would be served from a store
the barrier does not wrap, and the error names it: the shared default store,
its Raft group, or its tenant's own store. Only an operator can create one, on
a non-raft backend with `--data-dir` (see "Give a database its own encryption
key" above). A volume- or raft-isolated tenant's databases are already
encrypted by the tenant's barrier.

**`qrdb database drop` failed and the database is still there.** That is the
intended behaviour: a drop that cannot reclaim the database's data does not
report success and does not remove anything. Fix what it reported — most often
a store or a per-tenant volume that is unreachable, or a sealed barrier — and
run the same command again.

**A write failed with "database is being dropped".** The database was dropped
while the write was on its way, and the write was refused so it could not
reappear in the dropped database. Nothing was stored. If you meant to keep
writing, create the database again.

**`qrdb database drop` refused the `system` database.** The default database
holds the bootstrap records every request falls back to and cannot be
dropped.

**`qrdb database drop` says a drop is already in progress.** Another drop of
the same database has not finished. Wait for it rather than re-running; only
one of them can succeed.

**`qrdb database scale` says the database is not a distributed dedicated-raft
database.** Scaling works only on a `--backend raft` database created with
`--cluster-mode distributed`; any other database has no replicas to change.

**A lease or lock call, or a transaction naming a lease or comparing a
revision, is refused as "unsupported".** On a standalone node the file and
objstore backends keep no leases. Use pebble or memory; see
[../run-modes/user.md](../run-modes/user.md).

**"database not found" on a data-plane call.** Databases are never created
implicitly, so a typo in `--database` is an error rather than a new, empty
database you would not notice until later.

**You want to change backend.** Not possible in place. Create a new
database with the shape you want and migrate the data — backup and restore
([../backup-restore/user.md](../backup-restore/user.md)) is the usual route.

**Coming change: `--backend raft` is refused.** You are on a standalone node,
which has no Raft groups. Use `pebble`, or run a cluster.

**Coming change: `--replicas` or `--cluster-mode` is refused.** Those apply to
`--backend raft` only, and any other backend refuses them rather than ignoring
them.

**Coming change: a barrier on a `memory` database is refused.** Nothing is
stored at rest, so there is nothing for it to protect.

**Everything in one database returns "unavailable" while others work.** That
database is sealed; every engine on every transport refuses it until it is
unsealed. `qrdb database barrier-status <name>` confirms it;
`qrdb database unseal <name> --secret ...` opens it.

**Creating a raft database failed with a permission error even though you can
create databases.** Raft-backed creation checks `databases:create_raft` on top
of `databases:create` — a dedicated Raft group is privileged provisioning.
