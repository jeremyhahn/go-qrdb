<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Peer Discovery & Cluster Membership

This document describes how a go-qrdb cluster forms and how its Raft membership
is maintained over time. Two distinct concerns are covered:

1. **Peer discovery** — how a node finds the other Raft peers it should form or
   join a shard with, at startup.
2. **Cluster membership** — how membership is reconciled at runtime as nodes
   join and leave.

> **Scope note.** This is *peer* discovery for Raft cluster formation. It is
> distinct from **service discovery** (registering and resolving application
> service instances), which is built on go-qrdb's lease + watch + KV primitives
> by downstream consumers such as go-quicbaas. It is also distinct from **trust
> bootstrap** (Noise/SPKI/DANE CA establishment) — see [bootstrap.md](bootstrap.md).

## Ownership

| Concern                       | Home                                  |
|-------------------------------|---------------------------------------|
| Raft primitives (`StartShard`, `GetLeaderID`, replica add/remove) | go-quicraft |
| Discovery engine (`pkg/discovery`) + bootstrap orchestration (`pkg/cluster`) | **go-qrdb** |
| SDK cluster client (`Cluster()`) | **go-qrdb** SDK |
| Service discovery / registry  | go-quicbaas (on go-qrdb lease+watch+KV)|

The discovery engine and bootstrap orchestration live in go-qrdb, the layer
that actually owns the cluster. go-quicraft is reduced to Raft primitives only.

## Discovery engine (`pkg/discovery`)

A discovery *method* satisfies a small interface:

```go
type Method interface {
    Discover(ctx context.Context) ([]Peer, error)
    Name() string
    Stop() error
}

type Peer struct {
    NodeID  uint64
    Address string
}
```

Four methods ship:

| Method      | Mechanism                                                                |
|-------------|--------------------------------------------------------------------------|
| `static`    | Fixed member list from `raft.peers` (`id=address` specs).                |
| `multicast` | HMAC-SHA256-authenticated UDP multicast announcements (opt-in; requires an operator-chosen PSK of at least `discovery.MinSharedSecretLen` (16) bytes — there is no default secret). |
| `dns`       | DNS SRV lookup at `{service}.{proto}.{domain}` (default `_raft._udp`).   |
| `token`     | etcd-style registration with a shared-token HTTP discovery service.      |

Kubernetes deployments use `dns` against a headless Service — no Kubernetes-
specific method is needed.

The `Manager` chains methods with retry, returning the first non-empty result.

## Bootstrap orchestration (`pkg/cluster`)

`pkg/cluster` turns discovered peers into a running Raft shard. The reusable
entry point is transport-neutral and embeddable:

```go
opts := cluster.Options{
    Method:      "multicast",
    NodeID:      replicaID,
    RaftAddress: raftAddr,
    MulticastSecret: secret,
}
err := cluster.BootstrapShard(ctx, node, opts, shardID, createFn, shardCfg)
```

`BootstrapShard` runs: **discover → validate addresses → add self → check
quorum → build member map → `StartShard`**. The daemon (`qrdbd`) maps its CLI
flags / YAML into `cluster.Options`; embedders (go-xkms, go-quicbaas) do the
same from their own configuration. Helpers `BuildDiscovery`, `ParsePeers`, and
`IsSingleNode` are exported for finer-grained use.

Configuration lives under the `discovery` block — see
[12-configuration.md](12-configuration.md#discovery).

## Dynamic membership (opt-in)

`cluster.MembershipManager` reconciles the Raft group as nodes join and leave,
realizing the etcd-style **lease + watch** membership pattern on go-qrdb's own
primitives:

- Each node registers a member entry (`__sys:cluster/members/{nodeID}`, in the
  system namespace so it is hidden from user KV scans) backed by a
  **lease** and refreshes it on a keepalive interval.
- A node's death stops the keepalive; the lease expires; the entry is deleted;
  a **prefix watch** surfaces the removal.
- On the **leader**, join/leave events drive `AddMember`/`RemoveMember` on the
  shard's Raft group, applied by a single serialized worker (one in-flight
  reconfiguration per replica, latest intent wins).
- On startup and after a watch re-subscribe, the manager reconciles against a
  full membership **snapshot** (`ListMembers`) so members that joined or left
  while it was not watching are converged in both directions. The snapshot is
  O(cluster size); because membership sets are small and reconnects are rare,
  this full rescan is intentional rather than a revision-resumed watch.

It is built against two narrow interfaces (`MembershipStore` for lease-backed
entries + prefix watch, and `Reconfigurer` for the Raft add/remove + leader
lookup) so it is testable and embeddable without import cycles. The in-process
`MembershipStore` is backed by the MVCC lessor and watch hub; the same lessor
and watch subsystem also backs the SDK `Lease()`/`Watch()` surface that the
service registry uses — one source of truth, two consumers.

**Safety:** dynamic membership is **opt-in and off by default**. Raft
reconfiguration only occurs when a `Reconfigurer` is supplied and the local node
is the leader. A **quorum guard** (`MinQuorum`) refuses to remove a replica when
doing so would drop the active membership below quorum, so a transient liveness
blip cannot destroy the cluster. Joins/leaves are **idempotent** — the manager
consults `Reconfigurer.IsMember` and skips `AddMember` for replicas already in
the group (e.g. the initial bootstrap members) and `RemoveMember` for replicas
already gone. Reconfiguration retries with capped exponential backoff.

**Enabling it.** The `qrdbd` daemon wires the manager onto an in-process
(embedded) SDK client over its own service layer. Turn it on with
`--dynamic-membership` (or `discovery.dynamic_membership: true`); the
auto-removal quorum floor comes from `--cluster-min-peers` /
`discovery.min_peers`. Embedders get the same wiring from any qrdb SDK client
via `client.NewMembershipManager(self, shardID, cfg)` (or the lower-level
`client.NewMembershipStore()` / `client.NewMembershipReconfigurer()`).

## SDK `Cluster()` client

The SDK exposes cluster membership through a thin facade over the already-wired
control-plane operations — it adds no new wire RPCs:

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)          // filtered from Peers()
leader, ok, _ := cc.Leader(ctx, shardID)         // from GetShardState()
_ = cc.AddReplica(ctx, shardID, id, raftAddr)    // delegates to AddMember
_ = cc.RemoveReplica(ctx, shardID, id)           // delegates to RemoveMember
ch, _ := cc.WatchMembership(ctx, shardID)        // polls + emits deltas
for ev := range ch {
    // ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
}
```

`WatchMembership` polls (rather than using a dedicated streaming RPC) so it
works over every transport, including the unary-only REST transport. The poll
cadence is `DefaultMembershipPollInterval`.
