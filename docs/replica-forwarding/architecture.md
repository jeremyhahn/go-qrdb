# Replica Forwarding — Architecture

> **Status: PROPOSED — contract for implementation; decision D1 blocks the
> build.** · Scope: go-qrdb · Last verified: 2026-10-05
>
> Nothing in this folder is built yet. Identifiers marked *(new)* do not exist.
> The owner's rulings are recorded under [Rulings](#rulings); the open decision
> is under [PROPOSED — needs sign-off](#proposed--needs-sign-off).

## The problem

A database with its own Raft group (`raft` isolation) lives on the nodes its
placement record names: its **replicas**. A request for that database can
arrive at any node, through any of six transports. A node that holds no
replica must not serve the database from its own catalog. That started a
single-node shard whose acknowledged writes the real replicas never saw.
Today such a node refuses with `*placement.NotAReplicaError`
(`UNAVAILABLE`, 503), which carries the replica set, and the client must
redial a replica itself.

Every client then has to know the replica set and redial. Most clients
connect to one address and never learn the cluster's shape.

## The shape

**A node that holds no replica forwards the request, once, to the database's
replica leader, on the original caller's behalf, and returns the leader's
answer.** The caller sees one request and one answer. When no leader can be
found, the caller gets the typed `NotAReplicaError`, exactly as today.

```
client ──► node C (no replica of acme/orders)
             │  1. resolve: placement(acme, orders) → LeaderReplicaID → replica R1
             │  2. locate:  R1's raft address → member record → R1's API endpoint
             │  3. sign:    CallerAssertion{caller, request binding, hop 1}
             ▼
           node A (replica R1)
             │  4. verify the assertion against C's node certificate
             │  5. authorize AS THE CALLER (roles re-resolved on A)
             │  6. serve -- or answer NotLeader{leader: R2's address}
             ▼
           (one bounded retry: C forwards once more, to R2)
```

**Leader discovery** reads the placement record. Its `LeaderReplicaID` names
the replica that bootstrapped as leader; Raft may have elected another since.
The first forward goes there. A replica that is not the leader answers a write
with `NotLeaderError`, which names the current leader's Raft address. The
forwarding node then makes **one** more forward to that address. A replica
serves reads with the consistency the request asks for, so a read does not need
the leader. If the first target is unreachable, the one retry goes to the next
replica in placement order.

**Endpoint resolution** maps a replica's Raft address to its member record
(`cluster.Member`, keyed by node, carrying `APIEndpoints` per protocol). The
forward uses the endpoint for the node-to-node protocol of the ingress
transport (below). A member that does not advertise that protocol is skipped.

**Identity** travels as the existing signed on-behalf assertion
(`pkg/auth/forward.CallerAssertion`). The forwarding node has already
authenticated and authorized the caller at ingress. It signs an assertion
carrying the caller's identity (subject and tenant, never roles), bound to the
exact request and time-boxed. The target verifies the signature against the
forwarding node's mTLS certificate, checks that certificate is a cluster node
certificate, re-resolves the caller's roles from its own data, and authorizes
the request as the caller. Today this works for REST writes to the system
shard. This feature extends it to every network transport, and binds a
non-HTTP request by protocol, method name and payload digest instead of HTTP
method, path and body digest.

**Hop limit 1.** A forwarded request is never forwarded again. A target that
finds it is not a replica either (a placement that changed in flight) refuses
with `NotAReplicaError`. The forwarding node may spend its one retry, and then
returns the refusal.

## Transports

| Ingress transport | Forwards over | How |
|---|---|---|
| REST | REST | reverse-proxies the HTTP request to the target's REST endpoint, with the assertion headers |
| gRPC | gRPC | a server interceptor re-invokes the same method with the same request message on a connection to the target's gRPC endpoint, assertion in metadata |
| Unix socket | gRPC | the Unix socket serves the gRPC wire, so its interceptor is the gRPC one; it dials the target's gRPC endpoint |
| QUIC | QUIC | relays the request frame (method, payload) to the target's QUIC API endpoint, assertion in the frame's metadata |
| MCP | MCP | relays the JSON-RPC tool call to the target's MCP endpoint (streamable HTTP), assertion headers on the HTTP request |
| Embedded | QUIC | the in-process client encodes the call as the QUIC request it would have sent, and sends it to the target's QUIC endpoint; QUIC is always present because it is Raft's transport |

Node-to-node forwarding speaks REST, gRPC and QUIC at minimum (owner ruling).
MCP forwards MCP-to-MCP. A target that does not advertise MCP gets the typed
refusal, never a translated call.

**Streams are not forwarded.** Watch, the change feed and every other
server-streaming or bidirectional call refuses with `NotAReplicaError`, which
carries the replica set, and the client redials a replica. Proxying a
long-lived stream would tie the stream's life to an intermediate node that
holds no data for it.

## Data flow, end to end

1. Ingress authenticates and authorizes the caller as usual. Forwarding never
   bypasses a check: a request the ingress node would refuse is refused there.
2. The handler resolves the database's services. `RaftBackendFactory.Services`
   returns `*placement.NotAReplicaError`, which now also carries the
   placement's `LeaderReplicaID` *(new field)*.
3. The transport's forwarding layer catches that error before it is mapped to
   a status. Given the error, the `replicaforward.Locator` *(new)* returns at most two
   targets: the leader hint, then the retry candidate.
4. The forwarding layer signs the assertion and sends the request to the first
   target, then returns the target's answer verbatim: status, body, metadata.
5. On `NotLeader` naming a leader, or on a transport failure, it sends once to
   the second target. On anything else, or when targets run out, the caller
   gets the target's answer, or the original `NotAReplicaError` if no target
   could be reached.

## Trust boundaries

- **Only a cluster node may assert on a caller's behalf.** The assertion is
  honored only when the mTLS peer presents a node-scoped SPIFFE certificate
  that chains to the cluster CA. On a node with operator-supplied TLS
  (BYO-TLS), the operator's peer CA counts as the cluster CA for this check
  (owner ruling). The Key B lane provides that through `IsClusterCA`.
- **The assertion carries identity, never authority.** The target re-resolves
  the caller's roles and scope from its own replicated data and authorizes
  independently. A forwarded request can do exactly what the caller could do
  at the target directly, and nothing more.
- **Binding and TTL.** The assertion is bound to the protocol, method and
  payload digest and expires after `forward.DefaultTTL` (30 s). A captured
  assertion replays only the identical request within that window, as today.
- **A forwarded request is marked.** The assertion's presence is the hop
  marker. A target never forwards a request that arrived with one, neither by
  this feature nor by REST's existing write forwarding to a Raft leader. It
  answers `NotLeader` instead, and the forwarding node spends its retry.
- **Tenant scope is unchanged.** The forwarding node resolves the placement
  only for the tenant the caller is authorized for. A caller cannot use
  forwarding to learn another tenant's replica set.

## Component boundaries

| Piece | Package |
|---|---|
| `NotAReplicaError.LeaderReplicaID` | `pkg/placement` |
| Target resolution (placement → member → endpoint) and the per-transport forward driver | `pkg/replicaforward` *(new)* |
| Assertion signing and verification, generalized binding | `pkg/auth/forward` |
| On-behalf verification per transport | `pkg/auth/middleware` (REST, MCP), `pkg/api/grpc` interceptor (gRPC, Unix), `pkg/api/quic` (QUIC) |
| Forwarding layers | `pkg/api/rest`, `pkg/api/grpc`, `pkg/api/quic`, `pkg/api/mcp`, `sdk/go/transport/embedded` |
| Endpoint advertisement | `pkg/cluster` (see decision D1) |

## Alternatives rejected

- **Forward at the service layer.** One forwarding point under every
  transport, but it needs a remote client for every service method inside
  `pkg/` (the SDK module depends on `pkg/`, never the reverse). Rejected:
  that is a second SDK. Forwarding the transport's own request is generic per
  transport and has no per-method code.
- **Translate every ingress into one node-to-node protocol** (say, everything
  over QUIC). Rejected: REST and MCP would need a per-route mapping onto QUIC
  methods. Each transport forwarding its own wire is method-agnostic. The
  embedded transport is the exception: it has no wire of its own, and the
  ruling gives it QUIC.
- **Redirect the client** (HTTP 307 or a gRPC "go there" status). Rejected:
  the client still has to redial a node it may not be able to reach (another
  network, another certificate), and five of the six transports have no
  redirect a stock client follows.
- **Track the current leader in the placement record.** Rejected: it adds a
  replicated write on every election. The bounded retry on `NotLeader` reaches
  the current leader in at most one extra hop.
- **Forward more than once.** Rejected: a loop or an amplification bug becomes
  possible, and the second hop adds nothing the bounded retry does not.

## What this feature deliberately does not do

- It does not forward streams.
- It does not forward requests for the shared system shard. Those keep REST's
  existing write forwarding to the system leader, unchanged.
- It does not change who may do what. Authorization happens at ingress and
  again at the target, as the caller both times.
- It does not cache placements or leaders across requests. Each forward reads
  the placement through the node's replicated store.

## Rulings

From the owner, recorded verbatim in substance:

1. Leader discovery: the placement record's `LeaderReplicaID` → the member's
   `APIEndpoints`, one bounded retry, `NotAReplicaError` if no leader is known.
2. Identity: the original caller, through the signed on-behalf
   `CallerAssertion`, on every network transport. Hop limit 1. Reads and
   writes both forward.
3. Node-to-node forwarding works over REST, gRPC and QUIC at minimum. QUIC is
   always present (it is Raft's transport), so embedded forwards over QUIC by
   default.
4. Streams are refused with the typed `NotAReplicaError`, carrying the replica
   set; the client redials.
5. On BYO-TLS nodes the operator's peer CA counts as the cluster CA for
   on-behalf verification (Key B lane, `IsClusterCA`).

## PROPOSED — needs sign-off

- **D1. Where endpoints come from in a static cluster. Blocking.** Member
  records carrying `APIEndpoints` are written only by dynamic membership, which
  is off by default (`--dynamic-membership`). In a default static cluster there
  is no member record, so ruling 1 finds no endpoint and every forward falls
  back to the refusal.
  - (a) Publish each node's lease-backed member record in every raft-mode
    node, independent of dynamic membership. The `cluster.Advertiser` already
    does exactly this for dynamic membership; this runs it always, without the
    membership reconfiguration loop.
  - (b) Carry `APIEndpoints` in the feature-gate advertisement
    (`__sys:cluster/features/nodes/{replicaID}`), which every node already
    publishes under a lease.
  - (c) Forward only in clusters running dynamic membership, and document the
    refusal everywhere else.

  **Recommendation: (a).** One record per node already has the right shape
  and lifecycle (lease-bound, so a dead node disappears), and (b) would give
  the feature gate a second job. This changes default boot behaviour: every
  raft node writes one lease-backed record. That is why it needs sign-off.
- **D2. The BYO-TLS dependency.** On-behalf verification on BYO-TLS nodes
  needs `IsClusterCA` from the Key B lane. **Recommendation:** build against
  the existing cluster-CA check, and add the Key B call when that lane lands.
  The BYO-TLS integration test is written now and goes green with it.
