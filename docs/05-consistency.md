# Consistency Levels

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

QRDB supports five consistency levels for read operations (`consistency.Level`,
`pkg/consistency/level.go`), allowing applications to trade latency for staleness.
`consistency.ParseLevel` accepts the wire name in lower or upper case.

| Level | Wire name | Guarantee | Cost | Use Case |
|-------|-----------|-----------|------|----------|
| **Strong** | `strong` | Linearizable (ReadIndex) | one heartbeat quorum round | Financial transactions, account balance |
| **Linearizable** (default) | `linearizable` | Linearizable (ReadIndex) | one heartbeat quorum round | Most applications, user-facing data |
| **LeaseRead** | `lease_read`, `leaseread` | Linearizable; leader serves locally while its lease holds | local lookup while the lease holds | High-throughput reads, caching |
| **FollowerRead** | `follower_read`, `followerread` | Every entry this replica knew committed when the read began; leadership not confirmed | wait for the local replica to apply | Reads spread over replicas |
| **Eventual** | `eventual`, `stale` | None: local stale read | local lookup | Analytics, dashboards, non-critical reads |

A standalone node (`pkg/backend/local.go`) has one replica: `LocalBackend.ReadWithConsistency`
ignores the level and every read sees every committed write.

## 1. Strong Consistency

Strong consistency ensures that every read sees the effects of all writes that completed before it.

**Behavior:**

The Raft adapter (`HostAdapter.ReadWithConsistency`, `pkg/backend/raft/adapter.go`) sends a
Strong read down go-quicraft's `SyncRead`, the ReadIndex path of
[Linearizable](#2-linearizable-consistency-default). `Strong` is the zero value of
`consistency.Level` and cannot be told apart from an unset level, so a Strong read that
also sets `AllowStale` is served stale.

**When to use:** Correctness-critical operations where seeing stale data could cause logical errors.

**Example:** Checking account balance before transferring funds.

```go
value, err := kvService.GetWithConsistency(ctx, key, consistency.ReadOptions{
    Consistency: consistency.Strong,
})
// Guarantees: value reflects all writes before this read started
```

## 2. Linearizable Consistency (Default)

Linearizable consistency (the default) provides a good balance: it ensures that reads reflect all writes that completed before the read started, but with lower latency than Strong consistency.

**Mechanism:** ReadIndex protocol

The leader issues a heartbeat to confirm it is still the leader. Once a quorum acknowledges, the leader knows its state is current. The read is then executed against the state machine without consensus:

```
Client issues read
  |
  v
Leader broadcasts heartbeat to quorum
  |
  v
Quorum confirms leadership
  |
  v
Read applied against local state machine (no consensus needed)
  |
  v
Result returned to client
```

**Latency:** one round trip to a quorum, then a local lookup.

**Guarantee:** Linearizable: the read reflects every write that completed before it started.

**When to use:** Most applications that need reasonable consistency without sacrificing performance. Default choice.

**Example:** Retrieving user preferences after an update.

```go
value, err := kvService.Get(ctx, key)
// KVServicer.Get always reads at Linearizable
// Guarantees: value reflects all writes that completed before this read
```

## 3. LeaseRead

LeaseRead is a sub-millisecond consistency level based on the **Raft PhD thesis Section 6.4**. When the leader's lease is valid, it serves reads directly from its local state machine without any Raft round-trip.

**Mechanism:** Leader lease on the monotonic clock

go-qrdb's Raft adapter (`pkg/backend/raft/adapter.go`) sends `Strong`, `LeaseRead`
and `Linearizable` down the same path, go-quicraft's `SyncRead` (ReadIndex). Whether
the leader may answer that read from its lease is the shard's lease setting
(`raft.lease_read`, `--lease-read`), which defaults to `true`
(`pkg/config/config.go`). So the three levels give the same guarantee.

The lease is measured on the leader's **monotonic** clock and anchored at the
time the leader **sent** a heartbeat round, not the time the acknowledgments
arrived. It is valid while a quorum of voting members has acknowledged a round
the leader sent less than the lease length ago. The lease length is
`ElectionRTT - 2` ticks of real time, shortened by 1 % (go-quicraft
`leaseDriftDivisor = 100`, rounded so the lease errs short). A follower that
acknowledges a round sent at T grants no vote to another candidate for at least
that span, so no other leader can exist before the lease runs out. Each
acknowledgment carries the vote window its follower promises (its own
`ElectionRTT` ticks of real time), and the leader counts it toward the lease
only when that window covers the lease. A replica records its promised window
in its persisted Raft hard state before acknowledging, and after a restart
withholds its vote and pre-vote and does not campaign until the window has
passed, so a restart does not reopen a window; the cost is that after a
full-shard restart the first leader is elected about one election timeout
later.

```
Leader sends heartbeat round at T (monotonic clock)
↓
Quorum acknowledges the round
↓
Client read at T + d
↓
d < (ElectionRTT - 2) ticks less 1 %  →  served from the leader's local state
otherwise                              →  ReadIndex heartbeat quorum round, then served
```

When the lease is out, the read is not refused and is not served stale: it falls
back to the heartbeat quorum round, the same as a ReadIndex read without a lease.

**Latency:** <1ms while the lease holds (local lookup); one heartbeat round trip
when it does not.

**Guarantee:** Linearizable, with no stale-read window, while the clock **rate**
drift between replicas stays under 1 %. That 1 % covers a leader clock running
slow against a follower clock running fast. Anchoring at send time means a
leader that is paused or descheduled can only shorten its lease, never extend
it. Because the lease uses the monotonic clock, a wall-clock step (an NTP or
chrony correction, a manual `date`) does not affect it. The bound does not cover
a clock that stops or runs backward, which Go's monotonic clock does not do.

**Requirements:**

1. **Lease reads on:** `raft.lease_read: true` (the default), which sets go-quicraft's `LeaseRead`.
2. **Leader commitment:** `raft.check_quorum: true` (the default; the leader steps down if it can't reach a quorum). go-quicraft refuses `LeaseRead` without `CheckQuorum`.
3. **Timing:** go-quicraft `ElectionRTT >= 5` (lease duration must be meaningful).
   go-quicraft's own message on that check recommends 10, and the lease is
   `ElectionRTT - 2` ticks — a **fixed** 2-tick margin, so lowering
   `ElectionRTT` shrinks the lease disproportionately. Those 2 ticks absorb
   goroutine scheduling jitter between the leader's lease check and followers'
   election timeout, which is what prevents split-brain reads: a correctness
   property, not a latency one. **Do not lower `ElectionRTT` on a multi-node
   shard.** A single-node shard is exempt as a matter of fact rather than
   prudence — its quorum is itself, so the lease cannot be lost. That
   exemption is what `cluster.SingleNodeElectionRTT` applies
   (`cluster.ApplyTransportlessShardTuning`), and only to shards on
   transport-less hosts.
4. **Same timing on every replica:** every replica of a shard runs the same
   `ElectionRTT` and tick interval (`RTTMillisecond`); the lease length on the
   leader is computed from them and assumes the followers' election timers
   use the same values. A replica configured with a shorter window backs no lease;
   that is safe, but reads then take the heartbeat round.
5. **Clock rate:** clock rate drift between replicas under 1 %. Run chrony on
   every replica; see [deployment/user.md](deployment/user.md#keep-replica-clocks-disciplined).

**When to use:**

- High-throughput read workloads where latency is critical
- Any read that needs `Linearizable`: under the requirements above the two
  levels give the same guarantee

**Example: Reading a user session:**

```go
sessionData, err := kvService.GetWithConsistency(ctx, "session:user123", consistency.ReadOptions{
    Consistency: consistency.LeaseRead,
})
// Guarantees: linearizable (reflects all writes that completed before this read)
// Benefit: no network round trip while the leader's lease holds
```

**Failure scenario:**

```
T:      Leader sends a heartbeat round; a quorum acknowledges it
T+x:    Network partition isolates the leader
T+y:    Client reads "counter" on the old leader
        y < lease length: no follower that acknowledged the round at T has
        voted for another candidate yet, so no new leader exists and no
        write has committed elsewhere; the local value is current
        y >= lease length: the lease is out; the read takes the heartbeat
        quorum round, which cannot complete without a quorum, so the read
        does not return a value from the isolated leader
```

## 4. Eventual Consistency

Eventual consistency is the lowest-latency option: reads are served from the local state machine without any consensus or lease check.

**Mechanism:** Direct local read

The local replica returns whatever is in its state machine, regardless of whether it is current. The data will eventually be consistent with the leader (once replication completes), but reads may see stale values.

```
Client read
  |
  v
Local state machine lookup (no Raft, no lease check)
  |
  v
Result returned immediately (<1ms, no network)
```

**Latency:** <1ms (purely local, no synchronization)

**Guarantee:** None. Reads reflect the local state at the time of the read. Followers can lag arbitrarily far behind the leader.

**When to use:**

- Analytics and reporting (lag of minutes is acceptable)
- Dashboards (users expect dashboards to be slightly stale)
- Search indices (exact freshness not critical)
- Debugging and introspection

**Example: Analytics dashboard:**

```go
stats, err := kvService.ScanWithConsistency(ctx, "stats:", consistency.ReadOptions{
    Consistency: consistency.Eventual,
})
// Data may be minutes old if this is a follower
// Benefit: Instant response, zero overhead
```

## 5. FollowerRead

FollowerRead lets any replica, leader or follower, serve a read without a ReadIndex
round trip to the leader. The Raft adapter sends it to go-quicraft's `FollowerRead`.

**Mechanism:** The replica waits until its state machine has applied the commit index
it knows, then reads locally. If the replica is far behind, the read blocks until it
catches up or the context expires.

**Guarantee:** The read reflects every entry the replica knew to be committed when the
read began. Leadership is not confirmed, so during a network partition the read can miss
writes committed after the replica lost contact with the leader. For strict
linearizability use Linearizable.

**Exposure:** REST and MCP (`follower_read`) and the embedded SDK
(`transport.ConsistencyFollower`). The gRPC and QUIC `ConsistencyLevel` enum has no
value for it.

## 6. Consistency-Level Selection Matrix

Choose a consistency level based on your data and use case:

| Data | Correctness-Critical? | Latency-Critical? | Consistency Level |
|------|----------------------|-------------------|-------------------|
| Account balance | Yes | No | Strong or Linearizable |
| User settings | No | Yes | Linearizable or LeaseRead |
| Session token | No | Yes (very) | LeaseRead or Eventual |
| Metrics/stats | No | No | Eventual |
| Cache value | No | Yes (very) | Eventual |
| Notification | Yes | No | Linearizable |
| Config value | Yes | No | Linearizable |
| Audit log | Yes | No | Strong or Linearizable |

## 7. Read Path Routing

A read on a follower is served by the follower:

```
Follower receives read
  |
  v
Strong / Linearizable / LeaseRead: follower asks the leader for its
commit index (ReadIndex); the leader confirms leadership, or answers
from its lease
  |
  v
Follower waits until its state machine has applied that index
  |
  v
Follower executes the read locally and returns the result
```

FollowerRead skips the leader and Eventual skips the wait. A read on a follower pays one
round trip to the leader that a read on the leader does not.

## 8. Configuration

Consistency levels are specified at read-time via the service API. There is no global default configuration; each read operation chooses its level.

**Default (if not specified):** Linearizable (`consistency.Default`)

```go
// Default: Linearizable
value, err := kvService.Get(ctx, key)

// Explicit Strong
value, err := kvService.GetWithConsistency(ctx, key, consistency.ReadOptions{
    Consistency: consistency.Strong,
})

// Explicit LeaseRead
value, err := kvService.GetWithConsistency(ctx, key, consistency.ReadOptions{
    Consistency: consistency.LeaseRead,
})

// Explicit Eventual
value, err := kvService.GetWithConsistency(ctx, key, consistency.ReadOptions{
    Consistency: consistency.Eventual,
})
```

## 8a. Transport Exposure

Read consistency and write durability are selectable on every transport for
the KV, MVCC, and time-series read/write operations. The wire contract uses an
**unspecified** default: when a client omits the field the server applies its
default (linearizable reads / sync writes).

| Transport | Consistency (reads) | Write mode (writes) |
|-----------|--------------------|---------------------|
| **gRPC / Unix** | `ConsistencyLevel` enum on the request (`CONSISTENCY_LEVEL_STRONG/LINEARIZABLE/EVENTUAL`; `UNSPECIFIED`=default) | `WriteMode` enum (`WRITE_MODE_SYNC/WAL_DURABLE/BATCHED`) |
| **QUIC** | Same protobuf request enums as gRPC | Same |
| **REST** | `?consistency=` query param, any wire name in the table at the top | `?write_mode=sync\|wal_durable\|batched` query param |
| **MCP** | optional `consistency` tool argument, any wire name in the table at the top | optional `write_mode` tool argument |
| **Embedded / SDK** | `transport.WithConsistency(level)` (`ConsistencyStrong/Linearizable/Eventual/Follower`) / `WithAllowStale()` read option | `transport.WithWriteMode(mode)` write option |
| **Time-series** | `Consistency` field on the TS request structs (SDK) / `consistency` arg/param/enum on each transport | n/a (writes go through Raft proposal) |

**CLI flags** (the `qrdb` tool, via the SDK):

```bash
qrdb kv get mykey   --consistency eventual     # local stale read
qrdb kv list app/   --consistency strong       # ReadIndex read
qrdb kv put mykey v --write-mode batched       # amortized durable write
qrdb kv del mykey   --write-mode wal_durable
qrdb kv range a z   --consistency linearizable
```

Accepted CLI values (`transport.ParseConsistencyLevel`): consistency
`strong`/`linearizable`/`eventual` (alias `stale`)/`follower`; write mode
`sync`/`wal_durable`/`batched`. Omitting the flag uses the server default.

## 9. Scaling

- **Strong/Linearizable:** Latency scales with quorum round-trip time (RTT). Larger clusters = larger RTT.
- **LeaseRead:** Latency is independent of cluster size while the leader holds its lease (local lookup); near or after lease expiry a read pays one heartbeat round.
- **FollowerRead:** Latency is independent of cluster size; it grows with how far the replica lags.
- **Eventual:** Latency is independent of cluster size (purely local).

## Cross-References

- [MVCC Engine](key-value/engineering.md) -- Revision tracking and versioning
- [Transactions](transactions/engineering.md) -- Transactional consistency guarantees
- [Configuration](12-configuration.md) -- go-quicraft lease settings
- [Architecture](01-architecture.md) -- Read path implementation
