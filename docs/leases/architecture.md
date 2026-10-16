# Leases — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-13

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
  keys go. The leader renews it in memory; a periodic checkpoint records
  what is left of it, for the next leader.
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
granted TTL and checkpointed remaining TTL, every key's binding and the
lease-ID sequence are written in the
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
- **Every keep-alive through the log.** This was the behaviour until it was
  replaced. Rejected because each renewal paid a Raft-log fsync, and under CPU
  contention that fsync gave renewals a heavy latency tail (p90 12x the median
  on a loaded 2-core host) that a holder with a short TTL experiences as a
  spurious loss. Renewal needs the leader's word, not the group's.
- **Replicating every renewed lease each second.** This was the behaviour until
  it was replaced by etcd's checkpoints. Rejected because those proposals kept
  a commit-to-apply window open almost all the time, and a renewal's
  linearizable read waits behind it: under disk-sync contention renewals
  stalled for 0.2 to 5 seconds. It also bought nothing a failover needed — a
  new leader still had to grant a fresh TTL, since the deadline it learned was
  up to a second old.
- **A key stays in every lease any of its values named.** This was the
  behaviour until it was corrected. Rejected because it makes a lease delete
  data written after the key left it: the old lease's revoke or expiry deleted
  the newer value, silently.
- **Checking each key's current lease at revoke time instead of keeping a
  binding.** Rejected as the mechanism: it needs every key's lease to be
  readable from the stored value, which the non-MVCC state machine does not
  keep. The binding record answers "which lease holds this key" on both state
  machines from one place.

## Keep-alive: leader-local renewal

A keep-alive is the hottest lease operation — every holder issues one per TTL
fraction, forever — and it carries no information the group must agree on
*before* it answers. So it follows etcd's lessor model rather than writing the
log on every renewal:

- **Renewal is leader-local.** The leader resets the lease's deadline in memory.
  The reply is a heartbeat-quorum read (`SyncRead`), which is what proves the
  replica still leads when it answers; a deposed leader cannot renew. Nothing
  waits for a log fsync. A replica that is not the leader proposes the renewal
  through the log instead, and so does the leader until every replica of the
  group runs a binary with the lease-checkpoint feature: a replica that
  predates it counts leases only from the log, and on winning an election
  would revoke a lease whose renewals were made in its predecessor's memory.
- **Checkpoints record what is left, not what was renewed.** Every
  `lease.checkpoint_interval` (default `5m`) the leader proposes one
  `LeaseCheckpoint` entry per batch of up to 1000 leases, carrying each lease's
  remaining TTL rounded up to a whole second. Only leases whose TTL is longer
  than the interval are checkpointed — a lease that cannot outlive one interval
  gains nothing from it. Every replica persists the value as the lease's
  checkpoint; a checkpoint never moves a deadline.
- **A renewal clears a checkpoint, through the log.** A lease with a checkpoint
  recorded is not renewed in memory: the leader renews it with a replicated
  `LeaseKeepAlive`, whose apply deletes the checkpoint on every replica. So a
  checkpoint never understates a lease its holder has renewed since, and a
  checkpointed lease costs one replicated renewal per checkpoint, not one per
  renewal. This is the on-demand checkpoint etcd proposes on a renewal.
- **A checkpoint a renewal overtook is discarded.** The leader samples a
  checkpoint at one applied index and it applies at a later one, so a
  replicated renewal can land in between. The checkpoint therefore carries
  the applied index it was sampled at, and every renewal through the log
  records the index it applied at. On every replica, the apply ignores a
  lease's checkpoint when a renewal of that lease applied after the sample,
  so the renewal's full TTL stands. A restart, a log replay or a snapshot
  restore reaches the same verdict, because both indexes are part of the
  replicated table.
- **A leader hands over with a checkpoint.** Before a scale-down or a replica
  move transfers leadership away, the leader checkpoints every live lease and
  waits for it to apply, as etcd does, so the next leader counts each one from
  what it has left. It is best-effort and bounded: a checkpoint that fails is
  logged and the transfer goes ahead.
- **A new leader counts from the checkpoint.** A replica that wins an election
  first waits until it has applied every entry its predecessor committed (a
  linearizable read, which Raft serves only once the new leader has committed
  an entry of its own term), then sets each lease's deadline to at least
  *now + effective TTL + `lease.failover_grace`* (default `5s`), where the
  effective TTL is the checkpointed remaining TTL, or the granted TTL when the
  lease has none. Only then does it sweep. Every renewal since a lease's
  checkpoint went through the log and cleared it, so **a new leader never grants
  a lease more TTL than its holder was last renewed to, plus the grace**. A
  deadline already later than the floor is kept; promotion never shortens a
  lease.
- **A restarted replica counts from the checkpoint too.** It restores each
  lease with *now + effective TTL*, for the same reason.

`LeaseCheckpoint` is a new Raft opcode, so it is behind the `lease-checkpoint`
cluster feature gate: until every replica of the group can apply it, the leader
proposes no checkpoints and a new leader counts every lease from its full TTL.
The same gate covers the renewal indexes. They are recorded only after a
group's first checkpoint has applied, so a group whose gate is off keeps
exactly the replicated state it kept before. That first checkpoint starts the
recording and is itself discarded; the leader samples again at once, so a
group pays for this with one extra entry over its lifetime.

What this costs, stated plainly:

- After a leader change an abandoned lease lives up to `failover_grace` longer
  than it would have, plus the time between its checkpoint and the failover; a
  lease with no checkpoint — one whose TTL is not longer than the interval —
  starts its full TTL again. It still expires and still deletes its keys.
- A follower's deadline does not track the leader's in-memory renewals; only
  checkpoints, replicated renewals and its own promotion move it. Whether a
  lease has expired is the leader's call alone, so a follower lists every lease
  it holds until the leader's revoke applies.
- Standalone mode has no other replica, so it renews in memory with no
  checkpoint and no grace to give.

The measured effect on the renewal round trip is in
[../../test/performance/lease-keepalive.md](../../test/performance/lease-keepalive.md).

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
3. `LeaseKeepAlive` is answered by the leader from its own memory: it resets
   the remaining TTL there, after a quorum-confirmed read proves the answering
   replica still leads. It is **not** a log entry. A lease with a checkpoint
   recorded, and any renewal reaching a replica that is not the leader, is
   renewed through the log instead (a replicated `LeaseKeepAlive`, which clears
   the checkpoint). See
   [Keep-alive: leader-local renewal](#keep-alive-leader-local-renewal).
4. The leader detects expiry and proposes the cascade delete. Followers apply it
   from the log, so no follower expires anything on its own clock. The delete
   covers the keys bound to the lease at that log position.
5. `LeaseTimeToLive` reports `TTL`, `GrantedTTL`, `RemainingTTL` and the keys
   currently bound to the lease (`Keys`). The remaining TTL is the leader's to
   answer, as in etcd: a follower's deadline lags the leader's in-memory
   renewals, so a follower that holds the lease refuses with the leader's
   address instead, and the call is forwarded there — by the gRPC, unix, QUIC
   and MCP listeners like any not-leader refusal, by the REST handler, and by
   a database-scoped embedded transport configured to forward. The etcd
   listener does not forward; it answers `Unavailable`, which clientv3 retries
   on another endpoint. A lease a follower does not hold is not found there as
   on the leader, and a caller that needs only to know a lease exists — a row
   lock checking its holder, a database view checking it owns the lease —
   takes the follower's answer.
6. On restart or snapshot recovery the node rebuilds its leases, bindings and
   checkpoints from the persisted lease table. Every restored lease counts from
   its checkpointed remaining TTL, or its full TTL when it has none. After a
   leader change the new leader does the same, plus the failover grace.

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
