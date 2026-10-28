# Storage Engines — User Guide

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the "Target design"
> note below.** · Scope: go-qrdb · Last verified: 2026-10-27

Test reference: `make integration-test-storage-engines`; coverage row: `test/integration/COVERAGE.md` `[storage-engines](../../docs/storage-engines/)`.

Storage engines decide where bytes land. Most of the time you choose one once,
in the server config or when creating a database, and never think about it
again.

## Prerequisites

Storage engine selection is a **server** decision, made in `qrdbd`'s config or
at database creation. There is no client verb for it.

## Choosing an engine

| Backend | Pick it when | Notes |
|---|---|---|
| `pebble` (default) | production, anything durable and large | LSM store; the right default |
| `file` | small metadata sets, or when a real filesystem layout matters | durable: atomic writes plus a replayed commit journal; encrypted at rest under the seal, like `pebble` |
| `memory` | tests, caches, anything disposable | nothing survives a restart |
| `objstore` | snapshot/backup/archive tiers | go-objstore backend; not a hot apply path |

```yaml
storage:
  backend: pebble
  cache_size: 0
  wal_dir: ""
  max_open_files: 0
```

**There is no `qrdb` storage backend here.** `qrdbd` does not store through
another go-qrdb node.

In raft mode the state machines pick their own backend:

```yaml
raft:
  sm_storage: pebble    # pebble (default) | file | memory
```

### Sharing a `file` directory between processes

More than one process may open the same `file` data directory — an application
embedding a library and a daemon that serve the same data, for instance. Writes
are serialized across them by an advisory lock on `.qrdb-file-lock` in the
directory, and the guarded writes (`SaveIfAbsent`, `CompareAndSwap`,
`CompareAndDelete`) stay atomic: of two processes racing the same swap, exactly
one wins. Keep the directory on a local filesystem (the lock is not dependable
over NFS), and leave `.qrdb-file-lock` in place. A process that crashes releases
the lock; nothing needs cleaning up.

## Per-database storage

Each database chooses its own backend at creation:

```bash
qrdb database create app    --backend pebble
qrdb database create cache  --backend memory
qrdb database create secure --backend raft --data-dir /var/lib/qrdb/secure
qrdb database info app --output json      # shows backend, dataDir
```

This is fixed at creation. Changing it means a new database and a data
migration.

## At-rest encryption

With the seal enabled, the barrier encrypts what each backend leaves on disk.
The guarantee differs by backend: `pebble` and `file` are encrypted at rest,
`objstore` and `memory` are not. What each one still leaves visible on disk
is in the table in
[architecture.md](architecture.md#at-rest-encryption-one-mechanism-for-every-on-disk-backend).

**While the barrier is sealed** a `pebble` or `file` store opens, but every read
and write fails with the typed sealed error (`errors.Is(err, sdk.ErrSealed)`
in Go; `503` / `Unavailable` over the wire) until it is unsealed. No record is
served from a store whose key is not available.

**Enabling encryption re-encrypts a `file` store.** `qrdb barrier
enable-encryption` moves a node from strategy `none` to a keyed strategy and
copies every `file` store under the new key, record by record, verifies it and
swaps it in, exactly as it does a `pebble` store. That covers the standalone
`storage.backend: file` store and, in raft mode, `raft.sm_storage: file`. If
the copy does not match, nothing is swapped and the store stays under the old
key. See
[../barrier-seal/user.md](../barrier-seal/user.md).

**A `file` store written with the seal off cannot be opened with it on**, nor
the reverse; the daemon refuses it at start rather than serve plaintext
records beside encrypted ones. Move such data through backup and restore into
a node started with the seal on — see
[../barrier-seal/user.md](../barrier-seal/user.md#running-with-no-barrier-at-all).

## Tenant isolation

You do not configure this at the storage layer. Every tenant has its own
database behind its own barrier, on whichever backend you choose; no key prefix
is involved. See [../seal/multi-tenant.md](../seal/multi-tenant.md).

## Volume pools

A volume pool is a named set of local filesystem paths a database or tenant can
draw a dedicated store from. See [../volume-pools/user.md](../volume-pools/user.md).

## Troubleshooting

**The daemon refused `raft.sm_storage`.** Objstore is a snapshot and backup
tier and is refused as a hot apply path. Use `pebble`, `file` or `memory`. Note
also that `sm_storage` applies only to `sm_type: disk` and `fast`: every other
state machine carries its own storage and rejects a non-pebble `sm_storage`.

**Everything vanished after a restart.** The engine is `memory`. That is what it
does.

**A `file` store refused to open, saying it "was written plaintext and cannot
be opened encrypted"** (or the reverse). The data directory was written with
the seal in the other state. See "At-rest encryption" above; the engine
refuses rather than mix the two.

**A `file` store refused to open, saying its format version does not match
this build's.** A non-empty directory with no format marker, or with a marker
naming another version, is refused. Start the store in an empty directory: a
raft replica rebuilds from the cluster over raft's restore. A `file`-backed
backup in another format version is refused the same way; see [../backup-restore/user.md](../backup-restore/user.md).

**The server would not start, complaining about a directory.** Backing
directories are opened at startup, not lazily at first write, so a permissions
or path problem shows up immediately rather than at the first busy moment.

**An objstore backend refused to open with "carries no qrdb-objstore-format
marker".** The bucket or directory already holds objects that this
engine's layout did not write. The engine refuses a foreign layout rather
than read it with keys missing; there is no in-place migration. Point the
backend at an empty bucket or directory, or start empty. Give the engine a
bucket or prefix of its own: any object it did not write causes the same
refusal.

**You want an S3 or GCS backend.** Those come from go-objstore and are
build-tag gated — rebuild with `WITH_AWS_S3=1`, `WITH_GCP_STORAGE=1` and so on.
They are a snapshot and archive tier, not a hot path.

**A write failed while the node was shutting down.** That is a closed store,
not a fault: the node released its storage while your call was in flight. Match
it in code rather than reading the message —

```go
if err := kv.Put(ctx, key, value); err != nil && sdk.IsStorageClosed(err) {
	return nil // the node is going down; conclude and let a restart resume
}
```

Over a network transport you see gRPC `Unavailable` or HTTP `503` instead of
the typed value, which means the same thing: retry, or try another node.

**You want to change a database's backend.** Not possible in place. Create a new
database and migrate — see [../backup-restore/user.md](../backup-restore/user.md).

## Target design: `objstore` as an LSM in a bucket (not yet shipped)

See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout)
for the full design — **nothing in this section is implemented yet**, and
nothing here is a setting you can use today.

Today a standalone node's `storage.backend: objstore` stores one object per
key, and a database created with `backend: objstore` is refused when it opens
(see [../databases/user.md](../databases/user.md)). The proposal moves
`objstore` to the same sorted-segment layout the `pebble` engine uses, with
the segments living in an object-storage bucket rather than on local disk.
If it ships:

- You would pick `objstore` when you want a database's durable data in S3,
  GCS, Azure or a local object-store directory, with capacity that is not
  bounded by the node's attached disk, encrypted under the seal like every
  other engine.
- A small local directory would still be required (WAL, manifest, block
  cache) — object storage replaces the *bulk* of the data, not every byte on
  the node.
- A store written by today's `objstore` engine would not open under the new
  layout; you would move its data by backup and restore
  ([../backup-restore/user.md](../backup-restore/user.md)).
- `glacier` and `azurearchive` would remain backup and archive destinations
  reached through `qrdb backup create`, never a live `objstore` provider —
  their retrieval delays are measured in hours, which is incompatible with
  serving reads.
- Whether this also becomes available as a Raft hot apply path, not only a
  standalone or per-database backend, is an open question the design defers
  to a later, separately gated phase.
