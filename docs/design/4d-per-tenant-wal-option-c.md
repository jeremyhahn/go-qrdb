# Phase 3 / 4d Option C — Per-Tenant WAL Barriers (Implementation Plan)

Status: **design complete (2026-06-17), implementation gated behind Phases 1+2.**
Decision: maintainer chose Option C (full go-quicraft per-barrier snapshot-pool rework)
for per-tenant-KEY WAL on shared-host IsolationRaft groups. Effort ~12–17 days
(7–10 go-quicraft + 5–7 go-qrdb). No go-quicraft fork — all additive public APIs.

## 1. Current single-barrier model (go-quicraft)

- `pkg/host.go:138-150` `WithBarrier(b Barrier)` stores ONE barrier per host;
  `:1442` `Host.barrier` field; `:1520-1522` default LogDB gets `WithBarrier(options.barrier)`;
  `:1604` `SetEpochTracker(options.barrier)` wires one barrier into transport for
  snapshot in-flight protection.
- `pkg/internal/engine/snapshot_pool.go:129` `snapshotPool.barrier` (single);
  `:549-550` pins in-flight epoch; `:1118-1171` `globalSafePurgeFloor()` computes ONE
  floor across ALL shards; `:1198` `barrier.PurgeEpochsBefore(purgeFloor)` once per save.
- `pkg/logdb/waldb/db.go:189` `DB.barrier` (single); `:2650-2671` `MinLiveEpoch()` global
  min across all 16 internal shards.

Invariant today: one barrier encrypts all WAL shards + snapshots; purge is global.

## 2. The conflict

If shard A (tenantA/BarrierA) and shard B (tenantB/BarrierB) share the waldb,
`globalSafePurgeFloor()` returns ONE floor, but each barrier's DEK purge must use
`min(epochs of ITS shard only)`. Using a cross-shard minimum either over-retains
(no crypto-shred) or under-retains (purges live epochs → unrecoverable). The code
assumes one-barrier-per-host, so per-tenant barriers violate per-barrier purge
independence.

## 3. Proposed rework — per-barrier epoch tracking

### go-quicraft (additive, backward compatible)
- `host.go`: new `WithShardBarriers(fn func(shardID uint64) Barrier) HostOption`
  (+ `hostOptions.shardBarriers`); when set, wire to waldb via a barrier-shard map.
  `WithBarrier` (host-wide) remains the default.
- `snapshot_pool.go`: replace single `barrier` with `shardBarriers func(shardID) SnapshotBarrier`;
  add `perBarrierSafePurgeFloor(barrier)` = `min(snapshot epochs encrypted by THIS barrier,
  WAL min-epoch for THIS barrier)`; `purgeOldEpochs(shardID,...)` dispatches to the shard's
  barrier and purges only that barrier.
- `waldb/db.go`: new `PerBarrierEpochAwareLogDB { MinLiveEpochForBarrier(barrier) (uint64,error) }`;
  `DB.barrierShardMap func(shardID) interface{}`; `MinLiveEpochForBarrier` returns min write
  epoch across only the shards mapped to that barrier.
- transport snapshot paths (`host.go:219-287, 316-342`): route barrier by shardID.

### go-qrdb
- NEW `pkg/database/multiplex_logdb.go`: `multiplexLogDB{ sharedWAL, dedicatedWAL map[shardID]LogDB,
  routeToShared func(shardID) bool }`. `SaveState` splits updates by route and writes shared +
  per-shard dedicated (NOT atomic across WALs — matches current per-shard non-atomicity, Invariant 4).
  Same dispatch for snapshot save/get.
- `RaftBackendFactory.startShard`: quota-gate (`MaxDedicatedWALGroups`); if opted-in + under quota +
  tenant barrier present → `getOrCreateDedicatedWAL(shardID, tenantBarrier)` under the tenant DataRoot,
  start with the EXISTING `quicraft.WithLogDB(wal)` seam; else fall back to the shared system-key WAL
  (Route A). Increment/decrement the dedicated-WAL counter.
- quota: `MaxDedicatedWALGroups` + `CurrentDedicatedWALGroups` in the entitlement/quota types.

## 4. Correctness invariants
1. **Per-barrier purge floor**: `purgeFloor_B = min(snapshot epochs by B, WAL epochs by B)`.
2. **Frame-level encryption** (unchanged): each snapshot frame embeds its epoch; decrypt by
   frame epoch, not the snapshot's recorded epoch.
3. **Per-shard isolation**: purging BarrierA never affects BarrierB.
4. **Cross-WAL atomicity**: SaveState is NOT atomic across WALs (per-shard durability only);
   raft re-proposes on recovery.

## 5. Risks + mitigations
- snapshot/WAL epoch mismatch → per-barrier floor; unit test concurrent rotation+purge.
- frame epoch error across rotation → frame-embedded epoch (unchanged); recovery test.
- cross-WAL SaveState partial failure → raft re-proposes; document non-atomicity.
- crypto-shred on tenant delete → `PurgeEpochsBefore(maxuint64)` before deleting the tenant barrier.
- Test plan: unit (per-barrier floor + dispatch), multi-tenant integration (dedicated WALs +
  crash recovery + crypto-shred), mid-snapshot crash recovery with per-tenant barriers.

## 6. Sequencing (~12–17 days)
1. API seams (WithShardBarriers, PerBarrierEpochAwareLogDB) — 2d (go-quicraft)
2. snapshot-pool per-barrier epoch tracking — 3d (go-quicraft)
3. waldb MinLiveEpochForBarrier + dispatch — 2d (go-quicraft)
4. host/transport wiring — 2d (go-quicraft)
5. qrdb multiplexLogDB routing — 2d (go-qrdb)
6. RaftBackendFactory + quota + integration — 3d (go-qrdb)

Gated behind Phase 1 (multi-node proven) + Phase 2 (cluster-wide pool). Needs its own
security pass before shipping.
