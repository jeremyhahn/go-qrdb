# Storage Engines and Volume Pools — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Storage engines

**No wire API.** `pkg/storage.Engine` is an in-process interface, not a
capability. Every data-plane operation in every other feature is served *over*
an engine; none of them expose the engine itself.

What is observable over the wire is a database's configured backend, through
[../databases/api.md](../databases/api.md): `DatabaseInfo.Backend`
(`pebble | memory | file | raft`) and `DatabaseInfo.DataDir`.

The one cross-process form of the interface is the `qrdb` storage engine
(`storage.engine: qrdb`), which reaches a remote node through the SDK client
rather than a local store. go-xkms uses the same pattern from its side.

## Volume pools

| Capability | REST | gRPC / Unix | QUIC | MCP | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| List pools | MISSING | MISSING | MISSING | MISSING | `Client.VolumePools().List` | none defined |
| Pick a backend | MISSING | MISSING | MISSING | MISSING | `Client.VolumePools().Pick` | none defined |
| Register a pool | MISSING | MISSING | MISSING | MISSING | `Client.VolumePools().RegisterPool` | none defined |

**The volume pool surface is Tier 2 — composition-plane, in-process only.** It
has an `sdk/go/transport` client interface (`VolumePoolClient`) and a provider
probe (`VolumePoolProvider`), but no REST route, gRPC RPC, QUIC method or MCP
tool, and no permission constants.

That is consistent with what volume pools are: `VolumePoolBackend.LocalPath` is
a filesystem path on the serving node, so a remote caller could not act on the
answer. Recorded as `MISSING` per the feature-doc convention rather than
omitted, so a reader can see the shape of the surface rather than infer it.

`Client.VolumePools()` returns **nil** when the active transport does not
implement `VolumePoolProvider`. Callers must nil-check; the nil is not an error
value.

## Schemas

```json
// VolumePool
{ "name": "fast-nvme", "strategy": "round-robin",
  "backendCount": 2, "localPaths": ["/mnt/nvme0", "/mnt/nvme1"] }

// VolumePoolHint  (input to Pick)
{ "tenantID": "acme", "sizeHint": 53687091200, "region": "us-east" }

// VolumePoolBackend  (output of Pick)
{ "poolName": "fast-nvme", "localPath": "/mnt/nvme0" }

// RegisterVolumePoolRequest
{ "name": "fast-nvme", "paths": ["/mnt/nvme0", "/mnt/nvme1"], "strategy": "round-robin" }
```

## Error semantics

| Condition | Behavior |
|---|---|
| Unknown pool name on `Pick` | not-found |
| Transport without `VolumePoolProvider` | `Client.VolumePools()` returns nil |
| Non-local backend registered | refused — pools are `LocalOnly` |

## Versioning

Not applicable — there are no versioned routes in this feature.
