# Replica Forwarding — API

> **Status: SHIPPED — REST, gRPC, Unix socket, QUIC, MCP and embedded.** ·
> Scope: go-qrdb · Last verified: 2026-10-26

Replica forwarding adds **no new routes, RPCs or tools**, and one QUIC method,
`forward.OnBehalf`, that only a cluster node may call. It changes what every
existing **database-scoped, unary** capability does when the request reaches a
node that holds no replica of the database. Instead of refusing, the node
forwards the request to a replica and returns that node's answer. That
applies to KV, MVCC, batch, transactions, SQL, collections, time series,
vectors and leases, on every network transport, and to the data-plane calls of
a database-scoped embedded client.

## Per-transport form

| Transport | Forwarded over | Carrier of the on-behalf assertion | Marker on the answer |
|---|---|---|---|
| REST | REST | headers `X-Qrdb-On-Behalf-Assertion`, `X-Qrdb-On-Behalf-Sig` | header `X-Qrdb-Forwarded-To: <replica id>` |
| gRPC | gRPC | metadata `x-qrdb-on-behalf-assertion`, `x-qrdb-on-behalf-sig` | header metadata `x-qrdb-forwarded-to` |
| Unix socket | gRPC | as gRPC | as gRPC |
| QUIC | QUIC | the `forward.OnBehalf` request: an envelope of the caller's method and payload, and the assertion | none: an answer frame has no metadata |
| MCP | MCP (streamable HTTP) | the REST headers, on every HTTP request of the forward's session | `_meta["x-qrdb-forwarded-to"]` on the tool result |
| Embedded | QUIC | the `forward.OnBehalf` request, as on QUIC, for the caller the embedder configured | none, as on QUIC |

A client never sets the assertion. A request that carries one from anything
but a verified cluster node certificate is refused as unauthenticated.

The assertion is bound to the request it rides on. REST and MCP bind the HTTP
method, the request URI and the body. gRPC binds `grpc:<full method>` and the
request message, serialized deterministically. QUIC binds `quic:<method>` and
the payload. An assertion bound to one request verifies for no other.

## Which calls forward

| Call shape | Forwards |
|---|---|
| Unary call on a `raft`-isolated database | yes |
| Server-streaming or bidirectional call (gRPC and Unix `Watch`, REST `GET /watch`, QUIC `watch.Watch` and `watch.WatchPrefix`) | **no**: refused with `NotAReplicaError` and the replica set |
| MCP `qrdb_watch` (a bounded request, not a stream) | yes |
| Call on the shared system shard | no: each transport's own write forwarding to the system leader applies |
| A leader-only call on a follower -- a membership change (`admin member add`, `add-observer`, `promote`, `remove`), a system-shard write, or ceremony-initialize's admin-credential step -- on every transport: REST, gRPC, Unix, QUIC, MCP, embedded | yes, single hop, to the leader the refusal names, not through this feature's per-database path: REST's `tryForwardWrite`, gRPC's `forwardAdminWrite`, QUIC's `ForwardToLeader`, MCP's `forwardToLeader` each recognize `*service.NotLeaderError` and `*service.AdminCredentialNotLeaderError`. A membership change first waits, up to 10s, until the node knows a leader, so an election in progress ends in a refusal naming the leader rather than one naming none. The leader's answer is relayed; a refusal naming no resolvable leader is the answer |
| Call that arrived forwarded (carries an assertion) | no: hop limit 1 |

## Status mapping

| Condition | REST | gRPC / Unix | QUIC | MCP |
|---|---|---|---|---|
| Forwarded and answered | the target's status | the target's code | the target's status | the target's result |
| No target served (none reachable, every one refused, forwarding off, or a stream) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |
| A membership change found no leader within its 10s wait (`*service.ShardLeaderTimeoutError`, nothing proposed) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |
| The forward failed after it may have reached a target | 503, or 504 when the deadline passed | the transport's own code | `StatusUnavailable`, or `StatusDeadlineExceeded` | `UNAVAILABLE`, or `DEADLINE_EXCEEDED` |

The refusal carries the replica set as structured data, so a client that must
redial does not parse a message. Every transport carries the same reason and
metadata: reason `NOT_A_REPLICA` (or `NOT_LEADER` for a follower refusing a
write, naming the leader as `leader_raft_addr`), and these metadata keys:

| Key | Value |
|---|---|
| `tenant`, `database` | the database |
| `shard_id` | its Raft shard |
| `node_id` | the node that refused |
| `leader_replica_id` | the placement's bootstrap leader, `0` when it names none |
| `replicas` | how many replicas follow |
| `replica_<n>_id`, `replica_<n>_node`, `replica_<n>_raft_addr` | replica `n`, from 0 |

```json
// REST error body
{ "error": "placement: node 192.0.2.3:63001 is not a replica of acme/orders (shard 1000); its replicas are on 192.0.2.1:63001",
  "code": "NOT_A_REPLICA",
  "metadata": { "tenant": "acme", "database": "orders", "shard_id": "1000", "node_id": "192.0.2.3:63001",
                "leader_replica_id": "1", "replicas": "1",
                "replica_0_id": "1", "replica_0_node": "192.0.2.1:63001", "replica_0_raft_addr": "192.0.2.1:63001" } }
```

On gRPC and the Unix socket the same data is a `google.rpc.ErrorInfo` detail
(domain `qrdb.v1`). On QUIC it is the same `ErrorInfo` detail in the error
frame's `google.rpc.Status`. On MCP it is the error envelope's `reason` and
`metadata`.

A node that cannot confirm the cluster seal state — its lease on the seal
records lapsed, as it does while the system shard has no leader — refuses with
reason `SEAL_STATE_UNCONFIRMED` and no metadata, on the same carriers. It is
retryable like any `UNAVAILABLE`, and it is not `SEALED`: the barrier is
unsealed ([../barrier-seal/api.md](../barrier-seal/api.md)).

## Embedded

The embedded client has no wire of its own, so a call its node refuses as a
non-replica is sent as the QUIC request a QUIC client would have sent to the
replica: `forward.OnBehalf`, signed by the node, carrying the caller the
embedder configured. Without a configured caller nothing is forwarded and the
call returns `*placement.NotAReplicaError`, never a call made as the node.

```go
cert, err := tls.LoadX509KeyPair("<install-dir>/orders-app.crt", "<install-dir>/orders-app.key")
if err != nil { /* ... */ }
n, err := node.NewClusteredNode(ctx, cfg, members, serverOpts,
    node.WithEmbeddedCaller(cert), // the identity this node's client forwards as
)
orders, err := n.Client().DatabaseScoped("orders")
// The node authorizes the identity the call's context carries before it
// serves or forwards the call; a forward is made as the configured caller.
appCtx := tenant.WithTenantID(auth.WithIdentity(ctx, appIdentity), "acme")
err = orders.KV().Put(appCtx, "k", []byte("v"))
```

| Symbol | Package | Purpose |
|---|---|---|
| `WithEmbeddedCaller(tls.Certificate) NodeOption` | `sdk/go/node` | The identity the node's embedded client forwards as. A certificate that is empty, does not parse, or does not match its key fails the node's construction with `*node.ConfigError`. |
| `WithReplicaForwarding(ReplicaForwarder, auth.Authenticator) Option` | `sdk/go/transport/embedded` | Makes a database-scoped transport forward what the node refuses as a non-replica, as the identity the authenticator returns. `sdk/go/node` installs it. |
| `ReplicaForwarder` | `sdk/go/transport/embedded` | Forwards one QUIC request frame as a caller: `Forward` to a replica of the database, `ForwardToLeader` to a named leader address in one hop; `*quic.FrameForwarder` (`pkg/api/quic`) is one. |
| `NewFrameTransport(database string, send FrameSender) *Transport` | `sdk/go/transport/quic` | A QUIC client transport that hands each request frame to `send` instead of a connection. |
| `(*Server).ReplicaForwarder() (*quic.FrameForwarder, error)` | `pkg/server` | The node's in-process forwarder; `(nil, nil)` when forwarding is off. |
| `(*Server).AuthenticateCertificate(ctx, []*x509.Certificate) (*auth.Identity, error)` | `pkg/server` | Authenticates a certificate chain as the QUIC listener authenticates a client's. |

Each forward authenticates the configured certificate again, the way the QUIC
listener authenticates a client: the chain must verify, for client
authentication, to the node's CA pool, and the identity is what the node's one
authentication chain derives from it -- the mTLS authenticator, or an
authenticator a composer added (`node.WithAuthenticators`), such as an
attested-host authenticator. A revoked or expired certificate stops
forwarding at once. A caller of another tenant than the refused database is
not forwarded.

The calls an embedded client forwards are every unary method of the
data-plane client interfaces (KV, guarded KV, MVCC, transactions, batch,
leases, locks, indexes, time series, vectors, collections). A stream --
`Watch`, `WatchPrefix`, `ImportFile` -- is refused with the replica set.

| Refusal | Error |
|---|---|
| No caller configured, the caller does not authenticate or belongs to another tenant, or no replica served it | `*placement.NotAReplicaError`, wrapped in `*transport.RequestError` |
| The replica answered with an error | the replica's status, as a QUIC client receives it (`*transport.RequestError` with `StatusCode`) |
| The configured certificate does not verify | `*server.CallerCertificateError` (logged; the call gets the refusal) |

## Permissions

None new. Ingress authorizes the caller for the capability.
The target authorizes the same caller again, through the assertion, with the
permission the capability already requires. For the embedded client the
target authorizes the configured caller: its roles are resolved on the
target, never taken from the forwarding node.

## Versioning

No wire version changes. The error details, the REST `code`/`metadata` fields
and the MCP `reason`/`metadata` fields are additive; a client that ignores
them reads the answer or the error message alone. `forward.OnBehalf` uses the
frame format of every other QUIC method.
