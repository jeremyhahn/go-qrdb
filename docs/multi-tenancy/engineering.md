# Multi-Tenancy — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Tenants() transport.TenantClient
func (c *Client) WithTenant(tenantID string) (transport.KVClient, error)
```

`TenantBarrierService` is a **dedicated gRPC service**
(`qrdb.v1.TenantBarrierService`) with `Init`, `Unseal`, `UnsealShare`, `Seal`,
`Status`, `OfficerRegister`, `OfficerList`, `ShareClaimBegin`,
`ShareClaimComplete`, `OfficerClaimStatus`, `ShamirSealVote`.

Server option: `WithTenantBarrierService(svc tenant.TenantServicer)`.

## Isolation modes

`TenantInfo.IsolationType`, set at creation, defaulting to `prefix`:

| Mode | Raft | Storage | At-rest crypto |
|---|---|---|---|
| `prefix` | shared system shard | shared Pebble | shared system barrier |
| `barrier` | shared system shard | dedicated Pebble for IAM data, from the volume pool | per-tenant DEK over IAM data |
| `volume` | per-tenant local single-node, transport-less Raft host | dedicated filesystem `DataRoot` from the pool | per-tenant DEK over **everything, including the WAL** |
| `raft` | dedicated Raft group per database | SM data under the tenant's `DataRoot`; raft log in the shared host LogDB | per-tenant DEK over the SM data |

## Invariants

- **Isolation type is immutable once the tenant holds data** —
  `ErrTenantHasData`, HTTP 409.
- **`prefix` is logical separation, not cryptographic isolation.**
- **`barrier`, `volume` and `raft` require `storage.volume_pools`.** Without a
  pool, registering a barrier fails with
  `volume pool not configured on barrier registry`.
- **A `volume` tenant's host is built lazily**, only after its barrier is
  unsealed.
- **Entitlements are checked before quotas.** A plan that does not include a
  tier is refused without consulting counters.
- **`Max*` semantics**: `-1` means unlimited, `0` means use the system default.
- **`MaxStorageBytes` for `volume` tenants is reconciled in the background** by
  a reconciler that walks each volume tenant's `DataRoot`, at
  `tenant.quota_reconcile_interval` (default `5m`), to correct drift.
- **Tenant identity is sourced only from the authenticated mTLS identity**,
  never from a header or metadata field.
- **Per-node key independence**: the tenant root key is transported over
  authenticated mTLS with HPKE and **re-wrapped under each receiving node's own
  system barrier** (system-managed) or the tenant's strategy (self-managed).
- **Replica placement is quota-checked and spread across distinct healthy
  nodes**; `TenantShardReconciler` converges each node's assigned replicas and
  they survive restarts via the persistent placement table.

## Governance planes

| Plane | Controls | Enforced at |
|---|---|---|
| RBAC permissions | which operation a principal may call at all | transport middleware |
| Numeric quotas | how many of a resource a tenant may hold | service layer, before delegating |
| Feature entitlements | which tiers/features the plan permits | service layer, **before** the quota check |

**Do not collapse entitlements into RBAC roles.** They answer different
questions and are owned by different people.

Provisioning-governance permissions: `tenants:set_isolation`,
`tenants:set_entitlements`, `databases:create_raft`, `databases:create_volume`.

### Quotas — `quota.Quotas`

`MaxDatabases`, `MaxRaftGroups`, `MaxReplicasPerGroup`, `MaxTotalReplicas`,
`MaxStorageBytes`, `MaxDatabaseSizeBytes`, plus `MaxCollections`, `MaxKeys`,
`MaxUsers`, `MaxRoles`, `MaxCertificates`, `MaxBackups`, `MaxVectorDims`,
`MaxTSMetrics`. Each has a matching `Current*` counter on the tenant record.

### Entitlements — `quota.Entitlements`

| Field | Permits |
|---|---|
| `AllowedIsolationModes` | which isolation modes the tenant may select |
| `AllowDedicatedRaft` | provisioning dedicated Raft-backed databases |
| `AllowVolumeStorage` | provisioning databases on a dedicated volume |
| `AllowSystemManagedBarrier` | opting an isolated tenant into system-managed auto-unseal |
| `AllowedBarrierModes` | which seal strategies an isolated tenant may pick; **empty = built-in software defaults, hardware fail-closed** |

A request for a mode or feature the plan does not grant is rejected with
`*EntitlementError`.

## Configuration

```yaml
tenant:
  default_quotas:
    max_databases: 5
    max_raft_groups: 0
    max_replicas_per_group: 3
    max_total_replicas: 3
    max_storage_bytes: 1073741824   # 1 GiB
    system_barrier_quota: 0         # Isolated tenants denied system-barrier use by default
  default_entitlements:
    allowed_isolation_modes: [prefix]
    allow_dedicated_raft: false
    allow_volume_storage: false
    allow_system_managed_barrier: false
    allowed_barrier_modes: []       # Empty = software defaults (none/passphrase/shamir); hardware fail-closed
  quota_reconcile_interval: 5m
```

These are the built-in defaults: a tenant with no explicit plan may create up to
5 databases, consume up to 1 GiB, use only shared `prefix` isolation, and use only

**The built-in defaults are deliberately conservative**: 5 databases, 1 GiB,
`prefix` isolation only, and dedicated raft, volume storage, system-managed
barrier, system-barrier quota and hardware seal strategies **all denied** until
an operator grants them. Full reference:
[../configuration/](../configuration/).

## Encryption at rest

| Data | Encryption |
|------|------------|
| State-machine Pebble (shared shard) | Barrier-encrypting filesystem under the system barrier |
| Shared Raft WAL / LogDB | Encrypted at rest under the **system barrier** (committed entries are no longer cleartext on disk) |
| `volume` / `raft` tenant SM data | Encrypted under the **tenant DEK** on the tenant's `DataRoot` |
| `volume` tenant raft WAL | Encrypted under the tenant DEK (the per-tenant host barrier-wraps its own WAL) |
| `raft` tenant raft log (shared host) | Encrypted under the **system barrier** (plaintext-at-rest gap closed) |


> **Not implemented.** A **dedicated per-tenant WAL** for `raft`-mode groups
> (Phase 4d Option C) is **designed but not built**. It would encrypt the
> tenant's raft log under its own key on the shared host rather than the shared
> system key. It requires a go-quicraft per-barrier snapshot-pool rework
> (per-barrier epoch tracking) first. Plan:
> [design/4d-per-tenant-wal-option-c.md](../design/4d-per-tenant-wal-option-c.md).

## Deployment requirement

**The tenant service must be configured in production.**

Tenant identity is sourced only from the authenticated mTLS identity, and
unknown-tenant requests are rejected by the tenant service. **If a deployment
wires the data-plane service registry without a tenant service** — a
backward-compatibility path for single-tenant embeddings — **that
unknown-tenant rejection is skipped**: any tenant name presented by an
authenticated client is accepted. It is still namespaced and barrier-checked,
but it is **not validated against a tenant registry**.

That mode is intended only for trusted single-tenant or embedded use. A
production multi-tenant deployment that omits the tenant service has a real
isolation gap.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Changing isolation with data present | `ErrTenantHasData`, 409 | **yes** |
| Isolation mode not in `AllowedIsolationModes` | `*EntitlementError` | **yes** |
| Seal strategy not in `AllowedBarrierModes` | `*EntitlementError` | **yes** — hardware is fail-closed by default |
| Quota exceeded | quota error, before delegating | **yes** |
| Barrier tier without `storage.volume_pools` | `volume pool not configured on barrier registry` | **yes** |
| Unknown tenant, tenant service configured | rejected | **yes** |
| Unknown tenant, **no** tenant service | **accepted** — the documented gap above | **no** |
| Tenant barrier sealed | tenant operations refused | **yes** |

## Test strategy

- **Unit**: entitlement checks running before quota checks, `Max*` semantics for
  `-1` and `0`, the isolation-change refusal, and the volume-pool precondition.
- **The entitlement-before-quota ordering is its own test** — reversing it would
  leak information about counters to a plan with no access to the tier.
- **The no-tenant-service path is tested explicitly** so the gap stays visible
  rather than becoming a surprise.
- **Integration**: the multi-node tenant suites —
  `isolation_raft_cluster_test.go`, `isolation_raft_dedicated_wal_test.go`,
  `isolation_raft_rebalance_test.go`, `isolation_raft_scale_test.go` — live in
  the **cluster stage**, which is **not currently wired into CI**.

## Observability

`Current*` counters on the tenant record are the live usage view; the quota
reconciler corrects `volume` tenants' storage drift on its interval. Tenant
provisioning, quota and entitlement changes are audited through
[../audit/](../audit/).
