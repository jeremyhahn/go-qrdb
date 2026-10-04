# Replica Forwarding — User Guide

> **Status: IN PROGRESS — REST, gRPC, Unix socket, QUIC and MCP shipped;
> embedded pending.** · Scope: go-qrdb · Last verified: 2026-10-05

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

## Watches and other streams

A watch, a change feed or any streamed call is **not** forwarded. Through a
node that holds no replica it fails at once with "not a replica", and the
error lists the replica nodes. Reconnect the stream to one of them.

MCP's `qrdb_watch` is different: it collects events for a bounded time and
answers once, so it is forwarded like any other tool.

## When a replica goes down

A request already on its way to a replica as it died can fail with
"unavailable": the forwarding node cannot know whether the replica applied it.
The next request finds the replica unreachable and goes to the next one, or,
when there is none, is refused with the replica set. A node never answers
from a database it holds no replica of.

## Troubleshooting

| You see | Meaning | Do |
|---|---|---|
| "not a replica … its replicas are on …" on an ordinary request | no replica could be reached from the node you asked, or forwarding is off on it | check the replicas are up; check `replica_forwarding.enabled` on that node |
| "not a replica" on a watch | streams are never forwarded | reconnect the watch to a node the error lists |
| `replica forwarding off: no CA pool` in a node's log | the node has no CA to verify a replica with | configure the cluster CA (`--tls-ca-file`), or `server.WithCACertPool` when you embed the node |
| permission denied that you do not get on the replica directly | the replica refused the forwarding node's assertion: its certificate does not chain to the cluster CA the replica trusts | check every node's TLS configuration; on operator-supplied TLS, every node must trust the same peer CA |
| "unavailable … forward over … failed" on a write | the forward failed after it may have reached the replica, which may have applied it | read the key back before retrying, as for any write that timed out |
