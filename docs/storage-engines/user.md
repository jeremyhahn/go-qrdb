# Storage Engines and Volume Pools — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Storage engines decide where bytes land. Most of the time you choose one once,
in the server config or when creating a database, and never think about it
again.

## Prerequisites

Storage engine selection is a **server** decision, made in `qrdbd`'s config or
at database creation. There is no client verb for it.

## Choosing an engine

| Engine | Pick it when | Notes |
|---|---|---|
| `pebble` (default) | production, anything durable and large | LSM store; the right default |
| `file` | small metadata sets, or when a real filesystem layout matters | durable: atomic writes plus a replayed commit journal |
| `memory` | tests, caches, anything disposable | nothing survives a restart |
| `qrdb` | this node should store through another node | reaches a remote node over the SDK |

```yaml
storage:
  engine: pebble
  pebble:
    dir: /var/lib/qrdb/pebble
```

In raft mode the state machines pick their own backend:

```yaml
raft:
  sm_storage: pebble    # pebble (default) | file | memory
```

## Per-database storage

Each database chooses its own backend at creation:

```bash
qrdb database create app    --backend pebble
qrdb database create cache  --backend memory
qrdb database create secure --backend raft --data-dir /var/lib/qrdb/secure
qrdb database info app --output json      # shows engine, backend, dataDir
```

This is fixed at creation. Changing it means a new database and a data
migration.

## Tenant isolation

You do not configure this. Tenant-scoped consumers are handed an engine already
wrapped by the namespace engine, which prefixes every key with the tenant and
filters listings to that prefix. It is one implementation over every backend
rather than three that could drift.

## Volume pools

A volume pool is a named set of local filesystem paths a database or tenant can
draw a dedicated store from. There is **no CLI** — use the SDK:

```go
pools := client.VolumePools()
if pools == nil {
	// this transport does not support volume pools
}

_ = pools.RegisterPool(ctx, transport.RegisterVolumePoolRequest{
	Name:  "fast-nvme",
	Paths: []string{"/mnt/nvme0", "/mnt/nvme1"},
})

backend, _ := pools.Pick(ctx, "fast-nvme", transport.VolumePoolHint{
	TenantID: "acme", SizeHint: 50 << 30,
})
// backend.LocalPath is a path on the serving node
```

**Pools are local-only, on purpose.** The consumer is PebbleDB and it needs a
real filesystem path, which a remote object store cannot give it. This is a
documented invariant rather than an unfinished feature.

## Troubleshooting

**The daemon refused `raft.sm_storage`.** Objstore is a snapshot and backup
tier and is refused as a hot apply path. Use `pebble`, `file` or `memory`. Note
also that `sm_storage` applies only to `sm_type: disk` and `fast`: every other
state machine carries its own storage and rejects a non-pebble `sm_storage`.

**Everything vanished after a restart.** The engine is `memory`. That is what it
does.

**`client.VolumePools()` panicked.** It returns **nil** when the transport does
not support volume pools — the nil is not an error value, so it has to be
checked before use.

**A volume pool path does not exist on the node you are on.**
`VolumePoolBackend.LocalPath` is a filesystem path on the *serving* node. It is
not portable.

**The server would not start, complaining about a directory.** Backing
directories are opened at startup, not lazily at first write, so a permissions
or path problem shows up immediately rather than at the first busy moment.

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
