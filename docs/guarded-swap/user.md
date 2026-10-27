# Guarded Swap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

Use `GuardedSwap` whenever a record must move from one state to another and more
than one writer might try at once.

## Prerequisites

- A Go program using the SDK, holding a connected `transport.Client` on any
  transport: the client is the `SwapPlane` the helper takes.
- A token whose `scp` grants, on the key, `transaction:execute` and the read
  permission of the transport: `kv:get` over REST, `mvcc:get_revision` over
  gRPC, Unix, QUIC, MCP and embedded ([api.md](api.md), "Transports and
  permissions"). A token that grants all three works on every transport.

## First run

Create a record only if it is absent, and confirm the write landed:

```go
res, err := sdk.GuardedSwap(ctx, client, sdk.GuardedSwapRequest{
	Key: []byte("hello"),
	Decide: func(current []byte, exists bool) ([]byte, sdk.SwapDecision, error) {
		if exists {
			return nil, sdk.SwapDone, nil
		}
		return []byte("world"), sdk.SwapWrite, nil
	},
})
if err != nil {
	return err
}
fmt.Println(res.Written, res.Revision, res.Attempts) // true <revision> 1
```

Run it twice: the second run prints `false 0 0`, because `Decide` saw the
record and chose `SwapDone`.

## Claiming something exactly once

The claim is a record that must not be taken twice. Losing the guard means
losing the claim — there is nothing to retry, because the thing being claimed is
gone.

```go
res, err := sdk.GuardedSwap(ctx, client, sdk.GuardedSwapRequest{
	Key: []byte("claim/task-1"),
	Decide: func(current []byte, exists bool) ([]byte, sdk.SwapDecision, error) {
		if exists {
			return nil, sdk.SwapLose, nil // someone else holds it
		}
		return []byte(myWorkerID), sdk.SwapWrite, nil
	},
})

var lost *sdk.SwapLostError
switch {
case err == nil && res.Written:
	// I own the task.
case errors.As(err, &lost):
	// Someone else does. This is a normal outcome, not a failure.
default:
	return err
}
```

## Holding a lease with a generation

The writer retries while the stored generation is below its own, and concedes
once a higher one is installed. Every writer terminates.

```go
_, err := sdk.GuardedSwap(ctx, client, sdk.GuardedSwapRequest{
	Key:         []byte("lease/resource-1"),
	MaxAttempts: 16,
	Decide: func(current []byte, exists bool) ([]byte, sdk.SwapDecision, error) {
		if exists {
			held, perr := strconv.Atoi(string(current))
			if perr != nil {
				return nil, sdk.SwapLose, perr
			}
			if held >= myGeneration {
				return nil, sdk.SwapLose, nil // a newer holder is installed
			}
		}
		return []byte(strconv.Itoa(myGeneration)), sdk.SwapWrite, nil
	},
})
```

## What to do with each error

| you get | it means | do |
|---|---|---|
| `nil`, `Written: true` | you wrote | proceed |
| `nil`, `Written: false` | `Decide` said the record was already right | proceed; nothing changed |
| `SwapLostError` | another writer won | expected for claims; handle as an outcome |
| `SwapContendedError` | too many writers for your bound | rarely fix by raising `MaxAttempts` — a record this contended usually wants partitioning |
| `SwapStalledError` | **a fault in the store or your guard choice** | investigate; do not retry. Try `GuardOnValue` if the plane lacks `CompareModRevision` |
| `SwapConfigError` | your request is unusable | fix the call |

## Troubleshooting

| symptom | cause | fix |
|---|---|---|
| a permission-denied error from the first call | the token lacks the read permission: `kv:get` over REST (a `403` on `GET /kv/{key}?revision=true`), `mvcc:get_revision` on every other transport | grant the one the transport checks |
| a permission-denied error after `Decide` returned `SwapWrite` | the token lacks `transaction:execute` | grant it |
| `SwapStalledError` on every call | the store's transaction plane does not evaluate `CompareModRevision` | set `GuardOnValue`, if the record's value is monotonic |
| every swap of an absent key fails with an error | the transport reports a missing key as something other than `*transport.KeyNotFoundError` or a nil record | a transport defect ([api.md](api.md), "Transport requirements") |
| `Written: true` with `Revision: 0` | the transport does not propagate `TransactionResponse.Revision` | a transport defect ([api.md](api.md), "Transport requirements") |

## Two things to get right

**`Decide` may be called several times, and being called is not a promise that
you will write.** Do not perform side effects in it.

**Leave `GuardOnValue` alone unless you must set it.** The default guards on the
record's revision, which is safe for any record. The value guard is only safe
for records whose value moves monotonically — see `architecture.md`.
