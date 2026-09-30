# Storage Engines and Volume Pools — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

**No dedicated CLI verb group.** Storage engines are a server configuration
concern, and volume pools have an SDK surface but no `qrdb` verbs.

## Selecting an engine — `qrdbd` configuration

```yaml
storage:
  engine: pebble    # file | memory | pebble | qrdb
  file:
    dir: /var/lib/qrdb/data
  pebble:
    dir: /var/lib/qrdb/pebble
  qrdb:
    address: "db.example.com:63003"
    transport: grpc
```

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
make build WITH_AWS_S3=1 WITH_MINIO=1 WITH_GCP_STORAGE=1 \
           WITH_AZURE_BLOB=1 WITH_GLACIER=1 WITH_AZURE_ARCHIVE=1
```

## Parity

Nothing in this feature has a `CommandDefinition`, because nothing here has a
`qrdb` verb. The database verbs that select a backend are covered in
[../databases/cli.md](../databases/cli.md).
