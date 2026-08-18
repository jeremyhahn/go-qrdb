# Distributed Locks — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

Coordinating exclusive access across processes needs more than "whoever wrote
the key first wins". The hard case is a holder that pauses — a long GC, a
descheduled container, a network partition — long enough for its lease to expire
and someone else to take the lock, then resumes believing it still holds it.
Both processes then write. A lock with no way to detect that is a lock that
produces split-brain rather than preventing it.

## Shape chosen

A lock composed from primitives this store already has, plus a **fencing token**
to make stale holders detectable:

```
Lock = lease (TTL)  +  CAS transaction (acquisition)  +  watch (blocking)  +  MVCC revision (fencing token)
```

No new consensus opcodes. Acquisition is a compare-and-swap; the TTL is a
[lease](../leases/); blocking acquisition waits on a [watch](../watch/); and the
fencing token is the MVCC `ModRevision` at acquisition, which is globally
ordered and monotonically increasing.

The token is the whole point. A downstream service that records the highest
token it has seen and refuses anything lower will reject the resumed zombie
holder's write, even though the zombie is convinced it holds the lock.

### Alternatives rejected

- **A lock with only a TTL, no token.** Rejected: this is the classic broken
  distributed lock. The paused-holder case cannot be detected by the lock
  service at all, only by the resource downstream — and only if it is given a
  monotonic number to compare.
- **A dedicated lock consensus protocol.** Rejected: the existing lease, CAS and
  watch primitives compose into exactly the required semantics. A separate
  protocol would be a second thing to get right and a second thing to test.
- **Spin-polling for blocking acquisition.** Rejected: watch already delivers the
  release event, so blocking is a wait on a stream rather than a retry loop.
- **Automatic renewal while held.** Rejected for the same reason leases do not
  renew on access — liveness must be asserted explicitly (`lock extend`), or the
  lock outlives the process that holds it.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Lock service, reentrancy, fencing-token validation | `pkg/lock` | go-qrdb |
| TTL | `pkg/lease` — see [../leases/](../leases/) | go-qrdb |
| Acquisition CAS | `pkg/service` transaction path — see [../transactions/](../transactions/) | go-qrdb |
| Blocking wait | `pkg/watch` — see [../watch/](../watch/) | go-qrdb |
| Revision ordering that makes tokens monotonic | `pkg/mvcc` over **go-quicraft** commit order | go-qrdb / go-quicraft |
| SDK surface | `sdk/go` (`Client.Lock()`) | go-qrdb |

## Data flow

**Acquire (`TryLock`, non-blocking):** read the lock key; if unheld, propose a
CAS that claims it and grants a lease; on commit, the resulting `ModRevision`
becomes the `FencingToken`. If held by another holder, return immediately.

**Acquire (`Lock`, blocking):** the same CAS; on contention, watch the lock key
and retry the CAS when a release event arrives, until the wait timeout.

**Release:** validate the caller's fencing token against the lock's current
`ModRevision`. A stale token is refused. For a reentrant lock with
`AcquireCount > 1`, decrement rather than release.

**Expiry:** the lock's lease expires like any other; the lock key goes with it.
That is what releases a crashed holder's lock without anyone intervening.

## Storage

Lock records live under the internal prefix `__sys:lock/`, so application keys
can never collide with lock storage and locks never appear in ordinary KV
enumeration.

## Trust boundaries

- Locks are scoped per (tenant, database).
- Each verb carries its own permission: `locks:acquire`, `locks:release`,
  `locks:extend`, `locks:status`, `locks:list`. `trylock` is authorized as
  `locks:acquire`.
- **`HolderID` is caller-supplied and is not an authenticated identity.** It
  distinguishes holders for reentrancy; it is not an authorization input. Do not
  build a trust decision on it.
- **The fencing token is the safety mechanism, and it only works if the
  downstream resource checks it.** The lock service cannot enforce it on the
  caller's behalf.

## Standalone vs clustered

The semantics are identical in all three run modes. In embedded and standalone
the CAS and the sweep are local; clustered, both go through Raft. A caller does
not change code between them.

## What this feature deliberately does not do

- **It does not make an unfenced resource safe.** If the protected resource
  ignores the fencing token, a paused holder can still write. The lock narrows
  the window; the token closes it, and only the resource can apply it.
- **No lock ownership transfer** and no queueing/fairness guarantee — a blocked
  acquirer retries on release, it does not hold a position in a queue.
- **No deadlock detection.** Ordering lock acquisition consistently is the
  caller's responsibility.
