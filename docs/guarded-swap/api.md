# Guarded Swap — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-22
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
| `SwapConfigError` | unusable request: no key, nil `Decide`, negative `MaxAttempts` | yes — a caller programming error |

## Raw-byte compare-and-swap

The primitive underneath `Swap` is `CompareAndSwap(ctx, key, expected, next)
(bool, error)`. It is honoured on every engine (Memory, File, Pebble, ObjStore)
and by the tenant-routed store, the per-database store and the barrier-wrapped
(sealed-window) store. A losing swap, whether a mismatch or an absent key,
reports `false` with a nil error and never creates the key; inside the sealed
window the call fails closed with the typed sealed error instead of reporting
`false`. Consumers holding a storage engine directly assert for the optional
`GuardedStorage` interface (`docs/storage-engines/api.md`).

## Transport requirements

The client must implement `SwapPlane` — both the transaction execution and the
revision-carrying read — and the store's transaction plane must evaluate
`CompareModRevision`, or the caller must set `GuardOnValue` and accept the ABA
caveat in `architecture.md`.

`GetRevision` must report a missing key as `*transport.KeyNotFoundError` or as
a nil record. A transport that reports absence as some other error makes every
absent-record swap fail instead of creating the record.

A transport that does not propagate `TransactionResponse.Revision` reports
`Revision: 0` on success. The embedded transport did exactly that until this
helper landed and the omission surfaced; if you are writing a new transport,
copy the field on **every** response path.
