# Leases — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

## Problem

A key that should disappear when its owner stops caring — a session, a service
registration, a lock — cannot be expired by the writer, because the writer is
exactly the thing that might have died. Expiry has to be the store's job, and it
has to be cluster-wide rather than per-node.

## Shape chosen

A **lease** is a server-side TTL object with an identity. Keys attach to a lease
by ID; when the lease expires or is revoked, every key attached to it is
deleted, in one place, by the leader.

- `LeaseGrant(ttl)` returns a `LeaseInfo` with an `ID`.
- `Put(key, value, leaseID)` — or `MVCC().PutWithLease` — attaches the key.
- `LeaseKeepAlive(id)` resets the TTL; a client that stops calling it lets the
  keys go.
- `LeaseRevoke(id)` expires it immediately.

**A key belongs to the lease its latest write named, and to no other.** Writing
a key again under a different lease moves it to that lease; writing it again
with no lease (a plain `Put`, or `PutWithLease` with lease `0`) releases it from
any lease; deleting it releases it too. Revoking or expiring a lease deletes only
the keys still bound to it. A key re-written under lease B is untouched when its
earlier lease A expires. This is etcd's model, and the one a service registry
needs: a restarted instance re-registers under a fresh lease while its previous
registration's lease is still counting down, and that countdown must not delete
the new registration. A compare-and-swap is a value edit, not a re-attachment,
so the key keeps the lease it had.

The lease table is replicated state like the keys it governs. Each lease's
granted TTL, every key's binding and the lease-ID sequence are written in the
same atomic apply batch as the data write that changed them, carried in
snapshots, and restored when a node restarts or recovers from a snapshot.

Expiry is decided by the Raft leader and applied as a normal committed
operation, so every replica sees the deletions at the same point in the log.

### Alternatives rejected

- **Per-key TTL.** Rejected: the common case is *many* keys with one lifetime
  (a session's keys, a registration's records). Per-key TTL makes the caller
  keep them in sync, and there is no way to expire them together.
- **Client-side expiry (writer deletes on shutdown).** Rejected outright: it
  fails in exactly the case expiry exists for — the writer crashing.
- **Wall-clock expiry evaluated independently on each node.** Rejected: clock
  skew makes replicas disagree about whether a key exists, which is a
  consistency violation, not a rounding error. Expiry goes through the log.
- **A key stays in every lease any of its values named.** This was the
  behaviour until it was corrected. Rejected because it makes a lease delete
  data written after the key left it: the old lease's revoke or expiry deleted
  the newer value, silently.
- **Checking each key's current lease at revoke time instead of keeping a
  binding.** Rejected as the mechanism: it needs every key's lease to be
  readable from the stored value, which the non-MVCC state machine does not
  keep. The binding record answers "which lease holds this key" on both state
  machines from one place.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Lease objects, TTL accounting, expiry sweep | `pkg/lease` | go-qrdb |
| Key attachment and cascade delete | `pkg/service`, `pkg/mvcc` | go-qrdb |
| Leader election that decides who sweeps | — | **go-quicraft** |
| **Starting the sweeper** | `cmd/qrdbd`, `pkg/server`, `sdk/go/node` | go-qrdb |
| SDK surface | `sdk/go` (`Client.Lease()`) | go-qrdb |

**Owning the sweeper and starting it are different jobs, and the second is the
composer's.** `pkg/lease.RunExpiry` is a loop somebody has to run; it does not
start itself. Every clustered composition must start it, and there are three:
the daemon (`cmd/qrdbd`), the tenant host (`pkg/server`), and the embedded
clustered node (`sdk/go/node`). The third was missing for a time, so an
embedded clustered node closed a lease's `Done` channel on time and then never
deleted the keys bound to it.

**Standalone compositions start it too**: the daemon in mode
`standalone`, `sdk/go/node.NewStandaloneNode` and the flat
`sdk/go/standalone.NewServer`, all through
`lifecycle.StartLocalLeaseExpiry`. It is the same `RunExpiry` loop with a
constant leadership gate — a standalone node is the only replica — and it
revokes through `LeaseService.Revoke`, the same apply path a raft leader's
revoke takes. So leases expire, delete their keys and notify watchers the same
way in both modes, and "expiry is decided in one place" holds: one sweeper per
node, never two clocks for the same lease.

The failure is worth stating because it is quiet and it is not symmetric. The
holder learns it lost the lease; nothing else changes. For a consumer whose HA
design makes the bound key's existence the ownership signal, that is a control
plane that can **fence** a dead leader and can never **elect** a replacement —
every contender receives `LeaseHeldError` forever. A composition that grants
leases without sweeping them is not a degraded lease service, it is a
one-directional one.

Leases are the substrate for [../distributed-locks/](../distributed-locks/): a
lock's TTL *is* a lease, which is why a crashed lock holder's lock is released
without anybody asking.

## Data flow

1. `LeaseGrant(ttl)` is proposed through Raft; the committed entry creates the
   lease, fixes its granted TTL and takes the next ID from the replicated
   sequence.
2. The caller attaches keys by naming the lease ID on a write. Applying the
   write moves the key's binding to that lease, or releases it when the write
   names none, in the same batch as the value. A write naming a lease that no
   longer exists at apply time is refused and writes nothing.
3. `LeaseKeepAlive` resets the remaining TTL, again through the log.
4. The leader detects expiry and proposes the cascade delete. Followers apply it
   from the log, so no follower expires anything on its own clock. The delete
   covers the keys bound to the lease at that log position.
5. `LeaseTimeToLive` reports `TTL`, `GrantedTTL`, `RemainingTTL` and the keys
   currently bound to the lease (`Keys`).
6. On restart or snapshot recovery the node rebuilds its leases and bindings
   from the persisted lease table. Every restored lease starts a full TTL, since
   the remaining TTL was never replicated.

## Trust boundaries

- Leases are scoped to a (tenant, database) pair like every other data-plane
  object; a lease ID from one tenant is not resolvable in another -- it is
  answered exactly as a missing lease is (not-found, or for `Revoke` the
  idempotent success). *(Corrected: until this release the namespaced lease view
  operated on the global lease set, so any tenant could list, keep alive and
  revoke every tenant's leases by ID. Ownership is now recorded at grant; see
  engineering.md.)*
- Each verb carries its own permission (`lease:grant`, `lease:revoke`,
  `lease:keepalive`, `lease:ttl`, `lease:list`) — revoking somebody else's lease
  is a destructive act and is authorized separately from granting your own.
- Lease expiry deletes user keys. That makes the lease TTL a durability
  parameter, not a hint: a too-short TTL plus a stalled keep-alive is data loss
  by design.

## What this feature deliberately does not do

- **No renewal on access.** Reading or writing a leased key does not extend the
  lease. Only `LeaseKeepAlive` does. Implicit renewal would make expiry depend on
  traffic patterns rather than on the owner's liveness.
- **No sub-second TTL.** TTLs are seconds; the sweep granularity does not make a
  finer promise meaningful.
- **No transfer of ownership.** A lease has no holder identity — that concept
  belongs to locks, which layer it on top.
