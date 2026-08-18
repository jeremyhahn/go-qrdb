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
| `discovery.dynamic_membership` | `--dynamic-membership` |
| `discovery.min_peers` | `--cluster-min-peers` |

Dynamic membership is **off by default**, leader-only, and quorum-guarded.

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. The
3-node behaviour they drive is exercised by `make integration-test-cluster`,
which is **not currently wired into CI**.
