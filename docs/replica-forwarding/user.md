# Replica Forwarding — User Guide

> **Status: PROPOSED — contract for implementation.** · Scope: go-qrdb ·
> Last verified: 2026-10-05

A database with its own Raft group lives on a few nodes of the cluster: its
replicas. You do not need to know which. Send a request to any node, over any
protocol, and a node that holds no copy passes the request on to the node that
does. You get that node's answer, under your own identity and permissions,
exactly as if you had asked it directly.

## What you need

Nothing beyond a running cluster. Forwarding is on by default in raft mode.

## What changes for you

- Point clients at any node, or at a load balancer in front of all of them.
- Your permissions are checked twice: on the node you reach, and again on the
  node that serves the request. Both checks use your identity. A request you
  are not allowed to make is refused on the first node.
- A forwarded answer carries a `forwarded-to` marker (a header, a gRPC
  trailer, or frame metadata) naming the replica that served it. Use it when
  you are diagnosing latency.

## Watches and other streams

A watch, a change feed or any streamed call is **not** forwarded. Through a
node that holds no replica it fails at once with "not a replica", and the
error lists the replica nodes and their endpoints. Reconnect the stream to one
of them.

## Troubleshooting

| You see | Meaning | Do |
|---|---|---|
| "not a replica … its replicas are on …" on an ordinary request | no replica could be reached from the node you asked, or forwarding is off on it | check the replicas are up; check `cluster.replica_forwarding` on that node |
| "not a replica" on a watch | streams are never forwarded | reconnect the watch to a node the error lists |
| permission denied that you do not get on the replica directly | the replica refused the forwarding node's assertion: its certificate does not chain to the cluster CA the replica trusts | check every node's TLS configuration; on operator-supplied TLS, every node must trust the same peer CA |
| a forwarded write timed out | the replica may have applied it | read the key back before retrying, as for any write that timed out |
