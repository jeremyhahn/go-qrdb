<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Coordination — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

**The coordination surface is an SDK-level composition, not a wire protocol.**
It adds no endpoint, no RPC, no QUIC method, no MCP tool and no route to any
transport. Each of its five verbs is one or two calls on verbs the transports
already carry, so it is available over **all six transports** without any of
them knowing it exists.

That is why the matrix below shows, per coordination verb, **which existing
wire capability it issues on each transport**. Nothing in this feature is
`MISSING` on any transport: every shipped transport implements all four
sub-client interfaces the `Coordinator` composes, and each carries a
compile-time assertion to that effect.

## Entry points

```go
// From a connected client — the usual path.
func (c *Client) Coordination() (*Coordinator, error)

// From sub-clients you composed yourself.
func NewCoordinator(
	kv transport.KVClient,
	lease transport.LeaseClient,
	txn transport.TransactionClient,
	watch transport.WatchClient,
) (*Coordinator, error)
```

```go
type CoordinationClient interface {
	AcquireLease(ctx context.Context, key string, ttl time.Duration) (Lease, error)
	CompareAndSwap(ctx context.Context, key string, expect, set []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, val []byte) error
	Watch(ctx context.Context, prefix string) (WatchStream, error)
}
```

`Coordinator` is the only implementation. `CoordinationClient` exists so a
consumer can substitute a fake in its own tests.

## Capability matrix

Transports: **embedded** (in-process), **Unix socket** (the gRPC wire over a
UDS, so its methods match the gRPC column), **REST**, **gRPC**, **QUIC**,
**MCP**.

| Coordination verb | Wire capability it issues | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|---|
| `Get` | KV get | `GET /kv/{key}` | `Get` | `kv.Get` | `qrdb_get` | `Client.KV().Get` | `kv:get` |
| `Put` | KV put | `PUT /kv/{key}` | `Put` | `kv.Put` | `qrdb_put` | `Client.KV().Put` | `kv:put` |
| `CompareAndSwap` | **one transaction** | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |
| `AcquireLease` — grant | lease grant | `POST /lease/grant` | `LeaseGrant` | `lease.Grant` | `qrdb_lease_grant` | `Client.Lease().LeaseGrant` | `lease:grant` |
| `AcquireLease` — exclusive bind | **one transaction**, put with lease attached | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |
| `AcquireLease` — cleanup on a lost race | lease revoke | `POST /lease/revoke` | `LeaseRevoke` | `lease.Revoke` | `qrdb_lease_revoke` | `Client.Lease().LeaseRevoke` | `lease:revoke` |
| `Lease.Renew` | lease keep-alive | `POST /lease/keepalive` | `LeaseKeepAlive` | `lease.KeepAlive` | `qrdb_lease_keepalive` | `Client.Lease().LeaseKeepAlive` | `lease:keepalive` |
| `Lease.Release` | lease revoke | `POST /lease/revoke` | `LeaseRevoke` | `lease.Revoke` | `qrdb_lease_revoke` | `Client.Lease().LeaseRevoke` | `lease:revoke` |
| `Lease.Done` — liveness observer | lease time-to-live | `GET /lease/ttl` | `LeaseTimeToLive` | `lease.TimeToLive` | `qrdb_lease_ttl` | `Client.Lease().LeaseTimeToLive` | `lease:ttl` |
| `Watch` | watch a prefix | `GET /watch?key=<p>&prefix=true` | `Watch` (server-streaming, prefix field) | `watch.WatchPrefix` | `qrdb_watch` | `Client.Watch().WatchPrefix` | `watch:watch_prefix` |

**`CompareAndSwap` uses the transaction plane, not the KV guarded-write
verbs.** `POST /kv/compare-and-swap` (`kv.CompareAndSwap`) is a different
capability with different semantics; this surface does not use it. See
[../key-value/api.md](../key-value/api.md).

### Permissions a coordination consumer needs

The surface adds no permission of its own and cannot widen the caller's
credential. A consumer using the whole surface needs, at minimum:

```
kv:get  kv:put  transaction:execute
lease:grant  lease:keepalive  lease:revoke  lease:ttl
watch:watch_prefix
```

A consumer that uses only `Get`, `Put` and `CompareAndSwap` needs only
`kv:get`, `kv:put` and `transaction:execute`.

**`lease:ttl` is not optional for a lease holder.** The liveness observer calls
it on every poll. Without it the polls fail, which proves nothing about the
lease and therefore does not close `Done` early — but it removes the only path
by which a holder learns of a loss *before* its deadline, so every loss is
learned at the deadline instead. **`lease:revoke` is likewise not optional**:
without it `Release` cannot succeed and the cleanup of a lost acquisition race
leaks a grant until it expires.

## Requests and responses

The surface's own types are Go values, not wire schemas:

```go
type Event struct {
	Type      EventType   // EventPut | EventDelete
	Key       string
	Value     []byte      // the new value on a put; nil on a delete
	PrevValue []byte      // best-effort; see below
	Revision  int64       // the MVCC revision the change committed at
}
```

`EventType` is a type alias for `transport.EventType`, so the two can never
drift.

Mapping from the wire event: `Key` and `Revision` come from the event's `kv`
(its `mod_revision`); `Value` is populated on a put only; `PrevValue` comes
from `prev_kv` where present, and `prev_kv`'s key is used when the event
carried no `kv` key.

**`Coordinator.Watch` sends `WatchOptions{}`** — `start_revision` 0, `prev_kv`
false, `progress_notify` false. So there is **no replay** and `PrevValue` is
populated only if the server sends a previous value unasked. A consumer needing
either uses [../watch/api.md](../watch/api.md) directly.

For the underlying wire schemas — `LeaseInfo`, `TransactionRequest` /
`TransactionResponse`, the watch event envelope — see
[../leases/api.md](../leases/api.md), [../transactions/api.md](../transactions/api.md)
and [../watch/api.md](../watch/api.md).

## Status and error mapping

Transport-level statuses are the underlying capability's, unchanged:

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| CAS or bind precondition not met | 200, `succeeded: false` | `OK`, `succeeded: false` |
| Key not found on a get | 404 | `NOT_FOUND` |
| Lease not found (expired, revoked, never existed) | 404 | `NOT_FOUND` |
| `ttl <= 0` on grant | 400 | `INVALID_ARGUMENT` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Barrier sealed | 503 | `UNAVAILABLE` |
| Not leader, no resolver | 503 | `UNAVAILABLE` |
| Lease on a backend that keeps none (standalone file/objstore) | 501 | `UNIMPLEMENTED` |

What the coordination layer does with them:

| Wire condition | What the caller sees |
|---|---|
| Key not found, from `Get` | `(nil, nil)` — **translated, never surfaced** |
| Precondition not met, from `CompareAndSwap` | `(false, nil)` — an outcome, not an error |
| Precondition not met, from the acquire bind | `*LeaseHeldError{Key}` (and the grant is revoked) |
| Lease not found / expired, from `Release`'s revoke | `nil` — the binding is already gone, which is what was asked for |
| Lease not found / expired, from a liveness poll | `Done` closes; loss is proven |
| Keep-alive accepted after the ownership deadline | `*LeaseLostError{Key}` — **not** `nil` |
| Any other transport error | returned unchanged |

Full semantics, including which paths fail closed, are in
[engineering.md](engineering.md).

## Run-mode availability

Every verb is served in both run modes. The storage backend bounds the lease:

| Verb | Standalone, pebble/memory | Standalone, file/objstore | Raft |
|---|---|---|---|
| `Get`, `Put`, `CompareAndSwap` | serves | serves | serves |
| `AcquireLease` | serves | typed refusal | serves |
| `Watch` | serves | serves | serves |

Discover it before you depend on it, on any transport:

| | |
|---|---|
| REST | `GET /capabilities` |
| gRPC / Unix | `Capabilities` |
| QUIC | `admin.Capabilities` |
| MCP | `qrdb_capabilities` |
| Embedded | `Client.Admin().Capabilities(ctx)` |
| CLI | `qrdb admin capabilities` |

The permission is `admin:capabilities`.

The response carries `mode` (`"standalone"` or `"raft"`) plus per-family
booleans; `lease`, `watch`, `mvcc` and `kv` are true in both modes. The
capability report is mode-level: a standalone node on the file or objstore
backend reports `lease` true and refuses it per call.

The refusal is `*transport.CapabilityUnsupportedError` on every transport,
the embedded one included.

## Pagination

None of these verbs returns an unbounded result set. `Get` returns one value;
`CompareAndSwap` returns one boolean; `AcquireLease` returns one handle;
`Watch` is a stream with no pagination and back-pressure semantics described in
[engineering.md](engineering.md).

## Versioning and deprecation

The surface introduces no route of its own, so it has no versioning state of
its own. The routes it uses are the unversioned KV, transaction, lease and
watch paths documented in their own folders. Nothing here is deprecated.
