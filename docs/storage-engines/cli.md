# Storage Engines — CLI

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the "Target design"
> note below.** · Scope: go-qrdb · Last verified: 2026-10-27

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
`<data-dir>/sm/shard-<S>-replica-<R>`. An embedder's
`node.WithReplicaStorageFactory` opens that per-replica storage in place of
`sm_storage`. Every other state machine carries its own storage and rejects a
non-pebble `sm_storage`. With the seal barrier
enabled, `disk` and `fast` over `pebble` storage are encrypted at rest by the
barrier, like the `pebble` and `mvcc` state machines, and so is `file`
storage: every record and journal file goes through the barrier's encrypting
file system, as the standalone `storage.backend: file` does (see below).
`sm_storage: memory` persists nothing. On every combination the
state machine serves KV, batches, the guarded verbs and transactions; see
[../run-modes/architecture.md](../run-modes/architecture.md#raft--single--or-multi-node).

### At rest under the seal

With `seal.enabled: true`, `pebble` and `file` are encrypted at rest and fail
record reads and writes with the sealed error while sealed; `objstore` and
`memory` are not encrypted. What each leaves on disk is in the table in
[architecture.md](architecture.md#at-rest-encryption-one-mechanism-for-every-on-disk-backend).

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

## Target design: no CLI change

See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout).
**Not implemented** — future work under the SPEC row `object-storage-lsm`.
The proposed object-storage LSM segment layout adds no `qrdb` verb and no
flag. `qrdb database create ... --backend objstore` is unchanged as a
command; what changes is what the node does when such a database opens. The
proposed configuration is in [engineering.md](engineering.md)'s target
design, not here, because none of it exists yet. `glacier` and
`azurearchive` stay archive destinations reached through `qrdb backup create`
(see [../backup-restore/cli.md](../backup-restore/cli.md)).
