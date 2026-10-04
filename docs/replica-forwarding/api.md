# Replica Forwarding — API

> **Status: PROPOSED — contract for implementation.** · Scope: go-qrdb ·
> Last verified: 2026-10-05

Replica forwarding adds **no new routes, RPCs, frames or tools**. It changes
what every existing **database-scoped, unary** capability does when the
request reaches a node that holds no replica of the database. Instead of
refusing, the node forwards the request to the database's replica leader and
returns that node's answer. That applies to KV, MVCC, batch, transactions,
SQL, collections, time series, vectors and leases. It applies on every
transport, as the table below shows.

## Per-transport form

| Transport | Forwarded over | Carrier of the on-behalf assertion | Marker on the answer |
|---|---|---|---|
| REST | REST | headers `X-Qrdb-On-Behalf-Assertion`, `X-Qrdb-On-Behalf-Sig` | header `X-Qrdb-Forwarded-To: <replica id>` |
| gRPC | gRPC | metadata `x-qrdb-on-behalf-assertion`, `x-qrdb-on-behalf-sig` | trailer `x-qrdb-forwarded-to` |
| Unix socket | gRPC | as gRPC | as gRPC |
| QUIC | QUIC | frame metadata `on-behalf-assertion`, `on-behalf-sig` | frame metadata `forwarded-to` |
| MCP | MCP (streamable HTTP) | the REST headers on the HTTP request | the REST header |
| Embedded | QUIC | frame metadata, as QUIC | `transport.ForwardedTo(ctx)` reports the replica id |

A client never sets the assertion headers. A request that carries them from
anything but a verified cluster node certificate is refused with
`PERMISSION_DENIED`, as REST refuses it today.

## Which calls forward

| Call shape | Forwards |
|---|---|
| Unary call on a `raft`-isolated database | yes |
| Server-streaming or bidirectional call (watch, change feed, streamed scans, `Watch*` in embedded) | **no**: refused with `NotAReplicaError` and the replica set |
| Call on the shared system shard | no: unchanged (REST's existing write forwarding to the system leader still applies) |
| Call that arrived forwarded (carries an assertion) | no: hop limit 1 |

## Status mapping

| Condition | REST | gRPC / Unix | QUIC | MCP |
|---|---|---|---|---|
| Forwarded and answered | the target's status | the target's code | the target's status | the target's result |
| No target, every target unreachable, or a stream | 503 | `UNAVAILABLE` | `StatusUnavailable` | `UNAVAILABLE` |
| Target refuses the assertion | 403 | `PERMISSION_DENIED` | `StatusPermissionDenied` | `PERMISSION_DENIED` |

The 503 carries the replica set as structured data, so a client that must
redial does not parse a message:

```json
// REST error body
{ "error": "placement: node 192.0.2.3:63001 is not a replica of acme/orders (shard 1000); its replicas are on 192.0.2.1:63001",
  "code": "not_a_replica",
  "replicas": [ { "replica_id": 1, "node_id": "192.0.2.1:63001",
                  "endpoints": { "rest": "db1.example.com:63002", "grpc": "db1.example.com:63003",
                                 "quic": "db1.example.com:63004" } } ] }
```

On gRPC the same data is an `ErrorInfo` detail (reason `NOT_A_REPLICA`). On
QUIC it is the error frame's metadata, on MCP the JSON-RPC error `data`, and
on embedded it is the `*placement.NotAReplicaError` value itself.

## Permissions

None new. Ingress authorizes the caller for the capability as it does today.
The target authorizes the same caller again, through the assertion, with the
permission the capability already requires.

## Versioning

No wire version changes. The new metadata keys and the `replicas` error field
are additive. A client that ignores them behaves as before, except that it
now gets an answer instead of a 503.
