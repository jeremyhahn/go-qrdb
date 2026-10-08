# Storage Engines and Volume Pools — CLI

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the "Target design"
> note below.** · Scope: go-qrdb · Last verified: 2026-10-06

**No dedicated CLI verb group.** Storage engines are a server configuration
concern, and volume pools have an SDK surface but no `qrdb` verbs.

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

There is no `qrdb` storage backend in `qrdbd`'s own config -- a networked
adapter that stores through another go-qrdb node exists, but it is
go-xkms's own `pkg/storage/qrdb/` wrapper over the go-qrdb SDK, not a value
`storage.backend` accepts here.

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
barrier, like the `pebble` and `mvcc` state machines; `file` storage cannot be
encrypted, so `qrdbd start` (and the SDK) refuse it under the barrier rather
than keep the replica's data in plaintext. `sm_storage: memory` persists
nothing and is allowed. On every combination the
state machine serves KV, batches, the guarded verbs and transactions; see
[../run-modes/architecture.md](../run-modes/architecture.md#raft--single--or-multi-node).

## Per-database storage — `qrdb database`

A database's backend is chosen at creation and is visible through the database
verbs:

```bash
qrdb database create app --backend pebble
qrdb database create cache --backend memory
qrdb database create secure --backend raft --data-dir /var/lib/qrdb/secure
qrdb database info app --output json      # reports engine, backend, dataDir
```

See [../databases/cli.md](../databases/cli.md).

## Volume pools

Volume pools have **no CLI verbs**. They are reachable through the SDK:

```go
pools := client.VolumePools()   // nil if the transport does not support them
if pools == nil { /* handle */ }

list, err := pools.List(ctx)
backend, err := pools.Pick(ctx, "fast-nvme", transport.VolumePoolHint{
	TenantID: "acme", SizeHint: 50 << 30,
})
err = pools.RegisterPool(ctx, transport.RegisterVolumePoolRequest{
	Name: "fast-nvme", Paths: []string{"/mnt/nvme0", "/mnt/nvme1"},
})
```

`Client.VolumePools()` returns **nil** on a transport that does not implement
`VolumePoolProvider`. Nil-check it — the nil is not an error value.

Volume pools are **local-only** by design: the paths are filesystem paths on the
serving node, because the consumer is PebbleDB and it needs a real path.

## Build-time backend selection

Cloud object-storage backends come from go-objstore and are build-tag gated:

```bash
make build WITH_AWS_S3=1 WITH_GCP_STORAGE=1 \
           WITH_AZURE_BLOB=1 WITH_GLACIER=1 WITH_AZURE_ARCHIVE=1
```

## Parity

Nothing in this feature has a `CommandDefinition`, because nothing here has a
`qrdb` verb. The database verbs that select a backend are covered in
[../databases/cli.md](../databases/cli.md).

## Target design: configuring the LSM segment layout

See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout).
**Not implemented.** The `qrdb database create ... --backend objstore` verb
itself does not change; what changes is what the node does with it:

```yaml
storage:
  engine: objstore
  objstore:
    provider: s3          # local | memory | s3 | gcs | azure
    settings:
      bucket: qrdb-data
      prefix: "node-a/db-1"   # qrdb-controlled identifier, never a raw key
    local_dir: /var/lib/qrdb/objstore   # WAL, MANIFEST, local block cache
    create_on_shared: all    # all | lower | none
```

`local_dir` is drawn from the same local-only volume pool the `pebble` engine
already uses (see "Volume pools" above) — there is still no CLI verb for
volume pools, and this design adds none. `glacier` and `azurearchive` remain
build-tag-gated archive destinations reachable only through
`qrdb backup create` (see [../backup-restore/cli.md](../backup-restore/cli.md)),
never through `storage.engine: objstore`.
