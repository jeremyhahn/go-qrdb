# Replica Forwarding — Architecture

> **Status: SHIPPED — REST, gRPC, Unix socket, QUIC, MCP and embedded.** ·
> Scope: go-qrdb · Last verified: 2026-10-06
>
> The owner's rulings are recorded under [Rulings](#rulings).

## The problem

A database with its own Raft group (`raft` isolation) lives on the nodes its
placement record names: its **replicas**. A request for that database can
arrive at any node, through any of six transports. A node that holds no
replica must not serve the database from its own catalog. That started a
single-node shard whose acknowledged writes the real replicas never saw.
Without forwarding, such a node refuses with `*placement.NotAReplicaError`
(`UNAVAILABLE`, 503), which carries the replica set, and the client must
redial a replica itself.

Every client then has to know the replica set and redial. Most clients
connect to one address and never learn the cluster's shape.

## The shape

**A node that holds no replica forwards the request to a replica of the
database, on the original caller's behalf, and returns that replica's
answer.** The caller sees one request and one answer. When no replica serves
it, the caller gets the typed `NotAReplicaError`, exactly as before.

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
The first forward goes there, or to the replica the leader cache names. A
follower that cannot serve a write answers `NotLeaderError`, which names the
current leader's Raft address, and the forwarding node makes **one** more
forward, to that address or, when it names none, to the next replica in
placement order. A follower that proposes the write to its leader itself
simply serves it. A replica serves reads with the consistency the request asks
for, so a read does not need the leader.

The bound is on the targets that **answer**: at most two. A replica that is
**unreachable** -- the request never left this node for it -- does not spend
the retry; the forward moves to the next replica, each replica at most once.
That is what lets a forward right after the leader died reach the new leader:
the dead leader is skipped, the follower tried next names the new leader, and
the one retry reaches it. A failure after the request may have reached a
target -- a reset mid-answer, a timeout -- is never retried, because the
target may have applied it.

A replica is unreachable only once it has stayed so for the connection's
budget, half the forward's. A target restarting, or failing handshakes under
load, is **reconnected to** on a gRPC channel's backoff until a connection
passes, as a gRPC client waits on a channel in transient failure: counting it
unreachable at its first failed handshake would skip a replica that is about
to answer, and a single-replica database would get the refusal instead of its
replica's answer. The wait is bounded by the caller's context as well.

**Endpoint resolution** maps a replica's Raft address to its member record
(`cluster.Member`, keyed by node, carrying `APIEndpoints` per protocol). The
forward uses the endpoint for the node-to-node protocol of the ingress
transport (below). A member that does not advertise that protocol is skipped.
Every raft-mode node publishes its member record, with or without dynamic
membership (ruling 6).

**A per-shard leader cache** (ruling 7) remembers, for each database shard,
the replica the last successful forward reached and its resolved member
endpoints. A hit skips the member lookup. (The refusal that starts a forward
has already read the placement.) The cache never decides correctness, only
where to try first:

- **On a forward's outcome.** A `NotLeader` naming a new leader updates the
  entry to that leader. A transport error, a `NotAReplica` from the target, or
  a `NotLeader` that names no leader evicts it.
- **On a placement change.** Every node watches the placement table -- create,
  drop, move, rebalance, replica add and remove -- and drops the database's
  entry when its placement changes. No node keeps forwarding to a replica a
  rebalance removed. The table's change feed is the node's watch service over
  the replicated keys, so each node sees a change as it applies it. The feed
  serves forwarding only; the tenant shard reconciler keeps its poll.
- **On a member change.** A member record that changes (a new endpoint) or
  disappears (lease expiry) invalidates every entry pointing at that member.

On a miss, or after an eviction, the target is resolved again: placement, then
member records, then the one bounded retry. The cache is per node and in
memory. It holds no data and no identity, only addresses, and it is read
lock-free (a copy-on-write map behind an atomic pointer).

**Identity** travels as the existing signed on-behalf assertion
(`pkg/auth/forward.CallerAssertion`). The forwarding node has already
authenticated and authorized the caller at ingress. It signs an assertion
carrying the caller's identity (subject and tenant, never roles), bound to the
exact request and time-boxed. The target verifies the signature against the
forwarding node's mTLS certificate, checks that certificate is a cluster node
certificate, re-resolves the caller's roles from its own data, and authorizes
the request as the caller. REST's write forwarding to the system leader
already worked this way. This feature extends it to every network transport,
and binds a non-HTTP request by protocol, method name and payload digest
instead of HTTP method, path and body digest. A request with no authenticated
caller is never forwarded: a node identity alone authorizes nothing.

**The embedded caller.** An in-process embedded call carries no credential:
it reaches the service layer directly. Its caller is therefore one the
embedder configures on the node (`node.WithEmbeddedCaller`): a client
certificate and its key, which the node authenticates on every forward
exactly as its QUIC listener authenticates the certificate a client presents
-- the chain verified to the node's CA pool for client authentication, the
identity derived by the node's one authentication chain (the mTLS
authenticator, or an authenticator a composer added, such as an attested-host
authenticator). That identity is what the assertion carries and what the
target authorizes. A node with no caller configured forwards nothing: the
call is refused with `NotAReplicaError`, never made as the node.

**Hop limit 1.** A forwarded request is never forwarded again. A target that
finds it is not a replica either (a placement that changed in flight) refuses
with `NotAReplicaError`. The forwarding node may spend its one retry, and then
returns its own refusal.

## Transports

| Ingress transport | Forwards over | How |
|---|---|---|
| REST | REST | a middleware around the router holds back the handler's JSON 503; when it is the refusal, it sends the request -- method, URI, the caller's whole body, headers less credentials -- to the target's REST endpoint with the assertion headers, and relays the answer |
| gRPC | gRPC | the innermost unary interceptor re-invokes the same method with the same request message on a pooled connection to the target's gRPC endpoint, assertion in metadata, and relays the answer, its header and its trailer |
| Unix socket | gRPC | the Unix socket serves the gRPC services, so it installs the gRPC interceptor; it dials the target's gRPC endpoint |
| QUIC | QUIC | sends the target's QUIC endpoint a `forward.OnBehalf` request: an envelope of the caller's method and payload and the assertion. The frame format has no header, so the forward is a method of its own. The answer frame is relayed as is |
| MCP | MCP | opens an MCP session to the target's MCP endpoint and calls the same tool with the same arguments; every HTTP request of the session carries an assertion bound to it |
| Embedded | QUIC | a database-scoped embedded client makes the call on the node; when the node refuses it as a non-replica, it makes the same call on a QUIC client transport whose request frame is handed to the node's QUIC forwarder instead of a connection -- the `forward.OnBehalf` request a QUIC caller's forward sends, signed for the configured caller. Streams are not forwarded |

Node-to-node forwarding speaks REST, gRPC and QUIC at minimum (owner ruling).
MCP forwards MCP-to-MCP. A target that does not advertise the protocol is
skipped, and with no target left the caller gets the refusal, never a
translated call.

A forwarding node tells a target's **answer** from a failure on the way. REST
and MCP take any HTTP response as the answer. On gRPC every listener marks the
answer to a forwarded request with `x-qrdb-served-by`, which the forwarding
node strips. On QUIC any answer frame is the target's. A target's refusal --
not a replica either, or a follower naming its leader -- is told apart from
every other answer by its structured reason (see [api.md](api.md)), so the
forwarding node acts on it without parsing a message.

A QUIC forward runs over a UDP socket **connected** to the target. A target
whose process died then fails the connection's next keep-alive (the kernel's
port-unreachable), the connection closes, and the next forward finds the
target unreachable rather than writing into a connection nobody answers.

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
   placement's `LeaderReplicaID`.
3. The transport's forwarding layer recognizes the refusal -- by its type, or
   by the structured reason its own error mapping attached -- and signs the
   assertion. `replicaforward.Locator.Forward` drives the forward: the cached
   leader or the leader hint first, through the member records to the
   protocol's endpoint.
4. The forwarding layer sends the request and classifies the answer: served,
   `NotLeader` (with the leader), `NotAReplica`, or unreachable.
5. A served answer is relayed verbatim -- status, body, metadata -- with the
   replica that served it. `NotLeader` or `NotAReplica` spends the one retry;
   unreachable moves to the next replica. When no target serves, the caller
   gets the original `NotAReplicaError`.

## Trust boundaries

- **Only a cluster node may assert on a caller's behalf.** The assertion is
  honored only when the mTLS peer presents a node-scoped SPIFFE certificate
  issued by the cluster CA: every transport's on-behalf path passes one gate
  (`onBehalfPeerGate`) that refuses a revoked leaf, a leaf without a node
  SAN, and -- with an issuer verifier wired -- a chain the cluster CA did not
  issue (`IssuerVerifier.IsClusterCA`). On a node with operator-supplied TLS
  (BYO-TLS), the operator's peer CA is the cluster CA for this check (owner
  ruling 5); an embedded node handed its cluster identity and no cluster CA
  key is a BYO-TLS node, and `lifecycle.TrustPoolClusterCA` resolves its
  cluster CA as it does `qrdbd`'s. A tenant's per-tenant CA is trusted on the same listener, so a
  leaf it issued with a forged node SAN would otherwise speak as the cluster.
- **The assertion carries identity, never authority.** The target re-resolves
  the caller's roles and scope from its own replicated data and authorizes
  independently. A forwarded request can do exactly what the caller could do
  at the target directly, and nothing more.
- **Binding and TTL.** The assertion is bound to the protocol, method and
  payload digest and expires after `forward.DefaultTTL` (30 s). A captured
  assertion replays only the identical request within that window, as today.
- **A forwarded request is marked.** The assertion's presence is the hop
  marker: a target never replica-forwards a request that arrived with one.
  REST's existing write forwarding to the system leader keeps its own bounded
  hop counter, unchanged.
- **Tenant scope is unchanged.** The forwarding node resolves the placement
  only for the tenant the caller is authorized for. A caller cannot use
  forwarding to learn another tenant's replica set.
- **The embedded caller is authenticated, not asserted.** The embedder hands
  the node a certificate and its key, not an identity: the node refuses to
  start with a key that does not match the certificate, and derives the
  identity through its authentication chain on every forward, so revocation,
  expiry and an attested-host authenticator's freshness apply to it as to any
  QUIC client. A configured caller of one tenant is never forwarded for
  another tenant's database.

## Component boundaries

| Piece | Package |
|---|---|
| `NotAReplicaError.LeaderReplicaID` | `pkg/placement` |
| Target resolution (placement → member → endpoint), the leader cache, its invalidation, the structured refusal, metrics | `pkg/replicaforward` |
| Assertion signing and verification, request bindings, the node signing identity | `pkg/auth/forward` |
| On-behalf verification per transport | `pkg/auth/middleware`: REST and MCP (`AuthenticateHTTP`), gRPC and Unix (the unary interceptor), QUIC (`AuthenticateQUICOnBehalf`) |
| Forwarding layers | `pkg/api/rest`, `pkg/api/grpc` (also the Unix socket's), `pkg/api/quic` (its `FrameForwarder` also serves the embedded client), `pkg/api/mcp`; `sdk/go/transport/embedded` (the forwarding scope, its methods generated from the SDK client interfaces by `internal/scopegen`) |
| The QUIC request frame of an SDK call, without a connection | `sdk/go/transport/quic` (`NewFrameTransport`) |
| The embedded caller's authentication, the node's in-process forwarder | `pkg/server` (`AuthenticateCertificate`, `ReplicaForwarder`), composed by `sdk/go/node` (`WithEmbeddedCaller`) |
| The placement table's change feed | `pkg/server/lifecycle` |
| Composition | `cmd/qrdbd`, `sdk/go/node`, through `server.WithReplicaForwarding` |
| Endpoint advertisement | `pkg/cluster` (ruling 6) |

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
- It does not forward requests for the shared system shard through the
  per-database path above. Those keep each transport's own single-hop
  leader-forwarding instead: REST's existing write forwarding
  (`tryForwardWrite`/`forwardToLeader`, keyed on `*service.NotLeaderError`)
  now also recognizes `*service.AdminCredentialNotLeaderError` -- the
  ceremony-initialize admin-credential step's own leader-only refusal
  (`service.CeremonyServiceWithLeaderGate`, see
  [../clustering/architecture.md](../clustering/architecture.md)) -- so a
  follower forwards it to the leader instead of answering 503 for a request
  the leader would have served (node parity, owner ruling 2026-10-06: "all
  the nodes are equal..."). The same ruling extends this past REST, and to
  every leader-only refusal rather than the ceremony's alone: on gRPC, QUIC
  and MCP, any unary call or tool a follower refuses with
  `*service.NotLeaderError` -- a membership change (`admin member add`,
  `add-observer`, `promote`, `remove`), a system-shard write -- or with
  `*service.AdminCredentialNotLeaderError` is relayed to the leader that
  refusal names (`service.LeaderAddress` reads it from either). gRPC's
  forwarding interceptor acts on the `NOT_LEADER` refusal metadata of any
  unary call; `qrdbServiceServer.CeremonyInitialize` forwards the ceremony
  refusal itself, because that refusal carries no such metadata. QUIC's
  `handleStream` and MCP's tool middleware recognize both errors centrally.
  All of them send through one leader method on their existing per-transport
  forwarder (`grpc.ReplicaForwarder.forwardAdminWrite`,
  `quic.FrameForwarder.ForwardToLeader`, `mcp`'s
  `replicaForwarder.forwardToLeader`) -- `MemberByAddress` on the shared
  `replicaforward.Locator` resolves the leader's address to its member record
  exactly as the per-database path does, so no new resolution logic was
  added, only a single-hop send that skips placement and the bounded retry
  (a leader is one node, not a set to retry across). The leader's own answer,
  error or not, is the final result; a forward that never reached the leader,
  or a refusal naming no leader or one with no member record, answers with
  the follower's own refusal. `replica_forwarding.timeout` bounds that
  single hop from the leader lookup on -- the member records are a
  system-shard read, and one that cannot complete falls back to the
  follower's own typed refusal (`Unavailable`) when the timeout expires,
  rather than holding the caller's request open. A membership change reaches
  its transport naming no leader only when none was elected for 10s:
  `AdminService` waits for a known leader before it proposes
  (`waitForShardLeaderKnown`, the wait the scale path makes), answering
  `*service.ShardLeaderTimeoutError` (Unavailable) when none is, and it waits
  rather than re-proposing, because a change that timed out may still have
  committed. The Unix socket forwards through the same gRPC
  interceptor its per-database path uses. The embedded transport forwards
  through its QUIC client transport exactly as it does for a per-database
  refusal. REST resolves the leader through its own `auth.peer_admin_urls`
  registry rather than the `Locator`; when the nodes share a host, the
  registry must name every node, or the fallback (the leader's Raft host
  with this node's own admin port) resolves to the follower itself.
- It does not change who may do what. Authorization happens at ingress and
  again at the target, as the caller both times.
- It does not cache anything but the per-shard leader hint (ruling 7), and
  it never serves from that cache: a cached target is only where the forward
  goes first.

## Rulings

From the owner, recorded verbatim in substance:

1. Leader discovery: the placement record's `LeaderReplicaID` → the member's
   `APIEndpoints`, one bounded retry, `NotAReplicaError` if no leader is known.
   The retry is spent by a target that answered; a replica that could not be
   reached is skipped (see [Leader discovery](#the-shape)). That reading is
   what the integration requirement to reach the new leader right after the
   old one is killed needs, and it never sends a request that may have
   applied a second time.
2. Identity: the original caller, through the signed on-behalf
   `CallerAssertion`, on every network transport. Hop limit 1. Reads and
   writes both forward.
3. Node-to-node forwarding works over REST, gRPC and QUIC at minimum. QUIC is
   always present (it is Raft's transport), so embedded forwards over QUIC by
   default. (The forward reaches a replica's QUIC API listener; a replica that
   does not advertise one is skipped, as on every transport.)
4. Streams are refused with the typed `NotAReplicaError`, carrying the replica
   set; the client redials.
5. On BYO-TLS nodes the operator's peer CA counts as the cluster CA for
   on-behalf verification (`IsClusterCA`), on every transport.
6. (D1, option a) Every raft-mode node always publishes its lease-backed member
   record -- API endpoints and liveness -- through the existing
   `cluster.Advertiser`, without the dynamic-membership reconfiguration loop.
7. A per-shard leader cache, keyed by shard ID, holding the leader's replica
   ID and its resolved member endpoints, lock-free where practical:
   - updated by a `NotLeader` naming a new leader;
   - evicted by a transport error, `NotAReplica` or an unknown leader;
   - invalidated on every node by placement changes (move, rebalance, replica
     add and remove) and by member-record changes.
8. (Embedded identity, option b) The embedder configures the embedded
   client's caller: an mTLS identity, or an identity the node's pluggable
   authentication chain derives -- the attested-host authenticator among them.
   Forwarded embedded calls carry that identity in the signed on-behalf
   assertion, over QUIC. An embedded call with no configured identity is
   refused with the typed `NotAReplicaError` and is never forwarded as the
   node.

   A miss resolves again from placement, then member records, then the one
   bounded retry.

## Decisions taken

- **D1 → ruling 6.** Option (a): every raft-mode node publishes its member
  record. The alternatives (endpoints in the feature-gate advertisement;
  forwarding only under dynamic membership) were rejected.
- **D2.** Build against the existing cluster-CA check, then gate every
  transport's on-behalf path on `IsClusterCA` once the issuer verifier
  existed; the BYO-TLS suite is the network-transport suite, whose nodes run
  on operator-supplied TLS.
- **D3 → ruling 8.** Option (b) for the embedded caller: embedder-configured.
  The alternatives -- forwarding only calls whose context carries an identity,
  or signing as the node -- were rejected; the second contradicts ruling 2.
- **D4.** The embedded client forwards with the QUIC client transport's own
  encoding of each call (`quic.NewFrameTransport`) and the QUIC listener's own
  forwarder (`quic.FrameForwarder`), so there is no per-method forwarding code
  beyond a generated delegate per data-plane method, and nothing a QUIC
  forward does that an embedded one does not.
