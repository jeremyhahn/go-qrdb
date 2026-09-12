# Clustering — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Forming a cluster, watching it, changing its membership, and moving shards
around when the load goes lopsided.

## Prerequisites

- **Raft mode.** Standalone has no cluster; the verbs refuse with
  `requires mode: raft`. See [../run-modes/user.md](../run-modes/user.md).
- A reachable server, client certificates, and the `admin:*` /
  `rebalance:*` permissions for what you do. Adding and removing replicas are
  separate grants from reading peers.

## Forming a cluster

Nodes find each other with one of four discovery methods, configured on the
daemon:

| Method | How peers are found | Use it when |
|---|---|---|
| `static` | a fixed list in `raft.peers` (`id=address`) | you know the members up front |
| `dns` | DNS SRV at `{service}.{proto}.{domain}` (default `_raft._udp`) | Kubernetes headless Services, or any DNS-driven fleet |
| `multicast` | authenticated UDP announcements | a flat LAN with no DNS |
| `token` | registration with a shared-token HTTP discovery service | etcd-style bootstrapping |

**Multicast needs a secret you choose.** Announcements are HMAC-authenticated
and there is no default PSK — without one the method does not run. That is
deliberate: a shipped default secret is not authentication.

Kubernetes needs no special method — point `dns` at a headless Service.

Bootstrap runs: discover peers → validate addresses → add self → check quorum →
start the shard. If discovery finds too few peers, bootstrap fails at the quorum
check rather than starting a cluster that cannot commit.

### In FIPS 140-3 mode, do not use the Noise bootstrap method

If you run the daemon with `GODEBUG=fips140=only` (or `fips140=on`), the Noise
NK enrollment method is unavailable: its handshake uses X25519, which is not a
FIPS 140-3 approved key-agreement scheme.

Nothing about forming a cluster is blocked by this, but the way peers enroll
changes, and the node tells you so at startup:

- **The first node still advertises.** It comes up without a Noise listener and
  logs a WARN saying so. Joining nodes reach its `/v1/ca/bootstrap` endpoint on
  the admin port instead.
- **Joining nodes must use an approved method**: `--bootstrap-spki-pin` (with
  `--bootstrap-addr`), `--bootstrap-dane-dns`, or `--bootstrap-direct-url`.
- **`--bootstrap-noise-key` on its own is refused at startup**, with an error
  that names FIPS and lists the methods to use instead. If you give it
  alongside an approved method, the approved one is used and the Noise entry is
  dropped with a WARN.

`--bootstrap-noise-port` and `--bootstrap-noise-host` are still accepted and
validated; they simply configure a listener that is not opened.

## Watching the cluster

```bash
qrdb admin health            # is this node alive?
qrdb admin status            # detail
qrdb admin peers             # who is in this shard?
qrdb admin topology --output json
```

`admin health` is served on the one public route in the system, so a load
balancer can probe a node without credentials.

## Changing membership

**By hand:**

```bash
qrdb admin member add node4.example.com:63001 --shard-id 1 --replica-id 4
qrdb admin member remove 4 --shard-id 1
```

**Automatically.** The membership manager reconciles the Raft group as nodes
come and go, using leases and watches: each node holds a lease-backed member
entry and refreshes it; when a node dies the lease expires, the entry
disappears, and the leader reconfigures.

It is **off by default**:

```bash
qrdbd start --dynamic-membership --cluster-min-peers 3
```

`--cluster-min-peers` is the quorum floor: the manager **refuses** to remove a
replica if doing so would drop active membership below quorum. That guard is
the reason automatic membership is usable at all — without it a transient
network blip becomes a destroyed cluster.

## Taking a node out of service

```bash
qrdb admin drain
```

Then remove it from the shard if it is going away for good.

## Rebalancing

Look before you move. The report tells you whether there is a problem:

```bash
qrdb rebalance report --output json
```

`imbalance_score` is the number to watch; `hotspots` names the nodes carrying
too much.

Generate a plan, read it, then run it:

```bash
qrdb rebalance plan --dry-run --max-moves 10 --output json > plan.json
# read plan.json — each move names the shard, the source, the target and the bytes
qrdb rebalance execute < plan.json
```

`--target-balance 0` asks for perfectly balanced; a few percent is usually a
better goal than churning the cluster for the last point. `--max-moves` bounds
the disruption.

The result reports moves completed, moves failed, and the imbalance you actually
ended up with.

## From the SDK

```go
cc := client.Cluster()
members, _ := cc.Members(ctx, shardID)
leader, ok, _ := cc.Leader(ctx, shardID)

ch, _ := cc.WatchMembership(ctx, shardID)
for ev := range ch {
	// ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
}
```

`WatchMembership` polls rather than streaming, so it works on every transport
including REST.

## Troubleshooting

**Bootstrap fails at the quorum check.** Discovery did not find enough peers.
Check the method's inputs: the `raft.peers` list, the DNS SRV record, the
multicast secret matching on every node, or the token service.

**Multicast finds nothing.** Every node needs the *same* operator-chosen PSK,
and there is no default. A mismatch looks exactly like "no peers on the
network".

**Automatic removal did not happen.** Either dynamic membership is off (it is by
default), the local node is not the leader (reconfiguration is leader-only), or
the quorum guard refused because removing that replica would drop you below
`--cluster-min-peers`. The last one is the guard working.

**A node came back and got added twice.** It did not — joins and leaves are
idempotent. The manager checks `IsMember` and skips both directions.

**Membership looks stale after a network partition heals.** On startup and after
a watch re-subscribe the manager reconciles against a full membership snapshot,
so both joins and leaves that happened while it was not watching converge. Give
it a poll interval.

**`WatchMembership` events lag.** It polls at a fixed interval by design, so it
works over unary-only transports. It is not a streaming feed.

**Rebalance execute failed some moves.** The result counts them and reports the
final imbalance. Re-plan and re-run — the plan is computed from current state,
so a second pass picks up where the first stopped.

**A joining node reports that Noise NK is not FIPS 140-3 approved.** The daemon
is running in FIPS mode, where that method cannot be used. Configure
`--bootstrap-spki-pin` (with `--bootstrap-addr`), `--bootstrap-dane-dns` or
`--bootstrap-direct-url` instead — see
[Forming a cluster](#in-fips-140-3-mode-do-not-use-the-noise-bootstrap-method).
The refusal happens before the node dials anything, so nothing was written.

**A FIPS first node logs no Noise public key.** That is correct there. It logs a
WARN naming FIPS and the admin port instead; point joining nodes at
`/v1/ca/bootstrap` with an approved method. `noise-key.hex` is not written in
FIPS mode, because it would be a key produced by a primitive the deployment has
declared it may not use.

**Cluster verbs return "requires mode: raft".** The server is standalone.

**Member entries do not show up in `kv list`.** They live at
`__sys:cluster/members/{nodeID}`, inside the internal namespace, hidden from
user scans on purpose.
