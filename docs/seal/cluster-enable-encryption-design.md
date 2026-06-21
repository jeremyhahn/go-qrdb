<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Cluster-wide Enable-Encryption — Design

> **IMPLEMENTED — Phase C + Phase D** (2026-06-20).
> The design has been **shipped and validated** via integration tests
> `TestClusterEnableEncryption_SystemScope_RF3` and
> `TestClusterEnableEncryption_TenantScope_RF3`.
> **Embedded node parity** (2026-06-21): the embedded SDK node now supports the same
> cluster enable-encryption paths through `pkg/server.BuildTenantInfra()` convergence.
>
> - **Current implementation:** see [`per-node-barrier-architecture.md`](per-node-barrier-architecture.md)
>   (§ 8, Phase C/D) and [`enabling-encryption.md`](enabling-encryption.md)
>   (§ Cluster-wide enable-encryption).
> - **This document** is retained for its **investigation findings** (§1–§9 below),
>   which prove the cross-node replication/snapshot boundary is logical and
>   per-node keys are safe in a cluster. These findings underpin the implementation.

**Status:** IMPLEMENTED + VALIDATED (Phases C & D)
**Scope:** Multi-node coordination for migrating a previously-unencrypted barrier
to a keyed strategy across a cluster, building on the single-node migration that
is already shipped and E2E-proven (`docs/seal/enabling-encryption.md`).
**Chosen coordination model:** replicated-state-driven, fail-safe on partial
failure (a node down/unreachable mid-migration never blocks the cluster and
resumes on recovery).

---

## 1. Summary

There are two scopes, and they are **fundamentally different** in the cluster:

| | System barrier | Tenant dedicated barrier (IsolationRaft, RF>1) |
|---|---|---|
| Root key | **Per-node, independent** (each `{dataDir}/seal`) | **Cluster-shared** (leader mints once, HPKE-delivered to all replicas) |
| Key delivery on join | none (each node mints its own) | `clusterkeys.DistributeTenantKey` (Phase 4c-1c) |
| At-rest ciphertext | per-node | per-node files, but derived from the **shared** DEK |
| Cluster migration | **N independent local migrations** (no shared key, no cross-node data dependency) | **True consensus operation** (shared key + replicated data + snapshot streaming between replicas) |

**Consequence:** the only genuinely-new, consensus-sensitive work is the
**tenant** cluster migration. A "cluster-wide system migration" is just running
the already-proven single-node system recycle (`cmd/qrdbd/system_migrate.go`) on
each node independently — there is no shared key to commit and no cross-node data
dependency (confirmed: `rebalance_service.go` — "system shards share the
cluster-wide barrier key and need no key delivery"; each node mints its own
system root key in `initSealSubsystem`). This document therefore focuses on the
tenant case and treats system-scope cluster as an operational rollout (§8).

---

## 2. Grounding facts (from the codebase)

1. **Tenant root key is cluster-shared.** The leader mints it once
   (`StorageBarrier.Initialize` → root key + initial DEK), exports it as
   `seal.ImportedKey{RootKey, Salt, Epoch, KnownEpochs}`
   (`pkg/seal/import_key.go`), HPKE-seals it to each recipient node's mTLS cert
   (`keydelivery.Seal`, RFC 9180 DHKEM(P-256)), and ships it via
   `clusterkeys.Sender.DistributeTenantKey` over `/v1/cluster/keydelivery`. The
   receiver re-seals it under its **own** system barrier
   (`StorageBarrier.ImportRootKey`) so the sealed blob is node-local but the
   **derived DEK is identical across replicas** — DEK = HKDF(rootKey, salt,
   epoch). This identity is the whole point of shipping `ImportedKey` rather than
   letting each node regenerate.

2. **At-rest is per-node.** `atrest.Encryptor` wraps each node's Pebble VFS with
   `encfs.New(base, barrier)`; each replica encrypts its own files with the DEK
   of the barrier it holds. Raft replicates **logical entries**, not files.

3. **`KnownEpochs` already exists.** `ImportedKey.KnownEpochs` and the
   `EpochBarrier`'s epoch tracking mean a barrier can be told about **multiple
   epochs** and decrypt data written under any of them, while writing under the
   current epoch. `StrategyNone` writes epoch 0 (zero-epoch plaintext); keyed
   strategies write epoch ≥ 1. **This is the linchpin of the safety argument
   (§4).**

4. **Coordination template.** `RebalanceService.ExecuteRebalance` is the
   leader-driven fan-out to model on: enumerate owners from the replicated
   `placement.Store` (`__sys:placement/{tenant}/{db}` → `Placement.Replicas[]`),
   deliver the tenant key (`clusterkeys`), write the new state to the replicated
   placement table, and let each node's `TenantShardReconciler` converge on the
   change-feed. Replicated system state lives in the Raft-applied system KV
   (`pkg/placement`, `pkg/tenant` `__sys:` records).

5. **Completion tracking needs a NEW replicated record.** The existing
   `MigrationJournal` (`pkg/seal/migrate.go`) is **node-local** (at
   `rootKeyPath+".migration"`) — perfect for per-node crash recovery, but has no
   cluster visibility. "Done when all owners confirm" requires a replicated
   per-node status record (§5).

6. **Per-node recycle mechanics already exist and are E2E-proven:**
   `TenantHostRegistry.MigrateEncryption` (per-replica recycle of SM Pebble +
   Raft WAL) and the `seal` primitives (`MintKeyedSibling`, `ReopenKeyed`,
   `AdoptMigratedRootKey`, journal). The single-replica path is done; the cluster
   work wraps + extends it (the follower path must use the **delivered** key
   rather than minting its own — §6).

---

## 3. The consensus-safety crux

During a rolling migration the cluster is in a **mixed state**: some replicas of
a shard have re-encrypted to the keyed DEK (epoch ≥ 1), others still hold
none-format files (epoch 0). Two replication mechanisms cross replica boundaries
and must keep working in that mixed state:

- **Log replication.** Raft ships **logical entries**; each replica applies them
  and writes its own files through its own barrier. Mixed state is inherently
  safe here — entries carry no at-rest ciphertext.

- **Snapshot streaming (THE risk).** When a follower lags beyond the log, the
  leader sends a **snapshot** of the state machine. The danger: if snapshots are
  streamed as **raw encfs ciphertext**, a receiver whose barrier does not know
  the sender's epoch cannot read them → corruption / apply failure / data loss.

### 3.1 The safety mechanism: pre-distribute the keyed epoch as a KNOWN epoch

Before *any* re-encryption, the leader delivers the new keyed root key to **all**
replica nodes, and each node registers it as a **known epoch** on its live
tenant barrier (in addition to the existing none/epoch-0). After this step, every
replica can **decrypt** both epoch-0 (none) and the keyed epoch, even though it
still **writes** epoch 0 until its own re-encryption runs. Therefore a snapshot
produced by an already-migrated replica (keyed) is always decryptable by any
replica (all know the keyed epoch), and vice-versa. The mixed state is safe by
construction.

This reuses `ImportedKey.KnownEpochs` + the `EpochBarrier`'s multi-epoch
decryption, and mirrors the single-node restart-survival pattern already shipped
(register the persisted/migrated strategy alongside the configured one).

### 3.2 Prerequisite to VERIFY in go-quicraft before implementation

This design assumes one of the following is true of go-quicraft snapshot
streaming; **this must be confirmed (and a regression test added) before writing
the cluster code**:

- **(A) Logical/boundary re-encryption:** the snapshot is decrypted at the source
  through its barrier and re-encrypted at the destination through the receiver's
  barrier. If so, mixed state is trivially safe regardless of epochs.
- **(B) Raw ciphertext streaming + multi-epoch decode:** the snapshot is streamed
  as encfs bytes and the receiver decodes via its barrier's known epochs. Safe
  **iff** §3.1 (keyed epoch pre-distributed everywhere) holds before any replica
  re-encrypts.

If neither holds (receiver can only decode its single active epoch and snapshots
are raw), the rolling design is unsafe and we must fall back to a
**quiesce-the-whole-group** migration (all replicas of a shard migrate within one
maintenance window, no mixed state) — a documented degradation, not the default.

**Action item:** read go-quicraft `pkg/` snapshot send/recv + the `WALBarrier`/
encfs path; add a `TestSnapshotAcrossEpochs` proving a keyed replica can restore a
none replica's snapshot and vice-versa, *before* any qrdb cluster code lands.

---

## 4. Coordination model — replicated-state, fail-safe partial

Leader proposes intent into replicated state; each owning node acts on the
change-feed and writes back its own per-node phase; the leader (or any observer)
computes cluster completion by comparing the owner set against the acked set.
This survives leader change and node restart, and a laggard never blocks the
group.

### 4.1 Replicated coordination record

A new replicated record under a dedicated `__sys:` prefix, written through the
same system KV the placement/tenant records use (Raft-applied, consistent
cluster view):

```
__sys:encmig/{tenantID}/{database}            -> ClusterMigration (the request)
__sys:encmig/{tenantID}/{database}/{nodeID}   -> NodeMigrationStatus (per-node ack)
```

```go
// ClusterMigration is the replicated intent record (written once by the leader).
type ClusterMigration struct {
    TenantID   string
    Database   string
    ShardID    uint64
    Target     seal.StrategyID          // passphrase | shamir
    Mechanism  seal.MigrationMechanism  // swap | in-place
    KeyEpoch   uint64                   // the keyed epoch all owners must converge to
    Owners     []string                 // NodeIDs from Placement.Replicas at request time
    RequestedAt int64                   // stamped by the caller (no Date.now in SM)
    Generation uint64                   // monotonic; guards against stale/duplicate requests
}

// NodeMigrationStatus is one owning node's replicated progress.
type NodeMigrationStatus struct {
    NodeID     string
    Generation uint64
    Phase      seal.MigrationPhase      // Pending|Copying|Verifying|Committing|Complete|Failed
    Detail     string                   // last error / progress note
    UpdatedAt  int64
}
```

The leader computes completion: `complete == every Owners[i] has a
NodeMigrationStatus with matching Generation and Phase==Complete`. The record is
GC'd (or marked terminal) once complete; a `Failed` node is retried on its next
reconcile tick / reboot.

### 4.2 Execution flow

```
LEADER (on `qrdb barrier enable-encryption --tenant T` for a clustered tenant):
  1. Load Placement(T, db) -> Owners, ShardID, RF.
  2. Mint the keyed root key once over the tenant barrier (MintKeyedSibling),
     producing ImportedKey{RootKey,Salt,KeyEpoch,KnownEpochs={0,KeyEpoch}}.
  3. Key pre-distribution: DistributeTenantKey-style HPKE delivery of the keyed
     ImportedKey to EVERY owner (including self). Each recipient registers the
     keyed epoch as KNOWN on its live tenant barrier (decrypt-capable, still
     writes epoch 0). [Safety precondition §3.1]
     - Block until all owners ack key-known (or record Failed for unreachable).
  4. Write ClusterMigration{Generation=g, KeyEpoch, Owners} to replicated state.

EACH OWNING NODE (on change-feed for its tenant, or on boot if a record exists):
  5. If my NodeMigrationStatus.Generation==g && Phase==Complete -> done.
  6. Acquire the per-tenant migrating guard (already built), re-encrypt MY
     replica's SM Pebble + Raft WAL to the keyed epoch using the DELIVERED key
     (not MintKeyedSibling) -> swap -> AdoptMigratedRootKey -> ReopenKeyed.
  7. Write NodeMigrationStatus{Generation=g, Phase} at each phase transition.
  8. On crash mid-way: the LOCAL MigrationJournal (already built) roll-forward/
     back recovers the replica deterministically on reboot; then re-publish phase.

ROLLING: nodes self-pace; AvailabilityQuiesced migrates one replica at a time
while the Raft group stays available. Optionally the leader gates "node k may
start when node k-1 is Complete" by ordering Owners and having each node wait its
turn (read the replicated statuses) — keeps at most one replica recycling at once.

COMPLETION: when all Owners reach Complete, optionally drop epoch 0 from
KnownEpochs cluster-wide (a second, GC-only pass) so none-format data is no
longer decryptable — the true "fully encrypted" end state.
```

### 4.3 Leader-change & restart recovery

- The intent lives in **replicated state**, not in the leader's memory. A new
  leader re-derives outstanding work by reading `ClusterMigration` +
  `NodeMigrationStatus` for the current `Generation`.
- A node that was **down** during steps 3–7 finds the `ClusterMigration` record on
  reboot (or next reconcile tick), checks it hasn't acked the current Generation,
  and resumes: it still needs the delivered key — so the leader's key
  pre-distribution (step 3) is **idempotent and retried** for any owner missing a
  key-known ack (model on rebalance's `deliverKeyToDst`).
- `Generation` guards against a stale record from an aborted/older request racing
  a newer one.

### 4.4 Partial-failure semantics (fail-safe)

- A keyed replica and a not-yet-keyed replica **coexist and serve correctly**:
  each decrypts its own files; cross-replica snapshot/log works because every
  owner knows the keyed epoch (§3.1). The cluster never blocks on a laggard.
- "Migration complete" is a cluster-level predicate (all owners acked), surfaced
  in `qrdb barrier status --tenant T` as `N/RF nodes migrated`. The data is safe
  at every intermediate point; only the *guarantee* "all at-rest data is keyed"
  is pending until the last owner acks (and epoch 0 is GC'd).
- No global rollback: because each replica's local migration is independently
  crash-safe (journal) and the mixed state is safe, there is no "abort the
  cluster" path — a failed node simply retries. An operator can re-issue the
  request (new Generation) to force a sweep.

---

## 5. New components (interfaces only — no impl in this doc)

- `pkg/seal` (or `pkg/service`): `ClusterMigration` + `NodeMigrationStatus` types
  + a thin `ClusterMigrationStore` over the replicated system KV
  (`Put/Get/ListStatuses/PutStatus/Watch`), modeled on `placement.Store`.
- `pkg/service`: a `ClusterEncryptionMigrator` that the leader's
  enable-encryption handler routes a clustered tenant to: mint → pre-distribute
  key → write intent. Reuses `clusterkeys.Sender`.
- Per-node: extend `TenantHostRegistry.MigrateEncryption` with a variant that
  takes a **provided** keyed `ImportedKey` (skip `MintKeyedSibling`; import the
  delivered key as the keyed sibling) + a reconciler hook that watches
  `__sys:encmig/*` and drives the local recycle, writing `NodeMigrationStatus`.
- `cmd/qrdbd`: wire the reconciler (model on `TenantShardReconciler`) + the key
  pre-distribution receiver path (reuse `clusterkeys.Receiver` with a new Mode or
  a known-epoch import call).
- Transports/CLI/UI: `qrdb barrier status --tenant T` shows per-node progress;
  the existing enable-encryption endpoint detects RF>1 and routes to the cluster
  migrator.

---

## 6. Single-replica reuse vs. change

The shipped `TenantHostRegistry.MigrateEncryption` **mints its own** keyed key
(`MintKeyedSibling`). For the cluster, the key must be **shared**: the leader
mints once and every node (including the leader) re-encrypts against the
**delivered** key. So the per-node path is refactored to accept an injected keyed
barrier/`ImportedKey` and skip the local mint when one is provided; the
single-node (non-clustered, RF=1) path keeps minting locally. Everything else
(quiesce guard, recode SM+WAL, verify, swap, adopt, reopen, journal) is reused
verbatim.

---

## 7. System-scope cluster (operational, not consensus)

Because system keys are per-node-independent, "encrypt the system barrier across
the cluster" = run the proven single-node system recycle on each node. Options,
in increasing automation:

1. **Operator-run per node** (documented runbook): `qrdb barrier
   enable-encryption` on each node, one at a time (rolling; the system Raft group
   stays available because only one node recycles at once). Zero new code; fully
   safe. **Recommended default.**
2. **Leader-orchestrated rollout** (optional later): a thin replicated record
   that triggers each node's independent system recycle in sequence and tracks
   completion — pure convenience over option 1, with leader-change handling. Only
   if operators ask for one-command system rollout.

No shared key, no key delivery, no cross-node data dependency in either case.

---

## 8. Test plan

- **go-quicraft (prerequisite):** `TestSnapshotAcrossEpochs` — a keyed replica
  restores a none replica's snapshot and vice-versa (validates §3.2).
- **pkg/seal/pkg/service:** unit tests for `ClusterMigrationStore` + the
  completion predicate (owners vs acked, Generation guard).
- **pkg/server:** per-node reconciler drives the local recycle against a delivered
  key; mixed-state read correctness; restart-resume from the replicated record.
- **test/integration (the real proof):** a 3-node IsolationRaft RF=3 tenant with
  data on all replicas → `enable-encryption --tenant` → assert (a) rolling (group
  stays available throughout), (b) every replica ends keyed, (c) **zero data
  loss** on every replica, (d) a node killed mid-migration resumes and completes
  on reboot, (e) cross-replica snapshot during mixed state succeeds.

---

## 9. Open questions / risks (for review)

1. **go-quicraft snapshot epoch semantics (§3.2)** — the gating prerequisite.
   Must be verified + regression-tested before any qrdb cluster code.
2. **Epoch-0 GC final pass** — when (and whether) to drop the none epoch from
   `KnownEpochs` cluster-wide. Until dropped, none-format data remains
   decryptable; this is the difference between "all new writes keyed" and "all
   at-rest data keyed + plaintext format no longer readable." Proposed: a second
   replicated generation that each node runs after all owners are keyed.
3. **Key-material lifetime during pre-distribution** — the delivered `ImportedKey`
   carries the root key; it transits HPKE-sealed (good) and is held in a memguard
   enclave on receipt (reuse existing `ImportRootKey` path). Confirm no plaintext
   root key is persisted unsealed at any step.
4. **Interaction with concurrent rebalance** — a tenant being rebalanced (replica
   move) while migrating. Proposal: the migration `Generation` + placement
   `Watch` let a moved-in replica pick up the in-flight migration; but the
   simplest safe rule is to **reject enable-encryption while a rebalance is in
   flight for the same tenant** (and vice-versa) via a replicated lock.
5. **System-scope automation** — confirm option 1 (operator-run) is the accepted
   default, or whether option 2 (orchestrated) is wanted now.

---

## 10. Decision log

- **2026-06-20:** Coordination model = replicated-state-driven, fail-safe partial
  (user-selected). System-scope cluster = per-node operational rollout (keys are
  per-node-independent; no consensus needed). Tenant-scope cluster = the consensus
  work. Implementation gated on (a) this design's approval and (b) the go-quicraft
  snapshot-epoch verification (§3.2).

- **2026-06-20:** IMPLEMENTATION COMPLETE + VALIDATED. Phase C (go-quicraft
  logical snapshots) verified and shipped; Phase D (cluster enable-encryption)
  implemented in `pkg/clustermig` and validated by integration tests (system
  scope RF=3 rolling + zero data loss, tenant scope RF=3 online + zero data loss).
  Per-node credential delivery via HPKE envelopes in the replicated intent record
  (no secret in Raft log). All findings (§1–§9) confirmed; no safety concerns.
