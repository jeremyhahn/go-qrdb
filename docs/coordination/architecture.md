# Coordination — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
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

The coordination surface is `sdk/go/coordination.go`: five verbs — `Get`,
`Put`, `CompareAndSwap`, `AcquireLease`, `Watch` — that a control plane uses to
elect a leader, distribute configuration and observe change, without composing
five transport sub-clients itself.

## Problem

A consumer that wants leader election over qrdb has everything it needs
already: leases give it a TTL, transactions give it an atomic
create-if-absent, watch gives it change notification. What it does not have is
a place where those three are put together *correctly*, and the part that is
hard to get right is not the happy path.

Three things go wrong when each consumer wires it itself:

1. **The grant and the bind are two operations.** Grant a lease, then write the
   key: two callers can both grant, and the second's write can steal the first's
   binding. Exclusivity has to be a precondition the state machine evaluates,
   not a check the client performs.
2. **A lease that is lost is learned about late, or not at all.** The obvious
   implementation polls the server and believes the answer. The server truncates
   the remaining lifetime to whole seconds, so a healthy lease reads as zero; a
   lookup that merely times out proves nothing either way. Believing either
   reading produces the two opposite failures — a holder told it lost a lease it
   still holds, and a holder never told about one it has genuinely lost. The
   second puts two writers on one key.
3. **A watch that ends looks the same however it ended.** A closed channel
   cannot distinguish "you cancelled", "the server shut down cleanly" and "you
   fell behind and we dropped you". Only the third means changes happened that
   this consumer never saw, and only the third calls for reconciling against
   current state.

## Shape chosen

**One small consumer-facing interface over the sub-clients the `Client`
exposes.** No new consensus, no new server-side behaviour, no new wire
verb.

```
        sdk.CoordinationClient  (Get, Put, CompareAndSwap, AcquireLease, Watch)
                    |
              sdk.Coordinator          <- the only implementation
                    |
   +--------+-------+--------+---------+
   |        |                |         |
 KVClient  TransactionClient  LeaseClient  WatchClient      (sdk/go/transport)
   |        |                |         |
   +--------+-------+--------+---------+
                    |
        one transport: embedded, REST, gRPC, Unix, QUIC or MCP
```

Every capability is expressed in terms the store serves:

| Coordination verb | Expressed as |
|---|---|
| `Get` / `Put` | the KV data plane, unchanged |
| `CompareAndSwap` | **one** transaction whose precondition the state machine evaluates |
| `AcquireLease` | a lease grant, then **one** create-if-absent transaction with the lease attached to the put |
| `Watch` | a live prefix watch, with the terminal condition surfaced separately from the event channel |

The three failures above are answered structurally rather than by care at the
call site:

- Exclusivity is a **precondition inside the transaction**, so two racing
  acquirers cannot both win. A grant whose bind loses the race is revoked
  immediately, so a lost race costs no leaked lease.
- Ownership is bounded by a **hard deadline that only an acknowledged
  confirmation moves**. A holder is told it may have lost the key at or before
  the instant the server could give it to somebody else. See
  [engineering.md](engineering.md) for the model and the arithmetic.
- A watch carries **`Err()` alongside its channel**, set before the channel
  closes, so a consumer that observes the end always has the reason.

### Alternatives rejected

- **A server-side coordination service with its own verbs.** Rejected: the
  lease, transaction and watch families already compose into exactly these
  semantics. A new service would be a second consensus-adjacent thing to get
  right, a second thing to test, and a second thing to serve on six transports.
- **Serving lease and watch inertly where the node cannot back them** — a
  lease that never expires, a watch that never fires. Rejected: a primitive
  that silently does nothing is worse than a refusal, because the consumer only
  finds out when its election fails to elect. A node that does not compose the
  lease service refuses `AcquireLease` with a typed error.
- **A soft ownership deadline that credits a renewal already in flight.**
  Rejected, and this is the central safety decision. A keepalive that stalls in
  transport produces no extension on the server, so crediting it client-side
  holds the ownership signal open past a lease the server has already expired
  and handed on. That is precisely the failure the signal exists to prevent, and
  it is worth more than the occasional spurious fencing the strictness costs.
- **Reporting a late acknowledgement as a successful renewal.** Rejected: by the
  time it lands the holder has already been told it lost the key and has acted
  on that. Reporting success would send a holder that had already fenced back to
  work on a key it no longer safely owns.
- **Automatic background renewal inside the SDK.** Rejected for the same reason
  [../leases/](../leases/) does not renew on access: liveness must be asserted by
  the holder, explicitly, or the lease outlives the process holding it. The SDK
  runs a background *observer* (it watches for loss) but never a background
  *renewer*.
- **A `Delete` verb.** Rejected as unnecessary: a lease-bound key is deleted by
  `Release` or by expiry, and an ordinary key is removed through the KV surface
  the consumer already has.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| The coordination interface, `Coordinator`, the concrete lease and watch stream | `sdk/go/coordination.go` | go-qrdb |
| The four sub-client interfaces it composes | `sdk/go/transport` | go-qrdb |
| Terminal watch-error classification | `sdk/go/transport` (`WatchResponse.Err`, `ClassifyWatchError`) | go-qrdb |
| Lease objects, TTL accounting, the expiry sweep | `pkg/lease` — see [../leases/](../leases/) | go-qrdb |
| Transaction precondition evaluation | the state machine's apply: `pkg/store` (`pebbleEvaluateCompare`) and `pkg/engine` (`KVEngine.evaluateTxnCompare`) — see [../transactions/](../transactions/) | go-qrdb |
| Change fan-out and watcher eviction | `pkg/watch` — see [../watch/](../watch/) | go-qrdb |
| Commit order that makes revisions monotonic | `pkg/mvcc` over the consensus log | go-qrdb / go-quicraft |
| Consensus, leader election | — | **go-quicraft** |

The surface **introduces no server-side code**. Everything it does is a
composition of calls the transports carry, which is why it is
documented as its own feature rather than as an addition to any of them: what
is normative here is the composition and its failure semantics, not the
primitives.

## Data flow

**`Get`** — one KV read. A `KeyNotFoundError` from the transport is translated
to `(nil, nil)`; every other error passes through unchanged.

**`Put`** — one KV write, straight delegation.

**`CompareAndSwap`** — one transaction. `expect == nil` selects a
create-if-absent precondition (create-revision equals zero). A non-nil `expect`,
**including an empty slice**, selects a pair of preconditions: the key exists
(create-revision greater than zero) *and* its value equals `expect`. The
existence half is what keeps an empty `expect` honest — a value comparison alone
matches an absent key, because the evaluator reads a missing key's value as nil
and nil equals an empty slice, so the swap would create a key it was asked only
to swap. A precondition miss is `(false, nil)`; it is an outcome, not an error.

**`AcquireLease`** —

1. Round the requested TTL **up** to whole seconds, floor one second, and record
   the instant the grant request is *sent*.
2. Grant the lease.
3. In **one** transaction, put the key with the lease attached, guarded by
   create-revision equals zero.
4. If the transaction fails its precondition, or errors, revoke the just-granted
   lease on a **detached but bounded** context and return — `LeaseHeldError` for
   a lost race, the transport's error otherwise. The acquisition error is what
   the caller gets either way; a cleanup that fails leaks one grant that expires
   on its own TTL. The one exception: a bind that finds the just-granted lease
   not found means the sweep revoked it before the bind landed, so nothing is
   revoked and the caller gets `LeaseExpiredBeforeBindError`.
5. On success, start the liveness observer and return the `Lease`.

**`Watch`** — open a live prefix watch on the transport, then drain it on a
spawned goroutine that translates each transport event into a coordination
`Event`, records the terminal reason, and closes the event channel. The reason
is recorded **before** the close, so there is no window in which the stream
looks finished but has no reason yet.

## Standalone and raft — the same surface

**Every verb behaves the same on a standalone node and a raft node.** A
standalone node composes the lease, watch and lock families over the same
engine, lessor and watch hub as raft mode's MVCC state machine, applying
commands in process instead of through the log, and runs the lease-expiry
sweeper itself (see [../run-modes/architecture.md](../run-modes/architecture.md)).
So lease-based leadership and watch-driven convergence work on a single
process, and a lease survives a restart in both modes.

`CompareAndSwap` is linearizable on a standalone node because it is
engine-direct and single-process; consensus is what makes a swap linearizable
across *replicas*.

**The storage backend is no boundary either.** A standalone node composes the
MVCC engine over memory, file, pebble and objstore storage alike
(`lifecycle.NewLocalEngine`), so every verb is served on every backend.

**A consumer discovers what it has by asking, not by probing.**
`Client.Admin().Capabilities(ctx)` returns `Mode` alongside per-family booleans
including `Lease` and `Watch`, the node's sealed effective profile, the same on
every transport. Constructing the `Coordinator` tells you nothing about it —
every transport implements all four sub-client interfaces — and a family the
node does not compose is refused by the individual call.

## Trust boundaries

- Coordination objects are scoped per (tenant, database) like every other
  data-plane object. A lease ID from one tenant is not resolvable in another.
- Each underlying verb carries its own permission — `kv:get`, `kv:put`,
  `transaction:execute`, `lease:grant`, `lease:keepalive`, `lease:revoke`,
  `lease:ttl`, `watch:watch`. The surface adds no permission of its own
  and **cannot widen** what the caller's credential already allows. See
  [api.md](api.md).
- **The lease has no holder identity.** The value written under a leased key is
  a fixed one-byte marker; it is the key's *existence and lease attachment* that
  signal ownership, not its contents. Nothing in this surface authenticates who
  holds a lease, and no authorization decision may be built on holding one.
  Holder identity and reentrancy belong to [../distributed-locks/](../distributed-locks/).
- **The lease TTL is a durability parameter, not a hint.** Expiry deletes the
  bound key. A too-short TTL plus a stalled keepalive deletes data by design.

### Fencing — why the ownership signal exists, and what it does not do

The dangerous case for any TTL-based lock is a holder that pauses — a long GC,
a descheduled container, a partition — past its lease's lifetime, while another
process legitimately takes over, and then resumes believing it still holds the
key. Both write. A lock with no way to detect that produces split-brain rather
than preventing it.

This surface answers that with a **hard, conservatively-dated ownership
deadline**:

- Every confirmation is dated from the instant its request was **sent**, never
  from when the reply arrived. The server's clock for that lease started
  *later*, so the client's deadline is a **lower bound** on the server's own
  expiry instant.
- Only an **acknowledged** confirmation moves the deadline. Work in flight
  moves nothing.
- When the deadline passes, `Done` closes — whether or not the observer has
  managed to reach the server, and whether or not a renewal is outstanding.

So the guarantee is: **`Done` closes no later than the moment the server could
hand the key to somebody else.** That is what makes it usable as a fencing
signal — a fencing signal that arrives late is the one that puts two holders on
one key.

**What it does not do — and this is the scope fence, not an omission to work
around.** `Done` closing tells the holder; it does not stop the holder's
in-flight work, and it cannot reach the protected resource. If the resource is
outside qrdb and accepts writes without checking anything, a paused holder can
still write to it after the key was reassigned. The signal narrows the window;
closing it requires the *resource* to reject a stale writer.

Two ways to close it, both outside this surface:

- **The protected state lives in qrdb** — make the write itself
  `CompareAndSwap`, so a stale holder's write is refused by the store rather
  than by convention.
- **The protected resource is external and needs a monotonic number** — use
  [../distributed-locks/](../distributed-locks/), whose `LockInfo.FencingToken`
  is the lock key's MVCC create revision: globally ordered and monotonically
  increasing, so a resource that records the highest token it has seen and
  refuses anything lower rejects the resumed holder. **The coordination
  `Lease` carries no such token**, deliberately; it is a lease, not a lock.

## What this feature deliberately does not do

- **It does not add consensus, verbs or server-side behaviour.** It is a
  composition; every claim it makes is a claim one of the underlying families
  already makes.
- **It does not renew a lease for you.** No background keepalive. See the
  cadence arithmetic in [engineering.md](engineering.md).
- **It does not make an unfenced external resource safe.** See above.
- **It does not replay a watch from a revision.** The watch is live-only; it
  requests no start revision and no previous values. A consumer that must not
  miss changes across a stream ending reconciles against current state with
  `Get` — it does not resume from where it left off. For replay, use the
  [../watch/](../watch/) surface directly.
- **It does not offer holder identity, reentrancy, blocking acquisition or
  queueing.** A contender is refused, not queued. Those are
  [../distributed-locks/](../distributed-locks/).
- **It does not detect deadlock.** Acquiring keys in a consistent order is the
  caller's responsibility.
