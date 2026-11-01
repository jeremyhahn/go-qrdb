# Replica Forwarding — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

A database with its own Raft group lives on a few nodes of the cluster: its
replicas. You do not need to know which. Send a request to any node, over any
network protocol, and a node that holds no copy passes the request on to a
node that does. You get that node's answer, under your own identity and
permissions, exactly as if you had asked it directly.

## What you need

Nothing beyond a running raft cluster. Forwarding is on by default.

## What changes for you

- Point clients at any node, or at a load balancer in front of all of them.
- Your permissions are checked twice: on the node you reach, and again on the
  node that serves the request. Both checks use your identity. A request you
  are not allowed to make is refused on the first node.
- A forwarded answer names the replica that served it: the
  `X-Qrdb-Forwarded-To` header on REST, `x-qrdb-forwarded-to` header metadata
  on gRPC and the Unix socket, and `x-qrdb-forwarded-to` in an MCP tool
  result's `_meta`. A QUIC answer frame has no metadata and carries none. Use
  it when you are diagnosing latency.

## Embedding a node

An embedded client calls the node's services in-process and presents no
credential, so the node cannot know who is calling. To let its client forward,
give the node the identity to forward as: a client certificate and its key,
issued by the cluster CA for a user of the tenant.

```go
cert, err := tls.LoadX509KeyPair("<install-dir>/orders-app.crt", "<install-dir>/orders-app.key")
n, err := node.NewClusteredNode(ctx, cfg, members, serverOpts, node.WithEmbeddedCaller(cert))
```

- The node checks that certificate on every forward, as its QUIC listener
  checks a client's, and the replica authorizes **that** identity, with its
  roles there. A revoked or expired certificate stops forwarding at once.
- The node first authorizes the call itself, against the identity the
  embedding application binds to the call's context (`auth.WithIdentity`),
  as it does for every embedded call; a call carrying none is refused as
  unauthenticated and is not forwarded. Only then is it forwarded, as the
  configured caller.
- Without `WithEmbeddedCaller`, a call to a database the node holds no
  replica of fails with "not a replica" and the replica set. The node never
  forwards as itself.
- Forwarding goes to a replica's QUIC listener, so enable QUIC on the nodes
  that hold replicas.
- The node must hold the tenant's key, as any node must to authenticate a
  caller of that tenant on any transport. An embedded node takes part in the
  cluster's tenant key transport as a `qrdbd` node does, so give it a REST
  listener and, when nodes share a host, `auth.peer_admin_urls`.

## Watches and other streams

A watch, a change feed or any streamed call is **not** forwarded. Through a
node that holds no replica it fails at once with "not a replica", and the
error lists the replica nodes. Reconnect the stream to one of them.

MCP's `qrdb_watch` is different: it collects events for a bounded time and
answers once, so it is forwarded like any other tool.

## When a replica goes down

A request already on its way to a replica as it died can fail with
"unavailable": the forwarding node cannot know whether the replica applied it.
The next request keeps trying to connect to the replica for up to half of
`replica_forwarding.timeout` (5 seconds by default), so a replica that is
restarting or briefly overloaded still answers. After that it counts as
unreachable, and the request goes to the next replica. When there is no next
replica, the request is refused with the replica set. A node never answers
from a database it holds no replica of.

## Troubleshooting

| You see | Meaning | Do |
|---|---|---|
| "not a replica … its replicas are on …" on an ordinary request | no replica could be reached from the node you asked, or forwarding is off on it | check the replicas are up; check `replica_forwarding.enabled` on that node |
| "not a replica" on a watch | streams are never forwarded | reconnect the watch to a node the error lists |
| `replica forwarding off: no CA pool` in a node's log | the node has no CA to verify a replica with | configure the cluster CA (`--tls-ca-file`), or `server.WithCACertPool` when you embed the node |
| "not a replica" from an embedded client | the node has no caller configured, or the configured certificate did not authenticate (`replicaforward: embedded call not forwarded` in its log) | configure `node.WithEmbeddedCaller` with a certificate the cluster CA issued for a user of the tenant |
| permission denied that you do not get on the replica directly | the replica refused the forwarding node's assertion: its certificate does not chain to the cluster CA the replica trusts | check every node's TLS configuration; on operator-supplied TLS, every node must trust the same peer CA |
| "unavailable … forward over … failed" on a write | the forward failed after it may have reached the replica, which may have applied it | read the key back before retrying, as for any write that timed out |
