<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Multi-Tenancy: Isolation, Quotas & Entitlements

QRDB serves many tenants and many databases per tenant from a single cluster
endpoint. The tenant boundary is enforced by **who you authenticate as** (the
mTLS identity), not by a client-supplied value. This guide ties together the
three things an operator configures per tenant:

1. **Isolation** — how strongly a tenant's data is separated (the isolation
   ladder), from a shared key prefix up to a dedicated Raft group.
2. **Quotas** — numeric caps on what a tenant may consume (databases, raft
   groups, replicas, storage bytes).
3. **Entitlements** — which isolation tiers and features a tenant's plan
   permits at all.

For request-time scoping (how a call selects a tenant and database) see
[scoping.md](scoping.md). For the database lifecycle see
[databases/README.md](databases/README.md). For barrier key custody see
[seal/multi-tenant.md](seal/multi-tenant.md). The internal design rationale
lives in [design/multi-tenant-isolation-quota.md](design/multi-tenant-isolation-quota.md).

---

## The bootstrap tenant and database

The reserved, structurally privileged tenant is named `system`
(`tenant.SystemTenantID`). An authenticated identity that carries no tenant
resolves to `system`. The implicit bootstrap database — the one selected when a
request supplies no database scope — is also named `system`
(`tenant.DefaultDatabase`). Selecting the `system` database explicitly is
equivalent to selecting no database scope at all.

`CreateTenant` rejects `system` as a tenant id, and the `system` tenant may not
use any dedicated isolation mode (`barrier`, `volume`, or `raft`) — it must stay
reachable during bootstrap while tenant barriers are still sealed.

---

## The isolation ladder

A tenant's isolation mode is set on `TenantInfo.IsolationType` at creation and
defaults to `prefix`. Each rung adds stronger separation at a higher resource
and operational cost.

| Mode (`IsolationType`) | Raft | Storage | At-rest crypto | What is isolated |
|------------------------|------|---------|----------------|------------------|
| **`prefix`** (default) | Shared system shard | Shared Pebble | Shared system barrier | Logical key prefix only |
| **`barrier`** | Shared system shard | Dedicated Pebble for IAM data, from the volume pool | Per-tenant DEK (IAM data) | Key prefix + a per-tenant cryptographic barrier over the tenant's IAM data |
| **`volume`** | Per-tenant local single-node Raft host | Dedicated filesystem `DataRoot` from the volume pool | Per-tenant DEK over the tenant's entire data set, including its WAL | All of the tenant's data lives under its own `DataRoot`, full engines (KV/MVCC/lease/watch/timeseries) |
| **`raft`** | Dedicated Raft group per database | State-machine data under the tenant's `DataRoot`; raft log in the shared host LogDB | Per-tenant DEK over the SM data | Each database is its own Raft shard |

### `prefix` — shared store, logical key prefix (default)

The tenant's keys are prefixed with the tenant id against the shared `system`
store on the shared Raft shard. No per-tenant barrier is applied. This is the
lowest-overhead mode and the default when no isolation type is specified. It is
logical separation, not cryptographic isolation.

### `barrier` — per-tenant cryptographic barrier for IAM data

Adds a dedicated per-tenant `TenantBarrier` (its own DEK and `{tenantID}/`
namespace in an independent backend allocated from the storage volume pool) over
the tenant's IAM data. The tenant's KV data plane still rides the shared shard
with key prefixing. Requires `storage.volume_pools` to be configured; without a
pool, registering a barrier fails with `volume pool not configured on barrier
registry`. See [seal/multi-tenant.md](seal/multi-tenant.md) for the barrier
lifecycle, officer enrollment, and seal strategy selection.

### `volume` — dedicated filesystem volume, full engines

The tenant receives a dedicated filesystem `DataRoot` allocated from the volume
pool, hosting a **per-tenant single-node, transport-less Raft host** with the
full engine set (KV, MVCC, lease, watch, time-series). All of the tenant's data
— state machine **and** raft WAL — lives under `{DataRoot}` and is encrypted at
rest under the tenant's own DEK via a per-tenant barrier. The host is built
lazily, only after the tenant barrier is unsealed. Requires the volume pool and
the seal subsystem.

### `raft` — dedicated Raft group per database with multi-node replication

Each of the tenant's databases runs as its own Raft shard, replicated across
cluster nodes. The tenant gets a dedicated `DataRoot` from the pool and a
per-tenant barrier; the shard's state-machine data is rooted under
`{DataRoot}/databases/{db}` and encrypted under the tenant DEK on each node,
while the raft log stays in the shared host LogDB. Requires the volume pool and
the seal subsystem.

**Multi-node replication is production-ready.** A requested replication factor is
quota-checked and placed across distinct healthy nodes using a load-aware
scheduler. Each node's `TenantShardReconciler` watches the replicated placement
table and converges its assigned replicas (start on boot, follow placement
changes, stop on demotion). The tenant root key is securely transported to each
replica node over an authenticated mTLS control plane using HPKE encryption, with
each node re-wrapping the key under its own system barrier (for system-managed
auto-unseal) or the tenant's strategy (for self-managed unseal). Replicas survive
node restarts with no data loss via the persistent placement table and per-node
replica re-attach at boot.

```
prefix   shared raft, shared pebble, logical key prefix      (DEFAULT)
   |
barrier  shared raft, dedicated pebble for IAM, per-tenant DEK
   |
volume   per-tenant local raft host, own FS DataRoot,
         full engines, all data under tenant DEK (incl. WAL)
   |
raft     dedicated raft group per database, SM data under
         tenant DataRoot + DEK, raft log in shared host LogDB
         (multi-node replication: in development)
```

Changing a tenant's isolation type after it holds data is rejected with
`ErrTenantHasData` (HTTP 409) — migrate or delete the tenant's data first.

---

## The three governance planes

The planes are deliberately kept orthogonal. A provisioning call must pass
**all three**; do not collapse entitlements into RBAC roles.

| Plane | Controls | Enforced at |
|-------|----------|-------------|
| **RBAC permissions** | Which operation a principal may call at all | Transport middleware |
| **Numeric quotas** | How many of a resource a tenant may hold | Service layer (before delegating) |
| **Feature entitlements** | Which isolation tiers / features a tenant's plan permits | Service layer (before the quota check) |

### RBAC permissions

The provisioning-governance permissions added for multi-tenancy:

| Permission | Grants |
|------------|--------|
| `tenants:set_isolation` | Change a tenant's data-isolation mode |
| `tenants:set_entitlements` | Change a tenant's feature entitlements |
| `databases:create_raft` | Create a dedicated-Raft-backed database |
| `databases:create_volume` | Create a database on a dedicated volume |

See [permissions/README.md](permissions/README.md) for the full registry.

### Quotas

`quota.Quotas` carries per-resource caps and live usage counters. A `Max*`
value of `-1` means unlimited; `0` means use the system default.

| Field | Caps |
|-------|------|
| `MaxDatabases` | Number of databases the tenant may hold |
| `MaxRaftGroups` | Number of dedicated Raft groups |
| `MaxReplicasPerGroup` | Replication factor of any one dedicated-raft database |
| `MaxTotalReplicas` | Aggregate replicas across all the tenant's raft groups |
| `MaxStorageBytes` | Total on-disk bytes across the tenant's data |
| `MaxDatabaseSizeBytes` | On-disk size of any single database |

Additional caps exist for `MaxCollections`, `MaxKeys`, `MaxUsers`, `MaxRoles`,
`MaxCertificates`, `MaxBackups`, `MaxVectorDims`, and `MaxTSMetrics`. Each cap
has a matching `Current*` counter persisted on the tenant record.

`MaxStorageBytes` usage for `volume` tenants is reconciled by a background
reconciler that periodically walks each volume tenant's `DataRoot` to correct
drift (interval set by `tenant.quota_reconcile_interval`, default `5m`).

### Entitlements

`quota.Entitlements` gates which tiers a plan may use at all — checked **before**
the numeric quota:

| Field | Permits |
|-------|---------|
| `AllowedIsolationModes` | The isolation modes the tenant may select (e.g. `["prefix","volume","raft"]`) |
| `AllowDedicatedRaft` | Provisioning dedicated Raft-backed databases |
| `AllowVolumeStorage` | Provisioning databases on a dedicated volume |
| `AllowSystemManagedBarrier` | Opting a dedicated-isolation tenant into system-managed barrier auto-unseal |

A request for a mode or feature the plan does not grant is rejected with an
`*EntitlementError`. The built-in defaults are conservative: prefix-only, with
dedicated raft, volume storage, and system-managed barrier all denied until an
operator grants them.

---

## Configuration defaults

A tenant created without its own quotas/entitlements inherits the deployment
defaults from the `tenant:` block of the YAML config:

```yaml
tenant:
  default_quotas:
    max_databases: 5
    max_raft_groups: 0
    max_replicas_per_group: 3
    max_total_replicas: 3
    max_storage_bytes: 1073741824   # 1 GiB
  default_entitlements:
    allowed_isolation_modes: [prefix]
    allow_dedicated_raft: false
    allow_volume_storage: false
    allow_system_managed_barrier: false
  quota_reconcile_interval: 5m
```

These are the built-in defaults: a tenant with no explicit plan may create up to
5 databases, consume up to 1 GiB, and use only shared `prefix` isolation. The
dedicated raft and volume tiers stay denied until the operator grants them. See
[12-configuration.md](12-configuration.md) for the full reference.

---

## Operating tenants

### Create a tenant with an isolation mode

```bash
# Default prefix isolation
qrdb tenant create --id acme --name "Acme Corp"

# Dedicated-volume tenant (requires the volume_storage entitlement + a volume pool)
qrdb tenant create --id acme --name "Acme Corp" --isolation-type volume

# Opt a dedicated-isolation tenant into system-managed barrier auto-unseal
qrdb tenant create --id acme --name "Acme Corp" --isolation-type volume --barrier-auto-unseal
```

REST: `POST /v1/tenants` with `{"id","name","isolation_type","barrier_auto_unseal"}`.

### Set quotas

```bash
qrdb tenant update-quotas acme \
  --max-databases 20 \
  --max-raft-groups 4 \
  --max-replicas-per-group 3 \
  --max-total-replicas 9 \
  --max-storage-bytes 10737418240 \
  --max-database-size-bytes 2147483648
```

REST: `POST /v1/tenants/{id}/quotas`. SDK: `client.Tenants().UpdateTenantQuotas`.

### Set entitlements

```bash
qrdb tenant set-entitlement acme \
  --isolation-modes prefix,volume,raft \
  --allow-dedicated-raft \
  --allow-volume-storage \
  --allow-system-managed-barrier
```

REST: `PATCH /v1/tenants/{id}/entitlements`. SDK:
`client.Tenants().UpdateTenantEntitlements`. Requires `tenants:set_entitlements`.

### Create a dedicated-raft database with a replication factor

```bash
# --replicas applies to the raft backend only; a higher factor is clamped to the cluster size
qrdb database create payments --engine kv --backend raft --replicas 3
```

The requested factor is validated against `MaxReplicasPerGroup` and the tenant's
aggregate `MaxTotalReplicas` budget, then clamped to the live cluster size.

---

## Replica placement and multi-node replication

When a dedicated-raft database is created, the service computes a load-aware
placement decision — RF distinct, healthy, least-loaded nodes with one voting
replica per node — and persists it to a replicated placement table in the system
KV under `__sys:placement/{tenantID}/{database}` (`pkg/placement`). The raft
backend factory reads this table as the source of truth when resolving a shard
on the local node.

**All components are production-ready:**

- **Placement scheduling** (Phase 4c-1b): load-aware placement that minimizes shard moves
  and keeps every node ≤ (1+ε)·average load.
- **Secure key delivery** (Phase 4c-1c): the leader transports each tenant's root key
  to follower nodes over an authenticated mTLS control plane with HPKE recipient-binding
  and replay protection.
- **Per-node replica lifecycle** (Phase 4c-1d): each node's `TenantShardReconciler`
  watches the placement table and manages local replicas (boot re-attach, convergence,
  teardown on demotion).
- **Rebalance primitive** (Phase 4c-2): moving a replica delivers the key to the
  destination, updates the placement, and removes the source.

### Per-node-independent key design

The multi-node design preserves **per-node system-KEK independence** — there is
no shared cluster KEK. Each node maintains its own system barrier with its own
master key. When a tenant's database replica is placed on a node, the placement
leader transports the tenant root key over an authenticated mTLS control plane
to that node using HPKE encryption bound to the recipient node's certificate
pubkey. Each node persists its **own** local copy of the tenant barrier and
protects it according to the tenant's `BarrierAutoUnseal` choice:

- **System-managed auto-unseal** — the tenant root key is re-wrapped under
  *that node's* system barrier and persisted. At boot, the system barrier
  unseals automatically, and the tenant barrier auto-resolves its key. The
  replica starts automatically.
- **Self-managed unseal** — the tenant root key is protected under the tenant's
  own seal strategy and persisted. At boot, the tenant barrier comes up sealed,
  and the tenant must explicitly unseal it on **each** replica node before that
  node's replica starts.

See [seal/multi-tenant.md → Tenant barrier key management](seal/multi-tenant.md#tenant-barrier-key-management)
for the mode selection and [design/multi-tenant-isolation-quota.md](design/multi-tenant-isolation-quota.md)
for the complete design rationale.

---

## Encryption at rest

| Data | Encryption |
|------|------------|
| State-machine Pebble (shared shard) | Barrier-encrypting filesystem under the system barrier |
| Shared Raft WAL / LogDB | Encrypted at rest under the **system barrier** (committed entries are no longer cleartext on disk) |
| `volume` / `raft` tenant SM data | Encrypted under the **tenant DEK** on the tenant's `DataRoot` |
| `volume` tenant raft WAL | Encrypted under the tenant DEK (the per-tenant host barrier-wraps its own WAL) |
| `raft` tenant raft log (shared host) | Encrypted under the **system barrier** (plaintext-at-rest gap closed) |

> A **dedicated per-tenant WAL** for `raft`-mode groups (Phase 4d Option C) is **designed
> but not yet implemented**. This would encrypt the tenant's raft log under its own key
> on the shared host, rather than the shared system-key WAL. Requires go-quicraft
> per-barrier snapshot-pool rework for full correctness (per-barrier epoch tracking).
> Sequenced after Phase 4c is solid. See [design/4d-per-tenant-wal-option-c.md](design/4d-per-tenant-wal-option-c.md)
> for the complete plan.

See [seal/README.md](seal/README.md) for the StorageBarrier architecture.

---

## Deployment note: the tenant service must be configured

Tenant identity is sourced **only** from the authenticated mTLS client identity
(never a header/metadata field), and unknown-tenant requests are rejected by the
tenant service. If a deployment wires the data-plane service registry **without**
a tenant service (a backward-compatibility path for single-tenant embeddings),
that unknown-tenant rejection is skipped — any tenant name presented by an
authenticated client is accepted (still namespaced and barrier-checked, but not
validated against a tenant registry). **Production multi-tenant deployments must
configure the tenant service** so tenant existence and ownership are enforced;
the registry-without-tenant-service mode is intended only for trusted
single-tenant/embedded use.

---

## See Also

- [scoping.md](scoping.md) — request-time tenant + database scoping
- [databases/README.md](databases/README.md) — database lifecycle and management
- [seal/multi-tenant.md](seal/multi-tenant.md) — per-tenant barriers and key custody
- [permissions/README.md](permissions/README.md) — permission registry
- [12-configuration.md](12-configuration.md) — YAML configuration reference
- [design/multi-tenant-isolation-quota.md](design/multi-tenant-isolation-quota.md) — internal design
