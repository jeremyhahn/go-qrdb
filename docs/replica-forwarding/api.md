# Replica Forwarding — API

> **Status: IN PROGRESS — REST, gRPC, Unix socket, QUIC and MCP shipped;
> embedded pending.** · Scope: go-qrdb · Last verified: 2026-10-05

Replica forwarding adds **no new routes, RPCs or tools**, and one QUIC method,
`forward.OnBehalf`, that only a cluster node may call. It changes what every
existing **database-scoped, unary** capability does when the request reaches a
node that holds no replica of the database. Instead of refusing, the node
forwards the request to a replica and returns that node's answer. That
applies to KV, MVCC, batch, transactions, SQL, collections, time series,
vectors and leases, on every network transport.

## Per-transport form

| Transport | Forwarded over | Carrier of the on-behalf assertion | Marker on the answer |
|---|---|---|---|
| REST | REST | headers `X-Qrdb-On-Behalf-Assertion`, `X-Qrdb-On-Behalf-Sig` | header `X-Qrdb-Forwarded-To: <replica id>` |
| gRPC | gRPC | metadata `x-qrdb-on-behalf-assertion`, `x-qrdb-on-behalf-sig` | header metadata `x-qrdb-forwarded-to` |
| Unix socket | gRPC | as gRPC | as gRPC |
| QUIC | QUIC | the `forward.OnBehalf` request: an envelope of the caller's method and payload, and the assertion | none: an answer frame has no metadata |
| MCP | MCP (streamable HTTP) | the REST headers, on every HTTP request of the forward's session | `_meta["x-qrdb-forwarded-to"]` on the tool result |
| Embedded | QUIC | *(pending)* | *(pending)* |

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
| Call on the shared system shard | no: unchanged (REST's existing write forwarding to the system leader still applies) |
| Call that arrived forwarded (carries an assertion) | no: hop limit 1 |

## Status mapping

| Condition | REST | gRPC / Unix | QUIC | MCP |
|---|---|---|---|---|
| Forwarded and answered | the target's status | the target's code | the target's status | the target's result |
| No target served (none reachable, every one refused, forwarding off, or a stream) | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |
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

## Permissions

None new. Ingress authorizes the caller for the capability as it does today.
The target authorizes the same caller again, through the assertion, with the
permission the capability already requires.

## Versioning

No wire version changes. The error details, the REST `code`/`metadata` fields
and the MCP `reason`/`metadata` fields are additive; a client that ignores
them behaves as before, except that it now gets an answer instead of a
refusal. `forward.OnBehalf` is a new QUIC method; the frame format is
unchanged.
