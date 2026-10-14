# Clustering — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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

### Adding a node: member-side CSR join

A joining node generates its own key **locally** and submits a certificate
signing request (CSR) to an already-admitted, unsealed member, which signs it
with the replicated cluster CA (Key A) and returns only the signed leaf
certificate. Neither the joiner's private key nor the cluster CA's private key
ever crosses the wire, in either direction.

Joining has two independent halves, and both must be configured on the
joining node:

1. **Fetch the trust anchor** — the CA **certificate** only — using one of the
   existing bootstrap methods: `--bootstrap-noise-key` (with
   `--bootstrap-noise-addr`), `--bootstrap-spki-pin` (with `--bootstrap-addr`),
   `--bootstrap-dane-dns`, or `--bootstrap-direct-url`.
2. **Submit the CSR** to an admitted member's join listener, over one of two
   transports:
   - Noise_NK: `--bootstrap-join-addr` with `--bootstrap-join-key` (that
     member's join-listener address and public key).
   - SPKI/Direct, plain HTTPS: `--bootstrap-join-url` (that member's admin
     HTTPS URL), pinned the same way `--bootstrap-spki-pin` pins the anchor
     fetch.

   Either transport normally needs a join token: a single-use,
   identity-bound secret an admin mints for this node's identity ahead of
   time (`qrdb admin join-token mint`, below) — never inline in YAML or on
   the command line. Pass it through `--bootstrap-join-token-file` (a file
   holding it) or `--bootstrap-join-token-env` (an environment variable
   holding it). The file must be owned by the user running `qrdbd` and
   unreadable by its group or by anyone else (mode 0600 or tighter); a
   group- or world-readable file, or a value too short to be a real token,
   is refused with a typed error rather than silently accepted.

   **Omit both flags instead, and the node queues for approval** — see
   [Approving a join without a token](#approving-a-join-without-a-token)
   below.

Configuring only the first half (an anchor-fetch method with none of the
join-transport flags) is refused at startup with `EnrollmentUnavailableError`,
before any dial or write — that error is this repository's own history: it is
the full "joining is unavailable" refusal from the release where only the
anchor-fetch half existed.

**On the member doing the signing**, start it with `--cluster-join-enabled`.
This runs the Noise_NK join listener (`--cluster-join-listen-host` /
`--cluster-join-listen-port`, defaulting to `--host` and `63007`) and
registers the SPKI/Direct route (`/v1/enroll/join`) on the member's existing
admin HTTPS listener — no separate port for that transport. The route is
reachable only once the barrier is unsealed, because the cluster CA is: an
admitted member signs nothing while sealed. There is no admission-token flag
here: the member validates a presented token against whatever an admin has
minted into the replicated store, not a value it was configured with.

**Any admitted, unsealed member can be the one you point at** — not only
the node that originally generated the cluster CA. Every such member
registers `/v1/ca/bootstrap` and runs the Noise anchor listener from the
same certificate it already caches locally, so `--bootstrap-addr`,
`--bootstrap-join-url`, and `--bootstrap-noise-addr`/`--bootstrap-join-addr`
may all name whichever member is reachable, including after the original
node is gone for good. See
[architecture.md](architecture.md#every-admitted-unsealed-member-serves-the-anchor).

**Minting a token for the joining node:**

```bash
qrdb admin join-token mint node-7 --ttl 1h
# Token:      <opaque single-use token>
# ExpiresAt:  2026-10-03T18:00:00Z
```

Copy the printed token into the joining node's `--bootstrap-join-token-file`
(it is shown exactly once — there is no way to retrieve it again, only to
mint a fresh one). Minting requires permission `cluster:join_token`, held by
`Owner` and by the cluster `Admin` role by default (owner ruling,
2026-10-06) — only the Admin grant survives strict separation of duties,
which is always on in a FIPS build and voids `Owner`; see
[../seal/fips140-mode.md](../seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips).

```bash
# Admitted member: sign joins over both transports
qrdbd start ... --cluster-join-enabled

# Joining node, Noise_NK
qrdbd start ... \
  --bootstrap-noise-key <hex> --bootstrap-noise-addr member1.db.example.com:63006 \
  --bootstrap-join-addr member1.db.example.com:63007 --bootstrap-join-key <hex> \
  --bootstrap-join-token-file /etc/qrdb/join-token.secret

# Joining node, SPKI/Direct
qrdbd start ... \
  --bootstrap-spki-pin <hex> --bootstrap-addr member1.db.example.com:63002 \
  --bootstrap-join-url https://member1.db.example.com:63002 \
  --bootstrap-join-token-file /etc/qrdb/join-token.secret
```

Add the joined node to the Raft group as usual once it starts
(`qrdb admin member add`, or dynamic membership) — or
`qrdb admin member add-observer` to bring it in as a non-voting observer
first, promoting it to a voter later with `qrdb admin member promote` once
it has caught up.

### Approving a join without a token

A node started with no `--bootstrap-join-token-file`/`-env` (join
transport flags still set) submits its CSR into a replicated approval
queue instead of presenting a token, and then **waits** — logging
periodically — until an operator decides, rather than crash-looping the
daemon start every few seconds:

```bash
# Joining node: no token flags at all
qrdbd start ... \
  --bootstrap-spki-pin <hex> --bootstrap-addr member1.db.example.com:63002 \
  --bootstrap-join-url https://member1.db.example.com:63002
```

From an admitted member, review and decide:

```bash
qrdb admin join-request list
# ID:           3fae8d0b...
# NodeURI:      spiffe://attacker.example/node/spoofed
# ReplicaID:    0
# SubmittedAt:  2026-10-05T12:00:00Z
# ExpiresAt:    2026-10-08T12:00:00Z

qrdb admin join-request approve 3fae8d0b... --node-id node-9
# OK
```

`NodeURI` is the joiner's own claim, shown for information only — `approve`
chooses the identity the leaf actually carries (`--node-id`, bound under
the cluster's trust domain), exactly as a minted token's bound identity
already is never the joiner's to choose. The waiting node picks up the
decision on its next retry (every `--bootstrap-join-approval-poll-interval`,
default 15s) and finishes starting with a signed leaf.

To refuse instead:

```bash
qrdb admin join-request reject 3fae8d0b... --reason "unrecognized source"
# OK
```

The joiner's next retry is refused and it stops polling. An entry nobody
decides on expires after 72 hours and is then treated as absent — a
joiner still running retries into a fresh queue entry rather than being
wedged forever.

**If a node's admission lapses** — it was minted a token and never
completed the join before its issued certificate's validity window
passed, or it simply needs a new certificate long after its first one
expired — mint it a fresh token and run the join again exactly as above,
with the same node id. There is no separate "renew" command: a lapsed
admission does not block an ordinary mint or join, so the steps are
identical to a first-time join.

**To permanently retire a node identity** — after removing it from the
cluster, or when its certificate must be treated as revoked — so it can
never be (re-)admitted, even once its old certificate's lapse would
otherwise have let it renew:

```bash
qrdb admin join-token retire node-7 --reason "decommissioned 2026-10-06"
# OK
```

A later `admin join-token mint node-7` is refused permanently. Retiring
requires permission `cluster:join_identity_retire`, held by `Owner` by
default, the same precedent `cluster:join_token` sets.

**You can still add a node by issuing its certificate out of band**, without
either member running a join listener: generate an ECDSA P-256 key and a
certificate signed by the cluster CA, with the node's SPIFFE identity as a URI
SAN (`spiffe://<trust-domain>/node/<raft-address>`, both `serverAuth` and
`clientAuth` extended key usages, and the node's DNS names/IPs as SANs). Copy
`ca.pem`, the new certificate and the new key to the new node — never
`ca-key.pem` — and start it with `--tls-ca-file`, `--tls-cert-file` and
`--tls-key-file` instead of any bootstrap flag.

### If your cluster was formed by an earlier release, rotate its CA

Treat the existing cluster CA key as **compromised**. It was readable without
authentication from every first node's admin port, and every node that joined
over SPKI, DANE or direct HTTPS holds a copy in `ca-key.pem`. Until the CA is
rotated, restrict network access to every node's admin port.

Rotate the cluster CA:

1. On the node that will hold the new CA, generate a new CA key and
   certificate. Keep the key on that node only.
2. Issue a new node certificate for every member from the new CA, as in the
   out-of-band issuance steps under
   [Adding a node](#adding-a-node-member-side-csr-join).
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
- **Joining nodes must use an approved method for the anchor fetch**:
  `--bootstrap-spki-pin` (with `--bootstrap-addr`), `--bootstrap-dane-dns`, or
  `--bootstrap-direct-url`.
- **`--bootstrap-noise-key` on its own is refused at startup**, with an error
  that names FIPS and lists the methods to use instead. If you give it
  alongside an approved method, the approved one is used and the Noise entry is
  dropped with a WARN.

`--bootstrap-noise-port` and `--bootstrap-noise-host` are still accepted and
validated; they simply configure a listener that is not opened.

**The member-side CSR join's Noise_NK transport is unavailable for the same
reason**, independent of the anchor-fetch method: `--cluster-join-enabled`
starts no Noise_NK join listener in FIPS mode (`StartJoinServer` refuses with
an error naming FIPS), and a joining node's `--bootstrap-join-addr` /
`--bootstrap-join-key` fail the same way `--bootstrap-noise-key` does. Use the
SPKI/Direct join transport (`--bootstrap-join-url`, with `--bootstrap-spki-pin`
or `--bootstrap-direct-url` for the anchor) on both sides instead — it is TLS
only and carries no FIPS restriction of its own.

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

**`admin member remove` is a Raft reconfiguration only — it does not touch
the node's member-side CSR join identity.** A node removed this way, that
joined through a token (not `--tls-ca-file`/`--tls-cert-file`/
`--tls-key-file`), still holds a certificate the cluster CA will happily
chain-verify, and nothing stops it reconnecting until its certificate
expires. If the node is being decommissioned rather than simply moved,
also retire its join identity (`admin join-token retire`, above) so it
can never mint a fresh token or renew, with its old certificate or a
new one.

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
sealed, and the next `Unseal` boots it. This re-seals the barrier even if it
was unsealed through a Shamir quorum ceremony rather than a single
credential: a cancelled boot seals the system barrier AND every tenant
barrier the cancelled attempt had already restored, so the retried `Unseal`
needs a fresh quorum of share holders to resubmit their shares -- the
ceremony's progress from the cancelled attempt is not retained.

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
gRPC and QUIC `FAILED_PRECONDITION`). A `qrdbd` node advertises only once it is
running and a member; a node enrolling through `qrdb enroll` / member-side CSR
join cannot do that before it is added, so the signing member publishes a
short-lived (15-minute) advertisement on its behalf as part of the join,
naming the features the joiner's own binary claims. `qrdb admin member add`
then sees a live advertisement and admits it, the same as any other node. If
`member add` does not happen within that window the advertisement lapses;
re-enroll to get a fresh one. From the Go SDK, a node can instead be
registered through dynamic membership over a client of the running cluster,
with its features in the registration published before the add.

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

**A node refuses to start at the `wait for leader` step: `server: shard 1: no
raft leader after ...`.** The system shard did not elect a leader in time. `qrdbd` waits
for 120 election timeouts: 15 seconds for a single-node daemon and 4 minutes
for a cluster on the default Raft tuning, long enough for the other founders to
come up. The message names the budget and the time spent, and says which of
three things happened: the node could not be asked, the host was too busy to
poll it on time, or the shard really did not elect. For a cluster, the usual
cause is too few founders running: start a quorum of them within the budget of
each other. Stopping the daemon (SIGINT/SIGTERM) ends the wait at once.

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

**A node refuses to start, or refuses a join: `refusing to write through a
symlink at ...`** (`*securefile.SymlinkRefusedError`, reachable with
`errors.As` through whichever caller wraps it --
`*clustertls.WriteError` for the CA material and role file, a
protocol-specific error for an enrollment key). The node's data directory (or
whichever path the cluster CA material, an enrollment key, or the role
file would be written to) is, or contains, a symlink — most commonly a
Kubernetes **Secret volume mount**: every file under a mounted Secret is
actually a symlink into a `..data/<timestamp>` directory the kubelet swaps
on rotation, not a plain file. qrdb writes `ca.pem`, `node-cert.pem`,
`node-key.pem`, `ca-key.pem` and its enrollment/role files into the data
directory, and now refuses rather than silently replacing the kubelet's
symlink itself with a plain file, which severs that path from the
Secret's rotation from then on. Point `--data-dir` at a writable volume
(an `emptyDir` or a
`PersistentVolumeClaim`) instead of a Secret mount, or copy the Secret's
contents into one with an init container before qrdb starts. See
[engineering.md](engineering.md) for the write path this affects.

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
unavailable in this release".** An anchor-fetch method is configured
(`--bootstrap-noise-key`, `--bootstrap-spki-pin`, `--bootstrap-dane-dns` or
`--bootstrap-direct-url`) but neither join-transport is: set
`--bootstrap-join-addr`/`--bootstrap-join-key` or `--bootstrap-join-url`, with
`--bootstrap-join-token-file` or `--bootstrap-join-token-env`, to reach an
admitted member's join listener — or issue the node's certificate out of band
and start it with the explicit TLS file flags instead. See
[Adding a node](#adding-a-node-member-side-csr-join).

**A joining node reports that Noise NK is not FIPS 140-3 approved.** The daemon
is running in FIPS mode, where that method cannot be used. Configure
`--bootstrap-spki-pin` (with `--bootstrap-addr`), `--bootstrap-dane-dns` or
`--bootstrap-direct-url` instead — see
[Forming a cluster](#in-fips-140-3-mode-do-not-use-the-noise-bootstrap-method).
The refusal happens before the node dials anything, so nothing was written.

**The node that generated the cluster CA is gone for good.** Point a new
node's `--bootstrap-addr` / `--bootstrap-join-url` (or the Noise
equivalents) at any OTHER admitted, unsealed member instead — every one of
them serves the identical trust anchor and signs CSRs from the same
replicated Key A, not only the node that happened to generate it. See
[Adding a node](#adding-a-node-member-side-csr-join).

**A FIPS first node logs no Noise public key.** That is correct there. It logs a
WARN naming FIPS and the admin port instead; point joining nodes at
`/v1/ca/bootstrap` with an approved method. `noise-key.hex` is not written in
FIPS mode, because it would be a key produced by a primitive the deployment has
declared it may not use.

**Cluster verbs return "requires mode: raft".** The server is standalone.

**Member entries do not show up in `kv list`.** They live at
`__sys:cluster/members/{nodeID}`, inside the internal namespace, hidden from
user scans on purpose.
