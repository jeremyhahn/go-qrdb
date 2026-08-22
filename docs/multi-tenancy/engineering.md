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

- **A resolver-served store is confined to its tenant's keyspace by
  construction.** `StoreResolver.Resolve` returns the system store wrapped in
  `kvstore.NewNamespacedKVStore` at `{tenant}/` for every shared-prefix tenant
  (including the system tenant and unknown tenants). Returning the bare shared store gave every prefix tenant one
  keyspace — the #41 defect. A caller must never have to remember to prefix.
- **The resolver-served store namespaces at `{tenant}/`, not
  `{tenant}/{db}/`.** It serves identity, and identity is tenant-wide — a user
  authenticates whichever database a request names. Per-database namespacing
  here breaks login, because firstboot writes the admin user with no database
  bound. Data-plane database scoping is the transports' and SQL provider's job
  and is unaffected.
- **No prefix is exempt from namespacing**, the reserved `__sys:` keyspace
  included. An exemption would be a second tenancy mechanism beside the
  namespace; it also let a crafted tenant id, a crafted DAO entity type, and
  `pkg/permission`'s tenantless scope keys each reach across tenants.
- **A by-id IAM operation routes to the tenant that HOLDS the id, not the
  caller's.** `tenant.IdentityIndex` maps `id -> tenant` in the system tenant's
  identity namespace; `tenant.OwningTenant` is the single rule both routers
  apply. Unindexed ids route to the caller (reach never widens), a caller who
  may not reach the owner is routed to its own tenant and gets a plain
  not-found (a cross-tenant denial would be an existence oracle), and a sealed
  owning barrier refuses with `ErrTenantSealed` — never a fallback read.
- **The index entry carries the id and the tenant ONLY.** It lives outside
  every barrier by necessity, so it may leak existence and placement and
  nothing else.
- **The index is maintained by the function that writes the record**, next to
  the record's other index writes — never by a sibling maintainer, which is how
  every write-path/read-path divergence in this repository started.
- **An index entry is a CLAIM, taken atomically, before the record is
  written.** Role ids are caller-supplied, so a last-writer-wins entry would
  let one tenant capture another's id and redirect every by-id operation on
  it. The claim uses `kvstore.AtomicKVStore.PutIfAbsent`, which every state
  machine implements — `PebbleStateMachine`, `MVCCEngine` and `KVEngine`,
  the last of which gained its handler with #41. A store without it is
  REFUSED (`tenant.ErrIdentityStoreNotAtomic`), never downgraded to
  read-then-write. Refusing before the first write is what leaves a refused
  capture with nothing behind.
- **A claim is released through a GUARDED delete**
  (`kvstore.AtomicKVStore.CompareAndDelete`), never a plain one. A guarded
  create with an unguarded release is half a primitive: two releasers both
  read holder A, the first deletes, another tenant legitimately claims the
  now-absent id, and the second delete removes THEIR claim.
- **A failed write keeps its quota reservation only when its outcome is
  UNKNOWN.** `quotaKVService` reserves bytes before the inner write and must
  decide, on an error, whether the tenant still owes them. Three cases, not
  two: a definitive REFUSAL (a guard evaluated and declined) releases; a
  definitive FAILURE releases, because it proves nothing was stored —
  validation, encoding, `NotLeader`, a state machine that applied nothing,
  a refused atomic capability; anything else RETAINS, because a Raft proposal
  can commit and then fail to deliver its response.
  The default for an unrecognised error is to retain. Both mistakes are real
  and they are not symmetric. Releasing an unknown outcome under-counts a
  value that IS stored, and the limit stops bounding anything. Retaining a
  definitive rejection leaks bytes on every invalid request — and for a
  **prefix tenant that is permanent**, because the reconciler repairs volume
  tenants from real storage and a prefix tenant's usage only ever climbs, so
  a caller sending invalid requests in a loop could exhaust the quota without
  writing a byte.
- **A claim is EDITED through a guarded swap**
  (`kvstore.AtomicKVStore.CompareAndSwap`, added in #56), never a plain put.
  It completes the set: `PutIfAbsent` takes a claim, `CompareAndDelete`
  releases one, and `CompareAndSwap` moves one from a value the caller
  observed to a new one, refusing if the record moved in between. A plain put
  over a claim discards whatever landed since the read, which is the same
  lost-update the guarded create exists to prevent, one operation later.
  An absent key is NOT created by a swap — a swap edits what is there, and
  creating on absence would let a released claim be resurrected by a caller
  holding a stale view of it.
- **Absence is `(false, nil)` for every guarded verb**, never an error. A
  backend that reports a missing key as a failure makes callers unable to
  distinguish "nothing was there" from "the store is broken", and the natural
  retry loop around a guarded verb spins on an error that will never clear.
  This holds through the encrypted wrappers too: `seal.StorageBarrier` maps
  its backend's `ErrSecretNotFound` to `(false, nil)` for both the swap and
  the delete.
- **A claim succeeds only for the call that created it.** A pre-existing entry
  is refused whatever tenant holds it, the claimant's own included: proceeding
  on a claim you did not win lets a concurrent same-tenant delete release it
  under you, leaving a record no by-id operation can reach. The cost is that a
  create dying mid-way burns its id, which is the safe direction.
- **An empty namespace prefix is an error, not a pass-through.** It would hand
  the caller the entire store root, which is the unconfined access the wrapper
  exists to prevent, delivered silently.
- **Index results crossing a namespace are dropped, not stripped.** A key
  outside the namespace has no business in its result; passing it through
  stripped would hand the caller another tenant's record.
- **A store is told which keys an index covers** (`EntityIndex.KeyPrefix`)
  rather than inferring `{entityType}/`. The inference holds only while the
  entity type is the leading key segment, which namespacing breaks — and a
  store that infers wrongly indexes nothing while reporting success.
- **Shared-prefix tenants have no engine-backed indexes** (#40 restores them).
  The engine matches writes to indexes by the first path segment of the key,
  which a namespace displaces. The store refuses index operations with
  `kvstore.ErrIndexUnsupported`; `pkg/dao` plans a full scan, so results are
  correct and only slower. Isolation was chosen over the index.



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
