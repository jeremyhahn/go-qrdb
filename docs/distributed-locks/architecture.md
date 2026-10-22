# Distributed Locks — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

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
| Per-(tenant, database) resolution | `service.Registry.LockService` — see [../multi-tenancy/](../multi-tenancy/) | go-qrdb |
| SDK surface | `sdk/go` (`Client.Lock()`) | go-qrdb |

## Data flow

**Acquire (`TryLock`, non-blocking):** grant a lease, then run a CAS that
creates the lock key bound to it only if the key does not exist; on commit, the
resulting `ModRevision` becomes the `FencingToken`, written back into the lock
record. If the CAS fails, read the record: held by another holder, revoke the
new lease and return `LockHeldError` immediately. A reentrant re-acquisition by the
same holder revokes the new lease, increments `AcquireCount` on the existing
one and returns the stored token unchanged.

**Acquire (`Lock`, blocking):** the same CAS; on contention, watch the lock key
and retry the CAS when a release event arrives, until the caller's context is
done.

**Release:** validate the caller's fencing token against the token stored at
acquisition. A stale token is refused with `NotLockHolderError`. For a
reentrant lock with `AcquireCount > 1`, decrement rather than release.

**Expiry:** the lock's lease expires like any other; the lock key goes with it.
That is what releases a crashed holder's lock without anyone intervening.

## Storage

Lock records live under the internal prefix `__sys:lock/`, so application keys
can never collide with lock storage and locks never appear in ordinary KV
enumeration.

The stored key is `__sys:lock/{key}` with the key supplied by the caller, and
it carries **no tenant segment**. That is deliberate and is not what separates
tenants — see "Tenant isolation" below.

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

## Tenant isolation is a property of the SERVICE, not of the key

The trust boundary above — *locks are scoped per (tenant, database)* — is a
requirement the code owes, and the only mechanism that discharges it is which
engines the lock service was built over.

A lock key names neither the tenant nor the database, so the isolation of a
lock is entirely the isolation of the transaction, lease, watch and MVCC
servicers the lock service holds. Every transport therefore resolves its lock
servicer per request, through `service.Registry.LockService(tenant, database)`,
exactly as it resolves KV and MVCC. What that accessor returns is a lock
service built over the `<tenant>/<database>/`-namespaced engines, so the
physical record is at `<tenant>/<database>/__sys:lock/{key}` and the blocking
watch, the lease and the CAS all address that same namespaced key.

A lock servicer shared across tenants under `prefix` isolation would make two
tenants locking the same name take the same lock: one tenant's `deploy` would
block every other's, and `List`, `Status` and `LockHeldError` would disclose
foreign lock names and holder IDs. The fencing token does not prevent that; it
guards release and extend only.

### The alternative that was rejected

Namespacing the key *inside* the single lock service — writing
`{tenant}/{database}/__sys:lock/{key}` from within `pkg/lock` — is cheaper and
is rejected. It would be a SECOND tenancy mechanism alongside the registry
namespace, and [../multi-tenancy/architecture.md](../multi-tenancy/architecture.md)
rules that out by name: there is no exempt prefix, an exemption keyed on a
string prefix is a second mechanism, and two mechanisms drift. Confinement
comes from construction — the engines the servicer holds — not from a rule
about the key.

Two compositions keep a single lock servicer:

- **A registry-less composition** — the embedded transport and single-database
  servers — where the whole store and the caller's own keyspace are the same
  set.
- **A resolution that already reached a dedicated store** — a `raft`-isolated
  tenant's per-database shard, or a `volume`-isolated tenant's own Host. Those
  bundles belong to exactly one tenant already. On the `raft` path no composite
  prefix is applied at all: the shard IS the keyspace, and adding a tenant
  segment there would be the same duplicate encoding the multi-tenancy design
  rejects.

A database on its own dedicated **non-raft** store has no lease, watch or MVCC
engine, so it cannot serve a lock at all; the accessor refuses rather than
returning a lock that nothing can expire.

## Standalone vs clustered

The semantics are identical in all run modes. In embedded and standalone the
CAS and the expiry sweep are local; clustered, both go through Raft. A caller
does not change code between them.

Standalone serves the lease, watch and MVCC resolutions over the same engine,
lessor and watch hub as raft mode and runs the lease-expiry sweeper itself —
see [../run-modes/architecture.md](../run-modes/architecture.md).

**A lock survives a restart in both modes.** Its lease is durable: a lock held
when the server stops is still held by the same holder afterwards; its TTL
resumes from the lease's last checkpoint, or is a full TTL when it has none,
and a holder that died with the process loses the lock when that runs out. See [../leases/engineering.md](../leases/engineering.md#persistence).

**The storage engine bounds it.** A standalone node on the file or objstore
backend keeps no leases, so every lock verb there is refused with a typed
capability error rather than serving a lock nothing could expire.

## What this feature deliberately does not do

- **It does not make an unfenced resource safe.** If the protected resource
  ignores the fencing token, a paused holder can still write. The lock narrows
  the window; the token closes it, and only the resource can apply it.
- **No lock ownership transfer** and no queueing/fairness guarantee — a blocked
  acquirer retries on release, it does not hold a position in a queue.
- **No deadlock detection.** Ordering lock acquisition consistently is the
  caller's responsibility.

## Companion documents

- [../18-distributed-locking.md](../18-distributed-locking.md) — the distributed-locking reference in detail.
