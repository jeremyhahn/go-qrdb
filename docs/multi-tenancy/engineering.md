# Multi-Tenancy — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-02

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
- **Every volume-pool path is validated before it is created.** A pool's paths
  reach `os.MkdirAll`, so `pool.BuildLocalCandidates` requires each one to be
  non-empty, absolute, canonical (`filepath.Clean(p) == p`), free of any
  symlinked component, and non-overlapping with the other paths in the same
  request. Failures are `*VolumePoolPathError`, wrapping either a
  `*validation.PathError` naming the predicate or a `*VolumePoolOverlapError`
  naming the volume it collides with.

  The symlink predicate is not redundant with the canonical one: `filepath.Clean`
  reasons about a path lexically while the syscall follows it physically, so a
  perfectly canonical `/mnt/disk-a/vol` can still be a symlink elsewhere. The
  overlap predicate is separator-aware — `/data/foo2` is **not** inside
  `/data/foo` — because two volumes where one contains the other are one volume
  counted twice, and a placement strategy would report free space that does not
  exist.

  The five path predicates are shared with tenant `DataRoot` validation through
  `pkg/validation`; neither package carries its own copy.
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
- **An absent tenant is FOLDED at the boundary, never inferred at the
  predicate.** `tenant.IsSystemTenant` answers true for both `SystemTenantID`
  and `""`, because the unscoped keyspace and the system keyspace are the same
  keyspace — `StoreResolver`, `IdentityIndex`, `kv_service` and
  `database.manager_tenant` all ask it that question and all depend on the
  empty branch. That branch is safe only under a precondition: on an
  AUTHORIZATION path the predicate must never be handed `""`. `tenant.Normalize`
  is the single fold that establishes it, and every boundary that binds a
  tenant calls it — `FromContext` (context), `auth/middleware.finalizeIdentity`
  (identity **and** context), `sql/provider.QRDBSession.TenantID` (SQL
  session). Open-coding `if id == "" { id = SystemTenantID }` at a new boundary
  is the defect, not the fold: before #188 `auth/middleware` folded the context
  and left `identity.TenantID` raw, and `rbac.IsSystemTenant` reads the
  identity, so the one value the authorizer consumed was the one value the
  boundary did not normalize.
- **The fold maps absence onto PRIVILEGE, and says so.** Folding `""` to
  `SystemTenantID` preserves `IsSystemTenant`'s answer exactly — it is a
  normalization, not a policy — so a principal that authenticates with no
  tenant binding is system-scoped both before and after. `pkg/auth/mtls.go`
  mints exactly that shape: a certificate that chains to the trusted CA but
  carries neither a SPIFFE tenant SAN nor a Subject OU authenticates with
  `TenantID ""` and no error. qrdb's own issuance cannot produce one
  (`pkg/cert` refuses an empty `TenantID`; `cmd/qrdbd` issues its admin cert
  with `OU=system`), so the shape arrives only from a CA-signed certificate
  minted outside qrdb's issuance path. Making absence fail CLOSED instead —
  a distinct authorization-facing predicate for which "unknown" is not
  "system" — is a deliberate change to who can authenticate, not a
  normalization, and is tracked separately (#188). The precedent for it
  already exists at one boundary: `sdk/go/transport/embedded`'s RBAC surface
  REFUSES an identity with an empty `TenantID` (`InvalidIdentityError`) rather
  than folding it.
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
| Tenant shard elects no leader in 15s | `*ShardLeaderWaitError` (wraps `ErrTenantShardNoLeader`), host build refused | **yes** |

### Why the no-leader error carries a measurement

Building an `IsolationVolume` tenant host starts a single-node Raft shard and
waits for it to elect itself leader before binding services; until it does,
every proposal fails with "not leader". That wait is bounded at 15 seconds.

When it expires the error does not merely say the shard did not elect. It
reports what the wait **observed** — how many times the shard was actually
asked, the real time spent, the longest gap between two consecutive polls, and
the last error `GetLeaderID` returned — and names one of three causes:

- **the shard could not be asked** — `GetLeaderID` kept failing, so that error
  is the finding and the timeout is a symptom;
- **the poller was starved** — some gap exceeded ten poll intervals, so the
  scheduler consumed the budget and the election never received the wall time
  the budget implies. This is host contention, not an election defect;
- **the shard genuinely did not elect** — the poller ran on time for the whole
  budget. This is a real failure.

The distinction is not cosmetic. The bare sentinel that preceded it read
identically whether the shard was broken or the machine was merely busy, and
under CI load the second is common: a 20 ms poll sleep overruns by microseconds
on an idle host, so ten consecutive missed intervals is direct evidence of
descheduling rather than of anything raft did.

**The 15 second budget is not a tuning knob.** It already dwarfs the election's
configured cost. go-quicraft derives the election timeout as
`ElectionRTT * RTTMillisecond`, and this shard no longer takes the upstream
defaults: the tenant Host is built `WithoutTransport()`, so it carries
`cluster.TransportlessRTTMillisecond` (25 ms) and
`cluster.SingleNodeElectionRTT` (5 ticks) — a **125 ms** election budget.
Until those existed a transport-less host inherited go-quicraft's WAN defaults,
10 * 200 ms = 2 s, and this wait was about seven times them. 25 ms rather than
go-quicraft's documented LAN range was chosen by measurement: `RTTMillisecond`
is also the engine tick interval, so it prices continuous CPU as well as
election latency, and 25 ms captures 95% of the achievable boot latency for 32%
of the CPU that 5 ms costs.

Widening it to quiet a red suite would remove the tests' ability to fail for the
right reason: a genuine regression that quintupled election time would still fit
inside the larger budget, leaving tests that are red when the host is busy and
green when the code regresses. If this wait times out, read the verdict in the
error before changing the number.

### The wait reads time through a seam, and production still reads the real clock

The poll loop takes its clock as a parameter (`shardLeaderClock`: `Now` and
`Sleep`, and nothing else). Production passes `realShardLeaderClock`, so every
number in the error above — elapsed, longest gap — is measured against real wall
time. That is not incidental: the starvation verdict *is* a statement about wall
time, and a wait that measured virtual time in production would report
"the shard genuinely did not elect" on a host that never let it ask.

The seam exists for the tests, and for a reason that is the same defect one
level up from the one the error type was built to fix. Those tests script the
fake shard **by poll number** — fail on poll 1, fail again on poll 3 — while the
loop stops on a **wall-clock budget**. Those are the same quantity only on an
idle host, and on a shared CI runner they diverged: a 30 ms budget at a 1 ms
interval bought **two** polls where an idle host delivers thirty-two, so poll 3
never ran and a test failed asserting on an error its fixture had never been
asked to produce. The message it was checking was *correct*; `budget 30ms, 2
polls, longest gap 36ms` is the starvation verdict, reported accurately about
the starvation that broke the test.

The fix is not a larger budget — that moves the flake to a busier host. Under a
virtual clock, time advances only when the loop sleeps and by exactly the
interval it asked for, so the poll count is arithmetic rather than a bet on the
scheduler. **Do not respond to a future red here by enlarging a budget:** under
that clock a budget cannot be missed, so a failure is a real defect.

The budget is now roughly 120x the election it waits on, so whether it should be
*tightened* is a live question — and a separate one from not widening it. The
argument above establishes only that widening destroys the tests' ability to
fail for the right reason. Tightening needs its own evidence, because what this
wait absorbs is host descheduling rather than election time, and the two do not
shrink together: the election budget fell by 16x, the scheduler's worst pause
did not.

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
