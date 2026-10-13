<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Deploying Multi-Node IsolationRaft Clusters

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-13
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

This guide covers the operational considerations and deployment patterns for
running tenant databases with `IsolationRaft` isolation across multiple cluster
nodes. For the design rationale and implementation details, see
[design/multi-tenant-isolation-quota.md](design/multi-tenant-isolation-quota.md).

---

## Prerequisites

### Cluster setup

- A working qrdb Raft cluster with 3+ nodes (multi-node replication requires
  distinct nodes).
- All nodes agree on a single **cluster trust domain** (default: `qrdb.cluster`).
  This value is used in node certificate SPIFFE URIs and must be consistent
  across all nodes.
- All nodes have a configured **volume pool** (e.g., a shared NFS mount or
  per-node local filesystem). Each node must have the same pool root path.
  Operator expectation: homogeneous topology — same mount point on every node.
- The cluster uses **mTLS authentication** (production default). Plaintext mode
  (no client cert) is supported for development/testing but is not recommended.
- Node certificates carry a **SPIFFE URI SAN** in the form
  `spiffe://{trustDomain}/node/{raftAddr}`. This is mandatory for the secure
  per-node key delivery (Phase 4c-1c).

### Tenants and entitlements

- The tenant has an `AllowedIsolationModes` entitlement that includes `raft`.
- The tenant has `AllowDedicatedRaft: true` in their entitlements.
- Quotas are configured: `MaxRaftGroups`, `MaxReplicasPerGroup`, `MaxTotalReplicas`,
  and `MaxStorageBytes`.

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

Each node must have the same pool path configured:

```yaml
storage:
  volume_pools:
    - name: "default"
      path: "/var/qrdb/pools/default"   # Same path on every node
```

The pool is homogeneous — operator must ensure the same mount point exists
on all nodes. A tenant's dedicated store is allocated under the pool's
path, so its replicas can resolve the same path on each node.

### Barrier and key management

```yaml
seal:
  strategy: "software"  # or "shamir", depending on your policy
  
tenant:
  default_entitlements:
    allowed_isolation_modes: [prefix, volume, raft]
    allow_dedicated_raft: true
    allow_volume_storage: true
    allow_system_managed_barrier: false  # Operator choice
  
  default_quotas:
    max_raft_groups: 10
    max_replicas_per_group: 3
    max_total_replicas: 30
    max_storage_bytes: 10737418240  # 10 GiB
```

**BarrierAutoUnseal choice:**

- `false` (default, self-managed): tenant must unseal their barrier on each
  node after restart. Higher security, unattended-restart cost.
- `true` (system-managed): tenant barrier is auto-unsealed under the system
  barrier; replicas start automatically after restart. Lower security, higher
  convenience.

Both modes are fully supported in multi-node deployments.

---

## Creating an IsolationRaft database

### Single command

```bash
qrdb database create --tenant acme --database payments \
  --backend raft \
  --replicas 3
```

The replication factor is clamped to `min(requested, cluster_size)`, so a
request for `--replicas 5` on a 3-node cluster places 3 replicas. If you
request more than `MaxReplicasPerGroup` or the aggregate `MaxTotalReplicas`
budget, the operation fails.

### REST API

```bash
curl -X POST https://localhost:9090/v1/databases \
  -H "X-QRDB-Database: payments" \
  -H "X-QRDB-Tenant: acme" \
  -H "Content-Type: application/json" \
  --cert client.crt --key client.key \
  --cacert ca.crt \
  -d '{
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

- **Create database wizard** (raft mode): a **Replication Factor** input and a
  **Dedicated WAL** toggle (raft-only) — the toggle sends `dedicatedWal` on
  create.
- **Tenant detail → Quotas**: `Max Replicas / Group`, `Max Total Replicas`, and
  `Max Dedicated WAL Groups` fields.
- **Tenant detail → Entitlements**: `Allow Dedicated Raft`, `Allow Volume
  Storage`, `Allow System-Managed Barrier`, and `Allow Dedicated WAL` toggles,
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

1. **Placement decision** (Phase 4c-1b): The service picks 3 distinct,
   least-loaded, healthy nodes. The least-loaded node is elected leader.
2. **Placement persistence**: The decision is persisted to the replicated
   placement table in the system KV: `__sys:placement/acme/payments`.
3. **Key distribution** (Phase 4c-1c): If the request came from the leader node,
   the tenant barrier's root key is immediately sent to each follower node over
   an authenticated mTLS control plane. Non-leader replicas receive the key when
   they become involved in a rebalance or takeover.
4. **Replica start** (Phase 4c-1d): Each node's `TenantShardReconciler` watches
   the placement table, sees the new entry, and calls `Attach` to start the
   local replica (if the barrier is unsealed). Replicas converge to "running"
   within ~5 seconds (watch-based) or ~30 seconds (polling fallback).

---

## What happens on node restart

### Boot sequence (all nodes)

1. System barrier unseals (operator action for self-managed, automatic for
   system-managed).
2. The replicated placement table is loaded into the system KV.
3. `TenantShardReconciler.Run()` is launched and calls `ReconcileOnce`.
4. For each placed replica assigned to this node:
   - If the barrier is unsealed and the shard is not already running, call
     `Attach` to start it.
   - If the barrier is sealed (self-managed mode), defer and wait for the
     tenant to unseal.
5. After reconciliation, the node begins serving traffic. Sealed replicas are
   reachable but return "tenant barrier sealed" errors until unsealed.

### Leader restart

If the current leader node restarts:
1. Raft will elect a new leader (quorum-based).
2. The old leader rejoins as a follower.
3. The placement table stays unchanged (it's replicated).
4. New replicas are not spawned automatically; if you want to rebalance,
   use the rebalance API explicitly.

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

1. Configure and start the new node with the same trust domain and pool root.
2. Cluster peer discovery brings it into the Raft cluster.
3. Subsequent database creates can place replicas on the new node.
4. Existing databases are not automatically rebalanced; use the rebalance API
   to move replicas if desired.

### Node leave (removing a node from the cluster)

1. Stop the node gracefully (e.g., SIGTERM).
2. Remove it from the cluster discovery (if using static peer lists).
3. A graceful shutdown triggers the reconciler to close assigned replicas.
4. The leader will eventually remove the node from the placement table (if
   consensus is quorum-based on the remaining nodes).
5. Any unclean shutdown (crash) will trigger Raft membership change on the
   quorum; replicas on the crashed node are automatically demoted.

For a controlled rebalance, move replicas away from the node before stopping it.

---

## Secure key delivery (Phase 4c-1c)

### Architecture overview

Each tenant's dedicated-raft database has a per-tenant barrier with a root key.
When the database is created or a replica is moved, the leader must securely
deliver the tenant root key to each follower node. The delivery uses:

- **Transport**: Authenticated mTLS HTTPS on the admin control plane
  (`POST /v1/cluster/keydelivery`).
- **Crypto**: HPKE (RFC 9180) encryption bound to the recipient node's
  certificate pubkey (P-256 ECDH). Even without mTLS, the payload is encrypted
  so only the right node can decrypt.
- **Authentication**: Cluster peer verifies the sender certificate chains to
  the cluster CA, and the recipient verifies the placement table authorizes the
  delivery (tenant boundary enforcement).
- **Idempotency**: Nonce + TTL prevent replay.

### Node certificate requirements

Each node's TLS certificate must carry a SPIFFE URI SAN in the form:

```
spiffe://qrdb.cluster/node/127.0.0.1:50001
```

Where `qrdb.cluster` is the configured trust domain and `127.0.0.1:50001` is
the node's Raft address. This is mandatory. The key-delivery sender verifies
that the recipient certificate has this SAN and that it matches the target node
before sending the key.

See [bootstrap.md](bootstrap.md) for how to generate SPIFFE-SAN node certificates.

### Key re-wrapping at each node

When a node receives the tenant root key, it does NOT store it plaintext.
Instead:

- **System-managed auto-unseal**: The key is immediately re-wrapped under *that
  node's* system barrier (using AES-GCM under the system master key) and
  persisted. The plaintext is wiped from memory (`enclave.WipeBytes`).
- **Self-managed**: The key is persisted in the sealed form it arrived in
  (already HPKE-sealed by the sender; double-sealed under the sender's view).
  The tenant must unseal on this node to unwrap and use the key.

No plaintext tenant key ever sits on disk across nodes. Each node's barrier is
independent.

---

## Rebalance and move

### Rebalance API

```bash
qrdb rebalance auto
```

Computes a new placement decision (using current cluster load) and moves
replicas to reach the target. For dedicated-raft databases:

1. **Add**: Deliver the tenant key to the destination node.
2. **Wait**: The destination node's reconciler starts the replica and catches up
   via Raft snapshot transfer.
3. **Leader transfer** (if rebalancing leadership): Transfer leader to the new
   node (if load-balancing requires it).
4. **Remove**: The source node is removed from Raft quorum and the replica is
   stopped.

### Move a single replica

```bash
qrdb rebalance move --tenant acme --database payments \
  --from 127.0.0.1:50001 --to 127.0.0.2:50002
```

Moves a replica from one node to another, reusing the same logic as `rebalance auto`.

### During a move

- The database remains accessible on the leader.
- Read consistency is maintained (strong reads wait for the new replica to catch up).
- Write latency may increase while the snapshot is transferred.
- A crash during the move is safe: the placement table is replicated, so the
  move will be retried on recovery.

---

## Monitoring

### Replica status per node

```bash
qrdb cluster shards
```

Lists all shards (system + user databases) on the local node, including:
- Shard ID, Tenant, Database, Replication Factor
- State (running / starting / stopped / sealed)
- Leader replica (if applicable)
- Replica count on this node

### Placement table

```bash
qrdb admin placement list --tenant acme --database payments
```

Shows the replicated placement decision:

```
TenantID: acme
Database: payments
ShardID: 12345
RF: 3
Leader: Replica 1 (127.0.0.1:50001)

Replicas:
  Replica 1 (127.0.0.1:50001) [leader]
  Replica 2 (127.0.0.2:50002)
  Replica 3 (127.0.0.3:50003)
```

### Rebalance load report

```bash
qrdb admin imbalance-report
```

Shows the current load distribution across nodes and recommends placement changes.

---

## Troubleshooting

### Replica not starting after placement

**Symptom**: A placement entry exists, but the replica is not running.

**Diagnosis**:
1. Check if the barrier is unsealed (self-managed mode):
   ```bash
   qrdb barrier status --tenant acme
   ```
   If sealed, unseal it:
   ```bash
   qrdb barrier unseal --tenant acme
   ```
2. Check the node logs for reconciler errors:
   ```bash
   journalctl -u qrdbd | grep -i "reconcil"
   ```
3. Verify the placement entry:
   ```bash
   qrdb admin placement list --tenant acme --database payments
   ```

**Resolution**: If the barrier is not unsealed, unseal it first. If the
placement table is empty or wrong, recreate the database. If the node is
unhealthy, check cluster connectivity.

### Key delivery timeout

**Symptom**: Database create fails with "key delivery timeout" or replica
starts with "barrier not unsealed".

**Diagnosis**:
1. Verify mTLS connectivity between nodes:
   ```bash
   # From leader node
   curl -X POST https://<follower>:9091/v1/cluster/keydelivery \
     --cert node.crt --key node.key --cacert ca.crt
   ```
2. Check the leader logs for key-delivery errors:
   ```bash
   journalctl -u qrdbd | grep -i "keydelivery"
   ```
3. Verify node certificates have SPIFFE URIs:
   ```bash
   openssl x509 -in node.crt -noout -text | grep -A 1 "Subject Alternative Name"
   ```

**Resolution**: Verify mTLS setup and node certificates. If the follower node
is temporarily offline, the create will fail; retry after the node comes back.

### Placement inconsistency

**Symptom**: Different nodes report different placements for the same database.

**Diagnosis**: This should not happen if the replicated placement table (in
the system KV) is consistent. Check:
```bash
qrdb admin placement list --tenant acme --database payments
```

**Resolution**: If you see inconsistency, check the system KV consistency
(possible rare race or bug). File an issue with the logs and the placement
entries from all nodes.

### Full cluster restart with sealed barriers

**Scenario**: All nodes restart with system-managed auto-unseal enabled.

**What happens**:
1. All nodes boot with their system barriers sealed.
2. Operator unseals the system barrier (once, on any node).
3. Raft replicates the unseal operation.
4. All nodes converge to system-barrier-unsealed within ~5 seconds.
5. All tenant barriers auto-unseal (they are wrapped under the system barrier).
6. All replicas start automatically via the reconciler.

**If barriers are self-managed**: Operator must unseal each tenant barrier on
each node (more operational burden, higher security).

---

## See Also

- [multi-tenancy.md](multi-tenancy/architecture.md) — Isolation modes, quotas, entitlements
- [bootstrap.md](bootstrap.md) — TLS setup, SPIFFE URIs, initial cluster
  formation
- [seal/multi-tenant.md](seal/multi-tenant.md) — Barrier lifecycle, key
  management
- [design/multi-tenant-isolation-quota.md](design/multi-tenant-isolation-quota.md) — Design
  and implementation details of Phase 4c
