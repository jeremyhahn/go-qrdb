# Clustering — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-02

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
imbalance.

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
| `--bootstrap-spki-pin` with `--bootstrap-addr` | SPKI-pinned TLS |
| `--bootstrap-dane-dns` | DANE TLSA |
| `--bootstrap-direct-url` | direct HTTPS |

Under `GODEBUG=fips140=only` or `fips140=on`, Noise NK is excluded: its X25519
handshake is not a FIPS 140-3 approved key-agreement scheme.
`--bootstrap-noise-key` on its own is refused at startup with an error that
names FIPS and lists the approved flags; given alongside one of them, it is
dropped with a WARN and the approved method is used. The first node's
`--bootstrap-noise-port` / `--bootstrap-noise-host` are still accepted and
validated, and open no listener. See
[user.md](user.md#in-fips-140-3-mode-do-not-use-the-noise-bootstrap-method).

### Member-side CSR join

The joining node generates its own node key locally and submits a PKCS#10 CSR
to an admitted member's join listener; that member signs it with the
replicated cluster CA (Key A) and returns only the signed leaf. Neither
private key ever crosses the wire. Exactly one transport is configured per
joining node:

| Flag | Transport |
|---|---|
| `--bootstrap-join-addr` with `--bootstrap-join-key` | Noise_NK — **not available in FIPS 140-3 mode** |
| `--bootstrap-join-url` | SPKI/Direct, plain HTTPS against an admitted member's admin listener (`/v1/enroll/join`) |

| Flag | Meaning |
|---|---|
| `--bootstrap-join-token-file` | path to a file holding the join-admission secret |
| `--bootstrap-join-token-env` | name of an environment variable holding it (mutually exclusive with the file flag) |

### Signing a join: `--cluster-join-enabled`

An admitted, unsealed member signs CSRs for joining nodes when started with:

| Flag | Default | Meaning |
|---|---|---|
| `--cluster-join-enabled` | `false` | run the Noise_NK join listener and register the SPKI/Direct `/v1/enroll/join` route on the admin listener |
| `--cluster-join-listen-host` | `--host` | bind host for the Noise_NK join listener |
| `--cluster-join-listen-port` | `63007` | TCP port for the Noise_NK join listener |
| `--cluster-join-token-file` | — | path to a file holding the admission secret this node checks |
| `--cluster-join-token-env` | — | name of an environment variable holding it (mutually exclusive with the file flag) |

The SPKI/Direct route needs no separate port or flag of its own: it registers
on the same admin HTTPS listener `/v1/ca/bootstrap` already uses, and only
once the barrier is unsealed — the cluster CA is not available before that, so
the route is never a member of the sealed-window allowlist.

```bash
qrdbd start ... --cluster-join-enabled \
  --cluster-join-token-file /etc/qrdb/join-token.secret
```

## Startup refusal under an activated feature

No command manages the cluster feature gate: nodes advertise their features and
the cluster activates them on its own. A `qrdbd start` whose release lacks a
feature the cluster has activated exits with an error naming the feature; see
[user.md](user.md#upgrading-a-cluster).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. The
3-node behaviour they drive is exercised by `make integration-test-cluster`,
which is **not currently wired into CI**.
