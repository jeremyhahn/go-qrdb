# Consistency Levels

QRDB supports four consistency levels for read operations, allowing applications to trade latency for staleness depending on their needs.

| Level | Latency | Guarantee | Use Case |
|-------|---------|-----------|----------|
| **Strong** | 10–50ms | Full Raft SyncRead consensus | Financial transactions, account balance |
| **Linearizable** | 1–5ms | ReadIndex optimization (default) | Most applications, user-facing data |
| **LeaseRead** | <1ms | Leader serves if lease valid | High-throughput reads, caching |
| **Eventual** | <1ms | Local stale read | Analytics, dashboards, non-critical reads |

## 1. Strong Consistency

Strong consistency ensures that every read sees the effects of all writes that completed before it, via full Raft consensus.

**Behavior:**

The read request is proposed through Raft like a write. The leader blocks until the request is committed (replicated to a quorum and persisted), then executes the read against the state machine.

**Trade-off:** Highest latency (requires consensus) but strongest guarantee.

**When to use:** Correctness-critical operations where seeing stale data could cause logical errors.

**Example:** Checking account balance before transferring funds.

```go
// Application code
value, err := kvService.Get(ctx, key, &service.ReadOptions{
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

**Mechanism:** Lease-based replication

The leader maintains a lease duration (typically tied to the election timeout). As long as it receives heartbeat acknowledgments from a quorum, the lease is valid and the leader knows no other node can be elected. During the lease period, reads are served directly from the leader's local state machine.

```
Leader

Lease valid for 150ms (current time: 100ms)
↓
Client read
↓
Local lookup (no Raft, no network)
↓
Result returned (<1ms)
↓
Leader heartbeat expires at 150ms
↓
If no quorum ack before 150ms: lease expires, leader stops serving reads
```

**Latency:** <1ms (purely local lookup, no network round-trip)

**Trade-off:** If the leader crashes or the clock drifts, a brief window of stale reads is possible.

**Requirements:**

1. **go-quicraft config:** `LeaseRead: true`
2. **Leader commitment:** `CheckQuorum: true` (leader steps down if it can't reach a quorum)
3. **Timing:** `ElectionRTT >= 5` (lease duration must be meaningful)

**When to use:**

- High-throughput read workloads where latency is critical
- Scenarios where a brief window of stale reads is acceptable
- Caching layers where eventual consistency is expected
- Time-series data reads where seconds-old data is acceptable

**Example: Caching user session:**

```go
sessionData, err := kvService.Get(ctx, "session:user123", &service.ReadOptions{
    Consistency: consistency.LeaseRead,
})
// Guarantees: None (data may be stale if leader crashed recently)
// Benefit: Extremely fast, no network involved
```

**Example: Metrics dashboard:**

```go
metrics, err := kvService.Scan(ctx, "metrics:", &service.ReadOptions{
    Consistency: consistency.LeaseRead,
})
// Guarantees: None (data may lag by a few seconds)
// Benefit: Sub-millisecond response, no RTT
```

**Failure scenario:**

```
Time=100: Leader's lease valid (good until 250ms)
         Server: Get("counter") → 100

Time=120: Network partition
         Quorum is partitioned; leader can't reach quorum

Time=200: Client reads "counter"
         Leader still thinks lease is valid (until 250ms)
         Server: Get("counter") → 100 (stale; actual value is now 105)

Time=250: Leader's lease expires
         Leader steps down (CheckQuorum enforced)
         New leader elected with counter = 105
```

To mitigate this risk:

- Set `ElectionRTT` appropriately (don't make leases too long)
- Monitor leader restarts and network partitions
- Use Linearizable (default) for correctness-critical data
- Combine LeaseRead with application-level checksums or versioning

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
stats, err := kvService.Scan(ctx, "stats:", &service.ReadOptions{
    Consistency: consistency.Eventual,
})
// Data may be minutes old if this is a follower
// Benefit: Instant response, zero overhead
```

**Example: Server status page:**

```go
status, err := adminService.Status(ctx, &service.AdminOptions{
    Consistency: consistency.Eventual,
})
// Shows local node state at this instant
// Other nodes may have different state
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
value, err := kvService.Get(ctx, key, &service.ReadOptions{
    Consistency: consistency.Strong,
})

// Explicit LeaseRead
value, err := kvService.Get(ctx, key, &service.ReadOptions{
    Consistency: consistency.LeaseRead,
})

// Explicit Eventual
value, err := kvService.Get(ctx, key, &service.ReadOptions{
    Consistency: consistency.Eventual,
})
```

## 8. Performance Impact

Latency by consistency level (measured on local 3-node cluster):

| Operation | Strong | Linearizable | LeaseRead | Eventual |
|-----------|--------|--------------|-----------|----------|
| Get (single key) | 35ms | 3ms | 0.2ms | 0.05ms |
| Scan 1000 keys | 40ms | 8ms | 1ms | 0.3ms |
| Range query | 45ms | 10ms | 2ms | 0.5ms |

**Scaling characteristics:**

- **Strong/Linearizable:** Latency scales with quorum round-trip time (RTT). Larger clusters = larger RTT.
- **LeaseRead/Eventual:** Latency is independent of cluster size (purely local).

## 9. Comparison Table

| Property | Strong | Linearizable | LeaseRead | Eventual |
|----------|--------|--------------|-----------|----------|
| Requires Raft consensus | Yes | Yes (heartbeat) | No (lease check) | No |
| Requires network RTT | Yes | Yes | No | No |
| Sees all prior writes | Yes | Yes | Maybe | No |
| Latency | 10–50ms | 1–5ms | <1ms | <1ms |
| Suitable for leader | Yes | Yes | Yes | Yes |
| Suitable for follower | Yes (forward) | Yes (forward) | No | Yes |
| Recovers on crash | Yes | Yes | Degraded | Degraded |

## Cross-References

- [MVCC Engine](02-mvcc.md) -- Revision tracking and versioning
- [Transactions](03-transactions.md) -- Transactional consistency guarantees
- [Watch](04-watch.md) -- Real-time notification consistency
- [Configuration](12-configuration.md) -- go-quicraft lease settings
- [Architecture](01-architecture.md) -- Read path implementation
