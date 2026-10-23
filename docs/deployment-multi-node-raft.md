# Deploying Multi-Node IsolationRaft Clusters

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-23
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

This guide covers the operational considerations and deployment patterns for
running tenant databases with `IsolationRaft` isolation across multiple cluster
nodes. For the design rationale and implementation details, see
[multi-tenancy/](multi-tenancy/).

---

## Prerequisites

### Cluster setup

- A working qrdb Raft cluster with 3+ nodes (multi-node replication requires
  distinct nodes).
- All nodes agree on a single **cluster trust domain** (default: `qrdb.cluster`).
  This value is used in node certificate SPIFFE URIs and must be consistent
  across all nodes.
- The **volume pool** exists in the replicated catalog
  (`qrdb volume-pool create <pool>`) and every node maps that pool name to a
  local path under `storage.volume_pools`. The paths need not be identical:
  each node resolves the pool to its own local mount.
- The cluster uses **mTLS authentication** (production default). Plaintext mode
  (no client cert) is supported for development/testing but is not recommended.
- Node certificates carry a **SPIFFE URI SAN** in the form
  `spiffe://{trustDomain}/node/{raftAddr}`. This is mandatory for the secure
  per-node key delivery.

### Tenants and entitlements

- The tenant's `allowed_isolation_modes` entitlement includes `raft`.
- The tenant has `allow_dedicated_raft: true` in its entitlements.
- A tenant whose databases live in a volume pool has that pool in its
  `allowed_volume_pools` entitlement; with none, pool placement fails closed.
- Quotas are configured: `max_raft_groups`, `max_replicas_per_group`,
  `max_total_replicas`, and `max_storage_bytes`.

---

## Configuration

### Cluster trust domain

Set in the daemon YAML config (default: `qrdb.cluster`):

```yaml
auth:
  cluster_trust_domain: "qrdb.cluster"   # Unique per cluster; used for SPIFFE identities
```

The trust domain is threaded into:
1. Node certificate SPIFFE URI SAN: `spiffe://qrdb.cluster/node/{raftAddr}`
2. Sender/receiver key-delivery validation (both must agree)
3. Cluster peer authentication gate

### Volume pool

Each node maps the catalog pool name to its own local path:

```yaml
storage:
  volume_pools:
    - name: "default"
      path: "/var/qrdb/pools/default"   # This node's mount for the pool
      provider: "dir"                   # Default
```

A tenant's dedicated store is allocated under the pool's path plus the
tenant's id, which every node resolves against its own mount.

### Barrier and key management

```yaml
seal:
  strategy: "passphrase"  # none (default), passphrase, or shamir

tenant:
  default_entitlements:
    allowed_isolation_modes: [prefix, volume, raft]
    allow_dedicated_raft: true
    allow_volume_storage: true
    allow_system_managed_barrier: false  # Operator choice
    allowed_volume_pools: [default]

  default_quotas:
    max_raft_groups: 10
    max_replicas_per_group: 3
    max_total_replicas: 30
    max_storage_bytes: 10737418240  # 10 GiB
```

**Barrier auto-unseal choice** (`qrdb tenant create --barrier-auto-unseal`):

- `false` (default, self-managed): tenant must unseal their barrier on each
  node after restart. Higher security, unattended-restart cost.
- `true` (system-managed): tenant barrier is auto-unsealed under the system
  barrier; replicas start automatically after restart. Lower security, higher
  convenience. Requires the `allow_system_managed_barrier` entitlement.

Both modes are fully supported in multi-node deployments.

---

## Creating an IsolationRaft database

### Single command

```bash
qrdb database create payments \
  --backend raft \
  --replicas 3
```

The database is created in the calling tenant (`acme`, from the caller's
identity). The replication factor is clamped to `min(requested, cluster_size)`,
so a request for `--replicas 5` on a 3-node cluster places 3 replicas. If you
request more than `max_replicas_per_group` or the aggregate
`max_total_replicas` budget, the operation fails.

### REST API

```bash
curl -X POST https://localhost:63002/v1/databases \
  -H "Content-Type: application/json" \
  --cert client.crt --key client.key \
  --cacert ca.crt \
  -d '{
    "name": "payments",
    "backend": "raft",
    "replicas": 3,
    "dedicatedWal": true
  }'
```

`dedicatedWal: true` opts the group into a per-tenant-KEY WAL (its raft log
encrypted under the tenant's own barrier instead of the shared system barrier).
It is gated by the `allow_dedicated_wal` entitlement and the
`max_dedicated_wal_groups` quota; when not opted-in or quota-exhausted the group
uses the shared, system-key-encrypted WAL. Omit the field for the default.

### Web UI

The admin web UI exposes these operations directly:

- **Create database wizard** (raft mode): a **Replication Factor (Replicas)**
  input and a **Dedicated WAL** toggle (raft-only) — the toggle sends
  `dedicatedWal` on create.
- **Tenant detail → Quotas**: `Max Replicas / Group`, `Max Total Replicas`, and
  `Max Dedicated WAL Groups` fields.
- **Tenant detail → Entitlements**: `Allow dedicated Raft`, `Allow volume
  storage`, `Allow system-managed barrier`, and `Allow dedicated WAL` toggles,
  plus the allowed isolation modes.
- **Admin → Topology** (raft clusters): the placement/replica-distribution view
  — per node, its raft address, shard count, and the per-shard replica/leader
  layout (backed by `GET /admin/topology`).

The cluster surfaces above (the **Cluster** and **Topology** nav entries, and
the wizard's Replication Factor / Dedicated WAL controls) appear automatically
when the UI is connected to a multi-node cluster. In browser/web mode the UI has
no pre-recorded topology, so it probes `GET /admin/topology` on connect and
treats the server as a Raft cluster when more than one distinct node is reported;
a single-node server presents the standalone UI. (Desktop profiles record the
standalone/clustered choice at setup time.)

### What happens

1. **Placement decision**: The service picks 3 distinct,
   least-loaded, healthy nodes. The least-loaded node is elected leader.
2. **Placement persistence**: The decision is persisted to the replicated
   placement table in the system KV: `__sys:placement/acme/payments`.
3. **Barrier keys**: Every serving node initialises the tenant barrier with
   its own key from the owner's signed, keyless init statement; no key is
   carried between nodes for the `none`, `passphrase` and `shamir` strategies
   or for a system-managed barrier. For an injected (hardware) strategy, the
   owner-sealed portable blob is sent to each replica node over the
   authenticated mTLS control plane (see
   [Secure key delivery](#secure-key-delivery)). Delivery is best-effort: a
   failed delivery does not fail the create.
4. **Replica start**: Each node's `TenantShardReconciler` watches the
   placement table (polling every 5 seconds when no watch is available), sees
   the new entry, and calls `Attach` to start the local replica (if the
   barrier is unsealed). A new replica joins as an observer and is promoted to
   a voter once it has caught up.

---

## What happens on node restart

### Boot sequence (all nodes)

1. The node's own system barrier is unsealed. Each node is unsealed on its
   own; there is no cluster auto-unseal (see
   [barrier-seal/architecture.md](barrier-seal/architecture.md#no-cluster-auto-unseal)).
2. The replicated placement table is loaded into the system KV.
3. `TenantShardReconciler.Run()` is launched and calls `ReconcileOnce`.
4. For each placed replica assigned to this node:
   - If the barrier is unsealed and the shard is not already running, call
     `Attach` to start it.
   - If the barrier is sealed (self-managed mode), defer and wait for the
     tenant to unseal it on this node
     (`qrdb tenant barrier-unseal --id acme --key <base64>`).
5. After reconciliation, the node begins serving traffic. Sealed replicas are
   reachable but return `qrdb/seal: tenant barrier is sealed` until unsealed.

### Leader restart

If the current leader node restarts:
1. Raft will elect a new leader (quorum-based).
2. The old leader rejoins as a follower.
3. The placement table stays unchanged (it's replicated).
4. New replicas are not spawned automatically; to rebalance, use
   `qrdb rebalance plan` and `qrdb rebalance execute` (see
   [Rebalance and move](#rebalance-and-move)).

### Data integrity after restart

Every started replica re-attaches to the same shard ID and DataRoot. The
persistent placement table and per-node reconciler ensure:
- No orphaned shards (the placement table is the source of truth).
- No crashed/partial shards (a shard is only started after successful barrier
  unseal and entry in the placement table).
- No data loss (Raft consensus + persistent state-machine data under the
  DataRoot).

---

## Node join and leave

### Node join (adding a node to the cluster)

1. Configure and start the new node with the same trust domain, and map the
   volume pool to a local path.
2. Admit it to the Raft cluster: a join token (`qrdb admin join-token mint`)
   and an approved join request (`qrdb admin join-request approve`), or
   `qrdb admin member add`. Automatic membership is off by default
   (`qrdbd start --dynamic-membership`). See
   [clustering/user.md](clustering/user.md#adding-a-node-member-side-csr-join).
3. Subsequent database creates can place replicas on the new node.
4. Existing databases are not automatically rebalanced; use
   `qrdb rebalance plan` and `qrdb rebalance execute` to move replicas if
   desired.

### Node leave (removing a node from the cluster)

1. Put the node into drain mode (`qrdb admin drain`). See
   [clustering/user.md](clustering/user.md#taking-a-node-out-of-service).
2. Move its replicas away (`qrdb rebalance plan` / `execute`, or
   `qrdb database scale`).
3. Remove it from Raft membership (`qrdb admin member remove`). See
   [clustering/user.md](clustering/user.md#changing-membership).
4. Stop the node gracefully (e.g., SIGTERM).

---

## Secure key delivery

### Architecture overview

Each node initialises an isolated tenant's barrier with its own key (see
[barrier-seal/architecture.md](barrier-seal/architecture.md#per-node-barrier-keys)).
Only for an injected (hardware) seal strategy does a node send anything: the
tenant's owner-sealed portable blob, delivered to each replica node when the
database is created. No plaintext root key is sent. The delivery uses:

- **Transport**: Authenticated mTLS HTTPS on the admin control plane
  (`POST /v1/cluster/keydelivery`; the sender fetches the recipient's key from
  `GET /v1/cluster/nodekey`).
- **Crypto**: HPKE (RFC 9180, DHKEM(P-256)-HKDF-SHA256-AES-256-GCM) bound to
  the recipient node's certificate key. Even without mTLS, the payload is
  encrypted so only the right node can decrypt.
- **Authentication**: Cluster peer verifies the sender certificate chains to
  the cluster CA, and the recipient verifies the placement table authorizes the
  delivery (tenant boundary enforcement).
- **Idempotency**: A 32-byte nonce and a 5-minute TTL prevent replay.

### Node certificate requirements

Each node's TLS certificate must carry a SPIFFE URI SAN in the form:

```
spiffe://qrdb.cluster/node/127.0.0.1:63001
```

Where `qrdb.cluster` is the configured trust domain and `127.0.0.1:63001` is
the node's Raft address. This is mandatory. The key-delivery sender verifies
that the recipient certificate has this SAN and that it matches the target node
before sending the key.

See [bootstrap.md](bootstrap.md) for how to generate SPIFFE-SAN node certificates.

### At the receiving node

The receiver imports the blob into its barrier registry. The barrier stays
sealed until the tenant unseals it on that node. Each node's barrier is
independent.

---

## Rebalance and move

### Rebalance plan

```bash
qrdb rebalance report
qrdb rebalance plan --dry-run --max-moves 10 --output json > plan.json
qrdb rebalance execute < plan.json
```

The plan is computed from current cluster load; `execute` runs its moves. For
dedicated-raft databases, each move:

1. **Add**: The destination node is added to the group as an observer; its
   reconciler starts the replica, which catches up via Raft.
2. **Promote**: Once caught up, the observer is promoted to a voter.
3. **Remove**: The source node is removed from the group and its replica is
   stopped.

See [clustering/user.md](clustering/user.md#rebalancing) for reading the
report and the plan.

### Change the replica count

```bash
qrdb database scale payments --replicas 5
```

Adds or removes replicas of one database, observer-first on the way up.

### During a move

- The database remains accessible on the leader.
- Write latency may increase while the new replica catches up.
- The `execute` result lists each failed move with the phase it failed in and
  why; generate a new plan to retry.

---

## Monitoring

### Cluster topology and replica state

```bash
qrdb admin topology
qrdb admin replica-states
```

`topology` shows the full cluster topology as seen from this node
(`GET /admin/topology`). `replica-states` shows the applied index and state-machine hash of every
replica on this node.

### One database's placement

```bash
qrdb database info payments
```

Shows the database's details, including its shard ID and replicas.

### Rebalance load report

```bash
qrdb rebalance report
```

Shows the per-node load distribution and identifies cluster hotspots.

---

## Troubleshooting

### Replica not starting after placement

**Symptom**: A placement entry exists, but the replica is not running.

**Diagnosis**:
1. Check if the tenant barrier is unsealed on this node (self-managed mode):
   ```bash
   qrdb tenant barrier-status --id acme
   ```
   If sealed, unseal it:
   ```bash
   qrdb tenant barrier-unseal --id acme --key <base64>
   ```
2. Check the node logs for reconciler errors:
   ```bash
   journalctl -u qrdbd | grep -i "placement reconciler"
   ```
3. Verify the placement entry:
   ```bash
   qrdb database info payments
   ```

**Resolution**: If the barrier is not unsealed, unseal it first. If the
placement table is empty or wrong, recreate the database. If the node is
unhealthy, check cluster connectivity.

### Key delivery failure

**Symptom**: With an injected (hardware) seal strategy, a replica node has no
imported barrier for the tenant after create. Create itself succeeds; delivery
is best-effort.

**Diagnosis**:
1. Verify mTLS connectivity between nodes:
   ```bash
   curl https://<replica-node>:63002/v1/cluster/nodekey \
     --cert node.crt --key node.key --cacert ca.crt
   ```
2. Check the sending node's logs for key-delivery errors:
   ```bash
   journalctl -u qrdbd | grep -i "clusterkeys"
   ```
3. Verify node certificates have SPIFFE URIs:
   ```bash
   openssl x509 -in node.crt -noout -text | grep -A 1 "Subject Alternative Name"
   ```

**Resolution**: Verify mTLS setup and node certificates.

### Placement inconsistency

**Symptom**: Different nodes report different placements for the same database.

**Diagnosis**: This should not happen if the replicated placement table (in
the system KV) is consistent. Run on each node:
```bash
qrdb database info payments
```

**Resolution**: If you see inconsistency, check the system KV consistency
(`qrdb admin replica-states` on each node) and collect the logs and the
placement entries from all nodes.

### Full cluster restart with sealed barriers

**Scenario**: All nodes restart with system-managed auto-unseal enabled.

**What happens**:
1. All nodes boot with their system barriers sealed.
2. Operator unseals the system barrier on each node; there is no cluster
   auto-unseal.
3. A node waits for its system shard's leader before it serves.
4. On each unsealed node, the system-managed tenant barriers auto-unseal (they
   are wrapped under the system barrier).
5. All replicas start automatically via the reconciler.

**If barriers are self-managed**: Operator must unseal each tenant barrier on
each node (more operational burden, higher security).

---

## See Also

- [multi-tenancy/architecture.md](multi-tenancy/architecture.md) — Isolation
  modes, quotas, entitlements
- [bootstrap.md](bootstrap.md) — TLS setup, SPIFFE URIs, initial cluster
  formation
- [seal/multi-tenant.md](seal/multi-tenant.md) — Barrier lifecycle, key
  management
- [clustering/user.md](clustering/user.md) — Membership, drain, rebalancing
- [multi-tenancy/](multi-tenancy/) — Design and implementation details
