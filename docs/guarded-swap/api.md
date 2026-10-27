# Guarded Swap — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

## Entry point

```go
func GuardedSwap(
	ctx context.Context,
	client SwapPlane,
	req GuardedSwapRequest,
	opts ...transport.WriteOption,
) (GuardedSwapResult, error)
```

Reached through the SDK: `sdk.GuardedSwap(ctx, client, req)` — a
`transport.Client` satisfies `SwapPlane` as it stands.

```go
type SwapPlane interface {
	transport.TransactionClient
	GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error)
}
```

Two capabilities rather than one because the transaction plane cannot read;
see [architecture.md](architecture.md).

## Transports and permissions

`GuardedSwap` is a Go SDK helper, not an RPC: no transport carries a guarded
swap as one call. Each attempt is the two calls below, made on whichever
transport the `SwapPlane` uses, and the caller's token needs both permissions.

| call | REST | gRPC / Unix | QUIC | MCP tool | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| read the record | `GET /kv/{key}?revision=true` | `GetRevision` | `mvcc.GetRevision` | `qrdb_mvcc_get_revision` | `Client.MVCC().GetRevision` | REST: `kv:get`; every other transport: `mvcc:get_revision` |
| propose the guarded write | `POST /tx` | `Transaction` | `tx.Transaction` | `qrdb_tx_execute` | `Client.Transaction().ExecuteTransaction` | `transaction:execute` |

The read's permission differs on REST. `GET /kv/{key}?revision=true` is the
`GET /kv/{key}` route, which REST authorizes with `kv:get`; the `revision`
query parameter changes the response, not the authorization. A REST caller
granted `mvcc:get_revision` alone is refused with `403` on the read. A token
meant to work on every transport grants `kv:get`, `mvcc:get_revision` and
`transaction:execute`.

## Request

```go
type GuardedSwapRequest struct {
	Key         []byte  // required
	Decide      Decide  // required
	MaxAttempts int     // 0 = DefaultSwapAttempts (8); negative = SwapConfigError
	GuardOnValue bool   // false (default) guards on ModRevision
}

type Decide func(current []byte, exists bool) (write []byte, decision SwapDecision, err error)
```

`exists` distinguishes an absent key from one holding an empty value; `current`
is nil when `exists` is false.

| decision | effect |
|---|---|
| `SwapWrite` | propose the returned bytes under the guard |
| `SwapDone` | stop, report success, write nothing |
| `SwapLose` | stop, report `SwapLostError`, write nothing |

An error returned by `Decide` stops the loop and is passed through **unwrapped**,
so a decoder failure reports itself rather than being reported as contention.

## Result

```go
type GuardedSwapResult struct {
	Written  bool  // false means Decide chose SwapDone
	Revision int64 // the MVCC revision the write landed at, 0 when nothing was written
	Attempts int   // guards proposed, including the winning one
}
```

## Errors

All are matched with `errors.As`. **None is returned when a write landed.**

| type | meaning | is it a fault? |
|---|---|---|
| `SwapLostError` | `Decide` conceded the record | no — the ordinary path for a no-retry consumer |
| `SwapContendedError` | bound reached, record moved every time | no — genuine contention |
| `SwapStalledError` | guard refused, record did NOT move | **yes** — the store is not honouring the guard, or does not evaluate the compare target |
| `SwapConfigError` | unusable request: nil client, no key, nil `Decide`, negative `MaxAttempts`, or an unknown `SwapDecision` | yes — a caller programming error |

These four are built in the SDK and never cross a wire. Any other error is one
of the two underlying calls failing (a refused permission, a sealed store, an
unreachable leader), and it is returned unchanged. Its REST status and gRPC
code are those of the call that failed:
[../key-value/api.md](../key-value/api.md) "Status and error mapping" for the
read, [../transactions/api.md](../transactions/api.md) "Status and error
mapping" for the write. A refused guard is not an error on the wire: the
transaction answers success with `Succeeded: false`, and the loop re-reads.

## Raw-byte compare-and-swap

Beside `GuardedSwap`, the storage layer offers a raw-byte compare-and-swap,
`CompareAndSwap(ctx, key, expected, next) (bool, error)`. It is honoured on every engine (Memory, File, Pebble, ObjStore)
and by the tenant-routed store, the per-database store and the barrier-wrapped
(sealed-window) store. A losing swap, whether a mismatch or an absent key,
reports `false` with a nil error and never creates the key; inside the sealed
window the call fails closed with the typed sealed error instead of reporting
`false`. Consumers holding a storage engine directly assert for the optional
`GuardedStorage` interface ([../storage-engines/api.md](../storage-engines/api.md)).

## Transport requirements

The client must implement `SwapPlane` — both the transaction execution and the
revision-carrying read — and the store's transaction plane must evaluate
`CompareModRevision`, or the caller must set `GuardOnValue` and accept the ABA
caveat in `architecture.md`.

`GetRevision` must report a missing key as `*transport.KeyNotFoundError` or as
a nil record. A transport that reports absence as some other error makes every
absent-record swap fail instead of creating the record.

A transport that does not propagate `TransactionResponse.Revision` reports
`Revision: 0` on success; a transport copies the field on **every** response
path.
