# Coordination — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-12
> Conflicts with this document are defects. Fix them here first.

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

## Where it lives

| File | Holds |
|---|---|
| `sdk/go/coordination.go` | the whole surface: interfaces, `Coordinator`, the concrete lease and watch stream, every error type |
| `sdk/go/coordination_test.go` | unit tests over fakes for the four transport sub-clients. It is an in-package test (`package sdk`) because it shortens the unexported `leaseCleanupTimeout` directly, the only test seam |
| `sdk/go/coordination_standalone_test.go` | end to end against a real standalone node — CAS, lease and watch served |
| `sdk/go/conformance/coordination_parity_test.go` | the lease, watch and lock suite against standalone and raft nodes on all six transports |
| `sdk/go/coordination_raft_test.go` | end to end against a real single-replica raft group — lease lifecycle, expiry, exclusivity, renewal cadence, watch delivery |

## Exported surface

Package `sdk` (`github.com/jeremyhahn/go-qrdb/sdk/go`).

```go
type CoordinationClient interface {
	AcquireLease(ctx context.Context, key string, ttl time.Duration) (Lease, error)
	CompareAndSwap(ctx context.Context, key string, expect, set []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, val []byte) error
	Watch(ctx context.Context, prefix string) (WatchStream, error)
}

type Lease interface {
	Key() string
	Renew(ctx context.Context) error
	Release(ctx context.Context) error
	Done() <-chan struct{}
}

type WatchStream interface {
	Events() <-chan Event
	Err() error
}

type Event struct {
	Type      EventType
	Key       string
	Value     []byte
	PrevValue []byte
	Revision  int64
}

type EventType = transport.EventType

const (
	EventPut    = transport.EventPut
	EventDelete = transport.EventDelete
)
```

Constructors:

```go
func (c *Client) Coordination() (*Coordinator, error)

func NewCoordinator(
	kv transport.KVClient,
	lease transport.LeaseClient,
	txn transport.TransactionClient,
	watch transport.WatchClient,
) (*Coordinator, error)
```

`Coordinator` is the only implementation of `CoordinationClient`, asserted at
compile time (`var _ CoordinationClient = (*Coordinator)(nil)`). `Lease` and
`WatchStream` are interfaces over unexported types for the same reason: a
consumer substitutes a fake in its own tests.

Error types, all matched with `errors.As`:

```go
type CoordinationUnsupportedError struct{ Capability string }  // "kv" | "lease" | "transaction" | "watch"
type LeaseHeldError            struct{ Key string }
type LeaseLostError            struct{ Key string }
type LeaseExpiredBeforeBindError struct{ Key string; TTL time.Duration; Err error } // Unwrap returns Err
```

`Client.Coordination` additionally returns `*ClientClosedError` on a closed
client.

Unexported, but load-bearing and named here because they are what the
behaviour below is measured against:

| Symbol | Value | Role |
|---|---|---|
| `leaseMarker` | `[]byte{1}` | the value written under a leased key |
| `leaseCleanupTimeout` | `5 * time.Second` | bound on the detached revoke that cleans up a grant whose bind lost |

## Invariants

### Construction

- `Client.Coordination` type-asserts the client's transport against all four
  sub-client interfaces and returns `CoordinationUnsupportedError` naming the
  first missing one. **All six shipped transports implement all four**, so this
  error is reachable in practice only from a third-party transport.
- `NewCoordinator` rejects a nil argument with the same error, naming it.
- **Construction says nothing about the run mode.** Every transport implements
  the interfaces in both modes; the mode-dependent refusal comes from the
  individual call. See *Capability split* below.

### `Get`

- A key that does not exist returns `(nil, nil)`. Absence is not an error.
- Absence is recognised as `*transport.KeyNotFoundError` via `errors.As`. Any
  other error is returned unchanged.
- A present key holding an empty value returns a **non-nil empty slice**, so a
  caller can tell absent from empty. This rests on the transport returning a
  non-nil slice for an empty value; the coordination layer neither adds nor
  removes that distinction.

### `Put`

- Straight delegation to the KV data plane, with the transport's default write
  option (synchronous durability). Identical in both run modes.

### `CompareAndSwap`

- **One** transaction. There is no read-then-write window, and two racing
  callers cannot both win.
- `expect == nil` → precondition: create-revision **equals** 0 (create if
  absent). First writer wins; later writers get `(false, nil)`.
- `expect != nil`, **empty slice included** → two preconditions: create-revision
  **greater than** 0 (the key exists) **and** value **equals** `expect`. The
  existence half is required, not decorative: the compare evaluator reads a
  missing key's value as nil, and nil equals an empty slice, so a value compare
  alone would let `CompareAndSwap(key, []byte{}, set)` create a key it was asked
  only to swap.
- On success the transaction's single success op is a put of `set` under `key`.
- A precondition miss is `(false, nil)`. A transport or server failure is
  `(false, err)`.
- Identical in both run modes.

### `AcquireLease`

- The requested TTL is rounded **up** to whole seconds and floored at one
  second. A 1500 ms request grants 2 s; a 10 ms request grants 1 s.
- Acquisition is **exclusive**: the bind is one transaction guarded by
  create-revision equals 0, so a contender never displaces the current holder —
  it receives `LeaseHeldError`.
- A grant whose bind fails — lost race or transport error — is **revoked**. The
  revoke runs on a context detached from the caller's (it must still run when
  the caller's context is already cancelled) but carrying its own
  `leaseCleanupTimeout` deadline, so an unresponsive lease service cannot hold
  `AcquireLease` open indefinitely. A cleanup that fails leaks one grant that
  expires on its own TTL; the **acquisition** error is what the caller gets
  either way.
- The grant and the bind are two round trips, and the TTL runs from the grant.
  A bind that lands after the TTL finds the lease already revoked by the sweep
  and returns `LeaseExpiredBeforeBindError` (`Key`, `TTL`, unwrapping to the
  transport's `LeaseNotFoundError` for that lease). Lease IDs are never reused,
  so the not-found can only be that grant expiring: nothing was bound and there
  is nothing to revoke, so this one bind failure skips the cleanup. A
  not-found naming any other lease is an ordinary bind error and is revoked.
- The value written under the key is `leaseMarker`, a fixed non-empty constant.
  The surface offers no way to store holder identity in the bound key; ownership
  is signalled by the key's existence and its lease attachment.
- The bound key is deleted when the lease is released or expires. Deletion on
  expiry is the leader's sweep (`pkg/lease.Lessor.RunExpiry`), which every clustered
  composition must start — see [../leases/architecture.md](../leases/architecture.md).
  A composition that grants leases without sweeping them can fence a dead holder
  and can never elect a successor: every contender receives `LeaseHeldError`
  forever.

### `Watch`

- Opens a **live** prefix watch: `transport.WatchOptions{}` — no start
  revision, no previous values, no progress notifications. **There is no replay
  from this surface.**
- Because no start revision is requested, `Event.PrevValue` is populated only if
  the server sends a previous value unasked. Treat it as best-effort and do not
  build on it; a consumer that needs previous values uses
  [../watch/](../watch/) directly with `PrevKV`.
- One `Event` per key change, in the order the transport delivers them. A nil
  event in a batch is skipped.
- Event translation: `Key` and `Revision` come from the event's `Kv`
  (`ModRevision`); `Value` is set only on a put, so a delete carries a nil
  `Value`; `PrevValue` comes from `PrevKv` when present, and `PrevKv`'s key is
  used when `Kv` did not supply one.
- The event channel is **unbuffered**, and closed exactly once.
- `Err()` is set **before** the channel closes, so a consumer that observes the
  end always has the reason.
- The caller stops the watch by cancelling its context. A drain goroutine runs
  until then; **a caller that never cancels leaks it** for the life of the
  stream.

## The ownership model — read this before writing a renewal loop

Three facts, and everything else follows from them.

1. **`heldUntil` is a hard deadline.** It is the instant up to which the lease
   is *known* to be held.
2. **Only an acknowledged confirmation moves it.** A grant or a keepalive the
   server accepted confirms a whole TTL. A liveness lookup confirms exactly the
   whole seconds it reported — the server truncates, so that figure is a floor,
   not the truth. Nothing else moves it, ever. A renewal in flight, however
   promising, moves nothing, because a keepalive that dies in transport extends
   nothing on the server either.
3. **Every confirmation is dated from the instant its request was sent**, not
   from when the reply arrived. The server's clock for that lease started
   *later*, so `heldUntil` is a **lower bound** on the server's own expiry
   instant.

From those: **`Done` closes no later than the moment the server could hand the
key to somebody else.** That is the property the whole design is arranged
around, and it is worth the spurious fencing that strictness occasionally costs.

### What closes `Done`

Exactly three things, and no others:

| Cause | Detail |
|---|---|
| The deadline passes | The observer weighs the deadline before and after every lookup, and caps both its sleep and each lookup's timeout at whatever is left, so ownership ends **at the deadline** rather than at whichever poll happens to notice. |
| The server proves the lease is absent | A typed `*transport.LeaseNotFoundError` or `*transport.LeaseExpiredError` from a liveness lookup. This is proof, so it costs no confirming round. |
| `Release` succeeds | Or the server confirms the lease is already gone. |

**What does not close `Done`:** a liveness lookup that timed out, a connection
reset, or a lookup reporting a truncated remaining TTL of zero. None of those
proves anything, and treating them as loss told holders on a perfectly good
cadence that they had lost a lease the server still held. Uncertainty does not
end a lease — but neither does it postpone the signal past the deadline: a
holder that cannot reach the server cannot renew either, so its lease really is
running out.

### The liveness observer

Started by `AcquireLease`, stopped only by a successful `Release`.

| Quantity | Value |
|---|---|
| Poll interval | `max(1s, floor(TTL_seconds / 3) seconds)` — integer division, so a 7 s lease polls at 2 s and a 2 s lease polls at 1 s |
| Sleep before each poll | `min(interval, time until the deadline)` |
| Each lookup's timeout | the same `min(interval, remaining)` |
| Deadline weighed | **before** the lookup and **again after** it |

Both caps matter and neither is redundant. A fixed cadence lines up with the
deadline only by coincidence — a 7 s lease polls at 2 s, 4 s, 6 s, 8 s while
ownership ends at 7 s — and a lookup that hangs runs its whole budget
regardless.

The observer is an *observer*. **It never renews.**

### `Renew`

```
sent := now
if keepalive fails            -> return the transport error; nothing confirmed, nothing moved
if now >= heldUntil (or lost) -> close Done; return *LeaseLostError
otherwise                     -> heldUntil = max(heldUntil, sent + TTL); return nil
```

The deadline test and the extension take the same lock as the observer's
loss decision, so a confirmation and a loss decide in **one** order rather than
two. A confirmation landing in the moment between the deadline passing and the
observer noticing cannot resurrect the lease: both the deadline and the
recorded-loss flag are tested.

**Late acknowledgement — `LeaseLostError`.** A keepalive the server accepted,
whose reply arrived after the deadline, returns `LeaseLostError` rather than
`nil`. The server may well still hold the lease; that is not the point. The
holder has already been told it lost the key and has acted on that, so the ack
is stale news, and reporting it as success would put a holder that already
fenced back to work on a key it no longer safely owns.

`Renew` closes `Done` itself in that case if the observer has not already, so a
caller that got `LeaseLostError` and a sibling goroutine selecting on `Done` are
never looking at different answers.

**The remedy for a late ack is a shorter cadence, not trusting the ack.**

### Renewal cadence — the arithmetic

Write the loop the obvious way:

```go
for {
	select {
	case <-l.Done():
		return  // fence
	case <-time.After(interval):
	}
	if err := l.Renew(ctx); err != nil {
		// see the error table below
	}
}
```

Name the quantities:

| | |
|---|---|
| `T` | the **granted** TTL — the requested duration rounded up to whole seconds, floor 1 s |
| `I` | your renewal interval |
| `R` | one keepalive round trip (a consensus proposal, so measure it, do not guess) |
| `J` | scheduler overshoot — how late your timer actually fires under load |

The sleep starts when the **previous** `Renew` *returned*, and the deadline was
set from when it was **sent**. So the interval that must fit inside `T` is:

```
    R_previous  +  I  +  J  +  R_this   <   T
```

which is the condition to renew safely, and rearranged is the cadence rule:

```
    I  <  T  -  2·R_max  -  J_max
```

**Both round trips are in the budget.** A rule built on the sleep alone omits
the previous round trip, which is exactly the part that eats the slack; on a
loaded host the round trips have been measured two orders of magnitude larger
than the sleep overshoot, so a sleep-based rule reports a cadence as comfortable
right up until it loses the lease.

#### It is not enough to survive one cycle

Renewals fail. A cadence that only just fits leaves no attempt after a failed
one, so a single dropped keepalive costs the lease. What you actually choose `I`
for is **how many consecutive failed renewals you can absorb**.

After the last acknowledged renewal at `S₀` the deadline is `S₀ + T`. Attempt
`n` after it goes out at roughly `S₀ + n·(I + R + J)` and is acknowledged `R`
later, so it can still save the lease while

```
    n·(I + R + J)  +  R  <  T
```

and the number of consecutive failures you can absorb is `n_max − 1`.

Worked, with `T = 30 s`, `R = 250 ms`, `J = 100 ms`:

| `I` | Last attempt that can still save it | Consecutive failures absorbed | Keepalives per lease lifetime |
|---|---|---|---|
| 15 s | n = 1 | **0** — one dropped renewal loses the key | 2 |
| 10 s (`T/3`) | n = 2 | 1 | 3 |
| 5 s | n = 5 | 4 | 6 |
| 3 s | n = 8 | 7 | 10 |
| 2 s | n = 12 | 11 | 15 |

**`T/3` is the sane default.** It matches the observer's own poll cadence, costs
three keepalives per lifetime, and absorbs one failure. Go shorter when the
protected work cannot tolerate a lost key; the cost is linear in traffic and
every one of those keepalives is a consensus proposal.

#### Each boundary, and what happens at it

| Situation | What happens |
|---|---|
| Ack lands **before** the deadline | Deadline moves to `send + T`. Normal. |
| Ack lands **after** the deadline | `LeaseLostError`, `Done` closed. You do not hold the key. Fence. |
| Keepalive **fails** (timeout, reset, refused) | Nothing is confirmed, nothing moves. `Done` is **not** closed by this. You still hold the key until the deadline — **retry immediately**, do not wait out the interval. |
| Keepalive **never returns** | Irrelevant to ownership. `Done` closes at the deadline regardless. |
| `T` rounds to 1 s | `I < 1 − 2R − J`. With a 250 ms round trip that is under 400 ms, with zero tolerance for a single failure. **TTLs below about 5 s are not usable for a renewal loop over a real network.** |
| The host stalls past `T` | The lease is genuinely gone. `Done` closes; `Renew` reports `LeaseLostError`. This is correct behaviour, not a defect. |

### `Release`

- Serialized on a mutex held **across** the revoke, so a second caller waits for
  the in-flight outcome instead of reporting a success the server never gave.
- Marked released **only** once a revoke is acknowledged, or the server confirms
  the lease is already gone (`LeaseNotFoundError` / `LeaseExpiredError` — the
  binding, and the bound key with it, is what the caller asked to remove).
- A revoke that **fails** is not a release: the error is returned, `Done` stays
  open, the observer keeps running, and a later `Release` retries. Returning nil
  there would tell the caller the key was freed while the server still held it
  and contenders still saw `LeaseHeldError`.
- **Idempotent after success**: a second call returns nil and touches no server.
- A released lease is over on the same terms as an expired one, so a keepalive
  landing afterwards is stale in the same way and reports `LeaseLostError`.

## Error semantics

| Error | Returned by | Meaning | Is it a fault? |
|---|---|---|---|
| `*ClientClosedError` | `Client.Coordination` | the client is closed | caller bug |
| `*CoordinationUnsupportedError` | `Client.Coordination`, `NewCoordinator` | the transport lacks the named sub-client, or a nil argument | wiring bug |
| `*LeaseHeldError` | `AcquireLease` | another holder owns the key | **no** — the ordinary outcome of losing an election |
| `*LeaseLostError` | `Renew` | the keepalive was acknowledged after the deadline | **no** — a correct report of lost ownership; fence |
| `*LeaseExpiredBeforeBindError` | `AcquireLease` | the grant-to-bind round trips outlasted the TTL, and the sweep revoked the grant before the bind landed; the key was not bound | **no** — acquire again, with a TTL longer than a round trip |
| `*transport.KeyNotFoundError` | never surfaced by `Get` | translated to `(nil, nil)` | n/a |
| `*transport.CapabilityUnsupportedError` | `AcquireLease` on a backend that keeps no leases (standalone file/objstore) | the engine cannot hold a lease | design-time, not runtime |
| `*transport.WatchCanceledError` | `WatchStream.Err` | the server terminated the watch; reason carried verbatim | depends on the reason — see below |
| `context.Canceled` / `context.DeadlineExceeded` | `WatchStream.Err` | the caller ended it | no |

`CompareAndSwap` returning `(false, nil)` is **not an error** and must not be
handled as one. It is the precondition losing, which is the whole point of a
compare-and-swap.

### Fail-closed paths

- **Exclusive acquisition fails closed.** The precondition is evaluated by the
  state machine; a contender is refused rather than displacing the holder.
- **Ownership fails closed.** `Done` closes at the deadline whether or not the
  observer could reach the server, and whether or not a renewal is outstanding.
  The failure mode is a *premature* fence, never a late one.
- **A failed revoke fails closed.** It reports failure rather than claiming a
  cleanup that did not happen.
- **A late ack fails closed.** `LeaseLostError`, not `nil`.
- **A backend that keeps no leases fails closed.** `AcquireLease` on a
  standalone file or objstore node is a typed refusal, never a lease nothing
  expires.

### Availability — how a caller discovers what it has

| Verb | Standalone, pebble/memory | Standalone, file/objstore | Raft |
|---|---|---|---|
| `Get`, `Put`, `CompareAndSwap` | serves | serves | serves |
| `AcquireLease` | serves | `CapabilityUnsupportedError` | serves |
| `Watch` | serves | serves | serves |

Ask, do not probe:

```go
caps, err := client.Admin().Capabilities(ctx)
// caps.Mode is "standalone" or "raft"
// caps.Lease, caps.Watch, caps.MVCC and caps.KV are true in both modes
```

The capability report is mode-level; the backend boundary is answered per
call, with `*transport.CapabilityUnsupportedError` on every transport.
*(Corrected: this section described a standalone node refusing
`AcquireLease` and `Watch` with `requires mode: raft`, with an untyped error on
REST and MCP. Both are served in both modes since 2026-10-01.)*

## The `WatchStream` terminal contract

`Err()` is meaningful **only after `Events()` has closed**. Before that it is
nil and means nothing.

| `Err()` | What ended the watch | What the consumer does |
|---|---|---|
| `nil` | the server closed the stream cleanly | nothing to act on; re-establish if it still wants a watch |
| `context.Canceled` / `context.DeadlineExceeded` | the caller cancelled | orderly shutdown |
| `*transport.WatchCanceledError` | the server terminated it; `Reason` carried verbatim | **inspect the reason** — see below |
| `*transport.WatchCompactedError` | a requested start revision was compacted | **not reachable from this surface** |
| `*transport.WatchStartRevisionUnsupportedError` | replay requested on a store with no history | **not reachable from this surface** |

**The last two rows are honest about their reachability.** Both conditions are
raised only for a *positive* start revision, and `Coordinator.Watch` always
sends `WatchOptions{}` with `StartRevision` zero, which the hub treats as a
live-only watch. They are listed because `errors.As` against them is harmless
and because a future change that adds a start revision would make them live —
not because a consumer of this surface will see one.

**The condition that IS reachable, and that does mean you missed changes, is
watcher overflow.** The hub must never block the state-machine apply loop on
delivery, so a watcher whose buffer fills is **evicted**, not slowed. That is
recorded as an overflow error, which classifies as a generic
`*transport.WatchCanceledError` with the reason text carried verbatim. So:

> **A `WatchCanceledError` may mean you fell behind and changes happened that
> you never saw.** Recover by reconciling against current state with `Get`, not
> by re-establishing the watch and carrying on.

**Cancellation wins over a simultaneous close, deliberately.** A transport
commonly closes the raw stream *as a consequence* of the caller cancelling, and
Go's `select` picks among ready cases at random, so reading the closed channel
first is no proof the close came first. When the raw stream ends without a
terminal cancellation, the reason recorded is `ctx.Err()` — nil when the caller
is still watching, its cancellation otherwise.

### Back-pressure is the caller's problem

The event channel is **unbuffered**. A consumer that does not read promptly
stalls the drain goroutine, which stalls the transport's stream, which fills the
hub's per-watcher buffer, which evicts the watcher. **A slow consumer causes its
own eviction.** Read the channel into your own buffer, or do the slow work
elsewhere.

## Known limitations

**None specific to a transport.** Two were recorded here — an eviction reaching
an embedded consumer as a clean close, and a watch cancelled mid-hand-over
parking the embedded transport's forwarding goroutine — and both were properties
of `embedded.Transport.WatchPrefix` alone. Both are fixed, and the coordination
layer needs no transport-specific caveat as a result: an eviction now arrives
with its reason on every transport, and cancelling a watch releases everything
behind it.

That second guarantee is stated over the whole forwarding chain rather than
over the transport, because the chain is longer than one hop: a namespaced
(tenant, database) watch passes through a server-side wrapper with forwarding
goroutines of its own, and it parked on exactly the same unguarded handover
after the transport had stopped. Every hop selects on the caller's context now.
See the invariants in [../watch/engineering.md](../watch/engineering.md).

What remains is not a limitation but the design above: `Err()` is meaningful
only after `Events()` has closed, and a consumer whose correctness depends on
seeing every change should still reconcile against current state when a watch
ends with a non-nil, non-context error — that is what the terminal reason is
*for*.

## Configuration

The surface has **no configuration of its own** — no keys, no environment
variables, no flags. What governs it:

| Gate | Effect |
|---|---|
| the storage backend | whether `AcquireLease` serves: the file and objstore backends keep no leases |
| `lease.enabled`, `watch.enabled`, `mvcc.enabled` | narrow the profile in either mode; config can only narrow |
| the granted TTL | passed per call, rounded up to whole seconds, floor 1 s |

See [../run-modes/](../run-modes/) and [../configuration/](../configuration/).

## Persistence

- A leased key is an ordinary user key holding `leaseMarker`, with a lease
  attached. It is deleted by release, or by the leader's expiry sweep.
- A CAS key is an ordinary user key. Nothing about it is special.
- **Watches hold no persistent state.** Nothing survives a restart; a consumer
  reconnecting re-establishes and reconciles.
- The surface reserves **no key prefix**. Keys are whatever the caller names, in
  the caller's own tenant and database namespace. (Contrast
  [../distributed-locks/](../distributed-locks/), which stores lock records under
  an internal prefix.)

## Test strategy

**Unit** (`coordination_test.go`, fakes for all four sub-clients): the exact
transaction each verb constructs — one compare for the create path, two for the
value path, an empty `expect` still carrying the existence compare, the acquire
op carrying the lease ID; absence translation; error propagation; the bounded
cleanup revoke; release idempotency, retryability after a failed revoke, and
concurrent-release serialization; the constructor's nil guards.

**Liveness, driven by scripted transport replies rather than by timing**: a
truncated zero reading does not close `Done`; a transient lookup error does not;
a run of failed lookups with successful renewals does not; a run of failed
lookups **without** renewals does; a persistent zero does; a server-proven
absence does, in one lookup, with no confirming round.

**The in-flight renewal window** — the four cases that make the hard deadline
honest: an ack inside the lifetime renews; an ack after it reports
`LeaseLostError` *and* `Done` is already closed when it does; a renewal
outstanding across the deadline does not defer it by even a poll interval; a
renewal that never resolves does not defer it at all. Plus two cadence
properties: ownership ends at the deadline rather than at the next poll, and a
hung lookup is cut short at the deadline rather than running its full budget.

**Watch termination** — the three ends are asserted separately, including the
random-`select` race between the caller's cancellation and the stream closing
with it, repeated so a single lucky pass cannot make it green.

**Standalone, against a real node** (`coordination_standalone_test.go`): `Get`,
`Put` and `CompareAndSwap` serve, and `AcquireLease` and `Watch` serve too --
a watch opened before the acquire delivers the leased key.

**Parity, on every transport** (`sdk/go/conformance/coordination_parity_test.go`):
one lease, watch and lock suite against a standalone memory, file and pebble
node and a raft node on REST, gRPC, Unix, QUIC, MCP and embedded, and a
standalone objstore node on embedded. Every backend composes the lease-keeping
engine, so no row refuses the lease *(corrected: this described a file/objstore
row where the lease was refused)*. The expiry case attaches its key under a
one-second lease and, if the host is too loaded for the put to land inside it
(the server proves the lease gone), retries under twice the TTL; the reap is
then bounded by that TTL plus one sweep interval plus the revoke's own write.

**Raft, against a real single-replica group** (`coordination_raft_test.go`):
lease lifecycle; expiry **including the reap of the bound key**, over both
clustered composition paths and with the caller's boot context already
cancelled; exclusivity; a sub-TTL renewal cadence that must keep `Done` open
across several cycles; watch delivery and the caller's own cancellation reading
as exactly that.

Two rules that apply to anyone adding tests here:

- **Assert the reap against the store, not against a lease getter.** The
  lessor's list filters expired leases, so asserting on it passes whether or not
  anything was reaped. Assert the key is present while held *and* gone
  afterwards, so the second assertion cannot pass against a key that was never
  written.
- **A timing test must measure its own precondition and act on it.** The
  renewal-cadence test discards an attempt whose cycle genuinely overran the
  TTL — in that state the lease really did expire and `Done` closing is correct,
  so failing would assert a defect on evidence that shows nothing of the kind.
  It retries, and when the retries are exhausted it fails naming every measured
  overrun and attributing the cost to the sleep or to the round trips **only
  when the split forces it**. It does not skip: a measured precondition is not
  an exemption from "never use skip guards".
- **A test's call budget starts after the node is writable.** Every raft test
  boots its node, waits for the first commit (`awaitWritable`, which logs how
  long that took), and only then takes its `raftCallBudget` context. A budget
  taken before boot charges the election to the calls under test, so a host
  starved enough to slow the boot fails a lease call with `quicraft: timeout`
  that has nothing to do with leases. Budgets are named failure bounds, never
  pacing intervals: waits return the moment their condition holds.

## Observability

The surface emits **no logs, metrics or audit records of its own**. Everything
observable comes from the underlying families: the lease verbs, the transaction
execution and the watch establishment and cancellation are logged, metered and
audited where those are configured. See [../leases/](../leases/),
[../transactions/](../transactions/), [../watch/](../watch/) and
[../audit/](../audit/).

A consumer that wants to see coordination behaviour specifically should
instrument its own side: count `LeaseHeldError` (elections lost),
`LeaseLostError` (late acks — a cadence that is too slow), `Done` closures, and
non-nil `WatchStream.Err()` by type.

## Integration testing

`make integration-test-coordination` is mapped to no test files, so it runs zero tests and says so. The feature is covered by `TestCoordinator_SameOnEveryTransport` in `sdk/go/conformance`. The per-transport accounting, including every gap, is the `coordination` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
