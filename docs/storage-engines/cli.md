# Storage Engines — CLI

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the "Target design"
> note below.** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

**No dedicated CLI verb group.** Storage engines are a server configuration
concern.

## Selecting an engine — `qrdbd` configuration

```yaml
storage:
  backend: pebble    # pebble | memory | file | objstore
  cache_size: 0        # pebble block cache bytes; 0 = pebble default
  wal_dir: ""          # separate pebble WAL directory; empty = data dir
  max_open_files: 0    # 0 = pebble default
```

An `objstore` backend additionally configures its provider:

```yaml
storage:
  backend: objstore
  objstore:
    provider: s3        # local | memory | s3 | gcs | azure
    settings:
      bucket: "my-bucket"
      region: "us-east-1"
```

`storage.backend` has no `qrdb` value: `qrdbd` does not store through another
go-qrdb node.

In raft mode the state machines select their backend separately:

```yaml
raft:
  sm_storage: pebble    # pebble (default) | file | memory
```

Objstore is a snapshot and backup tier and is **refused** as a hot apply path.

`sm_storage` is read by the storage-generic state machines (`sm_type: disk` or
`fast`), which keep their per-replica storage under
`<data-dir>/sm/shard-<S>-replica-<R>`. Every other state machine carries its
own storage and rejects a non-pebble `sm_storage`. With the seal barrier
enabled, `disk` and `fast` over `pebble` storage are encrypted at rest by the
barrier, like the `pebble` and `mvcc` state machines, and so is `file`
storage: every record and journal file goes through the barrier's encrypting
file system, as the standalone `storage.backend: file` does (see below).
`sm_storage: memory` persists nothing. On every combination the
state machine serves KV, batches, the guarded verbs and transactions; see
[../run-modes/architecture.md](../run-modes/architecture.md#raft--single--or-multi-node).

### At rest under the seal

With `seal.enabled: true`, what `storage.backend` leaves on disk:

| `storage.backend` | On disk | While sealed |
|---|---|---|
| `pebble` | every sstable, WAL, manifest and options file encrypted by the barrier's encrypting file system | record reads and writes fail with the sealed error |
| `file` | every record file and commit journal encrypted by the same file system; file names are SHA-256 digests of the keys; only the format marker is plaintext | record reads and writes fail with the sealed error |
| `objstore` | values and object names in the clear; `qrdbd start` logs a warning naming the gap | — |
| `memory` | nothing | — |

The warning for `objstore` is logged once at start and reported in the
capability report's `unencryptedAtRest` list:

```
seal is enabled but data is stored unencrypted at rest  where="standalone backend \"objstore\" stores its data without the seal barrier's at-rest encryption; use backend \"pebble\" or \"file\""
```

A `file` data directory records whether it was written through the barrier. One
written with `seal.enabled: false` is refused at start once the seal is on,
and the reverse, rather than mixing plaintext and ciphertext records. There
is no in-place path from `seal.enabled: false`. See
[user.md](user.md#at-rest-encryption).

`qrdb barrier enable-encryption` (from strategy `none`) re-encrypts a `file`
store, standalone or `raft.sm_storage: file`, record by record under the new
key, as it does a `pebble` store; see
[../barrier-seal/cli.md](../barrier-seal/cli.md#qrdb-barrier-enable-encryption).

## Per-database storage — `qrdb database`

A database's backend is chosen at creation and is visible through the database
verbs:

```bash
qrdb database create app --backend pebble
qrdb database create cache --backend memory
qrdb database create secure --backend raft --data-dir /var/lib/qrdb/secure
qrdb database info app --output json      # reports backend, dataDir
```

See [../databases/cli.md](../databases/cli.md).

## Volume pools

The `qrdb` volume pool verbs are documented in
[../volume-pools/cli.md](../volume-pools/cli.md).

## Build-time backend selection

Cloud object-storage backends come from go-objstore and are build-tag gated:

```bash
make build WITH_AWS_S3=1 WITH_GCP_STORAGE=1 \
           WITH_AZURE_BLOB=1 WITH_GLACIER=1 WITH_AZURE_ARCHIVE=1
```

## Parity

Nothing in this feature has a `CommandDefinition`, because storage engines have
no `qrdb` verb. The volume pool verbs are covered in
[../volume-pools/cli.md](../volume-pools/cli.md); the database verbs that select a backend are covered in
[../databases/cli.md](../databases/cli.md).

## Target design: configuring the LSM segment layout

See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout).
**Not implemented.** The `qrdb database create ... --backend objstore` verb
itself does not change; what changes is what the node does with it:

```yaml
storage:
  backend: objstore
  objstore:
    provider: s3          # local | memory | s3 | gcs | azure
    settings:
      bucket: qrdb-data
      prefix: "node-a/db-1"   # qrdb-controlled identifier, never a raw key
    local_dir: /var/lib/qrdb/objstore   # WAL, MANIFEST, local block cache
    create_on_shared: all    # all | lower | none
```

`local_dir` is drawn from the same local-only volume pool the `pebble` engine
already uses (see [../volume-pools/cli.md](../volume-pools/cli.md)); this design adds
no verb. `glacier` and `azurearchive` remain
build-tag-gated archive destinations reachable only through
`qrdb backup create` (see [../backup-restore/cli.md](../backup-restore/cli.md)),
never through `storage.backend: objstore`.
