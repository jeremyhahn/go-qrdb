# Replica Forwarding — Engineering

> **Status: PROPOSED — contract for implementation.** · Scope: go-qrdb ·
> Last verified: 2026-10-05
>
> Identifiers marked *(new)* do not exist yet. Identifiers not so marked were
> grepped on the date above. Rulings 6 (member records on every raft node) and
> 7 (the leader cache) are in [architecture.md](architecture.md#rulings).

## Exported surface

### `pkg/placement`

```go
type NotAReplicaError struct {
	TenantID string
	Database string
	ShardID  uint64
	NodeID   string
	Replicas []Replica
	// LeaderReplicaID is the placement's bootstrap leader -- a hint, not a
	// fact: Raft may have elected another replica since. Zero when unknown.
	LeaderReplicaID uint64 // (new)
}
```

### `pkg/replicaforward` *(new)*

```go
// Target is one node a request may be forwarded to.
type Target struct {
	ReplicaID uint64
	RaftAddr  string
	Endpoint  string // the target's API endpoint for the chosen protocol
}

// MemberSource lists the cluster's member records (cluster.Member); the
// MembershipStore already implements it.
type MemberSource interface {
	ListMembers(ctx context.Context) ([]cluster.Member, error)
}

// Locator turns a refusal into at most two targets for one protocol.
type Locator struct{ /* members MemberSource; self string */ }

func NewLocator(members MemberSource, selfRaftAddr string) *Locator

// Targets returns the leader hint first, then the next replica in placement
// order, skipping this node and any member that does not advertise proto.
// No target is *NoForwardTargetError.
func (l *Locator) Targets(ctx context.Context, refusal *placement.NotAReplicaError, proto cluster.APIProtocol) ([]Target, error)

// Retry decides the second attempt from the first answer: the leader a
// NotLeader names (resolved to its endpoint), otherwise the second target.
func (l *Locator) Retry(ctx context.Context, first Target, leaderRaftAddr string, rest []Target, proto cluster.APIProtocol) (Target, bool)

// LeaderCache is the per-shard leader hint (ruling 7): shard ID -> the
// replica the last forward reached and its member endpoints. Reads are
// lock-free (copy-on-write map behind an atomic.Pointer); writes serialize.
type LeaderCache struct{ /* ... */ }

func NewLeaderCache() *LeaderCache
func (c *LeaderCache) Get(shardID uint64) (CachedLeader, bool)
func (c *LeaderCache) Put(shardID uint64, leader CachedLeader)
func (c *LeaderCache) Evict(shardID uint64)
func (c *LeaderCache) EvictMember(replicaID uint64) // every entry pointing at it

type CachedLeader struct {
	ReplicaID uint64
	RaftAddr  string
	Endpoints map[cluster.APIProtocol]string
}

// Binding is what an assertion is bound to, for any transport.
type Binding struct {
	Protocol string // "rest", "grpc", "quic", "mcp"
	Method   string // HTTP method, gRPC full method, QUIC method, MCP tool
	Path     string // HTTP path; empty for the others
	Digest   string // forward.BodyDigest of the payload as sent
}
```

### `pkg/auth/forward`

The assertion's `Method`/`Path`/`BodySHA256` fields keep their names and
serialization. A non-HTTP binding stores `"<protocol>:<method>"` in `Method`
and an empty `Path`, so REST assertions are byte-identical to today's. Two
helpers are added:

```go
func SignBinding(subject, tenantID string, b replicaforward.Binding, forwarderNodeID string, key crypto.Signer, now time.Time) (assertionB64, sigB64 string, err error) // (new)
func VerifyBinding(assertionB64, sigB64 string, peer *x509.Certificate, trustDomain string, now time.Time, b replicaforward.Binding) (*CallerAssertion, error) // (new)
```

The carrier per transport:

| Transport | Assertion | Signature |
|---|---|---|
| REST, MCP | header `X-Qrdb-On-Behalf-Assertion` | header `X-Qrdb-On-Behalf-Sig` |
| gRPC, Unix | metadata `x-qrdb-on-behalf-assertion` | metadata `x-qrdb-on-behalf-sig` |
| QUIC | frame metadata key `on-behalf-assertion` *(new)* | `on-behalf-sig` *(new)* |

### Composition options *(new)*

| Option | Package |
|---|---|
| `WithReplicaForwarding(l *replicaforward.Locator, signer crypto.Signer, nodeID string)` | `pkg/api/rest`, `pkg/api/grpc`, `pkg/api/quic`, `pkg/api/mcp` |
| `embedded.WithReplicaForwarding(l *replicaforward.Locator, dial QUICDialer)` | `sdk/go/transport/embedded` |

`pkg/server` wires all of them from the node's member store, its node key and
its SPIFFE node id, in raft mode. Standalone mode has no replicas to forward
to, and wires none.

### Cache invalidation hooks

| Event | Hook | Effect |
|---|---|---|
| a forward answered `NotLeader{leader}` | the forwarding layer | `Put` the named leader |
| a transport error, `NotAReplica`, or `NotLeader` with no leader | the forwarding layer | `Evict(shard)` |
| a placement record written or deleted (create, drop, move, rebalance, add/remove replica) | a Watch on the placement prefix (`__sys:placement/`), on every node | `Evict(shard)` |
| a member record changed or expired | a Watch on `cluster.MembershipPrefix`, on every node | `EvictMember(replica)` |

Both watches re-subscribe after a feed ends, as the membership manager's
does. When a watch is down, the cache is cleared rather than trusted.

## Invariants

1. **At most two forwards per request, never a forward of a forward.** An
   ingress request carrying an assertion is never forwarded (the hop marker),
   and the forwarding layer makes at most one retry.
2. **Authorization twice, as the caller both times.** Ingress authorizes
   before resolving services; the target authorizes the asserted caller with
   roles it resolves itself. A node identity alone authorizes nothing.
3. **The answer is the target's.** Status, body and metadata are relayed
   unchanged, except hop-by-hop transport headers, so a caller cannot tell a
   forwarded answer from a direct one. The only thing added is the response
   header or metadata `x-qrdb-forwarded-to: <replica id>`.
4. **Fail to the refusal, never to the local catalog.** Every failure to
   forward ends in `NotAReplicaError` or the target's own error. Nothing falls
   back to serving locally.
5. **Streams refuse.** A streaming method is identified by its descriptor
   (gRPC `StreamDesc`, the QUIC streaming method set, the REST watch routes,
   embedded `Watch*`), and it returns the refusal without a forward.
6. **Idempotence is the caller's.** A write whose forward timed out after the
   target received it may have applied. The caller gets the transport error,
   exactly as for a direct write that timed out. The retry is made only on
   `NotLeader` (the write was not applied) or a connection that never opened,
   never on a timeout after sending.

## Error semantics

| Error | When | Class |
|---|---|---|
| `*placement.NotAReplicaError` | no target, every target refused, or a streaming method | `Unavailable` (503), unchanged |
| `*replicaforward.NoForwardTargetError{TenantID, Database, Protocol}` *(new)* | no replica advertises the protocol | wrapped by the returned `NotAReplicaError`'s log line; the caller sees `NotAReplicaError` |
| `*forward.AssertionError` (existing family) | the target rejects an assertion | `PermissionDenied` (403); logged at ERROR on both nodes, because it means a misconfigured cluster CA or a forged hop |
| target's own error | anything the target answers | relayed verbatim |

On the wire, `NotAReplicaError` carries the replica set as structured data, so
a client that must redial does not parse the message:

- REST: `"replicas": [{"replica_id", "node_id", "endpoints": {...}}]` in the
  error body;
- gRPC: an `ErrorInfo` detail with reason `NOT_A_REPLICA` and metadata keys
  `replica.<n>.node_id`;
- QUIC: the error frame's metadata;
- MCP: the JSON-RPC error `data`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `cluster.replica_forwarding` | `true` in raft mode | turns forwarding off on this node; the node then always refuses with `NotAReplicaError` |
| `cluster.replica_forwarding_timeout` | `10s` | bound on one forward, connection included |

There is no setting for the hop limit or the retry count. Both are part of
the contract.

## Persistence

The leader cache is in memory, per node, and holds addresses only. Nothing
about it is persisted or replicated.


None of its own. The feature reads the placement table and the member records
(`__sys:cluster/members/{node}`), both replicated. Under decision D1 (a),
every raft node publishes its member record under a lease, as dynamic
membership already does.

## Test strategy

**Unit:**

- `pkg/replicaforward`: target ordering, skipping self and non-advertising
  members, the retry choice for each answer shape, and no-target refusal.
- `pkg/auth/forward`: binding round trips per protocol. A REST assertion is
  byte-identical to today's. Each binding field is tamper-checked.
- Each transport's forwarding layer, against a fake target server:
  - forward and relay;
  - hop marker refused;
  - retry on `NotLeader`;
  - retry on connection refused;
  - no retry on a post-send timeout;
  - streams refused.

**Cache (unit, `pkg/replicaforward`):** a hit skips the placement read; a
`NotLeader` updates the entry; a transport error evicts it; a placement-key
event evicts the shard; a member event evicts the entries pointing at it; a
watch outage clears the cache.

**Integration (`test/integration/`), one per transport, three nodes, RF=1 so
two nodes hold no replica:**

- a write and a read through the non-replica node succeed, and the data is on
  the replica;
- the caller's own authorization applies: a caller without the permission is
  refused at ingress, and a caller from another tenant is refused;
- the placement's bootstrap leader is moved (leadership transfer), and the
  forward still lands through the one retry;
- the replica node is stopped, and the non-replica node answers the typed
  refusal with the replica set, never a local answer;
- with RF=3, the leader of the database's group is killed, and the next request
  forwarded through a non-replica reaches the new leader (cache updated or
  evicted, not trusted);
- a stream (watch) through the non-replica node is refused with the replica
  set.

The same suite on a BYO-TLS cluster lands with the Key B lane (decision D2).

## Observability

| Log event | Level |
|---|---|
| `replicaforward: forwarded` (`tenant`, `database`, `protocol`, `target`, `attempt`) | DEBUG |
| `replicaforward: no target` (`tenant`, `database`, `protocol`) | WARN |
| `replicaforward: target rejected assertion` | ERROR |

| Metric | Meaning |
|---|---|
| `qrdb_replica_forward_total{protocol,outcome}` | outcome is `ok`, `retried_ok`, `refused` or `error` |
| `qrdb_replica_forward_duration_seconds{protocol}` | time per forward |
