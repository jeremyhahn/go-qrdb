# Clustering — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

On a multi-node cluster this is currently refused by go-quicraft with
`member identity required`: the verb carries no member identity, and a replica
is admitted by its identity alone. Form the cluster with every member named in
`--member-identities` instead.

| Flag | Type | Meaning |
|---|---|---|
| `--shard-id` | uint | shard ID |
| `--replica-id` | uint | replica ID |

```bash
qrdb admin member add node4.example.com:63001 --shard-id 1 --replica-id 4
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

Peer enrollment is configured on the joining node. **In this release, every
flag in the table below selects a join path that refuses to start** with
`EnrollmentUnavailableError`, before any dial or write, because the cluster CA
private key is no longer distributed. Add a node with `--tls-ca-file`,
`--tls-cert-file` and `--tls-key-file` instead. See
[user.md](user.md#adding-a-node-joining-through-enrollment-is-unavailable-in-this-release).

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

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. The
3-node behaviour they drive is exercised by `make integration-test-cluster`,
which is **not currently wired into CI**.
