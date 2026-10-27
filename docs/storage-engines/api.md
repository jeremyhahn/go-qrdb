# Storage Engines — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

## Storage engines

**No wire API.** `pkg/storage.Storage` is an in-process interface, not a
capability. Every data-plane operation in every other feature is served *over*
an engine; none of them expose the engine itself.

What is observable over the wire is a database's configured backend, through
[../databases/api.md](../databases/api.md): `DatabaseInfo.Backend`
(`pebble | memory | file | raft`) and `DatabaseInfo.DataDir`. A per-database
`objstore` backend passes config validation but is refused when the database
opens (`database.InvalidBackendError`); `objstore` serves only as the
standalone node's `storage.backend`.

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

## Volume pools

The volume pool surface is documented in
[../volume-pools/api.md](../volume-pools/api.md).

## The closed-store condition on the wire

The engine has no wire API, but the condition "this node closed its store under
your request" reaches a caller through whatever data-plane transport it used.
What crosses depends on the transport:

| Transport | What the caller receives |
|---|---|
| `embedded` | the value: `errors.Is(err, sdk.ErrStorageClosed)` holds, and `errors.As` reaches `*sdk.StorageClosedError` |
| `grpc`, `unix` | the classification: `codes.Unavailable`, on every RPC |
| `rest` | the classification: HTTP `503`, on every verb — `GET /kv/{key}`, `GET /kv` (list), `/kv/scan`, `PUT` (plain and lease), `DELETE`, `POST /batch` |
| `quic` | the classification: `StatusUnavailable`, on every frame |
| `mcp` | the classification: `UNAVAILABLE` (−32006), on every tool |

**Only `embedded` preserves the typed value**, because only `embedded` never
serializes it. The others carry a status code, which is what a wire can carry;
a remote caller reads the code and must not parse the message.

All four remote transports report the same retryable family — `503`,
`codes.Unavailable`, `StatusUnavailable`, `UNAVAILABLE` — so a client that
backs off and retries on one does it on all of them. See
[the API server's status mapping](../api-server/api.md#common-status-mapping)
for the whole table.

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
| Any engine operation after `Close` | error matching `ErrStorageClosed`, never a panic |
| Any operation on an encrypted store while its barrier is sealed | the barrier's sealed error (`503` / `UNAVAILABLE`; `sdk.ErrSealed` on `embedded`) |

## Versioning

Not applicable — there are no versioned routes in this feature.

## Not built

An object-storage LSM segment layout for `objstore` is not implemented; this
API describes the shipped engines only.
