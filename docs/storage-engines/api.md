# Storage Engines and Volume Pools — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Storage engines

**No wire API.** `pkg/storage.Storage` is an in-process interface, not a
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

## The closed-store condition on the wire

The engine has no wire API, but the condition "this node closed its store under
your request" reaches a caller through whatever data-plane transport it used.
What crosses depends on the transport:

| Transport | What the caller receives |
|---|---|
| `embedded` | the value: `errors.Is(err, sdk.ErrStorageClosed)` holds, and `errors.As` reaches `*sdk.StorageClosedError` |
| `grpc`, `unix` | the classification: `codes.Unavailable`, on every RPC |
| `rest` | the classification: HTTP `503`, on every verb — `GET /kv/{key}`, `/kv/list`, `/kv/scan`, `PUT` (plain and lease), `DELETE`, `POST /batch` |
| `quic`, `mcp` | a generic request failure — **the classification is not carried yet** |

**Only `embedded` preserves the typed value**, because only `embedded` never
serializes it. The others carry a status code, which is what a wire can carry;
a remote caller reads the code and must not parse the message. The `quic` and
`mcp` gaps are recorded here rather than left to be discovered — see
[engineering.md](engineering.md).

## Error semantics

| Condition | Behavior |
|---|---|
| Unknown pool name on `Pick` | not-found |
| Transport without `VolumePoolProvider` | `Client.VolumePools()` returns nil |
| Non-local backend registered | refused — pools are `LocalOnly` |
| Any engine operation after `Close` | error matching `ErrStorageClosed`, never a panic |

## Versioning

Not applicable — there are no versioned routes in this feature.
