# Multi-Tenant Isolation, Quota & Entitlement System — Design

> **Status: ARCHIVE — historical record. Do NOT build from this document.**
> Last verified: 2026-10-13 · superseded by the `docs/multi-tenancy/` feature folder, which is canonical.

Status: **proposed** (awaiting maintainer sign-off on the decisions in Part 5).
Foundation for a service-based offering on top of qrdb.

## Goal (maintainer)

A tenant should be able to have their own isolated **data directory + database**,
not just a shared key prefix in the shared (`system`) database. The shared
space with key-prefixing is the default; a tenant may instead choose a dedicated
FS location (from a volume pool) where **all** their data is stored. Under raft
clustering, such a tenant needs their **own dedicated raft group**. A full
**quota + permission/entitlement** system governs what each tenant may configure
(may they create raft groups at all? only in shared space, or on a dedicated
volume? how many databases? max database size? which isolation modes?) and is
the foundation for tiered service plans.

## Already built (verified — build on these)

- **Quotas** (`pkg/quota/`): `Quotas{MaxDatabases, MaxRaftGroups, MaxStorageBytes, …}`
  + `Current*` counters; `quota.Service` implements `quota.Checker`; `TenantService`
  implements `quota.QuotaProvider` (`IncrementQuotaUsage`/`DecrementQuotaUsage`) and is
  already wired into `DatabaseService.CreateForTenant` (checks `ResourceDatabases`).
- **Tenant isolation** (`pkg/tenant/`): `IsolationPrefix` (shared store + key prefix),
  `IsolationBarrier` (dedicated Pebble for IAM from the volume pool, per-tenant DEK).
  `TenantInfo{IsolationType, Quotas}`; `StoreResolver` routes IAM per mode.
- **Volume pool** (`pkg/storage/pool/`): allocates a contained FS path per tenant
  (used today for the barrier IAM Pebble via `seal.BarrierRegistry.allocatePooledBackend`).
- **Database manager** (`pkg/database/`): `Create` honors `cfg.DataDir`; `BackendRaft`
  is **stubbed** (`factory.go:~137`); `shard.Allocator` wired via `WithAllocator`.
  *(Superseded: the per-node `shard.Allocator` and `WithAllocator` are gone.
  Every raft database claims its shard ID from one replicated high-water mark,
  `shard.Claimer`, set with `Manager.SetShardIDClaimer`; see
  [../databases/engineering.md](../databases/engineering.md#cluster-wide-shard-ids).)*
- **Multi-DB data plane** (just landed): `{tenant}/{db}/` key-namespacing across all
  5 transports + Go/TS SDKs; resolver `pkg/server/rest_registry.go`.
- **Permissions** (`pkg/permission/`): databases CRUD perms exist; none yet for
  raft-group / dedicated-volume / isolation-mode provisioning.

**Missing:** data-plane routing to per-tenant backends; `BackendRaft` impl;
enforcement of `MaxRaftGroups` / `MaxStorageBytes`; entitlement gating; new
isolation modes; new permissions.

## Isolation capability ladder

```
IsolationPrefix  — shared Raft, shared Pebble, logical key prefix (DEFAULT)
IsolationBarrier — shared Raft, dedicated Pebble for IAM only (data still shared)
IsolationVolume  — dedicated Pebble per database under tenant's own FS root (NEW)
IsolationRaft    — dedicated Raft group per database, own FS root, replicated (NEW)
```

The quota+entitlement system governs which rung a tenant's plan permits and how
much they may consume.

## Three governance planes (keep separate)

| Plane | Controls | Enforced at | State |
|---|---|---|---|
| RBAC permissions | which ops a principal may call | transport middleware | complete |
| Numeric quotas | how many of a resource + system-barrier byte budget | service layer (pre-delegate) | complete |
| Feature entitlements | which isolation tiers + seal strategies a plan permits | service layer (pre-quota) | complete |

RBAC (`databases:create`) and entitlements (plan allows dedicated raft?) are
orthogonal — both must pass. Do **not** merge entitlements into RBAC roles.

### Quota fields (numeric caps)

| Field | Caps | Used for |
|-------|------|----------|
| `MaxDatabases`, `MaxRaftGroups`, `MaxReplicasPerGroup`, `MaxTotalReplicas`, `MaxStorageBytes`, `MaxDatabaseSizeBytes` | Resource limits | Controlling tenant provisioning |
| **`SystemBarrierQuota`** | Bytes, gated (0=denied, -1=unlimited, >0=capped) | Controlling isolated tenants' use of the shared system barrier for encryption |

### Entitlement fields (feature flags)

| Field | Controls | Semantics |
|-------|----------|-----------|
| `AllowedIsolationModes` | Which isolation rungs a tenant may use | Empty list = default + others must be explicitly listed |
| `AllowDedicatedRaft`, `AllowVolumeStorage`, `AllowSystemManagedBarrier` | Which features are available | Boolean: true = enabled, false = disabled |
| **`AllowedBarrierModes`** | Which sealing strategies a tenant's dedicated barrier may use | Empty list = built-in software (none/passphrase/shamir) only; non-empty = exact allowlist; hardware strategies fail-closed by default |

## Phases

### Phase 1 — Quota enforcement completion + entitlements (no new isolation modes)
Close the gap where `MaxRaftGroups`/`MaxStorageBytes` exist but are unenforced;
add the `Entitlements` struct + `EntitlementChecker`; add 4 permissions
(`tenants:set_isolation`, `tenants:set_entitlements`, `databases:create_raft`,
`databases:create_volume`); `UpdateEntitlements` API on all transports;
config-driven default quotas/entitlements. Lowest risk, foundational. No deps.

### Phase 2 — IsolationVolume + cluster-wide pool — COMPLETE 2026-06-17

**IsolationVolume:** Per-tenant dedicated filesystem volume with full Raft engines (KV,
MVCC, lease, watch, timeseries via per-tenant local Raft host). Each tenant gets a
`DataRoot` allocated from the volume pool; `CreateForTenant` routes all `DataDir` and
WAL under the tenant's `DataRoot`. Per-tenant `(tenant,db)→DatabaseServices` cache
(`pkg/server/tenant_db_registry.go`) resolver routes IsolationVolume tenants there;
boot rehydrates per tenant. Layout: `{poolRoot}/{tenantID}/{barrier,databases,raft}`.

**Cluster-wide pool:** The pool DEFINITION (name, root, capacity) + tenant→slot
allocation are replicated cluster metadata in the system shard (same substrate the
membership and IAM stores use). This ensures all nodes converge on the same pool
inventory and tenant slot assignments. PER-NODE physical storage is independent —
each node has the same pool mount path but stores volume tenant data locally (no
shared physical storage). See pkg/storage/pool + pkg/placement (pool + tenant binding).

Deps: Phase 1 + configured volume pool.

### Phase 3 — Byte-level quota usage tracking
Write-path counter in `BackendKVService.Put/Delete` (check `MaxStorageBytes`) +
background reconciler (`pkg/quota/reconciler.go`) that walks `DataRoot` to correct
drift. Deps: Phase 2.

### Phase 4 — IsolationRaft (dedicated raft group per database) — HIGHEST RISK
`RaftBackendFactory` + `ShardController{StartShard,StopShard}` (may need a small
go-quicraft change to surface shard lifecycle on the host); un-stub `BackendRaft`;
per-tenant `WatchHub`/`Lessor`; raft data under `{DataRoot}/databases/{db}/raft/`;
boot starts tenant shards before serving; enforce `MaxRaftGroups`.
**MVP:** single-node dedicated raft (static peer list for multi-node; auto-placement
and membership-change handling are post-MVP). Deps: Phases 1–3 + go-quicraft host
investigation.

## Decisions — RESOLVED by maintainer (2026-06-16)

1. **IsolationVolume = FULL engines via a per-tenant local Raft cluster** (NOT KV-only). Dedicated-volume databases get MVCC/lease/watch/timeseries; this pulls the per-tenant raft state-machine work into Phase 2. IsolationVolume = local raft on the dedicated volume; IsolationRaft = dedicated multi-node replicated raft group.
2. Entitlements inline in TenantInfo (recommended default; refactor to plan/tiers later if the business model needs it).
3. **FS path: volume-pool only** — DataRoot always allocated from the configured pool (contained, no operator-arbitrary path).
4. **Dedicated-raft = multi-node auto-placement** from the start (node topology, load-balanced shard placement).
5. Implied by #4: **rebalance-aware** — tenant raft groups participate in shard placement/rebalance (not merely operator-managed).
6. Quota byte counter: in-memory accumulator + periodic flush + reconciler (recommended default).
7. IsolationBarrier and IsolationVolume stay separate modes (no break to existing IsolationBarrier).

NOTE: #1 and #4 make the raft subsystem central and large. Per-tenant raft groups +
multi-node auto-placement + rebalance depend on go-quicraft shard-lifecycle control
(`ShardController.StartShard/StopShard`) which must be investigated before Phases 2/4.
Build order: Phase 1 first (no raft dependency), then a go-quicraft raft-capability
investigation, then re-plan Phases 2–4 around confirmed shard control.

## Phase 4c (multi-node dedicated raft) — COMPLETE 2026-06-17 (all sub-stages shipped)

Dedicated tenant raft groups replicate across cluster nodes. Decisions:
- **Placement strategy:** load-aware planner with a bounded-load target (NOT consistent
  hashing). Explicit, persisted, constraint-aware: pick RF distinct least-loaded healthy
  nodes (anti-affinity = one voting replica/node), keep every node ≤ (1+ε)·avg, minimize
  group moves on topology change. Extends the existing `RebalanceService` CV load scoring.
- **Replica quota (BOTH):** `MaxRaftGroups` (exists) + new `MaxReplicasPerGroup` (per-group
  RF cap) + new `MaxTotalReplicas` (aggregate budget across all the tenant's groups, the
  capacity/billing lever) + `CurrentTotalReplicas` counter. Tenant chooses `replicas` (RF)
  at CreateDatabase, validated ≤ per-group cap, total+new ≤ aggregate budget, ≤ cluster size.
- **Tenant DEK cluster-availability = per-node re-wrap via secure key transport** (per-node
  system-KEK independence preserved — NO shared cluster KEK). Placement leader holds the
  tenant root key and securely transports it over the authenticated mTLS control plane to
  each target node (the unwired Shamir share-transport skeleton this once pointed at
  has been removed); each
  node persists its OWN local copy of the tenant barrier. Protection strategy per node
  depends on the tenant's BarrierAutoUnseal choice (both modes supported multi-node):
  - **system-managed:** re-wrapped under THAT node's system barrier → auto-resolves at boot
    (replica auto-starts), "like the system key" (which is itself per-node-independent).
  - **self-managed:** protected under the tenant's own strategy per node → after a restart
    the tenant must unseal on EACH replica node; that node's replica stays sealed/not-started
    until unsealed (high security, unattended-restart cost).
  On replica add (placement/rebalance), the key is transported to the new node then.
- **Coordination = replicated placement table** in system KV (`__sys:placement/{tenant}/{db}`
  → {shardID, []replica{replicaID,node,DataRoot}}) + a per-node `TenantShardReconciler`
  watching it (reuse the `__sys:` lease+watch substrate `MembershipManager` uses); leader
  drives `RequestAddNode`/`RemoveNode`; each node self-starts/stops its assigned replicas.
  No bespoke push-RPC. Route A (SM data under DataRoot via cfg.DataDir, raft log shared
  host LogDB — no go-quicraft change).
- **Build sub-stages:** 4c-1a quota+replicas field; 4c-1b placement table+scheduler;
  4c-1c secure tenant-key transport + per-node re-wrap (security-critical); 4c-1d per-node
  reconciler + multi-member StartShard + boot reattach + drop teardown + multi-node test.
  4c-2: complete the rebalance move primitive + node join/leave reconciliation.

#### 4c-1b — IMPLEMENTED (placement decision + persistence)

`pkg/placement/` is the home of the replicated placement table and the load-aware
scheduler:

- `table.go` — `Placement{TenantID, Database, ShardID, RF, LeaderReplicaID,
  Replicas[]}` + `Replica{ReplicaID, NodeID, RaftAddr, DataRoot}` (JSON), with
  `Leader()`, `ReplicaForNode()`, `Members()` helpers.
- `store.go` — `Store` over the replicated system KV under
  `__sys:placement/{tenantID}/{database}` (same substrate the membership and IAM
  stores use). `Put`/`Get`/`Delete`/`List`/`ListForTenant`/`Watch`. Watch is
  served only when the KV implements the optional `Watcher` seam (wired by 4c-1d);
  otherwise it returns `ErrWatchUnsupported`.
- `scheduler.go` — `Scheduler.Place(ctx, PlaceRequest)` picks RF distinct, healthy,
  least-loaded nodes (anti-affinity = one voting replica/node), partitioned at the
  `(1+ε)·avg` bytes ceiling so a hot node is never chosen while a colder distinct
  node remains; deterministic tie-break on (Bytes, ShardCount, NodeID); leader =
  least-loaded selected (replica ID 1). Returns `*InsufficientNodesError`
  (matching `ErrInsufficientNodes`) when healthy distinct nodes < RF.
- `TopologyProvider` is the pure seam (`Nodes() []NodeLoad`). The production
  implementation is `service.HostTopologyProvider` over `host.GetNodeHostInfo`
  (roster + shard counts) and `RebalanceService.GetImbalanceReport` (byte/shard
  load — reuses, not re-derives, the CV scoring).

Wiring: `DatabaseService.CreateForTenant` clamps RF to `min(requested,
clusterSize)`, then (raft path) computes + persists the `Placement` after shard-ID
allocation; a placement failure rolls the create back (drop + quota release).
`DropForTenant` deletes the entry. `RaftBackendFactory` reads the table as the
source of truth (`WithPlacementInfoSource`): a placement entry assigning a replica
to the local node supplies the shard ID + DataRoot; otherwise it falls back to the
meta.json-derived source so Phase-4b single-node databases are unchanged. This
stage does NOT start shards across nodes (4c-1d) or transport keys (4c-1c).

## Phase 4c-1c secure tenant-key transport — DECISIONS RESOLVED 2026-06-17 (build in progress)

Security-critical: distributes a dedicated tenant's raft ROOT KEY to the nodes hosting its
replicas. Findings: no concrete cross-node key transport exists today (the Shamir
share-transport skeleton in `pkg/seal` was dormant, never wired, and has been removed). Node mTLS exists (go-quicraft QUIC raft
transport `RequireAndVerifyClientCert`+revocation; qrdb admin-HTTPS leader-forward, but that
client presents NO client cert). `EpochBarrier.Initialize` generates its own root key — no
import-supplied-key API; but a qrdb-side import works via `SystemBarrierStrategy.SealRootKey(k)`
→ set Epoch=1/fresh Salt/KnownEpochs → `PutIfAbsent(RootKeyPath)` → `Unseal()` (NO go-quicraft
change required). Leader gets plaintext via `StorageBarrier.GetMasterKey()`.

DESIGN: new `pkg/keydelivery/` + a new mutually-authenticated per-node internal endpoint
(recommend a route family on the existing admin-HTTPS server + a generalized `NodeAdminResolver`
+ an inter-node client that PRESENTS the node cert). Envelope {tenant,db,shard,replica,
targetNodeID,mode,payload,nonce,issuedAt}. System-managed payload = raw root key (mTLS +
recommend HPKE-sealed to target node cert pubkey); self-managed payload = the already-sealed
PORTABLE blob (NO plaintext transit). Authz: mTLS-to-cluster-CA proves "a node"; receiver checks
the replicated placement table `ReplicaForNode(self)` proves "the RIGHT node". Replay: nonce+TTL+
idempotent PutIfAbsent. Re-wrap: new `StorageBarrier.ImportRootKey` + `barrier_registry.ImportTenant`/
`RegisterVolumeTenantWithRootKey`. Pull-backstop via the 4c-1d reconciler (placement.Store.Watch
EventPut → if assigned-local-replica-with-no-key, pull from leader → start). enclave.WipeBytes +
metadata-only audit (no key bytes in logs).

DECISIONS RESOLVED 2026-06-17 (maintainer):
- **(h1) Channel = dedicated cluster-peer control plane, NOT user RBAC.** The cluster
  authenticates as a NODE (node cert chaining to the cluster CA — the QUIC raft transport's
  trust root), never by being granted the `admin` user role (that would conflate machine
  identity with top user privilege = escalation risk). A new route family is mounted behind
  a DISTINCT node-cert auth middleware (bypasses the user-RBAC chain). Authz = two gates:
  (1) cert chains to cluster CA → "is a cluster peer"; (2) replicated placement table
  `ReplicaForNode(receiver)` hosts THIS tenant's replica → "is the RIGHT node". Gate (2) is
  the tenant-boundary enforcement (placement table is consensus-replicated authority → cross-
  tenant delivery structurally impossible; no admin scope to leak). Reuse the existing HTTPS
  listener IFF its client-auth mode supports it; else a dedicated cluster listener (decide
  from wiring investigation).
- **(h2) HPKE recipient-binding** (seal root key to target node cert pubkey, on top of mTLS).
- **(h3) self-managed = ship the portable SEALED blob** (no plaintext transit).
- **(h4) SPIFFE-SAN==NodeID channel binding WHEN inter-node mTLS is present; graceful fallback
  when not.** HPKE recipient-binding makes payload confidentiality independent of the channel,
  so the feature does NOT hard-require mTLS: with mTLS off (dev/debug), the SPIFFE check is
  skipped and authz falls back to HPKE-recipient-binding (cryptographic node-identity proof —
  only the right node can decrypt) + placement gate, with a logged SECURITY-DOWNGRADE warning.
  mTLS stays the production default/strongest mode.
- **(h5) CORRECTED 2026-06-17 after investigation — keep `placement.Replica.NodeID` as STRING.**
  There is NO uint64 NodeID anywhere: canonical cluster-node identity is `host.id` (string hex,
  go-quicraft host.go:1535); `membership` keys by `ReplicaID` (uint64) which is a DIFFERENT
  concept (per-group raft replica index, not node identity); `placement.Replica.NodeID` is
  already `string` and matches `host.id`. Converting to uint64 would wrongly conflate NodeID
  with ReplicaID. NO change — keep string, aligned with `host.id`.
- **(h6) Skip optional go-quicraft `Barrier.ImportRootKey`** — qrdb-side
  `SystemBarrierStrategy.SealRootKey` import path already works (no fork).

### 4c-1c implementation plan (ground-truthed 2026-06-17)
- **Crypto (stdlib only, NO new dep, NO fork):** all certs are ECDSA **P-256**
  (cmd/qrdbd/tls.go generateCA/Node/Client). `crypto/hpke` (Go 1.26, RFC 9180) provides
  `DHKEM(ecdh.P256())` + `NewDHKEMPublicKey`/`Seal`/`Open`. Convert the recipient node's
  ECDSA cert key to ECDH via `(*ecdsa.PublicKey).ECDH()` → HPKE-seal the payload directly to
  the node's existing cert key. HPKE `info` = context binding {tenant,db,shard,replica,
  targetNodeID} (recipient + context bound cryptographically).
- **Channel:** mount cluster-peer routes on the EXISTING admin-HTTPS listener (no new port).
  ClientAuth is `VerifyClientCertIfGiven` when auth on (start.go:1397) → an inter-node client
  presenting a cluster-CA node cert is verified at the TLS layer against `ClientCAs`. Add the
  cluster routes to the auth-bypass allowlist (same precedent as the sealed-window barrier
  routes) and gate them with a DEDICATED node-cert middleware (NOT user RBAC): extract
  `r.TLS.PeerCertificates[0]` → verify cluster-CA chain → identity. When auth off
  (`NoClientCert`, dev) there is no peer cert → HPKE-recipient-binding + placement gate carry
  authz, with a logged downgrade warning.
- **NodeID representation (resolved 2026-06-17):** the placement table keys on the node's
  RAFT ADDRESS string (`HostTopologyProvider.Nodes()` sets NodeID=addr, placement_topology.go:73),
  NOT go-quicraft host.id. So the receiver's `selfNodeID` = `f.raftAddress`, the placement gate
  `ReplicaForNode(raftAddr)`, and the cert SAN value is the raft address. (host.id is go-quicraft-
  internal; the authz-relevant identity is the raft address the placement layer already uses.)
- **h4 SAN:** add a URI SAN `spiffe://qrdb/node/{raftAddress}` to generateNodeCert (ADDITIVE;
  existing certs without it fall back). Reuse/extend `pkg/auth/spiffe` (the SPIFFE source of
  truth) with a node-identity form alongside the existing tenant-workload form.
- **Authz gates:** (1) cert chains to cluster CA; (2) SPIFFE-SAN node == placement
  `ReplicaForNode(self).NodeID` when SAN present (else skip → fallback); (3) replicated
  placement table assigns a replica of THIS tenant/db to the receiver (tenant boundary).
- **Import seam:** new `BarrierRegistry.ImportTenant(tenantID, payload, mode)` +
  `StorageBarrier.ImportRootKey`. CORRECTNESS REFINEMENT (2026-06-17): the transported
  system-managed plaintext is NOT the bare root key but the full DEK-derivation context
  `{RootKey, Salt, Epoch, KnownEpochs}` (leader exports via `EpochBarrier.SealedRootKeyData()`).
  Rationale: DEKs = HKDF(rootKey, salt, epoch); replicas must derive IDENTICAL DEKs so raft
  snapshot transfer between nodes is correct regardless of whether snapshots ship logical
  state or encrypted bytes. Salt is non-secret (strategy.go:114) → transporting it is free and
  eliminates the risk. Recipe: `strategy.SealRootKey(rootKey)` → {Strategy,Ciphertext}; set
  the SHARED Epoch/Salt/KnownEpochs (matching NoneStrategy marker shape: Epoch=1, 32B Salt,
  KnownEpochs=[1] for a fresh import) → `PutIfAbsent(RootKeyPath)` (idempotent) → `Unseal`
  (reconstructs in-process via go-quicraft `EpochBarrier.SetSealedRootKey`). Each node re-wraps
  under ITS OWN system barrier (node-local Ciphertext) but keeps the shared derivation context.
  Self-managed: HPKE-open → persist the portable sealed blob verbatim (no plaintext, already
  carries its own Salt/Epoch). keydelivery is payload-agnostic (seals arbitrary bytes) so this
  refinement does NOT change the crypto core.
- **Envelope/replay:** nonce + epoch/issuedAt TTL + handler dispatch map.
- **Pull-backstop:** 4c-1d reconciler watches `placement.Store.Watch` (EventPut) → if assigned
  a local replica with no key, pull from leader. (Watch returns ErrWatchUnsupported on backends
  without the Watcher seam → 4c-1d wires it / polling fallback.)
- enclave.WipeBytes on all transient key material; metadata-only audit (no key bytes in logs).

### 4c-1c CORE COMPLETE + security-reviewed 2026-06-17
Built+verified (all green, race-clean): pkg/keydelivery 95.9%, pkg/auth/spiffe 100%,
pkg/seal import seam 91.1%, pkg/clusterkeys 94.4%, pkg/auth/middleware exempt. Security review
(security-engineer) found HIGH-1 (sender sealed to UNVERIFIED recipient pubkey → MITM root-key
disclosure under no-mTLS) + 3 MEDIUM + LOW; ALL FIXED + regression-tested:
- HIGH-1: Sender now authenticates the fetched recipient cert (chain→cluster-CA +
  spiffe node-SAN==target NodeID, fail-closed). NewSender(resolver, client, caPool,
  pinnedTrustDomain, logger). 3 attack-path regression tests (MITM self-signed, wrong-SAN,
  no-SAN) assert zero POSTs. ⇒ the cert node SAN is now MANDATORY on the sender path.
- MED-1 wipe decoded RootKey copy; MED-2 64KiB MaxBytesReader on delivery body; MED-3
  replay-nonce recorded only after successful Open; LOW-1 collapsed Open-error status oracle.
Verified-correct (no finding): receiver placement tenant-boundary gate, HPKE context-binding,
P-256 pin, local re-wrap (no shared cluster KEK), idempotency, freshness/replay window.

### 4c-1d per-node reconciler + 4c-2 rebalance — IMPLEMENTED

**4c-1d TenantShardReconciler** (pkg/server/tenant_shard_reconciler.go) converges each
node's replicas to the replicated placement table:

- Boot re-attach: ReconcileOnce runs at daemon startup before traffic, re-attaching
  shards in meta.json.
- Watch-based convergence: When placement.Store.Watch succeeds, EventPut triggers
  shard start, EventDelete closes demoted shards.
- Poll fallback: When Watch returns ErrWatchUnsupported, polls (default 5s).
- Barrier-aware deference: Shards with absent/sealed barriers are silently deferred
  to the next tick; key delivery and later ticks converge.

**4c-2 rebalance move** (pkg/service/rebalance_service.go ExecuteMove) fully supports
dedicated-raft groups: distributes the tenant key to the destination, waits for
reconciler start, transfers leadership if needed, and removes the source.

**4c daemon wiring** — shipped and tested:

- Trust domain: configured in `cluster.trust_domain` (YAML, default "qrdb.cluster");
  used for SPIFFE SAN generation and key-delivery pin validation.
- Node certificates: generateNodeCert includes SPIFFE URI SAN
  `spiffe://{trustDomain}/node/{raftAddr}` (mandatory for 4c-1c).
- Receiver wiring: WithClusterKeyReceiver ServerOption mounts the key-delivery
  handler on the REST mux; gate checks cert chain + replicated placement table.
- Sender wiring: Sender initialized in start.go; hooked into CreateForTenant to
  distribute the tenant key to each non-local replica immediately after placement.
- Multi-node test: test/integration/isolation_raft_cluster_test.go::TestIsolationRaft_EndToEnd
  proves end-to-end across 3 real nodes with SPIFFE-SAN certs. Verified: placement
  scheduling, key distribution, reconciler-driven start, restart survival.

## Phase 4d spike result (2026-06-17) — per-tenant WAL: held for decision
KEY REFRAME: per-tenant-key WAL ALREADY ships via IsolationVolume (per-tenant host = own WAL+barrier, tenant_host.go:90-92). 4d's real scope = per-tenant-key WAL for IsolationRaft groups on the SHARED host. Routing IS small: a qrdb `multiplexLogDB` (shared waldb + map[shardID]dedicated waldb, dispatch by shardID, split mixed SaveState batches) injected via go-quicraft's EXISTING public `WithLogDB` (host.go:138) — NO go-quicraft routing change; per-instance `waldb.Open(dir, WithBarrier(tenantEpochBarrier))` works. HARD PART: go-quicraft assumes ONE barrier per host for snapshot epoch tracking/purge (SetEpochTracker host.go:1604, PurgeEpochsBefore snapshot_pool.go:1198, MinLiveEpoch) — per-tenant WAL barriers break the single-floor model → ~2-3wk go-quicraft snapshot-pool rearchitecture for full correctness (vs ~3-5d routing-only). Other risk: qrdb-built shared WAL loses host buildWALOptions disk-full/HealthCheck wiring (needs a tiny go-quicraft builder seam). dragonboat precedent: Expert.LogDBFactory + 16-way sharded logdb for I/O concurrency, but NO per-group KEY isolation (novel either way).
4d DECISIONS (1-4): (1) shared-host dedicated WAL vs route dedicated-WAL IsolationRaft tenants onto the existing per-tenant host (clean, no go-quicraft change, forfeits shared-host multi-node for those)? (2) epoch-pinned dedicated WALs (no rotation/purge) interim vs fund the go-quicraft per-barrier snapshot rework now? (3) go-quicraft seam (WAL builder helper) vs qrdb-duplicate buildWALOptions vs none? (4) accept non-atomic cross-WAL SaveState batches (per-shard durability only)? RECOMMENDATION: since IsolationVolume already gives per-tenant-key WAL, prefer (1)-route-to-per-tenant-host OR (2)-epoch-pinned-interim; defer full rework until 4c multi-node lands. **DECIDED 2026-06-17 (maintainer): Option C — FULL go-quicraft per-barrier snapshot-pool rework (~2-3wk).** Full correctness: per-tenant-KEY WAL on the shared host + multi-node + WAL rotation/purge. This is a cross-repo (go-quicraft) effort: rearchitect the single-barrier-per-host snapshot epoch model (SetEpochTracker host.go:1604, PurgeEpochsBefore snapshot_pool.go:1198, MinLiveEpoch) to per-barrier/per-shard epoch tracking, then qrdb multiplexLogDB (per-shard waldb with WithBarrier(tenantEpochBarrier)) via WithLogDB + quota-gated opt-in + shared-WAL fallback. SEQUENCED AFTER 4c is solid (4c-1d C1/H1 fixes + 4c-2). Will need its own design+spike+security pass in go-quicraft.

## Raft-log encryption & per-tenant WAL — DECIDED 2026-06-16

FINDING (verified): qrdb encrypts only at the SM-storage layer (Pebble via encfs under
the barrier). The system `Host` is built WITHOUT `quicraft.WithBarrier`, so the shared
raft log/WAL is **plaintext at rest** — committed KV entries sit in cleartext until log
compaction. Baseline encryption-at-rest gap (NIST SC-28 / PCI Req 3 / HIPAA) independent
of multi-tenancy. (The Phase-2 IsolationVolume per-tenant Host DOES barrier-wrap its WAL.)

DECISIONS:
- **Encrypt the shared system Host WAL now** under the system barrier (pass
  `quicraft.WithBarrier(systemBarrier)` to NewHost). Verify boot ordering: the system
  barrier must be unsealed before the LogDB opens (it already is — daemon unseals before
  building the host). Closes the baseline at-rest gap for the shared shard + Phase-4b
  raft-group log entries. [Immediate task]
- **Per-tenant WAL = configurable, quota-gated opt-in with shared-WAL fallback.** A tenant
  may opt a raft group into a DEDICATED per-tenant WAL/LogDB rooted under its DataRoot and
  barrier-wrapped under the tenant DEK (full per-tenant crypto-isolation incl. the log +
  clean crypto-shred + replication). Governed by a new quota (e.g. MaxDedicatedWALGroups
  or a flag within the replica quota); when the quota is exhausted or the tenant opts out,
  the group uses the system-key SHARED WAL (Route A). This APPROVES Route B (the substantial
  go-quicraft per-shard-LogDB change) as opt-in. Sub-phase 4d: (4d-a) go-quicraft per-shard
  LogDB/WAL primitive [spike first — high risk, shared waldb at host.go:1527]; (4d-b) qrdb
  dedicated-WAL opt-in + quota + shared-WAL fallback, wired into RaftBackendFactory StartShard.

## Tenant barrier key management — DECIDED 2026-06-16 (Phase 2 blocker resolution)

A dedicated-isolation tenant's barrier (IsolationBarrier / IsolationVolume) supports
TWO key-management modes, chosen per tenant:

- **Self-managed (default, secure):** `CreateTenant` does NOT auto-init the tenant
  barrier (matches the `bootstrap.go` note — system admin holds no tenant key). The
  tenant admin initializes + unseals their own barrier; after a daemon restart the
  barrier comes up SEALED and the tenant must re-unseal before their data is
  reachable. The per-tenant `Host` builds lazily only after unseal.
- **System-managed auto-unseal (opt-in, low-security convenience):** the tenant root
  key is wrapped (AEAD) under the SYSTEM barrier's master key (the locked Single
  KEK/DEK custody model — `[[single_kek_dek_encryption_model]]`). Enables auto-init
  at `CreateTenant` and credential-free re-attach at boot (the system barrier is
  already unsealed at boot, so it can unwrap the tenant root key). Implemented as a
  qrdb-application seal strategy that composes the system barrier as the unseal
  authority (lives in qrdb `pkg/seal`, registered in the StrategyRegistry).

Per-tenant selector: `TenantInfo.BarrierAutoUnseal bool` (default false = self-managed).
Entitlement-gated: a plan may forbid system-managed (force self-managed) for security.
This fixes the Phase-2 live blocker (empty-credential init rejected by the deferred
passphrase strategy) and applies to IsolationBarrier too.

## Raft feasibility (investigated 2026-06-16)

go-quicraft `Host.StartShard(members, join, createFn, cfg)` / `StopShard(shardID)`
(`pkg/host.go:1700/2045`) are RUNTIME ops (qrdb already calls them via
`AdminService.StartShard`). Membership (`RequestAddNode`/`RemoveNode`/
`RequestLeaderTransfer`), per-shard snapshots (`NewHostShardSnapshotProvider`),
and per-shard `WatchHub`/`Lessor` (via `store.WithLessor`/`WithWatchHub` closures
in a per-shard `CreateFunc`) are all available with **no go-quicraft change**.

**Critical constraint:** a single `Host` pins ALL shards' SM data + the shared
WAL/LogDB under one host-wide `NodeHostDir`/`WALDir`. There is no per-shard dir
override. Therefore:

- **Phase 2 (IsolationVolume — local raft on dedicated volume): SOLVED, no quicraft change.**
  Use a SEPARATE per-tenant `Host` built with `quicraft.NewHost(cfg, WithoutTransport())`
  and its own `NodeHostDir`/`WALDir` under the tenant's pool `DataRoot`, then
  `StartShard(ShardID=1, ReplicaID=1, join=false, perShardCreateFunc)`. Gives a
  durable, FS-isolated, full-engine single-node raft cluster. (`NewHost` has no
  process-global singletons; `WithoutTransport` removes the only real constraint, the port.)
- **Phase 4 (IsolationRaft — multi-node, own FS root): FORK needs sign-off.**
  - Route A (no quicraft change): multi-node tenant shards on the SHARED cluster
    host; raft data lives under the shared host dir, NOT the tenant `DataRoot`
    (does not satisfy "own FS root").
  - Route B (quicraft change): add per-shard data/snapshot dir override
    (`config.Config.DataDir` / a `HostConfig` hook at `host.go:1824`) — and the
    HARD part, per-shard WAL (single shared `waldb` at `host.go:1527`) = high risk.

Other Phase-4 work (qrdb-side): new `RaftBackendFactory` (un-stub `BackendRaft`,
inject host+allocator+encryptor+registry), a placement scheduler (greenfield,
over `host.GetNodeHostInfo()` + discovery roster), and COMPLETING the shard-move
primitive in `pkg/service/rebalance_service.go` (today `ExecuteRebalance` only
adds a replica — a real move needs add→catch-up→leader-transfer→remove+RemoveData).

## File map

| Phase | Files |
|---|---|
| 1 | `pkg/quota/{checker,types}.go`, `pkg/tenant/service.go`, `pkg/service/database_service.go`, `pkg/permission/constants.go`, `pkg/auth/middleware/mapping.go`, transport handlers |
| 2 | `pkg/tenant/service.go`, `pkg/database/manager_tenant.go`, `pkg/database/backend_kv_service.go` (new), `pkg/server/tenant_db_registry.go` (new), `pkg/server/rest_registry.go`, `cmd/qrdbd/start.go` |
| 3 | `pkg/database/backend_kv_service.go`, `pkg/quota/reconciler.go` (new), `cmd/qrdbd/start.go` |
| 4 | `pkg/database/backend_raft_factory.go` (new), `pkg/database/factory.go`, `pkg/server/lifecycle/lifecycle.go`, `pkg/server/rest_registry.go`, `cmd/qrdbd/start.go`, possibly go-quicraft |
