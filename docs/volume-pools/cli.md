# Volume Pools — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first. Decisions
> D1-D10 are ruled; the Phase 1 build is in progress.
>
> Each verb below gets a `CommandDefinition` and runs on every transport
> through the root `--transport` flag, documented in
> [../key-value/cli.md](../key-value/cli.md#global-flags). `tenant move` and
> `tenant move-status` are **Phase 2** (decision D5); every other verb here
> is Phase 1.

Command group: `qrdb volume-pool`. This is a qrdb-owned control-plane group,
like `cluster` and `rebalance`. Decision D7 in
[architecture.md](architecture.md#decisions-ruled) rules that it
joins the locked CLI surface; making that addition is a handbook change
outside this repository and is tracked there, not here. The tenant verbs
that touch pools, `tenant create --pool` and `tenant move`, are in the
`tenant` group.

Every verb here needs a `system`-tenant identity holding the matching
`volume_pools:*` permission (see [api.md](api.md)).

Output formats: `--output table` (default) and `--output json`, as elsewhere.

Exit codes:

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | the server refused the request; the typed error is printed |
| `2` | usage error |

## `qrdb volume-pool list`

Lists every pool in the catalog with its capacity, reservation, usage, tenant
count and a per-node health summary. No arguments or flags beyond the global
ones.

```bash
qrdb volume-pool list
```

```
NAME   PROVIDER  CAPACITY  RESERVED  USED    TENANTS  NODES (ready/locked/unavail/unmapped)
fast   dir       2.0 TiB   200 GiB   50 GiB  3        3/0/0/0
vault  luks2     4.0 TiB   1.0 TiB   310 GiB 2        2/1/0/0
```

## `qrdb volume-pool get <pool>`

Inspects one pool, with one row per node: whether the node maps the pool, the
provider state, size, free space, how long ago the node reported, and the
provider's detail text. It also prints the first page of assigned tenants.

| Argument | Required | Meaning |
|---|---|---|
| `<pool>` | yes | the pool name |

```bash
qrdb volume-pool get vault
```

A node that has not reported within the lease TTL is shown as `no report`,
never with stale numbers.

## `qrdb volume-pool create <pool>`

Adds a pool to the catalog. The pool is not served until each node maps it in
its own configuration and restarts or reloads (see [user.md](user.md)).

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--provider` | string | `dir` | no | the volume provider every node uses for this pool; must be registered on the node handling the request |
| `--description` | string | — | no | free text |
| `--capacity` | size (`2TiB`, `500GiB`, bytes) | derived | no | allocatable capacity; unset means the smallest size a healthy node reports |
| `--overcommit` | float | `1.0` | no | reservations may reach capacity × overcommit; must be ≥ 1.0 |

```bash
qrdb volume-pool create fast --description "NVMe tier"
qrdb volume-pool create vault --provider luks2 --capacity 4TiB
```

## `qrdb volume-pool update <pool>`

Changes a pool's description, capacity or overcommit. The provider is
immutable. Lowering capacity below the current reservation is refused with
`RESOURCE_EXHAUSTED`.

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--description` | string | unchanged | no | free text |
| `--capacity` | size | unchanged | no | allocatable capacity; `0` returns to the derived value |
| `--overcommit` | float | unchanged | no | must be ≥ 1.0 |

```bash
qrdb volume-pool update fast --capacity 3TiB
```

## `qrdb volume-pool delete <pool>`

Removes a pool from the catalog. Refused while any tenant is assigned to it;
the error names the tenants. It changes nothing on disk.

```bash
qrdb volume-pool delete scratch
```

## `qrdb volume-pool tenants <pool>`

Lists the tenants assigned to a pool, with each one's reservation and measured
use. Paginated.

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--page-size` | int | server default | no | rows per page |
| `--page-token` | string | — | no | resume after a previous page |

```bash
qrdb volume-pool tenants fast --page-size 50
```

## Tenant verbs that touch pools

### `qrdb tenant create --pool`

Replaces the removed `--data-root`. It applies to `volume` and `raft` tenants.

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--pool` | string | the tenant's default pool | no | the pool to place the tenant on; the tenant must be entitled to it |

```bash
qrdb tenant create --id big-co --name "Big Co" --isolation-type raft --pool fast
```

`--data-root` is removed. Passing it is a usage error (exit `2`): unknown flag.

### `qrdb tenant set-entitlement --allowed-volume-pools`

A new flag on the existing verb ([multi-tenancy](../multi-tenancy/cli.md)).

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--allowed-volume-pools` | strings | — (no pool) | no | the pools the tenant may be placed on; the first is its default; **empty = no pool, fail-closed** |

```bash
qrdb tenant set-entitlement acme --isolation-modes prefix,raft \
     --allow-dedicated-raft --allowed-volume-pools fast,bulk
```

### `qrdb tenant move <id>` *(Phase 2, decision D5)*

Moves a tenant to another pool, offline. The tenant is unavailable for the
duration of the copy on its slowest serving node.

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--to` | string | — | yes | the target pool; the tenant must be entitled to it and its reservation must fit |
| `--wait` | bool | `false` | no | block until the move is `cleaned` or fails, printing each state |

```bash
qrdb tenant move acme --to bulk --wait
```

### `qrdb tenant move-status <id>` *(Phase 2, decision D5)*

Prints the move journal: state, source and target pools, nodes copied, nodes
pending, and the last error.

```bash
qrdb tenant move-status acme
```

### `qrdb tenant get` / `list`

These now print `Pool:` instead of `DataRoot:`, and `Refused:` with the refusal
code and message when the answering node will not serve the tenant.

## `qrdbd start --allow-degraded-volume-boot` (decision D10)

This is the daemon's own flag, `cmd/qrdbd`, not the `qrdb` client -- it
controls what happens when this node's `storage.volume_pools` names a pool
whose mount this node cannot open.

| Flag | Type | Default | Required | Meaning |
|---|---|---|---|---|
| `--allow-degraded-volume-boot` | bool | `false` | no | start every volume and database this node CAN open and skip only the ones on a pool that failed, instead of the default: fail startup outright |

Equivalent config key: `storage.allow_degraded_volume_boot` (same default).
An explicit `--allow-degraded-volume-boot` on the command line wins over the
config file.

```bash
# Default: a configured pool that fails to open fails startup outright.
qrdbd start --config qrdbd.yaml
# volume: 1 volume pool(s) failed to open at boot (pass
# --allow-degraded-volume-boot to start anyway, serving every volume and
# database this node can): "fast" (provider "dir"): stat /mnt/nvme-a: no
# such file or directory;
# (exit 1)

# Opt in to the old degraded-boot behavior: start anyway, serve what this
# node can.
qrdbd start --config qrdbd.yaml --allow-degraded-volume-boot
```

Under `--allow-degraded-volume-boot`, the skip is never silent: it is logged
at ERROR, visible in `qrdb volume-pool get <pool>` (a `qrdb volume-pool get`,
not `qrdbd`, verb -- see above), and exported as
`qrdb_volume_pool_ready{pool}` = 0. A request for a database on the skipped
pool is refused with `pool_unavailable` on every transport (see
[api.md](api.md)), never a not-found error.
