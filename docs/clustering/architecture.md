# Clustering — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Covers peer discovery, cluster formation, membership, node administration and
shard rebalancing. **Raft mode only** — see
[../run-modes/](../run-modes/architecture.md).

## Problem

Turning a set of processes into a Raft cluster needs three things a consensus
library does not provide: finding the peers, deciding when there are enough of
them to start, and keeping the membership correct as nodes come and go. Leaving
those to each deployment means each one invents its own, subtly wrong.

## Shape chosen

Discovery, bootstrap orchestration and membership live **here**, above the
consensus primitives.

## Ownership

| Concern                       | Home                                  |
|-------------------------------|---------------------------------------|
| Raft primitives (`StartShard`, `GetLeaderID`, replica add/remove) | go-quicraft |
| Discovery engine (`pkg/discovery`) + bootstrap orchestration (`pkg/cluster`) | **go-qrdb** |
| SDK cluster client (`Cluster()`) | **go-qrdb** SDK |
| Service discovery / registry  | go-quicbaas (on go-qrdb lease+watch+KV)|

The discovery engine and bootstrap orchestration live in go-qrdb, the layer
that actually owns the cluster. go-quicraft is reduced to Raft primitives only.

That split is the central decision: go-quicraft is reduced to Raft primitives
(`StartShard`, `GetLeaderID`, replica add/remove) and go-qrdb owns the layer
that actually knows what a cluster is.

**Cluster discovery is not service discovery**, and not trust bootstrap.
Registering and resolving *application* service instances is built by downstream
consumers (go-quicbaas) on this store's lease + watch + KV primitives. Noise /
SPKI / DANE CA establishment is [../trust-bootstrap/](../trust-bootstrap/).

## Discovery engine

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

The `Manager` chains methods with retry, returning the first non-empty result.

## Bootstrap orchestration

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
[12-configuration.md](../12-configuration.md#discovery).

## Member identities

A replica with a Raft transport admits a member by its **node identity** and by
nothing else. The identity is `spki:` followed by the hex SHA-256 of the
member's transport-certificate public key, so it survives a certificate renewal
on the same keypair and changes only with the keypair. An address is routing
metadata; a certificate from the cluster CA is not, on its own, a claim to any
replica ID. This is go-quicraft's rule, and go-qrdb supplies the identities it
needs.

Where each identity comes from:

| Identity | Source |
|---|---|
| This replica's own | derived from the certificate it presents (`WithRaftMTLS` for the SDK node, `--tls-cert-file` for `qrdbd`) |
| Every other replica's | `raft.member_identities` (`--member-identities`), `replicaID=identity` specs the operator collects out of band |

A peer's identity is **never learned from the peer's own connection**: that
would let any holder of a cluster certificate register itself as any replica,
which is the attack identities close. The operator reads each node's identity
from the certificate the CA issued (`qrdbd node-identity --cert FILE`, or
`node.MemberIdentityFromPEM` in the SDK) before any node starts.

A founding replica must name every member of the initial membership; a joining
replica must name at least one existing member it will accept its bootstrap
from. The discovery method is irrelevant: `raft.member_identities` is keyed by
replica ID and applies to whatever membership discovery produces. A configured
entry for the local replica that disagrees with its certificate is refused
before the host is built. A single-node cluster has no transport and needs none.

### Adding replicas: identities come from the committed membership

**go-quicraft commits every member's identity in the shard's membership**
(`Membership.Identities`, read with `SyncGetShardMembership`) — seeded at
bootstrap from the founders' `MemberIdentities`, and changed only by committed
`*WithIdentity` config changes. go-qrdb keeps no identity store of its own; it
reads identities from there. Every add path — `member add` / `AddReplica` on
all six transports, rebalance, dynamic membership, the placement reconciler
admitting and promoting followers, founding a multi-replica dedicated
(IsolationRaft) database, and the admin shard start (`StartShard`, whose
members' identities are built exactly as a dedicated database's founders'
are) — resolves the identity it commits the same way:

| The replica being added | Identity committed |
|---|---|
| already a member of the target shard (a promotion, a re-add) | the one that shard committed for it |
| on a host that is a member of the system shard (every dedicated-shard replica, every rebalance target, a new system-shard replica ID at a member's address) | the one the system shard committed for that host, found by its Raft address |
| on a brand-new node no committed membership names | **none available — refused** unless the operator supplies it |

The operator may always supply the identity (the optional identity on
`member add`); if it contradicts one the cluster already committed for that
replica or host, the add is refused — for any shard, the system shard
included. Every replica the system shard places at the address is consulted:
if they carry different identities, the address does not name one host the
cluster agrees on and the add is refused as ambiguous (a conflict: `409` /
`FailedPrecondition`). An add naming a replica ID the membership already places
at a different address is refused the same way: the committed identity belongs
to that other host. A target shard this node has not loaded (a dedicated
shard being founded) contributes nothing; one that exists but cannot be read is
refused as unavailable (`503` / `Unavailable`) rather than resolved from the
host alone.

**Rebalance never moves the system shard.** Every host is a member of it — its
membership is where each host's identity is committed — so any destination the
cluster can vouch for already hosts a replica, and one that does not is a
brand-new node, which joins through `member add` with its identity. A planned
system-shard move is refused with `*service.RebalanceSystemShardMoveError`, a
move to a host that already hosts the shard with
`*service.RebalanceTargetHostsReplicaError`, and a destination without a
committed identity with `*service.MemberIdentityError`; each is logged and
counted as a failed move. A brand-new node's identity has to come
from outside the cluster for the reason above: the new node's own connection is
exactly what cannot be trusted to say which replica it is. So adding a
brand-new node to a running cluster is two steps — print its identity with
`qrdbd node-identity --cert`, and pass it to `member add`; every later add of a
replica on that host is automatic.

## Dynamic membership (opt-in)

`cluster.MembershipManager` reconciles the Raft group as nodes join and leave,
realizing the etcd-style **lease + watch** membership pattern on go-qrdb's own

## Dynamic membership — opt-in


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

## SDK `Cluster()` client

_ = cc.AddReplica(ctx, shardID, id, raftAddr, "") // delegates to AddMember; "" = committed identity
_ = cc.RemoveReplica(ctx, shardID, id)           // delegates to RemoveMember
ch, _ := cc.WatchMembership(ctx, shardID)        // polls + emits deltas
for ev := range ch {
    // ev.Members, ev.Added, ev.Removed, ev.LeaderChanged
}
```

`WatchMembership` polls (rather than using a dedicated streaming RPC) so it
works over every transport, including the unary-only REST transport. The poll
cadence is `DefaultMembershipPollInterval`.

### Alternatives rejected

- **Discovery inside go-quicraft.** Rejected: consensus and cluster formation
  are different concerns with different dependency graphs, and putting
  discovery below the store meant the store could not use its own primitives to
  do it.
- **A Kubernetes-specific discovery method.** Rejected: `dns` against a headless
  Service covers it. A dedicated method would be one more thing to maintain for
  no capability.
- **A default multicast secret.** Rejected outright: multicast announcements are
  HMAC-SHA256 authenticated with an **operator-chosen** PSK and there is no
  default. A shipped default secret is not authentication.
- **Dynamic membership on by default.** Rejected: automatic Raft reconfiguration
  is exactly the machinery that can destroy a cluster during a transient
  liveness blip. It is opt-in, leader-only, and quorum-guarded.
- **Refusing to start a first node in FIPS 140-3 mode.** Rejected. The Noise NK
  enrollment listener cannot run there — its X25519 handshake is not an approved
  key-agreement scheme — but nothing about being a first node requires Noise:
  SPKI-pinned TLS, DANE and direct HTTPS all fetch the same CA bundle from
  `/v1/ca/bootstrap`, and the operator never chose a method in the first place
  (there is no advertise-method option to choose). Refusing would mean go-qrdb
  cannot form a multi-node cluster under FIPS at all, over a method a FIPS
  deployment could not have used. So Noise is excluded, loudly, and the node
  advertises the approved methods.
- **Falling back from Noise without saying so.** Rejected on both sides. A
  bootstrap method downgraded in silence is its own defect: the operator is
  running a smaller enrollment surface than the documentation describes, and the
  only other thing that ever reports it is a peer's join failing on a different
  machine. Every exclusion logs at WARN naming FIPS, and the case with nothing
  left to select — a join configured with Noise and nothing else — is refused
  outright at configuration time.
- **Compiling the Noise transport out under a `fips` build tag.** Rejected, even
  though go-truststrap offers exactly that tag. It would produce two binaries
  whose enrollment surfaces differ and whose difference is invisible in the
  artifact, while `GODEBUG=fips140` is a run-time switch. One binary that reads
  the runtime it was started in is the smaller claim.
- **A revision-resumed membership watch.** Rejected in favour of a full
  `ListMembers` snapshot on startup and after a watch re-subscribe. It is
  O(cluster size), membership sets are small, and reconnects are rare — the
  simple rescan converges in both directions.
- **A dedicated streaming RPC for `WatchMembership`.** Rejected so the facade
  works over every transport including unary-only REST; it polls at
  `DefaultMembershipPollInterval`.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Discovery methods and `Manager` | `pkg/discovery` | go-qrdb |
| Bootstrap orchestration, `MembershipManager` | `pkg/cluster` | go-qrdb |
| Node admin (health, status, peers, drain, snapshot, shards, topology) | `pkg/service` admin | go-qrdb |
| Shard rebalancing | `pkg/placement`, `pkg/service` | go-qrdb |
| Raft primitives, leader election, log | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Admin()`, `Client.Cluster()`, rebalance client) | go-qrdb |

## Trust boundaries

- **A Raft member is admitted by its node identity.** Holding a certificate
  from the cluster CA is not enough to speak for a replica ID; the identity for
  that ID must have been supplied by the operator (see
  [Member identities](#member-identities)).
- **Multicast discovery requires an operator PSK** and authenticates
  announcements with HMAC-SHA256. Without a secret the method does not run.
- **Membership entries live under `__sys:cluster/members/{nodeID}`**, inside the
  internal namespace, so they are hidden from user KV scans.
- **Every admin verb carries its own permission** — `admin:health`,
  `admin:status`, `admin:peers`, `admin:add_member`, `admin:remove_member`,
  `admin:snapshot`, `admin:drain`, `admin:capabilities`, `admin:start_shard`,
  `admin:shard_state`. Adding and removing replicas are separately gated from
  reading peers.
- **`GET /health` is a public route** so a load balancer can probe an
  unauthenticated node; nothing else in this feature is.
- **Rebalance `execute` physically moves shards** and is gated apart from the
  read-only `stats`/`plan`/`report`.

## What this feature deliberately does not do

- **No service discovery.** Application service registries are a consumer
  concern built on lease + watch + KV.
- **No trust bootstrap.** See [../trust-bootstrap/](../trust-bootstrap/).
- **No FIPS-approved replacement for Noise NK.** Noise_NK_25519 has no approved
  DH to substitute; in FIPS 140-3 mode the method is excluded rather than
  swapped. See [../seal/fips140-mode.md](../seal/fips140-mode.md).
- **No automatic rebalancing.** Plans are generated and executed deliberately.
- **No cross-cluster federation.**
