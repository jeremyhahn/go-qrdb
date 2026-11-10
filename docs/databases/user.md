# Databases — User Guide

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
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

Database names belong to your tenant. Another tenant can have a database
called `app` too: yours and theirs are separate databases, and neither tenant
can see, reach or learn of the other's.

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

**In a cluster, a non-raft database lives on the node that created it.** You
can still reach it through any node. That node sends the request to the owner
and returns the owner's answer. `qrdb database list` shows the database on every
node, with status `remote` on the nodes that do not hold it. While the owner
is down, the database is unavailable. This holds for `pebble`, `memory`,
`file` and `objstore` databases.

**Know what the backend does today.** For most databases the backend you choose
is recorded but is not where the data goes: the tenant's isolation mode and the
server's own settings decide that. Vector data from every database on a node
goes to one node-local store. `objstore` is the exception on a standalone
node: the database's data goes to the node's `storage.objstore` provider,
under the database's own object prefix. A Raft-mode node refuses an
`objstore` database when it is created. The design below fixes the rest.

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
dropped" rather than stored. Once the drop has finished, a request naming the
database gets "database not found" from every node: it no longer exists.

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

**Keep a verifiable log.** A verifiable log is an append-only list of entries
in a database, kept as an RFC 9162 Merkle tree: anyone holding a head can check
that an entry is in it, and that a later head extends an earlier one, without
trusting the server. A signed head is a C2SP checkpoint. The `qrdb vlog`
commands are in a client built with verifiable logs (the default build), and
the root `--database` flag picks the database:

```bash
qrdb vlog create --log releases --origin db.example.com/releases --signing-key-type ed25519
qrdb vlog append --log releases --entry "v1.0.0 sha256:..." --entry "v1.0.1 sha256:..."
qrdb vlog get --log releases --from 0 --limit 100
qrdb vlog head --log releases
qrdb vlog prove-inclusion --log releases --index 1 --size 2
qrdb vlog prove-consistency --log releases --size1 1 --size2 2
qrdb vlog sign-head --log releases
qrdb vlog rotate-key --log releases --signing-key-type ecdsa-p256-sha256
qrdb vlog list
```

`--origin` is required on every log and becomes the checkpoint's first line;
`--signing-key-type` gives the log a head-signing key (`ed25519` or
`ecdsa-p256-sha256`), and a log without one cannot sign heads. The proofs print
as hex hashes, or as `sdk/go/transport/vlogwire` JSON with `--output json`; check them
with any RFC 9162 verifier against the root `vlog head` prints. Signing a head
needs `vlog:sign_head`, an Operator permission an Admin does not hold.

**A head is signed once per tree size.** `qrdb vlog sign-head` on a log whose
current size already has a checkpoint is refused ("a checkpoint for tree size
N already exists"; `*vlog.CheckpointExistsError` to an SDK caller): append an
entry first. `qrdb vlog head` shows the latest
checkpoint and the size it signs.

**Expire old entry bodies.** On a log created with `--allow-expire-bodies`,
`qrdb vlog expire --log releases --cutoff 1000` drops the bodies of the
entries before index 1000. Their hashes stay, so every head and proof is still
valid; `vlog get` shows those entries as `(expired)`. A log created without the
flag refuses it ("does not allow expiring entry bodies";
`*vlog.ExpireNotAllowedError` to an SDK caller), and creating a log that
already exists is refused with `*vlog.LogExistsError`. Every SDK transport
returns these typed errors; [api.md](api.md), "Verifiable logs", lists their
wire reasons.

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

## Upgrading a node whose databases predate the catalog

A release before the catalog recorded each database in a `meta.json` file in
the database's directory under the node's data directory. This release reads
the replicated catalog alone and never that file, so a node that still holds
one refuses to start with `database.UnmigratedDatabasesError`, which names
every such file, rather than starting with those databases missing from every
listing. Move the data out with the release that wrote it, remove the old
database directories, start the node, and create the databases again.

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

**A request failed with "is held by node ..., which did not serve the
request".** The database is not replicated. The node named in the message holds
it, and that node did not answer. Bring that node back, or send the request to
it directly. Nothing was written on the node you asked. A PostgreSQL or MySQL
client connected to another node gets a "not a replica" refusal instead,
because those protocols do not forward. Connect to the owner.

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
revision, is refused as "unsupported".** The database lives on a dedicated
non-raft store, which has no lease service. Use a database on the default
store; see [../run-modes/user.md](../run-modes/user.md).

**"database not found" on a data-plane call.** Databases are never created
implicitly, so a typo in `--database` is an error rather than a new, empty
database you would not notice until later.

**`qrdb vlog append` (or `sign-head`, `expire`) says the log was not found.**
Logs are never created implicitly, as databases are not: create it with
`qrdb vlog create`, and check `--database`, since each database has its own
logs.

**`qrdb vlog` says verifiable logs are not available in a barrier-enabled
database.** A log is kept under the cluster's system barrier, not under a
database's or tenant's own, so a database with a barrier (sealed or not) and a
barrier-isolated tenant have no logs; the server answers `501 Not Implemented`
(`UNIMPLEMENTED`). Keep the log in a database without a barrier.

**`qrdb vlog` is an unknown command.** The client was built without verifiable
logs (`WITH_QRDB_VLOG=0`); a server built without them answers every vlog call
with `501 Not Implemented` (`UNIMPLEMENTED`).

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
