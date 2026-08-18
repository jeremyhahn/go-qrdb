# Leases — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Lease objects, TTL accounting, expiry sweep | `pkg/lease` | go-qrdb |
| Key attachment and cascade delete | `pkg/service`, `pkg/mvcc` | go-qrdb |
| Leader election that decides who sweeps | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Lease()`) | go-qrdb |

Leases are the substrate for [../distributed-locks/](../distributed-locks/): a
lock's TTL *is* a lease, which is why a crashed lock holder's lock is released
without anybody asking.

## Data flow

1. `LeaseGrant(ttl)` is proposed through Raft; the committed entry creates the
   lease and fixes its granted TTL.
2. The caller attaches keys by naming the lease ID on a write.
3. `LeaseKeepAlive` resets the remaining TTL, again through the log.
4. The leader detects expiry and proposes the cascade delete. Followers apply it
   from the log, so no follower expires anything on its own clock.
5. `LeaseTimeToLive` reports `TTL`, `GrantedTTL`, `RemainingTTL` and, where
   tracked, the attached `Keys`.

## Trust boundaries

- Leases are scoped to a (tenant, database) pair like every other data-plane
  object; a lease ID from one tenant is not resolvable in another.
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
