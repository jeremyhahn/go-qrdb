# Clustering — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-16

Two command groups: `qrdb admin` (node and shard administration) and
`qrdb rebalance` (shard distribution). Both accept the root persistent flags
documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

**Raft mode only**, apart from `admin health`, `admin status` and
`admin capabilities`. In standalone the cluster verbs refuse with
`requires mode: raft`.

## `qrdb admin health`

Check node health status. No flags. Backed by the one public route in the
system, so a load balancer can probe an unauthenticated node.

```bash
qrdb admin health
```

## `qrdb admin status`

Show detailed node status. No flags.

## `qrdb admin capabilities`

Show the effective feature set of the connected node — build tags × run mode ×
config, including a `mode` field. No flags. See
[../run-modes/cli.md](../run-modes/cli.md).

```bash
qrdb admin capabilities --output json
```

## `qrdb admin peers`

List shard peer membership. No flags.

## `qrdb admin topology`

Show the full cluster topology as seen from this node. No flags.

```bash
qrdb admin topology --output json
```

## `qrdb admin state-hash`

Show, for every shard the connected node replicates, the replica's applied
index and the SHA-256 digest of its state at exactly that index, with
isolated tenants' shards listed under their tenant. The node answers from its
own state machines without a proposal. Run it against each node and compare:
two replicas of one shard at the same applied index must show the same hash.
Requires `admin:state_hash`, which only the system tenant can hold.

| Flag | Type | Meaning |
|---|---|---|
| `--shard` | uint, repeatable | only these shard IDs; default every shard |

The table has one row per replica: `TENANT` (`-` for the node's own shards),
`SHARD_ID`, `REPLICA_ID`, `APPLIED_INDEX`, `RECORDS`, `HASH` and `ERROR`
(`-` when the digest was computed). A replica that cannot produce a digest
shows its reason in `ERROR`, `-` in `HASH` and zero index and records, and
the command still succeeds. A `--shard` value that is not an unsigned
integer is refused before anything is sent. `--output json`
and `--output yaml` print the response documented in
[api.md](api.md#replica-state-hashes).

```bash
qrdb admin state-hash
qrdb admin state-hash --shard 1 --output json
```

## `qrdb admin replica-states`

Show, for every replica the connected node holds, the shard, replica ID,
applied index and state-machine hash. It answers for **this node only** and is
never forwarded: to compare replicas, run it against every node and group the
rows by shard. Two replicas of one shard whose applied index is equal must
report one hash. Witnesses hold no state machine and are not listed. See
[api.md](api.md#replica-states) for what the hash covers and why the reading is
taken on a quiet shard.

Needs `admin:replica_state` from a `system`-tenant identity.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | report only this shard; 0 (the default) reports every replica on the node |

Table output columns are `SHARD`, `REPLICA`, `APPLIED INDEX` and `HASH`, with
the hash in hexadecimal. `--output json` and `--output yaml` print the response
with the hash as a decimal number.

```bash
for host in node1.example.com node2.example.com node3.example.com; do
  qrdb --server "https://$host:8080" admin replica-states --shard-id 1 --output json
done
```

| Condition | Message |
|---|---|
| standalone node | `requires mode: raft` |
| `--shard-id` names a shard this node holds no replica of | the shard is not hosted on this node |
| the shard's state machine cannot hash itself | the state machine does not support hashing |

## `qrdb admin member add <address>`

Add a replica to a shard.

The replica is committed with its Raft member identity. A node the cluster
already knows needs nothing more: its identity is read from the committed
membership. A brand-new node needs `--identity`, printed from its certificate
by `qrdbd node-identity --cert`; without it the add is refused with a message
saying so. An `--identity` that contradicts the committed one is refused.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |
| `--replica-id` | uint | replica ID |
| `--identity` | string | the replica's member identity (`spki:<hex>`); required only for a node no committed membership names |

```bash
qrdb admin member add node4.example.com:63001 --shard-id 1 --replica-id 4 \
  --identity "$(qrdbd node-identity --cert node4-cert.pem)"
```

## `qrdb admin member add-observer <address>`

Add a replica to a shard as a non-voting observer rather than a voter.
Identical flags and identity rules to `member add`; the only difference is
the Raft role the new replica comes up in. Promote it to a voter later with
`member promote`.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |
| `--replica-id` | uint | replica ID |
| `--identity` | string | the replica's member identity (`spki:<hex>`); required only for a node no committed membership names |

```bash
qrdb admin member add-observer node5.example.com:63001 --shard-id 1 --replica-id 5 \
  --identity "$(qrdbd node-identity --cert node5-cert.pem)"
```

Permission `admin:add_member` — the same reconfiguration grant `member add`
needs, since adding an observer is a Raft membership change too. Works over
every transport (see [api.md](api.md#capability-matrix--node-administration)).

## `qrdb admin member remove <replica-id>`

Remove a replica from a shard.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |

```bash
qrdb admin member remove 4 --shard-id 1
```

Adding and removing replicas are separately permissioned
(`admin:add_member`, `admin:remove_member`) from reading peers.

Every `member` command may target any node, on every transport. Only the
shard's leader can commit a membership change. The node the command reaches
first waits, up to 10s, until it knows a leader for the shard -- an election
in progress is waited out, not refused -- and answers
`no raft leader known after 10s` (Unavailable, HTTP 503) if none is elected in
that time. Then a follower forwards the request, once, to the leader its
refusal names, and the leader authorizes the original caller
([../replica-forwarding/](../replica-forwarding/)). The change itself is never
proposed twice: one that timed out may still have committed. A command that
still answers `not leader` reached a leader that had moved again, or one it
could not reach; it is not retried.

## `qrdb admin member promote <replica-id>`

Promote a committed observer of a shard to a voter, keeping the address and
identity the shard already committed for it; nothing restarts. Refused with
`admin member promote: ... not a committed observer` for a replica that is
absent, already a voter, or a witness.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |

```bash
qrdb admin member promote 4 --shard-id 1
```

Permission `admin:add_member` — the same reconfiguration grant `member add`
needs, since a promotion is a Raft membership change too. Works over every
transport (see [api.md](api.md#capability-matrix--node-administration)).

## `qrdb admin member transfer-leadership <replica-id>`

Hand a shard's leadership to a voter and wait until it leads. Prints `OK`
once the named replica leads the shard.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID (required) |

```bash
qrdb admin member transfer-leadership 3 --shard-id 1
```

A target that does not take over within the server's five-second bound — one
that is stopped or partitioned — fails the command with a non-zero exit and
the message `leadership transfer away from replica <from> to replica <to> did
not complete after 5s`; on the wire that is the code `LEADER_TRANSFER_TIMEOUT`
([api.md](api.md)). Nothing changed, and the command may be run again. Naming
an observer, a witness or a removed replica is refused; so is a replica the
shard never had. Naming the current leader succeeds at once.

Permission `admin:remove_member` — moving leadership is the step
`member remove` already takes when the replica it removes leads the shard.
Like every `member` command it works on any node over every transport: a
follower forwards it to the leader.

## `qrdb admin snapshot`

Request a shard snapshot.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |

## `qrdb admin drain`

Transition the node into drain mode. No flags. Use before taking a node out of
service.

```bash
qrdb admin drain
```

## `qrdb admin node-compaction`

Show the platform's node-keyspace compaction policy -- the scheduled compaction
of the keys outside every database namespace -- or `Compaction: off`. No flags.
Requires `admin:node_compaction`, which only the system tenant can hold.

## `qrdb admin update-node-compaction`

Set the platform's node-keyspace compaction policy. It is off unless set,
never touches a database's keys, and is kept as a replicated record, so it
applies on whichever node leads the shared store and survives a node's loss.
Requires `admin:node_compaction` (system tenant only); a tenant's caller is
refused.

| Flag | Type | Meaning |
|---|---|---|
| `--mode` | string | `off \| revision \| period` (required) |
| `--retain-revisions` | int | `revision` mode: keep the last N revisions |
| `--retain-period` | duration | `period` mode: keep history newer than this (>= `1s`) |

```bash
qrdb admin update-node-compaction --mode revision --retain-revisions 100000
qrdb admin update-node-compaction --mode off
```

See [../key-value/engineering.md](../key-value/engineering.md)
"Node-keyspace compaction".

## `qrdb rebalance stats`

Show statistics for one shard: key count, byte count, leader and replica count.

| Flag | Type | Meaning |
|---|---|---|
| `--shard` | uint | shard ID to inspect |

## `qrdb rebalance report`

Display per-node load distribution and identify cluster hotspots. No flags.

```bash
qrdb rebalance report --output json
```

The report carries per-node load, an `imbalance_score`, and a `hotspots` list.

## `qrdb rebalance plan`

Analyze shard distribution across nodes and generate a move plan.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dry-run` | bool | false | generate the plan without executing it |
| `--max-moves` | int | 0 | maximum shard moves in the plan; `0` = no limit |
| `--target-balance` | float | 0 | target balance percentage 0–100; `0` = perfectly balanced |

```bash
qrdb rebalance plan --dry-run --max-moves 10 --output json > plan.json
```

## `qrdb rebalance execute`

Read a rebalance plan from **stdin** (JSON) and execute it against the cluster.
No flags.

```bash
qrdb rebalance plan --dry-run --output json | qrdb rebalance execute
qrdb rebalance execute < plan.json
```

Plan and apply are separable on purpose: review the moves before they happen.
`rebalance:execute` is a separate permission from the read-only verbs because it
physically moves shards.

The result reports moves completed, moves failed, duration and the final
imbalance, then one `Failed:` line per failed move naming the shard, the
source and destination, the phase that failed and the cause.

## Cluster formation

Discovery and bootstrap are `qrdbd` configuration, not client verbs:

| Setting | Flag |
|---|---|
| `raft.member_identities` | `--member-identities` |
| `discovery.dynamic_membership` | `--dynamic-membership` |
| `discovery.min_peers` | `--cluster-min-peers` |

### `qrdbd node-identity --cert <file>`

Prints the Raft member identity of a node's transport certificate
(`spki:<sha256-hex>` of its public key). It reads only the certificate, so the
identities of every node can be collected from the CA's issued certificates
before any node starts.

| Flag | Type | Meaning |
|---|---|---|
| `--cert` | path | the node's PEM certificate (its `--tls-cert-file`); required |

```bash
qrdbd node-identity --cert /etc/qrdb/node2-cert.pem
# spki:3f9a...
```

Every other replica lists it as `2=spki:3f9a...` in `--member-identities`
(`raft.member_identities`). A node's own entry may be omitted — it is derived
from its certificate — and a node also logs its identity at start
(`raft member identity`).

```bash
qrdbd start --replica-id 1 --peers 1=node1.db.example.com:63001,2=node2.db.example.com:63001,3=node3.db.example.com:63001 \
  --member-identities 2=spki:3f9a...,3=spki:c01d... \
  --tls-ca-file ca.pem --tls-cert-file node1-cert.pem --tls-key-file node1-key.pem
```

Dynamic membership is **off by default**, leader-only, and quorum-guarded.

Peer enrollment is configured on the joining node and has two independent
halves, both required for a join to succeed: fetching the trust anchor (the CA
**certificate** only) and submitting a CSR to an admitted member for signing
(member-side CSR join). Configuring only the first half selects a join path
that refuses to start with `EnrollmentUnavailableError`, before any dial or
write. See [user.md](user.md#adding-a-node-member-side-csr-join).

### Trust-anchor fetch

| Flag | Method |
|---|---|
| `--bootstrap-noise-key` with `--bootstrap-noise-addr` | Noise NK — **not available in FIPS 140-3 mode** |
| `--bootstrap-p256-key` with `--bootstrap-noise-addr` | P-256 suite (`TSBOOT1_P256_AESGCM_SHA256`) — FIPS 140-3 approved |
| `--bootstrap-spki-pin` with `--bootstrap-addr` | SPKI-pinned TLS |
| `--bootstrap-dane-dns` | DANE TLSA |
| `--bootstrap-direct-url` | direct HTTPS |

Both key flags may be given; the Noise suite is preferred where it is usable and
the P-256 suite is used otherwise.

Under `GODEBUG=fips140=only` or `fips140=on`, Noise NK is excluded: its X25519
handshake is not a FIPS 140-3 approved key-agreement scheme.
`--bootstrap-noise-key` on its own is refused at startup with an error that
names FIPS and the P-256 flag that replaces it; given alongside another key or
method, it is dropped with a WARN and the approved one is used. The first node's
`--bootstrap-noise-port` / `--bootstrap-noise-host` still apply: the listener
opens and serves the P-256 suite only, publishing its P-256 public key. See
[user.md](user.md#in-fips-140-3-mode-use-the-p-256-bootstrap-suite).

### Member-side CSR join

The joining node generates its own node key locally and submits a PKCS#10 CSR
to an admitted member's join listener; that member signs it with the
replicated cluster CA (Key A) and returns only the signed leaf, carrying the
**token's bound identity** as its URI SAN, plus that same identity's own
address as an IP or DNS SAN (needed for go-quicraft's Raft QUIC transport to
dial it) — never the joiner's self-reported node URI, and never the signing
member's own SANs. Neither private key ever crosses the wire. Exactly one
transport is configured per joining node:

| Flag | Transport |
|---|---|
| `--bootstrap-join-addr` with `--bootstrap-join-key` | Noise_NK — **not available in FIPS 140-3 mode** |
| `--bootstrap-join-addr` with `--bootstrap-join-p256-key` | P-256 suite — FIPS 140-3 approved |
| `--bootstrap-join-url` | SPKI/Direct, plain HTTPS against an admitted member's admin listener (`/v1/enroll/join`) |

| Flag | Meaning |
|---|---|
| `--bootstrap-join-token-file` | path to a file holding the single-use join token an admin minted for this node's identity (below). Refused with a typed error if group- or world-readable, or shorter than the minimum token length. |
| `--bootstrap-join-token-env` | name of an environment variable holding it (mutually exclusive with the file flag) |

### Signing a join: `--cluster-join-enabled`

An admitted, unsealed member signs CSRs for joining nodes when started with:

| Flag | Default | Meaning |
|---|---|---|
| `--cluster-join-enabled` | `false` | run the Noise_NK join listener and register the SPKI/Direct `/v1/enroll/join` route on the admin listener |
| `--cluster-join-listen-host` | `--host` | bind host for the Noise_NK join listener |
| `--cluster-join-listen-port` | `63007` | TCP port for the Noise_NK join listener |

There is no admission-token flag on this side any more. A signing member
validates a presented token against the single-use, identity-bound join
tokens an admin mints into the replicated store — see "Minting a join
token" below — not a static shared secret it was configured with.

The SPKI/Direct route needs no separate port or flag of its own: it registers
on the same admin HTTPS listener `/v1/ca/bootstrap` already uses, and only
once the barrier is unsealed — the cluster CA is not available before that, so
the route is never a member of the sealed-window allowlist.

**Every admitted, unsealed member with `--cluster-join-enabled` can sign,
not only the one that generated the cluster CA.** `/v1/ca/bootstrap` and
the Noise anchor listener are likewise served by any such member, from the
same cached certificate — see
[architecture.md](architecture.md#every-admitted-unsealed-member-serves-the-anchor).
A joining node's `--bootstrap-addr`/`--bootstrap-join-url` (or the Noise
equivalents) may name whichever member is reachable, including after the
original node is gone for good.

```bash
qrdbd start ... --cluster-join-enabled
```

### Minting a join token: `qrdb admin join-token mint`

An admin mints a single-use token bound to the joining node's SPIFFE node ID
under the cluster's pinned trust domain, before the node ever attempts to
join:

```bash
qrdb admin join-token mint node-7 --ttl 1h
```

```
Token:      <opaque single-use token>
ExpiresAt:  2026-10-03T18:00:00Z
```

The token is printed exactly once — there is no "list minted tokens" read
path — and is consumed atomically on first use; a second presentation, or one
past its expiry, is refused identically to an unknown token (no
token-validity oracle). Copy it into the joining node's
`--bootstrap-join-token-file` (or `--bootstrap-join-token-env`). Minting
requires permission `cluster:join_token` (`ScopeCross`, platform-only: held
by `Owner` and by the cluster `Admin` role, the
grant that keeps this reachable under strict separation of duties, which
voids `Owner`) — see
[api.md](api.md#post-v1clusterjoin-tokens). The verb is implemented on all
six transports (REST, gRPC, QUIC, Unix, MCP and embedded); the CLI above
reaches it through whichever transport `qrdb` is connected with.

### Reviewing tokenless join requests: `qrdb admin join-request`

A joiner configured with no join token at all (no
`--bootstrap-join-token-file`/`-env`) submits its CSR into a replicated
approval queue instead of presenting a secret — see
[architecture.md](architecture.md#approval-queue-tokenless-join). An
operator reviews the queue with three subcommands, all gated by the
identical `cluster:join_token` permission (`ScopeCross`, platform-only)
`join-token mint` already requires:

```bash
qrdb admin join-request list
```

```
ID:           3fae8d0bbc14194cfb2a20eb405f2a1948b2a0b508182ef8d088c30b859391b
NodeURI:      spiffe://attacker.example/node/spoofed
ReplicaID:    0
SubmittedAt:  2026-10-05T12:00:00Z
ExpiresAt:    2026-10-08T12:00:00Z
```

`NodeURI` is the joiner's own self-reported identity, shown for the
operator's information only — it plays no part in the identity the
eventual leaf carries. `ExpiresAt` is `DefaultPendingJoinRequestTTL` (72h)
past submission; an entry still undecided at that point is treated as
absent and a retry starts a fresh one.

```bash
qrdb admin join-request approve 3fae8d0b... --node-id node-7
```

Binds the queued CSR to `spiffe://<trust domain>/node/node-7` — an operator
choice, never the joiner's self-reported `NodeURI` above, the identical
invariant a minted join token's bound identity already carries. This does
**not** sign anything itself: the leaf is issued the next time the
joiner's own retry reaches the join listener. Refused with
`node identity already admitted to the cluster` (the same
`*clusterenroll.ExistingMemberError` `join-token mint` refuses with) when
`--node-id` names an identity already admitted or reserved.

```bash
qrdb admin join-request reject 3fae8d0b... --reason "unrecognized source"
```

Records a terminal refusal; the joiner's next poll is refused and its
retry loop stops. Both `approve` and `reject` are refused with
`no pending join request <id> awaiting a decision` for an id that is
absent, expired, or already decided — a decision is made exactly once.

All three subcommands are implemented on all six transports (REST, gRPC,
QUIC, Unix, MCP and embedded).

An admission that lapses — the identity's issued leaf has expired without
being explicitly retired — renews itself through this exact same command:
mint a fresh token for the same node id and run the ordinary join again.
There is no separate "renew" verb; a lapsed identity simply does not
block a new mint or CSR exchange.

### Retiring a node identity: `qrdb admin join-token retire`

An admin permanently refuses any future admission for a node identity —
after removing it from the cluster, or to treat its issued certificate as
revoked — with:

```bash
qrdb admin join-token retire node-7 --reason "decommissioned 2026-10-06"
```

```
OK
```

Retirement is permanent and unconditional: it blocks a fresh
`join-token mint` for that identity (`clusterenroll: node identity has
been retired and may never be (re-)admitted`) forever, whether or not its
most recent leaf has also lapsed — a removed member cannot re-enrol with
its old cert or identity by waiting out its certificate's expiry.
`--reason` is optional, free-text, and carried for audit only. Retiring
requires permission `cluster:join_identity_retire` (`ScopeCross`,
platform-only: no built-in role carries it by default, only `Owner`) —
see [api.md](api.md#post-v1clusterjoin-identitiesretire). The verb is
implemented on all six transports (REST, gRPC, QUIC, Unix, MCP and
embedded); the CLI above reaches it through whichever transport `qrdb` is
connected with.

## Startup refusal under an activated feature

No command manages the cluster feature gate: nodes advertise their features and
the cluster activates them on its own. A `qrdbd start` whose release lacks a
feature the cluster has activated exits with an error naming the feature; see
[user.md](user.md#upgrading-a-cluster).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. The
3-node behaviour they drive is exercised by `make integration-test-clustering`,
which runs in CI as the `clustering` leg of the `integration-features` job
(`.github/workflows/ci.yml`), required by `ci-gate`.
