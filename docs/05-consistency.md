# Consistency Levels

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

QRDB supports five consistency levels for read operations (`consistency.Level`, `pkg/consistency/level.go`), allowing applications to trade latency for staleness depending on their needs.

| Level | Latency | Guarantee | Use Case |
|-------|---------|-----------|----------|
| **Strong** | 1–5ms | SyncRead (ReadIndex), the same path as Linearizable | Financial transactions, account balance |
| **Linearizable** | 1–5ms | ReadIndex optimization (default) | Most applications, user-facing data |
| **LeaseRead** | <1ms | Linearizable; leader serves locally while its lease holds | High-throughput reads, caching |
| **Eventual** | <1ms | Local stale read | Analytics, dashboards, non-critical reads |
| **FollowerRead** | 1–5ms | ReadIndex on the receiving replica, leader or follower | Linearizable reads spread across replicas |

## 1. Strong Consistency

Strong consistency ensures that every read sees the effects of all writes that completed before it.

**Behavior:**

`Strong` is the zero value of `consistency.Level`, so it cannot be told apart from an unset level. `HostAdapter.ReadWithConsistency` (`pkg/backend/raft/adapter.go`) sends it to go-quicraft's `SyncRead`, the ReadIndex path `Linearizable` uses (section 2); with `AllowStale` set it is a stale local read instead. The read is not proposed through the Raft log.

**When to use:** Correctness-critical operations where seeing stale data could cause logical errors.

**Example:** Checking account balance before transferring funds.

```go
// Application code
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

**Latency:** 1–5ms (single RTT to a quorum + local execution)

**Guarantee:** Causally consistent with all writes before the read.

**When to use:** Most applications that need reasonable consistency without sacrificing performance. Default choice.

**Example:** Retrieving user preferences after an update.

```go
value, err := kvService.Get(ctx, key)
// Implicitly uses consistency.Linearizable
// Guarantees: value reflects all writes that completed before this read
```

## 3. LeaseRead

LeaseRead is a sub-millisecond consistency level based on the **Raft PhD thesis Section 6.4**. When the leader's lease is valid, it serves reads directly from its local state machine without any Raft round-trip.

**Mechanism:** Leader lease on the monotonic clock

go-qrdb's Raft adapter (`pkg/backend/raft/adapter.go`) sends `LeaseRead` and
`Linearizable` down the same path, go-quicraft's `SyncRead` (ReadIndex). Whether
the leader may answer that read from its lease is the shard's `lease_read`
setting (`raft.lease_read`, `--lease-read`), which defaults to `true`
(`pkg/config/config.go`). So the two levels give the same guarantee.

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


1. **go-quicraft config:** `LeaseRead: true`
2. **Leader commitment:** `CheckQuorum: true` (leader steps down if it can't reach a quorum)
3. **Timing:** `ElectionRTT >= 5` (lease duration must be meaningful).
   The lease is `ElectionRTT - 2` ticks — a **fixed** 2-tick margin, so lowering
   `ElectionRTT` shrinks the lease disproportionately. Those 2 ticks absorb
   goroutine scheduling jitter between the leader's lease check and followers'
   election timeout, which is what prevents split-brain reads: a correctness
   property, not a latency one. **Do not lower `ElectionRTT` on a multi-node
   shard.** A single-node shard is exempt as a matter of fact rather than
   prudence — its quorum is itself, so `hasValidLease` is unconditionally true
   and the lease cannot be lost. That exemption is what
   `cluster.SingleNodeElectionRTT` applies, and only to shards on
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

## 5. Consistency-Level Selection Matrix

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

## 6. Read Path Routing

When a read arrives at a follower node, it is automatically forwarded to the leader:

```
Follower receives read
  |
  v
Follower detects not leader
  |
  v
Forwards request to leader via Raft
  |
  v
Leader executes read (using selected consistency level)
  |
  v
Leader returns result to follower
  |
  v
Follower returns result to client
```

**Note:** Forwarding adds latency. For the lowest latency, clients should connect directly to the leader when possible.

## 7. Configuration

Consistency levels are specified at read-time via the service API. There is no global default configuration; each read operation chooses its level.

**Default (if not specified):** Linearizable

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

## 7a. Transport Exposure

Read consistency and write durability are selectable on every transport for
the KV, MVCC, and time-series read/write operations. The wire contract uses an
**unspecified** default: when a client omits the field the server applies its
default (linearizable reads / sync writes).

| Transport | Consistency (reads) | Write mode (writes) |
|-----------|--------------------|---------------------|
| **gRPC / Unix** | `ConsistencyLevel` enum on the request (`CONSISTENCY_LEVEL_STRONG/LINEARIZABLE/EVENTUAL`; `UNSPECIFIED`=default) | `WriteMode` enum (`WRITE_MODE_SYNC/WAL_DURABLE/BATCHED`) |
| **QUIC** | Same protobuf request enums as gRPC | Same |
| **REST** | `?consistency=strong\|linearizable\|eventual` query param | `?write_mode=sync\|wal_durable\|batched` query param |
| **MCP** | optional `consistency` tool argument | optional `write_mode` tool argument |
| **Embedded / SDK** | `transport.WithConsistency(level)` / `WithAllowStale()` read option | `transport.WithWriteMode(mode)` write option |
| **Time-series** | `Consistency` field on the TS request structs (SDK) / `consistency` arg/param/enum on each transport | n/a (writes go through Raft proposal) |

**CLI flags** (the `qrdb` tool, via the SDK):

```bash
qrdb kv get mykey   --consistency eventual     # local stale read
qrdb kv list app/   --consistency strong       # full-consensus read
qrdb kv put mykey v --write-mode batched       # amortized durable write
qrdb kv del mykey   --write-mode wal_durable
qrdb kv range a z   --consistency linearizable
```

Accepted CLI values: consistency `strong`/`linearizable`/`eventual`; write mode
`sync`/`wal_durable`/`batched`. Omitting the flag uses the server default.

## 8. Performance Impact

- **Strong/Linearizable:** Latency scales with quorum round-trip time (RTT). Larger clusters = larger RTT.
- **LeaseRead:** Latency is independent of cluster size while the leader holds its lease (local lookup); near or after lease expiry a read pays one heartbeat round.
- **Eventual:** Latency is independent of cluster size (purely local).

## 9. Comparison Table

| Property | Strong | Linearizable | LeaseRead | Eventual |
|----------|--------|--------------|-----------|----------|
| Requires Raft consensus | Yes (heartbeat) | Yes (heartbeat) | No (lease check) | No |
| Requires network RTT | Yes | Yes | No (while the lease holds) | No |
| Sees all prior writes | Yes | Yes | Yes (clock-rate drift < 1 %) | No |
| Latency | 1–5ms | 1–5ms | <1ms | <1ms |
| Suitable for leader | Yes | Yes | Yes | Yes |
| Suitable for follower | Yes (forward) | Yes (forward) | Yes (forward) | Yes |
| Recovers on crash | Yes | Yes | Yes | Degraded |

## Cross-References

- [MVCC Engine](key-value/engineering.md) -- Revision tracking and versioning
- [Transactions](transactions/engineering.md) -- Transactional consistency guarantees
- [Configuration](12-configuration.md) -- go-quicraft lease settings
- [Architecture](01-architecture.md) -- Read path implementation
