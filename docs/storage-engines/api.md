# Storage Engines and Volume Pools — API

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the "Target design"
> note below.** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Storage engines

**No wire API.** `pkg/storage.Storage` is an in-process interface, not a
capability. Every data-plane operation in every other feature is served *over*
an engine; none of them expose the engine itself.

What is observable over the wire is a database's configured backend, through
[../databases/api.md](../databases/api.md): `DatabaseInfo.Backend`
(`pebble | memory | file | raft`) and `DatabaseInfo.DataDir`.

### Optional `GuardedStorage`

Beside `Storage`, `pkg/storage` declares an optional interface (re-exported as
`GuardedStorage` in the SDK) that a backend implements to offer an atomic
compare-and-swap on raw bytes:

```go
CompareAndSwap(ctx context.Context, key, expected, next []byte) (swapped bool, err error)
```

Memory, File, Pebble and ObjStore all implement it; discover it by type
assertion, as for `Syncer`. `Storage` itself is unchanged. A swap replaces the
value only when it still equals `expected`; a mismatch or an absent key is
`false` with a nil error (an absent key is never created); an error means the
outcome is unknown. The compare and the write happen under one lock that `Set`,
`Delete` and batch commits also respect, so a concurrent writer cannot land
between them. On the File engine that is the cross-process write lock, so the
guarantee holds across processes sharing the directory. Memory, Pebble and
ObjStore hold an in-process lock only; across processes sharing an ObjStore
bucket the guarantee does not hold.

Two optional in-process seams sit beside `Storage`: `storage.Syncer` (flush on
demand) and `storage.Exclusive` (`Exclusive(fn func(storage.ReadWriter) error)
error`), implemented by the `file` engine. `Exclusive` runs `fn` holding the
engine's cross-process write lock, so a read-check-write cannot be interleaved
by any writer on the same directory in this process or another; `fn` writes
through the `ReadWriter` it is handed, never through the engine. Errors from
the lock are `*filelock.Error` (`pkg/util/filelock`); a closed engine returns
`*storage.ClosedError`.

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
| `quic` | the classification: `StatusUnavailable`, on every frame |
| `mcp` | the classification: `UNAVAILABLE` (−32006), on every tool |

**Only `embedded` preserves the typed value**, because only `embedded` never
serializes it. The others carry a status code, which is what a wire can carry;
a remote caller reads the code and must not parse the message.

Both remote rows used to read "a generic tool failure — the classification is
not carried yet". They carry it now, each once its transport gained a
vocabulary wide enough to say it: `quic` maps the closed store to
`StatusUnavailable`, and `mcp` to `UNAVAILABLE` (−32006) in the error envelope
on its tool result. All four remote transports therefore report the same
retryable family — `503`, `codes.Unavailable`, `StatusUnavailable`,
`UNAVAILABLE` — so a client that backs off and retries on one does it on all of
them. See
[the API server's status mapping](../api-server/api.md#common-status-mapping)
for the whole table.

The two arrived by the same route and are worth telling apart from a fix in the
handlers: nothing about how the closed store is DETECTED changed. The condition
was always classified `Unavailable` by the service-error mapper; what was
missing was somewhere on the wire to put the answer.

## At rest: what a caller can observe

At-rest encryption has no request of its own; the per-backend guarantee is in
[architecture.md](architecture.md#at-rest-encryption-one-mechanism-for-every-on-disk-backend).
Two things reach a caller:

- **A store whose barrier is sealed** answers every data-plane request with
  the barrier's sealed error: `503` on REST, `UNAVAILABLE` on gRPC, QUIC and
  MCP, and on `embedded` the value itself (`errors.Is(err, sdk.ErrSealed)`).
  It is the same answer for
  `pebble` and `file`; see
  [../barrier-seal/api.md](../barrier-seal/api.md).
- **The capability report's `unencryptedAtRest` list** (`unencrypted_at_rest`
  in the protobuf) names each store that keeps data outside the barrier while
  the seal is on. A standalone `storage.backend: objstore` is the one entry
  this feature can produce; `pebble` and `file` never appear in it.

## Error semantics

| Condition | Behavior |
|---|---|
| Unknown pool name on `Pick` | not-found |
| Transport without `VolumePoolProvider` | `Client.VolumePools()` returns nil |
| Non-local backend registered | refused — pools are `LocalOnly` |
| Any engine operation after `Close` | error matching `ErrStorageClosed`, never a panic |
| Any operation on an encrypted store while its barrier is sealed | the barrier's sealed error (`503` / `UNAVAILABLE`; `sdk.ErrSealed` on `embedded`) |

## Versioning

Not applicable — there are no versioned routes in this feature.

## Target design: no caller-visible change

See [architecture.md](architecture.md#target-design-object-storage-lsm-segment-layout)
for the full design. From every wire transport's point of view, nothing here
changes:

- The backend enum stays `objstore` — `DatabaseInfo.Backend` does not gain or
  rename a value. The LSM segment layout is an internal rebuild of what
  `backend: objstore` means, not a new caller-visible option.
- There is still no wire API for storage engines (this feature has none
  today, per "Storage engines" above), and this design does not add one.
- **One normative statement this design may eventually reverse**: today
  `raft.sm_storage`/`state_storage: objstore` is refused outright (see
  [engineering.md](engineering.md)'s error table). Phase 1 of the target
  design does not touch that refusal — it only unblocks `backend: objstore`
  as a local, non-replicated binding. Lifting the Raft hot-apply-path refusal
  is OS-7, deferred to a gated phase 3, and is called out here so a reader of
  this API doc is not surprised by it later.
