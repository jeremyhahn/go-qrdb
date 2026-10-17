# Guarded Swap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-22
> Conflicts with this document are defects. Fix them here first.

Use `GuardedSwap` whenever a record must move from one state to another and more
than one writer might try at once.

## Claiming something exactly once

The claim is a record that must not be taken twice. Losing the guard means
losing the claim — there is nothing to retry, because the thing being claimed is
gone.

```go
res, err := sdk.GuardedSwap(ctx, client, sdk.GuardedSwapRequest{
	Key: []byte("__sys:claim/task-1"),
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
	Key:         []byte("__sys:lease/resource-1"),
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

## Two things to get right

**`Decide` may be called several times, and being called is not a promise that
you will write.** Do not perform side effects in it.

**Leave `GuardOnValue` alone unless you must set it.** The default guards on the
record's revision, which is safe for any record. The value guard is only safe
for records whose value moves monotonically — see `architecture.md`.
