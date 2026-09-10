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

# Coordination — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

Use this surface when several copies of your program must agree on something:
which one is in charge, what the current configuration is, or who owns a piece
of work.

## Before you start: which half can this node serve?

Two of the five verbs need consensus and two do not. **Check, do not
discover it at an election.**

```bash
qrdb admin capabilities --output json
```

| `mode` | `Get`, `Put`, `CompareAndSwap` | `AcquireLease`, `Watch` |
|---|---|---|
| `raft` | yes | yes |
| `standalone` | yes | **refused** |

On a standalone node `AcquireLease` and `Watch` return an error saying the
family is not configured, with the remedy `requires mode: raft`. They do not
half-work — no lease that never expires, no watch that never fires — because a
primitive that silently does nothing is worse than one that says no.

If you need leadership or change notification, run against a raft node. If your
program must run in both, design leadership as an optional capability and use
compare-and-swap for the rest.

## Getting the surface

```go
coord, err := client.Coordination()
if err != nil {
	return err
}
```

Construction succeeds in **both** modes — it tells you nothing about what the
node will serve. That is what the capability check above is for.

## Task 1 — elect exactly one leader

Acquiring is exclusive. The first caller gets the key; every other caller gets
`LeaseHeldError` until the holder releases it or its lease expires.

```go
lease, err := coord.AcquireLease(ctx, "service/indexer/leader", 30*time.Second)

var held *sdk.LeaseHeldError
switch {
case err == nil:
	// I am the leader.
case errors.As(err, &held):
	// Someone else is. This is a normal outcome, not a failure —
	// wait and try again.
	return nil
default:
	return err
}
defer func() {
	// Release on a context of its OWN. By the time you unwind, the caller's
	// context may already be cancelled — and detaching from it must not mean
	// detaching from every deadline, or an unresponsive server holds your
	// shutdown open indefinitely.
	rel, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = lease.Release(rel)
}()
```

### Stop working the moment you stop leading

`lease.Done()` closes when you no longer safely hold the key — because it
expired, or because you released it. **Wire it to whatever must stop.**

```go
work, stopWork := context.WithCancel(ctx)
defer stopWork()
go func() {
	<-lease.Done()
	stopWork() // ownership is over; abandon the work
}()
```

This is the whole reason the handle exists. A leader that keeps working after
`Done` closes is the split-brain case this surface is built to make detectable.

### Keep renewing, or you will lose it

Nothing renews for you. Reading or writing the key does not renew it either.

```go
ticker := time.NewTicker(10 * time.Second) // a third of the 30s TTL
defer ticker.Stop()
for {
	select {
	case <-work.Done():
		return nil // lost it, or shutting down
	case <-ticker.C:
		if err := renewWithRetry(ctx, lease, 3); err != nil {
			return err
		}
	}
}
```

```go
// renewWithRetry retries a failed keepalive AT ONCE rather than waiting out the
// interval. A failure confirms nothing and changes nothing — you still hold the
// key until your deadline — so the worst thing to do is sleep on it.
func renewWithRetry(ctx context.Context, l sdk.Lease, attempts int) error {
	var err error
	for range attempts {
		if err = l.Renew(ctx); err == nil {
			return nil
		}
		var lost *sdk.LeaseLostError
		if errors.As(err, &lost) {
			return err // ownership is over; Done has already closed
		}
		select {
		case <-l.Done():
			return err
		case <-time.After(200 * time.Millisecond):
		}
	}
	return err
}
```

## Task 2 — choose the TTL and the renewal interval

This is the decision people get wrong, so here is the reasoning rather than a
rule of thumb.

Your lease has a **deadline**. It moves only when a renewal is *acknowledged*,
and it is measured from when that renewal was **sent**. So the time that has to
fit inside the TTL is not your sleep — it is:

```
    the previous renewal's round trip
  + your sleep
  + however late your timer actually fired
  + this renewal's round trip
```

**Both round trips are in the budget.** A keepalive is a consensus proposal;
measure it on your own cluster rather than assuming it is small.

Renewals also *fail*. What you are really choosing the interval for is **how
many consecutive failed renewals you can absorb before the key is gone**. With
a 30-second TTL, a 250 ms round trip and 100 ms of timer slack:

| Renew every | Failed renewals you can absorb | Keepalives per lease lifetime |
|---|---|---|
| 15 s | **0** — one dropped renewal loses the key | 2 |
| 10 s | 1 | 3 |
| 5 s | 4 | 6 |
| 3 s | 7 | 10 |
| 2 s | 11 | 15 |

**Start at a third of the TTL.** Go shorter when losing the key mid-work is
expensive; the cost is linear in traffic and each renewal is a consensus round
trip.

Two more things about the TTL itself:

- It is **rounded up to whole seconds**, with a floor of one second. Asking for
  1500 ms grants 2 s; asking for 10 ms grants 1 s.
- **Do not go below about five seconds.** A 1-second lease leaves under half a
  second of renewal budget once two round trips are paid, with no tolerance for
  a single failure.
- **Expiry deletes the key.** The TTL is a durability parameter, not a hint.

## Task 3 — claim something exactly once (works in either mode)

`CompareAndSwap` needs no consensus, so this is the pattern to reach for when
your program must also run standalone.

```go
// Claim: create only if absent. The first caller wins.
ok, err := coord.CompareAndSwap(ctx, "claim/task-1", nil, []byte(myWorkerID))
if err != nil {
	return err
}
if !ok {
	// Someone else claimed it. A normal outcome, not an error.
	return nil
}
```

```go
// Advance: swap only if the key exists AND still holds what you expect.
ok, err := coord.CompareAndSwap(ctx, "state/pipeline", []byte("staged"), []byte("live"))
```

Two things to know:

- **`ok == false` is not an error.** It means the precondition lost, which is
  the entire point. Handle it as an outcome; `err` is nil.
- **A nil `expect` and an empty `expect` are different.** `nil` means "create
  only if absent". An empty (but non-nil) slice means "the key exists and its
  value is empty". Passing an empty slice will *not* create the key.

Use this to **fence** your writes, too. If the state a leader protects lives in
qrdb, make every write a `CompareAndSwap` rather than a `Put`: a stale leader's
write is then refused by the store instead of by convention.

## Task 4 — watch a prefix for changes

```go
watchCtx, stopWatch := context.WithCancel(ctx)
defer stopWatch() // ALWAYS: this is what ends the watch

w, err := coord.Watch(watchCtx, "fleet/")
if err != nil {
	return err
}

for ev := range w.Events() {
	switch ev.Type {
	case sdk.EventPut:
		apply(ev.Key, ev.Value) // ev.Revision is the revision it committed at
	case sdk.EventDelete:
		forget(ev.Key) // ev.Value is nil on a delete
	}
}
```

### Then find out why it ended — this part is not optional

The channel closing means the watch is over. It does not say **why**, and the
three reasons need different responses.

```go
switch err := w.Err(); {
case err == nil:
	// The server closed the stream cleanly. Nothing was missed.
	// Re-establish if you still want a watch.
case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	// You ended it. Orderly shutdown; nothing to do.
default:
	// THE SERVER ended it. You may have missed changes.
	// Reconcile against current state with Get before you resume —
	// do NOT just re-establish and carry on.
}
```

The realistic reason for that last branch is that **you fell behind**. The
server will never block its commit path waiting for a slow watcher; it drops
one instead. So a consumer that reads slowly causes its own disconnection.

**Keep the loop fast.** The event channel is unbuffered — do the slow work
somewhere else:

```go
for ev := range w.Events() {
	select {
	case myQueue <- ev: // hand off immediately
	case <-watchCtx.Done():
		return
	}
}
```

Two limits to plan around:

- **The watch is live-only.** There is no way to resume from a revision through
  this surface, so "reconcile with `Get`" is the recovery, not "replay".
- **`ev.PrevValue` is best effort.** This surface does not ask the server for
  previous values, so do not build on it.

## Troubleshooting

| What you see | What it means | What to do |
|---|---|---|
| `LeaseHeldError` from `AcquireLease` | someone else holds the key | you are a follower — back off and retry; not a failure |
| `LeaseHeldError` **forever**, and nobody is alive to hold it | expired leases are not being swept on this deployment | this is a server-side composition problem, not a client one — the leader's expiry sweep must be running; see [../leases/](../leases/) |
| `LeaseLostError` from `Renew` | the keepalive was accepted, but **too late** — your deadline had already passed | stop working. `Done` is already closed. Do **not** treat it as success. Then shorten your renewal interval — this is the symptom of a cadence that is too slow |
| `Renew` returns a timeout / connection error | nothing was confirmed and nothing changed; you still hold the key until your deadline | retry **immediately**, do not wait out the interval |
| `Done` closed and you have no idea why | either the deadline passed without an acknowledged renewal, or the server said the lease is gone | fence first, diagnose second. Check renewal round-trip times against the TTL |
| `Done` closes while you were renewing on time | the round trips, not your sleep, ate the budget — or the host stalled past the TTL | measure the keepalive round trip; if it is large, lengthen the TTL or shorten the interval |
| `Release` returns an error | the revoke was not acknowledged, so the key may still be held | the lease is unchanged and `Done` is still open — **retry the release** |
| `AcquireLease` or `Watch` fails with "not configured" / `requires mode: raft` | you are talking to a standalone node | run against a raft node, or use `CompareAndSwap` instead |
| `CompareAndSwap` returns `(false, nil)` | the precondition lost | this is an outcome, not an error — handle it |
| A watch ends with a non-nil, non-context `Err()` | the server dropped you; you may have missed changes | reconcile with `Get`, then re-establish. If it recurs, your consumer is too slow |
| `Watch` events stop arriving but the channel stays open | there is simply nothing to report | idle is not dead. Only a closed channel means the watch is over |

## Three mistakes to avoid

**Do not treat a late acknowledgement as success.** `Renew` returning
`LeaseLostError` means the server accepted your keepalive *after* you had
already been told you lost the key. It is stale news. Acting on it puts you back
to work on a key someone else may now own.

**Do not sleep after a failed renewal.** A failure confirms nothing — your
deadline is still running and every second you wait is one you could have spent
retrying.

**Do not assume `Done` stops your work.** It closes a channel. Whatever must
stop has to be listening.
