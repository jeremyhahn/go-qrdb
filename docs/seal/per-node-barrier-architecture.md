<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Per-Node Barrier Architecture

**Status:** IMPLEMENTED — Phases A, C, D shipped + validated (2026-06-20). Phase B deferred pending xkms integration; Phase E in progress.
**Supersedes:** the cluster-specific draft in
`cluster-enable-encryption-design.md` (now a pointer to this doc).

This document defines the unified at-rest encryption model for go-qrdb across
standalone, single-node-daemon, and multi-node-cluster deployments, and how
go-xkms / go-quicbaas extend it with hardware-backed crypto. It is the canonical
reference for: the envelope model + hot-path performance invariant, the
`credential → per-node DEK` abstraction, the tenant barrier model + quota gate,
the two at-rest layers and how both are encrypted per-node, and cluster
enable-encryption.

> **Deployment parity:** The daemon (`cmd/qrdbd`) and the embedded SDK node
> (`sdk/go/node`) both drive encryption-at-rest using the same per-node barrier
> architecture and `scopedEncryptionMigrator` composition. The embedded node gains
> daemon-parity enable-encryption support through `pkg/server.BuildTenantInfra()`
> convergence (see [embedding.md](../embedding.md)). There is no duplication of
> migration logic between daemon and embedded paths.

---

## 1. Goals & principles

1. **Per-node keys are the foundation.** Every barrier — system and tenant —
   mints/holds its own key material locally. No at-rest encryption key is shared
   across nodes and none stays resident as a special "cluster key."
2. **xkms-extensible.** Hardware/cloud strategies (TPM2, PKCS#11, FIDO2,
   cloud-KMS) are injected by go-xkms through the existing `StrategyRegistry`
   seam (`WithStrategyRegistry`/`WithSealStrategy`/`WithFactorRegistry`). Adding
   hardware is an xkms-side registration; qrdb needs no change. **Per-node keys
   are *required* for this**: hardware-bound keys are non-exportable, so they can
   never form a shared cluster key.
3. **Performance is independent of strategy.** The chosen strategy
   (none/passphrase/shamir/hardware) affects only the one-time unseal, never the
   per-operation hot path (§2). High-traffic + hardware coexist.
4. **Tenant-controlled, admin-governed.** A tenant chooses its own barrier mode
   (if isolated); the system admin governs what modes a tenant may use and
   whether it may consume the shared system barrier, via entitlements + quotas.
5. **Complete at-rest coverage.** Both at-rest layers — the state-machine Pebble
   *and* the raft WAL + snapshots — are encrypted per-node (§7), closing the
   current clustered gap.

---

## 2. The envelope model & the hot-path invariant

Two keys with completely different jobs:

- **Root key (KEK)** — protected by the strategy (`none`/`passphrase`/`shamir`/
  hardware). Used **once, at unseal**, to unwrap the DEK. Never touched again
  while the barrier is open.
- **Data encryption key (DEK)** — a symmetric AES key that encrypts/decrypts
  **every** block, file, WAL record, and snapshot byte via AES-GCM. While
  unsealed it lives in a **go-enclave locked-memory enclave** (formerly
  memguard), and the derived `cipher.AEAD`
  (AES key schedule) is **cached** so the hot path is pure AES-NI.

```
unseal (ONCE):      root key (strategy: sw/hw/quorum) --unwraps--> DEK
hot path (EVERY op): cached AES-GCM(DEK)   // resident software key, AES-NI, GB/s
```

> **INVARIANT (binding on all strategies, including xkms hardware): the DEK is
> always a resident software AES key; a strategy only *wraps/unwraps* it and must
> never perform per-operation encryption.** A hardware strategy unwraps the DEK
> once at unseal (a few ms on the TPM/HSM) and is never on the write path. This
> is what makes hardware crypto and high throughput coexist; xkms strategies MUST
> follow the wrap-not-encrypt envelope pattern (no per-op HSM/KMS calls).

**Consequence:** all key-scope options (per-node vs shared) and all strategies
have **identical hot-path throughput** — the per-write cost is AES-GCM on a
resident DEK regardless. There is therefore **no performance argument for sharing
a DEK across nodes.**

---

## 3. The `credential → per-node DEK` abstraction

Every strategy reduces to producing an **unlock credential**; the per-node
barrier then derives/seals its own key from that credential plus a **per-node
salt**:

| Strategy | Unlock credential | Cluster behavior |
|---|---|---|
| `none` | empty (zero-epoch) | per-node, trivial |
| `passphrase` | operator-supplied secret | per-node: same passphrase, **different** per-node keys |
| `shamir` | quorum-reconstructed secret `U` (§5) | per-node: **one quorum**, different per-node keys |
| xkms hardware | device-released secret (per node) | per-node by nature (non-exportable) |

`DEK_node = HKDF(unwrap(credential, sealed_root_key_node), salt_node, epoch)`.
The credential is transient (present only during unseal); the persistent at-rest
keys are per-node and distinct. `passphrase` and `shamir` thus become the *same*
mechanism differing only in how the credential is obtained — and xkms hardware
plugs into the identical seam.

---

## 4. Why per-node works in a cluster (the logical boundary)

Verified against go-quicraft + go-qrdb (see `cluster-enable-encryption-design.md`
§ findings): **Raft replication and snapshots cross the wire as logical
plaintext**, not at-rest ciphertext.

- **Log replication:** raft ships logical command entries; each follower appends
  to its own WAL and applies through its own SM, re-encrypting with its own DEK.
- **Snapshots:** the SM `SaveSnapshot` writes logical key/value pairs read
  *through* the (decrypting) barrier; the receiver re-encrypts on
  `RecoverFromSnapshot` through *its own* barrier.
- **Proof:** the system shard already runs this way today — each node has an
  independent per-node DEK and cross-node snapshots work — confirming per-node
  keys replicate correctly.

So per-node keys are not a compromise; they are the model the system shard
already validates in production.

---

## 5. Shamir in a cluster — Option 3 (one quorum, per-node keys)

**Problem:** Shamir splits *one* secret into N shares (M-of-N quorum,
separation-of-duties). Per-node keys give *many* keys, so "split the key" is
ambiguous in a cluster.

**Design (Option 3):** Shamir splits a single generated **unlock secret `U`**
(one share set, one ceremony, distributed to officers). At init, each node
derives its **own** per-node root key from `U` + its per-node salt, seals it
locally, and wipes `U`. Persisted: the officers' shares + each node's per-node
sealed key.

**Unseal:** officers submit M shares → `U` is reconstructed → each owning node
unlocks **its own** per-node key with `U`, derives its own DEK, serves; `U` is
wiped. `U` reaches each node sealed/transient during the ceremony and is never
persisted as a key.

**Why it wins:** one quorum / one share set (the expected Shamir UX) **and**
per-node data keys (a stolen node disk is useless without the quorum; persistent
encryption keys are never shared). The only shared element, `U`, is transient
during the ceremony — exactly like a passphrase. Rejected alternatives: Option 1
(per-node independent quorums = N ceremonies, painful) and Option 2 (shared root
key = broader blast radius, software-only, reintroduces resident shared key).

Shamir is a *software* separation-of-duties scheme and is an alternative to
hardware (a tenant picks one). Both compose through the §3 abstraction.

---

## 6. Tenant barrier model

- **Shared tenant** → rides the **system barrier** (`StrategySystemBarrier`); no
  dedicated key.
- **Isolated tenant** → **dedicated barrier**; mode ∈ {`none`, `passphrase`,
  `shamir`, xkms-hardware}, restricted to the tenant's **entitled** set. Each
  replica applies the mode per-node (§3).
- **Isolated tenant opting back into the system barrier** → governed by a
  per-tenant **`SystemBarrierQuota`** (bytes) on the tenant's entitlements,
  accounted against the **system tenant's** storage. `0` ⇒ the "use system
  barrier" option is not offered to that tenant. This extends the existing
  `pkg/quota` entitlement model (alongside `AllowSystemManagedBarrier`,
  `AllowVolumeStorage`, `MaxDedicatedWALGroups`, …) and lets the admin govern
  system-tenant disk consumption exactly as required.

Enforcement: tenant create / barrier-init validates the requested mode against
`AllowedBarrierModes` (entitlement) and, for the system-barrier option, against
`SystemBarrierQuota > 0` plus current usage. Quota accounting hooks the existing
storage accountant.

---

## 7. At-rest completeness — two layers, both per-node

There are two at-rest layers; today only the first is encrypted in clusters.

- **Layer A — encfs on the state-machine Pebble** (`pkg/atrest` + `pkg/seal/
  encfs`). Per-node already; the main data layer. ✔
- **Layer B — the go-quicraft host barrier on the raft WAL + raft snapshot
  files.** Fixed in Phase C (go-quicraft logical snapshots): raft-snapshot
  streaming is now decrypted at the source through the sender's barrier and
  re-encrypted at the destination through the receiver's barrier (mirroring the
  SM layer), so each node encrypts its WAL and snapshot files with its own
  per-node DEK. Clustered hosts wire Layer B via `WithShardBarriers`. ✔

**Fix (in scope — #2):** make go-quicraft raft-snapshot streaming **logical**
(decrypt-at-source through the sender's barrier, re-encrypt-at-dest through the
receiver's barrier — mirroring the SM layer), so each node can encrypt its raft
WAL **and** snapshot files with its **own** per-node DEK while the wire stays
logical. Then wire Layer B on clustered hosts (`WithShardBarriers` / the tenant
barrier) and fix the stale comment. Per the §2 invariant this is resident-DEK
AES-NI on writes (fast); the snapshot re-encryption is at transfer time only
(infrequent). Result: **complete per-node at-rest coverage, no shared key.**

---

## 8. Cluster enable-encryption (simplified by per-node)

With per-node keys there is no shared key to mint/deliver/commit, so cluster
enable-encryption collapses to **per-node independent migrations + a replicated
completion record**:

- Leader enumerates owners from the replicated `placement.Store`
  (`Placement.Replicas[]`).
- Writes a replicated `__sys:encmig/{tenant}` intent record (Generation-guarded).
- Each owning node, on the change-feed (or on boot), runs the **proven
  single-node recycle** on its own replica (reuse `TenantHostRegistry.
  MigrateEncryption` / `recodeSystemBarrier`) and writes back its
  `NodeMigrationStatus`.
- Complete when all owners ack. **Fail-safe partial:** a down/unreachable node
  never blocks the group (per-node logical replication keeps mixed state safe —
  §4) and resumes from the replicated record on recovery; leader change is
  survived because intent lives in replicated state.

Per-node credential delivery uses in-record HPKE-sealed envelopes: the
coordinator seals the unlock credential (passphrase / Shamir secret) individually
to each owner's public key via `keydelivery.Seal`; the per-node ciphertext blobs
are stored in the replicated migration record and each node opens only its own
entry with its private key. The plaintext credential never appears in the Raft
log. For Shamir M-of-N clusters, one quorum is distributed to officers; each
node unseals its own per-node root key with the reconstructed secret during the
ceremony, then seals it locally and wipes the shared unlock credential `U`.

System scope across a cluster = the same fan-out of each node's independent local
system recycle (keys are per-node), rolling.

---

## 9. xkms / quicbaas extensibility (unchanged seam, strengthened)

- Hardware/cloud strategies register as `SealStrategy` via `WithStrategyRegistry`;
  factors via `WithFactorRegistry`. A tenant barrier mode is "any registered
  strategy the tenant is entitled to."
- Per-node keys make hardware natural: each node's device unseals its own root
  key; nothing is exported. xkms strategies MUST honor the §2 envelope invariant
  (wrap/unwrap only).
- quicbaas (L4) composes tenants + login; it consumes this model unchanged — a
  tenant's chosen mode flows through to qrdb's per-tenant barrier.

---

## 10. Security analysis

- **Blast radius:** a stolen node disk yields only that node's ciphertext, keyed
  by that node's DEK, sealed by that node's strategy — useless without that
  node's unlock path. No single key compromises the cluster.
- **Key lifetime:** root keys/credentials are transient at unseal; DEKs live in
  a go-enclave locked-memory enclave (formerly memguard) while unsealed
  (mlock'd, guard-paged) with only the cached AEAD on the hot path. `U`
  (shamir) and passphrases are wiped after unseal.
- **Hardware non-export preserved:** per-node keys never leave their node; the
  §2 invariant prevents per-op HSM exposure.
- **Separation of duties preserved:** shamir Option 3 keeps M-of-N quorum unseal.

---

## 11. Phased implementation plan

Each phase is independently shippable, gated by tests; later phases do not
destabilize earlier ones.

- **Phase A — Tenant governance (lowest risk).** IMPLEMENTED ✔
  `SystemBarrierQuota` + `AllowedBarrierModes` entitlements in `pkg/quota`;
  enforcement at tenant create / barrier-init; SDK/CLI/UI surfacing; quota
  accounting against the system tenant. No consensus, no crypto change.
  **Validated:** unit tests in `pkg/quota`, quota accounting integration tests.

- **Phase B — `credential → per-node DEK` abstraction.** DEFERRED. Refactor the
  seal strategy model so every strategy yields an unlock credential and the
  barrier derives a per-node key (per-node salt). Implement **Shamir Option 3**
  on top. Keep single-node behavior identical; add per-node-salt derivation.
  Touches `pkg/seal` (qrdb) + go-quicraft `pkg/seal` epoch/derivation. Pending
  xkms integration strategy verification.

- **Phase C — Layer B logical snapshots (go-quicraft).** IMPLEMENTED ✔
  Raft-snapshot streaming is now logical (decrypt-at-source, re-encrypt-at-dest
  through per-node barriers). Layer B wired on clustered hosts via
  `WithShardBarriers`. **Validated:** `TestSnapshotAcrossEpochs` in go-quicraft;
  cross-node snapshot re-encryption verified; clustered WAL + snapshot files now
  per-node-encrypted at rest.

- **Phase D — Cluster enable-encryption.** IMPLEMENTED + VALIDATED ✔
  Replicated `__sys:encmig` record in `pkg/clustermig` (Store, Coordinator,
  Reconciler) + per-node reconciler fan-out (reuse single-node recycle) +
  completion tracking + status surfacing. Per-node credential delivery via
  HPKE-sealed envelopes in the replicated record. **Validated:**
  `TestClusterEnableEncryption_SystemScope_RF3` (3-node, rolling, zero data loss,
  leader-change survival, resume-via-idempotency) and
  `TestClusterEnableEncryption_TenantScope_RF3` (3-node IsolationRaft, online,
  zero data loss, system barrier untouched).

- **Phase E — xkms integration verification.** Prove an xkms hardware strategy
  slots into the per-node barrier through the registry end-to-end (a test/mock
  hardware strategy in qrdb; real validation in go-xkms).

---

## 12. Open questions / risks

1. **go-quicraft snapshot rewrite (Phase C)** is the consensus-risk piece; needs
   the cross-epoch snapshot regression + cluster integration before/after.
2. **Existing-deployment migration:** clusters running today with the shared
   tenant key + Layer-B-off. Path: per-node is backward-compatible for Layer A
   (already per-node-capable); enabling Layer B is additive; a shared-key tenant
   can be migrated to per-node via the Phase D recycle. Document the upgrade.
3. **`SystemBarrierQuota` accounting granularity** (bytes vs object count) and
   how it reconciles with the existing storage accountant — settle in Phase A.
4. **Shamir `U` delivery during the ceremony** (Phase B/D): reconstruct-central-
   then-seal-to-each-node vs replicate-share-submissions; must never persist
   shares or `U` in the raft log. Settle in Phase B.

---

## 13. Test plan (per phase)

- **A:** quota/entitlement unit tests (allowed modes, quota=0 disallows system
  barrier, accounting); transport/CLI/UI tests.
- **B:** seal unit tests (per-node-salt derivation; passphrase same-secret-
  different-keys; Shamir Option 3 one-quorum-many-keys round-trip); restart
  survival.
- **C (go-quicraft):** `TestSnapshotAcrossEpochs`; cluster integration: keyed
  raft files at rest + cross-node snapshot install + zero data loss.
- **D:** integration RF=3 enable-encryption (rolling, zero loss, mid-migration
  node kill + resume, leader-change survival).
- **E:** registry end-to-end with a mock hardware strategy.
