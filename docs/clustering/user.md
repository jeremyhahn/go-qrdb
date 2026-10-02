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

### Every node needs the other nodes' identities

A replica accepts Raft traffic only from a member whose **identity** it was
given. An identity is a hash of the public key in a node's transport
certificate, so it is collected from the certificates, not from the network:

1. Issue each node its certificate from the cluster CA.
2. Print each node's identity: `qrdbd node-identity --cert node2-cert.pem`.
3. Give every node the identities of the others, by replica ID:
   `raft.member_identities: "2=spki:…,3=spki:…"` (or `--member-identities`).
   A node's own entry can be left out; it is derived from its certificate.

A founding node must list every member of the initial cluster; a node started
with `raft.join: true` must list at least one existing member. Renewing a
certificate on the same key keeps the identity; a new key changes it.

A single-node cluster has no network transport and needs no identities.

### Adding a node: joining through enrollment is unavailable in this release

**A node started with bootstrap flags refuses to start** in this release:
`--bootstrap-noise-key`, `--bootstrap-spki-pin`, `--bootstrap-dane-dns` and
`--bootstrap-direct-url` all select the join path, and the join path now fails
with `EnrollmentUnavailableError`. The error message tells you what to do
instead. The refusal comes before the node dials anything, and nothing is
written to its data directory.

The reason is a security fix. Earlier releases enrolled a joining node by
handing it the cluster CA **private key**, which it used to sign its own
certificate. The key was served by `/v1/ca/bootstrap`, a route that needs no
credential, so anyone who could reach a first node's admin port could read it.
The key never leaves the node that holds it any more. The replacement,
where the joining node generates its key locally and a running member signs
its certificate request, ships in a following release.

**To add a node now**, issue its certificate yourself on the node that holds
the cluster CA (`ca.pem` and `ca-key.pem` in its data directory):

1. Generate an ECDSA P-256 key and a certificate signed by the cluster CA,
   with the node's SPIFFE identity as a URI SAN:
   `spiffe://<trust-domain>/node/<raft-address>`. Give it both the
   `serverAuth` and `clientAuth` extended key usages, and add the node's DNS
   names and IP addresses as SANs.
2. Copy `ca.pem`, the new certificate and the new key to the new node, and
   **never `ca-key.pem`**.
3. Start the new node with `--tls-ca-file`, `--tls-cert-file` and
   `--tls-key-file` pointing at them, and without bootstrap flags.
4. Add it to the Raft group as usual (`qrdb admin add-member`, or dynamic
   membership).

### If your cluster was formed by an earlier release, rotate its CA

Treat the existing cluster CA key as **compromised**. It was readable without
authentication from every first node's admin port, and every node that joined
over SPKI, DANE or direct HTTPS holds a copy in `ca-key.pem`. Until the CA is
rotated, restrict network access to every node's admin port.

Rotate the cluster CA:

1. On the node that will hold the new CA, generate a new CA key and
   certificate. Keep the key on that node only.
2. Issue a new node certificate for every member from the new CA, as in
   [Adding a node](#adding-a-node-joining-through-enrollment-is-unavailable-in-this-release).
3. Build a trust bundle holding **both** CA certificates. Restart each member,
   one at a time, with `--tls-ca-file` set to that bundle and its new
   certificate and key. Both old and new certificates verify during this step.
4. Once every member presents a certificate from the new CA, restart each
   member with `--tls-ca-file` set to the new CA alone.
5. Delete `ca-key.pem` from every node except the one holding the new CA.
6. Reissue every client and Security Officer certificate that chains to the
   old CA.

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
qrdb admin member add node4.example.com:63001 --shard-id 1 --replica-id 4 \
  --identity "$(qrdbd node-identity --cert node4-cert.pem)"
qrdb admin member remove 4 --shard-id 1
```

A brand-new node needs `--identity` once: the cluster has never committed its
identity, and cannot learn it safely from the node itself. Start the new node
with `raft.join: true` and the existing members in its
`raft.member_identities`. Once it is a member, its identity is committed, and
every later add of a replica on that node — rebalancing, dedicated databases,
re-adding it — looks it up without you.

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

**`NewClusteredNode` returns once the cluster can serve.** A founder blocks
until a quorum of the founders has elected a leader and that leader has
committed in its term, so its first read succeeds. Start the founders within
about 15 seconds of each other: a founder that waits longer for its peers
fails with `*server.ShardLeaderWaitError` (no leader) or
`*server.ShardReadWaitError` (a leader that never committed), naming what it
saw. The constructor honours its `ctx`: cancel it to give up early. A sealed
founder waits the same way in `Unseal`; one that fails or is cancelled stays
sealed, and the next `Unseal` boots it.

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

## Upgrading a cluster

Upgrade one node at a time, as for any rolling upgrade. A release that
introduces a new replicated command -- guarded writes, which make a database
drop atomic with every write, are the first -- does not use it anywhere in the
cluster until every node, not just a given Raft group's current members, runs
a release that advertises it. A group whose current members all advertise the
feature is not enough on its own: a later membership change could still bring
in a node on an older release, and that node is caught up by REPLAYING the
group's committed log, which would already contain the new command. Gating on
cluster-wide activation rather than a group's current membership is what
keeps a half-upgraded cluster working and never diverging.

When every node advertises the feature, the cluster **activates** it, and
activation is permanent. **After activation, no node may be started or added on
an older release** -- neither an existing node rolled back nor a new one. A
release that knows about feature activation refuses to start with an error
naming the feature, and the cluster refuses to add a replica on a node that does
not advertise it (see below), but a release from before activation existed can
do neither; roll forward instead.

A new node is added only once it advertises every feature the cluster uses.
Membership changes, scale-ups and rebalances that would place a replica on a
node that does not are refused with an error naming the node, the feature, and
the missing advertisement (`featuregate.UnsupportedReplicaError`; REST 409,
gRPC and QUIC `FAILED_PRECONDITION`). A joining node's advertisement is
published by its enrollment; a `qrdbd` node otherwise advertises only once it
is a member. So after activation, `qrdb admin member add` of a node that has
not advertised is refused -- until enrollment publishes its advertisement, plan
capacity additions before the last node of a rolling upgrade. From the Go SDK, a node can instead be registered through
dynamic membership over a client of the running cluster, with its features in
the registration.

Before activation, rolling a node back is safe. A node stopped gracefully
withdraws its advertisement at once. After a crash its advertisement lapses
only when its lease expires, so **after a crash, wait out the lease TTL (10
seconds by default) before starting the node on an older release**.

### The Noise bootstrap wire is not version-skewed

The first node of a cluster runs the Noise NK bootstrap **server**
(`pkg/clusterenroll`'s `Advertiser`, started on the boot that generates or
loads the cluster CA) that a joining node's enrollment dials as a **client**.
Both halves are this codebase's own call into `go-truststrap`, so a bump to
`go-truststrap` that changes the wire format is a qrdb-to-qrdb compatibility
question, not a question about an external service. A release whose
`go-truststrap` adds a suite-negotiation header to the first bootstrap frame
cannot complete a handshake against a peer running the pre-negotiation wire,
in either direction: **a mixed-version cluster cannot enroll a new member
until every node that might advertise is on the same `go-truststrap` wire.**
This is independent of, and checked separately from, the replicated-command
feature-gating described above — a bootstrap wire mismatch fails before any
Raft log entry is involved. Finish rolling every node forward before adding
a new member if the release notes call out a bootstrap wire change.

## Troubleshooting

**A node refuses to start: "the cluster has activated feature ...".** The
cluster activated a feature this node's release does not implement. Install a
release that does; activation cannot be undone. A node embedded in an
application through the Go SDK cannot refuse to start after it is built:
it logs the same error, takes no part in the feature, and reports its health
as `degraded` until it is upgraded.

**Bootstrap fails at the quorum check.** Discovery did not find enough peers.
Check the method's inputs: the `raft.peers` list, the DNS SRV record, the
multicast secret matching on every node, or the token service.

**A node refuses to start: `no node identity in Config.MemberIdentities`.** The
node was not given the identity of the replica the message names. Add it to
`raft.member_identities` — see
[Every node needs the other nodes' identities](#every-node-needs-the-other-nodes-identities).

**A node refuses to start: `member identity for local replica … is configured
as … but its certificate presents …`.** The entry for the node itself names a
different key than the certificate it was started with — usually a copy-paste
from another node, or a certificate reissued on a new key. Remove the local
entry or correct it.

**`member add` says there is no committed member identity for the replica.**
The node is new to the cluster. Print its identity with
`qrdbd node-identity --cert <its certificate>` and pass it with `--identity`.

**`member add` says the identity contradicts the committed identity.** The
cluster already committed a different key for that replica or host — usually a
copy-paste from another node, or a node reissued on a new key. Check the
certificate; a node on a new key must be removed and re-added.

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

**A joining node refuses to start with "joining an existing cluster is
unavailable in this release".** This is expected in this release: bootstrap
flags select a join, and joins through enrollment are unavailable because the
CA key is no longer distributed. Issue the node's certificate on the node that
holds the cluster CA and start it with the explicit TLS file flags. See
[Adding a node](#adding-a-node-joining-through-enrollment-is-unavailable-in-this-release).

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
